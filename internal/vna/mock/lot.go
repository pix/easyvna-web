package mock

import (
	"fmt"
	"sync"
)

// A lot of "identical" antennas, made or bought together, for the BATCH
// mode: every time a lot scenario is selected (SetAntenna, the Device
// panel, "Next antenna" in BATCH), the next unit of that lot is connected.
// Units differ like a real production run: the whole lot is cut slightly
// long (its resonance sits about 0.25 % low), each unit's length varies by
// about ±0.22 % (1σ), its feed resistance by ±8 % and its Q by ±7 %, and
// every seventh unit is a clear outlier (about 2 % low, a badly made one).
// The sequence is deterministic, so a demo or a test always sees the same
// lot.

// LotBands are the lot scenarios ("lot433", "lot869", "lot915").
var LotBands = []string{"433", "869", "915"}

// Lot model parameters.
const (
	LotBias                       = -0.0025 // relative resonance offset of the whole lot
	LotSigma                      = 0.0022  // 1σ unit-to-unit relative resonance spread
	LotOutlier                    = -0.02   // relative offset of an outlier unit
	lotOutlierEvery, lotOutlierAt = 7, 3
)

var lotNext = struct {
	sync.Mutex
	k map[string]int
}{k: map[string]int{}}

// nextLotUnit returns the next unit number (1, 2, …) of a lot.
func nextLotUnit(band string) int {
	lotNext.Lock()
	defer lotNext.Unlock()
	lotNext.k[band]++
	return lotNext.k[band]
}

// lotRand is a deterministic pseudo-random number in [-1, 1] for (k, salt).
func lotRand(k, salt int) float64 {
	x := uint64(k)*0x9E3779B97F4A7C15 + uint64(salt)*0xD1B54A32D192ED03
	x ^= x >> 31
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 29
	x *= 0x94D049BB133111EB
	x ^= x >> 32
	return float64(x%200001)/100000 - 1
}

// lotGauss is roughly normal with σ = 1 (sum of three uniforms).
func lotGauss(k, salt int) float64 {
	// Each uniform has σ = 1/√3, so the sum has σ = 1.
	return lotRand(k, salt) + lotRand(k, salt+101) + lotRand(k, salt+202)
}

// LotOffset is the relative resonance offset of unit k of a lot.
func LotOffset(k int) float64 {
	if k%lotOutlierEvery == lotOutlierAt {
		return LotOutlier + 0.002*lotRand(k, 7)
	}
	return LotBias + LotSigma*lotGauss(k, 1)
}

// LotAntenna returns unit k (1-based) of the lot for a LoRa band.
func LotAntenna(band string, k int) Antenna {
	f, r, q, f2 := 869e6, 46.0, 13.0, 2.6e9
	switch band {
	case "433":
		f, r, q, f2 = 433.92e6, 40, 20, 1.32e9
	case "915":
		f, r, q, f2 = 915e6, 47, 12, 2.75e9
	}
	return Antenna{
		Name: fmt.Sprintf("%s whip from a lot, unit %d", band, k),
		Modes: []Mode{{F0: f * (1 + LotOffset(k)), R: r * (1 + 0.08*lotRand(k, 3)), Q: q * (1 + 0.07*lotRand(k, 4))},
			{F0: f2, R: 90, Q: 10}},
		CableLen: 0.10, CableVF: 0.695, CableLossDB: 0.08, Drift: 0.0004, S21Coupling: 0.03,
	}
}

func init() {
	for _, b := range LotBands {
		b := b
		Scenarios["lot"+b] = func() Antenna { return LotAntenna(b, nextLotUnit(b)) }
	}
}
