package server

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

func noiseRig(t *testing.T, antenna string, noise float64) *httptest.Server {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context) (vna.Device, error) {
		sim := mock.New(mock.Options{PointTime: time.Microsecond, Seed: 3, Antenna: mock.Scenarios[antenna](), Noise: noise})
		return nanovnah.Open(ctx, sim, nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{}, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); <-done })
	return ts
}

func warnCodes(r Result) map[string]bool {
	m := map[string]bool{}
	for _, w := range r.Summary.Warnings {
		m[w.Code] = true
	}
	return m
}

// No noise or calibration warning on clean simulator sweeps, whatever the
// antenna, profile or calibration state; both appear when they should.
func TestNoNoiseFalsePositivesOnSimulator(t *testing.T) {
	for _, ant := range mock.ScenarioNames() {
		ts := noiseRig(t, ant, 0)
		for _, cal := range []string{"on", "off"} {
			call(t, ts, "POST", "/api/cal/step/"+cal, "{}", nil)
			for _, p := range []string{"wide", "survey", "433", "869", "915"} {
				call(t, ts, "POST", "/api/profile", `{"id":"`+p+`"}`, nil)
				for i := 0; i < 3; i++ {
					var r Result
					call(t, ts, "POST", "/api/sweep", "", &r)
					if w := warnCodes(r); w["noisy"] || (w["calmismatch"] && cal == "on") {
						t.Errorf("%s, cal %s, %s sweep %d: %v", ant, cal, p, i, r.Summary.Warnings)
					}
				}
			}
		}
		// TUNE's narrow 101-point window too.
		call(t, ts, "POST", "/api/cal/step/on", "{}", nil)
		call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
		call(t, ts, "POST", "/api/tune", `{"on":true}`, nil)
		for i := 0; i < 3; i++ {
			var r Result
			call(t, ts, "POST", "/api/sweep", "", &r)
			if warnCodes(r)["noisy"] {
				t.Errorf("%s TUNE: %v", ant, r.Summary.Warnings)
			}
		}
		call(t, ts, "POST", "/api/tune", `{"on":false}`, nil)
	}
}

func TestNoisyMeasurementWarned(t *testing.T) {
	ts := noiseRig(t, "869", 0.004) // five times the normal noise
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var r Result
	call(t, ts, "POST", "/api/sweep", "", &r)
	if !warnCodes(r)["noisy"] {
		t.Errorf("noisy sweep: %v", r.Summary.Warnings)
	}
	call(t, ts, "POST", "/api/sweep", "", &r)
	n := 0
	for _, w := range r.Summary.Warnings {
		if w.Code == "noisy" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("one noisy warning expected, got %d", n)
	}
}
