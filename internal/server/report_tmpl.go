package server

// reportHTML is a print-first document: on screen it looks like a sheet of
// paper, and Print → Save as PDF gives a clean A4 (or Letter) report with a
// running footer "page X of Y" (CSS @page margin boxes, Chromium-based
// browsers; other browsers print without the page numbers).
const reportHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}: EasyVNA antenna report</title>
<style>
@page {
  size: A4 portrait;
  margin: 12mm 13mm 15mm 13mm;
  @bottom-left { content: "EasyVNA Web · report {{.ReportID}} · generated {{time .Generated}}"; font: 7.5pt Helvetica, Arial, sans-serif; color: #666; }
  @bottom-right { content: "page " counter(page) " of " counter(pages); font: 7.5pt Helvetica, Arial, sans-serif; color: #666; }
}
:root { color-scheme: light; --ink: #1a1a1a; --ink2: #444; --muted: #6b6b6b; --line: #d4d4d0; --soft: #f4f4f1;
  --pass: #0a7d0a; --ok: #b07a00; --fail: #b3261e; --na: #8a8a85; }
* { box-sizing: border-box; }
html { background: #e6e6e2; }
body { margin: 0; color: var(--ink); font: 9.5pt/1.4 Helvetica, Arial, "Segoe UI", sans-serif; -webkit-print-color-adjust: exact; print-color-adjust: exact; }
.paper { width: 210mm; max-width: 100%; margin: 12px auto 24px; background: #fff; padding: 12mm 13mm; box-shadow: 0 2px 12px rgba(0,0,0,.15); }
.toolbar { width: 210mm; max-width: 100%; margin: 12px auto 0; display: flex; gap: 8px; }
.toolbar button, .toolbar a { font: 10pt Helvetica, Arial, sans-serif; padding: 6px 12px; border: 1px solid #bbb; border-radius: 6px; background: #fff; color: #222; text-decoration: none; cursor: pointer; }

/* header */
.rh { display: grid; grid-template-columns: 1fr auto; gap: 4mm; border-bottom: 0.6mm solid var(--ink); padding-bottom: 3mm; margin-bottom: 4mm; }
.kicker { font-size: 8.5pt; letter-spacing: .12em; text-transform: uppercase; color: var(--muted); font-weight: 700; }
h1 { font-size: 20pt; line-height: 1.1; margin: 1mm 0 2mm; }
.meta { display: grid; grid-template-columns: auto 1fr auto 1fr; gap: .6mm 4mm; font-size: 8.5pt; }
.meta dt { color: var(--muted); }
.meta dd { margin: 0; }
.credit { text-align: right; font-size: 7.5pt; color: var(--muted); }
.credit b { color: var(--ink2); font-size: 9pt; }
.sim { display: inline-block; margin-top: 2mm; font-size: 7.5pt; font-weight: 700; letter-spacing: .08em; padding: .4mm 2mm; border: .3mm solid var(--ok); color: var(--ok); border-radius: 1mm; }
.calok { color: var(--pass); } .calbad { color: var(--fail); }

h2 { font-size: 12pt; margin: 5mm 0 2mm; padding-bottom: 1mm; border-bottom: .3mm solid var(--line); break-after: avoid; page-break-after: avoid; }
h2 small { font-weight: 400; color: var(--muted); font-size: 8.5pt; }
h3 { font-size: 10pt; margin: 3mm 0 1.5mm; break-after: avoid; page-break-after: avoid; }

/* verdict cards */
.verdicts { display: grid; grid-template-columns: repeat(auto-fill, minmax(86mm, 1fr)); gap: 3mm; }
.vc { border: .3mm solid var(--line); border-left: 2mm solid var(--na); border-radius: 1.5mm; padding: 2.5mm 3mm; break-inside: avoid; page-break-inside: avoid; }
.vc.good { border-left-color: var(--pass); } .vc.ok { border-left-color: var(--ok); } .vc.bad { border-left-color: var(--fail); }
.vtop { display: flex; justify-content: space-between; align-items: baseline; gap: 2mm; }
.vband { font-weight: 700; font-size: 10.5pt; } .vrange { color: var(--muted); font-size: 8pt; }
.vlabel { font-size: 18pt; font-weight: 800; letter-spacing: .04em; line-height: 1; }
.good .vlabel { color: var(--pass); } .ok .vlabel { color: var(--ok); } .bad .vlabel { color: var(--fail); } .na .vlabel { color: var(--na); }
.vgrid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1mm 3mm; margin-top: 2mm; }
.vgrid div { font-size: 8pt; color: var(--muted); }
.vgrid b { display: block; font-size: 10pt; color: var(--ink); font-variant-numeric: tabular-nums; }
.vnote { margin-top: 1.5mm; font-size: 8pt; color: var(--fail); }

.notes { border: .3mm solid var(--line); background: var(--soft); border-radius: 1.5mm; padding: 2.5mm 3mm; margin-top: 3mm; white-space: pre-wrap; break-inside: avoid; }
.notes b { display: block; font-size: 8pt; color: var(--muted); text-transform: uppercase; letter-spacing: .08em; margin-bottom: 1mm; }
.warn { border: .4mm solid var(--fail); border-radius: 1.5mm; padding: 2mm 3mm; margin: 2mm 0; font-size: 8.5pt; break-inside: avoid; }

/* plots */
.card { break-inside: avoid; page-break-inside: avoid; }
svg.plot { display: block; width: 100%; height: auto; }
.row2 { display: grid; grid-template-columns: 124fr 52fr; gap: 4mm; align-items: start; margin-top: 2mm; break-inside: avoid; page-break-inside: avoid; }
.row2 svg.smith { max-width: 52mm; }

/* tables */
table { width: 100%; border-collapse: collapse; font-size: 8.5pt; font-variant-numeric: tabular-nums; break-inside: avoid; page-break-inside: avoid; }
th, td { text-align: left; padding: 1mm 1.5mm; border-bottom: .2mm solid var(--line); vertical-align: top; }
th { font-size: 7.5pt; color: var(--muted); font-weight: 700; text-transform: uppercase; letter-spacing: .04em; }
td.num, th.num { text-align: right; }
.kv td:first-child { color: var(--muted); width: 38%; }
.kv4 { margin-top: 2mm; } .kv4 td { width: auto !important; } .kv4 td:nth-child(odd) { color: var(--muted); white-space: nowrap; }
table.dips td:nth-child(-n+8) { white-space: nowrap; }
.cols { display: grid; grid-template-columns: 70mm 1fr; gap: 4mm; align-items: start; margin-top: 2mm; }
.pos { color: var(--pass); font-weight: 700; } .neg { color: var(--fail); font-weight: 700; }
.foot { margin-top: 6mm; padding-top: 2mm; border-top: .3mm solid var(--line); font-size: 7.5pt; color: var(--muted); display: flex; justify-content: space-between; gap: 4mm; }

@media print {
  html { background: #fff; }
  .paper { width: auto; margin: 0; padding: 0; box-shadow: none; }
  .toolbar, .noprint { display: none !important; }
  .foot { display: none; }
}
@media (max-width: 700px) {
  .paper { padding: 5mm; } .meta { grid-template-columns: auto 1fr; } .row2, .cols { grid-template-columns: 1fr; }
}
</style></head><body>
<div class="toolbar noprint"><button onclick="print()">Print / save as PDF</button><a href="?download=1">Download HTML</a></div>
<div class="paper">

<header class="rh">
  <div>
    <div class="kicker">EasyVNA antenna report</div>
    <h1>{{.Name}}</h1>
    <dl class="meta">
      <dt>Date</dt><dd>{{time .Generated}}</dd>
      <dt>Report</dt><dd>{{.ReportID}}</dd>
      <dt>Device</dt><dd>{{if .Device}}{{.Device}}{{else}}–{{end}}</dd>
      <dt>Firmware</dt><dd>{{if .Firmware}}{{.Firmware}}{{else}}–{{end}}</dd>
      <dt>Profile</dt><dd>{{if .Profiles}}{{.Profiles}}{{else}}–{{end}}</dd>
      <dt>Calibration</dt><dd class="{{if .CalOK}}calok{{else}}calbad{{end}}">{{.CalState}}</dd>
    </dl>
  </div>
  <div class="credit"><b>EasyVNA Web</b><br>by PiX<br>{{if .Simulated}}<span class="sim">SIMULATED DATA</span>{{end}}</div>
</header>

{{range .Warnings}}<div class="warn"><b>{{.Title}}</b>: {{.Text}}</div>{{end}}

{{if .Verdicts}}<h2>Verdict per band</h2>
<div class="verdicts">{{range .Verdicts}}
  <div class="vc {{.Class}}">
    <div class="vtop"><div><div class="vband">{{.Band}}</div><div class="vrange">{{.Range}}</div></div><div class="vlabel">{{.Label}}</div></div>
    <div class="vgrid">
      <div>Worst SWR<b>{{.WorstSWR}}</b>@ {{.WorstAt}}</div>
      <div>Resonance<b>{{.Resonance}}</b>{{.Offset}}</div>
      <div>Return loss<b>{{.RL}}</b>worst in band</div>
      <div>Mismatch loss<b>{{.ML}}</b>worst in band</div>
      {{if .BW2}}<div>SWR &lt; 2 bandwidth<b>{{.BW2}}</b></div><div>Q (estimated)<b>{{.Q}}</b></div>{{end}}
    </div>
    {{if .Note}}<div class="vnote">{{.Note}}</div>{{end}}
  </div>{{end}}
</div>{{end}}

{{if .Notes}}<div class="notes"><b>Notes</b>{{.Notes}}</div>{{end}}

{{range .Sections}}<section class="prof">
  <h2>{{.Label}} <small>· swept {{hms .Res.Time}}{{if gt .Res.AvgN 1}} · average of {{.Res.AvgN}}{{end}}</small></h2>
  <div class="card">{{.SWR}}</div>
  <div class="row2"><div class="card">{{.RL}}</div><div class="card">{{.Smith}}</div></div>
  <table class="kv kv4"><tbody>{{range .KeyRows}}<tr>{{range .}}<td>{{.K}}</td><td>{{.V}}</td>{{end}}</tr>{{end}}</tbody></table>
  {{if .Res.Summary.Dips}}<h3>Resonances</h3><table class="dips"><thead><tr><th>#</th><th class="num">MHz</th><th class="num">SWR</th><th class="num">RL</th><th class="num">ML</th><th>Z</th><th class="num">BW&lt;2</th><th class="num">Q</th><th>Note</th></tr></thead><tbody>
    {{range $i, $d := .Res.Summary.Dips}}<tr><td>{{inc $i}}</td><td class="num"><b>{{mhz $d.Freq}}</b></td><td class="num">{{swr $d.SWR}}</td><td class="num">{{f1 $d.RL}}</td><td class="num">{{f2 $d.ML}}</td><td>{{z $d.R $d.X}}</td><td class="num">{{bw $d.BW2}}</td><td class="num">{{q $d.Q}}</td><td>{{$d.Advice}}</td></tr>{{end}}
    </tbody></table>{{else}}<p class="vrange">No resonance dip in this sweep.</p>{{end}}
</section>{{end}}

{{with .Hold}}<section class="card"><h2>HOLD: stable positions <small>· {{.Label}}</small></h2>
  <table class="kv" style="width:auto;margin-bottom:2mm"><tbody>
    <tr><td>Positions</td><td><b>{{.Positions}}</b></td></tr>
    <tr><td>Resonance spread</td><td><b>{{.Spread}}</b> ({{.Range}})</td></tr>
    <tr><td>Worst in-band SWR</td><td><b>{{.WorstBand}}</b></td></tr>
  </tbody></table>
  <table><thead><tr><th>#</th><th>Captured</th><th class="num">Resonance</th><th class="num">Best SWR</th><th class="num">Worst in band</th></tr></thead><tbody>
  {{range .Rows}}<tr><td>{{.ID}}</td><td>{{hms .Time}}</td><td class="num">{{mhz .ResFreq}} MHz</td><td class="num">{{swr .MinSWR}}</td><td class="num">{{if .BandWorst}}{{swr .BandWorst}}{{else}}–{{end}}</td></tr>{{end}}
  </tbody></table>
</section>{{end}}

{{with .Trim}}<section class="card"><h2>Trim log <small>· {{.Name}} · target {{mhz3 .Target}} MHz</small></h2>
  <div class="cols"><div>{{$.TrimSpark}}
    {{with .Prediction}}<p style="margin:1mm 0 0">Next: {{if gt .CutMm 0.15}}<b>cut {{f1 .CutMm}} mm</b>{{else if lt .CutMm -0.15}}<b>lengthen {{f1 (neg .CutMm)}} mm</b>{{else}}<b>on target</b>{{end}} → {{f1 .NewLengthMm}} mm<br><span class="vrange">{{f2 .MHzPerMm}} MHz/mm ({{.Method}}{{if eq .Method "fit"}}, learnt from {{.Points}} logs{{end}})</span></p>{{end}}</div>
  <table><thead><tr><th>#</th><th>Time</th><th class="num">Length</th><th class="num">Cut</th><th class="num">Resonance</th><th class="num">Δ target</th><th class="num">SWR @ target</th></tr></thead><tbody>
  {{range $i, $e := .Entries}}<tr><td>{{inc $i}}</td><td>{{hms $e.Time}}</td><td class="num">{{f1 $e.LengthMm}} mm</td><td class="num">{{if $e.CutMm}}{{f1 $e.CutMm}} mm{{else}}–{{end}}</td><td class="num">{{mhz3 $e.FreqHz}} MHz</td><td class="num">{{dmhz $e.FreqHz $.Trim.Target}} MHz</td><td class="num">{{swr $e.SWRAtTarget}}</td></tr>{{end}}
  </tbody></table></div>
</section>{{end}}

{{with .Compare}}<section class="card"><h2>S21 compare <small>· relative to {{.AName}} · {{.Band.Name}} {{mhz .Band.Lo}}–{{mhz .Band.Hi}} MHz</small></h2>
  <p class="vrange" style="margin:0 0 2mm">Fixed reference antenna on port 1, antenna under test on port 2. Relative numbers only (no THRU calibration needed); same placement and cables for every antenna.</p>
  <table><thead><tr><th>#</th><th>Antenna</th><th class="num">Δ in band (avg)</th><th class="num">Worst</th><th class="num">Best</th><th>Measured</th></tr></thead><tbody>
  {{range $i, $e := .Leaderboard}}<tr><td>{{inc $i}}</td><td><b>{{$e.Name}}</b></td><td class="num {{if gt $e.MeanDB 0.05}}pos{{else if lt $e.MeanDB -0.05}}neg{{end}}">{{sgn1 $e.MeanDB}} dB</td><td class="num">{{sgn1 $e.WorstDB}} dB</td><td class="num">{{sgn1 $e.BestDB}} dB</td><td>{{hms $e.Time}}</td></tr>{{end}}
  </tbody></table>
</section>{{end}}

<p class="vrange" style="margin-top:4mm">Grades: PASS = worst in-band SWR &lt; 1.5, OK &lt; 2, FAIL otherwise (band edges interpolated). Q is estimated from the SWR &lt; 2 bandwidth assuming a match at resonance. Mismatch loss = −10·log10(1−|Γ|²).</p>
<div class="foot"><span>EasyVNA Web · by PiX (and the Claude, OpenAI and Deepseek AI band)</span><span>report {{.ReportID}} · generated {{time .Generated}}</span></div>
</div></body></html>`
