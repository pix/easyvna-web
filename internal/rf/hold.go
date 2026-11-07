package rf

import (
	"fmt"
	"math"
	"math/cmplx"
	"time"
)

// HOLD as a "stable positions" envelope.
//
// A naive per-sweep min/max envelope turns into chaos while the antenna is
// being bent or moved: sweeps taken mid-motion mix positions and single
// outliers dominate. Holder instead only accepts *stable positions*: a new
// sweep that differs too much from the previous one marks the antenna as
// moving and is rejected; when N consecutive sweeps agree, their complex
// average (lightly smoothed along frequency) is captured as one position.
// The envelope is the min/max over captured positions only.

// HoldConfig tunes the motion detector.
type HoldConfig struct {
	N int // consecutive agreeing sweeps needed to capture a position (2..10)
	// Threshold is the RMS |ΔΓ| between sweeps above which the antenna is
	// considered to be moving. Measured on a real H4 at 869 MHz with a static
	// antenna: ~0.0016 RMS between consecutive sweeps; a 1-point (450 kHz)
	// resonance shift gives ~0.004, a 5-point shift ~0.015.
	Threshold float64
}

// Sensitivity presets for Threshold.
var HoldSensitivity = map[string]float64{"low": 0.010, "normal": 0.006, "high": 0.004}

// DefaultHoldConfig is N=3, normal sensitivity.
func DefaultHoldConfig() HoldConfig { return HoldConfig{N: 3, Threshold: HoldSensitivity["normal"]} }

// HoldPosition is one captured stable position.
type HoldPosition struct {
	ID       int          `json:"id"` // 1-based, stable across removals
	Time     time.Time    `json:"time"`
	Gamma    []complex128 `json:"-"`
	SWR      []float64    `json:"swr"`
	ResFreq  float64      `json:"resFreq"`   // resonance (dip nearest the reference, or best match)
	MinSWR   float64      `json:"minSWR"`    // best SWR of this position
	BandWrst float64      `json:"bandWorst"` // worst SWR in the primary band (0 if no band)
}

// HoldStatus values.
const (
	HoldWaiting   = "waiting"   // first sweep after start/reset
	HoldMoving    = "moving"    // sweep rejected: differs from the previous one
	HoldSettling  = "settling"  // stable run in progress (Run of N)
	HoldCaptured  = "captured"  // a new position was captured with this sweep
	HoldDuplicate = "duplicate" // stable, but the same as an existing position
	HoldStill     = "still"     // still at the last captured position
)

// Holder is the HOLD state machine. Not safe for concurrent use.
type Holder struct {
	Cfg       HoldConfig
	Band      Band // primary band for per-position worst SWR (optional)
	RefFreq   float64
	Positions []HoldPosition
	nextID    int
	freqKey   string
	prev      []complex128
	run       [][]complex128
	settled   bool // the current stable run already produced (or matched) a position
}

// HoldUpdate is what one sweep did.
type HoldUpdate struct {
	Status   string  `json:"status"`
	Run      int     `json:"run"`      // sweeps in the current stable run
	Need     int     `json:"need"`     // N
	Diff     float64 `json:"diff"`     // RMS |ΔΓ| to the previous sweep
	Captured int     `json:"captured"` // ID of the position captured by this sweep (0 = none)
	SameAs   int     `json:"sameAs"`   // for "duplicate"/"still": matching position ID
}

// NewHolder creates a holder.
func NewHolder(cfg HoldConfig, band Band, ref float64) *Holder {
	if cfg.N < 2 {
		cfg.N = 2
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = HoldSensitivity["normal"]
	}
	return &Holder{Cfg: cfg, Band: band, RefFreq: ref, nextID: 1}
}

// rmsDiff is the RMS of |a-b| over all points.
func rmsDiff(a, b []complex128) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.Inf(1)
	}
	s := 0.0
	for i := range a {
		d := cmplx.Abs(a[i] - b[i])
		s += d * d
	}
	return math.Sqrt(s / float64(len(a)))
}

// smooth3 applies a light [1 2 1]/4 kernel along frequency.
func smooth3(g []complex128) []complex128 {
	n := len(g)
	out := make([]complex128, n)
	for i := range g {
		switch {
		case n < 3:
			out[i] = g[i]
		case i == 0:
			out[i] = (3*g[0] + g[1]) / 4
		case i == n-1:
			out[i] = (3*g[n-1] + g[n-2]) / 4
		default:
			out[i] = (g[i-1] + 2*g[i] + g[i+1]) / 4
		}
	}
	return out
}

func gridKey(freq []float64) string {
	if len(freq) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%.0f/%.0f", len(freq), freq[0], freq[len(freq)-1])
}

// Add feeds one sweep. A change of frequency grid resets everything.
func (h *Holder) Add(freq []float64, s11 []complex128, now time.Time) HoldUpdate {
	u := HoldUpdate{Need: h.Cfg.N}
	if k := gridKey(freq); k != h.freqKey {
		h.freqKey, h.Positions, h.prev, h.run, h.settled = k, nil, nil, nil, false
	}
	cur := append([]complex128(nil), s11...)
	if h.prev == nil {
		h.prev, h.run = cur, [][]complex128{cur}
		u.Status, u.Run = HoldWaiting, 1
		return u
	}
	u.Diff = rmsDiff(cur, h.prev)
	h.prev = cur
	moving := u.Diff > h.Cfg.Threshold
	if !moving && len(h.run) > 0 && rmsDiff(cur, h.run[0]) > 1.5*h.Cfg.Threshold {
		moving = true // slow creep: still changing relative to the start of the run
	}
	if moving {
		h.run, h.settled = [][]complex128{cur}, false
		u.Status, u.Run = HoldMoving, 1
		return u
	}
	h.run = append(h.run, cur)
	u.Run = len(h.run)
	if h.settled {
		u.Status = HoldStill
		if n := len(h.Positions); n > 0 {
			u.SameAs = h.Positions[n-1].ID
		}
		return u
	}
	if len(h.run) < h.Cfg.N {
		u.Status = HoldSettling
		return u
	}
	avg := smooth3(Average(h.run[len(h.run)-h.Cfg.N:]))
	h.settled = true
	for _, p := range h.Positions {
		if rmsDiff(avg, p.Gamma) <= h.Cfg.Threshold {
			u.Status, u.SameAs = HoldDuplicate, p.ID
			return u
		}
	}
	h.Positions = append(h.Positions, h.position(freq, avg, now))
	u.Status, u.Captured = HoldCaptured, h.Positions[len(h.Positions)-1].ID
	return u
}

func (h *Holder) position(freq []float64, g []complex128, now time.Time) HoldPosition {
	d := Derive(g, nil)
	p := HoldPosition{ID: h.nextID, Time: now, Gamma: g, SWR: d.SWR}
	h.nextID++
	best := 0
	for i, v := range d.SWR {
		if v < d.SWR[best] {
			best = i
		}
	}
	p.MinSWR, p.ResFreq = d.SWR[best], freq[best]
	ref := h.RefFreq
	if ref <= 0 {
		ref = freq[best]
	}
	if dp := NearestDip(FindDips(freq, d, 1, 1), Band{Lo: ref, Hi: ref, Ref: ref}); dp != nil {
		p.ResFreq = dp.Freq
	}
	if h.Band.Valid() {
		if bs := SummarizeBand(freq, d, h.Band); bs.Points > 0 || bs.Covered {
			p.BandWrst = bs.WorstSWR
		}
	}
	return p
}

// Remove deletes a position by ID; it returns false if not found.
func (h *Holder) Remove(id int) bool {
	for i, p := range h.Positions {
		if p.ID == id {
			h.Positions = append(h.Positions[:i], h.Positions[i+1:]...)
			return true
		}
	}
	return false
}

// HoldSummary is the robustness readout over all captured positions.
type HoldSummary struct {
	Positions   int       `json:"positions"`
	ResMin      float64   `json:"resMin"`
	ResMax      float64   `json:"resMax"`
	ResSpread   float64   `json:"resSpread"` // Hz, max - min: the key robustness figure
	WorstBand   float64   `json:"worstBand"` // worst in-band SWR across positions (0 if no band)
	WorstBandID int       `json:"worstBandId"`
	EnvMin      []float64 `json:"envMin,omitempty"`
	EnvMax      []float64 `json:"envMax,omitempty"`
}

// Summary computes the envelope and spread.
func (h *Holder) Summary() HoldSummary {
	s := HoldSummary{Positions: len(h.Positions)}
	if len(h.Positions) == 0 {
		return s
	}
	n := len(h.Positions[0].SWR)
	s.EnvMin, s.EnvMax = make([]float64, n), make([]float64, n)
	s.ResMin, s.ResMax = math.Inf(1), math.Inf(-1)
	for k, p := range h.Positions {
		for i, v := range p.SWR {
			if k == 0 || v < s.EnvMin[i] {
				s.EnvMin[i] = v
			}
			if k == 0 || v > s.EnvMax[i] {
				s.EnvMax[i] = v
			}
		}
		s.ResMin, s.ResMax = math.Min(s.ResMin, p.ResFreq), math.Max(s.ResMax, p.ResFreq)
		if p.BandWrst > s.WorstBand {
			s.WorstBand, s.WorstBandID = p.BandWrst, p.ID
		}
	}
	s.ResSpread = s.ResMax - s.ResMin
	return s
}
