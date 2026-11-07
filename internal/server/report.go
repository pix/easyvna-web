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

// ---------- SVG helpers (printable, no JavaScript) ----------

type svgSeries struct {
	Y     []float64
	Color string
	Dash  bool
}

type svgPlot struct {
	W, H       float64
	Freq       []float64
	Series     []svgSeries
	YMin, YMax float64
	YTicks     []float64
	YFmt       func(float64) string
	Bands      []rf.Band
	HLines     []float64
	Marks      []float64 // vertical marker lines (Hz)
	Title      string
}

func niceTicks(lo, hi float64, n int) []float64 {
	span := hi - lo
	if span <= 0 || n < 1 {
		return []float64{lo}
	}
	raw := span / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := 10 * mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw {
			step = m * mag
			break
		}
	}
	var out []float64
	for v := math.Ceil(lo/step) * step; v <= hi+step*1e-9; v += step {
		out = append(out, math.Round(v/step)*step)
	}
	return out
}

func (p svgPlot) render() template.HTML {
	const l, r, t, b = 44.0, 10.0, 22.0, 26.0
	x0, x1, y0, y1 := l, p.W-r, t, p.H-b
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg viewBox="0 0 %.0f %.0f" class="plot" role="img" aria-label="%s">`, p.W, p.H, template.HTMLEscapeString(p.Title))
	fmt.Fprintf(&sb, `<text x="%.0f" y="14" class="ttl">%s</text>`, x0, template.HTMLEscapeString(p.Title))
	if len(p.Freq) < 2 {
		sb.WriteString(`</svg>`)
		return template.HTML(sb.String())
	}
	fmin, fmax := p.Freq[0], p.Freq[len(p.Freq)-1]
	fx := func(f float64) float64 { return x0 + (f-fmin)/(fmax-fmin)*(x1-x0) }
	fy := func(v float64) float64 {
		v = math.Max(p.YMin, math.Min(p.YMax, v))
		return y1 - (v-p.YMin)/(p.YMax-p.YMin)*(y1-y0)
	}
	for _, bd := range p.Bands {
		if bd.Hi < fmin || bd.Lo > fmax {
			continue
		}
		a, z := fx(math.Max(bd.Lo, fmin)), fx(math.Min(bd.Hi, fmax))
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="band"/>`, a, y0, math.Max(1.5, z-a), y1-y0)
	}
	for _, tk := range p.YTicks {
		y := fy(tk)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="grid"/><text x="%.1f" y="%.1f" class="yl">%s</text>`,
			x0, x1, y, y, x0-4, y+3, p.YFmt(tk))
	}
	for _, tk := range niceTicks(fmin/1e6, fmax/1e6, int((x1-x0)/70)) {
		x := fx(tk * 1e6)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="grid"/><text x="%.1f" y="%.1f" class="xl">%g</text>`,
			x, x, y0, y1, x, y1+14, tk)
	}
	fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" class="xl" text-anchor="end">MHz</text>`, x1, p.H-2)
	for _, h := range p.HLines {
		y := fy(h)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="thr"/>`, x0, x1, y, y)
	}
	for _, m := range p.Marks {
		if m >= fmin && m <= fmax {
			fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="mark"/>`, fx(m), fx(m), y0, y1)
		}
	}
	for _, s := range p.Series {
		var pts strings.Builder
		for i, f := range p.Freq {
			if i < len(s.Y) {
				fmt.Fprintf(&pts, "%.1f,%.1f ", fx(f), fy(s.Y[i]))
			}
		}
		dash := ""
		if s.Dash {
			dash = ` stroke-dasharray="5 4"`
		}
		fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.8"%s/>`, pts.String(), s.Color, dash)
	}
	fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="frame"/></svg>`, x0, y0, x1-x0, y1-y0)
	return template.HTML(sb.String())
}

func smithSVG(res *Result, size float64) template.HTML {
	c, R := size/2, size/2-14
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg viewBox="0 0 %.0f %.0f" class="smith" role="img" aria-label="Smith chart">`, size, size)
	fmt.Fprintf(&sb, `<defs><clipPath id="uc"><circle cx="%.1f" cy="%.1f" r="%.1f"/></clipPath></defs><g clip-path="url(#uc)">`, c, c, R)
	for _, r := range []float64{0.2, 0.5, 1, 2, 5} {
		fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" class="grid" fill="none"/>`, c+R*r/(1+r), c, R/(1+r))
	}
	for _, x := range []float64{0.2, 0.5, 1, 2, 5} {
		for _, sg := range []float64{1, -1} {
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" class="grid" fill="none"/>`, c+R, c-sg*R/x, R/x)
		}
	}
	fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="grid"/>`, c-R, c+R, c, c)
	fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" class="thr" fill="none"/></g>`, c, c, R/3)
	fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" class="frame" fill="none"/>`, c, c, R)
	var pts strings.Builder
	for _, g := range res.S11 {
		fmt.Fprintf(&pts, "%.1f,%.1f ", c+g[0]*R, c-g[1]*R)
	}
	fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="#2a78d6" stroke-width="1.6"/>`, pts.String())
	if len(res.Config.Bands) > 0 {
		b := res.Config.Bands[0]
		var bp strings.Builder
		for i, f := range res.Freq {
			if f >= b.Lo && f <= b.Hi {
				fmt.Fprintf(&bp, "%.1f,%.1f ", c+res.S11[i][0]*R, c-res.S11[i][1]*R)
			}
		}
		if bp.Len() > 0 {
			fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="#2a78d6" stroke-width="4" stroke-linecap="round"/>`, bp.String())
		}
	}
	fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" class="xl">50</text></svg>`, c, c+12)
	return template.HTML(sb.String())
}

func sparkSVG(entries []TrimEntry, target float64) template.HTML {
	if len(entries) == 0 {
		return ""
	}
	const w, h = 260.0, 70.0
	lo, hi := target, target
	for _, e := range entries {
		lo, hi = math.Min(lo, e.FreqHz), math.Max(hi, e.FreqHz)
	}
	pad := math.Max((hi-lo)*0.15, 1e6)
	lo, hi = lo-pad, hi+pad
	fx := func(i int) float64 {
		if len(entries) == 1 {
			return w / 2
		}
		return 8 + float64(i)/float64(len(entries)-1)*(w-16)
	}
	fy := func(f float64) float64 { return h - 6 - (f-lo)/(hi-lo)*(h-12) }
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg viewBox="0 0 %.0f %.0f" class="spark"><line x1="0" x2="%.0f" y1="%.1f" y2="%.1f" class="thr"/>`, w, h, w, fy(target), fy(target))
	var pts strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&pts, "%.1f,%.1f ", fx(i), fy(e.FreqHz))
	}
	fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="#2a78d6" stroke-width="2"/>`, pts.String())
	for i, e := range entries {
		fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="3" fill="#2a78d6"/>`, fx(i), fy(e.FreqHz))
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// ---------- report model ----------

type reportBandRow struct {
	Profile string
	Band    rf.BandSummary
	Ref     float64
}

type reportSection struct {
	Profile string
	Label   string
	Res     *Result
	SWR     template.HTML
	RL      template.HTML
	Smith   template.HTML
	Bands   []rf.BandSummary
}

type reportData struct {
	Name, Notes  string
	Generated    time.Time
	Device       string
	Cal          string
	ReadOnly     bool
	Rows         []reportBandRow
	Sections     []reportSection
	Trim         *TrimSession
	TrimSpark    template.HTML
	Compare      *CompareSession
	Warnings     []rf.Warning
	HasAnySweeps bool
}

func verdictClass(v string) string {
	switch v {
	case "good", "ok", "bad":
		return v
	}
	return "na"
}

var reportFuncs = template.FuncMap{
	"mhz":  func(f float64) string { return fmt.Sprintf("%.2f", f/1e6) },
	"mhz3": func(f float64) string { return fmt.Sprintf("%.3f", f/1e6) },
	"f1":   func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"f2":   func(v float64) string { return fmt.Sprintf("%.2f", v) },
	"sgn1": func(v float64) string { return fmt.Sprintf("%+.1f", v) },
	"swr": func(v float64) string {
		if v >= 99 {
			return ">99"
		}
		return fmt.Sprintf("%.2f", v)
	},
	"vclass": verdictClass,
	"upper":  strings.ToUpper,
	"deref": func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"time": func(t time.Time) string { return t.Format("2006-01-02 15:04") },
	"neg":  func(v float64) float64 { return -v },
	"offmhz": func(p *float64) string {
		if p == nil {
			return "–"
		}
		return fmt.Sprintf("%+.1f", *p/1e6)
	},
	"inc": func(i int) int { return i + 1 },
	"z": func(r, x float64) string {
		sign := "+"
		if x < 0 {
			sign = "−"
		}
		return fmt.Sprintf("%.1f %s j%.1f Ω", r, sign, math.Abs(x))
	},
}

var reportTmpl = template.Must(template.New("report").Funcs(reportFuncs).Parse(reportHTML))

// buildReport assembles the report for the chosen profiles.
func (s *Server) buildReport(name, notes string, profiles []string, trimName string) ([]byte, error) {
	s.mu.Lock()
	d := reportData{Name: name, Notes: notes, Generated: time.Now(), ReadOnly: s.opt.ReadOnly}
	if s.info != nil {
		d.Device = fmt.Sprintf("%s, firmware %s", s.info.Model, s.info.Version)
		if s.info.Fork != "" {
			d.Device += ", fork " + s.info.Fork
		}
		if s.info.Mock {
			d.Device += " (SIMULATED)"
		}
	}
	d.Cal = s.cal.Status.Raw
	var picked []*Result
	order := map[string]int{}
	for i, p := range s.profiles {
		order[p.ID] = i
	}
	for _, id := range profiles {
		if r := s.lastByProfile[id]; r != nil {
			picked = append(picked, r)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return order[picked[i].Config.Profile] < order[picked[j].Config.Profile] })
	labels := map[string]string{}
	for _, p := range s.profiles {
		if p.Identify {
			labels[p.ID] = p.Label + " sweep (" + p.Sub + ")"
		} else {
			labels[p.ID] = p.Label + " MHz profile (" + p.Sub + ")"
		}
	}
	if s.compare != nil && len(s.compare.Leaderboard) > 1 {
		c := *s.compare
		d.Compare = &c
	}
	s.mu.Unlock()

	for _, r := range picked {
		sec := reportSection{Profile: r.Config.Profile, Label: labels[r.Config.Profile], Res: r, Bands: r.Summary.Bands}
		if sec.Label == "" {
			sec.Label = r.Config.Profile + " (" + mhzRange(r.Config.Start, r.Config.Stop) + ")"
		}
		maxSWR := 5.0
		if r.Config.Identify {
			maxSWR = 10
		}
		var marks []float64
		for _, dp := range r.Summary.Dips {
			marks = append(marks, dp.Freq)
		}
		sec.SWR = svgPlot{W: 520, H: 230, Freq: r.Freq, Series: []svgSeries{{Y: r.Derived.SWR, Color: "#2a78d6"}},
			YMin: 1, YMax: maxSWR, YTicks: niceTicks(1, maxSWR, 5), YFmt: func(v float64) string { return fmt.Sprintf("%g", v) },
			Bands: r.Config.Bands, HLines: []float64{1.5, 2}, Marks: marks, Title: "SWR"}.render()
		lo := -30.0
		for _, v := range r.Derived.S11dB {
			lo = math.Min(lo, v)
		}
		lo = math.Max(-60, math.Floor(lo/10)*10)
		sec.RL = svgPlot{W: 520, H: 200, Freq: r.Freq, Series: []svgSeries{{Y: r.Derived.S11dB, Color: "#2a78d6"}},
			YMin: lo, YMax: 0, YTicks: niceTicks(lo, 0, 5), YFmt: func(v float64) string { return fmt.Sprintf("%g", v) },
			Bands: r.Config.Bands, HLines: []float64{-9.54, -13.98}, Title: "Return loss (S11 dB)"}.render()
		sec.Smith = smithSVG(r, 230)
		d.Sections = append(d.Sections, sec)
		if !r.Config.Identify {
			for _, b := range r.Summary.Bands {
				d.Rows = append(d.Rows, reportBandRow{Profile: r.Config.Profile, Band: b, Ref: b.Band.Reference()})
			}
		}
		for _, w := range r.Summary.Warnings {
			w.Title = sec.Label + ": " + w.Title
			d.Warnings = append(d.Warnings, w)
		}
	}
	d.HasAnySweeps = len(d.Sections) > 0
	if trimName != "" {
		if t, err := s.store.LoadTrim(trimName); err == nil {
			t.predict()
			d.Trim = t
			d.TrimSpark = sparkSVG(t.Entries, t.Target)
		}
	}
	if !d.HasAnySweeps && d.Trim == nil && d.Compare == nil {
		return nil, errors.New("nothing to report yet: sweep at least one profile")
	}
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
	if _, err := SnapshotFile(name); err != nil {
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
	html, err := s.buildReport(name, req.Notes, req.Profiles, req.Trim)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	file, err := s.store.SaveReport(name, html)
	if err != nil {
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

const reportHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}: antenna report</title>
<style>
:root{--ink:#0b0b0b;--ink2:#52514e;--muted:#6f6d68;--grid:#e1e0d9;--border:#d6d5cf;--good:#0ca30c;--warn:#fab219;--bad:#d03b3b}
*{box-sizing:border-box}body{margin:0;background:#fff;color:var(--ink);font:13px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:920px;margin:0 auto;padding:24px 20px}
h1{font-size:24px;margin:0 0 2px}h2{font-size:16px;margin:22px 0 8px;border-bottom:1px solid var(--border);padding-bottom:4px}h3{font-size:14px;margin:12px 0 6px}
.meta{color:var(--ink2)}.muted{color:var(--muted)}
table{border-collapse:collapse;width:100%;font-variant-numeric:tabular-nums}th,td{text-align:left;padding:5px 7px;border-bottom:1px solid var(--border);vertical-align:top}th{font-size:11.5px;color:var(--muted)}
.badge{display:inline-block;font-weight:700;font-size:11px;letter-spacing:.05em;padding:1px 8px;border-radius:999px;color:#fff;-webkit-print-color-adjust:exact;print-color-adjust:exact}
.badge.good{background:var(--good)}.badge.ok{background:var(--warn);color:#0b0b0b}.badge.bad{background:var(--bad)}.badge.na{background:#c3c2b7;color:#0b0b0b}
.plots{display:grid;grid-template-columns:1fr 240px;gap:10px;align-items:start}.plots .col{display:flex;flex-direction:column;gap:8px}
svg{width:100%;height:auto}svg .grid{stroke:var(--grid);stroke-width:1}svg .thr{stroke:#898781;stroke-dasharray:4 4;fill:none}svg .mark{stroke:#898781;stroke-width:1}
svg .frame{fill:none;stroke:#c3c2b7}svg .band{fill:rgba(42,120,214,.13);-webkit-print-color-adjust:exact;print-color-adjust:exact}
svg text{font:10px system-ui,sans-serif;fill:#6f6d68}svg .yl{text-anchor:end}svg .xl{text-anchor:middle}svg .ttl{font-weight:600;fill:#0b0b0b;font-size:11px}
.spark{width:260px;height:70px}.warn{border:1px solid var(--bad);border-radius:6px;padding:6px 10px;margin:6px 0}
section{break-inside:avoid;page-break-inside:avoid}
.noprint{margin:0 0 14px}.noprint a,.noprint button{font:inherit;padding:6px 12px;border:1px solid var(--border);border-radius:6px;background:#fff;color:inherit;text-decoration:none;cursor:pointer}
@media print{.noprint{display:none}main{padding:0}@page{size:A4;margin:14mm}}
@media (max-width:640px){.plots{grid-template-columns:1fr}}
</style></head><body><main>
<div class="noprint"><button onclick="print()">Print / save as PDF</button> <a href="?download=1">Download HTML</a></div>
<h1>{{.Name}}</h1>
<div class="meta">Antenna report, {{time .Generated}}{{if .Device}} · {{.Device}}{{end}}</div>
{{if .Cal}}<div class="muted">Firmware calibration flags: {{.Cal}}</div>{{end}}
{{if .Notes}}<p>{{.Notes}}</p>{{end}}
{{range .Warnings}}<div class="warn"><b>{{.Title}}</b>: {{.Text}}</div>{{end}}

{{if .Rows}}<section><h2>Verdict per band</h2>
<table><thead><tr><th>Band</th><th>Verdict</th><th>Worst SWR</th><th>Best SWR</th><th>Worst RL</th><th>Mismatch loss (worst)</th><th>Resonance</th><th>Offset</th></tr></thead><tbody>
{{range .Rows}}<tr><td><b>{{.Band.Band.Name}}</b> <span class="muted">{{mhz .Band.Band.Lo}}–{{mhz .Band.Band.Hi}} MHz</span></td>
<td><span class="badge {{vclass .Band.Verdict}}">{{upper .Band.Verdict}}</span></td>
<td>{{swr .Band.WorstSWR}} @ {{mhz .Band.WorstSWRFreq}}</td><td>{{swr .Band.BestSWR}}</td><td>{{f1 .Band.WorstRL}} dB</td><td>{{f2 .Band.WorstML}} dB</td>
<td>{{if .Band.DipFreq}}{{mhz (deref .Band.DipFreq)}} MHz{{else}}–{{end}}</td>
<td>{{if .Band.DipOffset}}{{offmhz .Band.DipOffset}} MHz from {{mhz .Ref}}{{else}}–{{end}}</td></tr>{{end}}
</tbody></table></section>{{end}}

{{range .Sections}}<section><h2>{{.Label}} <span class="muted" style="font-weight:400">· swept {{time .Res.Time}}{{if gt .Res.AvgN 1}} · average of {{.Res.AvgN}}{{end}}</span></h2>
<div class="plots"><div class="col">{{.SWR}}{{.RL}}</div><div class="col">{{.Smith}}
<table><tbody>
<tr><td>Best SWR</td><td><b>{{swr .Res.Summary.MinSWR}}</b> @ {{mhz .Res.Summary.MinSWRFreq}} MHz</td></tr>
<tr><td>Return loss</td><td>{{f1 .Res.Summary.BestRL}} dB</td></tr>
<tr><td>Mismatch loss</td><td>{{f2 .Res.Summary.MLAtMin}} dB</td></tr>
<tr><td>Z at best</td><td>{{z .Res.Summary.ZAtMinR .Res.Summary.ZAtMinX}}</td></tr>
<tr><td>BW SWR&lt;2</td><td>{{if .Res.Summary.BW2.Found}}{{mhz .Res.Summary.BW2.Width}} MHz{{if or .Res.Summary.BW2.LoOpen .Res.Summary.BW2.HiOpen}} (at least){{end}}{{else}}–{{end}}</td></tr>
<tr><td>Q (est.)</td><td>{{if .Res.Summary.Q}}{{f1 .Res.Summary.Q}}{{else}}–{{end}}</td></tr>
</tbody></table></div></div>
{{if .Res.Summary.Dips}}<h3>Resonances</h3><table><thead><tr><th>#</th><th>MHz</th><th>SWR</th><th>RL</th><th>ML</th><th>Z</th><th>BW&lt;2</th><th>Q</th><th>Note</th></tr></thead><tbody>
{{range $i, $d := .Res.Summary.Dips}}<tr><td>{{inc $i}}</td><td><b>{{mhz $d.Freq}}</b></td><td>{{swr $d.SWR}}</td><td>{{f1 $d.RL}} dB</td><td>{{f2 $d.ML}} dB</td><td>{{z $d.R $d.X}}</td>
<td>{{if $d.BW2.Found}}{{mhz $d.BW2.Width}} MHz{{else}}–{{end}}</td><td>{{if $d.Q}}{{f1 $d.Q}}{{else}}–{{end}}</td><td>{{$d.Advice}}</td></tr>{{end}}
</tbody></table>{{end}}
</section>{{end}}

{{with .Trim}}<section><h2>Trim log: {{.Name}}</h2>
<p class="meta">Target {{mhz3 .Target}} MHz{{with .Prediction}} · next: {{if gt .CutMm 0.0}}cut <b>{{f1 .CutMm}} mm</b>{{else}}lengthen <b>{{f1 (neg .CutMm)}} mm</b>{{end}} → {{f1 .NewLengthMm}} mm ({{.Method}}, {{f2 .MHzPerMm}} MHz/mm){{end}}</p>
{{$.TrimSpark}}
<table><thead><tr><th>#</th><th>Time</th><th>Length</th><th>Cut</th><th>Resonance</th><th>SWR at res.</th><th>SWR at target</th></tr></thead><tbody>
{{range $i, $e := .Entries}}<tr><td>{{inc $i}}</td><td>{{time $e.Time}}</td><td>{{f1 $e.LengthMm}} mm</td><td>{{if $e.CutMm}}{{f1 $e.CutMm}} mm{{else}}–{{end}}</td><td>{{mhz3 $e.FreqHz}} MHz</td><td>{{swr $e.SWRAtRes}}</td><td>{{swr $e.SWRAtTarget}}</td></tr>{{end}}
</tbody></table></section>{{end}}

{{with .Compare}}<section><h2>S21 compare (relative to {{.AName}})</h2>
<p class="meta">Fixed reference antenna on port 1, antenna under test on port 2, {{.Band.Name}} band {{mhz .Band.Lo}}–{{mhz .Band.Hi}} MHz, profile {{.Profile}}. Relative numbers only (no THRU calibration needed); same placement and cable for every antenna.</p>
<table><thead><tr><th>#</th><th>Antenna</th><th>Δ in band (avg)</th><th>Worst</th><th>Best</th><th>Measured</th></tr></thead><tbody>
{{range $i, $e := .Leaderboard}}<tr><td>{{inc $i}}</td><td><b>{{$e.Name}}</b></td><td>{{sgn1 $e.MeanDB}} dB</td><td>{{sgn1 $e.WorstDB}} dB</td><td>{{sgn1 $e.BestDB}} dB</td><td>{{time $e.Time}}</td></tr>{{end}}
</tbody></table></section>{{end}}
<p class="muted">Generated by EasyVNA Web. SWR grades: GOOD &lt; 1.5, OK &lt; 2, BAD otherwise (worst point in band, band edges interpolated). Q is estimated from the SWR&lt;2 bandwidth assuming a match at resonance.</p>
</main></body></html>`
