package server

import "html/template"

// sheetHTML is the one-page antenna sheet (PRINT): identity, key figures,
// the four plots, details, notes and a trim-log excerpt. Its layout is
// sized to fit one A4 or US Letter page (Letter is the shorter one:
// 261 mm of content height with these margins). With #print in the URL
// it opens the print dialog by itself.
const sheetHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.A.Name}}: antenna sheet</title>
<style>
@page { margin: 9mm 10mm; }
:root { color-scheme: light; --ink: #1a1a1a; --ink2: #444; --muted: #6b6b6b; --line: #d4d4d0; --soft: #f4f4f1;
  --pass: #0a7d0a; --ok: #b07a00; --fail: #b3261e; --na: #8a8a85; }
html { background: #e6e6e2; }
body { margin: 0; color: var(--ink); font: 8.5pt/1.3 Helvetica, Arial, "Segoe UI", sans-serif; -webkit-print-color-adjust: exact; print-color-adjust: exact; }
` + paperCSS + `
.paper { padding: 9mm 10mm; }
.sheet { display: flex; flex-direction: column; gap: 2.2mm; }
/* header */
.sh { display: grid; grid-template-columns: 1fr auto; gap: 4mm; border-bottom: .6mm solid var(--ink); padding-bottom: 2mm; }
.kicker { font-size: 7.5pt; letter-spacing: .14em; text-transform: uppercase; color: var(--muted); font-weight: 700; }
h1 { font-size: 19pt; line-height: 1.05; margin: .5mm 0 .8mm; overflow-wrap: anywhere; }
.what { font-size: 10pt; color: var(--ink2); margin-bottom: 1.2mm; }
.meta { display: grid; grid-template-columns: auto 1fr auto 1fr; gap: .3mm 3mm; font-size: 7.5pt; }
.meta dt { color: var(--muted); } .meta dd { margin: 0; }
.calbad { color: var(--fail); font-weight: 700; } .calok { color: var(--pass); }
.photo { width: 44mm; height: 31mm; border: .3mm solid var(--line); border-radius: 1mm; overflow: hidden; display: flex; align-items: center; justify-content: center; background: var(--soft); }
.photo img { max-width: 100%; max-height: 100%; display: block; }
.sim { display: inline-block; font-size: 7pt; font-weight: 700; letter-spacing: .08em; padding: .2mm 1.5mm; border: .3mm solid var(--ok); color: var(--ok); border-radius: 1mm; margin-left: 2mm; vertical-align: middle; }
.uncal { border: .5mm solid var(--fail); color: var(--fail); font-weight: 700; border-radius: 1mm; padding: 1mm 2mm; font-size: 8.5pt; }
.warn { border: .4mm solid var(--fail); border-radius: 1mm; padding: .8mm 2mm; font-size: 7.5pt; }
.drift { border-left: .8mm solid var(--line); padding: .3mm 2mm; font-size: 7.3pt; }
.drift.dw { border-left-color: var(--ok); }
/* key figures */
.kf { display: grid; grid-template-columns: 31mm 1fr; gap: 2.5mm; align-items: stretch; }
.verdict { border: .3mm solid var(--line); border-left: 2mm solid var(--na); border-radius: 1.5mm; padding: 2mm; display: flex; flex-direction: column; justify-content: center; }
.verdict.good { border-left-color: var(--pass); } .verdict.ok { border-left-color: var(--ok); } .verdict.bad { border-left-color: var(--fail); }
.verdict b { font-size: 21pt; font-weight: 800; letter-spacing: .04em; line-height: 1; }
.good b { color: var(--pass); } .ok b { color: var(--ok); } .bad b { color: var(--fail); } .na b { color: var(--na); }
.verdict span { font-size: 7pt; color: var(--muted); margin-top: 1mm; }
.figs { display: grid; grid-template-columns: repeat(6, 1fr); gap: 1.2mm 2.5mm; }
.fig { border-top: .3mm solid var(--line); padding-top: .6mm; min-width: 0; }
.fig i { display: block; font-style: normal; font-size: 6.8pt; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fig b { display: block; font-size: 12.5pt; font-variant-numeric: tabular-nums; line-height: 1.15; white-space: nowrap; }
.fig b.long { font-size: 9pt; }
.fig small { display: block; font-size: 6.5pt; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fig.good b { color: var(--pass); } .fig.ok b { color: var(--ok); } .fig.bad b { color: var(--fail); }
/* plots */
svg.plot { display: block; width: 100%; height: auto; }
.prow { display: grid; grid-template-columns: 136fr 52fr; gap: 2mm; align-items: center; }
.prow .stack { display: flex; flex-direction: column; gap: 1mm; }
.legend { font-size: 7pt; color: var(--ink2); display: flex; gap: 4mm; flex-wrap: wrap; }
/* details */
.cols { display: grid; grid-template-columns: 1fr 1fr; gap: 4mm; align-items: start; }
.cols.three { grid-template-columns: 1fr 1.15fr .95fr; gap: 3mm; }
h3 { font-size: 8pt; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 0 0 .8mm; }
table { width: 100%; border-collapse: collapse; font-size: 7.5pt; font-variant-numeric: tabular-nums; }
th, td { text-align: left; padding: .5mm 1mm; border-bottom: .2mm solid var(--line); vertical-align: top; }
th { font-size: 6.5pt; color: var(--muted); text-transform: uppercase; }
td.num, th.num { text-align: right; }
.kv td:first-child { color: var(--muted); width: 42%; }
.notes { white-space: pre-wrap; font-size: 7.8pt; max-height: 31mm; overflow: hidden; border: .3mm solid var(--line); background: var(--soft); border-radius: 1mm; padding: 1.2mm 2mm; }
.muted { color: var(--muted); }
.sfoot { border-top: .3mm solid var(--line); padding-top: 1mm; font-size: 7pt; color: var(--muted); display: flex; justify-content: space-between; gap: 4mm; }
@media print {
  html { background: #fff; }
  .paper { width: auto; margin: 0; padding: 0; box-shadow: none; }
  .toolbar, .noprint { display: none !important; }
  .sheet { break-inside: avoid; page-break-inside: avoid; }
}
@media (max-width: 700px) {
  .paper { padding: 4mm; } .figs { grid-template-columns: repeat(3, 1fr); } .prow, .cols, .kf { grid-template-columns: 1fr; }
  .meta { grid-template-columns: auto 1fr; }
}
</style>
` + paperScript + `
<script>if (location.hash === "#print") addEventListener("load", function () { setTimeout(function () { print(); }, 250); });</script>
</head><body>
` + paperToolbar + `
<div class="paper"><div class="sheet">

<header class="sh">
  <div>
    <div class="kicker">Antenna sheet{{if .Simulated}}<span class="sim">SIMULATED DATA</span>{{end}}{{if .File}}<span class="sim">FROM FILE · NOT LIVE</span>{{end}}</div>
    <h1>{{.A.Name}}</h1>
    <div class="what">{{if .A.Type}}{{.A.Type}}{{else}}antenna{{end}}{{if .A.Band}} · {{.A.Band}}{{end}}</div>
    <dl class="meta">
      <dt>Date</dt><dd>{{time .Generated}}</dd>
      <dt>Device</dt><dd>{{if .Device}}{{.Device}}{{else}}–{{end}}</dd>
      <dt>Sheet</dt><dd>{{.ID}}</dd>
      <dt>Firmware</dt><dd>{{if .Firmware}}{{.Firmware}}{{else}}–{{end}}</dd>
      <dt>Sweep</dt><dd style="grid-column: span 3">{{.Sweep}}</dd>
      <dt>Calibration</dt><dd style="grid-column: span 3" class="{{if .CalOK}}calok{{else}}calbad{{end}}">{{.CalState}}</dd>
    </dl>
  </div>
  {{if .A.Photo}}<div class="photo"><img src="{{.A.Photo | photo}}" alt="Photo of {{.A.Name}}"></div>{{end}}
</header>

{{range .Warnings}}<div class="warn"><b>{{.Title}}</b>: {{.Text}}</div>{{end}}

<section class="kf" aria-label="Key figures">
  <div class="verdict {{.VClass}}"><b>{{.Verdict}}</b><span>{{if .VNote}}{{.VNote}}{{else}}PASS = worst in-band SWR &lt; 1.5, OK &lt; 2{{end}}</span></div>
  <div class="figs">{{range .Figs}}<div class="fig {{.Class}}"><i>{{.K}}</i><b{{if gt (len .V) 9}} class="long"{{end}}>{{.V}}</b>{{if .Sub}}<small>{{.Sub}}</small>{{end}}</div>{{end}}</div>
</section>

<div>{{.SWR}}</div>
<div class="prow"><div class="stack"><div>{{.RL}}</div><div>{{.Z}}</div></div><div>{{.Smith}}</div></div>
<div class="legend">{{range .Legend}}<span>{{.}}</span>{{end}}</div>
{{with .Drift}}<div class="drift{{if .Warnings}} dw{{end}}"><b>History:</b> {{.Line}}{{range .Warnings}}{{if ne .Level "info"}} <b>{{.Title}}</b>: {{.Text}}{{end}}{{end}}</div>{{end}}

<div class="cols{{if .Trim}} three{{end}}">
  <div>
    <h3>Details</h3>
    {{if .Details}}<table class="kv"><tbody>{{range .Details}}<tr><td>{{.K}}</td><td>{{.V}}</td></tr>{{end}}</tbody></table>{{else}}<p class="muted">–</p>{{end}}
  </div>
  <div>
    <h3>Notes</h3>
    {{if .A.Notes}}<div class="notes">{{.A.Notes}}</div>{{else}}<p class="muted">–</p>{{end}}
  </div>
  {{if .Trim}}<div>
    <h3>Trim log{{if .TrimMore}} · last {{len .Trim}} of {{add .TrimMore (len .Trim)}}{{end}}</h3>
    <table><thead><tr><th class="num">Length</th><th class="num">Cut</th><th class="num">f res</th><th class="num">SWR@tgt</th></tr></thead><tbody>
    {{range .Trim}}<tr><td class="num">{{f1 .LengthMm}}</td><td class="num">{{if .CutMm}}{{f1 .CutMm}}{{else}}–{{end}}</td><td class="num">{{mhz .FreqHz}}</td><td class="num">{{swr .SWRAtTarget}}</td></tr>{{end}}
    </tbody></table>
    <p class="muted" style="margin:.6mm 0 0;font-size:6.8pt">mm · MHz · target {{mhz .TrimTgt}} MHz{{with .TrimNext}} · next: {{if gt .CutMm 0.15}}{{if .Cautious}}<b>first cut {{f1 .FirstCutMm}} mm</b> (full estimate {{f1 .CutMm}}; MHz/mm not learnt yet, a cut wire cannot be lengthened){{else}}<b>cut {{f1 .CutMm}} mm</b>{{end}}{{else if lt .CutMm -0.15}}<b>too short: do not cut</b> (lengthen {{f1 (neg .CutMm)}}){{else}}<b>on target</b>{{end}}{{end}}</p>
  </div>{{end}}
</div>

<footer class="sfoot"><span>{{.App}} · by PiX (and the Claude, OpenAI and Deepseek AI band)</span><span>{{.URL}}</span></footer>
</div></div>
</body></html>`

var sheetTmpl = template.Must(template.New("sheet").Funcs(reportFuncs).Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
	// photo marks the stored data: URL (validated by AntennaInfo.clean) as a
	// safe image URL; html/template would otherwise replace it.
	"photo": func(s string) template.URL { return template.URL(s) },
}).Parse(sheetHTML))
