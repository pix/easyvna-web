package rf

import (
	"math"
	"math/cmplx"
	"testing"
)

func TestDescribe(t *testing.T) {
	st := Describe([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if st.N != 8 || st.Mean != 5 || math.Abs(st.SD-2.138) > 0.001 || st.Min != 2 || st.Max != 9 || st.Spread != 7 || st.Median != 4.5 || st.MAD != 0.5 {
		t.Fatalf("%+v", st)
	}
	// n = 1: no spread, SD 0 (not NaN).
	st = Describe([]float64{868.2})
	if st.N != 1 || st.Mean != 868.2 || st.SD != 0 || st.Spread != 0 || st.Median != 868.2 || st.MAD != 0 {
		t.Fatalf("n=1: %+v", st)
	}
	// n = 2.
	st = Describe([]float64{1, 3})
	if st.SD != math.Sqrt2 || st.Median != 2 || st.MAD != 1 {
		t.Fatalf("n=2: %+v", st)
	}
	// Identical values.
	st = Describe([]float64{1.5, 1.5, 1.5})
	if st.SD != 0 || st.Spread != 0 || st.MAD != 0 || st.Mean != 1.5 {
		t.Fatalf("identical: %+v", st)
	}
	// Empty and non-finite input: zeros, never NaN.
	for _, x := range [][]float64{nil, {}, {math.NaN(), math.Inf(1)}} {
		st = Describe(x)
		if st != (Stat{}) {
			t.Fatalf("%v: %+v", x, st)
		}
	}
	st = Describe([]float64{1, math.NaN(), 3, math.Inf(-1)})
	if st.N != 2 || st.Mean != 2 || math.IsNaN(st.SD) {
		t.Fatalf("mixed: %+v", st)
	}
}

func TestOutliers(t *testing.T) {
	flags := func(x []float64, floor float64) []int {
		var o []int
		for i, f := range Outliers(x, floor) {
			if f {
				o = append(o, i)
			}
		}
		return o
	}
	eq := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	// A lot of resonances (MHz) with one badly made antenna.
	lot := []float64{866.1, 867.4, 865.8, 866.9, 851.2, 866.4, 867.0}
	if got := flags(lot, 0.87); !eq(got, []int{4}) {
		t.Fatalf("lot: %v", got)
	}
	// The same with a mean/σ rule would miss it at n = 5: one bad value
	// inflates σ. The MAD rule still finds it.
	five := []float64{866.1, 867.4, 865.8, 866.9, 851.2}
	st := Describe(five)
	if math.Abs(851.2-st.Mean) > 3*st.SD == true {
		t.Fatal("expected the 3σ rule to miss it (test premise)")
	}
	if got := flags(five, 0.87); !eq(got, []int{4}) {
		t.Fatalf("five: %v", got)
	}
	// n = 1 and n = 2: never flagged.
	if got := flags([]float64{900}, 0); got != nil {
		t.Fatal(got)
	}
	if got := flags([]float64{868, 900}, 0); got != nil {
		t.Fatal(got)
	}
	// n = 3 with a far value.
	if got := flags([]float64{868, 869, 880}, 0.87); !eq(got, []int{2}) {
		t.Fatalf("n=3: %v", got)
	}
	// Identical values (MAD 0): only differences larger than 3.5 × floor count.
	if got := flags([]float64{1.2, 1.2, 1.2, 1.2}, 0.05); got != nil {
		t.Fatal(got)
	}
	if got := flags([]float64{1.2, 1.2, 1.2, 1.25}, 0.05); got != nil {
		t.Fatalf("tiny difference flagged: %v", got)
	}
	if got := flags([]float64{1.2, 1.2, 1.2, 1.6}, 0.05); !eq(got, []int{3}) {
		t.Fatalf("MAD 0: %v", got)
	}
	// Zero floor and identical values: nothing (no division by zero).
	if got := flags([]float64{3, 3, 3}, 0); got != nil {
		t.Fatal(got)
	}
	// Non-finite values are skipped and never flagged.
	if got := flags([]float64{868, math.NaN(), 869, 868.5, 880}, 0.87); !eq(got, []int{4}) {
		t.Fatalf("NaN: %v", got)
	}
	// A consistent lot has no outliers.
	if got := flags([]float64{866.1, 867.4, 865.8, 866.9, 866.4, 867.0}, 0.87); got != nil {
		t.Fatalf("consistent: %v", got)
	}
}

// arrival test fixture: a series-RLC antenna with resonance f0 on a grid.
func rlcSweep(freq []float64, f0, noise float64, seed *uint64) []complex128 {
	out := make([]complex128, len(freq))
	for i, f := range freq {
		x := 50 * 13 * (f/f0 - f0/f)
		z := complex(48, x)
		g := (z - 50) / (z + 50)
		if noise > 0 {
			*seed = *seed*6364136223846793005 + 1442695040888963407
			a := float64(*seed>>11)/float64(1<<53) - 0.5
			*seed = *seed*6364136223846793005 + 1442695040888963407
			b := float64(*seed>>11)/float64(1<<53) - 0.5
			g += complex(noise*a, noise*b)
		}
		out[i] = g
	}
	return out
}

func openSweep(freq []float64) []complex128 {
	out := make([]complex128, len(freq))
	for i, f := range freq {
		out[i] = cmplx.Rect(0.99, -f/1e8)
	}
	return out
}

func TestArrival(t *testing.T) {
	freq := make([]float64, 201)
	for i := range freq {
		freq[i] = 779e6 + 180e6*float64(i)/200
	}
	seed := uint64(1)
	cfg := DefaultHoldConfig()
	a := NewArrival(cfg, nil)
	feed := func(f0 float64) ArrivalUpdate { return a.Add(freq, rlcSweep(freq, f0, 0.002, &seed), true) }
	steady := func(f0 float64, n int) []string {
		var st []string
		for i := 0; i < n; i++ {
			st = append(st, feed(f0).Status)
		}
		return st
	}
	// Empty batch: the first steady antenna is ready after N sweeps.
	st := steady(866e6, 3)
	if st[0] != ArrWaiting || st[1] != ArrSettling || st[2] != ArrReady {
		t.Fatalf("first: %v", st)
	}
	u := a.Add(freq, rlcSweep(freq, 866e6, 0.002, &seed), true)
	if u.Status != ArrStill {
		t.Fatalf("not captured yet, decided once: %v", u.Status)
	}
	a.Captured(rlcSweep(freq, 866e6, 0, &seed))
	if st := steady(866e6, 5); st[4] != ArrStill {
		t.Fatalf("after capture: %v", st)
	}
	// A hand touches it (big jump), then lets go: same antenna, not ready.
	if u := feed(855e6); u.Status != ArrMoving {
		t.Fatalf("jump: %v", u.Status)
	}
	st = steady(866e6, 4)
	if st[len(st)-1] != ArrSame && st[len(st)-2] != ArrSame {
		t.Fatalf("hand: %v", st)
	}
	for _, s := range st {
		if s == ArrReady {
			t.Fatalf("hand effect captured: %v", st)
		}
	}
	// Slow drift without a jump never captures.
	f := 866e6
	for i := 0; i < 60; i++ {
		f += 0.05e6
		if u := feed(f); u.Status == ArrReady {
			t.Fatalf("drift captured at %.2f MHz", f/1e6)
		}
	}
	// Swap: the cable is open for a moment, then the next antenna (even an
	// identical one) is captured once steady.
	a.Captured(rlcSweep(freq, f, 0, &seed))
	for i := 0; i < 3; i++ {
		if u := a.Add(freq, openSweep(freq), false); u.Status != ArrNoAntenna {
			t.Fatal(u.Status)
		}
	}
	st = steady(f, 4)
	if st[2] != ArrReady && st[3] != ArrReady {
		t.Fatalf("identical antenna after a swap: %v", st)
	}
	a.Captured(rlcSweep(freq, f, 0, &seed))
	// A swap seen only as a jump (no open state): captured when the new
	// antenna differs enough.
	feed(880e6)
	st = steady(874e6, 4)
	ready := false
	for _, s := range st {
		ready = ready || s == ArrReady
	}
	if !ready {
		t.Fatalf("different antenna after a jump: %v", st)
	}
	// Switching auto-capture on with the last captured antenna still
	// connected does not capture it twice.
	b := NewArrival(cfg, rlcSweep(freq, 874e6, 0, &seed))
	for i := 0; i < 5; i++ {
		if u := b.Add(freq, rlcSweep(freq, 874e6, 0.002, &seed), true); u.Status == ArrReady {
			t.Fatal("last capture captured again")
		}
	}
	// A grid change resets the run.
	g2 := freq[:101]
	if u := b.Add(g2, rlcSweep(g2, 874e6, 0, &seed), true); u.Status != ArrWaiting {
		t.Fatal(u.Status)
	}
}
