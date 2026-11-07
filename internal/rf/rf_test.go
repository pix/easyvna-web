package rf

import (
	"bytes"
	"math"
	"math/cmplx"
	"strings"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.6g, want %.6g ± %.3g", name, got, want, tol)
	}
}

func TestBasicConversions(t *testing.T) {
	// 100 Ω resistive load: Γ = 1/3, SWR 2, RL 9.542 dB.
	g := GammaFromZ(100)
	near(t, "Γ", real(g), 1.0/3, 1e-12)
	near(t, "SWR", SWR(g), 2, 1e-12)
	near(t, "RL", ReturnLoss(g), 9.5424, 1e-4)
	near(t, "S11dB", DB(g), -9.5424, 1e-4)
	z := Impedance(g)
	near(t, "R", real(z), 100, 1e-9)
	near(t, "X", imag(z), 0, 1e-9)

	// 25 Ω: Γ = -1/3, still SWR 2.
	near(t, "SWR(25Ω)", SWR(GammaFromZ(25)), 2, 1e-12)

	// Complex round trip.
	z0 := complex(30, -42)
	z1 := Impedance(GammaFromZ(z0))
	near(t, "R rt", real(z1), 30, 1e-9)
	near(t, "X rt", imag(z1), -42, 1e-9)

	// Perfect match / open / short stay finite.
	if SWR(0) != 1 || ReturnLoss(0) != MaxRLdB {
		t.Errorf("match: SWR %v RL %v", SWR(0), ReturnLoss(0))
	}
	if SWR(1) != MaxSWR || SWR(-1) != MaxSWR {
		t.Errorf("open/short SWR not capped")
	}
	if z := Impedance(1); real(z) != MaxOhms {
		t.Errorf("open impedance %v", z)
	}
	if z := Impedance(-1); cmplx.Abs(z) > 1e-9 {
		t.Errorf("short impedance %v", z)
	}
	near(t, "GammaFromSWR", GammaFromSWR(2), 1.0/3, 1e-12)
}

func TestSeriesLC(t *testing.T) {
	f := 868e6
	l, c := SeriesLC(2*math.Pi*f*10e-9, f) // 10 nH
	near(t, "L", l, 10e-9, 1e-15)
	if c != 0 {
		t.Errorf("c = %v", c)
	}
	l, c = SeriesLC(-1/(2*math.Pi*f*2e-12), f) // 2 pF
	near(t, "C", c, 2e-12, 1e-18)
	if l != 0 {
		t.Errorf("l = %v", l)
	}
}

func TestPortExtensionUndoesCable(t *testing.T) {
	tau := CableDelay(0.10, 0.695) // ~480 ps
	near(t, "tau", tau, 0.10/(0.695*299792458), 1e-15)
	for _, f := range []float64{433e6, 868e6, 1.5e9} {
		g := GammaFromZ(complex(42, 17))
		measured := g * cmplx.Rect(1, -2*2*math.Pi*f*tau) // what the VNA sees through the cable
		back := PortExtension(measured, f, tau)
		if cmplx.Abs(back-g) > 1e-12 {
			t.Errorf("f=%g: got %v want %v", f, back, g)
		}
		// Cable does not change |Γ|.
		near(t, "|Γ|", cmplx.Abs(measured), cmplx.Abs(g), 1e-12)
	}
	s := complex(0.5, 0.1)
	if TransmissionExtension(s, 1e9, 0) != s {
		t.Error("zero delay must be identity")
	}
}

// seriesRLC returns a sweep of an ideal series RLC load.
func seriesRLC(r, f0, q, start, stop float64, n int) ([]float64, []complex128) {
	w0 := 2 * math.Pi * f0
	l := q * r / w0
	c := 1 / (w0 * w0 * l)
	freq := make([]float64, n)
	s11 := make([]complex128, n)
	for i := range freq {
		f := start + (stop-start)*float64(i)/float64(n-1)
		w := 2 * math.Pi * f
		freq[i] = f
		s11[i] = GammaFromZ(complex(r, w*l-1/(w*c)))
	}
	return freq, s11
}

func TestAnalyzeSeriesRLC(t *testing.T) {
	const r, f0, q = 50.0, 868e6, 20.0
	freq, s11 := seriesRLC(r, f0, q, 800e6, 950e6, 1501)
	d := Derive(s11, nil)
	s, err := Analyze(freq, d, []Band{Band869})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "min SWR", s.MinSWR, 1, 1e-3)
	near(t, "min SWR freq", s.MinSWRFreq, f0, 0.2e6)
	if s.Resonance == nil {
		t.Fatal("no series resonance found")
	}
	near(t, "resonance", s.Resonance.Freq, f0, 1e3)
	near(t, "R at resonance", s.Resonance.R, r, 0.1)

	// Analytic SWR=2 edges for a matched series RLC (R = Z0):
	// |Γ|² = X²/(4R² + X²)  →  X = 2R|Γ|/sqrt(1-|Γ|²).
	g := 1.0 / 3
	xEdge := 2 * r * g / math.Sqrt(1-g*g)
	w0 := 2 * math.Pi * f0
	l := q * r / w0
	// X(ω) = ωL - 1/(ωC) = L(ω² - ω0²)/ω  →  solve for ω.
	solve := func(x float64) float64 {
		// Lω² - xω - Lω0² = 0
		return (x + math.Sqrt(x*x+4*l*l*w0*w0)) / (2 * l) / (2 * math.Pi)
	}
	near(t, "BW2 lo", s.BW2.Lo, solve(-xEdge), 50e3)
	near(t, "BW2 hi", s.BW2.Hi, solve(xEdge), 50e3)
	if s.BW2.LoOpen || s.BW2.HiOpen || !s.BW2.Found {
		t.Errorf("BW2 flags: %+v", s.BW2)
	}
	if !(s.BW15.Width < s.BW2.Width) {
		t.Errorf("BW1.5 %v should be narrower than BW2 %v", s.BW15.Width, s.BW2.Width)
	}
	if len(s.Bands) != 1 || s.Bands[0].Verdict != "good" || !s.Bands[0].Covered {
		t.Fatalf("band summary: %+v", s.Bands)
	}
	if len(s.Dips) != 1 {
		t.Fatalf("dips: %+v", s.Dips)
	}
	near(t, "dip freq", s.Dips[0].Freq, f0, 0.1e6)
	if !s.Dips[0].InBand || s.Dips[0].Band != "869" {
		t.Errorf("dip classification: %+v", s.Dips[0])
	}
}

func TestSWRBandwidthEdges(t *testing.T) {
	freq := []float64{1, 2, 3, 4, 5}
	swr := []float64{1.5, 1.2, 1.1, 3, 5}
	bw := SWRBandwidth(freq, swr, 2, 2)
	if !bw.LoOpen || bw.Lo != 1 || bw.HiOpen {
		t.Errorf("lo edge: %+v", bw)
	}
	near(t, "hi crossing", bw.Hi, 3+(2-1.1)/(3-1.1), 1e-12)
	if bw := SWRBandwidth(freq, swr, 4, 2); bw.Found {
		t.Errorf("SWR 5 point must not have a bandwidth: %+v", bw)
	}
}

func TestSummarizeBandInterpolatesEdges(t *testing.T) {
	// Coarse sweep with points at 860, 865, 870, 875 MHz; band 863-870.
	freq := []float64{860e6, 865e6, 870e6, 875e6}
	d := Derived{SWR: []float64{3, 1.2, 1.4, 2.5}, RL: []float64{6, 20, 15, 7}}
	bs := SummarizeBand(freq, d, Band869)
	// SWR at 863 MHz by interpolation = 3 + (1.2-3)*3/5 = 1.92 → worst.
	near(t, "worst", bs.WorstSWR, 1.92, 1e-9)
	near(t, "worst freq", bs.WorstSWRFreq, 863e6, 1)
	near(t, "best", bs.BestSWR, 1.2, 1e-9)
	if bs.Points != 2 || !bs.Covered || !bs.Pass2 || bs.Pass15 || bs.Verdict != "ok" {
		t.Errorf("summary: %+v", bs)
	}
	// Band outside the sweep.
	bs = SummarizeBand(freq, d, Band433)
	if bs.Covered || bs.Points != 0 || bs.Verdict != "n/a" {
		t.Errorf("outside band: %+v", bs)
	}
}

func TestVerdict(t *testing.T) {
	for _, c := range []struct {
		swr  float64
		want string
	}{{1.2, "good"}, {1.49, "good"}, {1.5, "ok"}, {1.99, "ok"}, {2, "bad"}, {10, "bad"}, {0, "n/a"}} {
		if got := Verdict(c.swr); got != c.want {
			t.Errorf("Verdict(%v) = %s, want %s", c.swr, got, c.want)
		}
	}
}

// twoMode builds the admittance sum of two series resonators (fundamental
// plus a higher-order mode) like the mock antenna.
func twoMode(f1, f2 float64, n int) ([]float64, []complex128) {
	freq := make([]float64, n)
	s11 := make([]complex128, n)
	branch := func(f, f0, r, q float64) complex128 {
		w0, w := 2*math.Pi*f0, 2*math.Pi*f
		l := q * r / w0
		c := 1 / (w0 * w0 * l)
		return complex(r, w*l-1/(w*c))
	}
	for i := range freq {
		f := 300e6 + 700e6*float64(i)/float64(n-1)
		freq[i] = f
		z := 1 / (1/branch(f, f1, 45, 25) + 1/branch(f, f2, 55, 15))
		s11[i] = GammaFromZ(z)
	}
	return freq, s11
}

func TestFindDipsAndClassify(t *testing.T) {
	freq, s11 := twoMode(425e6, 845e6, 701)
	d := Derive(s11, nil)
	s, err := Analyze(freq, d, LoRaBands)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Dips) != 2 {
		t.Fatalf("want 2 dips, got %+v", s.Dips)
	}
	a, b := s.Dips[0], s.Dips[1]
	near(t, "dip1", a.Freq, 425e6, 3e6)
	near(t, "dip2", b.Freq, 845e6, 4e6)
	if a.Band != "433" || a.InBand || !a.Near || a.OffsetEdge >= 0 {
		t.Errorf("dip1 classification: %+v", a)
	}
	if !strings.Contains(a.Advice, "too long: shorten") || a.LengthScalePct >= 0 {
		t.Errorf("dip1 advice: %q (%v%%)", a.Advice, a.LengthScalePct)
	}
	if b.HarmonicN != 2 || b.Band != "869" {
		t.Errorf("dip2 should be ≈2× dip1 near 869: %+v", b)
	}
	if strings.Contains(b.Advice, "shorten") {
		t.Errorf("harmonic dip must not give trimming advice: %q", b.Advice)
	}
	if len(s.Bands) != 3 || s.Bands[0].DipFreq == nil {
		t.Fatalf("bands: %+v", s.Bands)
	}
	// Offsets are measured from the band reference (433.92 MHz), not the centre.
	near(t, "433 offset", *s.Bands[0].DipOffset, a.Freq-433.92e6, 1)
	near(t, "dip1 offsetRef", a.OffsetRef, a.Freq-433.92e6, 1)
}

func TestDipAboveBandSaysLengthen(t *testing.T) {
	dips := []Dip{{Freq: 940e6}}
	ClassifyDips(dips, LoRaBands)
	if dips[0].Band != "915" || dips[0].OffsetEdge <= 0 || !strings.Contains(dips[0].Advice, "too short: lengthen") {
		t.Errorf("%+v", dips[0])
	}
	far := []Dip{{Freq: 650e6}}
	ClassifyDips(far, LoRaBands)
	if far[0].Near || !strings.Contains(far[0].Advice, "Not near") {
		t.Errorf("%+v", far[0])
	}
}

func TestNoDipsInFlatSweep(t *testing.T) {
	freq := []float64{1e6, 2e6, 3e6, 4e6, 5e6}
	s11 := []complex128{0.9, 0.9, 0.9, 0.9, 0.9}
	if d := FindDips(freq, Derive(s11, nil), 3, 3); d == nil || len(d) != 0 {
		t.Errorf("dips in flat sweep: %+v", d)
	}
}

func TestTouchstoneRoundTrip(t *testing.T) {
	freq := []float64{863e6, 866.5e6, 870e6}
	s11 := []complex128{complex(0.1, -0.2), complex(0.01, 0.02), complex(-0.3, 0.25)}
	s21 := []complex128{complex(0.5, 0.5), complex(0.6, 0.4), complex(0.7, 0.1)}

	var b1 bytes.Buffer
	if err := WriteTouchstone(&b1, freq, s11, nil, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	out := b1.String()
	if !strings.HasPrefix(out, "! hello\n# HZ S RI R 50\n863000000 0.1 -0.2\n") {
		t.Errorf("s1p output:\n%s", out)
	}
	f, a, c, err := ReadTouchstone(&b1)
	if err != nil || len(f) != 3 || c != nil || a[2] != s11[2] || f[1] != 866.5e6 {
		t.Fatalf("s1p read: %v %v %v %v", f, a, c, err)
	}

	var b2 bytes.Buffer
	if err := WriteTouchstone(&b2, freq, s11, s21, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b2.String(), "866500000 0.01 0.02 0.6 0.4 0 0 0 0") {
		t.Errorf("s2p output:\n%s", b2.String())
	}
	f, a, c, err = ReadTouchstone(&b2)
	if err != nil || len(f) != 3 || len(c) != 3 || c[1] != s21[1] || a[0] != s11[0] {
		t.Fatalf("s2p read: %v %v %v %v", f, a, c, err)
	}
	if err := WriteTouchstone(&b2, freq, s11[:2], nil, nil); err == nil {
		t.Error("length mismatch not detected")
	}
}

func TestReadTouchstoneMAandDB(t *testing.T) {
	in := "# MHz S MA R 50\n868 0.5 90\n"
	f, s, _, err := ReadTouchstone(strings.NewReader(in))
	if err != nil || f[0] != 868e6 || cmplx.Abs(s[0]-complex(0, 0.5)) > 1e-12 {
		t.Fatalf("MA: %v %v %v", f, s, err)
	}
	in = "# GHz S DB R 50\n0.868 -6.0206 180\n"
	_, s, _, err = ReadTouchstone(strings.NewReader(in))
	if err != nil || cmplx.Abs(s[0]-complex(-0.5, 0)) > 1e-4 {
		t.Fatalf("DB: %v %v", s, err)
	}
}

func TestWriteCSV(t *testing.T) {
	var b bytes.Buffer
	if err := WriteCSV(&b, []float64{868e6}, []complex128{complex(1.0/3, 0)}, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "freq_hz,s11_re") || strings.Contains(lines[0], "s21") {
		t.Fatalf("csv: %q", lines)
	}
	cols := strings.Split(lines[1], ",")
	if cols[0] != "868000000" || !strings.HasPrefix(cols[4], "2") || !strings.HasPrefix(cols[7], "100") {
		t.Errorf("csv row: %v", cols)
	}
	b.Reset()
	_ = WriteCSV(&b, []float64{868e6}, []complex128{0.1}, []complex128{0.5})
	if !strings.Contains(b.String(), "s21_db") {
		t.Error("s21 columns missing")
	}
}
