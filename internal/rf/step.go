package rf

import (
	"fmt"
	"math"
	"math/cmplx"
	"sort"
)

// Step is an isolated jump of the reflection between two adjacent sweep
// points: the trace is smooth on both sides and moves at once. An antenna
// cannot do that; the NanoVNA does it where it changes harmonic mode
// (300 / 900 MHz on the H4). Measured on an H4: with a fresh calibration
// the 50 Ω load shows no step (|ΔΓ| 0.003) while an antenna on the same
// cable steps by |ΔΓ| 0.086 at 900 MHz, so it depends on the device under
// test and a calibration cannot remove it. A stale calibration adds one
// to the load too.
type Step struct {
	Index     int     `json:"index"` // first point after the jump
	Freq      float64 `json:"freq"`  // Hz, between the two points
	Jump      float64 `json:"jump"`  // |ΔΓ| of the jump
	SWRBefore float64 `json:"swrBefore"`
	SWRAfter  float64 `json:"swrAfter"`
}

const (
	stepMinJump   = 0.025 // |ΔΓ| below this is not worth a warning
	stepFactor    = 8.0   // jump ≥ this × the median point-to-point change
	stepIsolation = 0.35  // both neighbouring changes ≤ this × the jump
)

// FindSteps returns the isolated jumps of a sweep.
func FindSteps(freq []float64, d Derived) []Step {
	n := len(freq)
	if n < 6 || len(d.GammaMag) != n || len(d.SWR) != n {
		return nil
	}
	g := gammaOf(d)
	diff := make([]float64, n) // diff[k] = |Γk − Γk−1|, diff[0] unused
	for k := 1; k < n; k++ {
		diff[k] = cmplx.Abs(g[k] - g[k-1])
	}
	sorted := append([]float64(nil), diff[1:]...)
	sort.Float64s(sorted)
	med := sorted[len(sorted)/2]
	var out []Step
	for k := 2; k < n-1; k++ {
		j := diff[k]
		if j < stepMinJump || j < stepFactor*med || diff[k-1] > stepIsolation*j || diff[k+1] > stepIsolation*j {
			continue
		}
		out = append(out, Step{Index: k, Freq: (freq[k-1] + freq[k]) / 2, Jump: j, SWRBefore: d.SWR[k-1], SWRAfter: d.SWR[k]})
	}
	return out
}

// destepRL returns the return loss of the sweep with each step removed
// (the trace after a jump is moved back by the jump minus the local slope),
// so that a step does not show up as a resonance dip. nil: no step.
func destepRL(d Derived, steps []Step) []float64 {
	if len(steps) == 0 {
		return nil
	}
	g := gammaOf(d)
	for _, s := range steps {
		k := s.Index
		slope := (g[k-1] - g[k-2] + g[k+1] - g[k]) / 2
		delta := g[k] - g[k-1] - slope
		for j := k; j < len(g); j++ {
			g[j] -= delta
		}
	}
	rl := make([]float64, len(g))
	for i, v := range g {
		rl[i] = ReturnLoss(v)
	}
	return rl
}

// StepWarning explains the steps of a sweep (nil: none). It is
// informational: the rest of the trace is fine, and resonance dips ignore
// the steps.
func StepWarning(steps []Step) *Warning {
	if len(steps) == 0 {
		return nil
	}
	s := steps[0]
	where := fmt.Sprintf("%.1f MHz", s.Freq/1e6)
	if len(steps) > 1 {
		where += fmt.Sprintf(" (and %d more)", len(steps)-1)
	}
	why := "an instrument artefact"
	for _, sw := range []float64{300e6, 900e6, 1500e6} {
		if math.Abs(s.Freq-sw) < 0.02*sw {
			why = fmt.Sprintf("the NanoVNA changes harmonic mode at %.0f MHz", sw/1e6)
		}
	}
	return &Warning{Code: "step", Level: "info", Title: "Step in the trace at " + where,
		Text: fmt.Sprintf("SWR jumps from %s to %s between two points: %s. In harmonic mode the H4 really generates a third or a fifth of the frequency, and the antenna reflects that part too, unlike the calibration load: this most likely leaves the step, which a calibration cannot remove. It is an instrument effect, not the antenna; compare values across it with care. The resonance search ignores it. If the step also shows with the 50 Ω load connected, the calibration is stale: recalibrate this range.",
			swrStr(s.SWRBefore), swrStr(s.SWRAfter), why)}
}

func swrStr(v float64) string {
	if v >= 99 {
		return ">99"
	}
	return fmt.Sprintf("%.2f", v)
}

// Suspect reports whether a warning says the measurement cannot be trusted
// (informational ones, like a step, do not).
func Suspect(ws []Warning) bool {
	for _, w := range ws {
		if w.Level != "info" {
			return true
		}
	}
	return false
}
