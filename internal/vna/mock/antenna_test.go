package mock

import (
	"fmt"
	"math"
	"math/cmplx"
	"strings"
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

func TestSimNoiseLikeRealH4(t *testing.T) {
	a := Scenarios["869"]()
	a.Drift = 0
	sim := New(Options{Antenna: a, Seed: 3, PointTime: 1})
	sweep := func() []complex128 {
		var out []complex128
		_, _ = sim.Write([]byte("scan 779000000 959000000 401 3\r"))
		buf := make([]byte, 1<<16)
		var acc []byte
		for !strings.HasSuffix(string(acc), "ch> ") || !strings.Contains(string(acc), "scan 779") {
			n, _ := sim.Read(buf)
			acc = append(acc, buf[:n]...)
		}
		for _, l := range strings.Split(string(acc), "\r\n") {
			var f, re, im float64
			if n, _ := fmt.Sscanf(l, "%g %g %g", &f, &re, &im); n == 3 {
				out = append(out, complex(re, im))
			}
		}
		return out
	}
	x, y := sweep(), sweep()
	if len(x) != 401 || len(y) != 401 {
		t.Fatalf("points %d %d", len(x), len(y))
	}
	s := 0.0
	for i := range x {
		d := cmplx.Abs(x[i] - y[i])
		s += d * d
	}
	rms := math.Sqrt(s / 401)
	if rms < 0.0011 || rms > 0.0022 {
		t.Errorf("sweep-to-sweep RMS |ΔΓ| = %.4f, want ≈0.0016", rms)
	}
}
