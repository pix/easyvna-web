package server

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- antenna history ----------
//
// Every antenna record (antennas/<name>.json, the one the antenna sheet
// fills in) keeps a short summary of each measurement made of it: when a
// sheet or a report is made, or with "Log this measurement". Comparing the
// latest summary with the earlier ones of the same profile and band shows
// whether the antenna drifted: resonance moved, SWR got worse, or the dip
// got broader and its resistance rose (signs of loss, e.g. water in the
// coax).

// HistoryEntry is one measurement summary.
type HistoryEntry struct {
	Time        time.Time `json:"time"`     // when it was logged
	Measured    time.Time `json:"measured"` // when the sweep was taken (or the file opened)
	Kind        string    `json:"kind"`     // sheet, report, manual
	Profile     string    `json:"profile"`  // profile id (a file's matching profile, else "file")
	Band        string    `json:"band,omitempty"`
	Range       string    `json:"range"`
	Points      int       `json:"points"`
	Source      string    `json:"source"` // live or file
	File        string    `json:"file,omitempty"`
	Target      float64   `json:"target,omitempty"`      // Hz: band reference (or nearest band for identify sweeps)
	ResFreq     float64   `json:"resFreq,omitempty"`     // Hz, resonance dip near the target
	ResSWR      float64   `json:"resSWR,omitempty"`      // SWR at that dip
	ResR        float64   `json:"resR,omitempty"`        // Ω, R at the dip
	SWRAtTarget float64   `json:"swrAtTarget,omitempty"` // SWR at the target
	WorstSWR    float64   `json:"worstSWR,omitempty"`    // worst SWR in the band
	RL          float64   `json:"rl,omitempty"`          // dB, worst return loss in the band (best match without a band)
	R           float64   `json:"r"`                     // Ω at the target
	X           float64   `json:"x"`                     // Ω at the target
	Q           float64   `json:"q,omitempty"`
	BW2         float64   `json:"bw2,omitempty"` // Hz, SWR < 2 bandwidth of the dip (0 = open or none)
	Cal         string    `json:"cal"`           // calibrated, not calibrated, calibration does not fit, from file
	Key         string    `json:"key"`           // de-duplication: profile + measurement time
}

// group is the key entries are compared within.
func (e HistoryEntry) group() string { return e.Profile + "/" + e.Band }

// maxHistory bounds the history of one antenna.
const maxHistory = 500

// historyEntryLocked summarises r. s.mu must be held.
func (s *Server) historyEntryLocked(r *Result, kind string) HistoryEntry {
	e := HistoryEntry{Time: time.Now(), Measured: r.Time, Kind: kind, Profile: r.Config.Profile,
		Range: mhzRange(r.Config.Start, r.Config.Stop), Points: len(r.Freq), Source: "live"}
	if r.Source != nil {
		e.Source, e.File = "file", r.Source.File
		if r.Source.Profile != "" {
			e.Profile = r.Source.Profile
		}
	}
	e.Key = e.Profile + "@" + r.Time.UTC().Format(time.RFC3339Nano)
	e.Cal, _ = s.calLabelLocked(r)
	sum := r.Summary
	var bs *rf.BandSummary
	var dip *rf.Dip
	if !r.Config.Identify && len(sum.Bands) > 0 {
		bs = &sum.Bands[0]
		dip = rf.NearestDip(sum.Dips, bs.Band)
	} else if len(sum.Dips) > 0 {
		// Identify sweep: the best dip, against its nearest LoRa band.
		best := 0
		for i, d := range sum.Dips {
			if d.SWR < sum.Dips[best].SWR {
				best = i
			}
		}
		dip = &sum.Dips[best]
		for i := range sum.Bands {
			if sum.Bands[i].Band.Name == dip.Band {
				bs = &sum.Bands[i]
			}
		}
	}
	if bs != nil {
		e.Band, e.Target = bs.Band.Name, bs.Band.Reference()
		e.WorstSWR, e.RL = bs.WorstSWR, bs.WorstRL
	} else {
		e.RL = sum.BestRL
	}
	if dip != nil {
		e.ResFreq, e.ResSWR, e.ResR, e.Q = dip.Freq, dip.SWR, dip.R, dip.Q
		if dip.BW2.Found && !dip.BW2.LoOpen && !dip.BW2.HiOpen {
			e.BW2 = dip.BW2.Width
		}
		if e.Target == 0 {
			e.Target = dip.Freq
		}
	} else {
		e.Target = sum.MinSWRFreq
	}
	if v, ok := interpAt(r.Freq, r.Derived.SWR, e.Target); ok {
		e.SWRAtTarget = v
	}
	e.R, _ = interpAt(r.Freq, r.Derived.R, e.Target)
	e.X, _ = interpAt(r.Freq, r.Derived.X, e.Target)
	return e
}

// calLabelLocked is the short calibration state of r: "calibrated",
// "not calibrated", "calibration does not fit" or "from file"; ok is true
// only for "calibrated". s.mu must be held.
func (s *Server) calLabelLocked(r *Result) (label string, ok bool) {
	_, calOK := s.calNoteLocked(r)
	switch {
	case r.Source != nil:
		return "from file", false
	case !r.CalApplied:
		return "not calibrated", false
	case calOK:
		return "calibrated", true
	}
	return "calibration does not fit", false
}

// addHistory appends entries to an antenna's history (creating the record
// if needed) and returns the drift report of the last one. Entries already
// logged (same key) are not added twice.
func (s *Server) addHistory(name string, entries ...HistoryEntry) (*AntennaInfo, error) {
	a, err := s.store.LoadAntenna(name)
	if errors.Is(err, fs.ErrNotExist) {
		a = &AntennaInfo{Name: strings.TrimSpace(name), Updated: time.Now()}
		err = a.clean()
	}
	if err != nil {
		return nil, err
	}
	a.History = mergeHistory(a.History, entries...)
	return a, s.store.SaveAntenna(a)
}

func mergeHistory(h []HistoryEntry, entries ...HistoryEntry) []HistoryEntry {
	for _, e := range entries {
		dup := false
		for _, old := range h {
			dup = dup || old.Key == e.Key
		}
		if !dup {
			h = append(h, e)
		}
	}
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	return h
}

// ---------- drift detection ----------

// DriftWarning is a plain-language finding.
type DriftWarning struct {
	Code  string `json:"code"`  // resonance, swr, loss, cal
	Level string `json:"level"` // warn, bad, info
	Title string `json:"title"`
	Text  string `json:"text"`
}

// DriftReport compares the latest entry with the earlier entries of the
// same profile and band.
type DriftReport struct {
	Latest   HistoryEntry   `json:"latest"`
	Previous *HistoryEntry  `json:"previous,omitempty"` // the one just before
	Earlier  int            `json:"earlier"`            // earlier entries in the same group
	Line     string         `json:"line"`               // "previous measurement" summary
	Warnings []DriftWarning `json:"warnings"`
}

// Drift thresholds.
const (
	driftResFrac   = 0.0025 // resonance shift as a fraction of f…
	driftResMinHz  = 1e6    // …but at least 1 MHz
	driftSWRDelta  = 0.25   // worst in-band SWR rise
	driftQDrop     = 0.25   // relative Q drop (dip broadening)
	driftRRise     = 0.30   // relative rise of R at resonance…
	driftRRiseOhms = 8.0    // …and at least this many ohms
	driftBaseline  = 5      // earlier entries used for the baseline (median)
)

func median(v []float64) float64 {
	var x []float64
	for _, a := range v {
		if a > 0 && !math.IsNaN(a) {
			x = append(x, a)
		}
	}
	if len(x) == 0 {
		return 0
	}
	sort.Float64s(x)
	n := len(x)
	if n%2 == 1 {
		return x[n/2]
	}
	return (x[n/2-1] + x[n/2]) / 2
}

// detectDrift looks at the last entry of h. It returns nil for an empty
// history.
func detectDrift(h []HistoryEntry) *DriftReport {
	if len(h) == 0 {
		return nil
	}
	last := h[len(h)-1]
	rep := &DriftReport{Latest: last, Warnings: []DriftWarning{}}
	var earlier []HistoryEntry
	for _, e := range h[:len(h)-1] {
		if e.group() == last.group() && e.Key != last.Key {
			earlier = append(earlier, e)
		}
	}
	rep.Earlier = len(earlier)
	what := last.Band + " MHz band"
	if last.Band == "" {
		what = last.Range
	}
	if len(earlier) == 0 {
		rep.Line = "First logged measurement of this antenna for the " + what + "."
		return rep
	}
	prev := earlier[len(earlier)-1]
	rep.Previous = &prev
	base := earlier
	if len(base) > driftBaseline {
		base = base[len(base)-driftBaseline:]
	}
	pick := func(f func(HistoryEntry) float64) float64 {
		var v []float64
		for _, e := range base {
			v = append(v, f(e))
		}
		return median(v)
	}
	bRes := pick(func(e HistoryEntry) float64 { return e.ResFreq })
	bWorst := pick(func(e HistoryEntry) float64 { return e.WorstSWR })
	bQ := pick(func(e HistoryEntry) float64 { return e.Q })
	bR := pick(func(e HistoryEntry) float64 { return e.ResR })
	since := "earlier measurements"
	if len(base) == 1 {
		since = "the measurement of " + prev.Measured.Format("2006-01-02")
	}

	// Previous-measurement line.
	parts := []string{"Previous measurement " + prev.Measured.Format("2006-01-02 15:04")}
	if prev.ResFreq > 0 && last.ResFreq > 0 {
		parts = append(parts, fmt.Sprintf("resonance %.2f → %.2f MHz (%s)", prev.ResFreq/1e6, last.ResFreq/1e6, signed1((last.ResFreq-prev.ResFreq)/1e6)))
	}
	if prev.WorstSWR > 0 && last.WorstSWR > 0 {
		parts = append(parts, fmt.Sprintf("worst SWR %s → %s", swrF(prev.WorstSWR), swrF(last.WorstSWR)))
	} else if prev.SWRAtTarget > 0 && last.SWRAtTarget > 0 {
		parts = append(parts, fmt.Sprintf("SWR at %.2f MHz %s → %s", last.Target/1e6, swrF(prev.SWRAtTarget), swrF(last.SWRAtTarget)))
	}
	if prev.Q > 0 && last.Q > 0 {
		parts = append(parts, fmt.Sprintf("Q %.0f → %.0f", prev.Q, last.Q))
	}

	// Resonance shift.
	if bRes > 0 && last.ResFreq > 0 {
		df := last.ResFreq - bRes
		if math.Abs(df) > math.Max(driftResFrac*bRes, driftResMinHz) {
			dir := "up"
			hint := "the element got shorter, or it is bent or loose"
			if df < 0 {
				dir, hint = "down", "something detunes it (enclosure, hand, nearby metal), or the element got longer"
			}
			rep.Warnings = append(rep.Warnings, DriftWarning{Code: "resonance", Level: "warn", Title: "Resonance moved",
				Text: fmt.Sprintf("The resonance went %s by %.1f MHz compared with %s (%.2f → %.2f MHz): %s.",
					dir, math.Abs(df)/1e6, since, bRes/1e6, last.ResFreq/1e6, hint)})
		}
	}
	// SWR worse.
	if bWorst > 0 && last.WorstSWR > 0 {
		worse := last.WorstSWR-bWorst >= driftSWRDelta && last.WorstSWR >= 1.5
		gradeDrop := rank(rf.Verdict(last.WorstSWR)) > rank(rf.Verdict(bWorst))
		if worse || gradeDrop {
			lvl := "warn"
			if rf.Verdict(last.WorstSWR) == "bad" {
				lvl = "bad"
			}
			rep.Warnings = append(rep.Warnings, DriftWarning{Code: "swr", Level: lvl, Title: "SWR got worse",
				Text: fmt.Sprintf("Worst SWR in the %s band went from %s to %s compared with %s.", last.Band, swrF(bWorst), swrF(last.WorstSWR), since)})
		}
	}
	// Signs of rising loss: broader dip (lower Q), higher R at resonance.
	var signs []string
	if bQ > 0 && last.Q > 0 && last.Q < (1-driftQDrop)*bQ {
		signs = append(signs, fmt.Sprintf("the dip is broader (Q %.0f → %.0f)", bQ, last.Q))
	}
	if bR > 0 && last.ResR > 0 && last.ResR-bR >= driftRRiseOhms && last.ResR > (1+driftRRise)*bR {
		signs = append(signs, fmt.Sprintf("R at resonance rose (%.0f → %.0f Ω)", bR, last.ResR))
	}
	if len(signs) > 0 {
		rep.Warnings = append(rep.Warnings, DriftWarning{Code: "loss", Level: "warn", Title: "Signs of rising loss",
			Text: "Compared with " + since + " " + strings.Join(signs, " and ") +
				". Water in the coax, a corroded connector or a damaged radiator do this: check the cable and connectors."})
	}
	if len(rep.Warnings) > 0 {
		cals := map[string]bool{}
		for _, e := range base {
			cals[e.Cal] = true
		}
		if len(cals) > 1 || !cals[last.Cal] {
			rep.Warnings = append(rep.Warnings, DriftWarning{Code: "cal", Level: "info", Title: "Calibration differs",
				Text: "The compared measurements were not calibrated the same way (" + last.Cal + " now): part of the change may come from that."})
		}
	}
	if len(rep.Warnings) == 0 {
		parts = append(parts, "no drift")
	} else {
		var t []string
		for _, w := range rep.Warnings {
			if w.Level != "info" {
				t = append(t, strings.ToLower(w.Title))
			}
		}
		parts = append(parts, "drift: "+strings.Join(t, ", "))
	}
	rep.Line = strings.Join(parts, " · ") + "."
	return rep
}

func rank(v string) int {
	switch v {
	case "good":
		return 0
	case "ok":
		return 1
	case "bad":
		return 2
	}
	return -1
}

// ---------- API ----------

// HistoryView is GET /api/antennas/{name}/history.
type HistoryView struct {
	Name    string         `json:"name"`
	History []HistoryEntry `json:"history"`
	Drift   *DriftReport   `json:"drift,omitempty"`
}

func (s *Server) historyView(a *AntennaInfo) HistoryView {
	v := HistoryView{Name: a.Name, History: a.History, Drift: detectDrift(a.History)}
	if v.History == nil {
		v.History = []HistoryEntry{}
	}
	return v
}

func (s *Server) hHistoryGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a, err := s.store.LoadAntenna(name)
	if errors.Is(err, fs.ErrNotExist) {
		writeJSON(w, 200, HistoryView{Name: name, History: []HistoryEntry{}})
		return
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.historyView(a))
}

// hHistoryLog logs the current result ("Log this measurement").
func (s *Server) hHistoryLog(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if _, err := SnapshotFile(name); err != nil {
		writeErr(w, 400, errors.New("give the antenna a name (1-80 characters)"))
		return
	}
	s.mu.Lock()
	last := s.last
	if last == nil || last.Tune != nil {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("sweep a profile first (TUNE sweeps are not logged)"))
		return
	}
	e := s.historyEntryLocked(last, "manual")
	s.mu.Unlock()
	a, err := s.addHistory(name, e)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.historyView(a))
}

// hHistoryUndo removes the last entry.
func (s *Server) hHistoryUndo(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.LoadAntenna(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if len(a.History) == 0 {
		writeErr(w, 409, errors.New("the history is empty"))
		return
	}
	a.History = a.History[:len(a.History)-1]
	if err := s.store.SaveAntenna(a); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.historyView(a))
}

// hHistoryCSV serves the history as a spreadsheet.
func (s *Server) hHistoryCSV(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.LoadAntenna(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	var b bytes.Buffer
	cw := csv.NewWriter(&b)
	_ = cw.Write([]string{"measured", "logged", "kind", "profile", "band", "range", "source", "file", "target_mhz", "resonance_mhz",
		"swr_at_resonance", "swr_at_target", "worst_swr_in_band", "return_loss_db", "r_ohm", "x_ohm", "r_at_resonance_ohm", "q", "bw_swr2_mhz", "calibration"})
	f := func(v float64, prec int) string {
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'f', prec, 64)
	}
	for _, e := range a.History {
		_ = cw.Write([]string{e.Measured.Format(time.RFC3339), e.Time.Format(time.RFC3339), e.Kind, e.Profile, e.Band, e.Range, e.Source, e.File,
			f(e.Target/1e6, 4), f(e.ResFreq/1e6, 4), f(e.ResSWR, 3), f(e.SWRAtTarget, 3), f(e.WorstSWR, 3), f(e.RL, 2),
			strconv.FormatFloat(e.R, 'f', 2, 64), strconv.FormatFloat(e.X, 'f', 2, 64), f(e.ResR, 2), f(e.Q, 1), f(e.BW2/1e6, 3), e.Cal})
	}
	cw.Flush()
	base, _ := SnapshotFile(a.Name + "-history")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, strings.TrimSuffix(base, ".json")))
	_, _ = w.Write(b.Bytes())
}
