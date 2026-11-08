package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

// testBatch is an 869 batch with the profile's grid.
func testBatch() *Batch {
	p := DefaultProfiles()[3]
	b := &Batch{ID: "t1", Name: "869 whip lot A", Profile: p.ID, Label: "869 MHz profile", Start: p.Start, Stop: p.Stop,
		Points: p.Points, Bands: p.Bands, Target: p.Bands[0].Reference(), NextID: 1, Antennas: []BatchAntenna{}}
	b.Freq = make([]float64, b.Points)
	for i := range b.Freq {
		b.Freq[i] = b.Start + (b.Stop-b.Start)*float64(i)/float64(b.Points-1)
	}
	return b
}

// whip is the S11 of a simulated whip resonating at f0 (R at resonance, Q).
func whip(b *Batch, f0, r, q float64) [][2]float64 {
	a := mock.Antenna{Modes: []mock.Mode{{F0: f0, R: r, Q: q}, {F0: 2.6e9, R: 90, Q: 10}}, CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08}
	out := make([][2]float64, len(b.Freq))
	for i, f := range b.Freq {
		s11, _ := a.Measure(f, 0)
		out[i] = [2]float64{real(s11), imag(s11)}
	}
	return out
}

func addAnt(b *Batch, f0, r, q float64) {
	b.Antennas = append(b.Antennas, BatchAntenna{ID: b.NextID, Label: "#" + strconv.Itoa(b.NextID), Time: time.Unix(1700000000+int64(b.NextID)*60, 0),
		Cal: "calibrated", CalOK: true, S11: whip(b, f0, r, q)})
	b.NextID++
}

func TestAntennaTuning(t *testing.T) {
	b := testBatch()
	tol := b.AimHz()
	if math.Abs(tol-1.3035e6) > 1e3 || math.Abs(tight.Resolve(b.Target, nil).Hz-tol) > 1 {
		t.Fatalf("tolerance %.0f", tol)
	}
	cases := []struct {
		f0, r  float64
		status string
	}{
		{868.8e6, 47, "tuned"}, {864e6, 47, "shorten"}, {874e6, 47, "lengthen"}, {835e6, 47, "bad"}, {862e6, 20, "bad"},
	}
	for _, c := range cases {
		m, _ := antennaMetrics(b, whip(b, c.f0, c.r, 13), trimSlope{}, tight)
		if !m.Found || m.Tuning.Status != c.status {
			t.Fatalf("f0 %.1f R %.0f: %+v", c.f0/1e6, c.r, m.Tuning)
		}
		if math.Abs(m.ResFreq-c.f0) > 2e6 && c.r > 20 {
			t.Fatalf("resonance %.2f for %.2f", m.ResFreq/1e6, c.f0/1e6)
		}
		if c.status == "shorten" && (m.Tuning.Method != "pct" || !strings.Contains(m.Tuning.Text, "shorten ≈") || m.Tuning.ScalePct >= 0) {
			t.Fatalf("no length: %+v", m.Tuning)
		}
		if c.status == "lengthen" && !strings.HasPrefix(m.Tuning.Text, "↑ lengthen") {
			t.Fatal(m.Tuning.Text)
		}
		if c.f0 == 862e6 && m.Tuning.Text != "✗ poor match" {
			t.Fatal(m.Tuning.Text)
		}
		if c.f0 == 835e6 && (m.Tuning.Text != "✗ out of band" || m.Verdict != "bad") {
			t.Fatal(m.Tuning.Text, m.Verdict)
		}
		if c.status == "tuned" && (m.Verdict == "bad" || m.Tuning.Text != "✓ tuned" || m.PctBelow2 < 99) {
			t.Fatalf("tuned: %+v", m)
		}
	}
	// With the element length: the trim calculator's first estimate and
	// its cautious first cut (TUNE's rule).
	b.LengthMm = 164
	m, _ := antennaMetrics(b, whip(b, 860e6, 47, 13), trimSlope{}, tight)
	tu := m.Tuning
	want := 164 - 164*m.ResFreq/b.Target
	if tu.Method != "ratio" || !tu.Cautious || math.Abs(tu.CutMm-want) > 0.01 || tu.FirstCutMm != rf.FirstCut(want) || !strings.Contains(tu.Text, "shorten") ||
		!strings.Contains(tu.Detail, "First cut") {
		t.Fatalf("ratio: %+v (want cut %.2f)", tu, want)
	}
	// With a learnt MHz/mm: the full cut, not cautious.
	m, _ = antennaMetrics(b, whip(b, 860e6, 47, 13), trimSlope{MHzPerMm: 9, From: "whip"}, tight)
	if m.Tuning.Method != "fit" || m.Tuning.Cautious || math.Abs(m.Tuning.CutMm-(b.Target-m.ResFreq)/9e6) > 1e-9 {
		t.Fatalf("fit: %+v", m.Tuning)
	}
	// No resonance (an open cable).
	open := make([][2]float64, len(b.Freq))
	for i := range open {
		open[i] = [2]float64{0.99, 0}
	}
	m, _ = antennaMetrics(b, open, trimSlope{}, tight)
	if m.Found || m.Tuning.Status != "none" {
		t.Fatalf("open: %+v", m)
	}
}

// tight is the ± 0.15 % tolerance (the behaviour before the acceptance
// setting), auto the default.
var tight, auto = rf.Acceptance{Tol: rf.TolTight}, rf.Acceptance{}

func TestAntennaTuningAuto(t *testing.T) {
	b := testBatch()
	b.LengthMm = 82
	// 6 MHz low, a whip with a wide dip: the band is covered, OK with margin.
	m, _ := antennaMetrics(b, whip(b, 863e6, 47, 13), trimSlope{}, auto)
	if m.Tuning.Status != "ok" || !strings.HasPrefix(m.Tuning.Text, "✓ OK") || !m.Margin.OK || !m.Margin.Covered ||
		m.Margin.UsedPct <= 0 || m.Margin.UsedPct > 60 || !strings.Contains(m.Tuning.Detail, "Optional") {
		t.Fatalf("ok: %+v", m)
	}
	// On the fine aim: tuned.
	m, _ = antennaMetrics(b, whip(b, 868.6e6, 47, 13), trimSlope{}, auto)
	if m.Tuning.Status != "tuned" {
		t.Fatalf("tuned: %+v", m.Tuning)
	}
	// Far off: the band is not covered.
	m, _ = antennaMetrics(b, whip(b, 835e6, 47, 13), trimSlope{}, auto)
	if m.Tuning.Status != "bad" || m.Tuning.Text != "✗ out of band" || m.Margin.Covered {
		t.Fatalf("bad: %+v", m.Tuning)
	}
	// A poor match: centring would not help.
	m, _ = antennaMetrics(b, whip(b, 866.5e6, 15, 13), trimSlope{}, auto)
	if m.Tuning.Status != "bad" || m.Tuning.Text != "✗ poor match" {
		t.Fatalf("poor: %+v %+v", m.Tuning, m.Margin)
	}
	// A stricter SWR limit turns the same antenna from OK into bad.
	strict := rf.Acceptance{SWRLimit: 1.5}
	found := false
	for f := 868e6; f > 820e6 && !found; f -= 1e6 {
		m1, _ := antennaMetrics(b, whip(b, f, 47, 13), trimSlope{}, auto)
		if m1.Verdict != "ok" || m1.WorstSWR < 1.55 {
			continue
		}
		found = true
		m2, _ := antennaMetrics(b, whip(b, f, 47, 13), trimSlope{}, strict)
		if m2.Verdict != "bad" || m2.Tuning.Status != "bad" || m1.Tuning.Status == "bad" {
			t.Fatalf("limit at %.0f MHz: %s/%s %s", f/1e6, m1.Verdict, m2.Verdict, m2.Tuning.Status)
		}
	}
	if !found {
		t.Fatal("no antenna between SWR 1.55 and 2")
	}
}

func joined(sm BatchSummary) string { return strings.Join(sm.Conclusion, " | ") }

func TestBatchSummary(t *testing.T) {
	// Tight tolerance: a lot that all resonates ~4 MHz low, consistently.
	b := testBatch()
	b.LengthMm = 82
	for _, f := range []float64{864.6e6, 865.3e6, 864.9e6, 865.6e6, 865.0e6} {
		addAnt(b, f, 47, 13)
	}
	v := batchView(b, nil, tight)
	sm := v.Summary
	if sm.N != 5 || sm.Shorten != 5 || sm.Tuned != 0 || sm.BiasHz > -3e6 || sm.BiasHz < -5e6 || sm.SigmaHz > 0.6e6 || sm.BiasCutMm <= 0.2 {
		t.Fatalf("%+v", sm)
	}
	if sm.Grade != GradeWorth || !strings.HasPrefix(sm.Conclusion[0], "Worth correcting: all 5 antennas cover 863.00–870.00 MHz at SWR < 2.0") ||
		!strings.Contains(sm.Conclusion[0], "5 are outside the ± 1.30 MHz tolerance") || !strings.Contains(joined(sm), "spread: σ 0.") {
		t.Fatalf("conclusion: %q", sm.Conclusion)
	}
	if !strings.HasPrefix(sm.TuningLine, "0 of 5 tuned (± 1.30 MHz); 5 to shorten (") || !strings.Contains(sm.TuningLine, "mm)") {
		t.Fatal(sm.TuningLine)
	}
	if sm.Criteria != "Acceptance: band SWR < 2.0, resonance ± 0.15 % (± 1.30 MHz)" {
		t.Fatal(sm.Criteria)
	}
	res := sm.Stats[0]
	if res.Key != "res" || res.N != 5 || len(res.Outliers) != 0 || math.Abs(res.Mean-865.08) > 1.2 || res.SD <= 0 {
		t.Fatalf("res stats %+v", res)
	}
	// The same lot with Auto (the default): fine, no correction needed.
	sm = batchView(b, nil, auto).Summary
	if sm.Grade != GradeFine || sm.OK != 5 || sm.Shorten != 0 || !strings.HasPrefix(sm.Conclusion[0], "Fine: all 5 antennas cover") ||
		!strings.Contains(sm.Conclusion[0], "no correction needed") || strings.Contains(joined(sm), "Correct the pattern") {
		t.Fatalf("auto: %q %+v", sm.Conclusion, sm)
	}
	if sm.Window.Mode != rf.TolAuto || sm.Window.Hz < 8e6 || !strings.Contains(sm.Criteria, "resonance Auto ±") {
		t.Fatalf("auto window %+v", sm.Window)
	}
	// A mixed lot (tight): 7 tuned, 2 to shorten, 1 bad outlier.
	b = testBatch()
	for _, f := range []float64{867.6e6, 868.4e6, 868.0e6, 868.7e6, 867.5e6, 868.2e6, 867.9e6, 865.5e6, 866.0e6, 835e6} {
		addAnt(b, f, 47, 13)
	}
	sm = batchView(b, nil, tight).Summary
	if sm.Tuned != 7 || sm.Shorten != 2 || sm.Bad != 1 || sm.TuningLine != "7 of 10 tuned (± 1.30 MHz); 2 to shorten; 1 bad" || sm.Grade != GradeMust {
		t.Fatalf("mixed: %+v %q", sm, sm.TuningLine)
	}
	if got := sm.Stats[0].Outliers; len(got) != 1 || got[0] != 10 {
		t.Fatalf("outliers %v", got)
	}
	if !strings.Contains(joined(sm), "Statistical outlier") || !strings.Contains(joined(sm), "#10") || sm.Yield["bad"] != 1 ||
		!strings.HasPrefix(sm.Conclusion[0], "Must correct: 1 of 10 antennas do not cover") || !strings.Contains(sm.Conclusion[0], "individually") {
		t.Fatalf("%q %v", sm.Conclusion, sm.Yield)
	}
	// n = 0, 1, 2, 4 identical antennas: no outliers, no NaN anywhere.
	for _, n := range []int{0, 1, 2, 4} {
		b = testBatch()
		for i := 0; i < n; i++ {
			addAnt(b, 868.8e6, 47, 13)
		}
		v := batchView(b, nil, auto)
		if _, err := json.Marshal(v); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		for _, st := range v.Summary.Stats {
			if len(st.Outliers) != 0 || math.IsNaN(st.SD) || (n == 1 && st.SD != 0) {
				t.Fatalf("n=%d %s: %+v", n, st.Key, st)
			}
		}
		c := joined(v.Summary)
		if n == 1 && !strings.Contains(c, "One antenna so far") {
			t.Fatal(c)
		}
		if n == 4 && (v.Summary.Tuned != 4 || !strings.Contains(c, "Centred on the target") || !strings.Contains(c, "Indicative (n = 4)")) {
			t.Fatal(c)
		}
	}
	// Large variance around the right mean.
	b = testBatch()
	for _, f := range []float64{858e6, 880e6, 862e6, 876e6, 869e6} {
		addAnt(b, f, 47, 13)
	}
	sm = batchView(b, nil, auto).Summary
	if !strings.Contains(joined(sm), "Wide spread") {
		t.Fatal(joined(sm))
	}
}

// The lot judgement: n-aware, margin-based (the user's 2-antenna lot).
func TestBatchConclusionGrades(t *testing.T) {
	// n = 2, resonating ~6.3 MHz low with a wide margin: no variance
	// verdict, the mean with its 95 % confidence interval, "indicative".
	b := testBatch()
	b.LengthMm = 82
	addAnt(b, 859.5e6, 47, 13)
	addAnt(b, 865.8e6, 47, 13)
	sm := batchView(b, nil, auto).Summary
	c := joined(sm)
	if sm.Grade != GradeFine || !strings.Contains(c, "Indicative (n = 2)") || !strings.Contains(c, "95 % confidence interval") ||
		strings.Contains(c, "spread") || strings.Contains(c, "variance") || sm.BiasCIHz <= 0 {
		t.Fatalf("n=2: %s", c)
	}
	if want := rf.TQuantile95(1) * sm.SigmaHz / math.Sqrt2; math.Abs(sm.BiasCIHz-want) > 0.05*want {
		t.Fatalf("CI %.0f, want ≈ %.0f", sm.BiasCIHz, want)
	}
	// A lot 0.7 % low (≈ 6 MHz) with a wide SWR margin: fine, no correction.
	b = testBatch()
	b.LengthMm = 82
	for _, f := range []float64{862.4e6, 862.9e6, 862.6e6, 863.1e6, 862.7e6, 862.3e6} {
		addAnt(b, f, 47, 13)
	}
	sm = batchView(b, nil, auto).Summary
	if sm.Grade != GradeFine || !strings.Contains(sm.Headline, "no correction needed") || !strings.Contains(sm.Headline, "uses") ||
		strings.Contains(joined(sm), "Indicative") || sm.BiasUsedPct > 50 {
		t.Fatalf("fine: %s %+v", joined(sm), sm.BiasUsedPct)
	}
	// Near the margin: worth correcting.
	b = testBatch()
	for _, f := range []float64{882e6, 883e6, 882.5e6, 883.4e6, 882.2e6} {
		addAnt(b, f, 47, 13)
	}
	sm = batchView(b, nil, auto).Summary
	if sm.Grade != GradeWorth || !strings.HasPrefix(sm.Headline, "Worth correcting") || !strings.Contains(sm.Headline, "would centre the lot") {
		t.Fatalf("worth: %s (bias used %.0f %%, margin max %.0f %%)", joined(sm), sm.BiasUsedPct, sm.MarginMax)
	}
	// SWR failures: must correct.
	b = testBatch()
	for _, f := range []float64{895e6, 897e6, 896e6, 894e6, 896.5e6} {
		addAnt(b, f, 47, 13)
	}
	sm = batchView(b, nil, auto).Summary
	if sm.Grade != GradeMust || !strings.HasPrefix(sm.Headline, "Must correct: 5 of 5 antennas do not cover") || !strings.Contains(sm.Headline, "correct the pattern") {
		t.Fatalf("must: %s", joined(sm))
	}
	// Measure ×N: spread compared with the repeatability.
	b = testBatch()
	for _, f := range []float64{866e6, 868e6, 870e6, 867e6, 869.5e6} {
		addAnt(b, f, 47, 13)
	}
	for i := range b.Antennas {
		b.Antennas[i].Rep = &RepeatBrief{N: 5, Mode: rf.RepeatStill, ResSD: 0.1e6}
	}
	sm = batchView(b, nil, auto).Summary
	if sm.NRepeat != 5 || math.Abs(sm.RepeatHz-0.1e6) > 1 || !strings.Contains(joined(sm), "the spread is real") {
		t.Fatalf("repeat: %s", joined(sm))
	}
}

func TestBatchStore(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := testBatch()
	addAnt(b, 868.8e6, 47, 13)
	if err := st.SaveBatch(b); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadBatch("t1")
	if err != nil || got.Name != b.Name || len(got.Antennas) != 1 || len(got.Antennas[0].S11) != 401 {
		t.Fatalf("%v %+v", err, got)
	}
	l := st.ListBatches(func(string) rf.Acceptance { return rf.Acceptance{} })
	if len(l) != 1 || l[0].N != 1 || l[0].Tuned != 1 || l[0].Yield["good"] != 1 {
		t.Fatalf("%+v", l)
	}
	data, err := st.Export("t", time.Now())
	if err != nil || !strings.Contains(string(data), `"batches/t1.json"`) {
		t.Fatalf("backup: %v", err)
	}
	if _, err := st.LoadBatch("../x"); err == nil {
		t.Fatal("bad id accepted")
	}
	if err := st.DeleteBatch("t1"); err != nil || len(st.ListBatches(func(string) rf.Acceptance { return rf.Acceptance{} })) != 0 {
		t.Fatal(err)
	}
	// The backup brings it back.
	if _, err := st.Import(data); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadBatch("t1"); err != nil {
		t.Fatal(err)
	}
}

// newBenchServer is a server on the mock bench (antennas can be swapped).
func newBenchServer(t *testing.T, antenna string) (*httptest.Server, *Server, *mock.Bench) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bench, err := mock.NewBench(antenna, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context) (vna.Device, error) {
		return nanovnah.Open(ctx, bench.NewSim(), nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("ui")}}, Logger: log.New(io.Discard, "", 0),
		SetMockAntenna: bench.SetAntenna, MockAntenna: antenna, MockScenarios: mock.ScenarioNames()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); <-done })
	return ts, s, bench
}

func get(t *testing.T, ts *httptest.Server, path string) (string, http.Header) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	return string(b), resp.Header
}

func TestBatchAPI(t *testing.T) {
	ts, s, bench := newBenchServer(t, "lot869")
	var errOut map[string]string
	// Wide cannot hold a batch.
	call(t, ts, "POST", "/api/profile", `{"id":"wide"}`, nil)
	if c := call(t, ts, "POST", "/api/batches", `{"name":"x"}`, &errOut); c != 400 || !strings.Contains(errOut["error"], "band profile") {
		t.Fatalf("%d %v", c, errOut)
	}
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var v BatchView
	if c := call(t, ts, "POST", "/api/batches", `{"name":"869 whip lot A","notes":"bought 2026-11","lengthMm":82}`, &v); c != 200 {
		t.Fatal(c)
	}
	if v.Profile != "869" || v.Target != 869e6 || v.Points != 401 || s.state().Batch.Active != v.ID {
		t.Fatalf("%+v", v)
	}
	id := v.ID
	type capResp struct {
		Message string    `json:"message"`
		ID      int       `json:"id"`
		Batch   BatchView `json:"batch"`
	}
	var cr capResp
	for i := 1; i <= 5; i++ {
		if i > 1 {
			_ = bench.SetAntenna("lot869")
		}
		if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{}`, &cr); c != 200 || cr.ID != i {
			t.Fatalf("capture %d: %d %+v", i, c, cr.Message)
		}
	}
	if n := len(cr.Batch.Antennas); n != 5 || cr.Batch.Antennas[2].Label != "#3" || cr.Batch.Summary.N != 5 {
		t.Fatalf("%d antennas", n)
	}
	// Varied: unit 3 is the lot's outlier (≈2 % low).
	res := map[int]float64{}
	for _, a := range cr.Batch.Antennas {
		res[a.ID] = a.Metrics.ResFreq
		if a.Cal == "" || len(a.SWR) != 401 {
			t.Fatalf("%+v", a)
		}
	}
	if res[3] > 860e6 || cr.Batch.Antennas[2].Metrics.Tuning.Status != "bad" || res[1] == res[2] {
		t.Fatalf("lot: %v", res)
	}
	if o := cr.Batch.Summary.Stats[0].Outliers; len(o) != 1 || o[0] != 3 {
		t.Fatalf("outliers %v", o)
	}
	// Refused when the profile differs.
	call(t, ts, "POST", "/api/profile", `{"id":"433"}`, nil)
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{}`, &errOut); c != 409 || !strings.Contains(errOut["error"], "this batch uses the 869 MHz profile") {
		t.Fatalf("%d %v", c, errOut)
	}
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	// Rename, redo, delete + undo.
	if c := call(t, ts, "PUT", "/api/batches/"+id+"/antennas/2", `{"label":"SN 0042"}`, nil); c != 200 {
		t.Fatal(c)
	}
	_ = bench.SetAntenna("869tuned")
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{"replace":4}`, &cr); c != 200 || cr.ID != 4 {
		t.Fatal(c)
	}
	a4 := cr.Batch.Antennas[3]
	if a4.Redone != 1 || a4.Label != "#4" || math.Abs(a4.Metrics.ResFreq-868.5e6) > 2e6 || cr.Batch.Antennas[1].Label != "SN 0042" {
		t.Fatalf("redo: %+v", a4)
	}
	var dr struct {
		Batch BatchView `json:"batch"`
	}
	if c := call(t, ts, "DELETE", "/api/batches/"+id+"/antennas/5", "", &dr); c != 200 || len(dr.Batch.Antennas) != 4 || !dr.Batch.HasUndo {
		t.Fatal(c)
	}
	if c := call(t, ts, "POST", "/api/batches/"+id+"/undo", "", &dr); c != 200 || len(dr.Batch.Antennas) != 5 || dr.Batch.Antennas[4].ID != 5 {
		t.Fatal(c)
	}
	// Edit the batch.
	if c := call(t, ts, "PUT", "/api/batches/"+id, `{"name":"869 whip lot A (2026)"}`, &v); c != 200 || v.Name != "869 whip lot A (2026)" {
		t.Fatal(c)
	}
	// CSV: one row per antenna and the statistics.
	body, h := get(t, ts, "/api/batches/"+id+"/csv")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		// rows have different lengths; read leniently
		r := csv.NewReader(strings.NewReader(body))
		r.FieldsPerRecord = -1
		rows, err = r.ReadAll()
	}
	if err != nil || !strings.Contains(h.Get("Content-Disposition"), "869_whip_lot_A_2026_-batch.csv") {
		t.Fatalf("%v %s", err, h.Get("Content-Disposition"))
	}
	if rows[1][0] != "#" || rows[3][1] != "SN 0042" || rows[4][16] != "res" && !strings.Contains(rows[4][16], "res") {
		t.Fatalf("csv rows: %q %q %q", rows[1], rows[3], rows[4])
	}
	if !strings.Contains(body, "Resonance,MHz,5,") || !strings.Contains(body, "tuning,") || !strings.Contains(body, "conclusion,") {
		t.Fatal(body)
	}
	// JSON export, import as a new batch.
	js, h := get(t, ts, "/api/batches/"+id+"/json")
	var ex BatchExport
	if err := json.Unmarshal([]byte(js), &ex); err != nil || ex.Format != BatchFormat || len(ex.Batch.Antennas) != 5 || len(ex.Batch.Freq) != 401 ||
		ex.Figures == nil || len(ex.Figures.Antennas) != 5 || !strings.HasSuffix(h.Get("Content-Disposition"), `-batch.json"`) {
		t.Fatalf("export: %v", err)
	}
	var ir struct {
		Batch BatchView `json:"batch"`
	}
	if c := call(t, ts, "POST", "/api/batches/import", js, &ir); c != 200 || ir.Batch.ID == id || len(ir.Batch.Antennas) != 5 ||
		ir.Batch.Antennas[1].Label != "SN 0042" {
		t.Fatalf("import %d", c)
	}
	bad := strings.Replace(js, `"format": "easyvna-web-batch"`, `"format": "x"`, 1)
	if c := call(t, ts, "POST", "/api/batches/import", bad, &errOut); c != 400 {
		t.Fatal(c)
	}
	var list []BatchInfo
	call(t, ts, "GET", "/api/batches", "", &list)
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	// The sheet.
	var sh map[string]string
	call(t, ts, "POST", "/api/batch", `{"active":"`+id+`"}`, nil)
	if c := call(t, ts, "POST", "/api/batches/"+id+"/sheet", "", &sh); c != 200 {
		t.Fatal(c)
	}
	html, _ := get(t, ts, "/"+sh["url"])
	for _, want := range []string{"Batch sheet", "869 whip lot A (2026)", "SN 0042", "Lot statistics", "svg class=\"plot overlay lotswr\"", "svg class=\"plot spread\"", "cover the band at SWR &lt; 2", "on the ± 1.30 MHz TUNE aim", "◆"} {
		if want == "◆" {
			if !strings.Contains(html, "out\">") && !strings.Contains(html, " out\"") {
				t.Fatalf("no outlier cell")
			}
			continue
		}
		if !strings.Contains(html, want) {
			t.Fatalf("sheet lacks %q", want)
		}
	}
	// The REPORT can include the batch statistics.
	if c := call(t, ts, "POST", "/api/report", `{"name":"lot-report","profiles":[],"batch":"`+id+`"}`, &sh); c != 200 {
		t.Fatal(c)
	}
	html, _ = get(t, ts, "/"+sh["url"])
	if !strings.Contains(html, "Batch: 869 whip lot A (2026)") || !strings.Contains(html, "Lot") || !strings.Contains(html, "svg class=\"plot spread\"") ||
		!strings.Contains(html, "Resonance <span class=\"muted\">(MHz)</span>") {
		t.Fatal("report lacks the batch section")
	}
	// Delete and restore.
	if c := call(t, ts, "DELETE", "/api/batches/"+id, "", nil); c != 200 || s.state().Batch.Active != "" || s.state().Batch.Trash != id {
		t.Fatal(c)
	}
	if c := call(t, ts, "POST", "/api/batches/"+id+"/restore", "", &v); c != 200 || len(v.Antennas) != 5 || s.state().Batch.Active != id {
		t.Fatal(c)
	}
	// Persistence: a new server on the same store finds the active batch.
	s2 := New(Options{Open: func(context.Context) (vna.Device, error) { return nil, io.EOF }, Store: s.store, Static: fstest.MapFS{},
		Logger: log.New(io.Discard, "", 0)})
	if st := s2.state(); st.Batch.Active != id || st.Batch.N != 5 {
		t.Fatalf("reload: %+v", st.Batch)
	}
}

func TestBatchAutoCapture(t *testing.T) {
	ts, s, bench := newBenchServer(t, "lot869")
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var v BatchView
	call(t, ts, "POST", "/api/batches", `{"name":"auto"}`, &v)
	call(t, ts, "POST", "/api/batch", `{"auto":true}`, nil)
	if !s.state().Batch.Auto {
		t.Fatal("auto off")
	}
	sweep := func() *Result {
		var r Result
		if c := call(t, ts, "POST", "/api/sweep", "", &r); c != 200 {
			t.Fatal(c)
		}
		return &r
	}
	n := func() int { return s.state().Batch.N }
	// The first antenna is captured once steady.
	for i := 0; i < 6 && n() == 0; i++ {
		sweep()
	}
	if n() != 1 {
		t.Fatal("first antenna not captured")
	}
	// Steady on the same antenna: nothing more.
	for i := 0; i < 6; i++ {
		if r := sweep(); r.Batch == nil || r.Batch.Status == rf.ArrReady {
			t.Fatalf("%+v", r.Batch)
		}
	}
	if n() != 1 {
		t.Fatal("same antenna captured twice")
	}
	// Swap: unscrew (open cable), then the next unit.
	_ = bench.SetAntenna("open")
	if r := sweep(); r.Batch.Status != rf.ArrNoAntenna {
		t.Fatalf("open: %+v", r.Batch)
	}
	sweep()
	_ = bench.SetAntenna("lot869")
	captured := 0
	for i := 0; i < 6 && captured == 0; i++ {
		captured = sweep().Batch.Captured
	}
	if captured != 2 || n() != 2 {
		t.Fatalf("second antenna: %d", captured)
	}
	var got BatchView
	call(t, ts, "GET", "/api/batches/"+v.ID, "", &got)
	if !got.Antennas[1].Auto || got.Antennas[0].Metrics.ResFreq == got.Antennas[1].Metrics.ResFreq {
		t.Fatalf("%+v", got.Antennas)
	}
	// Auto-capture ignores sweeps of another profile.
	call(t, ts, "POST", "/api/profile", `{"id":"915"}`, nil)
	if r := sweep(); r.Batch == nil || r.Batch.Match || !strings.Contains(r.Batch.Problem, "869") {
		t.Fatalf("%+v", r.Batch)
	}
	call(t, ts, "POST", "/api/batch", `{"auto":false}`, nil)
}
