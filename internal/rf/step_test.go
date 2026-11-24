package rf

import (
	"math"
	"math/cmplx"
	"testing"
)

// broadbandWithStep is a broadband antenna like a measured 868 whip:
// best match at 815 MHz, SWR ≈ 1.3 over 840–960 MHz, and the NanoVNA's
// harmonic switch at 900 MHz leaving a step with an interpolated cal.
func broadbandWithStep(step bool) ([]float64, []complex128) {
	const n = 401
	freq := make([]float64, n)
	s11 := make([]complex128, n)
	for i := range freq {
		f := 780e6 + 180e6*float64(i)/float64(n-1)
		freq[i] = f
		var swr float64
		switch m := f / 1e6; {
		case m < 815:
			swr = 1.08 + 1.72*math.Pow((815-m)/35, 2)
		case m < 850:
			swr = 1.08 + 0.22*(m-815)/35
		case m < 900:
			swr = 1.30 - 0.05*(m-850)/50
		default: // rising above 900 MHz: the step makes a fake dip there
			swr = 1.25 + 0.15*(m-900)/60
		}
		mag := (swr - 1) / (swr + 1)
		s11[i] = cmplx.Rect(mag, 2*math.Pi*(f-780e6)/360e6)
		if step && f > 900e6 {
			s11[i] -= cmplx.Rect(0.055, 2*math.Pi*(f-780e6)/360e6)
		}
	}
	return freq, s11
}

func TestStepIsNotAResonance(t *testing.T) {
	freq, s11 := broadbandWithStep(true)
	d := Derive(s11, nil)
	steps := FindSteps(freq, d)
	if len(steps) != 1 || math.Abs(steps[0].Freq-900e6) > 1e6 {
		t.Fatalf("steps %+v", steps)
	}
	ws := Detect(freq, d)
	if len(ws) != 1 || !codes(ws)["step"] || Suspect(ws) {
		t.Errorf("warnings %+v", ws)
	}
	for _, dp := range FindDips(freq, d, 1, 1) {
		if math.Abs(dp.Freq-900e6) < 10e6 {
			t.Errorf("the step is taken for a dip: %+v", dp)
		}
	}
	ti := Tune(freq, d, 869e6)
	if !ti.Found || math.Abs(ti.ResFreq-815e6) > 2e6 || ti.Direction != "shorten" {
		t.Errorf("tune %+v", ti)
	}
	s, _ := Analyze(freq, d, []Band{Band869})
	if b := s.Bands[0]; b.DipFreq == nil || math.Abs(*b.DipFreq-815e6) > 2e6 {
		t.Errorf("band resonance %+v", b)
	}
	// Without the step nothing is flagged.
	freq, s11 = broadbandWithStep(false)
	if ws := Detect(freq, Derive(s11, nil)); len(ws) != 0 {
		t.Errorf("clean sweep flagged %+v", ws)
	}
}

func TestNearestDipPrefersMainDip(t *testing.T) {
	dips := []Dip{{Freq: 815e6, Prominence: 12}, {Freq: 880e6, Prominence: 1.5}, {Freq: 433e6, Prominence: 30}}
	if dp := NearestDip(dips, Band869); dp == nil || dp.Freq != 815e6 {
		t.Errorf("got %+v", dp)
	}
	// Two comparable dips: the nearest.
	dips[1].Prominence = 9
	if dp := NearestDip(dips, Band869); dp == nil || dp.Freq != 880e6 {
		t.Errorf("got %+v", dp)
	}
	// None near the band: the closest.
	if dp := NearestDip(dips[2:], Band869); dp == nil || dp.Freq != 433e6 {
		t.Errorf("got %+v", dp)
	}
}

func TestBandImpedanceAtReference(t *testing.T) {
	freq, s11 := seriesRLC(50, 869e6, 15, 820e6, 920e6, 201)
	s, _ := Analyze(freq, Derive(s11, nil), []Band{Band869})
	b := s.Bands[0]
	near(t, "R", b.ZRefR, 50, 0.5)
	near(t, "X", b.ZRefX, 0, 0.5)
}
