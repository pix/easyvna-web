package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// CompareSession is the S21 COMPARE state: a fixed reference antenna on
// port 1, the antennas under test on port 2. Antenna A's S21 is stored and
// every later antenna is shown relative to it. No THRU calibration is
// needed because only the difference between two S21 sweeps is used.
type CompareSession struct {
	Profile     string         `json:"profile"`
	Band        rf.Band        `json:"band"`
	AName       string         `json:"aName"`
	ATime       time.Time      `json:"aTime"`
	Freq        []float64      `json:"freq"`
	AS21dB      []float64      `json:"aS21dB"`
	ABandDB     float64        `json:"aBandDB"` // power-average S21 in band, dB
	Leaderboard []CompareEntry `json:"leaderboard"`
}

// CompareEntry is one tested antenna.
type CompareEntry struct {
	Name    string    `json:"name"`
	Time    time.Time `json:"time"`
	MeanDB  float64   `json:"meanDB"` // in-band power average, relative to A
	WorstDB float64   `json:"worstDB"`
	BestDB  float64   `json:"bestDB"`
}

// CompareLive is attached to every sweep while COMPARE is active.
type CompareLive struct {
	Ready     bool      `json:"ready"`             // A is stored and the grid matches
	Problem   string    `json:"problem,omitempty"` // why it is not ready
	Band      rf.Band   `json:"band"`
	BandDB    float64   `json:"bandDB"` // live in-band power-average S21, dB (absolute)
	MeanDB    float64   `json:"meanDB"` // Δ vs A, dB
	WorstDB   float64   `json:"worstDB"`
	WorstFreq float64   `json:"worstFreq"`
	BestDB    float64   `json:"bestDB"`
	BestFreq  float64   `json:"bestFreq"`
	Delta     []float64 `json:"delta,omitempty"` // per point, dB
}

// compareBand picks the band for a profile: its primary band, or the whole
// sweep for identify profiles.
func compareBand(cfg Config) rf.Band {
	if !cfg.Identify && len(cfg.Bands) > 0 {
		return cfg.Bands[0]
	}
	return rf.Band{Name: "sweep", Lo: cfg.Start, Hi: cfg.Stop}
}

// bandPowerDB is the in-band power average of S21 (dB), using all points
// inside the band (or the nearest point if the band falls between points).
func bandPowerDB(freq, s21dB []float64, b rf.Band) float64 {
	sum, n := 0.0, 0
	for i, f := range freq {
		if f >= b.Lo && f <= b.Hi {
			sum += math.Pow(10, s21dB[i]/10)
			n++
		}
	}
	if n == 0 {
		return rf.InterpAt(freq, s21dB, b.Reference())
	}
	return 10 * math.Log10(sum/float64(n))
}

func (c *CompareSession) live(res *Result) *CompareLive {
	out := &CompareLive{Band: c.Band}
	if len(res.Derived.S21dB) == 0 {
		out.Problem = "no S21 in this sweep"
		return out
	}
	out.BandDB = bandPowerDB(res.Freq, res.Derived.S21dB, c.Band)
	switch {
	case len(c.AS21dB) == 0:
		out.Problem = "store antenna A first"
		return out
	case res.Config.Profile != c.Profile || len(res.Freq) != len(c.Freq) ||
		res.Freq[0] != c.Freq[0] || res.Freq[len(res.Freq)-1] != c.Freq[len(c.Freq)-1]:
		out.Problem = fmt.Sprintf("A was stored on the %s profile: switch back to it", c.Profile)
		return out
	}
	out.Ready = true
	out.Delta = make([]float64, len(res.Freq))
	out.WorstDB, out.BestDB = math.Inf(1), math.Inf(-1)
	for i, f := range res.Freq {
		d := res.Derived.S21dB[i] - c.AS21dB[i]
		out.Delta[i] = d
		if f >= c.Band.Lo && f <= c.Band.Hi {
			if d < out.WorstDB {
				out.WorstDB, out.WorstFreq = d, f
			}
			if d > out.BestDB {
				out.BestDB, out.BestFreq = d, f
			}
		}
	}
	if math.IsInf(out.WorstDB, 0) { // band between points
		v := rf.InterpAt(res.Freq, out.Delta, c.Band.Reference())
		out.WorstDB, out.BestDB, out.WorstFreq, out.BestFreq = v, v, c.Band.Reference(), c.Band.Reference()
	}
	out.MeanDB = out.BandDB - c.ABandDB
	return out
}

// CompareView is the COMPARE part of State (without the big arrays).
type CompareView struct {
	Active      bool           `json:"active"`
	HasA        bool           `json:"hasA"`
	AName       string         `json:"aName,omitempty"`
	ATime       *time.Time     `json:"aTime,omitempty"`
	Profile     string         `json:"profile,omitempty"`
	Band        *rf.Band       `json:"band,omitempty"`
	Leaderboard []CompareEntry `json:"leaderboard"`
	HasUndo     bool           `json:"hasUndo"`
}

func (s *Server) compareViewLocked() CompareView {
	v := CompareView{Active: s.compareOn, Leaderboard: []CompareEntry{}, HasUndo: s.store.HasComparePrev()}
	if c := s.compare; c != nil {
		v.HasA = len(c.AS21dB) > 0
		v.AName, v.Profile = c.AName, c.Profile
		if v.HasA {
			t, b := c.ATime, c.Band
			v.ATime, v.Band = &t, &b
		}
		v.Leaderboard = append(v.Leaderboard, c.Leaderboard...)
	}
	return v
}

func (s *Server) hCompareStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	if req.On && s.tune != nil {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("close TUNE first"))
		return
	}
	if req.On && !s.compareOn {
		s.compareOn, s.compareS21Prev = true, s.cfg.S21
		s.cfg.S21 = true
	} else if !req.On && s.compareOn {
		s.compareOn = false
		s.cfg.S21 = s.compareS21Prev
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state())
}

// hCompareStoreA stores the latest S21 sweep as antenna A. Replacing an
// existing A clears the leaderboard (its numbers were relative to the old
// A); the previous session is kept for undo.
func (s *Server) hCompareStoreA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	last, cfg, prev := s.last, s.cfg, s.compare
	s.mu.Unlock()
	if last == nil || len(last.Derived.S21dB) == 0 || last.Config.Profile != cfg.Profile {
		writeErr(w, 409, errors.New("no S21 sweep yet: wait for the live S21 reading, then store A"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "A"
	}
	band := compareBand(last.Config)
	c := &CompareSession{Profile: last.Config.Profile, Band: band, AName: name, ATime: last.Time,
		Freq: last.Freq, AS21dB: append([]float64(nil), last.Derived.S21dB...),
		ABandDB: bandPowerDB(last.Freq, last.Derived.S21dB, band), Leaderboard: []CompareEntry{}}
	c.Leaderboard = append(c.Leaderboard, CompareEntry{Name: name, Time: last.Time})
	if err := s.store.SaveCompare(c, prev); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.compare = c
	s.mu.Unlock()
	s.publishState()
	msg := "stored " + name + " as reference A"
	if prev != nil && len(prev.AS21dB) > 0 {
		msg += fmt.Sprintf(" (replaced %s and its %d results; Undo restores them)", prev.AName, len(prev.Leaderboard))
	}
	writeJSON(w, 200, map[string]any{"message": msg, "state": s.state()})
}

// hCompareAdd records the live antenna on the leaderboard (same name
// replaces its earlier entry).
func (s *Server) hCompareAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, 400, errors.New("give the antenna a name"))
		return
	}
	s.mu.Lock()
	c, last := s.compare, s.last
	var live *CompareLive
	if c != nil && last != nil {
		live = c.live(last)
	}
	if live == nil || !live.Ready {
		s.mu.Unlock()
		p := "store A first"
		if live != nil {
			p = live.Problem
		}
		writeErr(w, 409, errors.New(p))
		return
	}
	cp := *c
	cp.Leaderboard = nil
	replaced := false
	for _, e := range c.Leaderboard {
		if e.Name == name {
			replaced = true
			continue
		}
		cp.Leaderboard = append(cp.Leaderboard, e)
	}
	cp.Leaderboard = append(cp.Leaderboard, CompareEntry{Name: name, Time: last.Time, MeanDB: live.MeanDB, WorstDB: live.WorstDB, BestDB: live.BestDB})
	sort.SliceStable(cp.Leaderboard, func(i, j int) bool { return cp.Leaderboard[i].MeanDB > cp.Leaderboard[j].MeanDB })
	s.mu.Unlock()
	if err := s.store.SaveCompare(&cp, c); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.compare = &cp
	s.mu.Unlock()
	s.publishState()
	msg := fmt.Sprintf("%s: %+.1f dB vs %s", name, live.MeanDB, c.AName)
	if replaced {
		msg += " (replaced earlier result)"
	}
	writeJSON(w, 200, map[string]any{"message": msg, "state": s.state()})
}

// hCompareUndo restores the previous compare session (undo of store A,
// add, or reset).
func (s *Server) hCompareUndo(w http.ResponseWriter, r *http.Request) {
	prev, err := s.store.LoadComparePrev()
	if err != nil {
		writeErr(w, 404, errors.New("nothing to undo"))
		return
	}
	s.mu.Lock()
	cur := s.compare
	s.mu.Unlock()
	if err := s.store.SaveCompare(prev, cur); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.compare = prev
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": "compare restored", "state": s.state()})
}

// hCompareReset starts over (undoable).
func (s *Server) hCompareReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cur := s.compare
	s.mu.Unlock()
	if err := s.store.SaveCompare(nil, cur); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.compare = nil
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": "compare cleared", "state": s.state()})
}
