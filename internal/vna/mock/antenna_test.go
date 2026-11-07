package mock

import (
	"math"
	"testing"
)

func TestBendHasStillPositionsAndMotionBursts(t *testing.T) {
	a := Scenarios["bend"]()
	// Inside a still phase the offset is constant; positions differ between periods.
	still := func(k int) float64 { return a.bendOffset(float64(k)*a.BendPeriod + 3) }
	for k := 1; k < 6; k++ {
		if a.bendOffset(float64(k)*a.BendPeriod+2.0) != a.bendOffset(float64(k)*a.BendPeriod+5.9) {
			t.Fatalf("period %d not still", k)
		}
		if math.Abs(still(k)) > a.Bend {
			t.Fatalf("offset out of range")
		}
	}
	distinct := 0
	for k := 1; k < 6; k++ {
		if math.Abs(still(k)-still(k-1)) > 0.1*a.Bend {
			distinct++
		}
	}
	if distinct < 3 {
		t.Errorf("only %d distinct positions", distinct)
	}
	// During a motion burst consecutive sweeps (0.2 s apart) differ.
	moved := 0
	for ph := 0.0; ph < 1.4; ph += 0.2 {
		if a.bendOffset(6*7+ph) != a.bendOffset(6*7+ph+0.2) {
			moved++
		}
	}
	if moved < 6 {
		t.Errorf("motion burst too quiet: %d", moved)
	}
	if (Antenna{}).bendOffset(3) != 0 {
		t.Error("no bend configured but offset != 0")
	}
}
