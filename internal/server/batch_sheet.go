package server

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- the one-page batch sheet ----------

const (
	svgTuned  = "#0a7d0a"
	svgOff    = "#b07a00"
	svgBad    = "#b3261e"
	svgLot    = "#6d8fbf" // one captured antenna in the overlay
	svgLotEnv = "#dfe6f1" // min/max envelope of the lot
)

type batchRow struct {
	ID                                     int
	Label, Status, SClass, Detail          string
	Res, Off, OffPct, SWRT, Worst, BW, RL  string
	Verdict, VClass, Cover, Cal            string
	CalOK, OutRes, OutSWR, OutWorst, OutBW bool
	OutRL, Outlier                         bool
}

type batchSheetData struct {
	ID        string
	V         BatchView
	Generated time.Time
	App, URL  string
	Device    string
	Simulated bool
	Band      string
	Target    string
	Tol       string
	Dates     string
	Overlay   template.HTML
	Spread    template.HTML
	Rows      []batchRow
	Halves    [][]batchRow // compact: the rows in two side-by-side tables
	Size      string       // "", "compact" (15-40 antennas: two narrower tables), "long" (over 40: the table continues on a second page)
	Legend    []string
}

func statusClass(st string) string {
	switch st {
	case "tuned":
		return "good"
	case "shorten", "lengthen":
		return "ok"
	}
	return "bad"
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func fmtStat(v float64, dec int) string {
	return fmt.Sprintf("%.*f", dec, v)
}

// batchRows turns the antennas into table rows.
func batchRows(v BatchView) []batchRow {
	var rows []batchRow
	for _, a := range v.Antennas {
		m := a.Metrics
		r := batchRow{ID: a.ID, Label: a.Label, Status: m.Tuning.Text, SClass: statusClass(m.Tuning.Status), Detail: m.Tuning.Detail,
			Res: "–", Off: "–", SWRT: swrF(m.SWRTarget), Worst: "–", BW: "–", RL: fmt.Sprintf("%.1f", m.RLTarget),
			Verdict: verdictLabel[m.Verdict], VClass: verdictClass(m.Verdict), Cover: fmt.Sprintf("%.0f %%", m.PctBelow2),
			Cal: a.Cal, CalOK: a.CalOK}
		if r.Verdict == "" {
			r.Verdict = "N/A"
		}
		if m.Found {
			r.Res, r.Off, r.OffPct = mhzF(m.ResFreq), signed2(m.Offset/1e6), signed2(m.OffsetPct)
		}
		if m.WorstSWR > 0 {
			r.Worst = swrF(m.WorstSWR)
		}
		if m.BW2 > 0 {
			r.BW = fmt.Sprintf("%.1f", m.BW2/1e6)
			if m.BW2Open {
				r.BW = "≥ " + r.BW
			}
		}
		r.OutRes, r.OutSWR, r.OutWorst = hasKey(m.Outliers, "res"), hasKey(m.Outliers, "swrTarget"), hasKey(m.Outliers, "worstSWR")
		r.OutBW, r.OutRL = hasKey(m.Outliers, "bw2"), hasKey(m.Outliers, "rlTarget")
		r.Outlier = len(m.Outliers) > 0
		rows = append(rows, r)
	}
	return rows
}

// overlaySVG draws every captured SWR curve, the lot's min/max envelope,
// the target and the band (outliers in the REF colour, dashed).
func overlaySVG(v BatchView, wmm, hmm float64) template.HTML {
	series := []svgSeries{}
	out := map[int]bool{}
	for _, a := range v.Antennas {
		if len(a.Metrics.Outliers) > 0 {
			out[a.ID] = true
		}
	}
	for _, a := range v.Antennas {
		if !out[a.ID] {
			series = append(series, svgSeries{Y: a.SWR, Color: svgLot, Width: 1.1})
		}
	}
	for _, a := range v.Antennas {
		if out[a.ID] {
			series = append(series, svgSeries{Y: a.SWR, Color: svgRef, Width: 1.6, Dash: "6 3"})
		}
	}
	var env *svgEnv
	if len(v.Antennas) > 1 {
		env = &svgEnv{Min: v.EnvMin, Max: v.EnvMax, Fill: svgLotEnv}
	}
	label15 := "SWR 1.5"
	if hmm < 40 {
		label15 = "" // no room between the two lines
	}
	title := fmt.Sprintf("SWR of the %d antennas", len(v.Antennas))
	if len(v.Antennas) == 1 {
		title = "SWR of the antenna"
	}
	return svgPlot{Wmm: wmm, Hmm: hmm, Freq: v.Freq, Series: series, Env: env, YMin: 1, YMax: 5, YTicks: niceTicks(1, 5, 4),
		YFmt: func(x float64) string { return fmt.Sprintf("%g", x) }, YUnit: "SWR", Bands: v.Bands,
		HLines: []svgHLine{{Y: 2, Label: "SWR 2", Dash: "9 5"}, {Y: 1.5, Label: label15, Dash: "2 4"}},
		VLines: []svgVLine{{F: v.Target, Label: "target " + mhzF(v.Target), Color: svgInk, Dash: "5 3"}},
		Title:  title, Class: "overlay"}.render()
}

// spreadSVG is the resonance strip: one mark per antenna on a frequency
// axis, with the band, the target and its tolerance window, the lot's mean
// and ±1σ. Marks: ● tuned, ▼ too long (shorten), ▲ too short
// (lengthen), ✕ bad or no resonance; a ring marks a statistical outlier.
func spreadSVG(v BatchView, wmm, hmm float64) template.HTML {
	W, H := wmm*4, hmm*4
	const l, r = 58.0, 14.0
	axis := H - 34
	tol := v.Summary.ToleranceHz
	band := rf.Band{}
	if len(v.Bands) > 0 {
		band = v.Bands[0]
	}
	lo, hi := math.Min(band.Lo, v.Target-tol), math.Max(band.Hi, v.Target+tol)
	for _, a := range v.Antennas {
		if a.Metrics.Found {
			lo, hi = math.Min(lo, a.Metrics.ResFreq), math.Max(hi, a.Metrics.ResFreq)
		}
	}
	pad := (hi - lo) * 0.06
	lo, hi = lo-pad, hi+pad
	fx := func(f float64) float64 { return l + (f-lo)/(hi-lo)*(W-l-r) }
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="plot spread" viewBox="0 0 %.0f %.0f" width="%.0fmm" height="%.0fmm" role="img" aria-label="Resonance of each antenna" font-family="Helvetica, Arial, sans-serif">`, W, H, wmm, hmm)
	sm := v.Summary
	title := fmt.Sprintf("Resonance of each antenna · target %s MHz ± %.2f", mhzF(v.Target), tol/1e6)
	fmt.Fprintf(&sb, `<text x="%.0f" y="18" font-size="%.0f" font-weight="700" fill="%s">%s</text>`, l, svgFont+1, svgInk, esc(title))
	top := 26.0
	// band, tolerance window, target
	if band.Valid() {
		a, z := fx(band.Lo), fx(band.Hi)
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, a, top, z-a, axis-top, svgBandFill)
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s">%s band</text>`, a+3, axis-4, svgFont-1, svgMuted, esc(band.Name))
	}
	a, z := fx(v.Target-tol), fx(v.Target+tol)
	fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="#d5ecd5" stroke="%s" stroke-width="1" stroke-dasharray="3 2"/>`, a, top, z-a, axis-top, svgTuned)
	fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.6" stroke-dasharray="5 3"/>`, fx(v.Target), fx(v.Target), top, axis, svgInk)
	// axis
	fmt.Fprintf(&sb, `<line x1="%.0f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`, l, W-r, axis, axis, svgFrame)
	for _, tk := range niceTicks(lo/1e6, hi/1e6, int((W-l-r)/80)) {
		x := fx(tk * 1e6)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/><text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="middle">%g</text>`,
			x, x, axis, axis+5, svgFrame, x, axis+17, svgFont, svgMuted, tk)
	}
	fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="end">MHz</text>`, W-r, H-4, svgFont, svgMuted)
	// mean ± 1σ (above the marks)
	if sm.N >= 2 && sm.SigmaHz > 0 {
		mean := v.Target + sm.BiasHz
		y := top + 8
		x1, x2 := fx(mean-sm.SigmaHz), fx(mean+sm.SigmaHz)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.6"/><line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.6"/><line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.6"/>`,
			x1, x2, y, y, svgInk, x1, x1, y-4, y+4, svgInk, x2, x2, y-4, y+4, svgInk)
		mx := fx(mean)
		fmt.Fprintf(&sb, `<path d="M%.1f %.1f l5 5 l-5 5 l-5 -5 z" fill="%s"/>`, mx, y-5, svgInk)
		lx, anchor := x2+5, "start"
		if x2 > W-r-150 {
			lx, anchor = x1-5, "end"
		}
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="%s">mean %s ± σ %.2f MHz</text>`, lx, y+4, svgFont-1, svgInk, anchor, mhzF(mean), sm.SigmaHz/1e6)
	}
	// marks, stacked so they do not overlap
	type mk struct {
		x     float64
		a     BatchAntennaView
		level int
	}
	var marks []mk
	for _, a := range v.Antennas {
		if a.Metrics.Found {
			marks = append(marks, mk{x: fx(a.Metrics.ResFreq), a: a})
		}
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].x < marks[j].x })
	const step = 11.0
	maxLevel := int(math.Max(1, math.Floor((axis-12-(top+18))/step)))
	for i := range marks {
		for lv := 0; ; lv++ {
			free := true
			for j := 0; j < i; j++ {
				if marks[j].level == lv && math.Abs(marks[j].x-marks[i].x) < 10 {
					free = false
				}
			}
			if free || lv >= maxLevel {
				marks[i].level = lv
				break
			}
		}
	}
	type box struct{ x0, x1, y float64 }
	var labels []box
	for _, m := range marks {
		x, y := m.x, axis-11-float64(m.level)*step
		switch m.a.Metrics.Tuning.Status {
		case "tuned":
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="4.4" fill="%s"/>`, x, y, svgTuned)
		case "shorten":
			fmt.Fprintf(&sb, `<path d="M%.1f %.1f h10 l-5 9 z" fill="%s"/>`, x-5, y-4.5, svgOff)
		case "lengthen":
			fmt.Fprintf(&sb, `<path d="M%.1f %.1f h10 l-5 -9 z" fill="%s"/>`, x-5, y+4.5, svgOff)
		default:
			fmt.Fprintf(&sb, `<path d="M%.1f %.1f l9 9 m0 -9 l-9 9" stroke="%s" stroke-width="2.4"/>`, x-4.5, y-4.5, svgBad)
		}
		if len(m.a.Metrics.Outliers) > 0 {
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="7.6" fill="none" stroke="%s" stroke-width="1.3"/>`, x, y, svgInk)
			// The label goes right of the ring, moved up when it would cover another one.
			lw := float64(len([]rune(m.a.Label)))*6.5 + 2
			ly := y + 4
			for tries := 0; tries < 4; tries++ {
				clash := false
				for _, b := range labels {
					clash = clash || (math.Abs(b.y-ly) < 10 && x+9 < b.x1 && x+9+lw > b.x0)
				}
				if !clash {
					break
				}
				ly -= 11
			}
			labels = append(labels, box{x + 9, x + 9 + lw, ly})
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" font-weight="700">%s</text>`, x+9, ly, svgFont-2, svgInk, esc(m.a.Label))
		}
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// buildBatchSheet renders the batch sheet.
func (s *Server) buildBatchSheet(id string, b *Batch) ([]byte, error) {
	s.mu.Lock()
	c := cloneBatch(b)
	d := batchSheetData{ID: id, Generated: time.Now(), App: s.opt.AppName, URL: s.opt.HomeURL}
	if s.info != nil {
		d.Device, d.Simulated = s.info.Model+" · fw "+s.info.Version, s.info.Mock
	}
	s.mu.Unlock()
	if len(c.Antennas) == 0 {
		return nil, errors.New("capture at least one antenna first")
	}
	if d.App == "" {
		d.App = "EasyVNA Web"
	}
	if d.URL == "" {
		d.URL = "https://github.com/pix/easyvna-web"
	}
	d.V = batchView(c, s.batchSlope(c))
	sm := d.V.Summary
	band := c.Band()
	d.Band = fmt.Sprintf("%s MHz band, %s–%s MHz", band.Name, mhzF(band.Lo), mhzF(band.Hi))
	d.Target, d.Tol = mhzF(c.Target), fmt.Sprintf("%.2f", c.ToleranceHz()/1e6)
	d.Dates = sm.First.Format("2006-01-02 15:04")
	if sm.Last.Sub(sm.First) >= time.Minute {
		if sm.Last.Format("2006-01-02") == sm.First.Format("2006-01-02") {
			d.Dates += "–" + sm.Last.Format("15:04")
		} else {
			d.Dates += " – " + sm.Last.Format("2006-01-02 15:04")
		}
	}
	d.Rows = batchRows(d.V)
	switch n := len(d.Rows); {
	case n > 40:
		d.Size = "long"
	case n > 14:
		d.Size = "compact"
		h := (n + 1) / 2
		d.Halves = [][]batchRow{d.Rows[:h], d.Rows[h:]}
	}
	h := 44.0
	if d.Size != "" {
		h = 36
	}
	d.Overlay = overlaySVG(d.V, 190, h)
	d.Spread = spreadSVG(d.V, 190, 25)
	d.Legend = []string{"— each antenna", "shaded: min / max of the lot", "- - target"}
	for _, a := range d.V.Antennas {
		if len(a.Metrics.Outliers) > 0 {
			d.Legend = append(d.Legend, "orange dashed: statistical outlier")
			break
		}
	}
	var buf bytes.Buffer
	if err := batchSheetTmpl.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hBatchSheet builds the batch sheet and saves it with the reports.
func (s *Server) hBatchSheet(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	file, err := ReportFileName(b.Name+"-batch", time.Now())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	html, err := s.buildBatchSheet(strings.TrimSuffix(file, ".html"), b)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	if err := s.store.SaveReportFile(file, html); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"file": file, "url": "api/reports/" + file})
}

// batchStatsHTML is shared by the batch sheet and the report.
const batchStatsHTML = `{{define "bstats"}}<table class="bst"><thead><tr><th>Statistic</th><th class="num">n</th><th class="num">mean</th><th class="num">SD</th><th class="num">min</th><th class="num">max</th><th class="num">spread</th><th>outliers</th></tr></thead><tbody>
{{range .Summary.Stats}}<tr><td>{{.Label}}{{if .Unit}} <span class="muted">({{.Unit}})</span>{{end}}</td><td class="num">{{.N}}</td>{{if .N}}<td class="num">{{stat .Mean .Dec}}</td><td class="num">{{if gt .N 1}}{{stat .SD (inc .Dec)}}{{else}}–{{end}}</td><td class="num">{{stat .Min .Dec}}</td><td class="num">{{stat .Max .Dec}}</td><td class="num">{{stat .Spread .Dec}}</td>{{else}}<td class="num">–</td><td class="num">–</td><td class="num">–</td><td class="num">–</td><td class="num">–</td>{{end}}<td>{{range $i, $id := .Outliers}}{{if $i}}, {{end}}<b>{{label $ $id}}</b>{{else}}–{{end}}</td></tr>{{end}}
</tbody></table>{{end}}`

const batchSheetHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.V.Name}}: batch sheet</title>
<style>
@page { margin: 9mm 10mm; }
:root { color-scheme: light; --ink: #1a1a1a; --ink2: #444; --muted: #6b6b6b; --line: #d4d4d0; --soft: #f4f4f1;
  --pass: #0a7d0a; --ok: #b07a00; --fail: #b3261e; --na: #8a8a85; }
` + paperCSS + `
body { font: 8.3pt/1.28 Helvetica, Arial, "Segoe UI", sans-serif; }
.paper { padding: 9mm 10mm; }
.sheet { display: flex; flex-direction: column; gap: 2mm; }
.sh { display: grid; grid-template-columns: 1fr auto; gap: 4mm; border-bottom: .6mm solid var(--ink); padding-bottom: 1.8mm; }
.kicker { font-size: 7.5pt; letter-spacing: .14em; text-transform: uppercase; color: var(--muted); font-weight: 700; }
h1 { font-size: 18pt; line-height: 1.05; margin: .5mm 0 1mm; overflow-wrap: anywhere; }
.meta { display: grid; grid-template-columns: auto 1fr auto 1fr; gap: .3mm 3mm; font-size: 7.4pt; }
.meta dt { color: var(--muted); } .meta dd { margin: 0; }
.calbad { color: var(--fail); font-weight: 700; } .calok { color: var(--pass); }
.sim { display: inline-block; font-size: 7pt; font-weight: 700; letter-spacing: .08em; padding: .2mm 1.5mm; border: .3mm solid var(--ok); color: var(--ok); border-radius: 1mm; margin-left: 2mm; vertical-align: middle; }
.tally { border: .3mm solid var(--line); border-left: 2mm solid var(--na); border-radius: 1.5mm; padding: 1.6mm 2.4mm; min-width: 36mm; display: flex; flex-direction: column; justify-content: center; }
.tally.good { border-left-color: var(--pass); } .tally.ok { border-left-color: var(--ok); } .tally.bad { border-left-color: var(--fail); }
.tally b { font-size: 20pt; font-weight: 800; line-height: 1; font-variant-numeric: tabular-nums; }
.tally span { font-size: 7.2pt; color: var(--muted); }
.tally .yield { color: var(--ink2); margin-top: .8mm; }
.concl { border: .3mm solid var(--line); background: var(--soft); border-radius: 1.2mm; padding: 1.4mm 2.4mm; font-size: 8pt; }
.concl p { margin: 0 0 .5mm; } .concl p:last-child { margin: 0; } .concl p:first-child { font-weight: 700; }
.concl .tl { font-weight: 700; }
svg.plot { display: block; width: 100%; height: auto; }
.legend { font-size: 6.8pt; color: var(--ink2); display: flex; gap: 4mm; flex-wrap: wrap; }
.legend .t { color: var(--pass); } .legend .o { color: var(--ok); } .legend .x { color: var(--fail); }
h3 { font-size: 7.6pt; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 0 0 .6mm; }
table { width: 100%; border-collapse: collapse; font-size: 7.2pt; font-variant-numeric: tabular-nums; }
th, td { text-align: left; padding: .35mm .9mm; border-bottom: .2mm solid var(--line); vertical-align: top; white-space: nowrap; }
th { font-size: 6.3pt; color: var(--muted); text-transform: uppercase; }
td.num, th.num { text-align: right; }
.muted { color: var(--muted); }
td.st { font-weight: 700; } td.st.good { color: var(--pass); } td.st.ok { color: var(--ok); } td.st.bad { color: var(--fail); }
td.v.good { color: var(--pass); } td.v.ok { color: var(--ok); } td.v.bad { color: var(--fail); font-weight: 700; }
td.out { font-weight: 800; box-shadow: inset 0 0 0 .3mm var(--ink); }
td.out::before { content: "◆ "; font-size: 6pt; }
tr.outl td:first-child { font-weight: 800; }
td.lbl { max-width: 30mm; overflow: hidden; text-overflow: ellipsis; }
.halves { display: grid; grid-template-columns: 1fr 1fr; gap: 3mm; align-items: start; }
.compact table.ants td { padding: .15mm .7mm; font-size: 6.6pt; line-height: 1.2; }
.compact table.ants th { padding: .2mm .7mm; }
.compact td.lbl { max-width: 18mm; }
.compact .concl { font-size: 7.6pt; padding: 1mm 2.2mm; } .compact table.bst td { padding: .2mm .9mm; }
.long table.ants { break-inside: auto; page-break-inside: auto; }
.long table.ants tr { break-inside: avoid; page-break-inside: avoid; }
.notes { white-space: pre-wrap; font-size: 7.6pt; max-height: 16mm; overflow: hidden; border: .3mm solid var(--line); background: var(--soft); border-radius: 1mm; padding: 1mm 2mm; }
.tnote { font-size: 6.6pt; color: var(--muted); margin: .5mm 0 0; white-space: normal; }
.sfoot { border-top: .3mm solid var(--line); padding-top: 1mm; font-size: 7pt; color: var(--muted); display: flex; justify-content: space-between; gap: 4mm; }
@media print {
  html { background: #fff; }
  .paper { width: auto; margin: 0; padding: 0; box-shadow: none; }
  .toolbar, .noprint { display: none !important; }
  .sheet:not(.long) { break-inside: avoid; page-break-inside: avoid; }
}
@media (max-width: 700px) {
  .paper { padding: 4mm; } .sh { grid-template-columns: 1fr; } .meta { grid-template-columns: auto 1fr; }
  .tables { overflow-x: auto; }
}
</style>
` + paperScript + `
<script>if (location.hash === "#print") addEventListener("load", function () { setTimeout(function () { print(); }, 250); });</script>
</head><body>
` + paperToolbar + `
<div class="paper"><div class="sheet {{.Size}}">

<header class="sh">
  <div>
    <div class="kicker">Batch sheet{{if .Simulated}}<span class="sim">SIMULATED DATA</span>{{end}}</div>
    <h1>{{.V.Name}}</h1>
    <dl class="meta">
      <dt>Profile</dt><dd>{{.V.Label}} · {{.Band}}</dd>
      <dt>Antennas</dt><dd><b>{{len .V.Antennas}}</b> · {{.Dates}}</dd>
      <dt>Target</dt><dd>{{.Target}} MHz, tuned within ± {{.Tol}} MHz</dd>
      <dt>Sheet</dt><dd>{{.ID}}</dd>
      <dt>Trim advice</dt><dd style="grid-column: span 3">{{.V.Summary.Slope}}</dd>
      <dt>Calibration</dt><dd style="grid-column: span 3" class="{{if .V.Summary.CalOK}}calok{{else}}calbad{{end}}">{{.V.Summary.CalNote}}{{if .Device}} · {{.Device}}{{end}}</dd>
    </dl>
  </div>
  <div class="tally {{if eq .V.Summary.Tuned (len .V.Antennas)}}good{{else if .V.Summary.Bad}}bad{{else}}ok{{end}}">
    <b>{{.V.Summary.Tuned}} / {{len .V.Antennas}}</b><span>tuned (± {{.Tol}} MHz)</span>
    <span class="yield">PASS {{index .V.Summary.Yield "good"}} · OK {{index .V.Summary.Yield "ok"}} · FAIL {{index .V.Summary.Yield "bad"}}</span>
  </div>
</header>

<section class="concl" aria-label="Conclusion">{{range .V.Summary.Conclusion}}<p>{{.}}</p>{{end}}<p class="tl">{{.V.Summary.TuningLine}}.</p></section>

<div>{{.Overlay}}</div>
<div>{{.Spread}}</div>
<div class="legend">{{range .Legend}}<span>{{.}}</span>{{end}}<span class="t">● tuned</span><span class="o">▼ shorten · ▲ lengthen</span><span class="x">✕ bad</span><span>◯ statistical outlier</span></div>

<div class="tables">
<h3>Lot statistics</h3>
{{template "bstats" .V}}
</div>

<div class="tables">
<h3>Antennas</h3>
{{if .Halves}}<div class="halves">{{range .Halves}}<table class="ants"><thead><tr><th>#</th><th>Label</th><th>Tuning</th><th class="num">Res.</th><th class="num">Offset</th><th class="num">SWR@tgt</th><th class="num">Worst</th><th>Band</th></tr></thead><tbody>
{{range .}}<tr{{if .Outlier}} class="outl"{{end}}><td>{{.ID}}</td><td class="lbl">{{.Label}}{{if not .CalOK}} <span class="calbad">†</span>{{end}}</td><td class="st {{.SClass}}">{{.Status}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Res}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Off}}</td><td class="num{{if .OutSWR}} out{{end}}">{{.SWRT}}</td><td class="num{{if .OutWorst}} out{{end}}">{{.Worst}}</td><td class="v {{.VClass}}">{{.Verdict}}</td></tr>
{{end}}</tbody></table>{{end}}</div>
<p class="tnote">Compact table ({{len .Rows}} antennas): bandwidth, return loss and band coverage are in the CSV export · † = not calibrated for this range · ◆ = statistical outlier for that figure (|x − median| &gt; 3.5 × 1.4826 × MAD), not the same as off target.</p>
{{else}}<table class="ants"><thead><tr><th>#</th><th>Label</th><th>Tuning</th><th class="num">Resonance</th><th class="num">Offset</th><th class="num">%</th><th class="num">SWR@tgt</th><th class="num">Worst</th><th class="num">BW&lt;2</th><th class="num">RL</th><th>Band</th><th>Calibration</th></tr></thead><tbody>
{{range .Rows}}<tr{{if .Outlier}} class="outl"{{end}}><td>{{.ID}}</td><td class="lbl">{{.Label}}</td><td class="st {{.SClass}}">{{.Status}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Res}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Off}}</td><td class="num">{{.OffPct}}</td><td class="num{{if .OutSWR}} out{{end}}">{{.SWRT}}</td><td class="num{{if .OutWorst}} out{{end}}">{{.Worst}}</td><td class="num{{if .OutBW}} out{{end}}">{{.BW}}</td><td class="num{{if .OutRL}} out{{end}}">{{.RL}}</td><td class="v {{.VClass}}">{{.Verdict}} · {{.Cover}}</td><td class="{{if not .CalOK}}calbad{{end}}">{{.Cal}}</td></tr>
{{end}}</tbody></table>
<p class="tnote">MHz · resonance = return-loss peak nearest the target (as TUNE) · BW&lt;2 = SWR &lt; 2 bandwidth of that dip, MHz · RL = return loss at the target, dB · Band = PASS (worst in-band SWR &lt; 1.5) / OK (&lt; 2) / FAIL, and the share of the band with SWR &lt; 2 · ◆ = statistical outlier for that figure (|x − median| &gt; 3.5 × 1.4826 × MAD, from 3 antennas on), not the same as off target.</p>{{end}}
</div>

{{if .V.Notes}}<div><h3>Notes</h3><div class="notes">{{.V.Notes}}</div></div>{{end}}

<footer class="sfoot"><span>{{.App}} · by PiX (and the Claude, OpenAI and Deepseek AI band)</span><span>{{.URL}}</span></footer>
</div></div>
</body></html>`

// batchFuncs are the template helpers of the batch sheet and its report
// section.
var batchFuncs = template.FuncMap{
	"stat": fmtStat,
	"label": func(v BatchView, id int) string {
		for _, a := range v.Antennas {
			if a.ID == id {
				return a.Label
			}
		}
		return fmt.Sprintf("#%d", id)
	},
}

var batchSheetTmpl = template.Must(template.Must(template.New("batch").Funcs(reportFuncs).Funcs(batchFuncs).Parse(batchStatsHTML)).Parse(batchSheetHTML))
