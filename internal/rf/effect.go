package rf

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// BandEffect is the effect of a change (live sweep against the REF trace)
// on one band or on one resonance dip.
type BandEffect struct {
	Label   string   `json:"label"` // "433", "869", "915" or "1290 MHz dip"
	Kind    string   `json:"kind"`  // band, dip, new (dip without REF counterpart), gone (REF dip not seen now)
	Freq    *float64 `json:"freq,omitempty"`
	RefFreq *float64 `json:"refFreq,omitempty"`
	DF      *float64 `json:"df,omitempty"`     // Hz, live − REF
	SWR     float64  `json:"swr,omitempty"`    // worst in band (band) or at the dip (dip)
	RefSWR  float64  `json:"refSWR,omitempty"` // same for REF
	Text    string   `json:"text"`
}

// effectSWRMin is the smallest SWR change worth showing.
const effectSWRMin = 0.05

// bandDip returns the dip classified to band b (by ClassifyDips) that is
// near it and closest to its reference, or nil.
func bandDip(dips []Dip, b Band) *Dip {
	var best *Dip
	for i := range dips {
		d := &dips[i]
		if d.Band != b.Name || !d.Near || d.HarmonicN > 0 && !d.InBand {
			continue
		}
		if best == nil || math.Abs(d.Freq-b.Reference()) < math.Abs(best.Freq-b.Reference()) {
			best = d
		}
	}
	return best
}

func overlaps(freq []float64, b Band) bool {
	return len(freq) > 1 && b.Hi > freq[0] && b.Lo < freq[len(freq)-1]
}

// BandEffects compares a live sweep with its REF for each LoRa band in view
// (resonance shift of the dip belonging to the band, worst in-band SWR
// before → after) and, for identify sweeps, for each other resonance dip
// (matched to the nearest REF dip).
func BandEffects(freq []float64, d Derived, dips []Dip, rfreq []float64, rd Derived, rdips []Dip, identify bool) []BandEffect {
	out := []BandEffect{}
	if len(freq) < 2 || len(rfreq) < 2 {
		return out
	}
	usedLive, usedRef := map[int]bool{}, map[int]bool{}
	idx := func(ds []Dip, p *Dip) int {
		for i := range ds {
			if &ds[i] == p {
				return i
			}
		}
		return -1
	}
	for _, b := range LoRaBands {
		if !overlaps(freq, b) || !overlaps(rfreq, b) {
			continue
		}
		e := BandEffect{Label: b.Name, Kind: "band"}
		a, r := bandDip(dips, b), bandDip(rdips, b)
		if a != nil && r != nil {
			fa, fr := a.Freq, r.Freq
			df := fa - fr
			e.Freq, e.RefFreq, e.DF = &fa, &fr, &df
			usedLive[idx(dips, a)], usedRef[idx(rdips, r)] = true, true
		}
		e.SWR, e.RefSWR = SummarizeBand(freq, d, b).WorstSWR, SummarizeBand(rfreq, rd, b).WorstSWR
		e.Text = effectText(e)
		out = append(out, e)
	}
	if !identify {
		return out
	}
	// Other dips (identify sweeps): pair each live dip with the nearest
	// unused REF dip within 6 % of its frequency.
	for i := range dips {
		if usedLive[i] {
			continue
		}
		a := dips[i]
		best, bd := -1, math.Inf(1)
		for j := range rdips {
			if usedRef[j] {
				continue
			}
			if dd := math.Abs(rdips[j].Freq - a.Freq); dd < bd && dd <= 0.06*a.Freq {
				best, bd = j, dd
			}
		}
		fa := a.Freq
		e := BandEffect{Label: fmt.Sprintf("%.0f MHz dip", a.Freq/1e6), Kind: "dip", Freq: &fa, SWR: a.SWR}
		if best < 0 {
			e.Kind = "new"
		} else {
			usedRef[best] = true
			fr := rdips[best].Freq
			df := fa - fr
			e.RefFreq, e.DF, e.RefSWR = &fr, &df, rdips[best].SWR
			e.Label = fmt.Sprintf("%.0f MHz dip", fr/1e6)
		}
		e.Text = effectText(e)
		out = append(out, e)
	}
	for j := range rdips {
		if usedRef[j] || rdips[j].Freq < freq[0] || rdips[j].Freq > freq[len(freq)-1] {
			continue
		}
		fr := rdips[j].Freq
		e := BandEffect{Label: fmt.Sprintf("%.0f MHz dip", fr/1e6), Kind: "gone", RefFreq: &fr, RefSWR: rdips[j].SWR}
		e.Text = effectText(e)
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return effectFreq(out[i]) < effectFreq(out[j]) })
	return out
}

func effectFreq(e BandEffect) float64 {
	switch {
	case e.RefFreq != nil:
		return *e.RefFreq
	case e.Freq != nil:
		return *e.Freq
	}
	for _, b := range LoRaBands {
		if b.Name == e.Label {
			return b.Reference()
		}
	}
	return 0
}

func swrShort(v float64) string {
	if v >= 99 {
		return ">99"
	}
	return fmt.Sprintf("%.1f", v)
}

func effectText(e BandEffect) string {
	var parts []string
	switch e.Kind {
	case "new":
		return fmt.Sprintf("new dip at %.1f MHz (SWR %s)", *e.Freq/1e6, swrShort(e.SWR))
	case "gone":
		return fmt.Sprintf("%s: gone", e.Label)
	}
	if e.DF != nil {
		v := math.Round(*e.DF/1e5) / 10
		if v == 0 {
			parts = append(parts, "±0.0 MHz")
		} else {
			parts = append(parts, fmt.Sprintf("%+.1f MHz", v))
		}
	} else if e.Kind == "band" {
		parts = append(parts, "no dip")
	}
	if e.SWR > 0 && e.RefSWR > 0 && math.Abs(e.SWR-e.RefSWR) >= effectSWRMin && (e.SWR < 99 || e.RefSWR < 99) {
		parts = append(parts, "SWR "+swrShort(e.RefSWR)+"→"+swrShort(e.SWR))
	}
	return e.Label + ": " + strings.Join(parts, ", ")
}

// FormatEffects joins the per-band texts: "433: +3.1 MHz · 869: +12.4 MHz, SWR 1.8→1.3".
func FormatEffects(es []BandEffect) string {
	var t []string
	for _, e := range es {
		t = append(t, e.Text)
	}
	return strings.Join(t, " · ")
}
