package rf

import (
	"strings"
	"testing"
)

func TestFirstCut(t *testing.T) {
	for _, c := range []struct{ full, want float64 }{
		{7.6, 4.5}, {10, 6}, {5, 3}, {0.9, 0.5}, {0.8, 0}, {1.66, 0.5}, {1.67, 1}, {0, 0}, {-3, 0},
	} {
		if got := FirstCut(c.full); got != c.want {
			t.Errorf("FirstCut(%g) = %g, want %g", c.full, got, c.want)
		}
	}
}

func TestCautiousUntilLearnt(t *testing.T) {
	// One log, resonance below target: too long, cautious first cut.
	p, err := PredictTrim([]TrimPoint{{LengthMm: 82, FreqHz: 856e6}}, 869e6)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Cautious || p.CutMm <= 1 || p.FirstCutMm != FirstCut(p.CutMm) || p.FirstCutMm >= p.CutMm {
		t.Fatalf("first log: %+v", p)
	}
	if !strings.Contains(p.Advice, "First cut") || !strings.Contains(p.Advice, "cannot be made longer") || !strings.Contains(p.Advice, "too long") {
		t.Errorf("advice: %q", p.Advice)
	}
	// Two logs at the same length (re-measure only): still nothing learnt.
	p, _ = PredictTrim([]TrimPoint{{82, 856e6}, {82, 856.3e6}}, 869e6)
	if !p.Cautious || p.Method != "ratio" {
		t.Errorf("no length change yet must stay cautious: %+v", p)
	}
	// Two logs with a cut: MHz/mm learnt, the full cut is recommended.
	p, _ = PredictTrim([]TrimPoint{{82, 856e6}, {80, 861e6}}, 869e6)
	if p.Cautious || p.Method != "fit" || p.FirstCutMm != p.CutMm || strings.Contains(p.Advice, "First cut") {
		t.Errorf("learnt: %+v", p)
	}
	// Resonance above the target: too short, never cut.
	p, _ = PredictTrim([]TrimPoint{{LengthMm: 82, FreqHz: 880e6}}, 869e6)
	if p.CutMm >= 0 || p.FirstCutMm != 0 || !strings.Contains(p.Advice, "Do not cut") || !strings.Contains(p.Advice, "too short") {
		t.Errorf("too short: %+v", p)
	}
	// Tiny cut: no 0 mm instruction, a plain hint instead.
	_, adv := CutAdvice(0.7, true)
	if !strings.Contains(adv, "less than 0.5 mm") {
		t.Errorf("tiny cut advice: %q", adv)
	}
	if f, adv := CutAdvice(0.1, true); f != 0 || !strings.Contains(adv, "On target") {
		t.Errorf("on target: %g %q", f, adv)
	}
}
