package server

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- the resonance strip of a batch ----------
//
// One horizontal frequency axis with separate lanes, top to bottom:
//
//	(a) the band, as a labelled bracket;
//	(b) the acceptance window (Auto: from the antennas' own SWR margin,
//	    asymmetric; else ± the tolerance) as a filled bar, TUNE's fine aim
//	    as a dark tick inside it, and the target line;
//	(c) one marker per antenna with its label, colour and shape by status,
//	    stacked in rows so neither markers nor labels overlap; statistical
//	    outliers are ringed;
//	(d) the lot summary: the mean (n < 3), the mean with its 95 %
//	    confidence interval (3 ≤ n < 5) or the mean ± σ (n ≥ 5);
//
// then the axis (with the target value labelled) and a one-line legend.
// The axis covers the band, the window, the summary and every antenna;
// an antenna further than 3 band-and-window widths away is drawn as an
// arrow at the edge with its value. One SVG unit is 0.25 mm; text is at
// least 10 units (7 pt). Every text is placed through a box check, so no
// two texts overlap and none leaves the viewBox (spreadLayout returns the
// boxes for the tests).

const (
	stripTuned = "#0a7d0a" // ● tuned
	stripOK    = "#11708f" // ◆ ok: covered, off the fine aim
	stripOff   = "#a35f00" // ▼ ▲ shorten / lengthen
	stripBad   = "#b3261e" // ✕ bad, no resonance
	stripWin   = "#cfe6cf" // acceptance window fill
	stripWinLn = "#0a7d0a"
	stripAim   = "#05520a"
	stripFS    = 10.0 // label font size (7 pt)
	stripRow   = 16.0 // row height in the antenna and summary lanes
)

// textBox is the box of one placed text (SVG units).
type textBox struct {
	X0, Y0, X1, Y1 float64
	Text           string
}

func (a textBox) overlaps(b textBox) bool {
	return a.X0 < b.X1 && b.X0 < a.X1 && a.Y0 < b.Y1 && b.Y0 < a.Y1
}

// textW estimates the width of s at font size fs (Helvetica: about 0.56 em
// per character on average; 0.6 is used to stay on the safe side).
func textW(s string, fs float64) float64 { return float64(len([]rune(s))) * fs * 0.6 }

type stripCanvas struct {
	sb    strings.Builder
	boxes []textBox
	W     float64
}

// box returns the box of a text at (x, baseline y) with the anchor.
func (c *stripCanvas) box(x, y, fs float64, anchor, s string) textBox {
	w := textW(s, fs)
	x0 := x
	switch anchor {
	case "middle":
		x0 = x - w/2
	case "end":
		x0 = x - w
	}
	return textBox{x0, y - fs*0.8, x0 + w, y + fs*0.25, s}
}

// free reports whether b fits in the canvas width and overlaps no text.
func (c *stripCanvas) free(b textBox, extra ...textBox) bool {
	if b.X0 < 1 || b.X1 > c.W-1 {
		return false
	}
	for _, o := range c.boxes {
		if o.overlaps(b) {
			return false
		}
	}
	for _, o := range extra {
		if o.overlaps(b) {
			return false
		}
	}
	return true
}

// text draws a text and records its box.
func (c *stripCanvas) text(x, y, fs float64, anchor, fill, weight, s string) textBox {
	b := c.box(x, y, fs, anchor, s)
	c.boxes = append(c.boxes, b)
	w := ""
	if weight != "" {
		w = ` font-weight="` + weight + `"`
	}
	fmt.Fprintf(&c.sb, `<text class="lbl" x="%.1f" y="%.1f" font-size="%.0f" fill="%s" text-anchor="%s"%s>%s</text>`, x, y, fs, fill, anchor, w, esc(s))
	return b
}

func shortLabel(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// stripMarker draws an antenna marker (radius about 6 units = 3 mm wide).
func stripMarker(sb *strings.Builder, status string, x, y float64) {
	switch status {
	case "tuned":
		fmt.Fprintf(sb, `<circle cx="%.1f" cy="%.1f" r="6" fill="%s"/>`, x, y, stripTuned)
	case "ok":
		fmt.Fprintf(sb, `<path d="M%.1f %.1f l6.5 6.5 l-6.5 6.5 l-6.5 -6.5 z" fill="%s"/>`, x, y-6.5, stripOK)
	case "shorten":
		fmt.Fprintf(sb, `<path d="M%.1f %.1f h13 l-6.5 11 z" fill="%s"/>`, x-6.5, y-5.5, stripOff)
	case "lengthen":
		fmt.Fprintf(sb, `<path d="M%.1f %.1f h13 l-6.5 -11 z" fill="%s"/>`, x-6.5, y+5.5, stripOff)
	default:
		fmt.Fprintf(sb, `<path d="M%.1f %.1f l11 11 m0 -11 l-11 11" stroke="%s" stroke-width="3"/>`, x-5.5, y-5.5, stripBad)
	}
}

// stripSummary is the lot summary bar: centre, half-width, label.
func stripSummary(v BatchView) (mean, half float64, label string, ok bool) {
	sm := v.Summary
	if sm.NRes < 1 {
		return 0, 0, "", false
	}
	mean = v.Target + sm.BiasHz
	switch {
	case sm.NRes < 3:
		return mean, 0, fmt.Sprintf("mean %s MHz (n = %d)", mhzF(mean), sm.NRes), true
	case sm.NRes < firmN:
		return mean, sm.BiasCIHz, fmt.Sprintf("mean %s ± %.2f MHz (95 %% CI, n = %d)", mhzF(mean), sm.BiasCIHz/1e6, sm.NRes), true
	}
	return mean, sm.SigmaHz, fmt.Sprintf("mean %s ± σ %.2f MHz (n = %d)", mhzF(mean), sm.SigmaHz/1e6, sm.NRes), true
}

// stripWindow is the acceptance window as absolute frequencies.
func stripWindow(v BatchView) (lo, hi float64, label string, ok bool) {
	sm := v.Summary
	w := sm.Window
	switch {
	case w.Mode == rf.TolAuto && w.Hz > 0 && sm.OffMin < 0 && sm.OffMax > 0:
		lo, hi = v.Target+sm.OffMin, v.Target+sm.OffMax
		label = fmt.Sprintf("Auto window %s to +%.2f MHz", signed2(sm.OffMin/1e6), sm.OffMax/1e6)
	case w.Hz > 0:
		lo, hi = v.Target-w.Hz, v.Target+w.Hz
		label = fmt.Sprintf("window ± %.2f MHz", w.Hz/1e6)
		if w.Mode == rf.TolAuto {
			label = "Auto window " + label[len("window "):]
		}
	default:
		return 0, 0, "", false
	}
	return lo, hi, label, true
}

// spreadSVG is the resonance strip, wmm wide (the height follows from the
// lanes; hmm is kept for the callers and ignored).
func spreadSVG(v BatchView, wmm, hmm float64) template.HTML {
	svg, _, _, _ := spreadLayout(v, wmm)
	return template.HTML(svg)
}

// spreadLayout draws the strip and returns it with its text boxes and size.
func spreadLayout(v BatchView, wmm float64) (string, []textBox, float64, float64) {
	W := wmm * 4
	const l, r = 14.0, 14.0
	sm := v.Summary
	band := rf.Band{}
	if len(v.Bands) > 0 {
		band = v.Bands[0]
	}
	// Range: band, window, aim, summary; then the antennas unless far away.
	lo, hi := v.Target, v.Target
	if band.Valid() {
		lo, hi = math.Min(lo, band.Lo), math.Max(hi, band.Hi)
	}
	wlo, whi, wlabel, wok := stripWindow(v)
	if wok {
		lo, hi = math.Min(lo, wlo), math.Max(hi, whi)
	}
	lo, hi = math.Min(lo, v.Target-sm.AimHz), math.Max(hi, v.Target+sm.AimHz)
	mean, half, slabel, sok := stripSummary(v)
	if sok {
		lo, hi = math.Min(lo, mean-half), math.Max(hi, mean+half)
	}
	core := math.Max(hi-lo, 1e6)
	for _, a := range v.Antennas {
		if f := a.Metrics.ResFreq; a.Metrics.Found && f > lo-3*core && f < hi+3*core {
			lo, hi = math.Min(lo, f), math.Max(hi, f)
		}
	}
	pad := (hi - lo) * 0.05
	lo, hi = lo-pad, hi+pad
	fx := func(f float64) float64 { return l + (f-lo)/(hi-lo)*(W-l-r) }
	clampX := func(x float64) float64 { return math.Max(l, math.Min(W-r, x)) }

	c := &stripCanvas{W: W} // the <svg> header is added at the end, when the height is known
	y := 13.0
	tol := "window " + sm.Window.Short
	if !wok {
		tol = "no acceptance window"
	}
	c.text(l, y, stripFS+1, "start", svgInk, "700", fmt.Sprintf("Resonance of each antenna · target %s MHz · %s", mhzF(v.Target), tol))
	// (a) band bracket
	y += 8
	bandY := 0.0 // baseline of the band's label (the window label may share it)
	if band.Valid() {
		a, z := fx(band.Lo), fx(band.Hi)
		label := fmt.Sprintf("%s band %s–%s MHz", band.Name, mhzF(band.Lo), mhzF(band.Hi))
		ly := y + 9
		bandY = ly
		cx := (a + z) / 2
		b := c.box(cx, ly, stripFS, "middle", label)
		if b.X0 < 1 {
			cx += 1 - b.X0
		} else if b.X1 > W-1 {
			cx -= b.X1 - (W - 1)
		}
		c.text(cx, ly, stripFS, "middle", svgInk, "", label)
		by := ly + 5
		fmt.Fprintf(&c.sb, `<path d="M%.1f %.1f v-4 M%.1f %.1f H%.1f M%.1f %.1f v-4" stroke="%s" stroke-width="1.6" fill="none"/>`, a, by+4, a, by+4, z, z, by+4, svgInk)
		y = by + 8
	}
	// (b) acceptance window, aim, target
	laneB := y
	labelRow := 0.0 // the window's label needed a row of its own
	barTop, barH := laneB+1, 10.0
	if wok {
		a, z := fx(wlo), fx(whi)
		fmt.Fprintf(&c.sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" stroke="%s" stroke-width="1.2"/>`, a, barTop, math.Max(2, z-a), barH, stripWin, stripWinLn)
		// The label: beside the bar, else at the end of the band's row,
		// else on a row of its own under the bar.
		tx := fx(v.Target)
		by := barTop + barH - 1.5
		placed := false
		for _, cd := range []struct {
			x, y   float64
			anchor string
		}{{z + 5, by, "start"}, {a - 5, by, "end"}, {W - r, bandY, "end"}, {l, bandY, "start"}} {
			if cd.y <= 0 {
				continue
			}
			b := c.box(cd.x, cd.y, stripFS, cd.anchor, wlabel)
			if cd.y == by && b.X0 <= tx && b.X1 >= tx {
				continue // the target line crosses the bar: the label must not
			}
			if c.free(b) {
				c.text(cd.x, cd.y, stripFS, cd.anchor, stripWinLn, "700", wlabel)
				placed = true
				break
			}
		}
		if !placed {
			ly := barTop + barH + 11
			b := c.box((a+z)/2, ly, stripFS, "middle", wlabel)
			x := (a + z) / 2
			if b.X0 < 1 {
				x += 1 - b.X0
			} else if b.X1 > W-1 {
				x -= b.X1 - (W - 1)
			}
			c.text(x, ly, stripFS, "middle", stripWinLn, "700", wlabel)
			labelRow = 13
		}
	}
	if aim := sm.AimHz; aim > 0 && (!wok || math.Abs(sm.Window.Hz-aim) > 1e3) {
		a, z := fx(v.Target-aim), fx(v.Target+aim)
		fmt.Fprintf(&c.sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="4" fill="%s"/>`, a, barTop+barH/2-2, math.Max(2, z-a), stripAim)
	}
	y = barTop + barH + 5 + labelRow
	// (c) antennas
	laneC := y
	type mark struct {
		x     float64
		a     BatchAntennaView
		row   int
		label string
		lb    textBox
		left  bool
		edge  int // −1 / +1: beyond the axis
	}
	var marks []mark
	for _, a := range v.Antennas {
		if !a.Metrics.Found {
			continue
		}
		m := mark{a: a, label: shortLabel(a.Label, 12)}
		f := a.Metrics.ResFreq
		switch {
		case f < lo:
			m.edge, m.x = -1, l+7
			m.label += " " + mhzF(f)
		case f > hi:
			m.edge, m.x = 1, W-r-7
			m.label += " " + mhzF(f)
		default:
			m.x = fx(f)
		}
		marks = append(marks, m)
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].x < marks[j].x })
	const maxRows = 8
	type occ struct{ x0, x1 float64 }
	rowsOcc := make([][]occ, maxRows)
	rows := 0
	fits := func(row int, x0, x1 float64) bool {
		for _, o := range rowsOcc[row] {
			if x0 < o.x1+2 && o.x0 < x1+2 {
				return false
			}
		}
		return true
	}
	for i := range marks {
		m := &marks[i]
		lw := textW(m.label, stripFS)
		placed := false
		for row := 0; row < maxRows && !placed; row++ {
			for _, left := range []bool{false, true} {
				x0, x1 := m.x-8, m.x+10+lw
				if left {
					x0, x1 = m.x-10-lw, m.x+8
				}
				if x0 < 1 || x1 > W-1 || !fits(row, x0, x1) {
					continue
				}
				m.row, m.left, placed = row, left, true
				rowsOcc[row] = append(rowsOcc[row], occ{x0, x1})
				break
			}
		}
		if !placed { // no room for the label: the marker alone, in the first row it fits
			m.label = ""
			for row := 0; row < maxRows; row++ {
				if fits(row, m.x-8, m.x+8) || row == maxRows-1 {
					m.row = row
					rowsOcc[row] = append(rowsOcc[row], occ{m.x - 8, m.x + 8})
					break
				}
			}
		}
		if m.row+1 > rows {
			rows = m.row + 1
		}
	}
	if rows == 0 {
		rows = 1
	}
	laneCEnd := laneC + float64(rows)*stripRow
	for _, m := range marks {
		my := laneC + float64(m.row)*stripRow + stripRow/2
		status := m.a.Metrics.Tuning.Status
		if m.edge != 0 {
			col := map[string]string{"tuned": stripTuned, "ok": stripOK, "shorten": stripOff, "lengthen": stripOff}[status]
			if col == "" {
				col = stripBad
			}
			d := float64(m.edge)
			fmt.Fprintf(&c.sb, `<path d="M%.1f %.1f l%.1f 6 l%.1f 6 z" fill="%s"/>`, m.x-d*6, my-6, d*12, -d*12, col)
		} else {
			stripMarker(&c.sb, status, m.x, my)
		}
		if len(m.a.Metrics.Outliers) > 0 {
			fmt.Fprintf(&c.sb, `<circle cx="%.1f" cy="%.1f" r="9.5" fill="none" stroke="%s" stroke-width="1.5"/>`, m.x, my, svgInk)
		}
		if m.label != "" {
			if m.left {
				c.text(m.x-10, my+3.5, stripFS, "end", svgInk, "700", m.label)
			} else {
				c.text(m.x+10, my+3.5, stripFS, "start", svgInk, "700", m.label)
			}
		}
	}
	y = laneCEnd + 2
	// (d) lot summary
	if sok {
		sy := y + stripRow/2
		mx := clampX(fx(mean))
		x1, x2 := clampX(fx(mean-half)), clampX(fx(mean+half))
		if half > 0 {
			fmt.Fprintf(&c.sb, `<path d="M%.1f %.1f H%.1f M%.1f %.1f v10 M%.1f %.1f v10" stroke="%s" stroke-width="2" fill="none"/>`, x1, sy, x2, x1, sy-5, x2, sy-5, svgInk)
		}
		fmt.Fprintf(&c.sb, `<path d="M%.1f %.1f l6 6 l-6 6 l-6 -6 z" fill="%s"/>`, mx, sy-6, svgInk)
		right := c.box(math.Max(x2, mx+6)+6, sy+3.5, stripFS, "start", slabel)
		left := c.box(math.Min(x1, mx-6)-6, sy+3.5, stripFS, "end", slabel)
		switch {
		case c.free(right):
			c.text(right.X0, sy+3.5, stripFS, "start", svgInk, "", slabel)
		case c.free(left):
			c.text(left.X1, sy+3.5, stripFS, "end", svgInk, "", slabel)
		default: // too wide for either side: below the bar, centred and clamped
			b := c.box(mx, sy+stripRow+1, stripFS, "middle", slabel)
			x := mx
			if b.X0 < 1 {
				x += 1 - b.X0
			} else if b.X1 > W-1 {
				x -= b.X1 - (W - 1)
			}
			c.text(x, sy+stripRow+1, stripFS, "middle", svgInk, "", slabel)
			y += stripRow
		}
		y += stripRow + 2
	}
	// Target line across lanes b–d.
	tx := fx(v.Target)
	fmt.Fprintf(&c.sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.6" stroke-dasharray="5 3"/>`, tx, tx, laneB-2, y, svgInk)
	// Axis: the target value, MHz, then the ticks that do not collide.
	axis := y + 1
	fmt.Fprintf(&c.sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.2"/>`, l, W-r, axis, axis, svgFrame)
	ty := axis + 14
	tb := c.box(tx, ty, stripFS, "middle", mhzF(v.Target))
	tX := tx
	if tb.X0 < 1 {
		tX += 1 - tb.X0
	} else if tb.X1 > W-1 {
		tX -= tb.X1 - (W - 1)
	}
	fmt.Fprintf(&c.sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="2"/>`, tx, tx, axis, axis+5, svgInk)
	c.text(tX, ty, stripFS, "middle", svgInk, "700", mhzF(v.Target))
	if b := c.box(W-r, ty, stripFS, "end", "MHz"); c.free(b) {
		c.text(W-r, ty, stripFS, "end", svgMuted, "", "MHz")
	}
	for _, tk := range niceTicks(lo/1e6, hi/1e6, int((W-l-r)/70)) {
		x := fx(tk * 1e6)
		if x < l || x > W-r {
			continue
		}
		s := fmt.Sprintf("%g", tk)
		b := c.box(x, ty, stripFS, "middle", s)
		b.X0, b.X1 = b.X0-3, b.X1+3
		if !c.free(b) {
			continue
		}
		fmt.Fprintf(&c.sb, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`, x, x, axis, axis+4, svgFrame)
		c.text(x, ty, stripFS, "middle", svgMuted, "", s)
	}
	y = ty + 6
	// Legend (wraps).
	type item struct{ shape, text string }
	items := []item{{"tuned", "tuned (± " + fmt.Sprintf("%.2f", sm.AimHz/1e6) + " MHz aim)"}, {"ok", "OK: band covered"}, {"shorten", "shorten"},
		{"lengthen", "lengthen"}, {"bad", "bad / no resonance"}, {"ring", "statistical outlier"}, {"win", "acceptance window"}}
	if sm.AimHz > 0 && (!wok || math.Abs(sm.Window.Hz-sm.AimHz) > 1e3) {
		items = append(items, item{"aim", "TUNE aim"})
	}
	items = append(items, item{"mean", "lot mean"})
	lx, ly := l, y+12
	for _, it := range items {
		w := 18 + textW(it.text, stripFS) + 10
		if lx+w > W-r {
			lx, ly = l, ly+15
		}
		cx, cy := lx+6, ly-3.5
		switch it.shape {
		case "ring":
			fmt.Fprintf(&c.sb, `<circle cx="%.1f" cy="%.1f" r="6" fill="none" stroke="%s" stroke-width="1.5"/>`, cx, cy, svgInk)
		case "win":
			fmt.Fprintf(&c.sb, `<rect x="%.1f" y="%.1f" width="13" height="9" fill="%s" stroke="%s" stroke-width="1.2"/>`, cx-6.5, cy-4.5, stripWin, stripWinLn)
		case "aim":
			fmt.Fprintf(&c.sb, `<rect x="%.1f" y="%.1f" width="13" height="4" fill="%s"/>`, cx-6.5, cy-2, stripAim)
		case "mean":
			fmt.Fprintf(&c.sb, `<path d="M%.1f %.1f l6 6 l-6 6 l-6 -6 z" fill="%s"/>`, cx, cy-6, svgInk)
		default:
			stripMarker(&c.sb, it.shape, cx, cy)
		}
		c.text(lx+16, ly, stripFS, "start", svgInk, "", it.text)
		lx += w
	}
	H := math.Ceil(ly + 5)
	head := fmt.Sprintf(`<svg class="plot spread" viewBox="0 0 %.0f %.0f" width="%.1fmm" height="%.1fmm" role="img" aria-label="Resonance of each antenna" font-family="Helvetica, Arial, sans-serif">`, W, H, wmm, H/4)
	return head + c.sb.String() + `</svg>`, c.boxes, W, H
}
