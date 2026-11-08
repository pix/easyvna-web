package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- setup check: is the cable part of the antenna? ----------
//
// The steps are Measure ×N jobs (for "setup"): A hands off with the cable
// as installed, B a hand on the cable 10–20 cm below the antenna, C
// (optional) touching the cable near the VNA. B and C are compared with A
// (rf.SetupCompare); the conclusion is kept as the last setup check, added
// to the antenna record when a sheet is made, and to the active batch.

// SetupNote is a setup check result as kept with an antenna or a batch.
type SetupNote struct {
	Time    time.Time `json:"time"`
	Profile string    `json:"profile"`
	Verdict string    `json:"verdict"` // quiet, slight, coupled
	Title   string    `json:"title"`
	Line    string    `json:"line"`
}

type setupSession struct {
	profile string
	target  float64
	a, b, c *rf.SetupCapture
	result  *rf.SetupVerdict
	note    *SetupNote
}

// SetupView is the setup check part of State.
type SetupView struct {
	Profile string            `json:"profile"`
	Steps   []rf.SetupCapture `json:"steps"`
	Result  *rf.SetupVerdict  `json:"result,omitempty"`
	Note    *SetupNote        `json:"note,omitempty"`
	Window  float64           `json:"window"` // Hz, tolerance window the shifts are compared with
}

func (s *Server) setupViewLocked() *SetupView {
	ss := s.setup
	if ss == nil {
		return nil
	}
	v := &SetupView{Profile: ss.profile, Steps: []rf.SetupCapture{}, Result: ss.result, Note: ss.note}
	for _, c := range []*rf.SetupCapture{ss.a, ss.b, ss.c} {
		if c != nil {
			v.Steps = append(v.Steps, *c)
		}
	}
	v.Window = s.setupWindowLocked()
	return v
}

// setupWindowLocked is the tolerance window of the setup check: the
// acceptance window of step A's measurement (± 0.15 % when there is none).
func (s *Server) setupWindowLocked() float64 {
	ss := s.setup
	if ss == nil {
		return 0
	}
	acc := s.settings.AcceptanceFor(ss.profile)
	w := acc.Resolve(ss.target, nil)
	if w.Mode == rf.TolAuto {
		w.Hz = 0
		if m := s.setupMargin; m != nil {
			w = acc.Resolve(ss.target, m)
		}
	}
	if w.Hz <= 0 {
		return ss.target * rf.OnTargetPct / 100
	}
	return w.Hz
}

// setupAddLocked records a step from a finished Measure ×N result.
func (s *Server) setupAddLocked(step string, res *Result, sweeps [][]complex128) error {
	cfg := res.Config
	prof := resultProfile(res)
	target := 0.0
	var band *rf.Band
	if !cfg.Identify && len(cfg.Bands) > 0 {
		b := cfg.Bands[0]
		band, target = &b, b.Reference()
	}
	if target <= 0 {
		target = res.Summary.MinSWRFreq
	}
	if step != "A" && (s.setup == nil || s.setup.a == nil) {
		return errors.New("capture step A (hands off) first")
	}
	if step != "A" && s.setup.profile != prof {
		return fmt.Errorf("step A was measured on the %s profile: measure step A again on this one", s.setup.profile)
	}
	ti := rf.Tune(res.Freq, res.Derived, target)
	c := &rf.SetupCapture{Step: step, Time: res.Time.Format("15:04:05"), N: len(sweeps), Found: ti.Found, ResFreq: ti.ResFreq,
		SWRTarget: ti.SWRAtTarget, Gamma: res.complexS11()}
	if res.Multi != nil {
		c.ResSD, c.Noise = res.Multi.ResSD, res.Multi.RMSNoise
	}
	if band != nil && len(res.Summary.Bands) > 0 {
		c.BandWorst = res.Summary.Bands[0].WorstSWR
	}
	switch step {
	case "A":
		s.setup = &setupSession{profile: prof, target: target, a: c}
		s.setupMargin = nil
		if res.Accept != nil {
			s.setupMargin = res.Accept.Margin
		}
		return nil
	case "B":
		s.setup.b = c
	case "C":
		s.setup.c = c
	}
	ss := s.setup
	win := s.setupWindowLocked()
	limit := s.settings.AcceptanceFor(prof).Limit()
	var diffs []rf.SetupDiff
	for _, x := range []*rf.SetupCapture{ss.b, ss.c} {
		if x != nil {
			diffs = append(diffs, rf.SetupCompare(*ss.a, *x, win, ss.target, limit))
		}
	}
	v := rf.SetupConclude(diffs)
	ss.result = &v
	ss.note = &SetupNote{Time: time.Now(), Profile: prof, Verdict: v.Verdict, Title: v.Title, Line: v.Line}
	if b := s.batch.cur; b != nil && b.Profile == prof {
		n := *ss.note
		b.Setup = &n
		b.Updated = time.Now()
		if err := s.store.SaveBatch(b); err != nil {
			return err
		}
		s.batch.version++
		s.batch.publish = true
	}
	return nil
}

func (s *Server) hSetupGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	v := s.setupViewLocked()
	s.mu.Unlock()
	if v == nil {
		v = &SetupView{Steps: []rf.SetupCapture{}}
	}
	writeJSON(w, 200, v)
}

// hSetup: POST /api/setup {"reset":true} starts over.
func (s *Server) hSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reset bool `json:"reset"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	if req.Reset {
		s.setup, s.setupMargin = nil, nil
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state())
}

// hMockAction drives the simulated bench: POST /api/mock/action
// {"action":"hand-on"|"hand-off"|"reseat"} or {"action":"trim","mm":0.5}
// (shortens the connected antenna's element, as MockCut: BATCH's adjusted
// versions in the simulator) (mock only).
func (s *Server) hMockAction(w http.ResponseWriter, r *http.Request) {
	if s.opt.MockAction == nil {
		writeErr(w, 404, errors.New("not in mock mode"))
		return
	}
	var req struct {
		Action string  `json:"action"`
		Mm     float64 `json:"mm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Action == "trim" {
		if s.opt.MockCut == nil || req.Mm == 0 || math.IsNaN(req.Mm) || math.Abs(req.Mm) > 20 {
			writeErr(w, 400, errors.New("trim: give mm (up to 20, + shortens)"))
			return
		}
		s.opt.MockCut(req.Mm)
		s.publishState()
		writeJSON(w, 200, s.state())
		return
	}
	if err := s.opt.MockAction(req.Action); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	switch req.Action {
	case "hand-on":
		s.mockHand = true
	case "hand-off":
		s.mockHand = false
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state())
}
