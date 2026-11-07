package server

import (
	"fmt"
	"html/template"
	"math"
	"strings"

	"github.com/pix/easyvna-web/internal/rf"
)

// Print-oriented SVG plots for the report. One SVG unit is 0.25 mm at the
// nominal size (viewBox width = width in mm × 4), so a 12-unit font prints
// at about 8.5 pt. Colours are chosen to survive greyscale: one dark trace,
// a light-grey band and threshold lines told apart by dash pattern.

const (
	svgFont      = 12.0
	svgTrace     = "#1d4f91"
	svgInk       = "#222"
	svgMuted     = "#555"
	svgGrid      = "#dedede"
	svgFrame     = "#8a8a8a"
	svgBandFill  = "#e2e8f2"
	svgBandFill2 = "#c9d4e6"
	svgRef       = "#c4562a" // REF overlay (also dashed, for greyscale)
	svgHold      = "#6f6f6f" // HOLD envelope (dotted)
)

type svgSeries struct {
	Y     []float64
	Color string
	Width float64
	Dash  string
}

type svgHLine struct {
	Y     float64
	Label string
	Dash  string
}

type svgMark struct {
	F     float64
	Label string
}

type svgPlot struct {
	Wmm, Hmm   float64
	Freq       []float64
	Series     []svgSeries
	YMin, YMax float64
	YTicks     []float64
	YFmt       func(float64) string
	YUnit      string
	Bands      []rf.Band
	HLines     []svgHLine
	Marks      []svgMark
	Title      string
	Class      string
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

func esc(s string) string { return template.HTMLEscapeString(s) }

func (p svgPlot) render() template.HTML {
	W, H := p.Wmm*4, p.Hmm*4
	const l, r, t, b = 58.0, 14.0, 30.0, 40.0
	x0, x1, y0, y1 := l, W-r, t, H-b
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="plot %s" viewBox="0 0 %.0f %.0f" width="%.0fmm" height="%.0fmm" role="img" aria-label="%s" font-family="Helvetica, Arial, sans-serif">`,
		p.Class, W, H, p.Wmm, p.Hmm, esc(p.Title))
	fmt.Fprintf(&sb, `<text x="%.0f" y="%.0f" font-size="%.0f" font-weight="700" fill="%s">%s</text>`, x0, 18.0, svgFont+1, svgInk, esc(p.Title))
	if p.YUnit != "" {
		fmt.Fprintf(&sb, `<text x="%.0f" y="%.0f" font-size="%.0f" fill="%s" text-anchor="end">%s</text>`, x1, 18.0, svgFont, svgMuted, esc(p.YUnit))
	}
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
		fill := svgBandFill
		if bd.Hi-bd.Lo < 1e6 {
			fill = svgBandFill2
		}
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, a, y0, math.Max(2, z-a), y1-y0, fill)
		if bd.Hi-bd.Lo >= 1e6 {
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s">%s</text>`, a+3, y1-5, svgFont-1, svgMuted, esc(bd.Name))
		}
	}
	for _, tk := range p.YTicks {
		if tk == 0 {
			tk = 0 // no "-0" label
		}
		y := fy(tk)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/><text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="end">%s</text>`,
			x0, x1, y, y, svgGrid, x0-6, y+4, svgFont, svgMuted, p.YFmt(tk))
	}
	for _, tk := range niceTicks(fmin/1e6, fmax/1e6, int((x1-x0)/90)) {
		x := fx(tk * 1e6)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/><text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="middle">%g</text>`,
			x, x, y0, y1, svgGrid, x, y1+16, svgFont, svgMuted, tk)
	}
	fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="end">MHz</text>`, x1, H-6, svgFont, svgMuted)
	for _, h := range p.HLines {
		y := fy(h.Y)
		fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.4" stroke-dasharray="%s"/>`, x0, x1, y, y, svgMuted, h.Dash)
		if h.Label != "" {
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="end">%s</text>`, x1-3, y-4, svgFont-1, svgMuted, esc(h.Label))
		}
	}
	fmt.Fprintf(&sb, `<svg x="%.1f" y="%.1f" width="%.1f" height="%.1f" viewBox="%.1f %.1f %.1f %.1f" overflow="hidden">`,
		x0, y0-1, x1-x0, y1-y0+2, x0, y0-1, x1-x0, y1-y0+2)
	for _, s := range p.Series {
		var pts strings.Builder
		for i, f := range p.Freq {
			if i < len(s.Y) && !math.IsNaN(s.Y[i]) {
				fmt.Fprintf(&pts, "%.1f,%.1f ", fx(f), fy(s.Y[i]))
			}
		}
		w := s.Width
		if w == 0 {
			w = 2.4
		}
		dash := ""
		if s.Dash != "" {
			dash = ` stroke-dasharray="` + s.Dash + `"`
		}
		fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="%.1f" stroke-linejoin="round"%s/>`, pts.String(), s.Color, w, dash)
	}
	sb.WriteString(`</svg>`)
	for _, m := range p.Marks {
		if m.F < fmin || m.F > fmax {
			continue
		}
		x := fx(m.F)
		lx := math.Max(x0+24, math.Min(x1-24, x))
		fmt.Fprintf(&sb, `<path d="M%.1f %.1f l-5 -8 h10 z" fill="%s"/><text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="middle" font-weight="700">%s</text>`,
			x, y0+30, svgInk, lx, y0+18, svgFont-1, svgInk, esc(m.Label))
	}
	fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="none" stroke="%s" stroke-width="1"/></svg>`, x0, y0, x1-x0, y1-y0, svgFrame)
	return template.HTML(sb.String())
}

// smithSVG draws a print-friendly Smith chart, sizeMm wide, with an
// optional REF trace (dashed).
func smithSVG(res *Result, sizeMm float64, ref ...*Result) template.HTML {
	S := sizeMm * 4
	c, R := S/2, S/2-30
	cy := c + 8
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="plot smith" viewBox="0 0 %.0f %.0f" width="%.0fmm" height="%.0fmm" role="img" aria-label="Smith chart" font-family="Helvetica, Arial, sans-serif">`, S, S, sizeMm, sizeMm)
	fmt.Fprintf(&sb, `<text x="6" y="18" font-size="%.0f" font-weight="700" fill="%s">Smith chart</text>`, svgFont+1, svgInk)
	id := fmt.Sprintf("uc%d", res.Seq)
	fmt.Fprintf(&sb, `<defs><clipPath id="%s"><circle cx="%.1f" cy="%.1f" r="%.1f"/></clipPath></defs><g clip-path="url(#%s)">`, id, c, cy, R, id)
	for _, r := range []float64{0.2, 0.5, 1, 2, 5} {
		fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="%s" stroke-width="1"/>`, c+R*r/(1+r), cy, R/(1+r), svgGrid)
	}
	for _, x := range []float64{0.2, 0.5, 1, 2, 5} {
		for _, sg := range []float64{1, -1} {
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="%s" stroke-width="1"/>`, c+R, cy-sg*R/x, R/x, svgGrid)
		}
	}
	fmt.Fprintf(&sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`, c-R, c+R, cy, cy, svgGrid)
	fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="%s" stroke-width="1.4" stroke-dasharray="8 5"/></g>`, c, cy, R/3, svgMuted)
	fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="%s" stroke-width="1.4"/>`, c, cy, R, svgFrame)
	for _, r := range []float64{0.2, 0.5, 1, 2} {
		g := (r - 1) / (r + 1)
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="middle">%g</text>`, c+g*R, cy+14, svgFont-1, svgMuted, r*50)
	}
	fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="middle">+j50</text><text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="middle">−j50</text>`,
		c, cy-R-6, svgFont-1, svgMuted, c, cy+R+16, svgFont-1, svgMuted)
	fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s">SWR 2</text>`, c+R/3*0.72+3, cy-R/3*0.72-2, svgFont-1, svgMuted)
	for _, rr := range ref {
		if rr == nil {
			continue
		}
		var rp strings.Builder
		for _, g := range rr.S11 {
			fmt.Fprintf(&rp, "%.1f,%.1f ", c+g[0]*R, cy-g[1]*R)
		}
		fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.8" stroke-dasharray="7 4"/>`, rp.String(), svgRef)
	}
	var pts strings.Builder
	for _, g := range res.S11 {
		fmt.Fprintf(&pts, "%.1f,%.1f ", c+g[0]*R, cy-g[1]*R)
	}
	fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.2" stroke-linejoin="round"/>`, pts.String(), svgTrace)
	if len(res.Config.Bands) > 0 {
		b := res.Config.Bands[0]
		var bp strings.Builder
		for i, f := range res.Freq {
			if f >= b.Lo && f <= b.Hi {
				fmt.Fprintf(&bp, "%.1f,%.1f ", c+res.S11[i][0]*R, cy-res.S11[i][1]*R)
			}
		}
		if bp.Len() > 0 {
			fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="6" stroke-linecap="round"/>`, bp.String(), svgTrace)
			fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="end">thick = %s band</text>`, S-6, 18.0, svgFont-1, svgMuted, esc(b.Name))
		}
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// sparkSVG plots logged resonance per trim entry against the target.
func sparkSVG(entries []TrimEntry, target float64) template.HTML {
	if len(entries) == 0 {
		return ""
	}
	const W, H = 260.0, 90.0 // 65 × 22.5 mm
	lo, hi := target, target
	for _, e := range entries {
		lo, hi = math.Min(lo, e.FreqHz), math.Max(hi, e.FreqHz)
	}
	pad := math.Max((hi-lo)*0.15, 1e6)
	lo, hi = lo-pad, hi+pad
	fx := func(i int) float64 {
		if len(entries) == 1 {
			return W / 2
		}
		return 14 + float64(i)/float64(len(entries)-1)*(W-28)
	}
	fy := func(f float64) float64 { return H - 10 - (f-lo)/(hi-lo)*(H-34) }
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="spark" viewBox="0 0 %.0f %.0f" width="65mm" height="22.5mm" font-family="Helvetica, Arial, sans-serif">`, W, H)
	fmt.Fprintf(&sb, `<line x1="0" x2="%.0f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.4" stroke-dasharray="8 5"/>`, W, fy(target), fy(target), svgMuted)
	fmt.Fprintf(&sb, `<text x="2" y="%.1f" font-size="%.0f" fill="%s">target %.2f MHz</text>`, fy(target)-5, svgFont-1, svgMuted, target/1e6)
	var pts strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&pts, "%.1f,%.1f ", fx(i), fy(e.FreqHz))
	}
	fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.4"/>`, pts.String(), svgTrace)
	for i, e := range entries {
		fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s"/>`, fx(i), fy(e.FreqHz), svgTrace)
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}
