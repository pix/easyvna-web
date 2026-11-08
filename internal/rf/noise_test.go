package rf

import (
	"math/rand/v2"
	"testing"
)

func addNoise(g []complex128, sd float64, seed uint64) []complex128 {
	rng := rand.New(rand.NewPCG(seed, 1))
	out := make([]complex128, len(g))
	for i, v := range g {
		out[i] = v + complex(rng.NormFloat64()*sd, rng.NormFloat64()*sd)
	}
	return out
}

func codes(ws []Warning) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w.Code] = true
	}
	return m
}

func TestNoiseLevelEstimates(t *testing.T) {
	freq, s11 := seriesRLC(45, 868e6, 14, 779e6, 959e6, 401)
	// Real H4 noise (0.0008 per component) reads as ~0.0016 RMS |ΔΓ|.
	clean := addNoise(s11, 0.0008, 1)
	if v := NoiseLevel(clean); v < 0.0012 || v > 0.0021 {
		t.Errorf("clean noise level %.4f, want ≈0.0016", v)
	}
	if w := Detect(freq, Derive(clean, nil)); len(w) != 0 {
		t.Errorf("clean sweep warned: %+v", w)
	}
	// A bad connector: five times the noise.
	noisy := addNoise(s11, 0.004, 2)
	if v := NoiseLevel(noisy); v < 0.006 || v > 0.011 {
		t.Errorf("noisy level %.4f, want ≈0.008", v)
	}
	if !codes(Detect(freq, Derive(noisy, nil)))["noisy"] {
		t.Error("noisy sweep not flagged")
	}
	// No noise at all, and sharp resonances on coarse grids, read as clean.
	if v := NoiseLevel(s11); v > 1e-4 {
		t.Errorf("noise-free level %.5f", v)
	}
	for _, c := range []struct {
		f0, q, start, stop float64
		n                  int
	}{{429.5e6, 28, 50e6, 1500e6, 401}, {868e6, 60, 50e6, 1500e6, 101}, {868e6, 14, 300e6, 1000e6, 101}, {433.9e6, 30, 388.92e6, 478.92e6, 401}} {
		f, g := seriesRLC(40, c.f0, c.q, c.start, c.stop, c.n)
		g = addNoise(g, 0.0008, 3)
		if w := codes(Detect(f, Derive(g, nil))); w["noisy"] || w["calmismatch"] {
			t.Errorf("false warning on %+v: %v (level %.4f)", c, w, NoiseLevel(g))
		}
	}
	tf, tg := twoMode(429e6, 851e6, 401)
	if w := codes(Detect(tf, Derive(addNoise(tg, 0.0008, 4), nil))); w["noisy"] {
		t.Errorf("two-mode wide sweep flagged noisy")
	}
}

func TestRepeatNoiseIgnoresMotion(t *testing.T) {
	_, a := seriesRLC(45, 868e6, 14, 779e6, 959e6, 401)
	_, b := seriesRLC(45, 873e6, 14, 779e6, 959e6, 401) // antenna moved: resonance +5 MHz
	a1, b1 := addNoise(a, 0.0008, 5), addNoise(b, 0.0008, 6)
	if RMSDiff(a1, b1) < 0.05 {
		t.Fatalf("test setup: the move should be large (%.3f)", RMSDiff(a1, b1))
	}
	if v := RepeatNoise(a1, b1); v > NoiseThreshold || v < 0.001 {
		t.Errorf("motion must not count as noise: %.4f", v)
	}
	if v := RepeatNoise(a1, addNoise(a, 0.0008, 7)); v < 0.0011 || v > 0.0022 {
		t.Errorf("repeat noise of a still antenna %.4f, want ≈0.0016", v)
	}
	if v := RepeatNoise(a1, addNoise(a, 0.006, 8)); v < NoiseThreshold {
		t.Errorf("a jittery sweep must count: %.4f", v)
	}
	if RepeatNoise(a1, a1[:100]) != 0 {
		t.Error("different grids")
	}
}

func TestCalibrationMismatchSpan(t *testing.T) {
	freq, s11 := seriesRLC(45, 868e6, 14, 779e6, 959e6, 401)
	g := addNoise(s11, 0.0008, 9)
	// |Γ| pushed above 1 over 10 % of the sweep (a cal for another cable).
	bad := append([]complex128(nil), g...)
	for i := 0; i < 40; i++ {
		bad[i] = g[i] / complex(cabs(g[i]), 0) * 1.06
	}
	w := Detect(freq, Derive(bad, nil))
	if !codes(w)["calmismatch"] || codes(w)["calcheck"] {
		t.Fatalf("calibration mismatch not flagged: %+v", w)
	}
	// One point above 1 is noise, not a mismatch.
	one := append([]complex128(nil), g...)
	one[0] = 1.05
	if codes(Detect(freq, Derive(one, nil)))["calmismatch"] {
		t.Error("a single point flagged")
	}
}

func cabs(c complex128) float64 { return Mag(c) }
