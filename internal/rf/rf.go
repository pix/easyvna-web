// Package rf contains the reflection/transmission math used to turn raw
// S-parameters from a VNA into antenna figures of merit (SWR, return loss,
// impedance, bandwidth, ...).
package rf

import (
	"math"
	"math/cmplx"
)

// Z0 is the reference impedance (ohms) used throughout.
const Z0 = 50.0

// Display caps keep values finite so they can be JSON-encoded and plotted.
const (
	MaxSWR   = 999.0 // SWR reported for |Γ| >= ~1
	MaxRLdB  = 120.0 // return loss reported for |Γ| == 0
	MinDB    = -150.0
	MaxOhms  = 1e6
	speedOfC = 299792458.0
)

// Mag returns |Γ|.
func Mag(g complex128) float64 { return cmplx.Abs(g) }

// SWR returns the voltage standing wave ratio for reflection coefficient g,
// capped at MaxSWR.
func SWR(g complex128) float64 {
	m := cmplx.Abs(g)
	if m >= 1 {
		return MaxSWR
	}
	s := (1 + m) / (1 - m)
	if s > MaxSWR || math.IsNaN(s) {
		return MaxSWR
	}
	return s
}

// GammaFromSWR is the inverse of SWR: |Γ| = (s-1)/(s+1).
func GammaFromSWR(s float64) float64 { return (s - 1) / (s + 1) }

// ReturnLoss returns the return loss in dB as a positive number
// (RL = -20·log10|Γ|), capped at MaxRLdB.
func ReturnLoss(g complex128) float64 {
	m := cmplx.Abs(g)
	if m <= 0 {
		return MaxRLdB
	}
	rl := -20 * math.Log10(m)
	if rl > MaxRLdB {
		return MaxRLdB
	}
	return rl
}

// DB returns 20·log10|s|, floored at MinDB.
func DB(s complex128) float64 {
	m := cmplx.Abs(s)
	if m <= 0 {
		return MinDB
	}
	d := 20 * math.Log10(m)
	if d < MinDB {
		return MinDB
	}
	return d
}

// PhaseDeg returns the phase of s in degrees (-180, 180].
func PhaseDeg(s complex128) float64 { return cmplx.Phase(s) * 180 / math.Pi }

// Impedance converts a reflection coefficient to impedance Z = Z0(1+Γ)/(1-Γ).
// Results are clamped to ±MaxOhms so an ideal open stays finite.
func Impedance(g complex128) complex128 {
	d := 1 - g
	if cmplx.Abs(d) < 1e-12 {
		return complex(MaxOhms, 0)
	}
	z := complex(Z0, 0) * (1 + g) / d
	return complex(clamp(real(z), -MaxOhms, MaxOhms), clamp(imag(z), -MaxOhms, MaxOhms))
}

// GammaFromZ converts impedance to reflection coefficient Γ = (Z-Z0)/(Z+Z0).
func GammaFromZ(z complex128) complex128 {
	return (z - complex(Z0, 0)) / (z + complex(Z0, 0))
}

// SeriesLC returns the series-equivalent inductance (H) for positive X or
// capacitance (F) for negative X at frequency f. Exactly one of them is
// non-zero (both zero when X == 0).
func SeriesLC(x, f float64) (l, c float64) {
	w := 2 * math.Pi * f
	switch {
	case x > 0:
		return x / w, 0
	case x < 0:
		return 0, -1 / (w * x)
	}
	return 0, 0
}

// PortExtension de-embeds a lossless line of one-way delay tau (seconds)
// from a reflection measurement taken at frequency f: Γ' = Γ·e^{+j2ωτ}.
// A positive tau moves the reference plane away from the VNA, towards the
// antenna (e.g. the end of a pigtail).
func PortExtension(g complex128, f, tau float64) complex128 {
	if tau == 0 {
		return g
	}
	return g * cmplx.Rect(1, 2*2*math.Pi*f*tau)
}

// TransmissionExtension removes a one-way delay tau from a transmission
// measurement: S21' = S21·e^{+jωτ}.
func TransmissionExtension(s complex128, f, tau float64) complex128 {
	if tau == 0 {
		return s
	}
	return s * cmplx.Rect(1, 2*math.Pi*f*tau)
}

// CableDelay returns the one-way delay (s) of a cable of the given physical
// length (m) and velocity factor.
func CableDelay(lengthM, vf float64) float64 {
	if vf <= 0 {
		return 0
	}
	return lengthM / (vf * speedOfC)
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(lo, math.Min(hi, v))
}
