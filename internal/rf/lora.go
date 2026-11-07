package rf

import (
	"fmt"
	"math"
	"sort"
)

// LoRa / ISM bands used for classification.
var (
	Band433    = Band{Name: "433", Lo: 433.05e6, Hi: 434.79e6}
	Band869    = Band{Name: "869", Lo: 863e6, Hi: 870e6}
	Band869g3  = Band{Name: "869.4–869.65", Lo: 869.4e6, Hi: 869.65e6}
	Band915    = Band{Name: "915", Lo: 902e6, Hi: 928e6}
	LoRaBands  = []Band{Band433, Band869, Band915}
	nearFactor = 0.15 // a dip within ±15 % of a band centre counts as "near" it
)

// Centre returns the arithmetic band centre.
func (b Band) Centre() float64 { return (b.Lo + b.Hi) / 2 }

// Verdict grades a worst-case in-band SWR.
func Verdict(worstSWR float64) string {
	switch {
	case worstSWR <= 0 || math.IsNaN(worstSWR):
		return "n/a"
	case worstSWR < 1.5:
		return "good"
	case worstSWR < 2:
		return "ok"
	}
	return "bad"
}

// Dip is a local minimum of SWR (maximum of return loss), i.e. a resonance
// that the antenna is (more or less) matched at.
type Dip struct {
	Index      int       `json:"index"`
	Freq       float64   `json:"freq"` // Hz, parabolically interpolated
	SWR        float64   `json:"swr"`
	RL         float64   `json:"rl"`
	R          float64   `json:"r"`
	X          float64   `json:"x"`
	Prominence float64   `json:"prominence"` // dB of return loss
	BW2        Bandwidth `json:"bw2"`
	// Classification against the LoRa bands.
	Band           string  `json:"band"` // nearest band name
	InBand         bool    `json:"inBand"`
	Near           bool    `json:"near"`
	OffsetEdge     float64 `json:"offsetEdge"`   // Hz to nearest band edge; <0 below, >0 above, 0 inside
	OffsetCentre   float64 `json:"offsetCentre"` // Hz relative to band centre
	HarmonicOf     float64 `json:"harmonicOf,omitempty"`
	HarmonicN      int     `json:"harmonicN,omitempty"`
	LengthScalePct float64 `json:"lengthScalePct,omitempty"` // suggested length change, %
	Advice         string  `json:"advice"`
}

// FindDips returns resonance dips: local return-loss maxima with at least
// minProm dB of prominence and at least minRL dB of return loss. Dips at the
// very edge of the sweep are ignored because their true minimum may lie
// outside it.
func FindDips(freq []float64, d Derived, minProm, minRL float64) []Dip {
	n := len(freq)
	rl := d.RL
	dips := []Dip{} // never nil: encodes as [] for the UI
	for i := 1; i < n-1; i++ {
		if !(rl[i] > rl[i-1] && rl[i] >= rl[i+1]) || rl[i] < minRL {
			continue
		}
		// Prominence: walk outwards until higher terrain, tracking the minimum.
		minL, minR := rl[i], rl[i]
		for k := i - 1; k >= 0; k-- {
			if rl[k] > rl[i] {
				break
			}
			minL = math.Min(minL, rl[k])
		}
		for k := i + 1; k < n; k++ {
			if rl[k] > rl[i] {
				break
			}
			minR = math.Min(minR, rl[k])
		}
		prom := rl[i] - math.Max(minL, minR)
		if prom < minProm {
			continue
		}
		dip := Dip{Index: i, Freq: freq[i], SWR: d.SWR[i], RL: rl[i], R: d.R[i], X: d.X[i], Prominence: prom}
		// Parabolic refinement of the peak position.
		den := rl[i-1] - 2*rl[i] + rl[i+1]
		if den < 0 {
			off := 0.5 * (rl[i-1] - rl[i+1]) / den
			if off > -1 && off < 1 {
				step := (freq[i+1] - freq[i-1]) / 2
				dip.Freq = freq[i] + off*step
			}
		}
		dip.BW2 = SWRBandwidth(freq, d.SWR, i, 2)
		dips = append(dips, dip)
	}
	sort.Slice(dips, func(a, b int) bool { return dips[a].Freq < dips[b].Freq })
	return dips
}

// ClassifyDips fills in band, harmonic and advice fields.
func ClassifyDips(dips []Dip, bands []Band) {
	for i := range dips {
		dp := &dips[i]
		// Harmonic / higher-order mode of a lower, deeper-or-similar dip?
		for j := 0; j < i; j++ {
			r := dp.Freq / dips[j].Freq
			nr := math.Round(r)
			if nr >= 2 && nr <= 5 && math.Abs(r-nr)/nr < 0.08 {
				dp.HarmonicOf, dp.HarmonicN = dips[j].Freq, int(nr)
				break
			}
		}
		best := -1
		bestDist := math.Inf(1)
		for k, b := range bands {
			dist := 0.0
			switch {
			case dp.Freq < b.Lo:
				dist = b.Lo - dp.Freq
			case dp.Freq > b.Hi:
				dist = dp.Freq - b.Hi
			}
			if dist/b.Centre() < bestDist {
				bestDist, best = dist/b.Centre(), k
			}
		}
		if best < 0 {
			dp.Advice = "No reference bands."
			continue
		}
		b := bands[best]
		dp.Band = b.Name
		dp.OffsetCentre = dp.Freq - b.Centre()
		switch {
		case dp.Freq < b.Lo:
			dp.OffsetEdge = dp.Freq - b.Lo
		case dp.Freq > b.Hi:
			dp.OffsetEdge = dp.Freq - b.Hi
		}
		dp.InBand = dp.OffsetEdge == 0
		dp.Near = dp.InBand || math.Abs(dp.OffsetCentre) <= nearFactor*b.Centre()
		dp.Advice = dipAdvice(dp, b)
	}
}

func dipAdvice(dp *Dip, b Band) string {
	harm := ""
	if dp.HarmonicN > 0 {
		harm = fmt.Sprintf(" ≈%d× the %.1f MHz dip: probably a higher-order mode, tune the fundamental instead.",
			dp.HarmonicN, dp.HarmonicOf/1e6)
	}
	switch {
	case dp.InBand:
		return fmt.Sprintf("In the %s band (%.1f MHz from centre).%s", b.Name, dp.OffsetCentre/1e6, harm)
	case !dp.Near:
		return "Not near a LoRa band." + harm
	case dp.HarmonicN > 0:
		return fmt.Sprintf("%.1f MHz %s the %s band.%s", math.Abs(dp.OffsetEdge)/1e6, belowAbove(dp.OffsetEdge), b.Name, harm)
	}
	// Resonant length scales ~1/f: new length = old · f_res / f_centre.
	dp.LengthScalePct = 100 * (dp.Freq/b.Centre() - 1)
	dir := "too long: shorten"
	if dp.OffsetEdge > 0 {
		dir = "too short: lengthen"
	}
	return fmt.Sprintf("%.1f MHz %s the %s band (%.1f–%.1f MHz). If this is a monopole/dipole/whip it is %s it by about %.1f %%.",
		math.Abs(dp.OffsetEdge)/1e6, belowAbove(dp.OffsetEdge), b.Name, b.Lo/1e6, b.Hi/1e6, dir, math.Abs(dp.LengthScalePct))
}

func belowAbove(off float64) string {
	if off < 0 {
		return "below"
	}
	return "above"
}

// NearestDip returns the dip closest to the band centre, or nil.
func NearestDip(dips []Dip, b Band) *Dip {
	var best *Dip
	for i := range dips {
		if best == nil || math.Abs(dips[i].Freq-b.Centre()) < math.Abs(best.Freq-b.Centre()) {
			best = &dips[i]
		}
	}
	return best
}
