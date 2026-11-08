package server

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- adjusted versions: one physical antenna, several captures ----------
//
// A capture can be an adjusted version of an earlier capture of the same
// antenna (AdjustedFrom): trimmed, bent, re-soldered… with what was changed
// (Change) and how much was cut (TrimMm, > 0 shortened). The versions of
// one antenna form a chain: the first capture is the antenna as received,
// the last one is its current version. The lot (n, statistics, outliers,
// grade, yield, strip, lot average) counts every physical antenna once, by
// its current version; the as-received lot (the first versions) is
// compared with it, and every trim with its resonance change is a MHz/mm
// sample for the trim advice of the whole batch.
//
// Chains are linear (one later version per capture): a new version always
// follows the antenna's current one. Deleting a capture splices its chain
// (the next version follows the one before; its change and trim are merged
// into the next one's, so the trims still add up); unlinking takes one
// capture out of its chain the same way, without merging.

const maxChange = 80 // characters of a change text

// lotGroup is one physical antenna: its captures (indexes into b.Antennas)
// from the version as received to the current one.
type lotGroup []int

// lotGroups splits the captures into physical antennas, in the order of
// their first capture. A capture whose parent is missing, a second later
// version of the same capture, or a capture in a loop counts as an antenna
// of its own: stored batches are checked (checkChains), so this only
// guards against hand-edited files.
func (b *Batch) lotGroups() []lotGroup {
	idx := make(map[int]int, len(b.Antennas))
	for i, a := range b.Antennas {
		idx[a.ID] = i
	}
	child := map[int]int{} // index → index of its later version
	hasParent := make([]bool, len(b.Antennas))
	for i, a := range b.Antennas {
		p, ok := idx[a.AdjustedFrom]
		if a.AdjustedFrom == 0 || !ok || p == i {
			continue
		}
		if _, dup := child[p]; dup {
			continue
		}
		child[p], hasParent[i] = i, true
	}
	seen := make([]bool, len(b.Antennas))
	var out []lotGroup
	for i := range b.Antennas {
		if hasParent[i] {
			continue
		}
		g := lotGroup{}
		for k := i; ; {
			seen[k] = true
			g = append(g, k)
			c, ok := child[k]
			if !ok || seen[c] {
				break
			}
			k = c
		}
		out = append(out, g)
	}
	for i := range b.Antennas {
		if !seen[i] {
			out = append(out, lotGroup{i})
		}
	}
	return out
}

// physical is the number of physical antennas (captures minus the
// superseded versions).
func (b *Batch) physical() int { return len(b.lotGroups()) }

// groupOf returns the physical antenna capture id belongs to.
func (b *Batch) groupOf(id int) lotGroup {
	for _, g := range b.lotGroups() {
		for _, k := range g {
			if b.Antennas[k].ID == id {
				return g
			}
		}
	}
	return nil
}

// nextOf is the index of the later version of capture id (−1: none).
func (b *Batch) nextOf(id int) int {
	for i, a := range b.Antennas {
		if a.AdjustedFrom == id && a.ID != id {
			return i
		}
	}
	return -1
}

// nextLabel is the default label of a new antenna: its position in the
// lot (#3), skipping labels already used.
func (b *Batch) nextLabel() string {
	used := map[string]bool{}
	for _, a := range b.Antennas {
		used[a.Label] = true
	}
	k := b.physical() + 1
	for used[fmt.Sprintf("#%d", k)] {
		k++
	}
	return fmt.Sprintf("#%d", k)
}

// defaultLabelRe matches the labels given by default (#5, #2 adj 1): those
// follow the antenna a capture is linked to.
var defaultLabelRe = regexp.MustCompile(`^(#\d+|.+ adj \d+)$`)

// versionLabel is the default label of version ver of an antenna whose
// first capture is labelled root: "#2 adj 1".
func versionLabel(root string, ver int) string {
	suffix := fmt.Sprintf(" adj %d", ver)
	return truncate(root, maxBatchLabel-len(suffix)) + suffix
}

// versionNo is what the "#" column shows: the antenna's position in the
// lot, and a letter per version when it has several (2a, 2b…).
func versionNo(pos, ver, versions int) string {
	if versions < 2 {
		return fmt.Sprint(pos)
	}
	if ver < 26 {
		return fmt.Sprintf("%d%c", pos, 'a'+ver)
	}
	return fmt.Sprintf("%d.%d", pos, ver+1)
}

// cleanAdjust trims and checks a change text and a trim.
func cleanAdjust(change string, trimMm float64) (string, float64, error) {
	change = strings.TrimSpace(strings.Join(strings.Fields(change), " "))
	if len([]rune(change)) > maxChange {
		change = string([]rune(change)[:maxChange])
	}
	if math.IsNaN(trimMm) || math.IsInf(trimMm, 0) || math.Abs(trimMm) > 1000 {
		return "", 0, errors.New("the trim must be a length in mm (+ shortened, − lengthened)")
	}
	return change, math.Round(trimMm*1000) / 1000, nil
}

// checkChains checks the versions: every AdjustedFrom names another
// capture of this batch, no capture has two later versions, and no chain
// loops.
func (b *Batch) checkChains() error {
	idx := map[int]int{}
	for i, a := range b.Antennas {
		idx[a.ID] = i
	}
	next := map[int]string{}
	for _, a := range b.Antennas {
		if a.AdjustedFrom == 0 {
			continue
		}
		if a.AdjustedFrom == a.ID {
			return fmt.Errorf("%s cannot be an adjusted version of itself", a.Label)
		}
		if _, ok := idx[a.AdjustedFrom]; !ok {
			return fmt.Errorf("%s is an adjusted version of capture %d, which is not in this batch", a.Label, a.AdjustedFrom)
		}
		if o, dup := next[a.AdjustedFrom]; dup {
			return fmt.Errorf("%s and %s are both adjusted versions of %s: an antenna has one version after another", o, a.Label, b.Antennas[idx[a.AdjustedFrom]].Label)
		}
		next[a.AdjustedFrom] = a.Label
	}
	for _, a := range b.Antennas {
		seen := map[int]bool{a.ID: true}
		for p := a.AdjustedFrom; p != 0; p = b.Antennas[idx[p]].AdjustedFrom {
			if seen[p] {
				return fmt.Errorf("the versions of %s loop back on themselves", a.Label)
			}
			seen[p] = true
		}
	}
	return nil
}

// linkVersion makes capture id a later version of the antenna capture to
// belongs to: it follows that antenna's current version (its own later
// versions come with it; its earlier ones stay an antenna of their own).
// A default label (#5) becomes the version's label (#2 adj 1).
func (b *Batch) linkVersion(id, to int, change string, trimMm float64) (string, error) {
	i, j := b.index(id), b.index(to)
	if i < 0 {
		return "", fmt.Errorf("no antenna %d in this batch", id)
	}
	if j < 0 {
		return "", fmt.Errorf("no antenna %d in this batch", to)
	}
	g := b.groupOf(to)
	for _, k := range g {
		if b.Antennas[k].ID == id {
			return "", fmt.Errorf("%s is already a version of that antenna", b.Antennas[i].Label)
		}
	}
	change, trimMm, err := cleanAdjust(change, trimMm)
	if err != nil {
		return "", err
	}
	tail, root := b.Antennas[g[len(g)-1]], b.Antennas[g[0]]
	a := &b.Antennas[i]
	old := a.Label
	a.AdjustedFrom, a.Change, a.TrimMm = tail.ID, change, trimMm
	if defaultLabelRe.MatchString(a.Label) {
		a.Label = versionLabel(root.Label, len(g))
	}
	if err := b.checkChains(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is now an adjusted version of %s", old, root.Label), nil
}

// unlinkVersion takes capture id out of its antenna's versions: it becomes
// an antenna of its own and the versions around it are joined.
func (b *Batch) unlinkVersion(id int) (string, error) {
	i := b.index(id)
	if i < 0 {
		return "", fmt.Errorf("no antenna %d in this batch", id)
	}
	a := &b.Antennas[i]
	n := b.nextOf(id)
	if a.AdjustedFrom == 0 && n < 0 {
		return "", fmt.Errorf("%s is not linked to another capture", a.Label)
	}
	if n >= 0 {
		nx := &b.Antennas[n]
		nx.AdjustedFrom = a.AdjustedFrom
		if nx.AdjustedFrom == 0 {
			nx.Change, nx.TrimMm = "", 0
		}
	}
	a.AdjustedFrom, a.Change, a.TrimMm = 0, "", 0
	return a.Label + " is now an antenna of its own", nil
}

// removeCapture deletes capture id; its later version follows the one
// before it, with the change and the trim of both steps.
func (b *Batch) removeCapture(id int) (string, error) {
	i := b.index(id)
	if i < 0 {
		return "", fmt.Errorf("no antenna %d in this batch", id)
	}
	a := b.Antennas[i]
	if n := b.nextOf(id); n >= 0 {
		nx := &b.Antennas[n]
		nx.AdjustedFrom = a.AdjustedFrom
		if a.AdjustedFrom == 0 {
			nx.Change, nx.TrimMm = "", 0 // the first version now
		} else {
			if a.Change != "" {
				nx.Change, _, _ = cleanAdjust(strings.Trim(a.Change+"; "+nx.Change, "; "), 0)
			}
			nx.TrimMm = math.Round((a.TrimMm+nx.TrimMm)*1000) / 1000
		}
	}
	b.Antennas = append(b.Antennas[:i:i], b.Antennas[i+1:]...)
	return a.Label + " deleted", nil
}

// ---------- learning MHz/mm from the adjustments ----------

// adjustLogs turns every physical antenna's trimmed versions into trim
// logs (lengths relative to its first version, resonances): a run of
// versions joined by trims is one log; a step without a trim (bent…) or
// without a resonance starts a new one. It returns the logs and the
// number of trims in them.
func adjustLogs(b *Batch, groups []lotGroup, ms []AntennaMetrics) ([][]rf.TrimPoint, int) {
	const base = 1000.0 // mm: only the length changes matter
	var logs [][]rf.TrimPoint
	steps := 0
	for _, g := range groups {
		var cur []rf.TrimPoint
		l := base
		flush := func() {
			if len(cur) >= 2 {
				logs = append(logs, cur)
				steps += len(cur) - 1
			}
			cur = nil
		}
		for v, k := range g {
			a, m := b.Antennas[k], ms[k]
			if v > 0 {
				l -= a.TrimMm
				if a.TrimMm == 0 {
					flush()
				}
			}
			if !m.Found || l <= 0 {
				flush()
				continue
			}
			cur = append(cur, rf.TrimPoint{LengthMm: l, FreqHz: m.ResFreq})
		}
		flush()
	}
	return logs, steps
}

// LearntSlope is the MHz/mm learnt from this batch's adjustments (with the
// batch's trim log, when it names one).
type LearntSlope struct {
	MHzPerMm    float64 `json:"mhzPerMm"` // 0: nothing usable
	Adjustments int     `json:"adjustments"`
	Log         string  `json:"log,omitempty"`
	Confidence  float64 `json:"confidence"`
	Text        string  `json:"text"`
}

// learnSlope pools the batch's trims with the trim log of base (the same
// Bayesian fit as the trim calculator, rf.LearnSlope; the textbook prior
// f/L from the log's last point, else the element length, else none). With
// no trim in the batch it returns base unchanged.
func learnSlope(b *Batch, groups []lotGroup, ms []AntennaMetrics, base trimSlope) (trimSlope, *LearntSlope) {
	logs, steps := adjustLogs(b, groups, ms)
	if steps == 0 {
		return base, nil
	}
	prior := 0.0
	switch {
	case len(base.Log) > 0 && base.Log[len(base.Log)-1].LengthMm > 0:
		last := base.Log[len(base.Log)-1]
		prior = last.FreqHz / last.LengthMm
	case b.LengthMm > 0:
		prior = b.Target / b.LengthMm
	}
	if len(base.Log) > 0 {
		logs = append([][]rf.TrimPoint{base.Log}, logs...)
	}
	fit := rf.LearnSlope(logs, prior)
	ls := &LearntSlope{MHzPerMm: fit.MHzPerMm, Adjustments: steps, Log: base.From, Confidence: fit.Confidence}
	from := fmt.Sprintf("%d adjustment%s", steps, plural(steps))
	if base.From != "" && len(base.Log) > 0 {
		from += fmt.Sprintf(" and the trim log %q", base.From)
	}
	if fit.MHzPerMm <= 0 {
		ls.Text = "no MHz/mm learnt from " + from + ": the resonance did not rise when the antennas were shortened (check the trims)"
		return base, ls
	}
	ls.Text = fmt.Sprintf("learnt %.2f MHz/mm from %s", fit.MHzPerMm, from)
	out := base
	out.MHzPerMm, out.Adj = fit.MHzPerMm, steps
	return out, ls
}

// ---------- the effect of each adjustment, and the lot before / after ----------

// AdjustStep is what one adjustment did: a version compared with the one
// before it.
type AdjustStep struct {
	ID         int     `json:"id"`   // the adjusted version
	From       int     `json:"from"` // the version before
	No         string  `json:"no"`   // "2b"
	FromNo     string  `json:"fromNo"`
	Antenna    string  `json:"antenna"` // label of the first version
	Label      string  `json:"label"`
	Change     string  `json:"change"` // what was changed, as entered (or from the trim)
	TrimMm     float64 `json:"trimMm"`
	Found      bool    `json:"found"` // a resonance in both
	ResFrom    float64 `json:"resFrom"`
	ResTo      float64 `json:"resTo"`
	DRes       float64 `json:"dRes"` // Hz
	SWRFrom    float64 `json:"swrFrom"`
	SWRTo      float64 `json:"swrTo"` // at the target
	MarginFrom float64 `json:"marginFrom"`
	MarginTo   float64 `json:"marginTo"` // % used, −1 unknown
	StatusFrom string  `json:"statusFrom"`
	StatusTo   string  `json:"statusTo"`
	MHzPerMm   float64 `json:"mhzPerMm,omitempty"` // this step alone (a trim and both resonances)
	Text       string  `json:"text"`               // "#2: cut 1 mm → resonance +5.8 MHz (862.67 → 868.43), SWR@target 1.22 → 1.10, margin 21 % → 1 %"
	Short      string  `json:"short"`              // "cut 1 mm → +5.80 MHz, SWR 1.22 → 1.10"
}

// LotBrief is a lot in a few figures (the lot as received, or as it is).
type LotBrief struct {
	N         int     `json:"n"`
	Covered   int     `json:"covered"` // cover the band at the SWR limit
	Tuned     int     `json:"tuned"`   // on the TUNE aim (or within the fixed tolerance)
	WorstSWR  float64 `json:"worstSWR"`
	BiasHz    float64 `json:"biasHz"`
	NRes      int     `json:"nRes"`
	MarginMax float64 `json:"marginMax"`
	Grade     string  `json:"grade"`
}

func lotBriefOf(s BatchSummary) LotBrief {
	return LotBrief{N: s.N, Covered: s.Yield["good"] + s.Yield["ok"], Tuned: s.Tuned, WorstSWR: s.WorstSWR, BiasHz: s.BiasHz,
		NRes: s.NRes, MarginMax: s.MarginMax, Grade: s.Grade}
}

// trimText says a trim in words: "1.0 mm shorter".
func trimText(mm float64) string {
	if mm < 0 {
		return fmt.Sprintf("%.1f mm longer", -mm)
	}
	return fmt.Sprintf("%.1f mm shorter", mm)
}

// changeText is what was done: the change as entered, with the trim when
// the text does not give it.
func changeText(change string, trimMm float64) string {
	switch {
	case change == "" && trimMm == 0:
		return "adjusted"
	case change == "":
		return trimText(trimMm)
	case trimMm != 0 && !strings.Contains(strings.ToLower(change), "mm"):
		return change + " (" + trimText(trimMm) + ")"
	}
	return change
}

func marginOf(m AntennaMetrics) float64 {
	if m.Found && m.Margin.OK {
		return m.Margin.UsedPct
	}
	return -1
}

func pctText(p float64) string {
	if p < 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f %%", math.Min(p, 999))
}

// adjustStep compares version k with version j (the one before it).
func adjustStep(b *Batch, ms []AntennaMetrics, root, j, k int, no, fromNo string) AdjustStep {
	a, p := b.Antennas[k], b.Antennas[j]
	m, pm := ms[k], ms[j]
	st := AdjustStep{ID: a.ID, From: p.ID, No: no, FromNo: fromNo, Antenna: b.Antennas[root].Label, Label: a.Label, Change: changeText(a.Change, a.TrimMm),
		TrimMm: a.TrimMm, Found: m.Found && pm.Found, SWRFrom: pm.SWRTarget, SWRTo: m.SWRTarget, MarginFrom: marginOf(pm), MarginTo: marginOf(m),
		StatusFrom: pm.Tuning.Status, StatusTo: m.Tuning.Status}
	res := "resonance not found"
	short := st.Change + " → no resonance"
	if st.Found {
		st.ResFrom, st.ResTo, st.DRes = pm.ResFreq, m.ResFreq, m.ResFreq-pm.ResFreq
		if a.TrimMm != 0 {
			st.MHzPerMm = st.DRes / 1e6 / a.TrimMm
		}
		res = fmt.Sprintf("resonance %s MHz (%s → %s)", signed2(st.DRes/1e6), mhzF(pm.ResFreq), mhzF(m.ResFreq))
		short = fmt.Sprintf("%s → %s MHz", st.Change, signed2(st.DRes/1e6))
	}
	st.Text = fmt.Sprintf("%s: %s → %s, SWR@target %s → %s, margin %s → %s", st.Antenna, st.Change, res, swrF(st.SWRFrom), swrF(st.SWRTo),
		pctText(st.MarginFrom), pctText(st.MarginTo))
	st.Short = fmt.Sprintf("%s, SWR %s → %s", short, swrF(st.SWRFrom), swrF(st.SWRTo))
	return st
}

// compareLine is the before / after sentence of the conclusion.
func compareLine(before, after LotBrief, adjusted []string, acc rf.Acceptance, auto bool) string {
	aim := "on the TUNE aim"
	if !auto {
		aim = "within the tolerance"
	}
	part := func(l LotBrief, first bool) string {
		s := fmt.Sprintf("%d of %d cover the band", l.Covered, l.N)
		if first {
			s += fmt.Sprintf(" at SWR < %.1f", acc.Limit())
		}
		s += fmt.Sprintf(", %d %s", l.Tuned, aim)
		if l.WorstSWR > 0 {
			s += ", worst SWR " + swrF(l.WorstSWR)
		}
		if l.NRes > 0 {
			s += fmt.Sprintf(", mean offset %s MHz", signed2(l.BiasHz/1e6))
		}
		return s
	}
	line := fmt.Sprintf("Adjusted: %s (%d of %d antenna%s). As received: %s; after adjustment: %s.", strings.Join(adjusted, ", "), len(adjusted), after.N,
		plural(after.N), part(before, true), part(after, false))
	if before.Grade != after.Grade && before.Grade != "" && after.Grade != "" {
		line += fmt.Sprintf(" Lot grade %s → %s.", strings.ToLower(gradeLabel[before.Grade][0]), strings.ToLower(gradeLabel[after.Grade][0]))
	}
	return line
}
