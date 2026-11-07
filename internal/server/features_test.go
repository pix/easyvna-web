package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

type rig struct {
	ts  *httptest.Server
	s   *Server
	dir string
	mu  sync.Mutex
	sim *mock.Sim
}

// newRig starts a server on the simulator with mock antenna control.
func newRig(t *testing.T, antenna string, pix, readOnly bool) *rig {
	t.Helper()
	g := &rig{dir: t.TempDir()}
	st, err := NewStore(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	cur := antenna
	open := func(ctx context.Context) (vna.Device, error) {
		sim := mock.New(mock.Options{PointTime: time.Microsecond, Seed: 11, Profiles: pix, Antenna: mock.Scenarios[cur]()})
		g.mu.Lock()
		g.sim = sim
		g.mu.Unlock()
		return nanovnah.Open(ctx, sim, nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond, ReadOnly: readOnly})
	}
	set := func(name string) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		cur = name
		if g.sim != nil {
			g.sim.SetAntenna(mock.Scenarios[name]())
		}
		return nil
	}
	cutTotal := 0.0
	cut := func(mm float64) {
		g.mu.Lock()
		defer g.mu.Unlock()
		cutTotal += mm
		a := mock.Scenarios[cur]()
		a.Modes[0].F0 = a.Modes[0].F0 * (82 + 6) / (82 - cutTotal + 6)
		if g.sim != nil {
			g.sim.SetAntenna(a)
		}
	}
	g.s = New(Options{Open: open, Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("ui")}},
		Logger: log.New(io.Discard, "", 0), ReadOnly: readOnly, SetMockAntenna: set, MockCut: cut, MockAntenna: antenna})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.s.Run(ctx); close(done) }()
	g.ts = httptest.NewServer(g.s.Handler())
	t.Cleanup(func() { g.ts.Close(); cancel(); <-done })
	return g
}

type msgState struct {
	Message string `json:"message"`
	State   State  `json:"state"`
}

func TestSurveyProfileAndWarnings(t *testing.T) {
	g := newRig(t, "dual", true, false)
	var pr msgState
	if c := call(t, g.ts, "POST", "/api/profile", `{"id":"survey"}`, &pr); c != 200 {
		t.Fatalf("survey: %d", c)
	}
	// Current PiX firmware has no `survey` profile: EasyVNA Web must not send it.
	if pr.State.Config.Start != 50e6 || pr.State.Config.Stop != 1500e6 || strings.Contains(pr.Message, "device profile") {
		t.Errorf("survey: %q %+v", pr.Message, pr.State.Config)
	}
	var res Result
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if len(res.Summary.Dips) < 2 || len(res.Summary.Warnings) != 0 {
		t.Errorf("survey dips %d warnings %+v", len(res.Summary.Dips), res.Summary.Warnings)
	}
	for _, c := range []struct{ ant, code string }{{"dummy", "lossy"}, {"open", "open"}} {
		call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"`+c.ant+`"}`, nil)
		call(t, g.ts, "POST", "/api/sweep", "", &res)
		if len(res.Summary.Warnings) != 1 || res.Summary.Warnings[0].Code != c.code {
			t.Errorf("%s: warnings %+v", c.ant, res.Summary.Warnings)
		}
	}
}

func TestAveraging(t *testing.T) {
	g := newRig(t, "869", false, false)
	if c := call(t, g.ts, "POST", "/api/avg", `{"n":3}`, nil); c != 400 {
		t.Errorf("avg 3 accepted: %d", c)
	}
	call(t, g.ts, "POST", "/api/avg", `{"n":4}`, nil)
	var res Result
	for i := 1; i <= 5; i++ {
		call(t, g.ts, "POST", "/api/sweep", "", &res)
		want := i
		if want > 4 {
			want = 4
		}
		if res.AvgN != want {
			t.Fatalf("sweep %d: avgN %d", i, res.AvgN)
		}
	}
	call(t, g.ts, "POST", "/api/profile", `{"id":"433"}`, nil) // new grid restarts the average
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if res.AvgN != 1 || res.Config.Avg != 4 {
		t.Errorf("after profile change: avgN %d avg %d", res.AvgN, res.Config.Avg)
	}
}

func TestRefSetReplaceUndoClear(t *testing.T) {
	g := newRig(t, "869", false, false)
	if c := call(t, g.ts, "POST", "/api/ref", "", nil); c != 409 {
		t.Errorf("REF before sweep: %d", c)
	}
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var r1 Result
	call(t, g.ts, "POST", "/api/sweep", "", &r1)
	var m msgState
	call(t, g.ts, "POST", "/api/ref", "", &m)
	if _, ok := m.State.Refs["869"]; !ok || m.Message != "REF stored" {
		t.Fatalf("set: %+v", m)
	}
	// Detune (other antenna) and sweep: Δf shows up.
	call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"869tuned"}`, nil)
	var r2 Result
	call(t, g.ts, "POST", "/api/sweep", "", &r2)
	if r2.Ref == nil || r2.Ref.DF == nil || *r2.Ref.DF < 5e6 {
		t.Fatalf("ref delta: %+v", r2.Ref)
	}
	call(t, g.ts, "POST", "/api/ref", "", &m)
	if !strings.Contains(m.Message, "replaced") {
		t.Errorf("replace: %q", m.Message)
	}
	call(t, g.ts, "POST", "/api/ref/undo", "", &m)
	var ref Result
	call(t, g.ts, "GET", "/api/ref?profile=869", "", &ref)
	if ref.Seq != r1.Seq {
		t.Errorf("undo restored seq %d, want %d", ref.Seq, r1.Seq)
	}
	m = msgState{}
	call(t, g.ts, "DELETE", "/api/ref", "", &m)
	if _, ok := m.State.Refs["869"]; ok {
		t.Error("clear failed")
	}
	m = msgState{}
	call(t, g.ts, "POST", "/api/ref/undo", "", &m)
	if _, ok := m.State.Refs["869"]; !ok {
		t.Error("undo of clear failed")
	}
	// Persisted: a new server instance sees it.
	if refs := g.s.store.LoadRefs(); refs["869"] == nil {
		t.Error("REF not on disk")
	}
}

func TestTuneAndTrimSession(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	var st State
	call(t, g.ts, "POST", "/api/tune", `{"on":true}`, &st)
	if !st.Tune.Active || st.Tune.Target != 869e6 || st.Config.Points != tunePoints || !st.Continuous {
		t.Fatalf("tune on: %+v cfg %+v", st.Tune, st.Config)
	}
	if st.Config.Start < 779e6 || st.Config.Stop > 959e6 || st.Config.Stop-st.Config.Start > 120e6 {
		t.Errorf("tune window %v..%v", st.Config.Start, st.Config.Stop)
	}
	if c := call(t, g.ts, "POST", "/api/tune", `{"on":true,"target":433920000}`, nil); c != 400 {
		t.Errorf("target outside profile accepted: %d", c)
	}
	// Trim: first log needs a length.
	if c := call(t, g.ts, "POST", "/api/trim/whip/log", `{"cutMm":1}`, nil); c != 400 {
		t.Errorf("cut without length: %d", c)
	}
	var ts TrimSession
	if c := call(t, g.ts, "POST", "/api/trim/whip/log", `{"lengthMm":82}`, &ts); c != 200 || len(ts.Entries) != 1 || ts.Prediction == nil {
		t.Fatalf("first log: %d %+v", c, ts)
	}
	first := ts.Entries[0]
	if first.FreqHz > 869e6 || ts.Prediction.CutMm <= 0 || ts.Target != 869e6 {
		t.Fatalf("too-long whip must say cut: %+v %+v", first, ts.Prediction)
	}
	// Two cuts: the simulated element follows, resonance rises, the fit learns.
	for i := 0; i < 2; i++ {
		cut := 0.8 * ts.Prediction.CutMm
		body := fmt.Sprintf(`{"cutMm":%.2f}`, cut)
		if c := call(t, g.ts, "POST", "/api/trim/whip/log", body, &ts); c != 200 {
			t.Fatalf("cut log %d: %d", i, c)
		}
	}
	if len(ts.Entries) != 3 || ts.Entries[2].FreqHz <= first.FreqHz || ts.Prediction.Method != "fit" {
		t.Fatalf("after cuts: %+v %+v", ts.Entries, ts.Prediction)
	}
	if ts.Entries[2].LengthMm >= 82 || ts.Entries[1].CutMm <= 0 {
		t.Errorf("lengths: %+v", ts.Entries)
	}
	call(t, g.ts, "POST", "/api/trim/whip/undo", "", &ts)
	if len(ts.Entries) != 2 {
		t.Errorf("undo: %d entries", len(ts.Entries))
	}
	var list []TrimInfo
	call(t, g.ts, "GET", "/api/trim", "", &list)
	if len(list) != 1 || list[0].Entries != 2 {
		t.Errorf("list: %+v", list)
	}
	call(t, g.ts, "GET", "/api/state", "", &st)
	if st.TrimActive != "whip" {
		t.Errorf("active trim %q", st.TrimActive)
	}
	// Leaving TUNE restores the profile sweep.
	call(t, g.ts, "POST", "/api/tune", `{"on":false}`, &st)
	if st.Tune.Active || st.Config.Start != 779e6 || st.Config.Points != 401 || st.Continuous {
		t.Errorf("tune off: %+v %+v", st.Tune, st.Config)
	}
}

func TestCompareWizard(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var st State
	call(t, g.ts, "POST", "/api/compare", `{"on":true}`, &st)
	if !st.Compare.Active || !st.Config.S21 {
		t.Fatalf("compare on: %+v", st.Compare)
	}
	var res Result
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if res.Compare == nil || res.Compare.Ready || res.Compare.BandDB > -10 {
		t.Fatalf("step 1 live: %+v", res.Compare)
	}
	if c := call(t, g.ts, "POST", "/api/compare/add", `{"name":"B"}`, nil); c != 409 {
		t.Errorf("add before A: %d", c)
	}
	var m msgState
	call(t, g.ts, "POST", "/api/compare/a", `{"name":"stock"}`, &m)
	if !m.State.Compare.HasA || m.State.Compare.AName != "stock" {
		t.Fatalf("store A: %+v", m.State.Compare)
	}
	for _, c := range []struct {
		ant  string
		sign float64
	}{{"869tuned", 1}, {"dummy", -1}} {
		call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"`+c.ant+`"}`, nil)
		call(t, g.ts, "POST", "/api/sweep", "", &res)
		if !res.Compare.Ready || res.Compare.MeanDB*c.sign < 1 || len(res.Compare.Delta) != len(res.Freq) {
			t.Fatalf("%s: %+v", c.ant, res.Compare)
		}
		call(t, g.ts, "POST", "/api/compare/add", `{"name":"`+c.ant+`"}`, &m)
	}
	lb := m.State.Compare.Leaderboard
	if len(lb) != 3 || lb[0].Name != "869tuned" || lb[2].Name != "dummy" || lb[2].MeanDB > -20 {
		t.Fatalf("leaderboard: %+v", lb)
	}
	// Replacing A clears the board; undo brings it back.
	call(t, g.ts, "POST", "/api/compare/a", `{"name":"dummy as A"}`, &m)
	if len(m.State.Compare.Leaderboard) != 1 || !strings.Contains(m.Message, "Undo") {
		t.Errorf("replace A: %+v %q", m.State.Compare, m.Message)
	}
	call(t, g.ts, "POST", "/api/compare/undo", "", &m)
	if len(m.State.Compare.Leaderboard) != 3 {
		t.Errorf("undo: %+v", m.State.Compare.Leaderboard)
	}
	// Profile mismatch is explained, not silently wrong.
	call(t, g.ts, "POST", "/api/profile", `{"id":"915"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if res.Compare == nil || res.Compare.Ready || !strings.Contains(res.Compare.Problem, "869") {
		t.Errorf("mismatch: %+v", res.Compare)
	}
	call(t, g.ts, "POST", "/api/compare", `{"on":false}`, &st)
	if st.Compare.Active || st.Config.S21 {
		t.Errorf("compare off: %+v", st.Compare)
	}
	if c := call(t, g.ts, "DELETE", "/api/compare", "", &m); c != 200 || m.State.Compare.HasA {
		t.Errorf("reset: %d %+v", c, m.State.Compare)
	}
}

func TestReport(t *testing.T) {
	g := newRig(t, "869", false, false)
	if c := call(t, g.ts, "POST", "/api/report", `{"name":"x"}`, nil); c != 409 {
		t.Errorf("empty report: %d", c)
	}
	for _, p := range []string{"869", "433"} {
		call(t, g.ts, "POST", "/api/profile", `{"id":"`+p+`"}`, nil)
		call(t, g.ts, "POST", "/api/sweep", "", nil)
	}
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	call(t, g.ts, "POST", "/api/tune", `{"on":true}`, nil)
	call(t, g.ts, "POST", "/api/trim/whip <1>/log", `{"lengthMm":82}`, nil)
	call(t, g.ts, "POST", "/api/tune", `{"on":false}`, nil)
	var st State
	call(t, g.ts, "GET", "/api/state", "", &st)
	if len(st.ReportChoices) != 2 {
		t.Fatalf("choices: %+v", st.ReportChoices)
	}
	var out map[string]string
	if c := call(t, g.ts, "POST", "/api/report", `{"name":"whip <1>","notes":"pigtail & case","trim":"whip <1>"}`, &out); c != 200 {
		t.Fatalf("report: %d %v", c, out)
	}
	resp, err := http.Get(g.ts.URL + "/" + out["url"])
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("get report: %v %v", err, resp.Status)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(b)
	for _, want := range []string{"whip &lt;1&gt;", "pigtail &amp; case", "869 MHz", "433", "<svg", "Trim log", "Mismatch loss", "Verdict per band", "@page"} {
		if !strings.Contains(html, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(html, "<1>") {
		t.Error("report does not escape the name")
	}
	files, _ := filepath.Glob(filepath.Join(g.dir, "reports", "*.html"))
	if len(files) != 1 {
		t.Errorf("report files: %v", files)
	}
	if c := call(t, g.ts, "GET", "/api/reports/..%2Fsettings.json", "", nil); c != 400 && c != 404 {
		t.Errorf("path traversal: %d", c)
	}
	var list []ReportInfo
	call(t, g.ts, "GET", "/api/reports", "", &list)
	if len(list) != 1 {
		t.Errorf("list: %+v", list)
	}
}

func TestReadOnlyMode(t *testing.T) {
	g := newRig(t, "869", true, true)
	var st State
	call(t, g.ts, "POST", "/api/connect", "", &st)
	if !st.ReadOnly {
		t.Fatal("state not read-only")
	}
	for _, p := range []string{"/api/cal/step/open", "/api/cal/save", "/api/cal/recall"} {
		if c := call(t, g.ts, "POST", p, `{}`, nil); c != 403 {
			t.Errorf("%s: %d", p, c)
		}
	}
	var pr msgState
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, &pr)
	if pr.Message != "" || pr.State.Config.Start != 779e6 {
		t.Errorf("read-only profile switch must not touch the device: %q", pr.Message)
	}
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	g.mu.Lock()
	logLines := append([]string(nil), g.sim.Log...)
	g.mu.Unlock()
	for _, l := range logLines {
		for _, bad := range []string{"cal ", "save", "recall", "profile 869"} {
			if strings.HasPrefix(l, bad) {
				t.Errorf("read-only server sent %q", l)
			}
		}
	}
}

func TestHoldStablePositions(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var m msgState
	call(t, g.ts, "POST", "/api/hold", `{"on":true}`, &m)
	if !m.State.Hold.On || m.State.Hold.N != 3 {
		t.Fatalf("hold on: %+v", m.State.Hold)
	}
	sweep := func() *HoldLive {
		var r Result
		call(t, g.ts, "POST", "/api/sweep", "", &r)
		if r.Hold == nil {
			t.Fatal("no hold info in result")
		}
		return r.Hold
	}
	var h *HoldLive
	for i := 0; i < 3; i++ {
		h = sweep()
	}
	if h.Status != "captured" || h.Captured != 1 || h.Positions != 1 {
		t.Fatalf("first position: %+v", h.HoldUpdate)
	}
	// "Bend" the antenna: the first sweep after the change is motion.
	call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"869tuned"}`, nil)
	if h = sweep(); h.Status != "moving" {
		t.Fatalf("not rejected as moving: %+v", h.HoldUpdate)
	}
	h = sweep()
	h = sweep()
	if h.Status != "captured" || h.Positions != 2 || h.ResSpread < 5e6 || len(h.EnvMax) != 401 || h.WorstBand < 1 {
		t.Fatalf("second position: %+v / spread %.1f MHz", h.HoldUpdate, h.ResSpread/1e6)
	}
	var full struct {
		Version   int `json:"version"`
		Positions []struct {
			ID  int       `json:"id"`
			SWR []float64 `json:"swr"`
		} `json:"positions"`
	}
	call(t, g.ts, "GET", "/api/hold", "", &full)
	if len(full.Positions) != 2 || len(full.Positions[1].SWR) != 401 || full.Version != h.Version {
		t.Fatalf("full positions: %d", len(full.Positions))
	}
	// Remove, undo, clear, undo.
	call(t, g.ts, "POST", "/api/hold", `{"remove":1}`, &m)
	if m.State.Hold.Positions != 1 || !m.State.Hold.HasUndo {
		t.Errorf("remove: %+v", m.State.Hold)
	}
	call(t, g.ts, "POST", "/api/hold", `{"undo":true}`, &m)
	if m.State.Hold.Positions != 2 {
		t.Errorf("undo remove: %+v", m.State.Hold)
	}
	call(t, g.ts, "POST", "/api/hold", `{"clear":true}`, &m)
	call(t, g.ts, "POST", "/api/hold", `{"undo":true}`, &m)
	if m.State.Hold.Positions != 2 {
		t.Errorf("undo clear: %+v", m.State.Hold)
	}
	if c := call(t, g.ts, "POST", "/api/hold", `{"n":1}`, nil); c != 400 {
		t.Errorf("n=1 accepted: %d", c)
	}
	call(t, g.ts, "POST", "/api/hold", `{"sensitivity":"high"}`, &m)
	if m.State.Hold.Sensitivity != "high" {
		t.Errorf("sensitivity: %+v", m.State.Hold)
	}
	call(t, g.ts, "POST", "/api/hold", `{"on":false}`, &m)
	var r Result
	call(t, g.ts, "POST", "/api/sweep", "", &r)
	if m.State.Hold.On || r.Hold != nil {
		t.Errorf("hold off still active")
	}
}
