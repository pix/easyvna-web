package server

import (
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"testing"

	"github.com/pix/easyvna-web/internal/rf"
)

// lotBatch is a batch of n simulated whips spread around 866 MHz; from 8
// antennas on, the last one is an outlier far below the others.
func lotBatch(n int, spread float64) *Batch {
	b := testBatch()
	b.LengthMm = 82
	for i := 0; i < n; i++ {
		f := 866e6 + spread*float64((i*7)%5-2)/2
		if n >= 8 && i == n-1 {
			f = 846e6
		}
		addAnt(b, f, 47, 13)
	}
	return b
}

func sheetServer(t *testing.T) *Server {
	t.Helper()
	return New(Options{Store: NewStoreBackend(NewMemBackend(nil), "test"), AppName: "EasyVNA Web test"})
}

// The batch sheet is two pages: the lot on the antenna sheet's layout
// (every antenna's curve and the lot average), then the statistics.
func TestBatchSheetTwoPages(t *testing.T) {
	s := sheetServer(t)
	long := strings.Repeat("small print ", 20)[:180] + " <b>x</b> <u>u</u>"
	for _, n := range []int{5, 8, 20} {
		b := lotBatch(n, 1.5e6)
		b.Link = "https://example.com/antenna?id=42"
		b.Photo = "data:image/png;base64,iVBORw0KGgo="
		b.PhotoCaption, b.LinkNote = cleanSmallPrint(long), cleanSmallPrint(long)
		b.Antennas[1].Label = "SN 0042"
		html, err := s.buildBatchSheet("test", b)
		if err != nil {
			t.Fatal(err)
		}
		h := string(html)
		if c := strings.Count(h, `<div class="paper page `); c != 2 {
			t.Fatalf("n=%d: %d pages", n, c)
		}
		p1, p2, br := strings.Index(h, `class="paper page p1"`), strings.Index(h, `class="paper page p2"`), strings.Index(h, `class="pgbreak"`)
		if !(p1 > 0 && p1 < br && br < p2) {
			t.Fatalf("n=%d: page order %d %d %d", n, p1, br, p2)
		}
		page1, page2 := h[p1:br], h[p2:]
		// Page 1: one SWR trace per antenna and the average, in every plot.
		for _, cls := range []string{"lotswr", "lotrl", "lotsmith"} {
			i := strings.Index(page1, `class="plot `)
			_ = i
			re := regexp.MustCompile(`<svg class="plot [^"]*` + cls + `"[^>]*>(?s:.*?)</svg>`)
			m := re.FindString(page1)
			if m == "" {
				t.Fatalf("n=%d: no %s plot", n, cls)
			}
			if c := strings.Count(m, `data-ant="`); c != n {
				t.Fatalf("n=%d %s: %d antenna traces", n, cls, c)
			}
			if !strings.Contains(m, `data-avg="1"`) {
				t.Fatalf("n=%d %s: no average", n, cls)
			}
		}
		if c := strings.Count(page1, `<span class="lg" data-ant=`) + strings.Count(page1, `<span class="lg outl" data-ant=`); c != n {
			t.Fatalf("n=%d: %d legend items", n, c)
		}
		for _, a := range b.Antennas {
			if !strings.Contains(page1, ">"+a.Label) {
				t.Fatalf("n=%d: legend lacks %s", n, a.Label)
			}
		}
		v := s.batchViewOf(b)
		crit := template.HTMLEscapeString(v.Summary.Criteria)
		if !strings.Contains(page1, crit) || !strings.Contains(page2, template.HTMLEscapeString(v.Summary.Window.Text)) || !strings.Contains(page1, "lot average (bold)") {
			t.Fatalf("n=%d: criteria %q missing", n, crit)
		}
		// Small print under the photo and the link, escaped.
		// <b> renders as bold, other markup (<u>) stays text.
		if !strings.Contains(page1, `<div class="smallprint photocaption">`) || !strings.Contains(page1, `<div class="smallprint linknote">`) ||
			strings.Count(page1, "<b>x</b>") != 2 || strings.Count(page1, "&lt;u&gt;u&lt;/u&gt;") != 2 || strings.Contains(h, "<u>") {
			t.Fatalf("n=%d: small print", n)
		}
		// Page 2: the statistics, the strip and every antenna.
		for _, want := range []string{`svg class="plot spread"`, "Lot statistics", "SN 0042", "cover the band at SWR &lt; 2", template.HTMLEscapeString(v.Summary.Headline)} {
			if !strings.Contains(page2, want) {
				t.Fatalf("n=%d: page 2 lacks %q", n, want)
			}
		}
		if (n > 10) != strings.Contains(page2, `class="halves"`) {
			t.Fatalf("n=%d: compact table", n)
		}
	}
}

// The resonance strip: no two texts overlap, none leaves the drawing,
// every text is at least 10 units (7 pt).
func TestSpreadStripLayout(t *testing.T) {
	two := lotBatch(2, 6e6)
	eight := lotBatch(8, 1e6)
	eight.Antennas[2].Label = "SN-2026-000123 long label"
	wide := testBatch()
	for _, f := range []float64{852e6, 858e6, 861e6, 866e6, 869e6, 872e6, 877e6, 884e6, 869.2e6, 868.9e6, 869.1e6} {
		addAnt(wide, f, 47, 13)
	}
	far := lotBatch(6, 1e6)
	addAnt(far, 805e6, 47, 13) // far away (tight window: a narrow axis): an arrow at the edge
	tightAcc := rf.Acceptance{Tol: rf.TolTight}
	cases := []struct {
		name string
		b    *Batch
		acc  rf.Acceptance
	}{{"n=2", two, auto}, {"n=8 outlier", eight, auto}, {"wide", wide, auto}, {"wide tight", wide, tightAcc}, {"far", far, tightAcc}, {"n=1", lotBatch(1, 0), auto}}
	textRe := regexp.MustCompile(`<text ([^>]*)>`)
	fsRe := regexp.MustCompile(`font-size="([0-9.]+)"`)
	for _, c := range cases {
		v := batchView(c.b, nil, c.acc)
		svg, boxes, W, H := spreadLayout(v, 190)
		texts := textRe.FindAllStringSubmatch(svg, -1)
		if len(texts) != len(boxes) || len(boxes) < 5 {
			t.Fatalf("%s: %d texts, %d boxes", c.name, len(texts), len(boxes))
		}
		for _, tx := range texts {
			if !strings.Contains(tx[1], `class="lbl"`) {
				t.Fatalf("%s: text without class lbl: %s", c.name, tx[1])
			}
			var fs float64
			fmt.Sscanf(fsRe.FindStringSubmatch(tx[1])[1], "%g", &fs)
			if fs < 10 {
				t.Fatalf("%s: font %g", c.name, fs)
			}
		}
		for i, a := range boxes {
			if a.X0 < 0 || a.X1 > W || a.Y0 < 0 || a.Y1 > H {
				t.Fatalf("%s: %q outside %gx%g: %+v", c.name, a.Text, W, H, a)
			}
			for _, b := range boxes[i+1:] {
				if a.overlaps(b) {
					t.Fatalf("%s: %q overlaps %q", c.name, a.Text, b.Text)
				}
			}
		}
		// Every antenna with a resonance has a marker and, here, its label.
		for _, a := range v.Antennas {
			if a.Metrics.Found && !strings.Contains(svg, ">"+esc(shortLabel(a.Label, 12))) {
				t.Fatalf("%s: no label for %s", c.name, a.Label)
			}
		}
		switch c.name {
		case "n=2":
			if strings.Contains(svg, "CI") || strings.Contains(svg, "σ") || !strings.Contains(svg, "(n = 2)") {
				t.Fatalf("n=2 summary: %s", svg)
			}
		case "wide":
			if !strings.Contains(svg, "± σ") || !strings.Contains(svg, "Auto window") {
				t.Fatal("wide: σ bar / window label")
			}
		case "far":
			if !regexp.MustCompile(`#7 80[0-9]\.[0-9]{2}<`).MatchString(svg) {
				t.Fatal("far: no edge value")
			}
		}
	}
	// 3 or 4 antennas: the 95 % confidence interval of the mean.
	v := batchView(lotBatch(4, 1e6), nil, auto)
	if svg, _, _, _ := spreadLayout(v, 182); !strings.Contains(svg, "95 % CI, n = 4") {
		t.Fatal("n=4: no CI")
	}
}
