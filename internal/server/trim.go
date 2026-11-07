package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"vnaview/internal/rf"
)

// TrimEntry is one logged (length, resonance) point.
type TrimEntry struct {
	Time        time.Time `json:"time"`
	LengthMm    float64   `json:"lengthMm"`
	CutMm       float64   `json:"cutMm,omitempty"` // cut made just before this measurement
	FreqHz      float64   `json:"freqHz"`
	SWRAtRes    float64   `json:"swrAtRes"`
	SWRAtTarget float64   `json:"swrAtTarget"`
}

// TrimSession is a per-antenna trim log, saved as <data>/trim/<name>.json.
type TrimSession struct {
	Name       string             `json:"name"`
	Target     float64            `json:"target"`
	Created    time.Time          `json:"created"`
	Entries    []TrimEntry        `json:"entries"`
	Prediction *rf.TrimPrediction `json:"prediction,omitempty"` // computed, not stored
}

func (t *TrimSession) predict() {
	t.Prediction = nil
	pts := make([]rf.TrimPoint, 0, len(t.Entries))
	for _, e := range t.Entries {
		pts = append(pts, rf.TrimPoint{LengthMm: e.LengthMm, FreqHz: e.FreqHz})
	}
	if p, err := rf.PredictTrim(pts, t.Target); err == nil {
		t.Prediction = &p
	}
}

// currentResonance returns the resonance to log: the TUNE readout when TUNE
// is active, else the dip nearest the target in the last sweep.
func (s *Server) currentResonanceLocked(target float64) (f, swrRes, swrTarget float64, err error) {
	last := s.last
	if last == nil {
		return 0, 0, 0, errors.New("no sweep yet")
	}
	if last.Tune != nil && last.Tune.Target == target {
		if !last.Tune.Found {
			return 0, 0, 0, errors.New("no resonance in view yet: wait until TUNE has found it")
		}
		return last.Tune.ResFreq, last.Tune.SWRAtRes, last.Tune.SWRAtTarget, nil
	}
	ti := rf.Tune(last.Freq, last.Derived, target)
	if !ti.Found {
		return 0, 0, 0, errors.New("no resonance found near the target in the last sweep")
	}
	return ti.ResFreq, ti.SWRAtRes, ti.SWRAtTarget, nil
}

func (s *Server) hTrimList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.ListTrim())
}

func (s *Server) hTrimGet(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.LoadTrim(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	t.predict()
	writeJSON(w, 200, t)
}

// hTrimLog logs the current resonance. Body: {"target", "lengthMm"} for the
// first entry (creates the session), then {"cutMm"} after each cut (the new
// length is the previous length minus the cut), or an explicit "lengthMm".
func (s *Server) hTrimLog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target   *float64 `json:"target"`
		LengthMm *float64 `json:"lengthMm"`
		CutMm    *float64 `json:"cutMm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	t, err := s.store.LoadTrim(name)
	if err != nil {
		if _, ferr := SnapshotFile(name); ferr != nil {
			writeErr(w, 400, ferr)
			return
		}
		t = &TrimSession{Name: name, Created: time.Now()}
	}
	if req.Target != nil && *req.Target > 0 {
		t.Target = *req.Target
	}
	if t.Target <= 0 {
		s.mu.Lock()
		if s.tune != nil {
			t.Target = s.tune.Target
		} else {
			t.Target = s.defaultTargetLocked()
		}
		s.mu.Unlock()
	}
	e := TrimEntry{Time: time.Now()}
	switch {
	case req.LengthMm != nil:
		e.LengthMm = *req.LengthMm
	case req.CutMm != nil && len(t.Entries) > 0:
		e.CutMm = *req.CutMm
		e.LengthMm = t.Entries[len(t.Entries)-1].LengthMm - *req.CutMm
	default:
		writeErr(w, 400, errors.New("enter the element length (mm) for the first entry, then the cut after each trim"))
		return
	}
	if e.LengthMm <= 0 || e.LengthMm > 1e5 || math.IsNaN(e.LengthMm) {
		writeErr(w, 400, fmt.Errorf("length %.1f mm is not plausible", e.LengthMm))
		return
	}
	if e.CutMm != 0 && s.opt.MockCut != nil {
		s.opt.MockCut(e.CutMm) // simulated antenna follows the "cut"
	}
	// Measure now, so a log right after a cut never uses a stale sweep.
	ctx, cancel := sweepCtx(r)
	_, serr := s.sweepOnce(ctx)
	cancel()
	if serr != nil {
		writeErr(w, http.StatusServiceUnavailable, serr)
		return
	}
	s.mu.Lock()
	e.FreqHz, e.SWRAtRes, e.SWRAtTarget, err = s.currentResonanceLocked(t.Target)
	s.mu.Unlock()
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	t.Entries = append(t.Entries, e)
	if err := s.store.SaveTrim(t); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.setTrimActive(t.Name)
	t.predict()
	writeJSON(w, 200, t)
}

// hTrimUndo removes the last entry (it is cheap to log again).
func (s *Server) hTrimUndo(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.LoadTrim(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if len(t.Entries) == 0 {
		writeErr(w, 409, errors.New("log is empty"))
		return
	}
	t.Entries = t.Entries[:len(t.Entries)-1]
	if err := s.store.SaveTrim(t); err != nil {
		writeErr(w, 500, err)
		return
	}
	t.predict()
	writeJSON(w, 200, t)
}

// hTrimTarget changes the session target (prediction follows).
func (s *Server) hTrimTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target float64 `json:"target"`
	}
	if err := readJSON(r, &req); err != nil || req.Target <= 0 {
		writeErr(w, 400, errors.New("target must be > 0"))
		return
	}
	t, err := s.store.LoadTrim(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	t.Target = req.Target
	if err := s.store.SaveTrim(t); err != nil {
		writeErr(w, 500, err)
		return
	}
	t.predict()
	writeJSON(w, 200, t)
}

func (s *Server) hTrimSelect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.store.LoadTrim(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	s.setTrimActive(name)
	writeJSON(w, 200, s.state())
}

func (s *Server) setTrimActive(name string) {
	s.mu.Lock()
	changed := s.settings.TrimActive != name
	s.settings.TrimActive = name
	set := s.settings
	s.mu.Unlock()
	if changed {
		_ = s.store.SaveSettings(set)
		s.publishState()
	}
}
