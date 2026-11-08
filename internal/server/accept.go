package server

import (
	"errors"
	"net/http"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- acceptance setting ----------
//
// The acceptance setting (rf.Acceptance) is the band SWR limit that drives
// GOOD / OK / BAD everywhere, and the resonance tolerance. It is stored in
// settings.json: a global default and, optionally, one per band profile.
// Sweeps are analysed with the setting of their profile (Config.Accept);
// changing it re-grades the results in memory, and batches are graded with
// their profile's setting each time they are shown.

// AcceptanceFor is the setting of a profile (its own, else the default).
func (st Settings) AcceptanceFor(profile string) rf.Acceptance {
	if a, ok := st.AcceptProfile[profile]; ok {
		return a
	}
	return st.Accept
}

// AcceptState is the acceptance part of State.
type AcceptState struct {
	Profile string        `json:"profile"`
	Own     bool          `json:"own"`     // the profile has its own setting
	Setting rf.Acceptance `json:"setting"` // effective for the profile
	Default rf.Acceptance `json:"default"`
	Limit   float64       `json:"limit"`
	Good    float64       `json:"good"`
}

func (s *Server) acceptStateLocked() AcceptState {
	p := s.cfg.Profile
	if s.tune != nil {
		p = s.tune.Base.Profile
	}
	a := s.settings.AcceptanceFor(p)
	_, own := s.settings.AcceptProfile[p]
	return AcceptState{Profile: p, Own: own, Setting: a, Default: s.settings.Accept, Limit: a.Limit(), Good: a.GoodLimit()}
}

// AcceptView is the acceptance of one result: the window resolved for its
// primary band and the band's SWR margin.
type AcceptView struct {
	Window rf.Window  `json:"window"`
	Margin *rf.Margin `json:"margin,omitempty"`
	Band   string     `json:"band,omitempty"`
}

// acceptViewOf resolves the result's acceptance for its primary band (nil
// for identify and custom sweeps without a band).
func acceptViewOf(r *Result) *AcceptView {
	cfg := r.Config
	if cfg.Identify || len(cfg.Bands) == 0 || len(r.Summary.Bands) == 0 || len(r.Freq) < 3 {
		return nil
	}
	bs := r.Summary.Bands[0]
	band := bs.Band
	target := band.Reference()
	res := 0.0
	if bs.DipFreq != nil {
		res = *bs.DipFreq
	}
	m := rf.BandMargin(r.Freq, r.Derived.SWR, band, target, res, cfg.Accept.Limit())
	return &AcceptView{Window: cfg.Accept.Resolve(target, &m), Margin: &m, Band: band.Name}
}

// regrade analyses r again with acceptance a (verdicts, pass flags). The
// measurement warnings are kept as they were.
func regrade(r *Result, a rf.Acceptance) {
	if r == nil || len(r.Freq) == 0 {
		return
	}
	r.Config.Accept = a
	warn := r.Summary.Warnings
	if sum, err := rf.AnalyzeAt(r.Freq, r.Derived, r.Config.Bands, a.Limit()); err == nil {
		sum.Warnings = warn
		r.Summary = sum
	}
	if r.Tune == nil {
		r.Accept = acceptViewOf(r)
	}
}

// tuneAcceptLocked fills the TUNE readout's acceptance: the "on target" aim
// (the window for a fixed tolerance, ± 0.15 % with Auto), the window and
// the margin of the band. s.mu must be held.
func (s *Server) tuneInfoLocked(res *Result, target float64) rf.TuneInfo {
	base := s.tune.Base
	acc := s.settings.AcceptanceFor(base.Profile)
	aim := acc.Resolve(target, nil).AimHz
	ti := rf.TuneAt(res.Freq, res.Derived, target, aim, acc.Limit())
	if len(base.Bands) > 0 && !base.Identify {
		band := base.Bands[0]
		res0 := 0.0
		if ti.Found {
			res0 = ti.ResFreq
		}
		m := rf.BandMargin(res.Freq, res.Derived.SWR, band, target, res0, acc.Limit())
		// The narrow TUNE window may cut the SWR < L region: the profile's
		// last full sweep sees all of it.
		if full := s.lastByProfile[base.Profile]; (!m.OK || m.Open) && full != nil && full.Accept != nil && full.Accept.Margin != nil && full.Accept.Margin.OK {
			fm := *full.Accept.Margin
			if !m.OK || fm.Window > m.Window {
				m.Window, m.Open = fm.Window, fm.Open
			}
		}
		w := acc.Resolve(target, &m)
		ti.Accept, ti.Margin = &w, &m
	} else {
		w := acc.Resolve(target, nil)
		ti.Accept = &w
	}
	return ti
}

// hAcceptance sets the acceptance setting:
// POST /api/acceptance {"profile":"869","scope":"profile"|"default","accept":{…}} or {"profile","reset":true}.
func (s *Server) hAcceptance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string         `json:"profile"`
		Scope   string         `json:"scope"`
		Accept  *rf.Acceptance `json:"accept"`
		Reset   bool           `json:"reset"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	var acc rf.Acceptance
	if !req.Reset {
		if req.Accept == nil {
			writeErr(w, 400, errors.New("give accept or reset"))
			return
		}
		a, err := req.Accept.Normalize()
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		acc = a
	}
	s.mu.Lock()
	prof := req.Profile
	if prof == "" {
		prof = s.acceptStateLocked().Profile
	}
	set := s.settings
	set.AcceptProfile = map[string]rf.Acceptance{}
	for k, v := range s.settings.AcceptProfile {
		set.AcceptProfile[k] = v
	}
	switch {
	case req.Reset:
		delete(set.AcceptProfile, prof)
	case req.Scope == "default":
		set.Accept = acc
		delete(set.AcceptProfile, prof) // "use for every profile" includes this one
	default:
		set.AcceptProfile[prof] = acc
	}
	if len(set.AcceptProfile) == 0 {
		set.AcceptProfile = nil
	}
	s.mu.Unlock()
	if err := s.store.SaveSettings(set); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.settings.Accept, s.settings.AcceptProfile = set.Accept, set.AcceptProfile
	s.regradeAllLocked()
	last := s.last
	s.mu.Unlock()
	s.publishState()
	if last != nil {
		s.hub.publish("sweep", last)
	}
	writeJSON(w, 200, s.state())
}

// regradeAllLocked re-grades the results in memory after a change of the
// acceptance setting. s.mu must be held.
func (s *Server) regradeAllLocked() {
	seen := map[*Result]bool{}
	for k, r := range s.lastByProfile {
		prof := k
		if r.Source != nil && r.Source.Profile != "" {
			prof = r.Source.Profile
		}
		regrade(r, s.settings.AcceptanceFor(prof))
		seen[r] = true
	}
	if r := s.last; r != nil && !seen[r] {
		if r.Tune != nil && s.tune != nil {
			ti := s.tuneInfoLocked(r, s.tune.Target)
			r.Tune = &ti
		} else {
			regrade(r, s.settings.AcceptanceFor(resultProfile(r)))
		}
	}
	s.batch.version++
}

// resultProfile is the profile whose acceptance grades r.
func resultProfile(r *Result) string {
	if r.Source != nil && r.Source.Profile != "" {
		return r.Source.Profile
	}
	return r.Config.Profile
}

// acceptFor is the acceptance setting of a profile.
func (s *Server) acceptFor(profile string) rf.Acceptance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.AcceptanceFor(profile)
}

// batchViewOf is the view of b with its learnt slope and its profile's
// acceptance setting.
func (s *Server) batchViewOf(b *Batch) BatchView {
	return batchView(b, s.batchSlope(b), s.acceptFor(b.Profile))
}
