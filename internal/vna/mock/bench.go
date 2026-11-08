package mock

import (
	"fmt"
	"sort"
	"sync"
)

// Bench is the simulated test bench behind `-mock` (and the browser
// simulator): the current antenna scenario, how much of its element has
// been trimmed, and the simulator that is currently open.
type Bench struct {
	mu        sync.Mutex
	current   antennaHolder
	name      string
	base      Antenna // the connected antenna before trimming (one unit of a lot stays the same unit)
	cutTotal  float64
	maxPoints int
	profiles  bool
}

// antennaHolder is a simulator whose antenna can be swapped (Sim, SimV2).
type antennaHolder interface{ SetAntenna(Antenna) }

// The simulated element: f0 = k/(L+δ) with L = 82 mm and δ = 6 mm end
// effect, so the plain ratio formula is a bit off and the trim calculator
// has something to learn.
const benchLen, benchDelta = 82.0, 6.0

// NewBench creates a bench with the named antenna scenario. profiles
// selects the EasyVNA firmware (the `profile` command) instead of stock
// NanoVNA-D; maxPoints 0 keeps the simulator default.
func NewBench(antenna string, maxPoints int, profiles bool) (*Bench, error) {
	if _, ok := Scenarios[antenna]; !ok {
		return nil, fmt.Errorf("unknown mock antenna %q", antenna)
	}
	return &Bench{name: antenna, base: Scenarios[antenna](), maxPoints: maxPoints, profiles: profiles}, nil
}

// ScenarioNames lists the antenna scenarios, sorted.
func ScenarioNames() []string {
	var n []string
	for k := range Scenarios {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Antenna returns the current scenario name.
func (b *Bench) Antenna() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.name
}

// SetAntenna swaps the antenna (and forgets any trimming).
func (b *Bench) SetAntenna(name string) error {
	mkNew, ok := Scenarios[name]
	if !ok {
		return fmt.Errorf("unknown mock antenna %q", name)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.name, b.base, b.cutTotal = name, mkNew(), 0
	if b.current != nil {
		b.current.SetAntenna(b.base)
	}
	return nil
}

// Cut shortens the simulated element by mm, which raises its resonance.
func (b *Bench) Cut(mm float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cutTotal += mm
	if b.current != nil {
		b.current.SetAntenna(b.antennaLocked())
	}
}

// AttachStandard puts an ideal calibration standard ("open", "short",
// "load", "thru", "isoln") at the end of the pigtail instead of the
// antenna, as the user does during a calibration; "" puts the antenna
// back. Only the V2 simulator needs it: its data are raw and calibrated by
// EasyVNA Web, while the simulated H4 firmware calibrates by itself.
func (b *Bench) AttachStandard(std string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.antennaLocked()
	if std != "" {
		a = Antenna{Name: "standard " + std, Std: std, CableLen: a.CableLen, CableVF: a.CableVF, CableLossDB: a.CableLossDB}
	}
	if b.current != nil {
		b.current.SetAntenna(a)
	}
}

// antennaLocked is the current scenario with the cuts applied.
func (b *Bench) antennaLocked() Antenna {
	a := b.base
	a.Modes = append([]Mode(nil), a.Modes...)
	if len(a.Modes) > 0 && b.cutTotal != 0 {
		k := a.Modes[0].F0 * (benchLen + benchDelta)
		a.Modes[0].F0 = k / (benchLen - b.cutTotal + benchDelta)
	}
	return a
}

// NewSim powers on a fresh simulator with the current antenna (as when the
// device is (re)connected) and makes it the one Cut and SetAntenna act on.
func (b *Bench) NewSim() *Sim {
	b.mu.Lock()
	defer b.mu.Unlock()
	sim := New(Options{Antenna: b.base, MaxPoints: b.maxPoints, Profiles: b.profiles})
	b.current = sim
	return sim
}

// NewSimV2 powers on a simulated NanoVNA V2 (binary protocol) with the
// current antenna, trimmed as far as it has been cut.
func (b *Bench) NewSimV2() *SimV2 {
	b.mu.Lock()
	defer b.mu.Unlock()
	sim := NewV2(V2Options{Antenna: b.antennaLocked()})
	b.current = sim
	return sim
}
