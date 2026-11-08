package rf

import (
	"math"
	"math/cmplx"
	"strings"
	"testing"
)

func TestVerdictAtLimit(t *testing.T) {
	// The default limit keeps the historical grading.
	for _, c := range []struct {
		swr, limit float64
		want       string
	}{
		{1.49, 2, "good"}, {1.5, 2, "ok"}, {1.99, 2, "ok"}, {2, 2, "bad"}, {0, 2, "n/a"}, {math.NaN(), 2, "n/a"},
		{1.24, 1.5, "good"}, {1.25, 1.5, "ok"}, {1.49, 1.5, "ok"}, {1.5, 1.5, "bad"},
		{1.74, 2.5, "good"}, {1.75, 2.5, "ok"}, {2.49, 2.5, "ok"}, {2.5, 2.5, "bad"},
		{1.7, 0, "ok"}, // 0: the default 2
	} {
		if got := VerdictAt(c.swr, c.limit); got != c.want {
			t.Errorf("VerdictAt(%v, %v) = %s, want %s", c.swr, c.limit, got, c.want)
		}
		if c.limit == 2 && Verdict(c.swr) != c.want {
			t.Errorf("Verdict(%v) differs", c.swr)
		}
	}
	if g := (Acceptance{}).GoodLimit(); g != 1.5 {
		t.Fatal(g)
	}
	if g := (Acceptance{SWRLimit: 2.5}).GoodLimit(); g != 1.75 {
		t.Fatal(g)
	}
}

func TestAcceptanceNormalize(t *testing.T) {
	ok := []Acceptance{{}, {SWRLimit: 1.5}, {SWRLimit: 2.5, Tol: TolLoose}, {Tol: TolCustom, TolPct: 0.3}, {Tol: TolCustom, TolMHz: 2}, {Tol: TolAuto}}
	for _, a := range ok {
		n, err := a.Normalize()
		if err != nil {
			t.Fatalf("%+v: %v", a, err)
		}
		if a.SWRLimit == 2 && n.SWRLimit != 0 {
			t.Fatal("2 is stored as the default")
		}
	}
	if n, _ := (Acceptance{SWRLimit: 2, Tol: TolAuto}).Normalize(); n != (Acceptance{}) {
		t.Fatalf("default not canonical: %+v", n)
	}
	for _, a := range []Acceptance{{SWRLimit: 1.05}, {SWRLimit: 11}, {Tol: "huge"}, {Tol: TolCustom}, {Tol: TolCustom, TolPct: 30}, {SWRLimit: math.NaN()}} {
		if _, err := a.Normalize(); err == nil {
			t.Fatalf("%+v accepted", a)
		}
	}
}

func TestAcceptanceResolve(t *testing.T) {
	tgt := 869e6
	for _, c := range []struct {
		a    Acceptance
		hz   float64
		text string
	}{
		{Acceptance{Tol: TolTight}, 1.3035e6, "band SWR < 2.0, resonance ± 0.15 % (± 1.30 MHz)"},
		{Acceptance{Tol: TolNormal}, 4.345e6, "band SWR < 2.0, resonance ± 0.5 % (± 4.35 MHz)"},
		{Acceptance{SWRLimit: 1.5, Tol: TolLoose}, 8.69e6, "band SWR < 1.5, resonance ± 1.0 % (± 8.69 MHz)"},
		{Acceptance{Tol: TolCustom, TolMHz: 2}, 2e6, "band SWR < 2.0, resonance ± 0.23 % (± 2.00 MHz)"},
		{Acceptance{Tol: TolCustom, TolPct: 0.25}, 2.1725e6, "band SWR < 2.0, resonance ± 0.25 % (± 2.17 MHz)"},
	} {
		w := c.a.Resolve(tgt, nil)
		if math.Abs(w.Hz-c.hz) > 1 || w.AimHz != w.Hz || w.Text != c.text {
			t.Errorf("%+v: %+v", c.a, w)
		}
	}
	// Auto: the window from the measured margin, the aim stays ± 0.15 %.
	w := Acceptance{}.Resolve(tgt, &Margin{OK: true, Window: 16.3e6})
	if !w.AutoOK || w.Hz != 16.3e6 || math.Abs(w.AimHz-1.3035e6) > 1 || !strings.Contains(w.Text, "resonance Auto ± 16.30 MHz") {
		t.Fatalf("auto: %+v", w)
	}
	w = Acceptance{}.Resolve(tgt, nil)
	if w.AutoOK || w.Hz != 0 || !strings.Contains(w.Text, "Auto (no measured curve") {
		t.Fatalf("auto without a curve: %+v", w)
	}
	w = Acceptance{}.Resolve(tgt, &Margin{OK: true})
	if w.Hz != 0 || !strings.Contains(w.Note, "not covered") {
		t.Fatalf("auto, not covered: %+v", w)
	}
}

// synthetic SWR curve: a symmetric V around f0 reaching limit at ±half.
func vCurve(f0, half, limit float64) ([]float64, []float64) {
	var freq, swr []float64
	for f := 779e6; f <= 959e6; f += 0.45e6 {
		freq = append(freq, f)
		swr = append(swr, 1+(limit-1)*math.Abs(f-f0)/half)
	}
	return freq, swr
}

func TestBandMarginAutoWindow(t *testing.T) {
	band := Band869 // 863–870, ref 869
	// Resonance at 866.5 (band centre), SWR < 2 for ± 25 MHz: the band
	// stays covered while the resonance moves between 870−25+(869−866.5)…
	freq, swr := vCurve(866.5e6, 25e6, 2)
	m := BandMargin(freq, swr, band, 869e6, 866.5e6, 2)
	if !m.OK || !m.Covered || math.Abs(m.Lo-841.5e6) > 0.2e6 || math.Abs(m.Hi-891.5e6) > 0.2e6 {
		t.Fatalf("region %+v", m)
	}
	if math.Abs(m.RoomDown-21.5e6) > 0.2e6 || math.Abs(m.RoomUp-21.5e6) > 0.2e6 || m.UsedPct > 1 {
		t.Fatalf("rooms %+v", m)
	}
	// Offsets from 869: −2.5 − 21.5 = −24 to −2.5 + 21.5 = +19.
	if math.Abs(m.OffMin+24e6) > 0.2e6 || math.Abs(m.OffMax-19e6) > 0.2e6 || math.Abs(m.Window-19e6) > 0.2e6 {
		t.Fatalf("window %+v", m)
	}
	// The window does not depend on where this antenna resonates (its
	// dip shape is what counts).
	freq2, swr2 := vCurve(860e6, 25e6, 2)
	m2 := BandMargin(freq2, swr2, band, 869e6, 860e6, 2)
	if math.Abs(m2.Window-m.Window) > 0.5e6 || math.Abs(m2.UsedFor(860e6-869e6)-100*9/24.0) > 2 {
		t.Fatalf("shifted: %+v used %.1f", m2, m2.UsedFor(-9e6))
	}
	if m2.UsedPct < 25 || m2.UsedPct > 35 { // rooms 28 and 15: 1 − 30/43
		t.Fatalf("used %.1f", m2.UsedPct)
	}
	// A narrow dip: the band is not covered, no window.
	freq3, swr3 := vCurve(866.5e6, 3e6, 2)
	m3 := BandMargin(freq3, swr3, band, 869e6, 866.5e6, 2)
	if !m3.OK || m3.Covered || m3.Window != 0 || m3.UsedPct < 100 {
		t.Fatalf("narrow: %+v", m3)
	}
	// A dip that never reaches the limit.
	freq4, swr4 := vCurve(866.5e6, 3e6, 2)
	for i := range swr4 {
		swr4[i] += 1.5
	}
	if m4 := BandMargin(freq4, swr4, band, 869e6, 866.5e6, 2); m4.OK {
		t.Fatalf("no region: %+v", m4)
	}
	// A stricter limit narrows the window.
	m5 := BandMargin(freq, swr, band, 869e6, 866.5e6, 1.5)
	if !m5.OK || m5.Window >= m.Window {
		t.Fatalf("strict: %+v", m5)
	}
}

func TestTQuantile(t *testing.T) {
	if TQuantile95(1) != 12.706 || TQuantile95(4) != 2.776 || TQuantile95(0) != 0 || math.Abs(TQuantile95(1000)-1.962) > 0.003 {
		t.Fatal("t table")
	}
	if ci := MeanCI95(2, 4); math.Abs(ci-3.182*2/2) > 1e-9 {
		t.Fatal(ci)
	}
	if MeanCI95(1, 1) != 0 {
		t.Fatal("n=1")
	}
}

func lorentz(freq []float64, f0, r float64, noise func(i int) complex128) []complex128 {
	out := make([]complex128, len(freq))
	for i, f := range freq {
		z := complex(r, 50*13*(f/f0-f0/f))
		g := (z - 50) / (z + 50)
		if noise != nil {
			g += noise(i)
		}
		out[i] = g
	}
	return out
}

func TestRepeatability(t *testing.T) {
	var freq []float64
	for f := 840e6; f <= 900e6; f += 0.3e6 {
		freq = append(freq, f)
	}
	// Five sweeps of an antenna whose resonance moves by ± 0.2 MHz.
	offs := []float64{-0.2e6, 0, 0.2e6, 0.1e6, -0.1e6}
	var sweeps [][]complex128
	for _, o := range offs {
		sweeps = append(sweeps, lorentz(freq, 868e6+o, 47, nil))
	}
	avg, r := Repeatability(freq, sweeps, 869e6, RepeatReseat)
	if r.N != 5 || r.Mode != RepeatReseat || r.ResN != 5 || len(avg) != len(freq) {
		t.Fatalf("%+v", r)
	}
	want := Describe([]float64{-0.2, 0, 0.2, 0.1, -0.1}).SD * 1e6
	if math.Abs(r.ResSD-want) > 0.03e6 || math.Abs(r.ResMean-868e6) > 0.05e6 {
		t.Fatalf("σ res %.0f (want %.0f), mean %.0f", r.ResSD, want, r.ResMean)
	}
	// The average is the vector mean (not an SWR average).
	for i := range avg {
		var s complex128
		for _, sw := range sweeps {
			s += sw[i]
		}
		if cmplx.Abs(avg[i]-s/5) > 1e-12 {
			t.Fatal("not the vector mean")
		}
	}
	if r.MaxDGamma <= 0 || r.RMSNoise <= 0 || r.SWRSD <= 0 {
		t.Fatalf("%+v", r)
	}
	for i := range freq {
		if r.EnvMin[i] > r.EnvMax[i] {
			t.Fatal("envelope")
		}
	}
	// Identical sweeps: zero spread.
	_, r = Repeatability(freq, [][]complex128{sweeps[1], sweeps[1], sweeps[1]}, 869e6, RepeatStill)
	if r.ResSD > 1 || r.MaxDGamma != 0 || r.RMSNoise != 0 {
		t.Fatalf("identical: σ %v max %v noise %v", r.ResSD, r.MaxDGamma, r.RMSNoise)
	}
	if p := PooledSD([]float64{1, 3}, []int{3, 3}); math.Abs(p-math.Sqrt(5)) > 1e-12 {
		t.Fatal(p)
	}
	if PooledSD([]float64{1}, []int{1}) != 0 {
		t.Fatal("n=1 pooled")
	}
}

func TestReseatDetector(t *testing.T) {
	a := make([]complex128, 50)
	b := make([]complex128, 50)
	for i := range a {
		a[i], b[i] = complex(0.1, 0.05), complex(0.12, 0.05)
	}
	r := NewReseat(HoldConfig{N: 3, Threshold: 0.006})
	// Untouched: keeps waiting however steady it is.
	for i := 0; i < 6; i++ {
		if st, _ := r.Add(a, true); st != ReseatWaiting {
			t.Fatalf("untouched %d: %s", i, st)
		}
	}
	// Unscrewed (open), reconnected, steady for N sweeps: ready.
	if st, _ := r.Add(nil, false); st != ReseatOpen {
		t.Fatal(st)
	}
	seq := []string{ReseatSettling, ReseatSettling, ReseatReady}
	for i, want := range seq {
		if st, _ := r.Add(b, true); st != want {
			t.Fatalf("after reconnect %d: %s want %s", i, st, want)
		}
	}
	// Moved (a jump) then steady.
	r = NewReseat(HoldConfig{N: 2, Threshold: 0.006})
	r.Add(a, true)
	if st, _ := r.Add(b, true); st != ReseatMoving {
		t.Fatal(st)
	}
	if st, _ := r.Add(b, true); st != ReseatReady {
		t.Fatal(st)
	}
}

func TestSetupCompare(t *testing.T) {
	g := func(off float64) []complex128 {
		out := make([]complex128, 100)
		for i := range out {
			out[i] = complex(0.1+off*float64(i%7)/7, 0.02)
		}
		return out
	}
	a := SetupCapture{Step: "A", N: 4, Found: true, ResFreq: 867e6, ResSD: 0.03e6, SWRTarget: 1.3, BandWorst: 1.4, Noise: 0.0016, Gamma: g(0)}
	tgt, win := 869e6, 1.3035e6
	// Quiet: within the noise.
	b := a
	b.Step, b.ResFreq, b.Gamma = "B", 867.02e6, g(0.0005)
	d := SetupCompare(a, b, win, tgt, 2)
	if d.Verdict != SetupQuiet || d.FreqNoise < 0.0002*tgt {
		t.Fatalf("quiet: %+v", d)
	}
	// Coupled: half the window or more.
	b.ResFreq, b.Gamma, b.SWRTarget, b.BandWorst = 860e6, g(0.08), 1.15, 1.3
	d = SetupCompare(a, b, win, tgt, 2)
	if d.Verdict != SetupCoupled || !d.Looks || !strings.Contains(d.Text, "Holding the cable") || math.Abs(d.Shift+7e6) > 1 {
		t.Fatalf("coupled: %+v", d)
	}
	v := SetupConclude([]SetupDiff{SetupCompare(a, SetupCapture{Step: "C", N: 4, Found: true, ResFreq: 867e6, Gamma: g(0), SWRTarget: 1.3, BandWorst: 1.4}, win, tgt, 2), d})
	if v.Verdict != SetupCoupled || !strings.Contains(v.Advice, "ferrite") || !strings.Contains(v.Line, "part of the antenna") {
		t.Fatalf("conclude: %+v", v)
	}
	// Slight: measurable, under half the window, small curve change.
	b = a
	b.Step, b.ResFreq, b.Gamma = "B", 867.4e6, g(0.012)
	d = SetupCompare(a, b, win, tgt, 2)
	if d.Verdict != SetupSlight {
		t.Fatalf("slight: %+v", d)
	}
	// The same shift is quiet against a wide (Auto) window if the curve barely changes.
	b.Gamma = g(0.004)
	if d = SetupCompare(a, b, 16e6, tgt, 2); d.Verdict != SetupQuiet {
		t.Fatalf("wide window: %+v", d)
	}
	if v := SetupConclude([]SetupDiff{d}); v.Verdict != SetupQuiet || !strings.HasPrefix(v.Title, "Cable is quiet") {
		t.Fatalf("%+v", v)
	}
}
