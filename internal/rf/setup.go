package rf

import (
	"fmt"
	"math"
	"strings"
)

// ---------- setup check: is the cable part of the antenna? ----------
//
// A monopole or whip without a proper ground plane uses the outside of the
// coax shield as its counterpoise: common-mode current flows on the cable,
// so the cable radiates and everything near it (a hand, the table, the
// mast's clamp) detunes the antenna. A hand on the cable adds a lossy load
// and changes the counterpoise: the resonance shifts, and the SWR can even
// look better, because loss hides mismatch, while less is radiated. A PETG
// or other plastic mast is nearly RF-transparent (εr ≈ 2.5–3, low loss)
// unless the element touches it. The cure is a choke at the feed point
// (clip-on ferrites, a sleeve balun) and measuring in the final
// installation.
//
// The check captures the antenna (an average of a few sweeps) with hands
// off and the cable as installed (A), then with a hand on the cable
// 10–20 cm below the antenna (B), and optionally touching the cable near
// the VNA (C), and compares B and C with A.
//
// Thresholds (SetupCompare):
//   - the noise: a change counts only above 3 σ of the difference of two
//     averages: for the resonance 3·√(σA²/nA + σB²/nB) of the single-sweep
//     resonances (at least 0.02 % of the target), for the curve
//     3 · RMS noise · √(1/nA + 1/nB) / √2 of RMS |ΔΓ| (at least 0.003);
//   - the tolerance window W (the acceptance setting, ± 0.15 % when there
//     is none): a shift under 10 % of W with an RMS |ΔΓ| under 0.01 and
//     the band's worst SWR within 0.1 is "quiet" (it cannot change a
//     verdict); a shift of 50 % of W or more, an RMS |ΔΓ| of 0.03 or more
//     (about a 4–5 MHz shift of a whip's dip at 869 MHz), or a change of
//     the band's worst SWR of a quarter of the limit's excess (0.25 at
//     SWR 2) or more is "coupled"; in between is "slight".

// SetupCapture is one step of the setup check.
type SetupCapture struct {
	Step      string       `json:"step"` // A, B, C
	Time      string       `json:"time"`
	N         int          `json:"n"`
	Found     bool         `json:"found"`
	ResFreq   float64      `json:"resFreq"`   // Hz, resonance of the average
	ResSD     float64      `json:"resSD"`     // Hz, σ of the single sweeps' resonance
	SWRTarget float64      `json:"swrTarget"` // SWR at the target
	BandWorst float64      `json:"bandWorst"` // worst SWR in the band (0: no band)
	Noise     float64      `json:"noise"`     // RMS |ΔΓ| between consecutive sweeps
	Gamma     []complex128 `json:"-"`
}

// SetupDiff is one step compared with step A.
type SetupDiff struct {
	Step       string  `json:"step"`
	Shift      float64 `json:"shift"`      // Hz, resonance(step) − resonance(A)
	ShiftOfWin float64 `json:"shiftOfWin"` // |Shift| in % of the tolerance window
	DSWRTarget float64 `json:"dswrTarget"`
	DBandWorst float64 `json:"dBandWorst"`
	DGamma     float64 `json:"dGamma"` // RMS |ΔΓ| between the two averages
	FreqNoise  float64 `json:"freqNoise"`
	GammaNoise float64 `json:"gammaNoise"`
	Verdict    string  `json:"verdict"` // quiet, slight, coupled
	Looks      bool    `json:"looksBetter"`
	Text       string  `json:"text"`
}

// Setup check verdicts.
const (
	SetupQuiet   = "quiet"
	SetupSlight  = "slight"
	SetupCoupled = "coupled"
)

// Setup check thresholds (see above).
const (
	setupQuietWin   = 0.10  // shift / window
	setupCoupledWin = 0.50  // shift / window
	setupQuietDG    = 0.010 // RMS |ΔΓ|
	setupCoupledDG  = 0.030
	setupQuietSWR   = 0.10
	setupCoupledSWR = 0.25 // × (limit − 1)
	setupMinFreq    = 0.0002
	setupMinDG      = 0.003
)

var setupWhere = map[string]string{"B": "holding the cable 10–20 cm below the antenna", "C": "touching the cable near the VNA"}

// SetupCompare compares step b with step a (hands off). window is the
// resonance tolerance (± Hz, 0: ± 0.15 % of target), limit the band SWR
// limit.
func SetupCompare(a, b SetupCapture, window, target, limit float64) SetupDiff {
	d := SetupDiff{Step: b.Step}
	if window <= 0 {
		window = target * OnTargetPct / 100
	}
	if limit <= 1 {
		limit = DefaultSWRLimit
	}
	na, nb := math.Max(1, float64(a.N)), math.Max(1, float64(b.N))
	d.FreqNoise = math.Max(3*math.Sqrt(a.ResSD*a.ResSD/na+b.ResSD*b.ResSD/nb), setupMinFreq*target)
	noise := math.Max(a.Noise, b.Noise)
	d.GammaNoise = math.Max(3*noise*math.Sqrt(1/na+1/nb)/math.Sqrt2, setupMinDG)
	if a.Found && b.Found {
		d.Shift = b.ResFreq - a.ResFreq
	}
	if window > 0 {
		d.ShiftOfWin = 100 * math.Abs(d.Shift) / window
	}
	d.DSWRTarget = b.SWRTarget - a.SWRTarget
	if a.BandWorst > 0 && b.BandWorst > 0 {
		d.DBandWorst = b.BandWorst - a.BandWorst
	}
	if len(a.Gamma) == len(b.Gamma) && len(a.Gamma) > 0 {
		d.DGamma = rmsDiff(a.Gamma, b.Gamma)
	}
	shift := math.Abs(d.Shift)
	if shift <= d.FreqNoise {
		shift = 0 // within the noise
	}
	dg := d.DGamma
	if dg <= d.GammaNoise {
		dg = 0
	}
	switch {
	case shift >= setupCoupledWin*window || dg >= setupCoupledDG || math.Abs(d.DBandWorst) >= setupCoupledSWR*(limit-1):
		d.Verdict = SetupCoupled
	case shift <= setupQuietWin*window && dg < setupQuietDG && math.Abs(d.DBandWorst) < setupQuietSWR:
		d.Verdict = SetupQuiet
	default:
		d.Verdict = SetupSlight
	}
	d.Looks = d.Verdict != SetupQuiet && (d.DSWRTarget < -0.05 || d.DBandWorst < -0.05)
	where := setupWhere[b.Step]
	if where == "" {
		where = "step " + b.Step
	}
	what := fmt.Sprintf("%s moved the resonance by %s MHz (%.0f %% of the ± %.2f MHz window)", capFirst(where), signedMHz(d.Shift), d.ShiftOfWin, window/1e6)
	if !a.Found || !b.Found {
		what = capFirst(where) + " changed the curve (no resonance to compare)"
	}
	swr := ""
	if d.DBandWorst != 0 {
		swr = fmt.Sprintf(", the band's worst SWR %.2f → %.2f", a.BandWorst, b.BandWorst)
	}
	switch d.Verdict {
	case SetupQuiet:
		d.Text = fmt.Sprintf("%s%s, RMS |ΔΓ| %.4f: within the noise or too small to change a verdict.", what, swr, d.DGamma)
	case SetupSlight:
		d.Text = fmt.Sprintf("%s%s, RMS |ΔΓ| %.4f: a small, measurable effect.", what, swr, d.DGamma)
	default:
		d.Text = fmt.Sprintf("%s%s, RMS |ΔΓ| %.4f: the cable is part of the antenna.", what, swr, d.DGamma)
	}
	if d.Looks {
		d.Text += " The SWR looks better with the hand: that is loss (the hand absorbs power), not a better antenna."
	}
	return d
}

// SetupVerdict is the overall result of a setup check.
type SetupVerdict struct {
	Verdict string      `json:"verdict"` // quiet, slight, coupled (worst step)
	Title   string      `json:"title"`
	Advice  string      `json:"advice"`
	Steps   []SetupDiff `json:"steps"`
	Line    string      `json:"line"` // one line for the antenna sheet / batch notes
}

// SetupConclude combines the step comparisons.
func SetupConclude(steps []SetupDiff) SetupVerdict {
	v := SetupVerdict{Verdict: SetupQuiet, Steps: steps}
	rank := map[string]int{SetupQuiet: 0, SetupSlight: 1, SetupCoupled: 2}
	worst := SetupDiff{}
	for _, s := range steps {
		if rank[s.Verdict] >= rank[v.Verdict] {
			v.Verdict, worst = s.Verdict, s
		}
	}
	shift := fmt.Sprintf("%s MHz", signedMHz(worst.Shift))
	switch v.Verdict {
	case SetupQuiet:
		v.Title = "Cable is quiet: measurements are trustworthy"
		v.Advice = "Touching the cable does not change the reading beyond the noise: what you measure is the antenna, not the cable."
		v.Line = fmt.Sprintf("cable quiet (touch: %s, %.0f %% of the window)", shift, worst.ShiftOfWin)
	case SetupSlight:
		v.Title = "Cable has a small effect"
		v.Advice = "Touching the cable changes the reading a little (" + shift + " when touched). Fine for tuning; a clip-on ferrite at the feed point removes it, and the final installation is the reference."
		v.Line = fmt.Sprintf("cable slightly sensitive (touch: %s, %.0f %% of the window)", shift, worst.ShiftOfWin)
	default:
		v.Title = "The cable is part of the antenna"
		v.Advice = "Shift of " + shift + " when touched: common-mode current flows on the coax shield, so the cable radiates and detunes the antenna. Add ferrite at the feed point (clip-on ferrites or a sleeve balun) and measure in the final installation; the hand-held reading is not the real performance."
		v.Line = fmt.Sprintf("cable is part of the antenna (touch: %s, %.0f %% of the window): add a choke at the feed point", shift, worst.ShiftOfWin)
	}
	for _, s := range steps {
		if s.Looks {
			v.Advice += " The SWR looked better when touched: loss masks mismatch; less is radiated."
			break
		}
	}
	return v
}

func signedMHz(hz float64) string {
	v := math.Round(hz/1e4) / 100
	if v == 0 {
		return "0.00"
	}
	return fmt.Sprintf("%+.2f", v)
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
