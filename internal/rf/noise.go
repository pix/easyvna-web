package rf

import (
	"fmt"
	"math"
	"math/cmplx"
	"sort"
)

// Measurement quality: noise and calibration consistency.
//
// A good NanoVNA-H4 sweep repeats to about 0.0016 RMS |ΔΓ| from one sweep to
// the next (see HoldConfig). A loose connector, a damaged cable or a bad
// USB supply makes it jitter much more. Two estimates are used, both in
// that unit ("equivalent RMS |ΔΓ| between two sweeps") so they share the
// HOLD threshold:
//
//   - within one sweep, the point-to-point jitter around a smooth local
//     fit: third differences Γ[i] − 3Γ[i+1] + 3Γ[i+2] − Γ[i+3] cancel any
//     locally quadratic trace, so only the random part is left; the median
//     ignores the few points where a sharp resonance bends the trace;
//   - between two consecutive sweeps, the same estimate on their
//     difference, so a smooth change (antenna moved, bent, trimmed) does
//     not count, only random jitter does.

// NoiseThreshold is the noise above which a measurement is called noisy:
// the HOLD "normal" motion threshold (≈ 4× real H4 noise).
var NoiseThreshold = HoldSensitivity["normal"]

// rayleighMedian is the median of |z| for a complex Gaussian with unit
// standard deviation per component: sqrt(2 ln 2).
const rayleighMedian = 1.1774100225154747

// sigmaFromThirdDiff estimates the per-component noise σ of g.
func sigmaFromThirdDiff(g []complex128) float64 {
	n := len(g)
	if n < 8 {
		return 0
	}
	d := make([]float64, 0, n-3)
	for i := 0; i+3 < n; i++ {
		d = append(d, cmplx.Abs(g[i]-3*g[i+1]+3*g[i+2]-g[i+3]))
	}
	sort.Float64s(d)
	med := d[len(d)/2]
	if len(d)%2 == 0 {
		med = (d[len(d)/2-1] + d[len(d)/2]) / 2
	}
	return med / (rayleighMedian * math.Sqrt(20))
}

// NoiseLevel estimates the random jitter of one sweep, as the RMS |ΔΓ| two
// such sweeps would show between them (0 for fewer than 8 points).
func NoiseLevel(g []complex128) float64 { return 2 * sigmaFromThirdDiff(g) }

// RepeatNoise estimates the random part of the RMS |ΔΓ| between two sweeps
// of the same grid (smooth differences removed). It returns 0 when the
// grids differ in length or are too short.
func RepeatNoise(a, b []complex128) float64 {
	if len(a) != len(b) || len(a) < 8 {
		return 0
	}
	d := make([]complex128, len(a))
	for i := range a {
		d[i] = a[i] - b[i]
	}
	// d has per-component σ√2 when a and b have σ each; RMS|a−b| = 2σ.
	return 2 * sigmaFromThirdDiff(d) / math.Sqrt2
}

// RMSDiff is the plain RMS |a−b| (smooth changes included).
func RMSDiff(a, b []complex128) float64 { return rmsDiff(a, b) }

// NoisyWarning describes a noisy measurement; level is the estimate.
func NoisyWarning(level float64, between bool) Warning {
	how := "The trace jitters from point to point"
	if between {
		how = "Consecutive sweeps differ randomly"
	}
	return Warning{Code: "noisy", Level: "warn", Title: "Noisy measurement",
		Text: fmt.Sprintf("%s (about %.3f RMS |ΔΓ|, %.0f× a NanoVNA's normal noise): check the connector and cable, then recalibrate.",
			how, level, level/0.0016)}
}

// gammaOf rebuilds Γ from the derived magnitude and phase.
func gammaOf(d Derived) []complex128 {
	g := make([]complex128, len(d.GammaMag))
	for i := range g {
		ph := 0.0
		if i < len(d.S11Phase) {
			ph = d.S11Phase[i] * math.Pi / 180
		}
		g[i] = cmplx.Rect(d.GammaMag[i], ph)
	}
	return g
}

// Calibration consistency: |Γ| > 1 means the load returns more power than
// it receives, which no antenna does; over a noticeable span it means the
// calibration does not fit the setup.
const (
	calOverGamma   = 1.02 // above noise and small cal errors
	calOverSpanPct = 2.0  // % of the sweep span
	calOverPoints  = 3
)

// overUnityRun returns the longest run of points with |Γ| > calOverGamma.
func overUnityRun(freq []float64, d Derived) (lo, hi float64, n int) {
	start := -1
	for i := 0; i <= len(freq); i++ {
		over := i < len(freq) && d.GammaMag[i] > calOverGamma
		switch {
		case over && start < 0:
			start = i
		case !over && start >= 0:
			if i-start > n {
				lo, hi, n = freq[start], freq[i-1], i-start
			}
			start = -1
		}
	}
	return lo, hi, n
}
