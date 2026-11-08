package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- BATCH: a lot of antennas measured one after the other ----------
//
// A batch is a lot of "identical" antennas (made or bought together) on one
// band profile. Each antenna is captured with its full sweep, so every
// figure can be recomputed; the batch shows whether each antenna is tuned
// (TUNE's target and tolerance, the trim calculator's cut) and how
// consistent the lot is (mean, spread, outliers). Batches are stored as
// batches/<id>.json.

// Batch is one lot. Its sweep (profile, range, points) is fixed when it is
// created: captures of another sweep are refused.
type Batch struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Notes        string         `json:"notes,omitempty"`
	Profile      string         `json:"profile"`
	Label        string         `json:"label"` // "869 MHz profile"
	Start        float64        `json:"start"`
	Stop         float64        `json:"stop"`
	Points       int            `json:"points"`
	Bands        []rf.Band      `json:"bands"`
	Target       float64        `json:"target"`                 // Hz, TUNE's target: the primary band's reference
	LengthMm     float64        `json:"lengthMm,omitempty"`     // nominal element length (for trim advice in mm)
	Trim         string         `json:"trim,omitempty"`         // trim log whose learnt MHz/mm is used
	Link         string         `json:"link,omitempty"`         // product / source page (see CleanLink)
	Photo        string         `json:"photo,omitempty"`        // one photo of the lot (data: URL)
	PhotoCaption string         `json:"photoCaption,omitempty"` // small print under the photo
	LinkNote     string         `json:"linkNote,omitempty"`     // small print under the link
	Created      time.Time      `json:"created"`
	Updated      time.Time      `json:"updated"`
	NextID       int            `json:"nextId"`
	Freq         []float64      `json:"freq,omitempty"` // the grid, set by the first capture
	Antennas     []BatchAntenna `json:"antennas"`
	Setup        *SetupNote     `json:"setup,omitempty"` // last setup check run during this batch
}

// BatchAntenna is one captured antenna.
type BatchAntenna struct {
	ID       int          `json:"id"`    // stable, 1-based
	Label    string       `json:"label"` // "#3" or a serial number
	Time     time.Time    `json:"time"`
	Cal      string       `json:"cal"` // calibration state at capture (see calLabelLocked)
	CalOK    bool         `json:"calOK"`
	Auto     bool         `json:"auto,omitempty"` // captured by auto-capture
	AvgN     int          `json:"avgN,omitempty"`
	Warnings []string     `json:"warnings,omitempty"` // measurement warning codes at capture
	Redone   int          `json:"redone,omitempty"`   // times it was measured again
	Rep      *RepeatBrief `json:"rep,omitempty"`      // Measure ×N: repeatability of the N sweeps
	S11      [][2]float64 `json:"s11"`
}

// Band returns the batch's primary band.
func (b *Batch) Band() rf.Band {
	if len(b.Bands) > 0 {
		return b.Bands[0]
	}
	return rf.Band{}
}

// AimHz is TUNE's fine "on target" aim around the target (rf.OnTargetPct,
// 0.15 % of the target: ±1.30 MHz at 869 MHz, ±0.65 MHz at 433.92 MHz,
// ±1.37 MHz at 915 MHz). It is a tuning aim, not an acceptance criterion:
// the acceptance setting decides (see accept.go).
func (b *Batch) AimHz() float64 { return b.Target * rf.OnTargetPct / 100 }

const (
	maxBatchAntennas = 200
	maxBatchLabel    = 40
)

var batchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)

func batchPath(id string) (string, error) {
	if !batchIDRe.MatchString(id) {
		return "", errors.New("bad batch id")
	}
	return "batches/" + id + ".json", nil
}

func newBatchID(t time.Time) string {
	return t.Format("20060102-150405") + fmt.Sprintf("-%03d", t.Nanosecond()/1e6)
}

// clean trims the text fields and checks them.
func (b *Batch) clean() error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 80 {
		return errors.New("give the batch a name (1-80 characters)")
	}
	b.Notes = strings.TrimSpace(b.Notes)
	if len(b.Notes) > 2000 {
		b.Notes = b.Notes[:2000]
	}
	b.Trim = strings.TrimSpace(b.Trim)
	if b.LengthMm < 0 || b.LengthMm > 1e5 || math.IsNaN(b.LengthMm) {
		return errors.New("length must be between 0 and 100000 mm")
	}
	link, err := CleanLink(b.Link)
	if err != nil {
		return err
	}
	b.Link = link
	return checkPhoto(b.Photo)
}

// SaveBatch writes a batch.
func (st *Store) SaveBatch(b *Batch) error {
	p, err := batchPath(b.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return st.b.WriteFile(p, data)
}

// LoadBatch reads a batch.
func (st *Store) LoadBatch(id string) (*Batch, error) {
	p, err := batchPath(id)
	if err != nil {
		return nil, err
	}
	data, err := st.b.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var b Batch
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("batch %s: %w", id, err)
	}
	if b.Antennas == nil {
		b.Antennas = []BatchAntenna{}
	}
	return &b, nil
}

// DeleteBatch removes a batch.
func (st *Store) DeleteBatch(id string) error {
	p, err := batchPath(id)
	if err != nil {
		return err
	}
	return st.b.Remove(p)
}

// BatchInfo is one row of GET /api/batches.
type BatchInfo struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Profile string         `json:"profile"`
	Label   string         `json:"label"`
	Created time.Time      `json:"created"`
	Updated time.Time      `json:"updated"`
	N       int            `json:"n"`
	Yield   map[string]int `json:"yield"` // good / ok / bad / n/a
	Tuned   int            `json:"tuned"`
	OK      int            `json:"ok"`    // accepted (tuned + ok)
	Grade   string         `json:"grade"` // fine, worth, must
}

// ListBatches lists the stored batches, most recently updated first.
func (st *Store) ListBatches(accept func(profile string) rf.Acceptance) []BatchInfo {
	out := []BatchInfo{}
	for _, f := range st.listMatch("batches", "*.json") {
		b, err := st.LoadBatch(strings.TrimSuffix(f.Name, ".json"))
		if err != nil {
			continue
		}
		v := batchView(b, nil, accept(b.Profile))
		out = append(out, BatchInfo{ID: b.ID, Name: b.Name, Profile: b.Profile, Label: b.Label, Created: b.Created,
			Updated: b.Updated, N: len(b.Antennas), Yield: v.Summary.Yield, Tuned: v.Summary.Tuned, OK: v.Summary.OK, Grade: v.Summary.Grade})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// ---------- per-antenna figures ----------

// Tuning says whether one antenna is tuned to the target and what to do.
type Tuning struct {
	Status string `json:"status"` // tuned, ok, shorten, lengthen, bad, none
	Text   string `json:"text"`   // short: "✓ tuned", "↓ shorten 0.9 mm"
	Detail string `json:"detail"` // a sentence
	// CutMm is the full length change (> 0: cut this much, < 0: lengthen);
	// 0 when no length is known (then ScalePct says it in %).
	CutMm      float64 `json:"cutMm"`
	FirstCutMm float64 `json:"firstCutMm"` // what to cut now (cautious until MHz/mm is learnt)
	Cautious   bool    `json:"cautious"`
	Method     string  `json:"method"`   // fit (learnt MHz/mm), ratio (length × f ratio), pct (no length)
	ScalePct   float64 `json:"scalePct"` // length change in % (< 0: shorten)
}

// AntennaMetrics are the figures of one antenna. Zero when unknown.
type AntennaMetrics struct {
	Found         bool      `json:"found"`         // a resonance near the target
	ResFreq       float64   `json:"resFreq"`       // Hz: TUNE's resonance (return-loss peak nearest the target)
	ResSWR        float64   `json:"resSWR"`        // SWR there
	Offset        float64   `json:"offset"`        // Hz, resonance − target
	OffsetPct     float64   `json:"offsetPct"`     // %
	SWRTarget     float64   `json:"swrTarget"`     // SWR at the target (band reference)
	RLTarget      float64   `json:"rlTarget"`      // dB, return loss at the target
	WorstSWR      float64   `json:"worstSWR"`      // worst SWR in the band
	WorstFreq     float64   `json:"worstFreq"`     //
	PctBelow2     float64   `json:"pctBelow2"`     // % of the band with SWR < 2
	PctBelowLimit float64   `json:"pctBelowLimit"` // % of the band with SWR below the acceptance limit
	BW2           float64   `json:"bw2"`           // Hz, SWR < 2 bandwidth around the resonance
	BW2Open       bool      `json:"bw2Open"`       // the SWR < 2 region runs into the sweep edge
	Verdict       string    `json:"verdict"`       // band verdict against the limit: good / ok / bad / n/a
	Margin        rf.Margin `json:"margin"`        // band margin at the SWR limit (the antenna's own Auto window)
	Tuning        Tuning    `json:"tuning"`
	Outliers      []string  `json:"outliers"` // keys of the batch statistics this antenna is an outlier of
}

// trimSlope is where the MHz/mm of the trim advice comes from.
type trimSlope struct {
	MHzPerMm float64 // learnt (fit), 0 = not learnt
	From     string  // trim log name
}

// antennaMetrics computes the figures of s11 on the batch's grid. It uses
// the same code as the main view (rf.Analyze through buildResult) and TUNE
// (rf.Tune), so the numbers agree with what the user sees there. acc is
// the acceptance setting of the batch's profile.
func antennaMetrics(b *Batch, s11 [][2]float64, slope trimSlope, acc rf.Acceptance) (AntennaMetrics, *Result) {
	cfg := Config{Profile: b.Profile, Start: b.Start, Stop: b.Stop, Points: b.Points, Bands: b.Bands, Accept: acc}
	r := buildResult(b.Freq, toComplex(s11), nil, cfg, time.Time{}, 0)
	m := AntennaMetrics{Outliers: []string{}, Verdict: "n/a"}
	if len(b.Freq) < 3 {
		m.Tuning = Tuning{Status: "none", Text: "✗ no data"}
		return m, r
	}
	win := acc.Resolve(b.Target, nil)
	ti := rf.TuneAt(r.Freq, r.Derived, b.Target, win.AimHz, acc.Limit())
	m.SWRTarget, m.RLTarget = ti.SWRAtTarget, ti.RLAtTarget
	m.Found = ti.Found
	band := b.Band()
	if ti.Found {
		m.ResFreq, m.ResSWR, m.Offset, m.OffsetPct = ti.ResFreq, ti.SWRAtRes, ti.Offset, ti.OffsetPct
		bd := band
		bd.Ref = b.Target
		if dp := rf.NearestDip(r.Summary.Dips, bd); dp != nil && dp.BW2.Found && math.Abs(dp.Freq-ti.ResFreq) < 0.02*b.Target {
			m.BW2, m.BW2Open = dp.BW2.Width, dp.BW2.LoOpen || dp.BW2.HiOpen
		}
	}
	if len(r.Summary.Bands) > 0 {
		bs := r.Summary.Bands[0]
		m.WorstSWR, m.WorstFreq, m.PctBelow2, m.PctBelowLimit, m.Verdict = bs.WorstSWR, bs.WorstSWRFreq, bs.PctBelow2, bs.PctBelowLimit, bs.Verdict
	}
	if band.Valid() {
		m.Margin = rf.BandMargin(r.Freq, r.Derived.SWR, band, b.Target, m.ResFreq, acc.Limit())
	}
	m.Tuning = tuningOf(b, m, slope, acc)
	return m, r
}

// tuningOf grades one antenna. The acceptance decides; the length advice
// comes from the trim calculator: from the learnt MHz/mm when a trim log
// has learnt it, else the first estimate L × f_res / f_target with TUNE's
// cautious first cut, else (no length given) the length change in %.
//
// The statuses: none (no resonance); bad (the band is not covered at the
// SWR limit: "poor match" when centring would not help, "out of band"
// otherwise); tuned (within TUNE's ± 0.15 % aim, or the fixed tolerance);
// ok (Auto: the band is covered at the limit, off the fine aim: the
// margin used is given and the trim is optional); shorten / lengthen (a
// fixed tolerance: outside it).
func tuningOf(b *Batch, m AntennaMetrics, slope trimSlope, acc rf.Acceptance) Tuning {
	win := acc.Resolve(b.Target, &m.Margin)
	aim := win.AimHz
	auto := win.Mode == rf.TolAuto
	tol := aim
	if !auto {
		tol = win.Hz
	}
	limit := fmt.Sprintf("SWR < %.1f", acc.Limit())
	t := Tuning{}
	if !m.Found {
		t.Status, t.Text, t.Detail = "none", "✗ no resonance", "No resonance near the target in this sweep."
		return t
	}
	t.ScalePct = 100 * (m.ResFreq/b.Target - 1) // f ∝ 1/L: a resonance 1 % low needs a 1 % shorter element
	full := 0.0
	switch {
	case slope.MHzPerMm > 0:
		full = (b.Target - m.ResFreq) / 1e6 / slope.MHzPerMm
		t.Method = "fit"
	case b.LengthMm > 0:
		if p, err := rf.PredictTrim([]rf.TrimPoint{{LengthMm: b.LengthMm, FreqHz: m.ResFreq}}, b.Target); err == nil {
			full = p.CutMm
			t.Method = "ratio"
		}
	default:
		t.Method = "pct"
	}
	if t.Method != "pct" {
		t.CutMm = full
		t.Cautious = t.Method == "ratio"
		t.FirstCutMm, _ = rf.CutAdvice(full, t.Cautious)
	}
	where := fmt.Sprintf("resonance %s MHz, %s MHz (%s %%) from %s MHz", mhzF(m.ResFreq), signed2(m.Offset/1e6), signed2(m.OffsetPct), mhzF(b.Target))
	move := func() string {
		dir := "shorten"
		if m.Offset > 0 {
			dir = "lengthen"
		}
		if t.Method == "pct" {
			return fmt.Sprintf("%s ≈%.1f %%", dir, math.Abs(t.ScalePct))
		}
		return fmt.Sprintf("%s %.1f mm", dir, math.Abs(t.CutMm))
	}
	poor := !m.Margin.OK || m.Margin.RoomDown+m.Margin.RoomUp < 0
	switch {
	case m.Verdict == "bad" && (math.Abs(m.Offset) <= tol || (auto && poor)):
		t.Status, t.Text = "bad", "✗ poor match"
		t.Detail = fmt.Sprintf("The band is not covered at %s (worst %s) and moving the resonance would not fix it (%s): the match itself is poor, not the length.", limit, swrF(m.WorstSWR), where)
	case m.Verdict == "bad":
		t.Status, t.Text = "bad", "✗ out of band"
		t.Detail = fmt.Sprintf("The band is not covered at %s (worst %s); %s: %s.", limit, swrF(m.WorstSWR), where, move())
	case math.Abs(m.Offset) <= tol:
		t.Status, t.Text = "tuned", "✓ tuned"
		t.Detail = fmt.Sprintf("On target: %s, within ±%.2f MHz.", where, tol/1e6)
		if auto && m.Margin.OK {
			t.Detail += fmt.Sprintf(" The band is covered at %s (worst %s, %.0f %% of the margin used).", limit, swrF(m.WorstSWR), m.Margin.UsedPct)
		}
	case auto:
		t.Status = "ok"
		t.Text = fmt.Sprintf("✓ OK · %.0f %% margin used", math.Min(m.Margin.UsedPct, 100))
		t.Detail = fmt.Sprintf("Covers the band at %s (worst %s); %s: %.0f %% of the margin used. Optional: %s would centre it on the target.",
			limit, swrF(m.WorstSWR), where, m.Margin.UsedPct, move())
	case m.Offset < 0:
		t.Status, t.Text = "shorten", "↓ "+move()
		t.Detail = fmt.Sprintf("Too long (resonates low): %s.", where)
	default:
		t.Status, t.Text = "lengthen", "↑ "+move()
		t.Detail = fmt.Sprintf("Too short (resonates high): %s. Do not cut.", where)
	}
	if (t.Status == "shorten" || t.Status == "ok" && m.Offset < 0) && t.Cautious && t.FirstCutMm < t.CutMm-0.05 {
		if t.FirstCutMm > 0 {
			t.Detail += fmt.Sprintf(" First cut %.1f mm (full estimate %.1f mm): MHz/mm is not learnt yet.", t.FirstCutMm, t.CutMm)
		} else {
			t.Detail += fmt.Sprintf(" Trim less than %.1f mm at a time and measure.", rf.CutStepMm)
		}
	}
	return t
}

func signed2(v float64) string {
	r := math.Round(v*100) / 100
	if r == 0 {
		return "0.00"
	}
	return fmt.Sprintf("%+.2f", r)
}

// ---------- batch statistics ----------

// BatchStatRow is one metric over the lot.
type BatchStatRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
	Dec   int    `json:"dec"`
	rf.Stat
	Outliers []int `json:"outliers"` // antenna IDs
}

// BatchSummary is everything computed over the lot.
type BatchSummary struct {
	N          int            `json:"n"`
	Stats      []BatchStatRow `json:"stats"`
	Yield      map[string]int `json:"yield"`
	Tuned      int            `json:"tuned"`
	OKN        int            `json:"okOnly"` // ok (Auto: covered, off the fine aim)
	OK         int            `json:"ok"`     // accepted: tuned + ok
	Shorten    int            `json:"shorten"`
	Lengthen   int            `json:"lengthen"`
	Bad        int            `json:"bad"`
	None       int            `json:"none"`
	TuningLine string         `json:"tuningLine"` // "7 of 10 OK (3 on the ± 0.15 % aim); 1 bad"
	// Acceptance: the lot's window (Auto: the median of the antennas'
	// own windows), TUNE's fine aim and the criteria text printed on sheets.
	Window      rf.Window `json:"window"`
	ToleranceHz float64   `json:"toleranceHz"` // = Window.Hz (the strip's window)
	AimHz       float64   `json:"aimHz"`
	OffMin      float64   `json:"offMin"` // Hz, lot window (median of the antennas' OffMin / OffMax)
	OffMax      float64   `json:"offMax"`
	Criteria    string    `json:"criteria"` // "Acceptance: band SWR < 2.0, resonance …"
	// Lot figures (without the resonance outliers).
	BiasHz      float64   `json:"biasHz"`      // mean offset from the target (systematic)
	BiasPct     float64   `json:"biasPct"`     // %
	BiasCIHz    float64   `json:"biasCIHz"`    // half-width of the mean's 95 % confidence interval (n ≥ 2)
	SigmaHz     float64   `json:"sigmaHz"`     // SD of the resonance (unit-to-unit spread)
	SpreadHz    float64   `json:"spreadHz"`    // range of the resonance
	NRes        int       `json:"nRes"`        // antennas in those figures
	BiasCutMm   float64   `json:"biasCutMm"`   //
	BiasUsedPct float64   `json:"biasUsedPct"` // share of the lot window the mean offset uses
	MarginMax   float64   `json:"marginMax"`   // largest margin used by one antenna, %
	WorstSWR    float64   `json:"worstSWR"`    // worst in-band SWR of the lot
	RepeatHz    float64   `json:"repeatHz"`    // pooled within-antenna σ of the resonance (Measure ×N), 0: none
	NRepeat     int       `json:"nRepeat"`     // antennas measured ×N
	Grade       string    `json:"grade"`       // fine, worth, must ("" with no antenna)
	Headline    string    `json:"headline"`    // the graded first sentence
	Conclusion  []string  `json:"conclusion"`  // plain words
	CalNote     string    `json:"calNote"`
	CalOK       bool      `json:"calOK"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
	Slope       string    `json:"slope"` // where the mm come from
}

// batchMetricDefs are the statistics rows: key, label, unit, decimals,
// value, and the outlier floor (the measurement repeatability of the
// metric, see rf.Outliers).
var batchMetricDefs = []struct {
	key, label, unit string
	dec              int
	val              func(m AntennaMetrics) float64
	floor            func(median float64) float64
}{
	{"res", "Resonance", "MHz", 2, func(m AntennaMetrics) float64 { return ifFound(m, m.ResFreq/1e6) },
		func(md float64) float64 { return 0.001 * math.Abs(md) }}, // 0.1 % (the trim model's scatter)
	{"offset", "Offset from target", "MHz", 2, func(m AntennaMetrics) float64 { return ifFound(m, m.Offset/1e6) }, nil},
	{"swrTarget", "SWR at target", "", 2, func(m AntennaMetrics) float64 { return m.SWRTarget },
		func(float64) float64 { return 0.05 }},
	{"worstSWR", "Worst SWR in band", "", 2, func(m AntennaMetrics) float64 { return nz(m.WorstSWR) },
		func(float64) float64 { return 0.05 }},
	{"bw2", "SWR < 2 bandwidth", "MHz", 1, func(m AntennaMetrics) float64 {
		if m.BW2 <= 0 || m.BW2Open {
			return math.NaN()
		}
		return m.BW2 / 1e6
	}, func(md float64) float64 { return 0.02 * math.Abs(md) }},
	{"rlTarget", "Return loss at target", "dB", 1, func(m AntennaMetrics) float64 { return m.RLTarget },
		func(float64) float64 { return 0.5 }},
	{"cover", "Band with SWR < limit", "%", 0, func(m AntennaMetrics) float64 { return m.PctBelowLimit },
		func(float64) float64 { return 5 }},
}

func ifFound(m AntennaMetrics, v float64) float64 {
	if !m.Found {
		return math.NaN()
	}
	return v
}

func nz(v float64) float64 {
	if v <= 0 {
		return math.NaN()
	}
	return v
}

// BatchAntennaView is one antenna as shown (no S11, its SWR instead).
type BatchAntennaView struct {
	ID       int            `json:"id"`
	Label    string         `json:"label"`
	Time     time.Time      `json:"time"`
	Cal      string         `json:"cal"`
	CalOK    bool           `json:"calOK"`
	Auto     bool           `json:"auto,omitempty"`
	AvgN     int            `json:"avgN,omitempty"`
	Warnings []string       `json:"warnings"`
	Redone   int            `json:"redone,omitempty"`
	Rep      *RepeatBrief   `json:"rep,omitempty"`
	Metrics  AntennaMetrics `json:"metrics"`
	SWR      []float64      `json:"swr"`
}

// BatchView is GET /api/batches/{id}.
type BatchView struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Notes        string             `json:"notes"`
	Profile      string             `json:"profile"`
	Label        string             `json:"label"`
	Start        float64            `json:"start"`
	Stop         float64            `json:"stop"`
	Points       int                `json:"points"`
	Bands        []rf.Band          `json:"bands"`
	Target       float64            `json:"target"`
	LengthMm     float64            `json:"lengthMm"`
	Trim         string             `json:"trim"`
	Link         string             `json:"link"`
	LinkText     string             `json:"linkText"`
	Photo        string             `json:"photo"`
	PhotoCaption string             `json:"photoCaption"`
	LinkNote     string             `json:"linkNote"`
	Setup        *SetupNote         `json:"setup,omitempty"`
	Accept       rf.Acceptance      `json:"accept"`
	AvgSWR       []float64          `json:"avgSWR"` // SWR of the lot average (vector average of S11)
	Created      time.Time          `json:"created"`
	Updated      time.Time          `json:"updated"`
	Freq         []float64          `json:"freq"`
	Antennas     []BatchAntennaView `json:"antennas"`
	EnvMin       []float64          `json:"envMin"`
	EnvMax       []float64          `json:"envMax"`
	Summary      BatchSummary       `json:"summary"`
	Version      int                `json:"version"`
	HasUndo      bool               `json:"hasUndo"`
}

// batchView computes every figure of b with the acceptance setting acc.
// slope may be nil (no learnt MHz/mm).
func batchView(b *Batch, slope *trimSlope, acc rf.Acceptance) BatchView {
	sl := trimSlope{}
	if slope != nil {
		sl = *slope
	}
	v := BatchView{ID: b.ID, Name: b.Name, Notes: b.Notes, Profile: b.Profile, Label: b.Label, Start: b.Start, Stop: b.Stop,
		Points: b.Points, Bands: b.Bands, Target: b.Target, LengthMm: b.LengthMm, Trim: b.Trim, Link: b.Link,
		LinkText: LinkText(b.Link), Photo: b.Photo, PhotoCaption: b.PhotoCaption, LinkNote: b.LinkNote,
		Created: b.Created, Updated: b.Updated, Freq: b.Freq, Antennas: []BatchAntennaView{}, Setup: b.Setup, Accept: acc}
	if v.Freq == nil {
		v.Freq = []float64{}
	}
	if v.Bands == nil {
		v.Bands = []rf.Band{}
	}
	ms := make([]AntennaMetrics, len(b.Antennas))
	var all [][]complex128
	for i, a := range b.Antennas {
		m, r := antennaMetrics(b, a.S11, sl, acc)
		ms[i] = m
		av := BatchAntennaView{ID: a.ID, Label: a.Label, Time: a.Time, Cal: a.Cal, CalOK: a.CalOK, Auto: a.Auto, AvgN: a.AvgN,
			Warnings: a.Warnings, Redone: a.Redone, Rep: a.Rep, SWR: r.Derived.SWR}
		if av.Warnings == nil {
			av.Warnings = []string{}
		}
		if len(v.EnvMin) == 0 {
			v.EnvMin, v.EnvMax = append([]float64(nil), av.SWR...), append([]float64(nil), av.SWR...)
		} else if len(av.SWR) == len(v.EnvMin) {
			for k, s := range av.SWR {
				v.EnvMin[k], v.EnvMax[k] = math.Min(v.EnvMin[k], s), math.Max(v.EnvMax[k], s)
			}
		}
		if len(a.S11) == len(b.Freq) {
			all = append(all, toComplex(a.S11))
		}
		v.Antennas = append(v.Antennas, av)
	}
	if len(all) > 0 {
		// The lot average: the vector average of every antenna's S11.
		v.AvgSWR = rf.Derive(rf.Average(all), nil).SWR
	}
	v.Summary = batchSummary(b, ms, sl, acc)
	for i := range v.Antennas {
		v.Antennas[i].Metrics = ms[i]
	}
	return v
}

// lotWindow is the acceptance window of the lot: the fixed tolerance, or
// with Auto the median of the antennas' own windows (rf.BandMargin).
func lotWindow(b *Batch, ms []AntennaMetrics, acc rf.Acceptance) (rf.Window, float64, float64) {
	if acc.Mode() != rf.TolAuto {
		w := acc.Resolve(b.Target, nil)
		return w, -w.Hz, w.Hz
	}
	var win, lo, hi []float64
	open := false
	for _, m := range ms {
		if m.Found && m.Margin.OK && m.Margin.Window > 0 {
			win, lo, hi = append(win, m.Margin.Window), append(lo, -m.Margin.OffMin), append(hi, m.Margin.OffMax)
			open = open || m.Margin.Open
		}
	}
	if len(win) == 0 {
		any := false
		for _, m := range ms {
			any = any || m.Margin.OK
		}
		var mp *rf.Margin
		if any {
			mp = &rf.Margin{OK: true}
		}
		return acc.Resolve(b.Target, mp), 0, 0
	}
	m := rf.Margin{OK: true, Window: rf.MedianWindow(win), Open: open}
	return acc.Resolve(b.Target, &m), -rf.MedianWindow(lo), rf.MedianWindow(hi)
}

// batchSummary computes the statistics, flags outliers in ms (in place)
// and writes the conclusion.
func batchSummary(b *Batch, ms []AntennaMetrics, sl trimSlope, acc rf.Acceptance) BatchSummary {
	s := BatchSummary{N: len(ms), Yield: map[string]int{"good": 0, "ok": 0, "bad": 0, "n/a": 0}, AimHz: b.AimHz(),
		Stats: []BatchStatRow{}, Conclusion: []string{}}
	s.Window, s.OffMin, s.OffMax = lotWindow(b, ms, acc)
	s.ToleranceHz = s.Window.Hz
	s.Criteria = "Acceptance: " + s.Window.Text
	limit := acc.Limit()
	for _, def := range batchMetricDefs {
		x := make([]float64, len(ms))
		for i, m := range ms {
			x[i] = def.val(m)
		}
		row := BatchStatRow{Key: def.key, Label: def.label, Unit: def.unit, Dec: def.dec, Stat: rf.Describe(x), Outliers: []int{}}
		if def.key == "cover" {
			row.Label = fmt.Sprintf("Band with SWR < %.1f", limit)
		}
		if def.floor != nil {
			for i, o := range rf.Outliers(x, def.floor(row.Median)) {
				if o {
					row.Outliers = append(row.Outliers, b.Antennas[i].ID)
					ms[i].Outliers = append(ms[i].Outliers, def.key)
				}
			}
		}
		s.Stats = append(s.Stats, row)
	}
	shortMm, longMm := []float64{}, []float64{}
	calBad := 0
	var repSD []float64
	var repN []int
	for i, m := range ms {
		s.Yield[verdictKey(m.Verdict)]++
		switch m.Tuning.Status {
		case "tuned":
			s.Tuned++
		case "ok":
			s.OKN++
		case "shorten":
			s.Shorten++
			if m.Tuning.Method != "pct" {
				shortMm = append(shortMm, math.Abs(m.Tuning.CutMm))
			}
		case "lengthen":
			s.Lengthen++
			if m.Tuning.Method != "pct" {
				longMm = append(longMm, math.Abs(m.Tuning.CutMm))
			}
		case "bad":
			s.Bad++
		default:
			s.None++
		}
		if m.WorstSWR > s.WorstSWR {
			s.WorstSWR = m.WorstSWR
		}
		if m.Found && m.Margin.OK {
			s.MarginMax = math.Max(s.MarginMax, m.Margin.UsedPct)
		}
		a := b.Antennas[i]
		if !a.CalOK {
			calBad++
		}
		if a.Rep != nil && a.Rep.N >= 2 {
			repSD, repN = append(repSD, a.Rep.ResSD), append(repN, a.Rep.N)
		}
		if s.First.IsZero() || a.Time.Before(s.First) {
			s.First = a.Time
		}
		if a.Time.After(s.Last) {
			s.Last = a.Time
		}
	}
	s.OK = s.Tuned + s.OKN
	s.NRepeat, s.RepeatHz = len(repSD), rf.PooledSD(repSD, repN)
	// Tuning summary line.
	n := s.N
	rng := func(v []float64) string {
		if len(v) == 0 {
			return ""
		}
		st := rf.Describe(v)
		if st.Max-st.Min < 0.05 {
			return fmt.Sprintf(" (%.1f mm)", st.Min)
		}
		return fmt.Sprintf(" (%.1f–%.1f mm)", st.Min, st.Max)
	}
	if n == 0 {
		s.TuningLine = "No antenna captured yet."
	} else {
		var parts []string
		if s.Window.Mode == rf.TolAuto {
			parts = []string{fmt.Sprintf("%d of %d cover the band at SWR < %.1f (%d on the ± %.2f MHz tuning aim)", s.OK, n, limit, s.Tuned, s.AimHz/1e6)}
		} else {
			parts = []string{fmt.Sprintf("%d of %d tuned (± %.2f MHz)", s.Tuned, n, s.Window.Hz/1e6)}
		}
		if s.Shorten > 0 {
			parts = append(parts, fmt.Sprintf("%d to shorten%s", s.Shorten, rng(shortMm)))
		}
		if s.Lengthen > 0 {
			parts = append(parts, fmt.Sprintf("%d to lengthen%s", s.Lengthen, rng(longMm)))
		}
		if s.Bad > 0 {
			parts = append(parts, fmt.Sprintf("%d bad", s.Bad))
		}
		if s.None > 0 {
			parts = append(parts, fmt.Sprintf("%d without resonance", s.None))
		}
		s.TuningLine = strings.Join(parts, "; ")
	}
	// Calibration.
	switch {
	case n == 0:
		s.CalNote, s.CalOK = "–", true
	case calBad == 0:
		s.CalNote, s.CalOK = fmt.Sprintf("all %d captured calibrated for this range", n), true
	case calBad == n:
		s.CalNote = "none captured with a calibration for this range: figures include the cable and the VNA's own errors"
	default:
		s.CalNote = fmt.Sprintf("%d of %d captured without a calibration for this range: compare with care", calBad, n)
	}
	switch {
	case sl.MHzPerMm > 0:
		s.Slope = fmt.Sprintf("mm from %.2f MHz/mm learnt in the trim log %q", sl.MHzPerMm, sl.From)
	case b.LengthMm > 0:
		s.Slope = fmt.Sprintf("mm estimated from the %.1f mm element (L × f_res / f_target, cautious first cut as in TUNE)", b.LengthMm)
	default:
		s.Slope = "no element length given: trim advice in % of the length"
	}
	s.Conclusion = batchConclusion(b, &s, ms, sl, acc)
	return s
}

func verdictKey(v string) string {
	switch v {
	case "good", "ok", "bad":
		return v
	}
	return "n/a"
}

// Lot grades.
const (
	GradeFine  = "fine"  // every antenna covers the band with margin to spare
	GradeWorth = "worth" // worth correcting: the offset uses much of the margin
	GradeMust  = "must"  // must correct: antennas fail the SWR limit
)

// Thresholds of the lot grade: the mean offset may use up to half of the
// lot's window, and no antenna more than three quarters of its own margin,
// before a correction is worth it.
const (
	worthBiasUsed   = 50.0
	worthMarginUsed = 75.0
	firmN           = 5 // from 5 antennas on, the lot figures are no longer "indicative"
)

// batchConclusion says in plain words what the lot is like. The judgement
// is margin-based: does every antenna cover the band at the SWR limit, and
// with how much margin? The mean offset (systematic: the pattern length)
// is expressed as the share of the lot's window it uses, with its 95 %
// confidence interval; the unit-to-unit spread is compared with that
// window (never with TUNE's fine ± 0.15 % aim) and, when the antennas were
// measured ×N, with the measurement repeatability. Below 3 antennas there
// is no spread verdict, below 5 the figures are called indicative.
func batchConclusion(b *Batch, s *BatchSummary, ms []AntennaMetrics, sl trimSlope, acc rf.Acceptance) []string {
	// The offset and the spread of the lot are computed without the
	// statistical outliers of the resonance (a badly made antenna would
	// otherwise hide the pattern's offset and inflate the spread); they are
	// named separately below.
	var res, off []float64
	var skipped []string
	for i, m := range ms {
		if !m.Found {
			continue
		}
		if hasKey(m.Outliers, "res") {
			skipped = append(skipped, b.Antennas[i].Label)
			continue
		}
		res = append(res, m.ResFreq)
		off = append(off, m.Offset)
	}
	without := ""
	if len(skipped) > 0 {
		without = " without the outlier" + plural(len(skipped)) + " " + strings.Join(skipped, ", ")
	}
	switch {
	case s.N == 0:
		out := []string{"No antenna captured yet: connect the first one and capture it."}
		if b.Setup != nil {
			out = append(out, "Setup check "+b.Setup.Time.Format("2006-01-02 15:04")+": "+b.Setup.Line+".")
		}
		return out
	case len(res) == 0:
		s.Grade = GradeMust
		s.Headline = "No resonance near the target in any capture: check the profile and the antennas."
		return []string{s.Headline}
	}
	limit := acc.Limit()
	band := b.Band()
	bandTxt := fmt.Sprintf("%s–%s MHz", mhzF(band.Lo), mhzF(band.Hi))
	lim := fmt.Sprintf("SWR < %.1f", limit)
	rs, os := rf.Describe(res), rf.Describe(off)
	s.NRes = len(res)
	s.BiasHz, s.BiasPct, s.SigmaHz, s.SpreadHz = os.Mean, 100*os.Mean/b.Target, rs.SD, rs.Spread
	s.BiasCIHz = rf.MeanCI95(os.SD, len(off))
	switch {
	case s.BiasHz < 0 && s.OffMin < 0:
		s.BiasUsedPct = math.Min(999, 100*s.BiasHz/s.OffMin)
	case s.BiasHz > 0 && s.OffMax > 0:
		s.BiasUsedPct = math.Min(999, 100*s.BiasHz/s.OffMax)
	case s.BiasHz != 0:
		s.BiasUsedPct = 999
	}
	// Pattern correction for the mean offset, with the same slope as the
	// per-antenna advice (full estimate: it is for the next production run).
	switch {
	case sl.MHzPerMm > 0:
		s.BiasCutMm = -os.Mean / 1e6 / sl.MHzPerMm
	case b.LengthMm > 0:
		if p, err := rf.PredictTrim([]rf.TrimPoint{{LengthMm: b.LengthMm, FreqHz: b.Target + os.Mean}}, b.Target); err == nil {
			s.BiasCutMm = p.CutMm
		}
	}
	low := os.Mean < 0
	dir, shorter := "low", "shorter"
	if !low {
		dir, shorter = "high", "longer"
	}
	fix := fmt.Sprintf("~%.1f %%", math.Abs(s.BiasPct))
	if s.BiasCutMm != 0 {
		fix = fmt.Sprintf("~%.1f mm", math.Abs(s.BiasCutMm))
	}
	ci := ""
	if len(off) >= 2 {
		ci = fmt.Sprintf(", 95 %% CI ± %.2f MHz", s.BiasCIHz/1e6)
	}
	offTxt := fmt.Sprintf("the lot resonates %.1f MHz %s (%s %%%s%s)", math.Abs(os.Mean)/1e6, dir, signed2(s.BiasPct), ci, without)
	if len(res) == 1 {
		offTxt = fmt.Sprintf("it resonates %.1f MHz %s (%s %%)", math.Abs(os.Mean)/1e6, dir, signed2(s.BiasPct))
	}
	centred := math.Abs(os.Mean) <= s.AimHz
	all := fmt.Sprintf("all %d antennas cover", s.N)
	if s.N == 1 {
		all = "the antenna covers"
	}
	var failing, tight []string
	for i, m := range ms {
		if m.Verdict == "bad" || (!m.Found && m.Verdict != "good" && m.Verdict != "ok") {
			failing = append(failing, b.Antennas[i].Label)
		}
		if m.Found && m.Margin.OK && m.Margin.UsedPct > worthMarginUsed && m.Verdict != "bad" {
			tight = append(tight, b.Antennas[i].Label)
		}
	}
	fixedOut := s.Shorten + s.Lengthen
	out := []string{}
	switch {
	case len(failing) > 0:
		s.Grade = GradeMust
		h := fmt.Sprintf("Must correct: %d of %d antennas do not cover %s at %s (%s).", len(failing), s.N, bandTxt, lim, strings.Join(failing, ", "))
		if s.BiasUsedPct > worthBiasUsed && !centred && len(res) >= 2 {
			h += fmt.Sprintf(" %s, which uses %.0f %% of the margin: correct the pattern (%s %s), then check those antennas again.", capFirst(offTxt), s.BiasUsedPct, fix, shorter)
		} else if len(res) >= 2 {
			h += fmt.Sprintf(" The lot itself is close to the target (mean offset %s MHz): those antennas are individually off or poorly matched; trim or reject them.", signed2(os.Mean/1e6))
		}
		s.Headline = h
	case fixedOut > 0:
		s.Grade = GradeWorth
		s.Headline = fmt.Sprintf("Worth correcting: %s %s at %s (worst %s), but %d %s outside the ± %.2f MHz tolerance; %s.",
			all, bandTxt, lim, swrF(s.WorstSWR), fixedOut, map[bool]string{true: "is", false: "are"}[fixedOut == 1], s.Window.Hz/1e6, offTxt)
	case s.BiasUsedPct > worthBiasUsed || len(tight) > 0:
		s.Grade = GradeWorth
		var why []string
		if s.BiasUsedPct > worthBiasUsed {
			why = append(why, fmt.Sprintf("%s, which uses %.0f %% of the margin", offTxt, s.BiasUsedPct))
		}
		if len(tight) > 0 {
			why = append(why, fmt.Sprintf("%s use%s more than %.0f %% of %s margin", strings.Join(tight, ", "), map[bool]string{true: "s", false: ""}[len(tight) == 1], worthMarginUsed, map[bool]string{true: "its", false: "their"}[len(tight) == 1]))
		}
		h := fmt.Sprintf("Worth correcting: %s %s at %s (worst %s), but %s.", all, bandTxt, lim, swrF(s.WorstSWR), strings.Join(why, ", and "))
		if !centred {
			h += fmt.Sprintf(" A %s %s pattern would centre the lot and restore the margin.", fix, shorter)
		}
		s.Headline = h
	default:
		s.Grade = GradeFine
		h := fmt.Sprintf("Fine: %s %s at %s (worst %s).", all, bandTxt, lim, swrF(s.WorstSWR))
		switch {
		case centred:
			h += fmt.Sprintf(" Centred on the target (mean offset %s MHz).", signed2(os.Mean/1e6))
		default:
			h += fmt.Sprintf(" %s, which uses %.0f %% of the margin: no correction needed (a %s %s pattern would centre it).", capFirst(offTxt), s.BiasUsedPct, fix, shorter)
		}
		s.Headline = h
	}
	out = append(out, s.Headline)
	// Sample size.
	switch {
	case len(res) == 1:
		out = append(out, "One antenna so far: capture more to see the lot's offset and spread.")
	case len(res) < firmN:
		lo, hi := (os.Mean-s.BiasCIHz)/1e6, (os.Mean+s.BiasCIHz)/1e6
		out = append(out, fmt.Sprintf("Indicative (n = %d): mean offset %s MHz, 95 %% confidence interval %s to %s MHz. Capture at least %d antennas for firm lot figures.",
			len(res), signed2(os.Mean/1e6), signed2(lo), signed2(hi), firmN))
	}
	// Spread: compared with the lot's window and the repeatability.
	if len(res) >= 3 {
		w := s.Window.Hz
		if w <= 0 {
			w = s.AimHz
		}
		used := 100 * 2 * rs.SD / w
		pfx := ""
		if len(res) < firmN {
			pfx = "Indicative: "
		}
		sd := rs.SD / 1e6
		switch {
		case used <= 25:
			out = append(out, fmt.Sprintf("%sconsistent lot: unit-to-unit spread σ %.2f MHz (range %.2f MHz%s); ± 2σ uses %.0f %% of the ± %.2f MHz window.", pfx, sd, rs.Spread/1e6, without, used, w/1e6))
		case used <= 50:
			out = append(out, fmt.Sprintf("%smoderate spread: σ %.2f MHz (range %.2f MHz%s); ± 2σ uses %.0f %% of the ± %.2f MHz window.", pfx, sd, rs.Spread/1e6, without, used, w/1e6))
		default:
			out = append(out, fmt.Sprintf("%swide spread: σ %.2f MHz (range %.2f MHz%s); ± 2σ uses %.0f %% of the ± %.2f MHz window, so some antennas lose margin even when the mean is right.", pfx, sd, rs.Spread/1e6, without, used, w/1e6))
		}
		if pfx == "" {
			out[len(out)-1] = capFirst(out[len(out)-1])
		}
		if rep := s.RepeatHz; rep > 0 {
			r := rep / 1e6
			switch {
			case rs.SD >= 3*rep:
				out = append(out, fmt.Sprintf("Spread between antennas σ %.2f MHz vs repeatability σ %.2f MHz (Measure ×N): the spread is real.", sd, r))
			case rs.SD <= 1.5*rep:
				out = append(out, fmt.Sprintf("Spread between antennas σ %.2f MHz vs repeatability σ %.2f MHz (Measure ×N): the spread is about the measurement repeatability, so the antennas are more alike than these numbers suggest.", sd, r))
			default:
				out = append(out, fmt.Sprintf("Spread between antennas σ %.2f MHz vs repeatability σ %.2f MHz (Measure ×N): part of the spread is measurement repeatability.", sd, r))
			}
		}
	}
	var outl []string
	for i, m := range ms {
		if len(m.Outliers) > 0 {
			outl = append(outl, b.Antennas[i].Label)
		}
	}
	if len(outl) > 0 {
		out = append(out, fmt.Sprintf("Statistical outlier%s (unlike the rest of the lot, whether in tolerance or not): %s.", plural(len(outl)), strings.Join(outl, ", ")))
	}
	if !s.CalOK {
		out = append(out, "Calibration: "+s.CalNote+".")
	}
	if b.Setup != nil {
		out = append(out, "Setup check "+b.Setup.Time.Format("2006-01-02 15:04")+": "+b.Setup.Line+".")
	}
	return out
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func firstFound(ms []AntennaMetrics) int {
	for i, m := range ms {
		if m.Found {
			return i
		}
	}
	return 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---------- capture ----------

// batchCheck says why res cannot be captured into b (nil: it can).
func batchCheck(b *Batch, res *Result) error {
	if res == nil {
		return errors.New("no sweep yet: tap " + b.Profile + " first")
	}
	if res.Tune != nil {
		return errors.New("TUNE is open: close TUNE to capture (it sweeps a narrow window)")
	}
	if res.Source != nil {
		return errors.New("a measurement opened from a file is not captured: sweep the antenna")
	}
	c := res.Config
	if c.Profile != b.Profile || c.Start != b.Start || c.Stop != b.Stop || len(res.Freq) != b.Points {
		return fmt.Errorf("this batch uses the %s (%s, %d points) and the current sweep is %s (%s, %d points): tap %s first",
			b.Label, mhzRange(b.Start, b.Stop), b.Points, c.Profile, mhzRange(c.Start, c.Stop), len(res.Freq), b.Profile)
	}
	if len(b.Freq) > 0 && (len(b.Freq) != len(res.Freq) || b.Freq[0] != res.Freq[0] || b.Freq[len(b.Freq)-1] != res.Freq[len(res.Freq)-1]) {
		return errors.New("the frequency points differ from the batch's earlier captures")
	}
	for _, w := range res.Summary.Warnings {
		switch w.Code {
		case "open":
			return errors.New("no antenna on the cable (|Γ| ≈ 1): connect the next antenna")
		case "calcheck":
			return errors.New("reflection above 1: the calibration does not match, recalibrate this profile")
		}
	}
	if len(b.Antennas) >= maxBatchAntennas {
		return fmt.Errorf("a batch holds at most %d antennas: start a new batch", maxBatchAntennas)
	}
	return nil
}

func isAntennaSweep(res *Result) bool {
	for _, w := range res.Summary.Warnings {
		if w.Code == "open" || w.Code == "calcheck" {
			return false
		}
	}
	return true
}

// round6 keeps Γ to 1e-6 (far below the noise) to keep stored batches small.
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func s11Of(g []complex128) [][2]float64 {
	out := make([][2]float64, len(g))
	for i, c := range g {
		out[i] = [2]float64{round6(real(c)), round6(imag(c))}
	}
	return out
}

// captureLocked stores res (or the averaged reading g, for auto-capture) as
// a new antenna of b, or as a new measurement of antenna replace. s.mu
// must be held. b is modified and saved.
func (s *Server) captureLocked(b *Batch, res *Result, g []complex128, replace int, label string, auto bool) (*BatchAntenna, error) {
	if err := batchCheck(b, res); err != nil && !(replace > 0 && strings.HasPrefix(err.Error(), "a batch holds")) {
		return nil, err
	}
	if g == nil {
		g = res.complexS11()
	}
	prev := cloneBatch(b)
	cal, calOK := s.calLabelLocked(res)
	a := BatchAntenna{Time: res.Time, Cal: cal, CalOK: calOK, Auto: auto, AvgN: res.AvgN, S11: s11Of(g)}
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	for _, w := range res.Summary.Warnings {
		a.Warnings = append(a.Warnings, w.Code)
	}
	if len(b.Freq) == 0 {
		b.Freq = append([]float64(nil), res.Freq...)
	}
	var out *BatchAntenna
	if replace > 0 {
		i := b.index(replace)
		if i < 0 {
			return nil, fmt.Errorf("no antenna %d in this batch", replace)
		}
		old := b.Antennas[i]
		a.ID, a.Label, a.Redone = old.ID, old.Label, old.Redone+1
		b.Antennas[i] = a
		out = &b.Antennas[i]
	} else {
		if b.NextID < 1 {
			b.NextID = 1
		}
		a.ID = b.NextID
		b.NextID++
		a.Label = fmt.Sprintf("#%d", a.ID)
		if l := strings.TrimSpace(label); l != "" {
			a.Label = truncate(l, maxBatchLabel)
		}
		b.Antennas = append(b.Antennas, a)
		out = &b.Antennas[len(b.Antennas)-1]
	}
	b.Updated = time.Now()
	if err := s.store.SaveBatch(b); err != nil {
		return nil, err
	}
	s.batch.undo[b.ID] = prev
	s.batch.version++
	return out, nil
}

func (b *Batch) index(id int) int {
	for i, a := range b.Antennas {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func cloneBatch(b *Batch) *Batch {
	c := *b
	c.Antennas = append([]BatchAntenna(nil), b.Antennas...)
	c.Freq = append([]float64(nil), b.Freq...)
	c.Bands = append([]rf.Band(nil), b.Bands...)
	return &c
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	for len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}

// ---------- server state ----------

// batchState is the server side of BATCH: the active batch (captures and
// auto-capture go there) and the auto-capture detector.
type batchState struct {
	cur     *Batch // active batch (cached; nil = none)
	auto    bool
	arr     *rf.Arrival
	version int               // bumps on every change of the active batch
	undo    map[string]*Batch // per batch: the batch before its last change
	trash   *Batch            // the last deleted batch (undo)
	publish bool              // state must be published after this sweep
}

// BatchState is the BATCH part of State.
type BatchState struct {
	Active  string `json:"active,omitempty"`
	Name    string `json:"name,omitempty"`
	Profile string `json:"profile,omitempty"`
	N       int    `json:"n"`
	Auto    bool   `json:"auto"`
	Version int    `json:"version"`
	HasUndo bool   `json:"hasUndo"`
	Trash   string `json:"trash,omitempty"` // id of the batch that can be restored
}

// BatchLive is attached to every sweep while a batch is active.
type BatchLive struct {
	ID       string `json:"id"`
	Auto     bool   `json:"auto"`
	Match    bool   `json:"match"`             // the sweep fits the batch
	Problem  string `json:"problem,omitempty"` // why it cannot be captured
	Status   string `json:"status,omitempty"`  // auto-capture: rf.Arr*
	Run      int    `json:"run,omitempty"`
	Need     int    `json:"need,omitempty"`
	Captured int    `json:"captured,omitempty"` // antenna ID auto-captured with this sweep
	N        int    `json:"n"`
	Version  int    `json:"version"`
}

func (s *Server) batchStateLocked() BatchState {
	bs := s.batch
	v := BatchState{Auto: bs.auto, Version: bs.version}
	if bs.cur != nil {
		v.Active, v.Name, v.Profile, v.N, v.HasUndo = bs.cur.ID, bs.cur.Name, bs.cur.Profile, len(bs.cur.Antennas), bs.undo[bs.cur.ID] != nil
	}
	if bs.trash != nil {
		v.Trash = bs.trash.ID
	}
	return v
}

// lastGamma is the reading of the last captured antenna of b (nil if none).
func lastGamma(b *Batch) []complex128 {
	if b == nil || len(b.Antennas) == 0 {
		return nil
	}
	last := b.Antennas[0]
	for _, a := range b.Antennas {
		if a.Time.After(last.Time) {
			last = a
		}
	}
	return toComplex(last.S11)
}

// setActiveLocked makes b the active batch (nil: none). It returns the
// settings to save (after unlocking) when the active batch changed.
func (s *Server) setActiveLocked(b *Batch) *Settings {
	bs := s.batch
	if b == nil || bs.cur == nil || bs.cur.ID != b.ID {
		bs.auto = false
		bs.arr = nil
	}
	bs.cur = b
	bs.version++
	id := ""
	if b != nil {
		id = b.ID
	}
	if s.settings.BatchActive == id {
		return nil
	}
	s.settings.BatchActive = id
	set := s.settings
	return &set
}

// feedBatchLocked runs auto-capture on a sweep. s.mu must be held.
func (s *Server) feedBatchLocked(res *Result) {
	bs := s.batch
	b := bs.cur
	if b == nil || res.Tune != nil || res.Source != nil {
		return
	}
	live := &BatchLive{ID: b.ID, Auto: bs.auto, N: len(b.Antennas), Version: bs.version}
	res.Batch = live
	if err := batchCheck(b, res); err != nil {
		live.Problem = err.Error()
		live.Match = !strings.Contains(err.Error(), "current sweep is") && !strings.Contains(err.Error(), "TUNE")
		if !live.Match || !bs.auto || bs.arr == nil {
			return
		}
	} else {
		live.Match = true
	}
	if !bs.auto || bs.arr == nil || s.measuringLocked() {
		return
	}
	hs := s.hold.cfg
	bs.arr.Cfg.Threshold, bs.arr.Cfg.N = hs.Threshold, hs.N
	u := bs.arr.Add(res.Freq, res.complexS11(), isAntennaSweep(res))
	live.Status, live.Run, live.Need = u.Status, u.Run, u.Need
	if u.Status != rf.ArrReady {
		return
	}
	a, err := s.captureLocked(b, res, u.Gamma, 0, "", true)
	if err != nil {
		live.Problem = err.Error()
		return
	}
	bs.arr.Captured(u.Gamma)
	live.Captured, live.N, live.Version = a.ID, len(b.Antennas), bs.version
	bs.publish = true
}

// batchSlope finds a learnt MHz/mm for b: from the trim log the batch
// names, when the trim calculator has learnt it (nil otherwise).
func (s *Server) batchSlope(b *Batch) *trimSlope {
	name := b.Trim
	if name == "" {
		return nil
	}
	t, err := s.store.LoadTrim(name)
	if err != nil {
		return nil
	}
	t.predict()
	if p := t.Prediction; p != nil && p.Method == "fit" && p.MHzPerMm > 0 {
		return &trimSlope{MHzPerMm: p.MHzPerMm, From: t.Name}
	}
	return nil
}

// loadActiveBatch restores the active batch at start-up.
func (s *Server) loadActiveBatch() {
	if id := s.settings.BatchActive; id != "" {
		if b, err := s.store.LoadBatch(id); err == nil {
			s.batch.cur = b
		} else if errors.Is(err, fs.ErrNotExist) {
			s.settings.BatchActive = ""
		}
	}
}
