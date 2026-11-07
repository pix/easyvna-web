package server

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- report model ----------

type reportKV struct{ K, V string }

type reportVerdict struct {
	Profile   string
	Band      string // "869 MHz band"
	Range     string // "863.00–870.00 MHz"
	Class     string // good / ok / bad / na
	Label     string // PASS / OK / FAIL / N/A / CHECK
	WorstSWR  string
	WorstAt   string
	Resonance string
	Offset    string
	RL        string
	ML        string
	BW2       string
	Q         string
	Note      string
}

type reportSection struct {
	Label   string
	Res     *Result
	SWR     template.HTML
	RL      template.HTML
	Smith   template.HTML
	Key     []reportKV
	KeyRows [][]reportKV // Key in pairs, for a compact two-column table
	CalNote string
	CalOK   bool
}

type reportHold struct {
	Label     string
	Positions int
	Spread    string
	Range     string
	WorstBand string
	Rows      []HoldPosView
}

type reportData struct {
	ReportID  string
	Name      string
	Notes     string
	Generated time.Time
	Device    string
	Firmware  string
	Simulated bool
	Profiles  string
	CalState  string
	CalOK     bool
	Verdicts  []reportVerdict
	Sections  []reportSection
	Trim      *TrimSession
	TrimSpark template.HTML
	Hold      *reportHold
	Compare   *CompareSession
	Warnings  []rf.Warning
}

var verdictLabel = map[string]string{"good": "PASS", "ok": "OK", "bad": "FAIL"}

func mhzF(f float64) string { return fmt.Sprintf("%.2f", f/1e6) }

func swrF(v float64) string {
	if v >= 99 {
		return ">99"
	}
	return fmt.Sprintf("%.2f", v)
}

// signed1 formats v with one decimal and a sign, without "-0.0".
func signed1(v float64) string {
	r := math.Round(v*10) / 10
	if r == 0 {
		return "0.0"
	}
	return fmt.Sprintf("%+.1f", r)
}

var reportFuncs = template.FuncMap{
	"mhz":  mhzF,
	"mhz3": func(f float64) string { return fmt.Sprintf("%.3f", f/1e6) },
	"f1":   func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"f2":   func(v float64) string { return fmt.Sprintf("%.2f", v) },
	"sgn1": signed1,
	"swr":  swrF,
	"neg":  func(v float64) float64 { return -v },
	"time": func(t time.Time) string { return t.Format("2006-01-02 15:04") },
	"hms":  func(t time.Time) string { return t.Format("15:04:05") },
	"inc":  func(i int) int { return i + 1 },
	"dmhz": func(f, t float64) string { return signed1((f - t) / 1e6) },
	"z": func(r, x float64) string {
		sign := "+"
		if x < 0 {
			sign = "−"
		}
		return fmt.Sprintf("%.1f %s j%.1f Ω", r, sign, math.Abs(x))
	},
	"bw": func(b rf.Bandwidth) string {
		if !b.Found {
			return "–"
		}
		s := fmt.Sprintf("%.1f MHz", b.Width/1e6)
		if b.LoOpen || b.HiOpen {
			s = "≥ " + s
		}
		return s
	},
	"q": func(q float64) string {
		if q <= 0 {
			return "–"
		}
		return fmt.Sprintf("%.0f", q)
	},
}

var reportTmpl = template.Must(template.New("report").Funcs(reportFuncs).Parse(reportHTML))

func (s *Server) calNoteLocked(r *Result) (string, bool) {
	a := s.cal.Active
	switch {
	case !r.CalApplied:
		return "not calibrated", false
	case a == nil:
		return "calibrated (range unknown)", false
	case a.Start == r.Config.Start && a.Stop == r.Config.Stop:
		return "calibrated for this range (" + a.Source + ")", true
	case a.Start <= r.Config.Start && a.Stop >= r.Config.Stop:
		return "calibration covers this range (" + mhzRange(a.Start, a.Stop) + ", interpolated)", true
	}
	return "calibration made for " + mhzRange(a.Start, a.Stop) + ", not this range", false
}

func bandVerdict(prof string, b rf.BandSummary, warned bool) reportVerdict {
	v := reportVerdict{Profile: prof, Range: mhzRange(b.Band.Lo, b.Band.Hi), Class: verdictClass(b.Verdict)}
	v.Band = b.Band.Name + " MHz band"
	if b.Band.Hi-b.Band.Lo < 1e6 {
		v.Band = b.Band.Name + " MHz sub-band"
	}
	v.Label = verdictLabel[b.Verdict]
	if v.Label == "" {
		v.Label = "N/A"
	}
	if warned {
		v.Label, v.Class, v.Note = "CHECK", "na", "measurement warning: see above"
	}
	v.WorstSWR, v.WorstAt = swrF(b.WorstSWR), mhzF(b.WorstSWRFreq)+" MHz"
	v.RL, v.ML = fmt.Sprintf("%.1f dB", b.WorstRL), fmt.Sprintf("%.2f dB", b.WorstML)
	v.Resonance, v.Offset = "–", "–"
	if b.DipFreq != nil {
		v.Resonance = mhzF(*b.DipFreq) + " MHz"
		ref := b.Band.Reference()
		v.Offset = signed1(*b.DipOffset/1e6) + " MHz vs " + fmt.Sprintf("%.3g", ref/1e6)
		if b.DipEdgeOff != nil && *b.DipEdgeOff == 0 {
			v.Offset += " (in band)"
		}
	}
	if !b.Covered && v.Note == "" {
		v.Note = "sweep does not cover the whole band"
	}
	return v
}

func verdictClass(v string) string {
	switch v {
	case "good", "ok", "bad":
		return v
	}
	return "na"
}

// buildReport assembles the report for the chosen profiles.
func (s *Server) buildReport(id, name, notes string, profiles []string, trimName string) ([]byte, error) {
	s.mu.Lock()
	d := reportData{ReportID: id, Name: name, Notes: notes, Generated: time.Now()}
	if s.info != nil {
		d.Device = s.info.Model
		d.Firmware = s.info.Version
		if s.info.Fork != "" {
			d.Firmware += " · " + s.info.Fork
		}
		d.Simulated = s.info.Mock
	}
	switch st := s.cal.Status; {
	case !st.Known:
		d.CalState = "unknown"
	case st.Applied && s.cal.Active != nil:
		d.CalState, d.CalOK = "applied · "+s.cal.Active.Source+", "+mhzRange(s.cal.Active.Start, s.cal.Active.Stop), true
	case st.Applied:
		d.CalState, d.CalOK = "applied (range unknown)", true
	default:
		d.CalState = "not applied"
	}
	order, labels := map[string]int{}, map[string]string{}
	for i, p := range s.profiles {
		order[p.ID] = i
		if p.Identify {
			labels[p.ID] = p.Label + " sweep · " + p.Sub
		} else {
			labels[p.ID] = p.Label + " MHz profile · " + p.Sub
		}
	}
	var picked []*Result
	for _, pid := range profiles {
		if r := s.lastByProfile[pid]; r != nil {
			picked = append(picked, r)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return order[picked[i].Config.Profile] < order[picked[j].Config.Profile] })
	var profNames []string
	for _, r := range picked {
		label := labels[r.Config.Profile]
		if label == "" {
			label = "Custom sweep · " + mhzRange(r.Config.Start, r.Config.Stop)
		}
		profNames = append(profNames, strings.SplitN(label, " · ", 2)[0])
		warned := len(r.Summary.Warnings) > 0
		sec := reportSection{Label: label, Res: r}
		sec.CalNote, sec.CalOK = s.calNoteLocked(r)
		if !r.Config.Identify {
			for i, b := range r.Summary.Bands {
				v := bandVerdict(r.Config.Profile, b, warned)
				if i == 0 {
					v.BW2, v.Q = "–", "–"
					if r.Summary.BW2.Found {
						v.BW2 = reportFuncs["bw"].(func(rf.Bandwidth) string)(r.Summary.BW2)
					}
					if r.Summary.Q > 0 {
						v.Q = fmt.Sprintf("%.0f", r.Summary.Q)
					}
				}
				d.Verdicts = append(d.Verdicts, v)
			}
		}
		for _, w := range r.Summary.Warnings {
			w.Title = strings.SplitN(label, " · ", 2)[0] + ": " + w.Title
			d.Warnings = append(d.Warnings, w)
		}
		sec.SWR, sec.RL = reportPlots(r)
		sec.Smith = smithSVG(r, 52)
		sm := r.Summary
		sec.Key = []reportKV{
			{"Best SWR", swrF(sm.MinSWR) + " @ " + mhzF(sm.MinSWRFreq) + " MHz"},
			{"Return loss there", fmt.Sprintf("%.1f dB", sm.BestRL)},
			{"Mismatch loss there", fmt.Sprintf("%.2f dB", sm.MLAtMin)},
			{"Impedance there", reportFuncs["z"].(func(float64, float64) string)(sm.ZAtMinR, sm.ZAtMinX)},
			{"SWR < 2 bandwidth", reportFuncs["bw"].(func(rf.Bandwidth) string)(sm.BW2)},
			{"Q (estimated)", reportFuncs["q"].(func(float64) string)(sm.Q)},
			{"Sweep", fmt.Sprintf("%s, %d points%s", mhzRange(r.Config.Start, r.Config.Stop), len(r.Freq), avgNote(r.AvgN))},
			{"Calibration", sec.CalNote},
		}
		for i := 0; i < len(sec.Key); i += 2 {
			sec.KeyRows = append(sec.KeyRows, sec.Key[i:min(i+2, len(sec.Key))])
		}
		d.Sections = append(d.Sections, sec)
	}
	d.Profiles = strings.Join(profNames, ", ")
	if s.compare != nil && len(s.compare.Leaderboard) > 1 {
		c := *s.compare
		d.Compare = &c
	}
	if hs := s.hold; hs.h != nil && len(hs.h.Positions) > 0 {
		sum := hs.h.Summary()
		h := &reportHold{Label: labels[hs.profile], Positions: sum.Positions,
			Spread: fmt.Sprintf("%.1f MHz", sum.ResSpread/1e6), Range: mhzF(sum.ResMin) + "–" + mhzF(sum.ResMax) + " MHz", WorstBand: "–"}
		if h.Label == "" {
			h.Label = hs.label
		}
		if sum.WorstBand > 0 {
			h.WorstBand = fmt.Sprintf("%s (position #%d)", swrF(sum.WorstBand), sum.WorstBandID)
		}
		for _, p := range hs.h.Positions {
			h.Rows = append(h.Rows, HoldPosView{ID: p.ID, Time: p.Time, ResFreq: p.ResFreq, MinSWR: p.MinSWR, BandWorst: p.BandWrst})
		}
		d.Hold = h
	}
	s.mu.Unlock()

	if trimName != "" {
		if t, err := s.store.LoadTrim(trimName); err == nil && len(t.Entries) > 0 {
			t.predict()
			d.Trim = t
			d.TrimSpark = sparkSVG(t.Entries, t.Target)
		}
	}
	if len(d.Sections) == 0 && d.Trim == nil && d.Compare == nil && d.Hold == nil {
		return nil, errors.New("nothing to report yet: sweep at least one profile")
	}
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func avgNote(n int) string {
	if n > 1 {
		return fmt.Sprintf(", average of %d", n)
	}
	return ""
}

// reportPlots renders the SWR (full width) and return-loss plots.
func reportPlots(r *Result) (template.HTML, template.HTML) {
	maxSWR := 5.0
	if r.Config.Identify {
		maxSWR = 10
	}
	var marks []svgMark
	for _, dp := range r.Summary.Dips {
		marks = append(marks, svgMark{F: dp.Freq, Label: fmt.Sprintf("%.1f", dp.Freq/1e6)})
	}
	thr := []svgHLine{{Y: 2, Label: "SWR 2", Dash: "9 5"}, {Y: 1.5, Label: "SWR 1.5", Dash: "2 4"}}
	swr := svgPlot{Wmm: 182, Hmm: 60, Freq: r.Freq, Series: []svgSeries{{Y: r.Derived.SWR, Color: svgTrace}},
		YMin: 1, YMax: maxSWR, YTicks: niceTicks(1, maxSWR, 4), YFmt: func(v float64) string { return fmt.Sprintf("%g", v) },
		YUnit: "SWR", Bands: r.Config.Bands, HLines: thr, Marks: marks, Title: "SWR", Class: "swr"}.render()
	lo := -30.0
	for _, v := range r.Derived.S11dB {
		lo = math.Min(lo, v)
	}
	lo = math.Max(-50, math.Floor(lo/10)*10)
	rl := svgPlot{Wmm: 124, Hmm: 52, Freq: r.Freq, Series: []svgSeries{{Y: r.Derived.S11dB, Color: svgTrace}},
		YMin: lo, YMax: 0, YTicks: niceTicks(lo, 0, 4), YFmt: func(v float64) string { return fmt.Sprintf("%g", v) },
		YUnit: "dB", Bands: r.Config.Bands,
		HLines: []svgHLine{{Y: -9.54, Label: "SWR 2", Dash: "9 5"}, {Y: -13.98, Label: "SWR 1.5", Dash: "2 4"}},
		Title:  "Return loss (S11)", Class: "rl"}.render()
	return swr, rl
}

// ReportChoice lists what a report can include (for the REPORT dialog).
type ReportChoice struct {
	Profile string    `json:"profile"`
	Label   string    `json:"label"`
	Time    time.Time `json:"time"`
	Verdict string    `json:"verdict,omitempty"`
}

func (s *Server) reportChoicesLocked() []ReportChoice {
	out := []ReportChoice{}
	for _, p := range s.profiles {
		if r := s.lastByProfile[p.ID]; r != nil {
			c := ReportChoice{Profile: p.ID, Label: p.Label, Time: r.Time}
			if len(r.Summary.Bands) > 0 && !r.Config.Identify {
				c.Verdict = r.Summary.Bands[0].Verdict
			}
			out = append(out, c)
		}
	}
	if r := s.lastByProfile["custom"]; r != nil {
		out = append(out, ReportChoice{Profile: "custom", Label: "Custom " + mhzRange(r.Config.Start, r.Config.Stop), Time: r.Time})
	}
	return out
}

func (s *Server) hReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string   `json:"name"`
		Notes    string   `json:"notes"`
		Profiles []string `json:"profiles"`
		Trim     string   `json:"trim"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	file, err := ReportFileName(name, time.Now())
	if err != nil {
		writeErr(w, 400, errors.New("give the antenna a name (1-80 characters)"))
		return
	}
	if req.Profiles == nil {
		s.mu.Lock()
		for _, c := range s.reportChoicesLocked() {
			req.Profiles = append(req.Profiles, c.Profile)
		}
		s.mu.Unlock()
	}
	html, err := s.buildReport(strings.TrimSuffix(file, ".html"), name, req.Notes, req.Profiles, req.Trim)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	if err := s.store.SaveReportFile(file, html); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.publishState()
	writeJSON(w, 200, map[string]string{"file": file, "url": "api/reports/" + file})
}

func (s *Server) hReportList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.ListReports())
}

func (s *Server) hReportGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.ReportPath(r.PathValue("file"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	disp := "inline"
	if r.URL.Query().Get("download") != "" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disp, r.PathValue("file")))
	_, _ = w.Write(b)
}
