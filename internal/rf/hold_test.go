package rf

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// holdSweep is a series-RLC antenna at f0 with a little measurement noise.
func holdSweep(rng *rand.Rand, f0 float64) ([]float64, []complex128) {
	freq, s11 := seriesRLC(48, f0, 14, 779e6, 959e6, 181)
	for i := range s11 {
		s11[i] += complex(rng.NormFloat64()*0.002, rng.NormFloat64()*0.002)
	}
	return freq, s11
}

func feed(t *testing.T, h *Holder, rng *rand.Rand, f0 float64) HoldUpdate {
	t.Helper()
	f, s := holdSweep(rng, f0)
	return h.Add(f, s, time.Now())
}

func TestHoldCapturesStablePositionsOnly(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	h := NewHolder(DefaultHoldConfig(), Band869, 869e6)
	want := []string{HoldWaiting, HoldSettling, HoldCaptured, HoldStill, HoldStill}
	for i, w := range want {
		if u := feed(t, h, rng, 860e6); u.Status != w {
			t.Fatalf("sweep %d: %s, want %s (%+v)", i, u.Status, w, u)
		}
	}
	if len(h.Positions) != 1 || math.Abs(h.Positions[0].ResFreq-860e6) > 1e6 {
		t.Fatalf("positions: %+v", h.Positions)
	}
	// Bending: every sweep lands somewhere else -> all rejected as moving.
	for i, f := range []float64{862e6, 866e6, 871e6, 868e6} {
		if u := feed(t, h, rng, f); u.Status != HoldMoving {
			t.Fatalf("motion sweep %d: %+v", i, u)
		}
	}
	if len(h.Positions) != 1 {
		t.Fatalf("motion sweeps leaked into the envelope: %d positions", len(h.Positions))
	}
	// Settle at a new position: captured after N agreeing sweeps.
	var u HoldUpdate
	for i := 0; i < 3; i++ {
		u = feed(t, h, rng, 872e6)
	}
	if u.Status != HoldCaptured || u.Captured != 2 || len(h.Positions) != 2 {
		t.Fatalf("second position: %+v", u)
	}
	sum := h.Summary()
	if sum.Positions != 2 || math.Abs(sum.ResSpread-12e6) > 1.5e6 || sum.WorstBand <= 1 || len(sum.EnvMin) != 181 {
		t.Fatalf("summary: %+v", sum)
	}
	for i := range sum.EnvMin {
		if sum.EnvMin[i] > sum.EnvMax[i] {
			t.Fatalf("envelope inverted at %d", i)
		}
	}
}

func TestHoldSingleOutlierRestartsRun(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	h := NewHolder(DefaultHoldConfig(), Band869, 869e6)
	feed(t, h, rng, 860e6)
	feed(t, h, rng, 860e6)
	// One glitch sweep (hand bumped the cable) before the run completes.
	if u := feed(t, h, rng, 880e6); u.Status != HoldMoving {
		t.Fatalf("outlier not rejected: %+v", u)
	}
	// Back at 860: the glitch makes this sweep differ again -> moving, then
	// a fresh run is needed. No position may contain the outlier.
	feed(t, h, rng, 860e6)
	feed(t, h, rng, 860e6)
	u := feed(t, h, rng, 860e6)
	if u.Status != HoldCaptured || len(h.Positions) != 1 || math.Abs(h.Positions[0].ResFreq-860e6) > 1e6 {
		t.Fatalf("after outlier: %+v %+v", u, h.Positions)
	}
}

func TestHoldDuplicateAndSlowCreep(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	h := NewHolder(DefaultHoldConfig(), Band869, 869e6)
	for i := 0; i < 3; i++ {
		feed(t, h, rng, 860e6)
	}
	feed(t, h, rng, 875e6) // move away
	feed(t, h, rng, 860e6) // and back to the same place
	feed(t, h, rng, 860e6)
	u := feed(t, h, rng, 860e6)
	if u.Status != HoldDuplicate || u.SameAs != 1 || len(h.Positions) != 1 {
		t.Fatalf("duplicate: %+v (%d positions)", u, len(h.Positions))
	}
	// Slow creep: each step is below the threshold, but the run drifts.
	h2 := NewHolder(DefaultHoldConfig(), Band869, 869e6)
	f := 850e6
	captured := 0
	for i := 0; i < 12; i++ {
		if u := feed(t, h2, rng, f); u.Status == HoldCaptured {
			captured++
		}
		f += 0.45e6
	}
	if captured > 3 {
		t.Errorf("creeping antenna produced %d positions", captured)
	}
}

func TestHoldGridChangeResetsAndRemove(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	h := NewHolder(HoldConfig{N: 2, Threshold: 0.015}, Band869, 869e6)
	feed(t, h, rng, 860e6)
	if u := feed(t, h, rng, 860e6); u.Status != HoldCaptured {
		t.Fatalf("N=2: %+v", u)
	}
	if !h.Remove(1) || h.Remove(1) || len(h.Positions) != 0 {
		t.Fatal("remove")
	}
	freq, s11 := seriesRLC(48, 433e6, 14, 389e6, 479e6, 101)
	if u := h.Add(freq, s11, time.Now()); u.Status != HoldWaiting {
		t.Errorf("grid change did not reset: %+v", u)
	}
}
