package rf

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ---------- acceptance criteria ----------
//
// There is no industry standard for how far an antenna's resonance may sit
// from the band. Antenna datasheets (LoRa 868 / 915 MHz whips and PCB
// antennas) specify the match over the operating band instead: typically
// VSWR ≤ 2:1 (return loss ≥ 9.5 dB), ≤ 1.5:1 for good ones, 2.2–2.5:1 for
// cheap ones. So the acceptance criterion is "worst SWR in the band below
// the limit", and the resonance tolerance is a secondary, user-chosen
// tuning goal.
//
// The verdict of a band with limit L:
//
//	BAD   worst in-band SWR ≥ L (the band is not covered at the limit)
//	OK    worst in-band SWR < L
//	GOOD  worst in-band SWR < G, G = 1 + (L − 1) / 2 (half the limit's
//	      excess over a perfect 1:1 match)
//
// With the default L = 2 this is the historical grading (GOOD < 1.5,
// OK < 2); L = 1.5 gives GOOD < 1.25, L = 2.5 gives GOOD < 1.75.

// DefaultSWRLimit is the band SWR limit when none is set: the common
// datasheet figure.
const DefaultSWRLimit = 2.0

// SWR limit presets.
var SWRLimitPresets = map[string]float64{"strict": 1.5, "typical": 2.0, "loose": 2.5}

// Resonance tolerance presets, in % of the target (OnTargetPct is "tight").
var TolPresets = map[string]float64{"tight": OnTargetPct, "normal": 0.5, "loose": 1.0}

// Tolerance modes.
const (
	TolAuto   = "auto"
	TolTight  = "tight"
	TolNormal = "normal"
	TolLoose  = "loose"
	TolCustom = "custom"
)

// Acceptance is the user's acceptance setting (per band profile, with a
// global default). The zero value is the default: SWR limit 2, resonance
// tolerance Auto.
type Acceptance struct {
	SWRLimit float64 `json:"swrLimit,omitempty"` // band SWR limit (0: DefaultSWRLimit)
	Tol      string  `json:"tol,omitempty"`      // auto (default), tight, normal, loose, custom
	TolPct   float64 `json:"tolPct,omitempty"`   // custom: ± % of the target
	TolMHz   float64 `json:"tolMHz,omitempty"`   // custom: ± MHz (used when TolPct is 0)
}

// Limit is the band SWR limit.
func (a Acceptance) Limit() float64 {
	if a.SWRLimit <= 1 || math.IsNaN(a.SWRLimit) {
		return DefaultSWRLimit
	}
	return a.SWRLimit
}

// GoodLimit is the GOOD threshold for the limit: 1 + (L − 1) / 2.
func (a Acceptance) GoodLimit() float64 { return GoodLimitFor(a.Limit()) }

// GoodLimitFor is 1 + (limit − 1) / 2 (1.5 for the default 2).
func GoodLimitFor(limit float64) float64 {
	if limit <= 1 {
		limit = DefaultSWRLimit
	}
	return 1 + (limit-1)/2
}

// Mode is the tolerance mode (auto when unset).
func (a Acceptance) Mode() string {
	switch a.Tol {
	case TolTight, TolNormal, TolLoose, TolCustom:
		return a.Tol
	}
	return TolAuto
}

// Normalize checks the setting and returns it in canonical form.
func (a Acceptance) Normalize() (Acceptance, error) {
	if a.SWRLimit != 0 && (math.IsNaN(a.SWRLimit) || a.SWRLimit < 1.1 || a.SWRLimit > 10) {
		return a, errors.New("the band SWR limit must be between 1.1 and 10")
	}
	a.SWRLimit = math.Round(a.SWRLimit*100) / 100
	if a.SWRLimit == DefaultSWRLimit {
		a.SWRLimit = 0
	}
	switch a.Tol {
	case "", TolAuto:
		a.Tol, a.TolPct, a.TolMHz = "", 0, 0
	case TolTight, TolNormal, TolLoose:
		a.TolPct, a.TolMHz = 0, 0
	case TolCustom:
		switch {
		case a.TolPct > 0 && a.TolPct <= 20:
			a.TolMHz = 0
		case a.TolPct == 0 && a.TolMHz > 0 && a.TolMHz <= 500:
		default:
			return a, errors.New("custom tolerance: give ± 0.001–20 % or ± 0.001–500 MHz")
		}
	default:
		return a, fmt.Errorf("unknown resonance tolerance %q (auto, tight, normal, loose or custom)", a.Tol)
	}
	return a, nil
}

// VerdictAt grades a worst-case in-band SWR against a band SWR limit.
func VerdictAt(worstSWR, limit float64) string {
	if limit <= 1 {
		limit = DefaultSWRLimit
	}
	switch {
	case worstSWR <= 0 || math.IsNaN(worstSWR):
		return "n/a"
	case worstSWR < GoodLimitFor(limit):
		return "good"
	case worstSWR < limit:
		return "ok"
	}
	return "bad"
}

// ---------- band margin and the Auto window ----------
//
// The Auto resonance tolerance asks the antenna's own curve how far its
// resonance may move before the band is no longer covered at the SWR
// limit. Around the resonance the SWR stays below the limit L from a to b
// (the SWR < L region, interpolated between points). Moving the antenna's
// resonance by d moves that region by about d (the shape of a whip's dip
// hardly changes over a few %), so the band [lo, hi] stays covered while
//
//	a + d < lo   and   b + d > hi,   i.e.   −(b − hi) < d < lo − a.
//
// RoomDown = b − hi and RoomUp = lo − a are the margins on each side.
// In offsets from the target t (resonance f): the resonance may sit
// between t + OffMin and t + OffMax with OffMin = (f − t) − RoomDown and
// OffMax = (f − t) + RoomUp, and the symmetric Auto window is
// ± min(−OffMin, OffMax). The "margin used" is how off-centre the band
// sits in the region: 0 % when both rooms are equal, 100 % when a band
// edge is at the limit, above 100 % when the band is not covered.

// Margin is the SWR margin of one sweep for one band.
type Margin struct {
	OK       bool    `json:"ok"`       // an SWR < L region was found around the resonance
	Limit    float64 `json:"limit"`    // the SWR limit used
	Lo       float64 `json:"lo"`       // Hz, SWR < L region
	Hi       float64 `json:"hi"`       // Hz
	Open     bool    `json:"open"`     // the region runs into the sweep edge: the margin is at least this
	RoomDown float64 `json:"roomDown"` // Hz the curve may move down (b − band.Hi)
	RoomUp   float64 `json:"roomUp"`   // Hz the curve may move up (band.Lo − a)
	Covered  bool    `json:"covered"`  // both rooms ≥ 0
	UsedPct  float64 `json:"usedPct"`  // margin used, % (capped at 999)
	OffMin   float64 `json:"offMin"`   // Hz: lowest resonance offset from the target that still covers the band
	OffMax   float64 `json:"offMax"`   // Hz: highest
	Window   float64 `json:"window"`   // Hz: symmetric Auto window ± min(−OffMin, OffMax), 0 when there is none
}

// BandMargin computes the margin of a sweep for band at the SWR limit.
// res is the resonance (0: the best match in the band is used) and
// target the frequency offsets are measured from.
func BandMargin(freq, swr []float64, band Band, target, res, limit float64) Margin {
	m := Margin{Limit: limit}
	n := len(freq)
	if n < 3 || len(swr) != n || !band.Valid() {
		return m
	}
	if limit <= 1 {
		limit = DefaultSWRLimit
		m.Limit = limit
	}
	// Centre of the region: the point nearest the resonance, else the best
	// match in (or nearest to) the band.
	c := -1
	if res > 0 {
		c = nearestIndex(freq, res)
		if swr[c] >= limit {
			c = -1
		}
	}
	if c < 0 {
		best := math.Inf(1)
		for i, f := range freq {
			if f >= band.Lo && f <= band.Hi && swr[i] < best {
				best, c = swr[i], i
			}
		}
		if c < 0 || swr[c] >= limit {
			return m
		}
		if res <= 0 {
			res = freq[c]
		}
	}
	bw := SWRBandwidth(freq, swr, c, limit)
	if !bw.Found {
		return m
	}
	m.OK = true
	m.Lo, m.Hi, m.Open = bw.Lo, bw.Hi, bw.LoOpen || bw.HiOpen
	m.RoomDown, m.RoomUp = m.Hi-band.Hi, band.Lo-m.Lo
	m.Covered = m.RoomDown >= 0 && m.RoomUp >= 0
	if target <= 0 {
		target = band.Reference()
	}
	off := res - target
	m.OffMin, m.OffMax = off-m.RoomDown, off+m.RoomUp
	if w := math.Min(-m.OffMin, m.OffMax); w > 0 {
		m.Window = w
	}
	total := m.RoomDown + m.RoomUp
	switch {
	case total <= 0:
		m.UsedPct = 999
	default:
		m.UsedPct = math.Min(999, 100*(1-2*math.Min(m.RoomDown, m.RoomUp)/total))
	}
	return m
}

func nearestIndex(freq []float64, f float64) int {
	best := 0
	for i, x := range freq {
		if math.Abs(x-f) < math.Abs(freq[best]-f) {
			best = i
		}
	}
	return best
}

// UsedFor is the share of the Auto window an offset uses, in % (offset
// over the room on its side; 0 when there is no window).
func (m Margin) UsedFor(offset float64) float64 {
	switch {
	case !m.OK:
		return 0
	case offset < 0 && m.OffMin < 0:
		return math.Min(999, 100*offset/m.OffMin)
	case offset > 0 && m.OffMax > 0:
		return math.Min(999, 100*offset/m.OffMax)
	case offset == 0:
		return 0
	}
	return 999
}

// Window is an acceptance setting resolved for one target (and, for Auto,
// one measured curve).
type Window struct {
	Mode    string  `json:"mode"`    // auto, tight, normal, loose, custom
	Limit   float64 `json:"limit"`   // band SWR limit
	Good    float64 `json:"good"`    // GOOD threshold
	Target  float64 `json:"target"`  // Hz
	Hz      float64 `json:"hz"`      // ± acceptance window around the target (0: none)
	Pct     float64 `json:"pct"`     // the same, % of the target
	AimHz   float64 `json:"aimHz"`   // TUNE's "on target" aim (the window, or ± 0.15 % with Auto)
	AutoOK  bool    `json:"autoOK"`  // Auto: computed from the curve
	AtLeast bool    `json:"atLeast"` // Auto: the SWR < L region runs into the sweep edge (window ≥ Hz)
	Note    string  `json:"note,omitempty"`
	Text    string  `json:"text"`  // "band SWR < 2.0, resonance ± 0.50 % (± 4.35 MHz)"
	Short   string  `json:"short"` // "± 4.35 MHz"
}

// Resolve turns the setting into a window around target. m is the margin of
// the measurement (used by Auto; nil when there is none).
func (a Acceptance) Resolve(target float64, m *Margin) Window {
	w := Window{Mode: a.Mode(), Limit: a.Limit(), Good: a.GoodLimit(), Target: target}
	aim := target * OnTargetPct / 100
	switch w.Mode {
	case TolAuto:
		w.AimHz = aim
		if m != nil && m.OK && m.Window > 0 {
			w.Hz, w.AutoOK, w.AtLeast = m.Window, true, m.Open
		} else if m != nil && m.OK {
			w.Note = fmt.Sprintf("the band is not covered at SWR < %s: no detuning is acceptable", fmtLimit(w.Limit))
		} else {
			w.Note = "no measured curve with an SWR < " + fmtLimit(w.Limit) + " region yet"
		}
	case TolCustom:
		if a.TolPct > 0 {
			w.Hz = target * a.TolPct / 100
		} else {
			w.Hz = a.TolMHz * 1e6
		}
		w.AimHz = w.Hz
	default:
		w.Hz = target * TolPresets[w.Mode] / 100
		w.AimHz = w.Hz
	}
	if target > 0 {
		w.Pct = 100 * w.Hz / target
	}
	w.Short = fmt.Sprintf("± %.2f MHz", math.Round(w.Hz/1e4+1e-6)/100)
	if w.AtLeast {
		w.Short = "≥ " + w.Short
	}
	res := ""
	switch {
	case w.Mode == TolAuto && w.AutoOK:
		res = fmt.Sprintf("resonance Auto %s (%.2f %%: the band stays below SWR %s)", w.Short, w.Pct, fmtLimit(w.Limit))
	case w.Mode == TolAuto:
		res = "resonance Auto (" + w.Note + ")"
	default:
		res = fmt.Sprintf("resonance ± %s %% (%s)", trimPct(w.Pct), w.Short)
	}
	w.Text = fmt.Sprintf("band SWR < %s, %s", fmtLimit(w.Limit), res)
	return w
}

func fmtLimit(l float64) string { return fmt.Sprintf("%.1f", l) }

func trimPct(p float64) string {
	s := fmt.Sprintf("%.3f", p)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// MedianWindow is the median of the positive values (0 if none): the Auto
// window of a lot is the median of its antennas' windows.
func MedianWindow(w []float64) float64 {
	var v []float64
	for _, x := range w {
		if x > 0 && finite(x) {
			v = append(v, x)
		}
	}
	return medianOf(v)
}

// ---------- small-sample statistics ----------

// t975 are the two-sided 95 % Student t quantiles for 1..30 degrees of freedom.
var t975 = []float64{12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
	2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
	2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042}

// TQuantile95 is the two-sided 95 % Student t quantile for df degrees of
// freedom (0 for df < 1).
func TQuantile95(df int) float64 {
	switch {
	case df < 1:
		return 0
	case df <= len(t975):
		return t975[df-1]
	}
	return 1.960 + 2.4/float64(df) // within 0.002 of the exact value from 30 on
}

// MeanCI95 is the half-width of the 95 % confidence interval of the mean
// of a sample with standard deviation sd (0 for n < 2).
func MeanCI95(sd float64, n int) float64 {
	if n < 2 {
		return 0
	}
	return TQuantile95(n-1) * sd / math.Sqrt(float64(n))
}
