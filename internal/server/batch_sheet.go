package server

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- the batch sheet: two pages ----------
//
// Page 1 has the antenna sheet's layout (header with photo and link, key
// figures, SWR, return loss, impedance and Smith plots, details, notes)
// for the whole lot: every antenna's curve on each plot, in its own
// colour and dash pattern, and the lot average (the vector average of
// S11) in bold. Page 2 is the lot statistics: the conclusion, the
// resonance strip, every antenna and the statistics table.

const svgAvg = "#111" // the lot average, bold

// lotColors × lotDashes give each antenna its own line style (30 before a
// style repeats). Outliers are drawn in the REF / outlier style instead.
var (
	lotColors = []string{"#1f6fb4", "#2a9d3a", "#8e44ad", "#d4801a", "#0f8c8c", "#c2185b", "#6d6d00", "#5d4037", "#3949ab", "#7a7a7a"}
	lotDashes = []string{"", "7 3", "2 2.5"}
)

type batchRow struct {
	ID                                     int
	Label, Status, SClass, Detail          string
	Res, Off, OffPct, SWRT, Worst, BW, RL  string
	Verdict, VClass, Cover, Cal            string
	CalOK, OutRes, OutSWR, OutWorst, OutBW bool
	OutRL, Outlier                         bool
}

// lotStyle is the line style of one antenna on page 1.
type lotStyle struct {
	ID      int
	Label   string
	Color   string
	Dash    string
	Outlier bool
	Swatch  template.HTML
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
	Aim       string
	Limit     string
	Good      string
	Dates     string
	Spread    template.HTML
	QR        template.HTML
	Rows      []batchRow
	Halves    [][]batchRow // compact: the rows in two side-by-side tables
	Size      string       // "", "compact" (11-40 antennas: two narrower tables), "long" (over 40: the table continues on a further page)
	// Page 1: the lot on the antenna sheet's layout.
	LotSWR, LotRL  template.HTML
	LotZ, LotSmith template.HTML
	LotLegend      []lotStyle
	AvgSwatch      template.HTML
	Figs           []sheetFig
	Grade          string // FINE / WORTH CORRECTING / MUST CORRECT
	GClass         string
	Details        []reportKV
}

func statusClass(st string) string {
	switch st {
	case "tuned", "ok":
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
			Verdict: verdictLabel[m.Verdict], VClass: verdictClass(m.Verdict), Cover: fmt.Sprintf("%.0f %%", m.PctBelowLimit),
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

// lotStyles gives every antenna its line style.
func lotStyles(v BatchView) []lotStyle {
	var out []lotStyle
	k := 0
	for _, a := range v.Antennas {
		st := lotStyle{ID: a.ID, Label: a.Label, Outlier: len(a.Metrics.Outliers) > 0}
		if st.Outlier {
			st.Color, st.Dash = svgRef, "6 3"
		} else {
			st.Color, st.Dash = lotColors[k%len(lotColors)], lotDashes[(k/len(lotColors))%len(lotDashes)]
			k++
		}
		st.Swatch = swatch(st.Color, st.Dash, 1.6)
		out = append(out, st)
	}
	return out
}

// swatch is a short line sample for the legend.
func swatch(color, dash string, w float64) template.HTML {
	d := ""
	if dash != "" {
		d = ` stroke-dasharray="` + dash + `"`
	}
	return template.HTML(fmt.Sprintf(`<svg class="sw" viewBox="0 0 24 6" width="6mm" height="1.5mm" aria-hidden="true"><line x1="0" x2="24" y1="3" y2="3" stroke="%s" stroke-width="%.1f"%s/></svg>`, color, w*1.4, d))
}

// lotDerived is every antenna's derived data and the lot average's.
func lotDerived(b *Batch) ([]rf.Derived, rf.Derived, [][2]float64) {
	var all [][]complex128
	var ds []rf.Derived
	for _, a := range b.Antennas {
		g := toComplex(a.S11)
		ds = append(ds, rf.Derive(g, nil))
		if len(g) == len(b.Freq) {
			all = append(all, g)
		}
	}
	if len(all) == 0 {
		return ds, rf.Derived{}, nil
	}
	avg := rf.Average(all)
	return ds, rf.Derive(avg, nil), s11Of(avg)
}

// lotPlots renders page 1's plots: every antenna and the lot average.
func lotPlots(d *batchSheetData, b *Batch, swrH float64) {
	v := d.V
	styles := lotStyles(v)
	ds, avg, avgS11 := lotDerived(b)
	acc := v.Accept
	attr := func(id int) string { return fmt.Sprintf(` data-ant="%d"`, id) }
	var swr, rl []svgSeries
	for i, st := range styles {
		if i >= len(ds) || len(ds[i].SWR) != len(v.Freq) {
			continue
		}
		w := 1.3
		if st.Outlier {
			w = 1.6
		}
		swr = append(swr, svgSeries{Y: ds[i].SWR, Color: st.Color, Dash: st.Dash, Width: w, Attr: attr(st.ID)})
		rl = append(rl, svgSeries{Y: ds[i].S11dB, Color: st.Color, Dash: st.Dash, Width: w, Attr: attr(st.ID)})
	}
	if len(avg.SWR) == len(v.Freq) {
		swr = append(swr, svgSeries{Y: avg.SWR, Color: svgAvg, Width: 3.4, Attr: ` data-avg="1"`})
		rl = append(rl, svgSeries{Y: avg.S11dB, Color: svgAvg, Width: 3.4, Attr: ` data-avg="1"`})
	}
	title := fmt.Sprintf("SWR of the %d antennas and the lot average", len(v.Antennas))
	hl := limitLines(acc)
	if swrH <= 40 {
		hl[1].Label = "" // no room for two labels between the lines
	}
	d.LotSWR = svgPlot{Wmm: 190, Hmm: swrH, Freq: v.Freq, Series: swr, YMin: 1, YMax: 5, YTicks: niceTicks(1, 5, 4),
		YFmt: func(x float64) string { return fmt.Sprintf("%g", x) }, YUnit: "SWR", Bands: v.Bands, HLines: hl,
		VLines: []svgVLine{{F: v.Target, Label: "target " + mhzF(v.Target), Color: svgInk, Dash: "5 3"}},
		Title:  title, Class: "overlay lotswr"}.render()
	lo := -30.0
	for _, x := range avg.S11dB {
		lo = math.Min(lo, x)
	}
	for _, dd := range ds {
		for _, x := range dd.S11dB {
			lo = math.Min(lo, x)
		}
	}
	lo = math.Max(-50, math.Floor(lo/10)*10)
	d.LotRL = svgPlot{Wmm: 136, Hmm: 33, Freq: v.Freq, Series: rl, YMin: lo, YMax: 0, YTicks: niceTicks(lo, 0, 3),
		YFmt: func(x float64) string { return fmt.Sprintf("%g", x) }, YUnit: "dB", Bands: v.Bands,
		HLines: []svgHLine{{Y: -rlOfSWR(acc.Limit()), Label: "SWR " + fmtG(acc.Limit()), Dash: "9 5"}},
		Title:  "Return loss (S11)", Class: "rl lotrl"}.render()
	// Impedance: R in the antenna's style, X lighter; the average bold
	// (R solid, X dashed).
	zlo, zhi := 0.0, 100.0
	for _, dd := range append(ds, avg) {
		for _, a := range [][]float64{dd.R, dd.X} {
			for _, x := range a {
				zlo, zhi = math.Min(zlo, x), math.Max(zhi, x)
			}
		}
	}
	zlo, zhi = math.Max(zlo, -600), math.Min(zhi, 600)
	ticks := niceTicks(zlo, zhi, 4)
	if len(ticks) > 0 {
		zlo, zhi = math.Min(zlo, ticks[0]), math.Max(zhi, ticks[len(ticks)-1])
	}
	clip := func(a []float64) []float64 {
		out := make([]float64, len(a))
		for i, x := range a {
			out[i] = math.Max(zlo, math.Min(zhi, x))
		}
		return out
	}
	var z []svgSeries
	for i, st := range styles {
		if i >= len(ds) || len(ds[i].R) != len(v.Freq) {
			continue
		}
		z = append(z, svgSeries{Y: clip(ds[i].R), Color: st.Color, Dash: st.Dash, Width: 1.2, Attr: attr(st.ID)},
			svgSeries{Y: clip(ds[i].X), Color: st.Color, Dash: st.Dash, Width: 1.1, Op: 0.45, Attr: attr(st.ID)})
	}
	if len(avg.R) == len(v.Freq) {
		z = append(z, svgSeries{Y: clip(avg.R), Color: svgAvg, Width: 3, Attr: ` data-avg="1"`},
			svgSeries{Y: clip(avg.X), Color: svgAvg, Width: 3, Dash: "7 4", Attr: ` data-avg="1"`})
	}
	d.LotZ = svgPlot{Wmm: 136, Hmm: 33, Freq: v.Freq, Series: z, YMin: zlo, YMax: zhi, YTicks: ticks,
		YFmt: func(x float64) string { return fmt.Sprintf("%g", x) }, YUnit: "Ω", Bands: v.Bands,
		HLines: []svgHLine{{Y: 50, Label: "50 Ω", Dash: "2 4"}},
		Title:  "Impedance: R strong, X light (Ω); lot average R solid, X dashed", Class: "z lotz"}.render()
	// Smith chart.
	var sb strings.Builder
	_, c, cy, R := smithFrame(&sb, 52, "smith lotsmith", "lotuc")
	for i, st := range styles {
		if i >= len(b.Antennas) {
			continue
		}
		var pts strings.Builder
		for _, g := range b.Antennas[i].S11 {
			fmt.Fprintf(&pts, "%.1f,%.1f ", c+g[0]*R, cy-g[1]*R)
		}
		dash := ""
		if st.Dash != "" {
			dash = ` stroke-dasharray="` + st.Dash + `"`
		}
		fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.3"%s%s/>`, pts.String(), st.Color, dash, attr(st.ID))
	}
	if avgS11 != nil {
		var pts strings.Builder
		for _, g := range avgS11 {
			fmt.Fprintf(&pts, "%.1f,%.1f ", c+g[0]*R, cy-g[1]*R)
		}
		fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="3" data-avg="1"/>`, pts.String(), svgAvg)
	}
	sb.WriteString(`</svg>`)
	d.LotSmith = template.HTML(sb.String())
	d.LotLegend = styles
	d.AvgSwatch = swatch(svgAvg, "", 3)
}

// lotFigures are page 1's key figures: lot values, n-aware.
func lotFigures(v BatchView) []sheetFig {
	sm := v.Summary
	limit := v.Accept.Limit()
	stat := func(key string) rf.Stat {
		for _, r := range sm.Stats {
			if r.Key == key {
				return r.Stat
			}
		}
		return rf.Stat{}
	}
	var figs []sheetFig
	res := stat("res")
	switch n := sm.NRes; {
	case n == 0:
		figs = append(figs, sheetFig{K: "Resonance (mean)", V: "–", Sub: "no resonance found"})
	case n < 2:
		figs = append(figs, sheetFig{K: "Resonance", V: mhzF(v.Target + sm.BiasHz), Sub: "MHz, one antenna"})
	case n < firmN:
		figs = append(figs, sheetFig{K: "Resonance (mean)", V: mhzF(v.Target + sm.BiasHz), Sub: fmt.Sprintf("± %.2f MHz 95 %% CI, n = %d", sm.BiasCIHz/1e6, n)})
	default:
		figs = append(figs, sheetFig{K: "Resonance (mean)", V: mhzF(v.Target + sm.BiasHz), Sub: fmt.Sprintf("σ %.2f MHz, n = %d", sm.SigmaHz/1e6, n)})
	}
	if sm.NRes > 0 {
		figs = append(figs, sheetFig{K: "Offset from target", V: signed2(sm.BiasHz / 1e6), Sub: fmt.Sprintf("MHz (%s %%)", signed2(sm.BiasPct))})
	} else {
		figs = append(figs, sheetFig{K: "Offset from target", V: "–"})
	}
	if res.N >= 2 {
		figs = append(figs, sheetFig{K: "Resonance range", V: fmt.Sprintf("%.2f", res.Spread), Sub: fmt.Sprintf("MHz, %s–%s", fmtStat(res.Min, 1), fmtStat(res.Max, 1))})
	} else {
		figs = append(figs, sheetFig{K: "Resonance range", V: "–", Sub: "needs 2 antennas"})
	}
	if sm.WorstSWR > 0 {
		figs = append(figs, sheetFig{K: "Worst SWR in band", V: swrF(sm.WorstSWR), Sub: "worst antenna", Class: swrClassAt(sm.WorstSWR, limit)})
	} else {
		figs = append(figs, sheetFig{K: "Worst SWR in band", V: "–"})
	}
	if st := stat("swrTarget"); st.N > 0 {
		figs = append(figs, sheetFig{K: "SWR at target", V: swrF(st.Min) + "–" + swrF(st.Max), Sub: "min–max @ " + mhzF(v.Target) + " MHz", Class: swrClassAt(st.Max, limit)})
	}
	covered := sm.Yield["good"] + sm.Yield["ok"]
	cls := "good"
	if covered < sm.N {
		cls = "bad"
	}
	figs = append(figs, sheetFig{K: fmt.Sprintf("Band at SWR < %s", fmtG(limit)), V: fmt.Sprintf("%d / %d", covered, sm.N), Sub: "antennas covering it", Class: cls})
	if sm.MarginMax > 0 {
		c := "good"
		if sm.MarginMax > worthMarginUsed {
			c = "ok"
		}
		if sm.MarginMax > 100 {
			c = "bad"
		}
		figs = append(figs, sheetFig{K: "Margin used", V: fmt.Sprintf("%.0f %%", math.Min(sm.MarginMax, 999)), Sub: "worst antenna", Class: c})
	} else {
		figs = append(figs, sheetFig{K: "Margin used", V: "–"})
	}
	figs = append(figs, sheetFig{K: "On the TUNE aim", V: fmt.Sprintf("%d / %d", sm.Tuned, sm.N), Sub: fmt.Sprintf("± %.2f MHz", sm.AimHz/1e6)})
	if st := stat("rlTarget"); st.N > 0 {
		figs = append(figs, sheetFig{K: "Return loss at target", V: fmt.Sprintf("%.1f", st.Mean), Sub: "dB, mean"})
	}
	if st := stat("bw2"); st.N > 0 {
		figs = append(figs, sheetFig{K: "SWR < 2 bandwidth", V: fmt.Sprintf("%.1f", st.Mean), Sub: "MHz, mean"})
	}
	if sm.NRes >= 3 {
		figs = append(figs, sheetFig{K: "Spread σ", V: fmt.Sprintf("%.2f", sm.SigmaHz/1e6), Sub: "MHz, unit to unit"})
	} else {
		figs = append(figs, sheetFig{K: "Spread σ", V: "–", Sub: "needs 3 antennas"})
	}
	if sm.NRepeat > 0 {
		figs = append(figs, sheetFig{K: "Repeatability σ", V: fmt.Sprintf("%.2f", sm.RepeatHz/1e6), Sub: fmt.Sprintf("MHz, %d measured ×N", sm.NRepeat)})
	}
	if len(figs) > 12 {
		figs = figs[:12]
	}
	return figs
}

var gradeLabel = map[string][2]string{GradeFine: {"FINE", "good"}, GradeWorth: {"WORTH CORRECTING", "ok"}, GradeMust: {"MUST CORRECT", "bad"}}

// buildBatchSheet renders the batch sheet (two pages).
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
	d.V = s.batchViewOf(c)
	sm := d.V.Summary
	band := c.Band()
	d.Band = fmt.Sprintf("%s MHz band, %s–%s MHz", band.Name, mhzF(band.Lo), mhzF(band.Hi))
	d.Target, d.Tol, d.Aim = mhzF(c.Target), fmt.Sprintf("%.2f", sm.ToleranceHz/1e6), fmt.Sprintf("%.2f", sm.AimHz/1e6)
	d.Limit, d.Good = fmtG(d.V.Accept.Limit()), fmtG(d.V.Accept.GoodLimit())
	d.Dates = sm.First.Format("2006-01-02 15:04")
	if sm.Last.Sub(sm.First) >= time.Minute {
		if sm.Last.Format("2006-01-02") == sm.First.Format("2006-01-02") {
			d.Dates += "–" + sm.Last.Format("15:04")
		} else {
			d.Dates += " – " + sm.Last.Format("2006-01-02 15:04")
		}
	}
	d.QR = qrSVG(c.Link, 22)
	d.Rows = batchRows(d.V)
	switch n := len(d.Rows); {
	case n > 40:
		d.Size = "long"
	case n > 10:
		d.Size = "compact"
		h := (n + 1) / 2
		d.Halves = [][]batchRow{d.Rows[:h], d.Rows[h:]}
	}
	d.Spread = spreadSVG(d.V, 190, 0)
	// Page 1: the plots shrink when the header's small print or the legend
	// take more room, so the page stays one page (see smallPrintMm).
	legendRows := math.Ceil(float64(len(c.Antennas)+1) / 8)
	swrH := 50 - finePrintMm(c.PhotoCaption, c.LinkNote) - math.Max(0, legendRows-3)*3.4
	lotPlots(&d, c, math.Max(30, swrH))
	d.Figs = lotFigures(d.V)
	if g, ok := gradeLabel[sm.Grade]; ok {
		d.Grade, d.GClass = g[0], g[1]
	} else {
		d.Grade, d.GClass = "N/A", "na"
	}
	d.Details = []reportKV{{"Target", fmt.Sprintf("%s MHz (TUNE aim ± %s MHz)", d.Target, d.Aim)}}
	if c.LengthMm > 0 {
		d.Details = append(d.Details, reportKV{"Element length", fmt.Sprintf("%g mm", c.LengthMm)})
	}
	d.Details = append(d.Details, reportKV{"Trim advice", sm.Slope}, reportKV{"Measured", d.Dates})
	if sm.NRepeat > 0 {
		d.Details = append(d.Details, reportKV{"Measure ×N", fmt.Sprintf("%d of %d antennas; repeatability σ %.2f MHz", sm.NRepeat, sm.N, sm.RepeatHz/1e6)})
	}
	if st := d.V.Setup; st != nil {
		d.Details = append(d.Details, reportKV{"Setup check", st.Time.Format("2006-01-02 15:04") + ": " + st.Line})
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
:root { color-scheme: light; --ink: #1a1a1a; --ink2: #444; --muted: #666; --line: #d4d4d0; --soft: #f4f4f1;
  --pass: #0a7d0a; --ok: #a35f00; --fail: #b3261e; --na: #8a8a85; }
` + paperCSS + `
body { font: 8.3pt/1.28 Helvetica, Arial, "Segoe UI", sans-serif; }
.paper { padding: 9mm 10mm; }
.paper + .paper { margin-top: 0; }
.sheet { display: flex; flex-direction: column; gap: 2mm; }
.sh { display: grid; grid-template-columns: 1fr auto; gap: 3mm; border-bottom: .6mm solid var(--ink); padding-bottom: 1.8mm; }
.kicker { font-size: 7.5pt; letter-spacing: .14em; text-transform: uppercase; color: var(--muted); font-weight: 700; }
h1 { font-size: 18pt; line-height: 1.05; margin: .5mm 0 1mm; overflow-wrap: anywhere; }
.p2 h1 { font-size: 13pt; margin: .3mm 0 .6mm; }
.what { font-size: 9.5pt; color: var(--ink2); margin-bottom: 1mm; }
.meta { display: grid; grid-template-columns: auto 1fr auto 1fr; gap: .3mm 3mm; font-size: 7.4pt; }
.meta dt { color: var(--muted); } .meta dd { margin: 0; }
.calbad { color: var(--fail); font-weight: 700; } .calok { color: var(--pass); }
.sim { display: inline-block; font-size: 7pt; font-weight: 700; letter-spacing: .08em; padding: .2mm 1.5mm; border: .3mm solid var(--ok); color: var(--ok); border-radius: 1mm; margin-left: 2mm; vertical-align: middle; }
.side { display: flex; flex-direction: column; gap: .8mm; }
.siderow { display: flex; gap: 2.5mm; align-items: flex-start; }
.fineprint { margin: 1.5mm 0 .8mm; display: flex; flex-direction: column; gap: .3mm; }
.pbox { width: 36mm; display: flex; flex-direction: column; gap: .5mm; }
.photo { width: 36mm; height: 31mm; border: .3mm solid var(--line); border-radius: 1mm; overflow: hidden; display: flex; align-items: center; justify-content: center; background: var(--soft); }
.photo img { max-width: 100%; max-height: 100%; display: block; }
.qrbox { display: flex; flex-direction: column; align-items: center; gap: .6mm; width: 24mm; }
.qrbox svg { display: block; }
.smallprint { font-size: 6.2pt; line-height: 1.25; color: var(--muted); overflow-wrap: anywhere; }
a.link { font-size: 6.2pt; line-height: 1.2; color: var(--ink2); text-decoration: none; overflow-wrap: anywhere; text-align: center; }
@media screen { a.link { color: #1d4f91; text-decoration: underline; } }
/* page 1: key figures */
.kf { display: grid; grid-template-columns: 31mm 1fr; gap: 2.5mm; align-items: stretch; }
.verdict { border: .3mm solid var(--line); border-left: 2mm solid var(--na); border-radius: 1.5mm; padding: 1.6mm 2mm; display: flex; flex-direction: column; justify-content: center; }
.verdict.good { border-left-color: var(--pass); } .verdict.ok { border-left-color: var(--ok); } .verdict.bad { border-left-color: var(--fail); }
.verdict b { font-size: 13pt; font-weight: 800; letter-spacing: .02em; line-height: 1.05; overflow-wrap: anywhere; }
.verdict b.long { font-size: 10pt; }
.verdict.good b { color: var(--pass); } .verdict.ok b { color: var(--ok); } .verdict.bad b { color: var(--fail); } .verdict.na b { color: var(--na); }
.verdict span { font-size: 6.8pt; color: var(--muted); margin-top: .8mm; }
.figs { display: grid; grid-template-columns: repeat(6, 1fr); gap: 1.2mm 2.5mm; }
.fig { border-top: .3mm solid var(--line); padding-top: .6mm; min-width: 0; }
.fig i { display: block; font-style: normal; font-size: 6.8pt; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fig b { display: block; font-size: 12pt; font-variant-numeric: tabular-nums; line-height: 1.15; white-space: nowrap; }
.fig b.long { font-size: 9pt; }
.fig small { display: block; font-size: 6.5pt; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fig.good b { color: var(--pass); } .fig.ok b { color: var(--ok); } .fig.bad b { color: var(--fail); }
.crit { font-size: 7pt; color: var(--ink2); display: flex; flex-direction: column; gap: .3mm; }
.crit b { color: var(--ink); }
.prow { display: grid; grid-template-columns: 136fr 52fr; gap: 2mm; align-items: center; }
.prow .stack { display: flex; flex-direction: column; gap: 1mm; }
.lotlegend { font-size: 6.6pt; color: var(--ink2); display: flex; flex-wrap: wrap; gap: .4mm 3mm; }
.lotlegend .lg { display: inline-flex; align-items: center; gap: 1mm; max-width: 22mm; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.lotlegend .lg.avg { font-weight: 700; max-width: none; }
.lotlegend .lg.note { max-width: none; color: var(--muted); }
.lotlegend .lg.outl { font-weight: 700; }
.lotlegend svg.sw { flex: none; }
.cols { display: grid; grid-template-columns: 1fr 1fr; gap: 4mm; align-items: start; }
.kv td:first-child { color: var(--muted); width: 30%; }
.kv td { white-space: normal; }
/* page 2 */
.sumrow { display: grid; grid-template-columns: auto 1fr; gap: 2.5mm; align-items: stretch; }
.tally { border: .3mm solid var(--line); border-left: 2mm solid var(--na); border-radius: 1.5mm; padding: 1.6mm 2.4mm; min-width: 36mm; max-width: 44mm; display: flex; flex-direction: column; justify-content: center; }
.tally.good { border-left-color: var(--pass); } .tally.ok { border-left-color: var(--ok); } .tally.bad { border-left-color: var(--fail); }
.tally b { font-size: 20pt; font-weight: 800; line-height: 1; font-variant-numeric: tabular-nums; }
.tally span { font-size: 7pt; color: var(--muted); }
.tally .yield { color: var(--ink2); margin-top: .8mm; }
.concl { border: .3mm solid var(--line); background: var(--soft); border-radius: 1.2mm; padding: 1.4mm 2.4mm; font-size: 7.8pt; }
.concl p { margin: 0 0 .5mm; } .concl p:last-child { margin: 0; } .concl p.head { font-weight: 700; }
.concl .tl { font-weight: 700; }
svg.plot { display: block; width: 100%; height: auto; }
h3 { font-size: 7.4pt; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 0 0 .6mm; }
table { width: 100%; border-collapse: collapse; font-size: 7.2pt; font-variant-numeric: tabular-nums; }
th, td { text-align: left; padding: .35mm .9mm; border-bottom: .2mm solid var(--line); vertical-align: top; white-space: nowrap; }
th { font-size: 6.5pt; color: var(--muted); text-transform: uppercase; }
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
.compact .concl { font-size: 7.4pt; padding: 1mm 2.2mm; } .compact table.bst td { padding: .2mm .9mm; }
.long table.ants { break-inside: auto; page-break-inside: auto; }
.long table.ants tr { break-inside: avoid; page-break-inside: avoid; }
.notes { white-space: pre-wrap; font-size: 7.4pt; max-height: 20mm; overflow: hidden; border: .3mm solid var(--line); background: var(--soft); border-radius: 1mm; padding: 1mm 2mm; }
.p1 .notes { max-height: 18mm; } .p2 .notes { max-height: 14mm; }
.tnote { font-size: 6.5pt; color: var(--muted); margin: .5mm 0 0; white-space: normal; }
.sfoot { border-top: .3mm solid var(--line); padding-top: 1mm; font-size: 7pt; color: var(--muted); display: flex; justify-content: space-between; gap: 4mm; }
.pgbreak { height: 0; break-after: page; page-break-after: always; }
@media print {
  html { background: #fff; }
  .paper { width: auto; margin: 0; padding: 0; box-shadow: none; }
  .toolbar, .noprint { display: none !important; }
  .sheet:not(.long) { break-inside: avoid; page-break-inside: avoid; }
}
@media screen and (max-width: 700px) {
  .paper { padding: 4mm; } .sh { grid-template-columns: 1fr; } .meta { grid-template-columns: auto 1fr; }
  .figs { grid-template-columns: repeat(3, 1fr); } .prow, .cols, .kf { grid-template-columns: 1fr; }
  .tables { overflow-x: auto; }
}
</style>
` + paperScript + `
<script>if (location.hash === "#print") addEventListener("load", function () { setTimeout(function () { print(); }, 250); });</script>
</head><body>
` + paperToolbar + `
<div class="paper page p1"><div class="sheet">

<header class="sh">
  <div>
    <div class="kicker">Batch sheet · the lot{{if .Simulated}}<span class="sim">SIMULATED DATA</span>{{end}}</div>
    <h1>{{.V.Name}}</h1>
    <div class="what">{{len .V.Antennas}} antennas · {{.V.Label}} · {{.Band}}</div>
    <dl class="meta">
      <dt>Measured</dt><dd>{{.Dates}}</dd>
      <dt>Device</dt><dd>{{if .Device}}{{.Device}}{{else}}–{{end}}</dd>
      <dt>Sheet</dt><dd>{{.ID}}</dd>
      <dt>Sweep</dt><dd>{{mhzRange .V.Start .V.Stop}} · {{.V.Points}} points</dd>
      <dt>Calibration</dt><dd style="grid-column: span 3" class="{{if .V.Summary.CalOK}}calok{{else}}calbad{{end}}">{{.V.Summary.CalNote}}</dd>
    </dl>
  </div>
  {{if or .V.Photo .QR}}<div class="side"><div class="siderow">{{if .V.Photo}}<div class="pbox"><div class="photo"><img src="{{.V.Photo | photo}}" alt="Photo of {{.V.Name}}"></div></div>{{end}}{{if .QR}}<div class="qrbox">{{.QR}}<a class="link" href="{{.V.Link}}" target="_blank" rel="noopener noreferrer">{{.V.LinkText}}</a></div>{{end}}</div></div>{{end}}
</header>

<section class="kf" aria-label="Key figures of the lot">
  <div class="verdict {{.GClass}}"><b{{if gt (len .Grade) 6}} class="long"{{end}}>{{.Grade}}</b><span>PASS {{index .V.Summary.Yield "good"}} · OK {{index .V.Summary.Yield "ok"}} · FAIL {{index .V.Summary.Yield "bad"}}</span><span>PASS = worst in-band SWR &lt; {{.Good}}, OK &lt; {{.Limit}}</span></div>
  <div class="figs">{{range .Figs}}<div class="fig {{.Class}}"><i>{{.K}}</i><b{{if gt (len .V) 9}} class="long"{{end}}>{{.V}}</b>{{if .Sub}}<small>{{.Sub}}</small>{{end}}</div>{{end}}</div>
</section>
<div class="crit"><span class="criteria">{{.V.Summary.Criteria}}</span><span class="headline"><b>Lot:</b> {{.V.Summary.Headline}}</span></div>

<div>{{.LotSWR}}</div>
<div class="prow"><div class="stack"><div>{{.LotRL}}</div><div>{{.LotZ}}</div></div><div>{{.LotSmith}}</div></div>
<div class="lotlegend"><span class="lg avg">{{.AvgSwatch}}lot average (bold)</span>{{range .LotLegend}}<span class="lg{{if .Outlier}} outl{{end}}" data-ant="{{.ID}}">{{.Swatch}}{{.Label}}{{if .Outlier}} ◆{{end}}</span>{{end}}<span class="lg note">{{with .V.Bands}}shaded: {{index . 0 | bandName}} MHz band · {{end}}◆ = statistical outlier (orange, dashed)</span></div>

<div class="cols">
  <div><h3>Details</h3><table class="kv"><tbody>{{range .Details}}<tr><td>{{.K}}</td><td>{{.V}}</td></tr>{{end}}</tbody></table></div>
  <div><h3>Notes</h3>{{if .V.Notes}}<div class="notes">{{.V.Notes}}</div>{{else}}<p class="muted">–</p>{{end}}</div>
</div>

{{if or (and .V.Photo .V.PhotoCaption) (and .QR .V.LinkNote)}}<div class="fineprint">{{if and .V.Photo .V.PhotoCaption}}<div class="smallprint photocaption">{{.V.PhotoCaption | smallprint}}</div>{{end}}{{if and .QR .V.LinkNote}}<div class="smallprint linknote">{{.V.LinkNote | smallprint}}</div>{{end}}</div>{{end}}
<footer class="sfoot"><span>{{.App}} · by PiX (and the Claude, OpenAI and Deepseek AI band)</span><span>page 1 of 2 · {{.URL}}</span></footer>
</div></div>

<div class="pgbreak" aria-hidden="true"></div>

<div class="paper page p2"><div class="sheet {{.Size}}">

<header class="sh">
  <div>
    <div class="kicker">Batch sheet · lot statistics{{if .Simulated}}<span class="sim">SIMULATED DATA</span>{{end}}</div>
    <h1>{{.V.Name}}</h1>
    <dl class="meta">
      <dt>Profile</dt><dd>{{.V.Label}} · {{.Band}}</dd>
      <dt>Antennas</dt><dd><b>{{len .V.Antennas}}</b> · {{.Dates}}</dd>
      <dt>Target</dt><dd>{{.Target}} MHz · TUNE aim ± {{.Aim}} MHz</dd>
      <dt>Sheet</dt><dd>{{.ID}}</dd>
      <dt>Acceptance</dt><dd style="grid-column: span 3" class="criteria">{{.V.Summary.Window.Text}}</dd>
    </dl>
  </div>
</header>

<div class="sumrow">
  <div class="tally {{if eq .V.Summary.OK (len .V.Antennas)}}good{{else if .V.Summary.Bad}}bad{{else}}ok{{end}}">
    <b>{{.V.Summary.OK}} / {{len .V.Antennas}}</b><span>cover the band at SWR &lt; {{.Limit}}</span>
    <span>{{.V.Summary.Tuned}} on the ± {{.Aim}} MHz TUNE aim</span>
    <span class="yield">PASS {{index .V.Summary.Yield "good"}} · OK {{index .V.Summary.Yield "ok"}} · FAIL {{index .V.Summary.Yield "bad"}}</span>
  </div>
<section class="concl" aria-label="Conclusion">{{range $i, $c := .V.Summary.Conclusion}}<p{{if eq $i 0}} class="head"{{end}}>{{$c}}</p>{{end}}<p class="tl">{{.V.Summary.TuningLine}}.</p></section>
</div>

<div>{{.Spread}}</div>

<div class="tables">
<h3>Antennas</h3>
{{if .Halves}}<div class="halves">{{range .Halves}}<table class="ants"><thead><tr><th>#</th><th>Label</th><th>Status</th><th class="num">Res.</th><th class="num">Offset</th><th class="num">SWR@tgt</th><th class="num">Worst</th><th>Band</th></tr></thead><tbody>
{{range .}}<tr{{if .Outlier}} class="outl"{{end}}><td>{{.ID}}</td><td class="lbl">{{.Label}}{{if not .CalOK}} <span class="calbad">†</span>{{end}}</td><td class="st {{.SClass}}">{{.Status}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Res}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Off}}</td><td class="num{{if .OutSWR}} out{{end}}">{{.SWRT}}</td><td class="num{{if .OutWorst}} out{{end}}">{{.Worst}}</td><td class="v {{.VClass}}">{{.Verdict}}</td></tr>
{{end}}</tbody></table>{{end}}</div>
<p class="tnote">Compact table ({{len .Rows}} antennas): bandwidth, return loss and band coverage are in the CSV export · Band = PASS (worst in-band SWR &lt; {{.Good}}) / OK (&lt; {{.Limit}}) / FAIL · † = not calibrated for this range · ◆ = statistical outlier for that figure (|x − median| &gt; 3.5 × 1.4826 × MAD), not the same as off target.</p>
{{else}}<table class="ants"><thead><tr><th>#</th><th>Label</th><th>Status</th><th class="num">Resonance</th><th class="num">Offset</th><th class="num">%</th><th class="num">SWR@tgt</th><th class="num">Worst</th><th class="num">BW&lt;2</th><th class="num">RL</th><th>Band</th><th>Calibration</th></tr></thead><tbody>
{{range .Rows}}<tr{{if .Outlier}} class="outl"{{end}}><td>{{.ID}}</td><td class="lbl">{{.Label}}</td><td class="st {{.SClass}}">{{.Status}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Res}}</td><td class="num{{if .OutRes}} out{{end}}">{{.Off}}</td><td class="num">{{.OffPct}}</td><td class="num{{if .OutSWR}} out{{end}}">{{.SWRT}}</td><td class="num{{if .OutWorst}} out{{end}}">{{.Worst}}</td><td class="num{{if .OutBW}} out{{end}}">{{.BW}}</td><td class="num{{if .OutRL}} out{{end}}">{{.RL}}</td><td class="v {{.VClass}}">{{.Verdict}} · {{.Cover}}</td><td class="{{if not .CalOK}}calbad{{end}}">{{.Cal}}</td></tr>
{{end}}</tbody></table>
<p class="tnote">MHz · resonance = return-loss peak nearest the target (as TUNE) · BW&lt;2 = SWR &lt; 2 bandwidth of that dip, MHz · RL = return loss at the target, dB · Band = PASS (worst in-band SWR &lt; {{.Good}}) / OK (&lt; {{.Limit}}) / FAIL, and the share of the band with SWR &lt; {{.Limit}} · ◆ = statistical outlier for that figure (|x − median| &gt; 3.5 × 1.4826 × MAD, from 3 antennas on), not the same as off target.</p>{{end}}
</div>

<div class="tables">
<h3>Lot statistics</h3>
{{template "bstats" .V}}
</div>

{{with .V.Setup}}<p class="tnote setupline"><b>Setup check</b> {{.Time.Format "2006-01-02 15:04"}}: {{.Line}}</p>{{end}}
{{if .V.Notes}}<div><h3>Notes</h3><div class="notes">{{.V.Notes}}</div></div>{{end}}

<footer class="sfoot"><span>{{.App}} · by PiX (and the Claude, OpenAI and Deepseek AI band)</span><span>page 2 of 2 · {{.URL}}</span></footer>
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
	"mhzRange": mhzRange,
	"bandName": func(b rf.Band) string { return b.Name },
}

var batchSheetTmpl = template.Must(template.Must(template.New("batch").Funcs(reportFuncs).Funcs(batchFuncs).Funcs(template.FuncMap{
	"photo": func(s string) template.URL { return template.URL(s) }, // checked by checkPhoto
}).Parse(batchStatsHTML)).Parse(batchSheetHTML))
