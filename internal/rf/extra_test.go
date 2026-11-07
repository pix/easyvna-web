package rf

import (
	"math"
	"math/cmplx"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestMismatchLossAndQ(t *testing.T) {
	near(t, "ML(SWR 2)", MismatchLoss(1.0/3), 0.5115, 1e-4)
	near(t, "ML(match)", MismatchLoss(0), 0, 1e-12)
	if MismatchLoss(1) != MaxMLdB || MismatchLoss(1.2) != MaxMLdB {
		t.Error("ML not capped")
	}
	// Series RLC with R = Z0: SWR=2 bandwidth gives Q back within a few %.
	const r, f0, q = 50.0, 868e6, 20.0
	freq, s11 := seriesRLC(r, f0, q, 780e6, 960e6, 3001)
	s, _ := Analyze(freq, Derive(s11, nil), nil)
	near(t, "Q", s.Q, q, 0.03*q)
	if len(s.Dips) != 1 || math.Abs(s.Dips[0].Q-q) > 0.03*q {
		t.Errorf("dip Q: %+v", s.Dips)
	}
	near(t, "ML at min", s.MLAtMin, 0, 1e-3)
}

func TestDetectOpenCalAndLossy(t *testing.T) {
	n := 201
	freq := make([]float64, n)
	for i := range freq {
		freq[i] = 300e6 + 700e6*float64(i)/float64(n-1)
	}
	mk := func(g func(f float64) complex128) Derived {
		s := make([]complex128, n)
		for i, f := range freq {
			s[i] = g(f)
		}
		return Derive(s, nil)
	}
	codes := func(w []Warning) string {
		var c []string
		for _, x := range w {
			c = append(c, x.Code)
		}
		return strings.Join(c, ",")
	}
	// Open cable: |Γ| ≈ 0.98 with phase rotation.
	if c := codes(Detect(freq, mk(func(f float64) complex128 { return cmplx.Rect(0.98, -2*math.Pi*f*1e-9) }))); c != "open" {
		t.Errorf("open: %q", c)
	}
	// Calibration mismatch: |Γ| ≈ 1.06.
	if c := codes(Detect(freq, mk(func(f float64) complex128 { return cmplx.Rect(1.06, f*1e-9) }))); c != "calcheck" {
		t.Errorf("cal: %q", c)
	}
	// Resistor-like load: Z ≈ 45 Ω with a little reactance.
	if c := codes(Detect(freq, mk(func(f float64) complex128 { return GammaFromZ(complex(45, 5*(f/650e6-1))) }))); c != "lossy" {
		t.Errorf("lossy: %q", c)
	}
	// A real resonant antenna on the same wide sweep is not flagged.
	_, s11 := seriesRLC(48, 868e6, 12, 300e6, 1000e6, n)
	if c := codes(Detect(freq, Derive(s11, nil))); c != "" {
		t.Errorf("real antenna flagged: %q", c)
	}
	// A narrow band sweep of a broadband match is not flagged as lossy.
	nf := []float64{860e6, 865e6, 870e6, 875e6, 880e6}
	nd := Derive([]complex128{0.1, 0.1, 0.1, 0.1, 0.1}, nil)
	if c := codes(Detect(nf, nd)); c != "" {
		t.Errorf("narrow sweep flagged: %q", c)
	}
}

func TestTuneDirection(t *testing.T) {
	// Resonance at 850 MHz, target 869: too long -> shorten.
	freq, s11 := seriesRLC(50, 850e6, 15, 820e6, 900e6, 161)
	ti := Tune(freq, Derive(s11, nil), 869e6)
	if !ti.Found || ti.Direction != "shorten" || math.Abs(ti.ResFreq-850e6) > 0.3e6 {
		t.Fatalf("%+v", ti)
	}
	near(t, "offset", ti.Offset, -19e6, 0.3e6)
	if ti.SWRAtTarget <= ti.SWRAtRes || ti.Verdict == "" {
		t.Errorf("target stats: %+v", ti)
	}
	// Resonance above target -> lengthen; on target -> ok.
	freq, s11 = seriesRLC(50, 880e6, 15, 840e6, 920e6, 161)
	if ti := Tune(freq, Derive(s11, nil), 869e6); ti.Direction != "lengthen" {
		t.Errorf("above: %+v", ti)
	}
	freq, s11 = seriesRLC(50, 869.3e6, 15, 840e6, 900e6, 161)
	if ti := Tune(freq, Derive(s11, nil), 869e6); ti.Direction != "ok" {
		t.Errorf("on target: %+v", ti)
	}
	// Resonance below the window: best match at the low edge -> shorten.
	freq, s11 = seriesRLC(50, 800e6, 15, 850e6, 900e6, 101)
	if ti := Tune(freq, Derive(s11, nil), 869e6); ti.Found || !ti.EdgeLow || ti.Direction != "shorten" {
		t.Errorf("below window: %+v", ti)
	}
}

func TestTuneWindow(t *testing.T) {
	a, b := TuneWindow(850e6, 869e6, 779e6, 959e6)
	if !(a < 850e6 && b > 869e6) || b-a < 0.05*869e6-1e3 {
		t.Errorf("window %v..%v", a, b)
	}
	// Clamped to the calibrated range.
	a, b = TuneWindow(790e6, 869e6, 779e6, 959e6)
	if a < 779e6 || b > 959e6 || a > 790e6 {
		t.Errorf("clamped window %v..%v", a, b)
	}
}

func TestCompareRefAndAverage(t *testing.T) {
	f1, s1 := seriesRLC(50, 860e6, 15, 800e6, 950e6, 301)
	f2, s2 := seriesRLC(50, 866e6, 15, 800e6, 950e6, 301)
	d1, d2 := Derive(s1, nil), Derive(s2, nil)
	a1, _ := Analyze(f1, d1, []Band{Band869})
	a2, _ := Analyze(f2, d2, []Band{Band869})
	b := Band869
	rd := CompareRef(f2, d2, a2.Dips, f1, d1, a1.Dips, 869e6, &b)
	if rd.DF == nil || math.Abs(*rd.DF-6e6) > 0.3e6 || !rd.SameRange || rd.DSWR >= 0 || rd.DBandWorst == nil {
		t.Fatalf("%+v", rd)
	}
	avg := Average([][]complex128{{1, 2}, {3, 4i}})
	if avg[0] != 2 || avg[1] != complex(1, 2) {
		t.Errorf("average %v", avg)
	}
}

func TestTrimRatioFirstEstimate(t *testing.T) {
	p, err := PredictTrim([]TrimPoint{{LengthMm: 82, FreqHz: 858e6}}, 869e6)
	if err != nil || p.Method != "ratio" {
		t.Fatal(p, err)
	}
	near(t, "new length", p.NewLengthMm, 82*858.0/869, 1e-9)
	near(t, "cut", p.CutMm, 82-82*858.0/869, 1e-9)
	if _, err := PredictTrim(nil, 869e6); err == nil {
		t.Error("empty log accepted")
	}
	// Resonance above target -> negative cut (lengthen).
	p, _ = PredictTrim([]TrimPoint{{LengthMm: 80, FreqHz: 880e6}}, 869e6)
	if p.CutMm >= 0 {
		t.Errorf("above target: %+v", p)
	}
}

// TestTrimLearnsAntenna simulates a trim session on an element with a big
// end effect (f = k/(L+δ), δ = 12 mm), so the textbook ratio overestimates
// the needed cut. Each round cuts 80 % of the prediction and re-measures
// with realistic scatter. The calculator must converge onto the target and
// learn a sensitivity close to the true one.
func TestTrimLearnsAntenna(t *testing.T) {
	const delta, l0, target = 12.0, 82.0, 869e6
	k := 840e6 * (l0 + delta)
	rng := rand.New(rand.NewPCG(3, 4))
	measure := func(l float64) float64 { return k/(l+delta) + rng.NormFloat64()*0.4e6 }
	log := []TrimPoint{{LengthMm: l0, FreqHz: measure(l0)}}
	l := l0
	var p TrimPrediction
	for round := 0; round < 8; round++ {
		var err error
		p, err = PredictTrim(log, target)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(log[len(log)-1].FreqHz-target) < 1.5e6 {
			break
		}
		cut := 0.8 * p.CutMm
		if cut <= 0 {
			t.Fatalf("round %d: predicted lengthening on a too-long element: %+v", round, p)
		}
		l -= cut
		log = append(log, TrimPoint{LengthMm: l, FreqHz: measure(l)})
	}
	final := k / (l + delta)
	t.Logf("%d logs, final %.2f MHz, learnt %.2f MHz/mm (textbook %.2f, true %.2f), confidence %.2f",
		len(log), final/1e6, p.MHzPerMm, p.PriorMHzPerMm, target/(k/target)/1e6, p.Confidence)
	if math.Abs(final-target) > 3e6 {
		t.Fatalf("did not converge: %.2f MHz after %d logs (%+v)", final/1e6, len(log), p)
	}
	trueSlope := target / (k / target) / 1e6 // f/(L+δ) at the target, MHz/mm
	if p.Method != "fit" || math.Abs(p.MHzPerMm-trueSlope) > math.Abs(p.PriorMHzPerMm-trueSlope) {
		t.Errorf("learnt %.2f MHz/mm, true %.2f, textbook %.2f (%+v)", p.MHzPerMm, trueSlope, p.PriorMHzPerMm, p)
	}
	// Never overshoot into "too short" territory by more than the noise.
	if final > target+2e6 {
		t.Errorf("overshot: %.2f MHz", final/1e6)
	}
}

func TestTrimInconsistentLog(t *testing.T) {
	// Resonance went down although the element got shorter: mislogged.
	p, err := PredictTrim([]TrimPoint{{80, 860e6}, {78, 850e6}}, 869e6)
	if err != nil || p.Confidence != 0 || p.Note == "" {
		t.Errorf("%+v %v", p, err)
	}
}
