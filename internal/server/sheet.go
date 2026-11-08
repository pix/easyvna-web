package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- antenna records (Antenna sheet dialog) ----------

// AntennaInfo is what the user tells about one antenna in the Antenna
// sheet dialog. It is stored per antenna name in antennas/<name>.json and
// fills the dialog again the next time that name is used.
type AntennaInfo struct {
	Name      string  `json:"name"`
	Type      string  `json:"type,omitempty"`
	Band      string  `json:"band,omitempty"` // band / purpose, e.g. "869 MHz LoRa EU"
	Connector string  `json:"connector,omitempty"`
	LengthMm  float64 `json:"lengthMm,omitempty"`
	Ground    string  `json:"ground,omitempty"` // ground plane / mounting
	Cable     string  `json:"cable,omitempty"`  // feed cable type and length
	Maker     string  `json:"maker,omitempty"`  // maker / source
	Owner     string  `json:"owner,omitempty"`  // owner / location
	Notes     string  `json:"notes,omitempty"`
	Link      string  `json:"link,omitempty"`  // product / source page (http or https, see CleanLink)
	Photo     string  `json:"photo,omitempty"` // data:image/jpeg;base64,... (downscaled by the page)
	// PhotoCaption and LinkNote are small print shown under the photo and
	// under the link (plain text, at most maxSmallPrint characters).
	PhotoCaption string    `json:"photoCaption,omitempty"`
	LinkNote     string    `json:"linkNote,omitempty"`
	Updated      time.Time `json:"updated"`
	// Setup is the last setup check made with this antenna (see setup.go).
	Setup *SetupNote `json:"setup,omitempty"`
	// History holds a summary of each measurement of this antenna (sheet,
	// report, "Log this measurement"); see history.go.
	History []HistoryEntry `json:"history,omitempty"`
}

// AntennaListEntry is one row of GET /api/antennas.
type AntennaListEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type,omitempty"`
	Band     string    `json:"band,omitempty"`
	HasPhoto bool      `json:"hasPhoto"`
	Updated  time.Time `json:"updated"`
	Logs     int       `json:"logs"` // history entries
}

const maxPhotoBytes = 3 << 20 // data URL length

// maxSmallPrint is the length limit (characters) of the photo caption and
// the link note.
const maxSmallPrint = 200

// cleanSmallPrint makes a photo caption or link note one plain line of at
// most maxSmallPrint characters (it is escaped when shown).
func cleanSmallPrint(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxSmallPrint {
		s = string(r[:maxSmallPrint])
	}
	return s
}

// smallPrintMm estimates the height (mm) small print of n characters takes
// in a block w mm wide at 6.2 pt: about 1.25 mm per character, 2.6 mm per
// line.
func smallPrintMm(text string, w float64) float64 {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	perLine := math.Max(8, math.Floor(w/1.25))
	return math.Ceil(float64(n)/perLine)*2.6 + 0.6
}

// clean trims every field and checks the name and the photo.
func (a *AntennaInfo) clean() error {
	for _, p := range []*string{&a.Name, &a.Type, &a.Band, &a.Connector, &a.Ground, &a.Cable, &a.Maker, &a.Owner} {
		*p = strings.TrimSpace(*p)
		if len(*p) > 120 {
			*p = (*p)[:120]
		}
	}
	a.Notes = strings.TrimSpace(a.Notes)
	if len(a.Notes) > 2000 {
		a.Notes = a.Notes[:2000]
	}
	a.PhotoCaption, a.LinkNote = cleanSmallPrint(a.PhotoCaption), cleanSmallPrint(a.LinkNote)
	if _, err := SnapshotFile(a.Name); err != nil {
		return errors.New("give the antenna a name (1-80 characters)")
	}
	if a.LengthMm < 0 || a.LengthMm > 1e5 || math.IsNaN(a.LengthMm) {
		return errors.New("length must be between 0 and 100000 mm")
	}
	link, err := CleanLink(a.Link)
	if err != nil {
		return err
	}
	a.Link = link
	return checkPhoto(a.Photo)
}

// checkPhoto accepts "" or a JPEG, PNG or WebP data: URL under the size
// limit.
func checkPhoto(photo string) error {
	if photo == "" {
		return nil
	}
	ok := false
	for _, p := range []string{"data:image/jpeg;base64,", "data:image/png;base64,", "data:image/webp;base64,"} {
		ok = ok || strings.HasPrefix(photo, p)
	}
	b64 := photo[strings.IndexByte(photo, ',')+1:]
	if !ok || len(photo) > maxPhotoBytes || strings.Trim(b64, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=") != "" {
		return errors.New("the photo must be a JPEG, PNG or WebP image under 2 MB")
	}
	return nil
}

func antennaPath(name string) (string, error) {
	f, err := SnapshotFile(name)
	if err != nil {
		return "", err
	}
	return "antennas/" + f, nil
}

// SaveAntenna stores an antenna record.
func (st *Store) SaveAntenna(a *AntennaInfo) error {
	p, err := antennaPath(a.Name)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return st.b.WriteFile(p, b)
}

// LoadAntenna reads an antenna record by name.
func (st *Store) LoadAntenna(name string) (*AntennaInfo, error) {
	p, err := antennaPath(name)
	if err != nil {
		return nil, err
	}
	b, err := st.b.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var a AntennaInfo
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAntennas lists antenna records, most recently updated first.
func (st *Store) ListAntennas() []AntennaListEntry {
	out := []AntennaListEntry{}
	for _, f := range st.listMatch("antennas", "*.json") {
		b, err := st.b.ReadFile("antennas/" + f.Name)
		if err != nil {
			continue
		}
		var a AntennaInfo
		if json.Unmarshal(b, &a) != nil || a.Name == "" {
			continue
		}
		out = append(out, AntennaListEntry{Name: a.Name, Type: a.Type, Band: a.Band, HasPhoto: a.Photo != "", Updated: a.Updated, Logs: len(a.History)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

func (s *Server) hAntennaList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.ListAntennas())
}

func (s *Server) hAntennaGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.LoadAntenna(r.PathValue("name"))
	if err != nil {
		code := 400
		if errors.Is(err, fs.ErrNotExist) {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, 200, a)
}

// ---------- the one-page antenna sheet ----------

type sheetFig struct {
	K, V, Sub string
	Class     string // good / ok / bad for SWR figures
}

type sheetData struct {
	ID        string
	A         AntennaInfo
	Generated time.Time
	App       string
	URL       string
	Device    string
	Firmware  string
	Simulated bool
	File      string // set for a measurement opened from a file
	Sweep     string
	CalState  string
	CalOK     bool
	Verdict   string // PASS / OK / FAIL / CHECK / N/A
	VClass    string
	VNote     string
	Figs      []sheetFig
	SWR, RL   template.HTML
	Z, Smith  template.HTML
	Legend    []string
	Details   []reportKV
	Trim      []TrimEntry
	TrimMore  int
	TrimTgt   float64
	TrimNext  *rf.TrimPrediction
	Drift     *DriftReport
	Ref       *rf.RefDelta // comparison with the REF trace, if one is stored
	Warnings  []rf.Warning
	LinkText  string
	QR        template.HTML
	Criteria  string // "Acceptance: band SWR < 2.0, resonance …"
	Multi     string // Measure ×N: "average of 5 · resonance σ …"
	Setup     *SetupNote
	Limit     float64
	Good      float64
}

// interpAt linearly interpolates y(f); ok is false outside the sweep.
func interpAt(freq, y []float64, f float64) (float64, bool) {
	n := len(freq)
	if n == 0 || len(y) != n || f < freq[0] || f > freq[n-1] {
		return 0, false
	}
	i := sort.SearchFloat64s(freq, f)
	if i == 0 {
		return y[0], true
	}
	if i >= n {
		return y[n-1], true
	}
	f0, f1 := freq[i-1], freq[i]
	if f1 == f0 {
		return y[i], true
	}
	t := (f - f0) / (f1 - f0)
	return y[i-1] + t*(y[i]-y[i-1]), true
}

func swrClass(v float64) string { return swrClassAt(v, rf.DefaultSWRLimit) }

// swrClassAt colours an SWR value against the band SWR limit.
func swrClassAt(v, limit float64) string {
	if v <= 0 {
		return ""
	}
	return rf.VerdictAt(v, limit)
}

// sheetFigures computes the key figures of r for its first band.
func sheetFigures(r *Result) (figs []sheetFig, verdict, vclass string) {
	d, sum := r.Derived, r.Summary
	limit := r.Config.Accept.Limit()
	swrClass := func(v float64) string { return swrClassAt(v, limit) }
	swrAt := func(f float64) (sheetFig, bool) {
		v, ok := interpAt(r.Freq, d.SWR, f)
		return sheetFig{V: swrF(v), Sub: "@ " + mhzF(f) + " MHz", Class: swrClass(v)}, ok
	}
	var band *rf.BandSummary
	if len(sum.Bands) > 0 && !r.Config.Identify {
		band = &sum.Bands[0]
	}
	resF, resSWR := sum.MinSWRFreq, sum.MinSWR
	resK := "Best match"
	if band != nil && band.DipFreq != nil {
		resF, resK = *band.DipFreq, "Resonance"
		if band.DipSWR != nil {
			resSWR = *band.DipSWR
		}
	} else if sum.Resonance != nil {
		resF, resK = sum.Resonance.Freq, "Resonance"
		if v, ok := interpAt(r.Freq, d.SWR, resF); ok {
			resSWR = v
		}
	}
	figs = append(figs, sheetFig{K: resK, V: mhzF(resF), Sub: "MHz"},
		sheetFig{K: "SWR there", V: swrF(resSWR), Class: swrClass(resSWR)})
	centre := resF
	if band != nil {
		b := band.Band
		centre = b.Centre()
		for _, e := range []struct {
			k string
			f float64
		}{{"SWR band centre", centre}, {"SWR low edge", b.Lo}, {"SWR high edge", b.Hi}} {
			if fg, ok := swrAt(e.f); ok {
				fg.K = e.k
				figs = append(figs, fg)
			} else {
				figs = append(figs, sheetFig{K: e.k, V: "–", Sub: "outside sweep"})
			}
		}
		figs = append(figs, sheetFig{K: "Worst in band", V: swrF(band.WorstSWR), Sub: "@ " + mhzF(band.WorstSWRFreq) + " MHz", Class: swrClass(band.WorstSWR)},
			sheetFig{K: "Return loss", V: fmt.Sprintf("%.1f", band.WorstRL), Sub: "dB, worst in band"},
			sheetFig{K: "Mismatch loss", V: fmt.Sprintf("%.2f", band.WorstML), Sub: "dB, worst in band"})
		verdict, vclass = verdictLabel[band.Verdict], verdictClass(band.Verdict)
		if verdict == "" {
			verdict = "N/A"
		}
	} else {
		figs = append(figs, sheetFig{K: "Return loss", V: fmt.Sprintf("%.1f", sum.BestRL), Sub: "dB at best match"},
			sheetFig{K: "Mismatch loss", V: fmt.Sprintf("%.2f", sum.MLAtMin), Sub: "dB at best match"})
		verdict, vclass = "N/A", "na"
	}
	figs = append(figs, sheetFig{K: "SWR < 2 bandwidth", V: reportFuncs["bw"].(func(rf.Bandwidth) string)(sum.BW2)})
	if rr, ok := interpAt(r.Freq, d.R, centre); ok {
		xx, _ := interpAt(r.Freq, d.X, centre)
		figs = append(figs, sheetFig{K: "Z at " + mhzF(centre) + " MHz", V: reportFuncs["z"].(func(float64, float64) string)(rr, xx)})
	}
	figs = append(figs, sheetFig{K: "Q (estimated)", V: reportFuncs["q"].(func(float64) string)(sum.Q)})
	if len(sum.Warnings) > 0 && band != nil {
		verdict, vclass = "CHECK", "na"
	}
	return figs, verdict, vclass
}

// buildSheet renders the one-page sheet for antenna a from the last sweep.
func (s *Server) buildSheet(id string, a AntennaInfo) ([]byte, *HistoryEntry, error) {
	s.mu.Lock()
	r := s.last
	if r == nil {
		s.mu.Unlock()
		return nil, nil, errors.New("no sweep yet: sweep the antenna first")
	}
	var entry *HistoryEntry
	if r.Tune == nil {
		e := s.historyEntryLocked(r, "sheet")
		entry = &e
	}
	d := sheetData{ID: id, A: a, Generated: time.Now(), App: s.opt.AppName, URL: s.opt.HomeURL, Warnings: r.Summary.Warnings,
		LinkText: LinkText(a.Link), QR: qrSVG(a.Link, 22)}
	if d.App == "" {
		d.App = "EasyVNA Web"
	}
	if d.URL == "" {
		d.URL = "https://github.com/pix/easyvna-web"
	}
	if s.info != nil {
		d.Device, d.Firmware, d.Simulated = s.info.Model, s.info.Version, s.info.Mock
		if s.info.Experimental {
			d.Device += " (experimental driver)"
		}
		if s.info.Fork != "" {
			d.Firmware += " · " + s.info.Fork
		}
	}
	d.CalState, d.CalOK = s.calNoteLocked(r)
	if !r.CalApplied && r.Source == nil {
		d.CalState = "NOT CALIBRATED: figures include the cable and the VNA's own errors"
	}
	label := "Custom sweep"
	if p, ok := s.profile(r.Config.Profile); ok {
		label = p.Label + " MHz profile"
		if p.Identify {
			label = p.Label + " sweep"
		}
	}
	d.Sweep = fmt.Sprintf("%s · %s · %d points · swept %s", label, mhzRange(r.Config.Start, r.Config.Stop), len(r.Freq), r.Time.Format("2006-01-02 15:04:05"))
	if src := r.Source; src != nil {
		d.File = src.File
		d.Device, d.Firmware, d.Simulated = "– (opened from a file)", "–", false
		d.Sweep = fmt.Sprintf("%s · %s · %s · %d points · opened %s", fileLabel(src), src.Format, mhzRange(r.Config.Start, r.Config.Stop), len(r.Freq), r.Time.Format("2006-01-02 15:04:05"))
		for _, n := range src.Notes {
			d.Sweep += " · " + n
		}
	}
	if r.AvgN > 1 {
		d.Sweep += fmt.Sprintf(" · average of %d", r.AvgN)
	}
	d.Figs, d.Verdict, d.VClass = sheetFigures(r)
	d.Ref = r.Ref
	d.Limit, d.Good = r.Config.Accept.Limit(), r.Config.Accept.GoodLimit()
	if r.Accept != nil {
		d.Criteria = "Acceptance: " + r.Accept.Window.Text
	} else {
		d.Criteria = fmt.Sprintf("Acceptance: band SWR < %.1f", d.Limit)
	}
	if r.Multi != nil {
		d.Multi = r.Multi.Text
	}
	if n := s.setupNoteFor(r); n != nil {
		d.Setup = n
	} else {
		d.Setup = a.Setup
	}
	if d.Verdict == "CHECK" {
		d.VNote = "measurement warning, see below"
	}
	ref := s.refs[r.Config.Profile]
	if r.Source != nil {
		ref = s.refs[s.cfg.Profile] // a file is compared with the current profile's REF
	}
	if ref != nil && (len(ref.Freq) != len(r.Freq) || ref.Config.Start != r.Config.Start || ref.Config.Stop != r.Config.Stop) {
		ref = nil
	}
	var envMin, envMax []float64
	holdN := 0
	if hs := s.hold; hs.h != nil && len(hs.h.Positions) > 0 && hs.profile == r.Config.Profile {
		sum := hs.h.Summary()
		if len(sum.EnvMin) == len(r.Freq) {
			envMin, envMax, holdN = sum.EnvMin, sum.EnvMax, sum.Positions
		}
	}
	s.mu.Unlock()

	// Plots: same style as the report, with REF (dashed) and HOLD (dotted).
	maxSWR := 5.0
	if r.Config.Identify {
		maxSWR = 10
	}
	swrSeries := []svgSeries{}
	if envMin != nil {
		swrSeries = append(swrSeries, svgSeries{Y: envMin, Color: svgHold, Width: 1.6, Dash: "2 3"}, svgSeries{Y: envMax, Color: svgHold, Width: 1.6, Dash: "2 3"})
	}
	rlSeries := []svgSeries{}
	if ref != nil {
		swrSeries = append(swrSeries, svgSeries{Y: ref.Derived.SWR, Color: svgRef, Width: 1.8, Dash: "7 4"})
		rlSeries = append(rlSeries, svgSeries{Y: ref.Derived.S11dB, Color: svgRef, Width: 1.8, Dash: "7 4"})
	}
	swrSeries = append(swrSeries, svgSeries{Y: r.Derived.SWR, Color: svgTrace})
	rlSeries = append(rlSeries, svgSeries{Y: r.Derived.S11dB, Color: svgTrace})
	thr := limitLines(r.Config.Accept)
	var marks []svgMark
	for _, dp := range r.Summary.Dips {
		marks = append(marks, svgMark{F: dp.Freq, Label: fmt.Sprintf("%.1f", dp.Freq/1e6)})
	}
	// Small print under the photo / the link takes room from the SWR plot,
	// so the sheet stays one page.
	side := math.Max(31+smallPrintMm(a.PhotoCaption, 36), 26+smallPrintMm(a.LinkNote, 24))
	swrH := math.Max(40, 50-math.Max(0, side-45))
	d.SWR = svgPlot{Wmm: 190, Hmm: swrH, Freq: r.Freq, Series: swrSeries, YMin: 1, YMax: maxSWR, YTicks: niceTicks(1, maxSWR, 4),
		YFmt: func(v float64) string { return fmt.Sprintf("%g", v) }, YUnit: "SWR", Bands: r.Config.Bands, HLines: thr, Marks: marks,
		Title: "SWR", Class: "swr"}.render()
	lo := -30.0
	for _, v := range r.Derived.S11dB {
		lo = math.Min(lo, v)
	}
	lo = math.Max(-50, math.Floor(lo/10)*10)
	d.RL = svgPlot{Wmm: 136, Hmm: 33, Freq: r.Freq, Series: rlSeries, YMin: lo, YMax: 0, YTicks: niceTicks(lo, 0, 3),
		YFmt: func(v float64) string { return fmt.Sprintf("%g", v) }, YUnit: "dB", Bands: r.Config.Bands,
		HLines: []svgHLine{{Y: -rlOfSWR(d.Limit), Label: "SWR " + fmtG(d.Limit), Dash: "9 5"}}, Title: "Return loss (S11)", Class: "rl"}.render()
	d.Z = impedancePlotWith(r, 136, 33, ref)
	d.Smith = smithSVG(r, 52, ref)
	d.Legend = []string{"— measured"}
	if ref != nil {
		d.Legend = append(d.Legend, "- - REF "+ref.Time.Format("15:04:05"))
	}
	if holdN > 0 {
		d.Legend = append(d.Legend, fmt.Sprintf("··· HOLD min / max over %d position(s)", holdN))
	}
	if len(r.Config.Bands) > 0 {
		d.Legend = append(d.Legend, "shaded: "+r.Config.Bands[0].Name+" MHz band")
	}

	for _, kv := range []reportKV{{"Connector", a.Connector}, {"Ground plane / mounting", a.Ground}, {"Feed cable", a.Cable},
		{"Maker / source", a.Maker}, {"Owner / location", a.Owner}} {
		if kv.V != "" {
			d.Details = append(d.Details, kv)
		}
	}
	if a.LengthMm > 0 {
		d.Details = append([]reportKV{{"Physical length", fmt.Sprintf("%g mm", a.LengthMm)}}, d.Details...)
	}
	if t, err := s.store.LoadTrim(a.Name); err == nil && len(t.Entries) > 0 {
		e := t.Entries
		if len(e) > 4 {
			d.TrimMore, e = len(e)-4, e[len(e)-4:]
		}
		d.Trim, d.TrimTgt = e, t.Target
		t.predict()
		d.TrimNext = t.Prediction
	}
	if entry != nil {
		d.Drift = detectDrift(mergeHistory(append([]HistoryEntry(nil), a.History...), *entry))
	}
	var buf bytes.Buffer
	if err := sheetTmpl.Execute(&buf, d); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), entry, nil
}

// hSheet saves the antenna record and builds the sheet from the last sweep
// (POST /api/sheet {"antenna": AntennaInfo}). The sheet is saved with the
// reports.
func (s *Server) hSheet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Antenna AntennaInfo `json:"antenna"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxPhotoBytes+(64<<10)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, 400, fmt.Errorf("bad JSON: %w", err))
		return
	}
	a := req.Antenna
	if err := a.clean(); err != nil {
		writeErr(w, 400, err)
		return
	}
	now := time.Now()
	a.Updated = now
	file, err := ReportFileName(a.Name+"-sheet", now)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if old, err := s.store.LoadAntenna(a.Name); err == nil {
		a.History = old.History // the dialog never sends the history
	}
	html, entry, err := s.buildSheet(strings.TrimSuffix(file, ".html"), a)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	if entry != nil {
		a.History = mergeHistory(a.History, *entry)
	}
	s.mu.Lock()
	if n := s.setupNoteFor(s.last); n != nil {
		a.Setup = n
	}
	s.mu.Unlock()
	if err := s.store.SaveAntenna(&a); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := s.store.SaveReportFile(file, html); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"file": file, "url": "api/reports/" + file})
}

// limitLines are the SWR plot's threshold lines: the band SWR limit and the
// GOOD threshold (2 and 1.5 by default).
func limitLines(a rf.Acceptance) []svgHLine {
	return []svgHLine{{Y: a.Limit(), Label: "SWR " + fmtG(a.Limit()), Dash: "9 5"}, {Y: a.GoodLimit(), Label: "SWR " + fmtG(a.GoodLimit()), Dash: "2 4"}}
}

func fmtG(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

// rlOfSWR is the return loss (dB) at an SWR.
func rlOfSWR(s float64) float64 { return -20 * math.Log10((s-1)/(s+1)) }

// setupNoteFor is the last setup check when it was made on r's profile
// (nil otherwise). s.mu must be held.
func (s *Server) setupNoteFor(r *Result) *SetupNote {
	if r == nil || s.setup == nil || s.setup.note == nil || s.setup.note.Profile != resultProfile(r) {
		return nil
	}
	n := *s.setup.note
	return &n
}
