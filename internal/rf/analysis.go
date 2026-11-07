package rf

import (
	"errors"
	"math"
)

// Band is a frequency range of interest (e.g. the EU 868 MHz SRD band).
type Band struct {
	Name string  `json:"name"`
	Lo   float64 `json:"lo"`            // Hz
	Hi   float64 `json:"hi"`            // Hz
	Ref  float64 `json:"ref,omitempty"` // offset reference, Hz (0 = centre)
}

// Valid reports whether the band is usable.
func (b Band) Valid() bool { return b.Hi > b.Lo && b.Lo > 0 }

// Derived holds per-point quantities derived from S11 (and optionally S21).
type Derived struct {
	GammaMag []float64 `json:"gammaMag"`
	SWR      []float64 `json:"swr"`
	RL       []float64 `json:"rl"`    // return loss, dB, positive
	S11dB    []float64 `json:"s11dB"` // = -RL
	S11Phase []float64 `json:"s11Ph"` // degrees
	R        []float64 `json:"r"`     // ohms
	X        []float64 `json:"x"`     // ohms
	S21dB    []float64 `json:"s21dB,omitempty"`
	S21Phase []float64 `json:"s21Ph,omitempty"`
}

// Bandwidth describes the contiguous range around the best match where
// SWR stays below Threshold.
type Bandwidth struct {
	Threshold float64 `json:"threshold"`
	Found     bool    `json:"found"`
	Lo        float64 `json:"lo"`
	Hi        float64 `json:"hi"`
	Width     float64 `json:"width"`
	// LoOpen/HiOpen are set when the region runs into the sweep edge, so the
	// true bandwidth is at least Width.
	LoOpen bool `json:"loOpen"`
	HiOpen bool `json:"hiOpen"`
	// FractionalPct is Width relative to the centre of the region, in percent.
	FractionalPct float64 `json:"fractionalPct"`
}

// Resonance is a zero crossing of the reactance X.
type Resonance struct {
	Freq float64 `json:"freq"`
	R    float64 `json:"r"`
	// Kind is "series" (X goes - to +, low R) or "parallel" (X goes + to -).
	Kind string `json:"kind"`
}

// BandSummary characterises the match inside a Band.
type BandSummary struct {
	Band         Band    `json:"band"`
	Points       int     `json:"points"` // measured points inside the band
	Covered      bool    `json:"covered"`
	WorstSWR     float64 `json:"worstSWR"`
	WorstSWRFreq float64 `json:"worstSWRFreq"`
	BestSWR      float64 `json:"bestSWR"`
	BestSWRFreq  float64 `json:"bestSWRFreq"`
	WorstRL      float64 `json:"worstRL"`
	PctBelow2    float64 `json:"pctBelow2"`
	PctBelow15   float64 `json:"pctBelow15"`
	Pass2        bool    `json:"pass2"`   // worst SWR < 2
	Pass15       bool    `json:"pass15"`  // worst SWR < 1.5
	Verdict      string  `json:"verdict"` // good / ok / bad / n/a
	// Nearest resonance dip to the band centre (if any dip was found).
	DipFreq    *float64 `json:"dipFreq,omitempty"`
	DipSWR     *float64 `json:"dipSWR,omitempty"`
	DipOffset  *float64 `json:"dipOffset,omitempty"`     // Hz from band reference
	DipEdgeOff *float64 `json:"dipEdgeOffset,omitempty"` // Hz from nearest band edge (0 inside)
	DipAdvice  string   `json:"dipAdvice,omitempty"`
}

// Summary is the headline analysis of a reflection sweep.
type Summary struct {
	Points      int           `json:"points"`
	MinSWR      float64       `json:"minSWR"`
	MinSWRFreq  float64       `json:"minSWRFreq"`
	MinSWRIndex int           `json:"minSWRIndex"`
	BestRL      float64       `json:"bestRL"` // return loss at best match
	ZAtMinR     float64       `json:"zAtMinR"`
	ZAtMinX     float64       `json:"zAtMinX"`
	Resonance   *Resonance    `json:"resonance,omitempty"` // series resonance nearest the best match
	Resonances  []Resonance   `json:"resonances"`
	BW2         Bandwidth     `json:"bw2"`
	BW15        Bandwidth     `json:"bw15"`
	Bands       []BandSummary `json:"bands"`
	Dips        []Dip         `json:"dips"`
	S21MaxDB    *float64      `json:"s21MaxDB,omitempty"`
	S21MaxFreq  *float64      `json:"s21MaxFreq,omitempty"`
}

// ErrEmpty is returned when there is nothing to analyse.
var ErrEmpty = errors.New("rf: empty sweep")

// Derive computes per-point quantities. s21 may be nil.
func Derive(s11, s21 []complex128) Derived {
	n := len(s11)
	d := Derived{
		GammaMag: make([]float64, n), SWR: make([]float64, n), RL: make([]float64, n),
		S11dB: make([]float64, n), S11Phase: make([]float64, n), R: make([]float64, n), X: make([]float64, n),
	}
	for i, g := range s11 {
		d.GammaMag[i] = Mag(g)
		d.SWR[i] = SWR(g)
		d.RL[i] = ReturnLoss(g)
		d.S11dB[i] = -d.RL[i]
		d.S11Phase[i] = PhaseDeg(g)
		z := Impedance(g)
		d.R[i], d.X[i] = real(z), imag(z)
	}
	if len(s21) == n && n > 0 {
		d.S21dB = make([]float64, n)
		d.S21Phase = make([]float64, n)
		for i, s := range s21 {
			d.S21dB[i] = DB(s)
			d.S21Phase[i] = PhaseDeg(s)
		}
	}
	return d
}

// Dip detection thresholds used by Analyze.
const (
	DipMinProminenceDB = 3.0
	DipMinRLdB         = 3.0
)

// Analyze computes the headline summary from frequencies (Hz, ascending) and
// derived per-point data. bands are summarised in order (may be empty).
func Analyze(freq []float64, d Derived, bands []Band) (Summary, error) {
	n := len(freq)
	if n == 0 || len(d.SWR) != n {
		return Summary{}, ErrEmpty
	}
	s := Summary{Points: n, MinSWR: math.Inf(1)}
	for i, v := range d.SWR {
		if v < s.MinSWR {
			s.MinSWR, s.MinSWRIndex = v, i
		}
	}
	i := s.MinSWRIndex
	s.MinSWRFreq = freq[i]
	s.BestRL = d.RL[i]
	s.ZAtMinR, s.ZAtMinX = d.R[i], d.X[i]

	s.Resonances = FindResonances(freq, d.R, d.X)
	best := -1
	for k, r := range s.Resonances {
		if r.Kind != "series" {
			continue
		}
		if best < 0 || math.Abs(r.Freq-s.MinSWRFreq) < math.Abs(s.Resonances[best].Freq-s.MinSWRFreq) {
			best = k
		}
	}
	if best >= 0 {
		r := s.Resonances[best]
		s.Resonance = &r
	}

	s.BW2 = SWRBandwidth(freq, d.SWR, i, 2)
	s.BW15 = SWRBandwidth(freq, d.SWR, i, 1.5)
	s.Dips = FindDips(freq, d, DipMinProminenceDB, DipMinRLdB)
	ClassifyDips(s.Dips, LoRaBands)
	s.Bands = []BandSummary{}
	for _, b := range bands {
		if !b.Valid() {
			continue
		}
		bs := SummarizeBand(freq, d, b)
		if dp := NearestDip(s.Dips, b); dp != nil {
			f, sw, off := dp.Freq, dp.SWR, dp.Freq-b.Reference()
			edge := 0.0
			if f < b.Lo {
				edge = f - b.Lo
			} else if f > b.Hi {
				edge = f - b.Hi
			}
			bs.DipFreq, bs.DipSWR, bs.DipOffset, bs.DipEdgeOff = &f, &sw, &off, &edge
			bs.DipAdvice = dp.Advice
		}
		s.Bands = append(s.Bands, bs)
	}
	if len(d.S21dB) == n {
		mx, mf := math.Inf(-1), 0.0
		for k, v := range d.S21dB {
			if v > mx {
				mx, mf = v, freq[k]
			}
		}
		s.S21MaxDB, s.S21MaxFreq = &mx, &mf
	}
	return s, nil
}

// SWRBandwidth walks outwards from index center while SWR < thr and returns
// the region, with edges linearly interpolated between sweep points.
func SWRBandwidth(freq, swr []float64, center int, thr float64) Bandwidth {
	bw := Bandwidth{Threshold: thr}
	n := len(freq)
	if n == 0 || center < 0 || center >= n || swr[center] >= thr {
		return bw
	}
	bw.Found = true
	lo := center
	for lo > 0 && swr[lo-1] < thr {
		lo--
	}
	if lo == 0 {
		bw.Lo, bw.LoOpen = freq[0], true
	} else {
		bw.Lo = crossing(freq[lo-1], freq[lo], swr[lo-1], swr[lo], thr)
	}
	hi := center
	for hi < n-1 && swr[hi+1] < thr {
		hi++
	}
	if hi == n-1 {
		bw.Hi, bw.HiOpen = freq[n-1], true
	} else {
		bw.Hi = crossing(freq[hi], freq[hi+1], swr[hi], swr[hi+1], thr)
	}
	bw.Width = bw.Hi - bw.Lo
	if c := (bw.Hi + bw.Lo) / 2; c > 0 {
		bw.FractionalPct = 100 * bw.Width / c
	}
	return bw
}

// crossing returns the frequency between (f1,v1) and (f2,v2) where the
// linearly interpolated value equals thr.
func crossing(f1, f2, v1, v2, thr float64) float64 {
	if v2 == v1 {
		return (f1 + f2) / 2
	}
	t := (thr - v1) / (v2 - v1)
	t = math.Max(0, math.Min(1, t))
	return f1 + t*(f2-f1)
}

// FindResonances returns the zero crossings of X. A crossing from negative
// to positive reactance is a series resonance; positive to negative is a
// parallel (anti-)resonance.
func FindResonances(freq, r, x []float64) []Resonance {
	out := []Resonance{}
	for i := 0; i+1 < len(x); i++ {
		a, b := x[i], x[i+1]
		if a == 0 && i > 0 {
			continue // counted as the end point of the previous segment
		}
		if (a < 0 && b >= 0) || (a > 0 && b <= 0) || (a == 0 && i == 0 && b != 0) {
			t := 0.0
			if b != a {
				t = -a / (b - a)
			}
			res := Resonance{
				Freq: freq[i] + t*(freq[i+1]-freq[i]),
				R:    r[i] + t*(r[i+1]-r[i]),
				Kind: "series",
			}
			if b < a {
				res.Kind = "parallel"
			}
			out = append(out, res)
		}
	}
	return out
}

// interp linearly interpolates y at frequency f (freq ascending, f within range).
func interp(freq, y []float64, f float64) float64 {
	n := len(freq)
	if f <= freq[0] {
		return y[0]
	}
	if f >= freq[n-1] {
		return y[n-1]
	}
	lo, hi := 0, n-1
	for hi-lo > 1 {
		m := (lo + hi) / 2
		if freq[m] <= f {
			lo = m
		} else {
			hi = m
		}
	}
	if freq[hi] == freq[lo] {
		return y[lo]
	}
	t := (f - freq[lo]) / (freq[hi] - freq[lo])
	return y[lo] + t*(y[hi]-y[lo])
}

// SummarizeBand reports the worst/best match inside band. The band edges are
// included by interpolation so a coarse sweep does not hide a bad band edge.
// Covered is false when the sweep does not span the whole band; then only the
// overlapping part is evaluated.
func SummarizeBand(freq []float64, d Derived, band Band) BandSummary {
	bs := BandSummary{Band: band, WorstSWR: 0, BestSWR: math.Inf(1)}
	n := len(freq)
	if n == 0 {
		return bs
	}
	bs.Covered = freq[0] <= band.Lo && freq[n-1] >= band.Hi
	lo, hi := math.Max(band.Lo, freq[0]), math.Min(band.Hi, freq[n-1])
	if lo > hi {
		bs.BestSWR = 0
		bs.Verdict = "n/a"
		return bs
	}
	type sample struct{ f, swr, rl float64 }
	samples := []sample{
		{lo, interp(freq, d.SWR, lo), interp(freq, d.RL, lo)},
		{hi, interp(freq, d.SWR, hi), interp(freq, d.RL, hi)},
	}
	for i, f := range freq {
		if f >= lo && f <= hi {
			samples = append(samples, sample{f, d.SWR[i], d.RL[i]})
			bs.Points++
		}
	}
	below2, below15 := 0, 0
	bs.WorstRL = math.Inf(1)
	for _, s := range samples {
		if s.swr > bs.WorstSWR {
			bs.WorstSWR, bs.WorstSWRFreq = s.swr, s.f
		}
		if s.swr < bs.BestSWR {
			bs.BestSWR, bs.BestSWRFreq = s.swr, s.f
		}
		if s.rl < bs.WorstRL {
			bs.WorstRL = s.rl
		}
		if s.swr < 2 {
			below2++
		}
		if s.swr < 1.5 {
			below15++
		}
	}
	bs.PctBelow2 = 100 * float64(below2) / float64(len(samples))
	bs.PctBelow15 = 100 * float64(below15) / float64(len(samples))
	bs.Pass2 = bs.Covered && bs.WorstSWR < 2
	bs.Pass15 = bs.Covered && bs.WorstSWR < 1.5
	bs.Verdict = Verdict(bs.WorstSWR)
	return bs
}
