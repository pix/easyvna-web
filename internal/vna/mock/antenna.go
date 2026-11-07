package mock

import (
	"math"
	"math/cmplx"
)

// Mode is one series-resonant branch of the antenna model (Foster form):
// a series RLC with resonance F0 (Hz), resistance R (Ω) at resonance and
// loaded quality factor Q.
type Mode struct {
	F0 float64
	R  float64
	Q  float64
	// Flat keeps R constant (a resistor-like load instead of a radiator).
	Flat bool
}

// Antenna is a multi-mode antenna behind a coax pigtail. The modes are
// connected in parallel (their admittances add), which produces one
// series resonance per mode with anti-resonances in between, like a real
// whip with a fundamental and a higher-order mode.
type Antenna struct {
	Name  string
	Modes []Mode
	// Pigtail between VNA port and antenna.
	CableLen float64 // m
	CableVF  float64
	// CableLossDB is the one-way loss at 1 GHz (scales with sqrt(f)).
	CableLossDB float64
	// Drift slowly moves all resonances by up to ±Drift (relative), so a
	// continuous sweep visibly changes (hand effect, temperature, ...).
	Drift float64
	// S21Coupling is the peak |S21| of the simulated two-antenna link.
	S21Coupling float64
	// Open simulates an unterminated cable (no antenna).
	Open bool
	// Bend makes the resonance wander like an antenna being bent or moved
	// by hand: still positions (relative offset up to ±Bend) separated by
	// short motion bursts. BendPeriod is still time + motion time.
	Bend       float64
	BendPeriod float64 // seconds (default 6)
}

// Scenarios selectable with -mock-antenna.
var Scenarios = map[string]func() Antenna{
	// A coil-loaded 433 MHz whip cut slightly long, whose second mode lands
	// just below the 863-870 MHz band (a common sight on cheap helicals).
	"dual": func() Antenna {
		return Antenna{
			Name:     "helical 433 whip, slightly long (2nd mode near 850 MHz)",
			Modes:    []Mode{{F0: 429.5e6, R: 41, Q: 28}, {F0: 851e6, R: 58, Q: 16}},
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, Drift: 0.0015, S21Coupling: 0.03,
		}
	},
	// A quarter-wave 868 MHz whip that is slightly long (resonance below band).
	"869": func() Antenna {
		return Antenna{
			Name:     "quarter-wave 868 whip, slightly long",
			Modes:    []Mode{{F0: 856e6, R: 38, Q: 14}, {F0: 2.6e9, R: 90, Q: 10}},
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, Drift: 0.0015, S21Coupling: 0.03,
		}
	},
	// A well-tuned 868/869 MHz whip with a bit more gain.
	"869tuned": func() Antenna {
		return Antenna{
			Name:     "869 whip, tuned",
			Modes:    []Mode{{F0: 868.5e6, R: 47, Q: 13}, {F0: 2.6e9, R: 90, Q: 10}},
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, Drift: 0.0015, S21Coupling: 0.04,
		}
	},
	// A fake antenna: essentially a 47 Ω resistor in a nice housing.
	"dummy": func() Antenna {
		return Antenna{
			Name:     "lossy dummy (resistor in an antenna housing)",
			Modes:    []Mode{{F0: 700e6, R: 47, Q: 0.08, Flat: true}},
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, S21Coupling: 0.0004,
		}
	},
	// Nothing connected to the pigtail.
	"open": func() Antenna {
		return Antenna{Name: "open cable (no antenna)", Open: true,
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, S21Coupling: 0.00002}
	},
	// A whip being bent/moved by hand: settles in a new position every few
	// seconds with a burst of motion in between (exercises HOLD).
	"bend": func() Antenna {
		return Antenna{
			Name:     "869 whip being bent by hand (motion bursts)",
			Modes:    []Mode{{F0: 867e6, R: 46, Q: 13}, {F0: 2.6e9, R: 90, Q: 10}},
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, S21Coupling: 0.035,
			Bend: 0.009, BendPeriod: 6,
		}
	},
	// A well-tuned 915 MHz antenna.
	"915": func() Antenna {
		return Antenna{
			Name:     "915 MHz whip, tuned",
			Modes:    []Mode{{F0: 914e6, R: 47, Q: 12}, {F0: 2.75e9, R: 90, Q: 10}},
			CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, Drift: 0.0015, S21Coupling: 0.03,
		}
	},
}

// DefaultAntenna is the "dual" scenario.
func DefaultAntenna() Antenna { return Scenarios["dual"]() }

// Impedance of the bare antenna at frequency f, t seconds after start.
func (a Antenna) Impedance(f, t float64) complex128 {
	scale := 1 + a.Drift*math.Sin(2*math.Pi*t/23) + a.bendOffset(t)
	if a.Open {
		// A few tenths of a pF of fringing capacitance at the open end.
		return 1 / complex(1e-7, 2*math.Pi*f*0.3e-12)
	}
	w := 2 * math.Pi * f
	var y complex128
	for _, m := range a.Modes {
		w0 := 2 * math.Pi * m.F0 * scale
		l := m.Q * m.R / w0
		c := 1 / (w0 * w0 * l)
		// Radiation resistance grows roughly with f^2 around resonance.
		r := m.R * (0.2 + 0.8*(f/(m.F0*scale))*(f/(m.F0*scale)))
		if m.Flat {
			r = m.R
		}
		z := complex(r, w*l-1/(w*c))
		y += 1 / z
	}
	if y == 0 {
		return complex(1e6, 0)
	}
	return 1 / y
}

// Measure returns S11 seen at the VNA port (through the pigtail) and the
// simulated S21 of a link to a second identical antenna.
func (a Antenna) Measure(f, t float64) (s11, s21 complex128) {
	z := a.Impedance(f, t)
	g := (z - 50) / (z + 50)
	tau := 0.0
	if a.CableVF > 0 {
		tau = a.CableLen / (a.CableVF * 299792458.0)
	}
	lossDB := a.CableLossDB * math.Sqrt(f/1e9)
	att := math.Pow(10, -lossDB/20)
	w := 2 * math.Pi * f
	s11 = g * cmplx.Rect(att*att, -2*w*tau)
	mg := cmplx.Abs(g)
	s21 = cmplx.Rect(a.S21Coupling*(1-mg*mg)*att, -w*(tau+3e-9))
	return s11, s21
}

// bendOffset is the relative resonance shift of a hand-bent antenna at time
// t: each period starts with a ~1.6 s motion burst (moving to a new random
// position with some wobble), then the antenna stays still.
func (a Antenna) bendOffset(t float64) float64 {
	if a.Bend == 0 {
		return 0
	}
	period := a.BendPeriod
	if period <= 0 {
		period = 6
	}
	const motion = 1.6
	k := math.Floor(t / period)
	ph := t - k*period
	pos := func(k float64) float64 { // deterministic pseudo-random in [-1, 1]
		x := uint64(int64(k)) * 0x9E3779B97F4A7C15
		x ^= x >> 31
		x *= 0xBF58476D1CE4E5B9
		x ^= x >> 29
		return float64(x%20001)/10000 - 1
	}
	if ph >= motion {
		return a.Bend * pos(k)
	}
	u := ph / motion
	s := u * u * (3 - 2*u) // smoothstep
	wobble := 0.6 * math.Sin(math.Pi*u) * math.Sin(2*math.Pi*u*3)
	return a.Bend * (pos(k-1) + (pos(k)-pos(k-1))*s + wobble)
}
