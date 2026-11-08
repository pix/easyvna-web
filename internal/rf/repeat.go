package rf

import (
	"math"
	"math/cmplx"
)

// ---------- several measurements of one antenna ----------
//
// Measure ×N takes N fresh sweeps of one antenna and keeps their complex
// (vector) average of S11 per point, as AVG does (Average): averaging Γ
// rather than SWR keeps the phase, so noise averages out instead of
// biasing the SWR upwards. Repeatability says how much the N sweeps
// differ: the standard deviation of the resonance and of the SWR at the
// target over the single sweeps, the largest |ΔΓ| between any two sweeps
// at any point, and the per-point SWR min/max envelope. In "re-seat" mode
// the antenna is unscrewed and reconnected (or moved) between sweeps, so
// the spread includes the connector and placement, not only the VNA's
// noise.

// Repeat modes.
const (
	RepeatStill  = "still"  // N sweeps in a row, the antenna untouched
	RepeatReseat = "reseat" // reconnected / moved between sweeps
)

// Repeat is the repeatability of N sweeps of one antenna.
type Repeat struct {
	N         int       `json:"n"`
	Mode      string    `json:"mode"`      // still, reseat
	Target    float64   `json:"target"`    // Hz the SWR and resonance are taken at / near
	ResN      int       `json:"resN"`      // sweeps where a resonance was found
	ResMean   float64   `json:"resMean"`   // Hz
	ResSD     float64   `json:"resSD"`     // Hz, σ of the resonance over the sweeps
	ResMin    float64   `json:"resMin"`    // Hz
	ResMax    float64   `json:"resMax"`    // Hz
	SWRMean   float64   `json:"swrMean"`   // SWR at the target, mean of the sweeps
	SWRSD     float64   `json:"swrSD"`     // σ of the SWR at the target
	MaxDGamma float64   `json:"maxDGamma"` // largest |Γi − Γj| at any point, any two sweeps
	RMSNoise  float64   `json:"rmsNoise"`  // RMS |ΔΓ| between consecutive sweeps (HOLD's motion measure)
	EnvMin    []float64 `json:"envMin,omitempty"`
	EnvMax    []float64 `json:"envMax,omitempty"`
}

// Repeatability averages sweeps (equal length, on grid freq) and measures
// how much they differ. target is the band reference (0: the best match of
// the average).
func Repeatability(freq []float64, sweeps [][]complex128, target float64, mode string) ([]complex128, Repeat) {
	r := Repeat{N: len(sweeps), Mode: mode, Target: target}
	if len(sweeps) == 0 || len(freq) < 3 {
		return nil, r
	}
	n := len(freq)
	for _, s := range sweeps {
		if len(s) != n {
			return nil, r
		}
	}
	avg := Average(sweeps)
	if r.Target <= 0 {
		d := Derive(avg, nil)
		best := 0
		for i, v := range d.SWR {
			if v < d.SWR[best] {
				best = i
			}
		}
		r.Target = freq[best]
	}
	r.EnvMin, r.EnvMax = make([]float64, n), make([]float64, n)
	var res, swr []float64
	for k, s := range sweeps {
		d := Derive(s, nil)
		for i, v := range d.SWR {
			if k == 0 || v < r.EnvMin[i] {
				r.EnvMin[i] = v
			}
			if k == 0 || v > r.EnvMax[i] {
				r.EnvMax[i] = v
			}
		}
		ti := Tune(freq, d, r.Target)
		if ti.Found {
			res = append(res, ti.ResFreq)
		}
		swr = append(swr, ti.SWRAtTarget)
	}
	rs, ss := Describe(res), Describe(swr)
	r.ResN, r.ResMean, r.ResSD, r.ResMin, r.ResMax = rs.N, rs.Mean, rs.SD, rs.Min, rs.Max
	r.SWRMean, r.SWRSD = ss.Mean, ss.SD
	sum, k := 0.0, 0
	for a := 0; a < len(sweeps); a++ {
		for b := a + 1; b < len(sweeps); b++ {
			for i := range sweeps[a] {
				r.MaxDGamma = math.Max(r.MaxDGamma, cmplx.Abs(sweeps[a][i]-sweeps[b][i]))
			}
		}
		if a > 0 {
			d := rmsDiff(sweeps[a], sweeps[a-1])
			sum += d * d
			k++
		}
	}
	if k > 0 {
		r.RMSNoise = math.Sqrt(sum / float64(k))
	}
	return avg, r
}

// PooledSD is the pooled within-group standard deviation: the square root
// of the mean variance, weighted by the degrees of freedom (n − 1).
func PooledSD(sd []float64, n []int) float64 {
	num, den := 0.0, 0
	for i, s := range sd {
		if i < len(n) && n[i] >= 2 && finite(s) {
			num += float64(n[i]-1) * s * s
			den += n[i] - 1
		}
	}
	if den == 0 {
		return 0
	}
	return math.Sqrt(num / float64(den))
}

// ---------- re-seat detection ----------

// Reseat waits, between two measurements of a "re-seat" Measure ×N, for
// the antenna to be disturbed (the cable seen open, or the trace jumped
// as with HOLD's motion detector) and then to be steady again for Cfg.N
// sweeps. Unlike Arrival it does not ask for a different reading: the
// same antenna comes back.
type Reseat struct {
	Cfg      HoldConfig
	departed bool
	prev     []complex128
	run      int
}

// Reseat statuses.
const (
	ReseatWaiting  = "waiting"   // not disturbed yet: reconnect or move the antenna
	ReseatOpen     = "noantenna" // open cable seen
	ReseatMoving   = "moving"
	ReseatSettling = "settling"
	ReseatReady    = "ready" // steady again: take the next measurement
)

// NewReseat creates the detector (HOLD's N and threshold).
func NewReseat(cfg HoldConfig) *Reseat {
	if cfg.N < 2 {
		cfg.N = 2
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = HoldSensitivity["normal"]
	}
	return &Reseat{Cfg: cfg}
}

// Add feeds one sweep; antenna is false for an open cable. It returns the
// status and the length of the current steady run.
func (r *Reseat) Add(s11 []complex128, antenna bool) (string, int) {
	cur := append([]complex128(nil), s11...)
	if !antenna {
		r.departed, r.prev, r.run = true, nil, 0
		return ReseatOpen, 0
	}
	if r.prev == nil || len(r.prev) != len(cur) {
		r.prev, r.run = cur, 1
		if r.departed {
			return ReseatSettling, 1
		}
		return ReseatWaiting, 1
	}
	d := rmsDiff(cur, r.prev)
	r.prev = cur
	if d > r.Cfg.Threshold {
		r.departed, r.run = true, 1
		return ReseatMoving, 1
	}
	r.run++
	switch {
	case !r.departed:
		return ReseatWaiting, r.run
	case r.run >= r.Cfg.N:
		return ReseatReady, r.run
	}
	return ReseatSettling, r.run
}
