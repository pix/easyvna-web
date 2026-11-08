package server

import (
	"context"
	"io"
	"log"
	"math/cmplx"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnav2"
)

func TestSolveAndCorrectOSL(t *testing.T) {
	e00, e11, t10 := complex(0.07, -0.02), complex(0.1, 0.05), complex(0.8, -0.3)
	raw := func(g complex128) complex128 { return e00 + t10*g/(1-e11*g) }
	o, s, l := []complex128{raw(1)}, []complex128{raw(-1)}, []complex128{raw(0)}
	a, b, c, err := SolveOSL(o, s, l)
	if err != nil {
		t.Fatal(err)
	}
	if cmplx.Abs(a[0]-e00) > 1e-12 || cmplx.Abs(b[0]-e11) > 1e-12 || cmplx.Abs(c[0]-t10) > 1e-12 {
		t.Fatalf("terms: %v %v %v", a, b, c)
	}
	for _, g := range []complex128{0.3i, -0.5, complex(0.2, -0.7), 1} {
		if got := CorrectOSL(raw(g), a[0], b[0], c[0]); cmplx.Abs(got-g) > 1e-9 {
			t.Errorf("correct %v: %v", g, got)
		}
	}
	if _, _, _, err := SolveOSL(o, o, l); err == nil {
		t.Error("OPEN == SHORT accepted")
	}
}

// v2Rig runs the server on the simulated NanoVNA V2 (binary protocol, raw
// data) with its calibration standards.
func v2Rig(t *testing.T) (*httptest.Server, *mock.Bench, *Store) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bench, _ := mock.NewBench("869", 0, false)
	open := func(ctx context.Context) (vna.Device, error) {
		return nanovnav2.Open(ctx, bench.NewSimV2(), nanovnav2.Options{PortName: "mock (V2 simulator)", Mock: true})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{}, Logger: log.New(io.Discard, "", 0),
		SetMockAntenna: bench.SetAntenna, MockCut: bench.Cut, MockStandard: bench.AttachStandard})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); <-done })
	return ts, bench, st
}

func TestHostCalibrationOnV2(t *testing.T) {
	ts, _, st := v2Rig(t)
	var state State
	call(t, ts, "GET", "/api/state", "", &state)
	for i := 0; i < 50 && !state.Connected; i++ {
		time.Sleep(20 * time.Millisecond)
		call(t, ts, "GET", "/api/state", "", &state)
	}
	if !state.Connected || state.Info.Driver != "nanovna-v2" || !state.Info.HostCal || !state.Info.Experimental || state.Cal.Status.Applied {
		t.Fatalf("V2 connect: %+v %+v", state.Info, state.Cal.Status)
	}
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var raw Result
	call(t, ts, "POST", "/api/sweep", "", &raw)
	if raw.CalApplied || warnCodes(raw)["noisy"] {
		t.Errorf("raw V2 data: calibrated %v, warnings %v", raw.CalApplied, raw.Summary.Warnings)
	}
	// Ideal antenna Γ at the end of the pigtail (what a calibration there sees).
	ant := mock.Scenarios["869"]()
	want := func(f float64) complex128 { return rf.GammaFromZ(ant.Impedance(f, 0)) }
	errAt := func(r Result) float64 {
		worst := 0.0
		for i, f := range r.Freq {
			if i%20 == 0 {
				worst = max(worst, cmplx.Abs(complex(r.S11[i][0], r.S11[i][1])-want(f)))
			}
		}
		return worst
	}
	if e := errAt(raw); e < 0.05 {
		t.Fatalf("raw data should be visibly off (%.3f)", e)
	}
	var cal CalState
	for _, step := range []string{"open", "short", "load", "done"} {
		body := "{}"
		if step == "open" {
			body = `{"fresh":true}`
		}
		if c := call(t, ts, "POST", "/api/cal/step/"+step, body, &cal); c != 200 || cal.Flow.Steps[step] != "done" {
			t.Fatalf("%s: %d %+v", step, c, cal.Flow)
		}
	}
	if !cal.Status.Applied || cal.Active == nil || cal.Active.Start != 779e6 {
		t.Fatalf("after DONE: %+v", cal)
	}
	var res Result
	call(t, ts, "POST", "/api/sweep", "", &res)
	// Drift of the simulated antenna is up to ±0.15 %: compare loosely.
	if e := errAt(res); e > 0.06 || !res.CalApplied {
		t.Errorf("corrected data off by %.3f (applied %v)", e, res.CalApplied)
	}
	// SAVE to the profile slot, switch away and back: auto-recall loads it.
	if c := call(t, ts, "POST", "/api/cal/save", "{}", &cal); c != 200 || cal.Active.Slot == nil || *cal.Active.Slot != 5 {
		t.Fatalf("save: %d %+v", c, cal.Active)
	}
	if _, err := st.Backend().Stat("hostcal/slot-5.json"); err != nil {
		t.Fatalf("saved terms: %v", err)
	}
	// Reconnecting (new device session) loads the profile's calibration.
	call(t, ts, "POST", "/api/connect", "", &state)
	if !state.Cal.Status.Applied || state.Cal.Active == nil || state.Cal.Active.Source != "slot 5" {
		t.Fatalf("reconnect: %+v", state.Cal)
	}
	// Another profile has none yet; coming back recalls slot 5.
	var pr msgState
	call(t, ts, "POST", "/api/profile", `{"id":"433"}`, &pr)
	call(t, ts, "POST", "/api/cal/step/reset", "{}", nil)
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, &pr)
	if !pr.State.Cal.Status.Applied || pr.Message != "recalled calibration slot 5" {
		t.Fatalf("auto-recall: %q %+v", pr.Message, pr.State.Cal.Status)
	}
	// TUNE's narrow window uses interpolated terms.
	call(t, ts, "POST", "/api/tune", `{"on":true}`, nil)
	call(t, ts, "POST", "/api/sweep", "", &res)
	if e := errAt(res); e > 0.06 || res.Tune == nil || !res.Tune.Found {
		t.Errorf("TUNE with interpolated host cal: err %.3f tune %+v", e, res.Tune)
	}
	call(t, ts, "POST", "/api/tune", `{"on":false}`, nil)
	// Errors of the flow do not drop the device.
	if c := call(t, ts, "POST", "/api/cal/recall", `{"slot":6}`, nil); c != 500 {
		t.Errorf("recall of an empty slot: %d", c)
	}
	call(t, ts, "GET", "/api/state", "", &state)
	if !state.Connected {
		t.Error("a calibration error dropped the device")
	}
}
