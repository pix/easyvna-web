package server

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

// newActionServer is a bench server with the simulator's actions (hand on
// the cable, re-seat) and a short re-seat.
func newActionServer(t *testing.T, antenna string) (*httptest.Server, *Server, *mock.Bench) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bench, err := mock.NewBench(antenna, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	mock.ReseatTime = 150 * time.Millisecond
	open := func(ctx context.Context) (vna.Device, error) {
		return nanovnah.Open(ctx, bench.NewSim(), nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("ui")}}, Logger: log.New(io.Discard, "", 0),
		SetMockAntenna: bench.SetAntenna, MockAntenna: antenna, MockScenarios: mock.ScenarioNames(), MockAction: bench.Action})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); <-done })
	return ts, s, bench
}

func TestAcceptanceSetting(t *testing.T) {
	ts, s, _ := newActionServer(t, "869") // resonates ~13 MHz low: OK at SWR 2
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var res Result
	call(t, ts, "POST", "/api/sweep", "", &res)
	b0 := res.Summary.Bands[0]
	if b0.Limit != 2 || b0.Good != 1.5 || res.Accept == nil || res.Accept.Window.Mode != rf.TolAuto {
		t.Fatalf("default: %+v %+v", b0, res.Accept)
	}
	if b0.Verdict != rf.VerdictAt(b0.WorstSWR, 2) {
		t.Fatal("verdict")
	}
	// A strict limit for the 869 profile only: the last result is re-graded.
	var st State
	if c := call(t, ts, "POST", "/api/acceptance", `{"scope":"profile","accept":{"swrLimit":1.2,"tol":"normal"}}`, &st); c != 200 {
		t.Fatalf("set: %d", c)
	}
	if !st.Acceptance.Own || st.Acceptance.Limit != 1.2 || st.Acceptance.Setting.Tol != "normal" {
		t.Fatalf("state %+v", st.Acceptance)
	}
	var last Result
	call(t, ts, "GET", "/api/last", "", &last)
	if bl := last.Summary.Bands[0]; bl.Limit != 1.2 || bl.Verdict != "bad" || last.Accept.Window.Mode != "normal" ||
		!strings.Contains(last.Accept.Window.Text, "band SWR < 1.2, resonance ± 0.5 % (± 4.35 MHz)") {
		t.Fatalf("regraded: %+v %+v", bl, last.Accept.Window)
	}
	// Other profiles keep the default.
	if a := s.settings.AcceptanceFor("433"); a.Limit() != 2 {
		t.Fatal("433 changed")
	}
	// Persisted (settings.json, so Export data carries it) and reloaded.
	data, err := s.store.Export("t", time.Now())
	if err != nil || !strings.Contains(string(data), "acceptProfile") {
		t.Fatalf("export: %v", err)
	}
	if got := s.store.LoadSettings().AcceptanceFor("869"); got.SWRLimit != 1.2 || got.Tol != "normal" {
		t.Fatalf("reload %+v", got)
	}
	st2, _ := NewStore(t.TempDir())
	if _, err := st2.Import(data); err != nil {
		t.Fatal(err)
	}
	if got := st2.LoadSettings().AcceptanceFor("869"); got.SWRLimit != 1.2 {
		t.Fatalf("import %+v", got)
	}
	// New sweeps use it; refusals.
	call(t, ts, "POST", "/api/sweep", "", &res)
	if res.Summary.Bands[0].Limit != 1.2 {
		t.Fatal("new sweep not graded with the setting")
	}
	for _, bad := range []string{`{"accept":{"swrLimit":1.01}}`, `{"accept":{"tol":"custom"}}`, `{}`} {
		if c := call(t, ts, "POST", "/api/acceptance", bad, nil); c != 400 {
			t.Fatalf("%s: %d", bad, c)
		}
	}
	// Default for every profile, then reset this profile's own setting.
	call(t, ts, "POST", "/api/acceptance", `{"scope":"default","accept":{"swrLimit":2.5}}`, &st)
	if st.Acceptance.Own || st.Acceptance.Limit != 2.5 || s.settings.AcceptanceFor("433").Limit() != 2.5 {
		t.Fatalf("default: %+v", st.Acceptance)
	}
	call(t, ts, "POST", "/api/acceptance", `{"scope":"profile","accept":{"tol":"tight"}}`, nil)
	call(t, ts, "POST", "/api/acceptance", `{"reset":true}`, &st)
	if st.Acceptance.Own || st.Acceptance.Limit != 2.5 {
		t.Fatalf("reset: %+v", st.Acceptance)
	}
}

func waitMeasure(t *testing.T, ts *httptest.Server, id int) *MeasureView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var st State
		call(t, ts, "GET", "/api/state", "", &st)
		if m := st.Measure; m != nil && m.ID == id && !m.Running {
			return m
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("measure did not finish")
	return nil
}

func TestMeasureN(t *testing.T) {
	ts, s, bench := newActionServer(t, "869tuned")
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, ts, "POST", "/api/avg", `{"n":4}`, nil)
	var st State
	if c := call(t, ts, "POST", "/api/measure", `{"n":5}`, &st); c != 200 || st.Measure == nil || !st.Measure.Running {
		t.Fatalf("start: %d %+v", c, st.Measure)
	}
	if c := call(t, ts, "POST", "/api/measure", `{"n":5}`, nil); c != 409 {
		t.Fatalf("second job: %d", c)
	}
	m := waitMeasure(t, ts, st.Measure.ID)
	if m.Status != "done" || m.Done != 5 || !strings.HasPrefix(m.Message, "average of 5 · resonance σ") {
		t.Fatalf("%+v", m)
	}
	var last Result
	call(t, ts, "GET", "/api/last", "", &last)
	if last.Multi == nil || last.Multi.N != 5 || last.AvgN != 5 || last.Multi.Mode != rf.RepeatStill || last.Multi.ResN != 5 ||
		last.Multi.ResSD <= 0 || last.Multi.ResSD > 0.5e6 || len(last.Multi.EnvMin) != len(last.Freq) {
		t.Fatalf("multi %+v", last.Multi)
	}
	// AVG is still ×4 for normal sweeps.
	call(t, ts, "GET", "/api/state", "", &st)
	if st.Config.Avg != 4 {
		t.Fatal("AVG changed")
	}
	// The sheet prints the repeatability, and HISTORY keeps it.
	var sh map[string]string
	if c := call(t, ts, "POST", "/api/sheet", `{"antenna":{"name":"whip"}}`, &sh); c != 200 {
		t.Fatalf("sheet %d", c)
	}
	html, _ := get(t, ts, "/"+sh["url"])
	if !strings.Contains(html, "average of 5") || !strings.Contains(html, "Acceptance: band SWR &lt; 2.0") {
		t.Fatal("sheet lacks the measurement / criteria line")
	}
	a, _ := s.store.LoadAntenna("whip")
	if len(a.History) != 1 || a.History[0].N != 5 || a.History[0].ResSD <= 0 {
		t.Fatalf("history %+v", a.History)
	}
	// Cancel.
	call(t, ts, "POST", "/api/measure", `{"n":20}`, &st)
	time.Sleep(200 * time.Millisecond)
	if c := call(t, ts, "POST", "/api/measure/cancel", "", nil); c != 200 {
		t.Fatalf("cancel %d", c)
	}
	if m := waitMeasure(t, ts, st.Measure.ID); m.Status != "cancelled" || m.Done >= 20 {
		t.Fatalf("cancelled: %+v", m)
	}
	// Re-seat: waits for the antenna to be disturbed, then steady again.
	call(t, ts, "POST", "/api/measure", `{"n":3,"reseat":true}`, &st)
	id := st.Measure.ID
	for i := 0; i < 2; i++ {
		deadline := time.Now().Add(10 * time.Second)
		for {
			call(t, ts, "GET", "/api/state", "", &st)
			if st.Measure.Phase == rf.ReseatWaiting && st.Measure.Done == i+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("not waiting: %+v", st.Measure)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(st.Measure.Message, "Unscrew and reconnect") {
			t.Fatal(st.Measure.Message)
		}
		time.Sleep(300 * time.Millisecond) // still waiting: nothing moved
		call(t, ts, "GET", "/api/state", "", &st)
		if st.Measure.Done != i+1 {
			t.Fatal("took a sweep without a re-seat")
		}
		bench.Reseat()
	}
	if m := waitMeasure(t, ts, id); m.Status != "done" || m.Done != 3 || !strings.Contains(m.Message, "re-seated") {
		t.Fatalf("reseat: %+v", m)
	}
	// Continue skips the wait.
	call(t, ts, "POST", "/api/measure", `{"n":2,"reseat":true}`, &st)
	id = st.Measure.ID
	time.Sleep(600 * time.Millisecond)
	call(t, ts, "POST", "/api/measure/continue", "", nil)
	if m := waitMeasure(t, ts, id); m.Status != "done" {
		t.Fatalf("continue: %+v", m)
	}
	// Refusals.
	for _, bad := range []string{`{"n":1}`, `{"n":5,"for":"x"}`, `{"n":5,"for":"batch"}`, `{"n":5,"for":"setup","step":"B"}`} {
		if c := call(t, ts, "POST", "/api/measure", bad, nil); c != 400 && c != 409 {
			t.Fatalf("%s: %d", bad, c)
		}
	}
}

func TestMeasureBatch(t *testing.T) {
	// Not a lot scenario: those number their units across the whole test run.
	ts, _, _ := newActionServer(t, "869")
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, ts, "POST", "/api/sweep", "", nil)
	var bv BatchView
	if c := call(t, ts, "POST", "/api/batches", `{"name":"lot ×N"}`, &bv); c != 200 {
		t.Fatalf("create %d", c)
	}
	var st State
	for i, name := range []string{"869", "869tuned", "cm869"} {
		call(t, ts, "POST", "/api/mock/antenna", `{"name":"`+name+`"}`, nil)
		if c := call(t, ts, "POST", "/api/measure", `{"n":3,"for":"batch"}`, &st); c != 200 {
			t.Fatalf("capture %d", c)
		}
		if m := waitMeasure(t, ts, st.Measure.ID); m.Status != "done" || m.Captured != i+1 {
			t.Fatalf("%+v", m)
		}
	}
	call(t, ts, "GET", "/api/batches/"+bv.ID, "", &bv)
	if len(bv.Antennas) != 3 || bv.Antennas[0].Rep == nil || bv.Antennas[0].Rep.N != 3 || bv.Antennas[0].AvgN != 3 ||
		bv.Summary.NRepeat != 3 || bv.Summary.RepeatHz <= 0 {
		t.Fatalf("%+v", bv.Summary)
	}
}

func TestSetupCheck(t *testing.T) {
	for _, c := range []struct {
		antenna, verdict string
	}{{"cm869", rf.SetupCoupled}, {"869tuned", rf.SetupQuiet}} {
		t.Run(c.antenna, func(t *testing.T) {
			ts, s, _ := newActionServer(t, c.antenna)
			call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
			call(t, ts, "POST", "/api/sweep", "", nil)
			var bv BatchView
			call(t, ts, "POST", "/api/batches", `{"name":"setup lot"}`, &bv)
			var st State
			if code := call(t, ts, "POST", "/api/measure", `{"n":4,"for":"setup","step":"A"}`, &st); code != 200 {
				t.Fatalf("A: %d", code)
			}
			waitMeasure(t, ts, st.Measure.ID)
			call(t, ts, "POST", "/api/mock/action", `{"action":"hand-on"}`, &st)
			if !st.MockHand {
				t.Fatal("hand")
			}
			call(t, ts, "POST", "/api/measure", `{"n":4,"for":"setup","step":"B"}`, &st)
			if m := waitMeasure(t, ts, st.Measure.ID); m.Status != "done" {
				t.Fatalf("B: %+v", m)
			}
			call(t, ts, "POST", "/api/mock/action", `{"action":"hand-off"}`, nil)
			var sv SetupView
			call(t, ts, "GET", "/api/setup", "", &sv)
			if sv.Result == nil || sv.Result.Verdict != c.verdict || len(sv.Steps) != 2 || sv.Window <= 0 {
				t.Fatalf("%s: %+v", c.antenna, sv.Result)
			}
			if c.verdict == rf.SetupCoupled && (!strings.Contains(sv.Result.Advice, "ferrite") || sv.Result.Steps[0].Shift > -2e6 ||
				!sv.Result.Steps[0].Looks || !strings.Contains(sv.Result.Advice, "loss masks mismatch")) {
				t.Fatalf("coupled: %+v", sv.Result)
			}
			// Kept with the batch and the antenna sheet.
			call(t, ts, "GET", "/api/batches/"+bv.ID, "", &bv)
			if bv.Setup == nil || bv.Setup.Verdict != c.verdict || !strings.Contains(strings.Join(bv.Summary.Conclusion, " "), "Setup check") {
				t.Fatalf("batch note %+v", bv.Setup)
			}
			var sh map[string]string
			call(t, ts, "POST", "/api/sheet", `{"antenna":{"name":"whip"}}`, &sh)
			html, _ := get(t, ts, "/"+sh["url"])
			if !strings.Contains(html, "Setup check") {
				t.Fatal("sheet lacks the setup line")
			}
			if a, _ := s.store.LoadAntenna("whip"); a.Setup == nil || a.Setup.Verdict != c.verdict {
				t.Fatalf("antenna %+v", a.Setup)
			}
		})
	}
}

func TestSmallPrintAndDefault(t *testing.T) {
	ts, s, _ := newActionServer(t, "869tuned")
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, ts, "POST", "/api/sweep", "", nil)
	long := strings.Repeat("<b>x</b> ", 40)
	var sh map[string]string
	body := `{"antenna":{"name":"sp","link":"https://example.com/a","photoCaption":"` + long + `","linkNote":"<b>x</b> note"}}`
	if c := call(t, ts, "POST", "/api/sheet", body, &sh); c != 200 {
		t.Fatalf("sheet %d", c)
	}
	a, _ := s.store.LoadAntenna("sp")
	if len([]rune(a.PhotoCaption)) != maxSmallPrint || a.LinkNote != "<b>x</b> note" {
		t.Fatalf("%q %q", a.PhotoCaption, a.LinkNote)
	}
	html, _ := get(t, ts, "/"+sh["url"])
	if !strings.Contains(html, `class="smallprint linknote">&lt;b&gt;x&lt;/b&gt; note`) || strings.Contains(html, "<b>x</b>") {
		t.Fatal("small print not escaped / missing")
	}
	var st State
	call(t, ts, "POST", "/api/settings", `{"linkNoteDefault":"  default   note "}`, &st)
	if st.Cal.Settings.LinkNoteDefault != "default note" {
		t.Fatalf("%q", st.Cal.Settings.LinkNoteDefault)
	}
}
