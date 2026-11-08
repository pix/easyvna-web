package rf

import (
	"strings"
	"testing"
)

func analyzed(freq []float64, s11 []complex128) (Derived, []Dip) {
	d := Derive(s11, nil)
	sum, _ := Analyze(freq, d, LoRaBands)
	return d, sum.Dips
}

func TestBandEffectsProfile(t *testing.T) {
	// 869 profile: REF resonance 860 MHz with R 30 Ω, now 868 MHz with R 48 Ω.
	rf, rs := seriesRLC(30, 860e6, 14, 779e6, 959e6, 401)
	lf, ls := seriesRLC(48, 868e6, 14, 779e6, 959e6, 401)
	rd, rdips := analyzed(rf, rs)
	ld, ldips := analyzed(lf, ls)
	es := BandEffects(lf, ld, ldips, rf, rd, rdips, false)
	if len(es) != 2 || es[0].Label != "869" || es[1].Label != "915" {
		t.Fatalf("bands in view: %+v", es)
	}
	e := es[0]
	if e.DF == nil || *e.DF < 7.5e6 || *e.DF > 8.5e6 || e.SWR >= e.RefSWR {
		t.Fatalf("869: %+v", e)
	}
	if !strings.HasPrefix(e.Text, "869: +8.0 MHz, SWR ") || !strings.Contains(e.Text, "→") {
		t.Errorf("text: %q", e.Text)
	}
	// 915 has no dip of its own: only the SWR change.
	if es[1].DF != nil || !strings.Contains(es[1].Text, "no dip") {
		t.Errorf("915: %+v", es[1])
	}
	if s := FormatEffects(es); !strings.Contains(s, " · 915: ") {
		t.Errorf("format: %q", s)
	}
	// Same sweep: no shift, no SWR change shown.
	es = BandEffects(lf, ld, ldips, lf, ld, ldips, false)
	if es[0].Text != "869: ±0.0 MHz" {
		t.Errorf("unchanged: %q", es[0].Text)
	}
}

func TestBandEffectsIdentify(t *testing.T) {
	// Wide sweep, two modes: 433 → 436, 1st higher mode 800 → 812.
	rf, rs := twoMode(433e6, 800e6, 401)
	lf, ls := twoMode(436e6, 812e6, 401)
	rd, rdips := analyzed(rf, rs)
	ld, ldips := analyzed(lf, ls)
	es := BandEffects(lf, ld, ldips, rf, rd, rdips, true)
	text := FormatEffects(es)
	if !strings.Contains(text, "433: +3.") || !strings.Contains(text, "MHz dip: +1") {
		t.Fatalf("identify effects: %q %+v", text, es)
	}
	// A dip that only exists now is reported as new, a vanished one as gone.
	ff, fs := seriesRLC(45, 433e6, 25, 300e6, 1000e6, 401)
	fd, fdips := analyzed(ff, fs)
	es = BandEffects(lf, ld, ldips, ff, fd, fdips, true)
	if !strings.Contains(FormatEffects(es), "new dip at") {
		t.Errorf("new dip: %q", FormatEffects(es))
	}
	es = BandEffects(ff, fd, fdips, lf, ld, ldips, true)
	if !strings.Contains(FormatEffects(es), "gone") {
		t.Errorf("gone dip: %q", FormatEffects(es))
	}
}
