package rf

import (
	"errors"
	"fmt"
	"math"
)

// TrimPoint is one logged measurement: element length and the resonance
// measured at that length.
type TrimPoint struct {
	LengthMm float64 `json:"lengthMm"`
	FreqHz   float64 `json:"freqHz"`
}

// TrimPrediction is the trim calculator's answer.
type TrimPrediction struct {
	Method      string  `json:"method"` // "ratio" (no usable history yet) or "fit" (learnt from the log)
	NewLengthMm float64 `json:"newLengthMm"`
	CutMm       float64 `json:"cutMm"`    // > 0: cut this much off; < 0: lengthen
	MHzPerMm    float64 `json:"mhzPerMm"` // sensitivity used (positive number)
	// PriorMHzPerMm is the textbook sensitivity f/L, for comparison.
	PriorMHzPerMm float64 `json:"priorMHzPerMm"`
	// EndEffectMm is δ in f = k/(L+δ) implied by the learnt sensitivity.
	EndEffectMm float64 `json:"endEffectMm"`
	// Confidence is the share of the slope estimate that comes from this
	// antenna's own log (0 = pure textbook ratio, 1 = pure data).
	Confidence float64 `json:"confidence"`
	Points     int     `json:"points"`
	Note       string  `json:"note,omitempty"`
	// Cautious is set until this antenna's MHz/mm has been learnt (fewer
	// than two logged lengths, or no length change yet): then only part of
	// the estimated cut is recommended, because a cut wire cannot be made
	// longer again.
	Cautious bool `json:"cautious"`
	// FirstCutMm is what to cut now: CutMm when learnt, else the cautious
	// share of it (CautiousFraction, rounded down to CutStepMm). 0 when the
	// element is too short or on target.
	FirstCutMm float64 `json:"firstCutMm"`
	// Advice says what to do in plain words.
	Advice string `json:"advice"`
}

// Cautious first cut.
const (
	CautiousFraction = 0.6 // share of the estimate cut while MHz/mm is unknown
	CutStepMm        = 0.5 // first cuts are rounded down to this step
	OnTargetMm       = 0.15
)

// FirstCut is the cautious first cut for an estimated full cut (mm): 60 %
// of it, rounded down to 0.5 mm. It is 0 for a full cut of 0 or less.
func FirstCut(fullMm float64) float64 {
	if fullMm <= 0 {
		return 0
	}
	return math.Floor(fullMm*CautiousFraction/CutStepMm+1e-9) * CutStepMm
}

// CutAdvice turns an estimated cut (mm, > 0 = too long, < 0 = too short)
// into what to cut now and a plain-language explanation.
func CutAdvice(fullMm float64, cautious bool) (first float64, advice string) {
	switch {
	case fullMm < -OnTargetMm:
		return 0, fmt.Sprintf("Resonance is above the target: the element is too short. Do not cut: cutting raises the resonance even more. "+
			"Lengthen it by about %.1f mm (solder on wire or add an extension), or accept the offset.", -fullMm)
	case fullMm <= OnTargetMm:
		return 0, "On target: leave it."
	case !cautious:
		return fullMm, fmt.Sprintf("Resonance is below the target: the element is too long. Cut %.1f mm (learnt from this antenna's log).", fullMm)
	}
	first = FirstCut(fullMm)
	why := "this antenna's MHz/mm is not learnt yet, and a cut wire cannot be made longer again. Log the result: the next estimate uses what this cut did."
	if first <= 0 {
		return 0, fmt.Sprintf("Resonance is below the target: the element is too long, but only by about %.1f mm. Trim less than %.1f mm at a time and measure: %s", fullMm, CutStepMm, why)
	}
	return first, fmt.Sprintf("Resonance is below the target: the element is too long. First cut: %.1f mm (full estimate %.1f mm). Cut only %.0f %% now: %s",
		first, fullMm, 100*CautiousFraction, why)
}

// ErrTrimInput is returned for unusable input.
var ErrTrimInput = errors.New("trim: need a length > 0 and a resonance > 0")

// Trim model parameters.
const (
	// trimNoiseFrac is the assumed 1σ measurement scatter of a logged
	// resonance (hand effect, cable movement, temperature), as a fraction of f.
	trimNoiseFrac = 0.001
	// trimPriorSpread is the 1σ uncertainty of the textbook slope f/L.
	trimPriorSpread = 0.3
)

// PredictTrim predicts the element length for target from the log.
//
// Near its current length an element's resonance changes linearly with
// length, f ≈ f̄ + s·(L − L̄), with s < 0. The textbook first guess for the
// slope is s0 = −f/L (it follows from f ∝ 1/L and gives the familiar
// L_new = L·f_res/f_target). Real antennas deviate from it (loading coils,
// end effects, feed and ground), so every logged cut is used to learn s:
// a Bayesian line fit with s0 as the prior (±30 %) and measurement noise of
// 0.1 % of f. Small cuts that move the resonance by less than the noise
// barely change the estimate; larger cuts quickly take over. The prediction
// starts from the latest measurement (the antenna as it is now, which is
// also what the TUNE readout shows) and applies the learnt slope.
func PredictTrim(log []TrimPoint, target float64) (TrimPrediction, error) {
	var pts []TrimPoint
	for _, p := range log {
		if p.LengthMm > 0 && p.FreqHz > 0 {
			pts = append(pts, p)
		}
	}
	if len(pts) == 0 || target <= 0 {
		return TrimPrediction{}, ErrTrimInput
	}
	last := pts[len(pts)-1]
	n := float64(len(pts))
	var mL, mF float64
	for _, p := range pts {
		mL += p.LengthMm / n
		mF += p.FreqHz / n
	}
	var sxx, sxy float64
	for _, p := range pts {
		sxx += (p.LengthMm - mL) * (p.LengthMm - mL)
		sxy += (p.LengthMm - mL) * (p.FreqHz - mF)
	}
	s0 := -last.FreqHz / last.LengthMm // Hz/mm
	slope, conf := posteriorSlope(sxx, sxy, trimNoiseFrac*mF, s0)
	p := TrimPrediction{Method: "ratio", PriorMHzPerMm: -s0 / 1e6, Points: len(pts), Confidence: conf}
	if slope >= 0 {
		// Resonance rose with length: the log is inconsistent (probably a
		// mislogged length); fall back to the textbook slope.
		slope = s0
		p.Confidence = 0
		p.Note = "The log is inconsistent (resonance did not rise when the element got shorter); using the textbook slope. Check the logged lengths."
	}
	if sxx > 0 && p.Confidence > 0 {
		p.Method = "fit"
	}
	anchorL, anchorF := last.LengthMm, last.FreqHz
	p.Cautious = len(pts) < 2 || p.Method != "fit"
	if len(pts) == 1 {
		// Use the exact ratio form for the very first estimate.
		nl := last.LengthMm * last.FreqHz / target
		p.NewLengthMm, p.CutMm = nl, last.LengthMm-nl
		p.MHzPerMm = -s0 / 1e6
		p.FirstCutMm, p.Advice = CutAdvice(p.CutMm, p.Cautious)
		return p, nil
	}
	nl := anchorL + (target-anchorF)/slope
	p.NewLengthMm, p.CutMm = nl, last.LengthMm-nl
	p.MHzPerMm = -slope / 1e6
	// Local slope of f = k/(L+δ) is −f/(L+δ)  ⇒  δ = −f/s − L.
	p.EndEffectMm = -anchorF/slope - anchorL
	p.FirstCutMm, p.Advice = CutAdvice(p.CutMm, p.Cautious)
	return p, nil
}

// posteriorSlope is the Bayesian line fit of PredictTrim: the data's slope
// sxy/sxx (centred sums), with measurement noise sigF (Hz), combined with
// the prior slope s0 (Hz/mm, ±trimPriorSpread). s0 = 0 means no prior: the
// data alone. conf is the data's share of the estimate.
func posteriorSlope(sxx, sxy, sigF, s0 float64) (slope, conf float64) {
	if sxx <= 0 {
		return s0, 0
	}
	sHat := sxy / sxx
	if s0 == 0 {
		return sHat, 1
	}
	precPrior := 1 / math.Pow(trimPriorSpread*s0, 2)
	precData := sxx / (sigF * sigF)
	return (sHat*precData + s0*precPrior) / (precData + precPrior), precData / (precData + precPrior)
}

// SlopeFit is a resonance-versus-length sensitivity learnt by LearnSlope.
type SlopeFit struct {
	MHzPerMm   float64 `json:"mhzPerMm"`   // > 0 when learnt (shortening raises the resonance)
	Confidence float64 `json:"confidence"` // the data's share of the estimate (see PredictTrim)
	Steps      int     `json:"steps"`      // length changes in the data
	// Inconsistent is set when the data say the resonance fell when the
	// element got shorter: nothing is learnt then.
	Inconsistent bool `json:"inconsistent,omitempty"`
}

// LearnSlope learns MHz/mm from several logs of elements of the same kind,
// each log one element (its lengths and resonances): the slope is shared,
// the lengths are not, so each log is centred on its own means and only
// the length changes within a log count. It is PredictTrim's fit (same
// noise model and prior) over the pooled logs. priorHzPerMm is the
// textbook f/L (> 0), or 0 to use the data alone. Logs with fewer than two
// usable points add nothing. MHzPerMm is 0 when nothing could be learnt.
func LearnSlope(logs [][]TrimPoint, priorHzPerMm float64) SlopeFit {
	var sxx, sxy, sumF float64
	var nF, steps int
	for _, lg := range logs {
		var pts []TrimPoint
		for _, p := range lg {
			if p.LengthMm > 0 && p.FreqHz > 0 {
				pts = append(pts, p)
			}
		}
		if len(pts) < 2 {
			continue
		}
		n := float64(len(pts))
		var mL, mF float64
		for _, p := range pts {
			mL += p.LengthMm / n
			mF += p.FreqHz / n
		}
		for i, p := range pts {
			sxx += (p.LengthMm - mL) * (p.LengthMm - mL)
			sxy += (p.LengthMm - mL) * (p.FreqHz - mF)
			sumF += p.FreqHz
			nF++
			if i > 0 && p.LengthMm != pts[i-1].LengthMm {
				steps++
			}
		}
	}
	fit := SlopeFit{Steps: steps}
	if sxx <= 0 || nF == 0 {
		return fit
	}
	slope, conf := posteriorSlope(sxx, sxy, trimNoiseFrac*sumF/float64(nF), -math.Abs(priorHzPerMm))
	if slope >= 0 {
		fit.Inconsistent = true
		return fit
	}
	fit.MHzPerMm, fit.Confidence = -slope/1e6, conf
	return fit
}
