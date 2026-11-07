package rf

import (
	"math"
	"sort"
)

// MismatchLoss returns the mismatch loss in dB, -10·log10(1-|Γ|²): the
// power that is reflected instead of delivered to the antenna. Capped at
// MaxMLdB for |Γ| >= 1.
func MismatchLoss(g float64) float64 {
	if g >= 1 {
		return MaxMLdB
	}
	ml := -10 * math.Log10(1-g*g)
	if ml > MaxMLdB || math.IsNaN(ml) {
		return MaxMLdB
	}
	return ml
}

// MaxMLdB caps mismatch loss for display.
const MaxMLdB = 60.0

// QFromBandwidth estimates the antenna Q from the fractional bandwidth at
// SWR threshold s, assuming the antenna is matched at its resonance
// (Yaghjian & Best: Q ≈ 2√β / FBW with β = (s-1)²/(4s)).
func QFromBandwidth(f0, bw, s float64) float64 {
	if f0 <= 0 || bw <= 0 || s <= 1 {
		return 0
	}
	return (s - 1) / math.Sqrt(s) / (bw / f0)
}

// Warning is an automatic hint about the measurement itself.
type Warning struct {
	Code  string `json:"code"`  // "open", "calcheck", "lossy"
	Level string `json:"level"` // "bad", "warn"
	Title string `json:"title"`
	Text  string `json:"text"`
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Round(p * float64(len(s)-1)))
	return s[i]
}

// Detect looks for measurements that are not an antenna: nothing connected
// (|Γ|≈1 everywhere), a calibration that does not fit (|Γ| > 1), or a
// lossy/resistive load posing as an antenna (SWR low and flat over a wide
// sweep with little reactance swing).
func Detect(freq []float64, d Derived) []Warning {
	out := []Warning{}
	n := len(freq)
	if n < 5 {
		return out
	}
	gLow := percentile(d.GammaMag, 0.05)
	gMed := percentile(d.GammaMag, 0.5)
	switch {
	case gMed > 1.04:
		out = append(out, Warning{Code: "calcheck", Level: "bad", Title: "Reflection above 1",
			Text: "|Γ| is above 1 across the sweep: the calibration does not match this setup (other cable, other range, or stale cal). Recalibrate this profile."})
	case gLow > 0.9:
		out = append(out, Warning{Code: "open", Level: "bad", Title: "No antenna?",
			Text: "|Γ| ≈ 1 everywhere: open or shorted cable, nothing connected, or a broken connector."})
	}
	span := freq[n-1] - freq[0]
	centre := (freq[n-1] + freq[0]) / 2
	if centre > 0 && span/centre >= 0.5 && gLow <= 0.9 {
		swr10, swr90 := percentile(d.SWR, 0.1), percentile(d.SWR, 0.9)
		absX := make([]float64, n)
		for i, x := range d.X {
			absX[i] = math.Abs(x)
		}
		if swr90 < 2.2 && swr90-swr10 < 0.8 && percentile(absX, 0.9) < 30 {
			out = append(out, Warning{Code: "lossy", Level: "warn", Title: "Looks lossy / resistive (fake?)",
				Text: "SWR is low and flat across a wide sweep with almost no reactance swing. A real antenna is only matched near resonance: this behaves like a resistor or a very lossy antenna."})
		}
	}
	return out
}

// RefDelta compares a sweep against a stored reference trace.
type RefDelta struct {
	RefTime     string   `json:"refTime,omitempty"`
	DipFreq     *float64 `json:"dipFreq,omitempty"`    // current resonance near the reference point
	RefDipFreq  *float64 `json:"refDipFreq,omitempty"` // reference resonance
	DF          *float64 `json:"df,omitempty"`         // Hz, current - reference
	At          float64  `json:"at"`                   // frequency used for ΔSWR (band reference or best match)
	SWR         float64  `json:"swr"`
	RefSWR      float64  `json:"refSWR"`
	DSWR        float64  `json:"dswr"`
	BandWorst   *float64 `json:"bandWorst,omitempty"`
	RefBandWst  *float64 `json:"refBandWorst,omitempty"`
	DBandWorst  *float64 `json:"dBandWorst,omitempty"`
	SameRange   bool     `json:"sameRange"`
	Description string   `json:"description"`
}

// InterpAt linearly interpolates y(freq) at f (clamped to the sweep).
func InterpAt(freq, y []float64, f float64) float64 {
	if len(freq) == 0 {
		return 0
	}
	return interp(freq, y, f)
}

// CompareRef computes Δf of the resonance nearest point and ΔSWR at point.
// point is normally the primary band reference frequency (or 0 to use the
// current best match).
func CompareRef(freq []float64, d Derived, dips []Dip, rfreq []float64, rd Derived, rdips []Dip, point float64, band *Band) RefDelta {
	var out RefDelta
	if len(freq) == 0 || len(rfreq) == 0 {
		return out
	}
	out.SameRange = freq[0] == rfreq[0] && freq[len(freq)-1] == rfreq[len(rfreq)-1]
	if point <= 0 {
		best := 0
		for i, v := range d.SWR {
			if v < d.SWR[best] {
				best = i
			}
		}
		point = freq[best]
	}
	near := func(ds []Dip) *Dip {
		var b *Dip
		for i := range ds {
			if b == nil || math.Abs(ds[i].Freq-point) < math.Abs(b.Freq-point) {
				b = &ds[i]
			}
		}
		return b
	}
	if a, b := near(dips), near(rdips); a != nil && b != nil {
		fa, fb := a.Freq, b.Freq
		df := fa - fb
		out.DipFreq, out.RefDipFreq, out.DF = &fa, &fb, &df
	}
	out.At = point
	out.SWR = InterpAt(freq, d.SWR, point)
	out.RefSWR = InterpAt(rfreq, rd.SWR, point)
	out.DSWR = out.SWR - out.RefSWR
	if band != nil && band.Valid() {
		w, rw := SummarizeBand(freq, d, *band).WorstSWR, SummarizeBand(rfreq, rd, *band).WorstSWR
		if w > 0 && rw > 0 {
			dw := w - rw
			out.BandWorst, out.RefBandWst, out.DBandWorst = &w, &rw, &dw
		}
	}
	return out
}

// TuneInfo is the big-number readout of the TUNE view.
type TuneInfo struct {
	Target      float64 `json:"target"`
	Found       bool    `json:"found"`     // a resonance was found in the window
	ResFreq     float64 `json:"resFreq"`   // Hz
	Offset      float64 `json:"offset"`    // Hz, resonance - target
	OffsetPct   float64 `json:"offsetPct"` // %
	Direction   string  `json:"direction"` // "shorten", "lengthen", "ok", "unknown"
	SWRAtRes    float64 `json:"swrAtRes"`
	SWRAtTarget float64 `json:"swrAtTarget"`
	RLAtTarget  float64 `json:"rlAtTarget"`
	MLAtTarget  float64 `json:"mlAtTarget"`
	Verdict     string  `json:"verdict"` // grade of SWR at target
	EdgeLow     bool    `json:"edgeLow"` // best match sits at the window edge
	EdgeHigh    bool    `json:"edgeHigh"`
}

// OnTargetPct is the offset (percent of target) treated as "on target".
const OnTargetPct = 0.15

// Tune evaluates a (narrow) sweep against the target frequency. The
// resonance is the return-loss peak nearest the target (parabolically
// refined), or the best-match point when no proper dip is visible.
func Tune(freq []float64, d Derived, target float64) TuneInfo {
	t := TuneInfo{Target: target, Direction: "unknown"}
	n := len(freq)
	if n < 3 || target <= 0 {
		return t
	}
	t.SWRAtTarget = InterpAt(freq, d.SWR, target)
	t.RLAtTarget = InterpAt(freq, d.RL, target)
	t.MLAtTarget = MismatchLoss(InterpAt(freq, d.GammaMag, target))
	t.Verdict = Verdict(t.SWRAtTarget)
	dips := FindDips(freq, d, 1.0, 1.0)
	if dp := NearestDip(dips, Band{Lo: target, Hi: target, Ref: target}); dp != nil {
		t.Found, t.ResFreq, t.SWRAtRes = true, dp.Freq, dp.SWR
	} else {
		best := 0
		for i, v := range d.SWR {
			if v < d.SWR[best] {
				best = i
			}
		}
		t.ResFreq, t.SWRAtRes = freq[best], d.SWR[best]
		t.EdgeLow, t.EdgeHigh = best == 0, best == n-1
		t.Found = !t.EdgeLow && !t.EdgeHigh && d.SWR[best] < 5
	}
	t.Offset = t.ResFreq - target
	t.OffsetPct = 100 * t.Offset / target
	switch {
	case !t.Found && t.EdgeLow:
		t.Direction = "shorten" // resonance is below the window: too long
	case !t.Found && t.EdgeHigh:
		t.Direction = "lengthen"
	case !t.Found:
		t.Direction = "unknown"
	case math.Abs(t.OffsetPct) <= OnTargetPct:
		t.Direction = "ok"
	case t.Offset < 0:
		t.Direction = "shorten" // resonance below target: element too long
	default:
		t.Direction = "lengthen"
	}
	return t
}

// TuneWindow picks a narrow sweep window that contains both the resonance
// and the target with margin, clamped to [lo, hi] (the calibrated range).
func TuneWindow(res, target, lo, hi float64) (start, stop float64) {
	if res <= 0 {
		res = target
	}
	span := math.Max(0.05*target, 2.6*math.Abs(res-target))
	centre := (res + target) / 2
	start, stop = centre-span/2, centre+span/2
	if start < lo {
		stop += lo - start
		start = lo
	}
	if stop > hi {
		start -= stop - hi
		stop = hi
	}
	if start < lo {
		start = lo
	}
	return math.Round(start/1e3) * 1e3, math.Round(stop/1e3) * 1e3
}

// Average returns the element-wise mean of complex sweeps of equal length.
func Average(sweeps [][]complex128) []complex128 {
	if len(sweeps) == 0 {
		return nil
	}
	out := make([]complex128, len(sweeps[0]))
	for _, s := range sweeps {
		for i := range out {
			out[i] += s[i]
		}
	}
	k := complex(float64(len(sweeps)), 0)
	for i := range out {
		out[i] /= k
	}
	return out
}
