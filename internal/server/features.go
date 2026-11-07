package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"vnaview/internal/rf"
)

// ---------- averaging ----------

// average keeps the last cfg.Avg sweeps of the same grid and returns their
// complex mean. Any change of range/points/S21 restarts the average.
func (s *Server) average(cfg Config, freq []float64, s11, s21 []complex128) ([]complex128, []complex128, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := cfg.Avg
	if n <= 1 {
		s.avgBuf, s.avgBuf21, s.avgKey = nil, nil, ""
		return s11, s21, 1
	}
	key := fmt.Sprintf("%.0f/%.0f/%d/%v/%d", cfg.Start, cfg.Stop, len(freq), s21 != nil, n)
	if key != s.avgKey {
		s.avgBuf, s.avgBuf21, s.avgKey = nil, nil, key
	}
	s.avgBuf = append(s.avgBuf, s11)
	if len(s.avgBuf) > n {
		s.avgBuf = s.avgBuf[len(s.avgBuf)-n:]
	}
	out21 := s21
	if s21 != nil {
		s.avgBuf21 = append(s.avgBuf21, s21)
		if len(s.avgBuf21) > n {
			s.avgBuf21 = s.avgBuf21[len(s.avgBuf21)-n:]
		}
		out21 = rf.Average(s.avgBuf21)
	}
	return rf.Average(s.avgBuf), out21, len(s.avgBuf)
}

// postProcessLocked adds REF / TUNE / COMPARE information to a result and
// keeps the latest result per profile for reports. s.mu must be held.
func (s *Server) postProcessLocked(res *Result) {
	cfg := res.Config
	var primary *rf.Band
	if len(cfg.Bands) > 0 {
		b := cfg.Bands[0]
		primary = &b
	}
	if s.tune != nil {
		ti := rf.Tune(res.Freq, res.Derived, s.tune.Target)
		res.Tune = &ti
		s.retuneLocked(ti)
	} else {
		s.lastByProfile[cfg.Profile] = res
	}
	if ref := s.refs[cfg.Profile]; ref != nil {
		point := 0.0
		if s.tune != nil {
			point = s.tune.Target
		} else if primary != nil && !cfg.Identify {
			point = primary.Reference()
		}
		var band *rf.Band
		if !cfg.Identify {
			band = primary
		}
		d := rf.CompareRef(res.Freq, res.Derived, res.Summary.Dips, ref.Freq, ref.Derived, ref.Summary.Dips, point, band)
		d.RefTime = ref.Time.Format(time.RFC3339)
		res.Ref = &d
	}
	if s.compareOn {
		c := s.compare
		if c == nil {
			c = &CompareSession{Band: compareBand(cfg)}
		}
		res.Compare = c.live(res)
	}
}

// ---------- TUNE ----------

type tuneState struct {
	Target        float64
	Base          Config // profile config to restore on exit
	WasContinuous bool
	Lo, Hi        float64 // window limits (the profile's calibrated range)
}

// TuneView is the TUNE part of State.
type TuneView struct {
	Active bool    `json:"active"`
	Target float64 `json:"target"`
	Start  float64 `json:"start"`
	Stop   float64 `json:"stop"`
}

const tunePoints = 101

func (s *Server) defaultTargetLocked() float64 {
	cfg := s.cfg
	if !cfg.Identify && len(cfg.Bands) > 0 {
		return cfg.Bands[0].Reference()
	}
	// Identify sweeps: the LoRa band nearest the best dip.
	if s.last != nil && len(s.last.Summary.Dips) > 0 {
		best := s.last.Summary.Dips[0]
		for _, d := range s.last.Summary.Dips {
			if d.SWR < best.SWR {
				best = d
			}
		}
		target, dist := rf.Band869.Reference(), math.Inf(1)
		for _, b := range rf.LoRaBands {
			if dd := math.Abs(best.Freq - b.Reference()); dd < dist {
				target, dist = b.Reference(), dd
			}
		}
		return target
	}
	return rf.Band869.Reference()
}

// retuneLocked moves the narrow window when the resonance drifts towards an
// edge or out of it, so the user never has to touch the range.
func (s *Server) retuneLocked(ti rf.TuneInfo) {
	t := s.tune
	cur := s.cfg
	span := cur.Stop - cur.Start
	res := ti.ResFreq
	switch {
	case !ti.Found && ti.EdgeLow:
		res = cur.Start - span/2
	case !ti.Found && ti.EdgeHigh:
		res = cur.Stop + span/2
	case !ti.Found:
		res = t.Target
	}
	nearEdge := ti.Found && (ti.ResFreq-cur.Start < 0.12*span || cur.Stop-ti.ResFreq < 0.12*span)
	start, stop := rf.TuneWindow(res, t.Target, t.Lo, t.Hi)
	tooWide := (stop - start) < 0.5*span
	if !ti.Found || nearEdge || tooWide || t.Target < cur.Start || t.Target > cur.Stop {
		if math.Abs(start-cur.Start) > 0.01*span || math.Abs(stop-cur.Stop) > 0.01*span {
			s.cfg.Start, s.cfg.Stop = start, stop
		}
	}
}

func (s *Server) hTune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On     bool     `json:"on"`
		Target *float64 `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	switch {
	case req.On && s.tune == nil:
		if s.compareOn {
			s.mu.Unlock()
			writeErr(w, 409, errors.New("finish or close COMPARE first"))
			return
		}
		target := s.defaultTargetLocked()
		if req.Target != nil {
			target = *req.Target
		}
		base := s.cfg
		lo, hi := base.Start, base.Stop
		if target < lo || target > hi {
			s.mu.Unlock()
			writeErr(w, 400, fmt.Errorf("target %.3f MHz is outside this profile (%s): pick the matching profile first", target/1e6, mhzRange(lo, hi)))
			return
		}
		s.tune = &tuneState{Target: target, Base: base, WasContinuous: s.continuous.Load(), Lo: lo, Hi: hi}
		res := target
		if s.last != nil && s.last.Config.Profile == base.Profile {
			if ti := rf.Tune(s.last.Freq, s.last.Derived, target); ti.Found {
				res = ti.ResFreq
			}
		}
		s.cfg.Start, s.cfg.Stop = rf.TuneWindow(res, target, lo, hi)
		s.cfg.Points = tunePoints
		s.cfg.S21 = false
		s.continuous.Store(true)
	case req.On && s.tune != nil && req.Target != nil:
		if *req.Target < s.tune.Lo || *req.Target > s.tune.Hi {
			s.mu.Unlock()
			writeErr(w, 400, fmt.Errorf("target outside %s", mhzRange(s.tune.Lo, s.tune.Hi)))
			return
		}
		s.tune.Target = *req.Target
		s.cfg.Start, s.cfg.Stop = rf.TuneWindow(*req.Target, *req.Target, s.tune.Lo, s.tune.Hi)
	case !req.On && s.tune != nil:
		t := s.tune
		s.tune = nil
		avg := s.cfg.Avg
		s.cfg = t.Base
		s.cfg.Avg = avg
		s.continuous.Store(t.WasContinuous)
	}
	s.mu.Unlock()
	s.kick()
	s.publishState()
	writeJSON(w, 200, s.state())
}

// kick wakes the sweep loop if continuous sweeping is on.
func (s *Server) kick() {
	if s.continuous.Load() {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// ---------- REF ----------

// RefInfo describes a stored REF trace.
type RefInfo struct {
	Time    time.Time `json:"time"`
	Points  int       `json:"points"`
	Start   float64   `json:"start"`
	Stop    float64   `json:"stop"`
	HasUndo bool      `json:"hasUndo"`
}

func (s *Server) hRefSet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	last := s.last
	if last == nil || last.Config.Profile != s.cfg.Profile {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("sweep this profile first, then tap REF"))
		return
	}
	prof := s.cfg.Profile
	prev := s.refs[prof]
	s.mu.Unlock()
	if err := s.store.SaveRef(prof, last, prev); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.refs[prof] = last
	s.mu.Unlock()
	s.publishState()
	msg := "REF stored"
	if prev != nil {
		msg = "REF replaced (previous from " + prev.Time.Format("15:04:05") + ")"
	}
	writeJSON(w, 200, map[string]any{"message": msg, "replaced": prev != nil, "state": s.state()})
}

func (s *Server) hRefClear(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	prof := s.cfg.Profile
	cur := s.refs[prof]
	s.mu.Unlock()
	if cur == nil {
		writeErr(w, 404, errors.New("no REF for this profile"))
		return
	}
	if err := s.store.SaveRef(prof, nil, cur); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	delete(s.refs, prof)
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": "REF cleared", "state": s.state()})
}

// hRefUndo swaps the current REF with the previous one (undo of set or clear).
func (s *Server) hRefUndo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	prof := s.cfg.Profile
	cur := s.refs[prof]
	s.mu.Unlock()
	prev, err := s.store.LoadRefPrev(prof)
	if err != nil {
		writeErr(w, 404, errors.New("nothing to undo"))
		return
	}
	if err := s.store.SaveRef(prof, prev, cur); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	if prev != nil {
		s.refs[prof] = prev
	} else {
		delete(s.refs, prof)
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": "REF restored", "state": s.state()})
}

func (s *Server) hRefGet(w http.ResponseWriter, r *http.Request) {
	prof := r.URL.Query().Get("profile")
	s.mu.Lock()
	if prof == "" {
		prof = s.cfg.Profile
	}
	ref := s.refs[prof]
	s.mu.Unlock()
	if ref == nil {
		writeErr(w, 404, errors.New("no REF"))
		return
	}
	writeJSON(w, 200, ref)
}

// ---------- mock control ----------

func (s *Server) hMockAntenna(w http.ResponseWriter, r *http.Request) {
	if s.opt.SetMockAntenna == nil {
		writeErr(w, 404, errors.New("not in mock mode"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.opt.SetMockAntenna(req.Name); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	s.opt.MockAntenna = req.Name
	s.avgBuf, s.avgKey = nil, ""
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state())
}

// ---------- averaging selector ----------

func (s *Server) hAvg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		N int `json:"n"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	switch req.N {
	case 1, 2, 4, 8, 16:
	default:
		writeErr(w, 400, errors.New("averaging must be 1, 2, 4, 8 or 16"))
		return
	}
	s.mu.Lock()
	s.cfg.Avg = req.N
	if s.tune != nil {
		s.tune.Base.Avg = req.N
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state())
}

// sweepCtx bounds a single sweep request.
func sweepCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Minute)
}
