package server

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// A 1×1 PNG.
const tinyPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func TestInterpAt(t *testing.T) {
	f := []float64{1, 2, 4}
	y := []float64{10, 20, 40}
	for _, c := range []struct{ f, want float64 }{{1, 10}, {1.5, 15}, {3, 30}, {4, 40}} {
		if v, ok := interpAt(f, y, c.f); !ok || math.Abs(v-c.want) > 1e-9 {
			t.Errorf("interp(%g) = %g %v", c.f, v, ok)
		}
	}
	if _, ok := interpAt(f, y, 0.5); ok {
		t.Error("outside the sweep accepted")
	}
}

func TestSheetFigures(t *testing.T) {
	g := newRig(t, "869", false, false)
	var res Result
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	if c := call(t, g.ts, "POST", "/api/sweep", "", &res); c != 200 {
		t.Fatal(c)
	}
	figs, verdict, _ := sheetFigures(&res)
	keys := map[string]string{}
	for _, f := range figs {
		keys[strings.SplitN(f.K, " ", 2)[0]] = f.V
	}
	for _, k := range []string{"Resonance", "SWR", "Worst", "Return", "Mismatch", "Z", "Q"} {
		if keys[k] == "" {
			t.Errorf("figure %s missing (%v)", k, keys)
		}
	}
	if verdict != "PASS" && verdict != "OK" && verdict != "FAIL" && verdict != "CHECK" {
		t.Errorf("verdict %q", verdict)
	}
	// Identify sweeps have no band: N/A verdict, figures at the best match.
	res.Config.Identify, res.Summary.Bands = true, nil
	if _, v, _ := sheetFigures(&res); v != "N/A" {
		t.Errorf("identify verdict %q", v)
	}
}

func TestAntennaSheet(t *testing.T) {
	g := newRig(t, "869", false, false)
	sheet := func(body string) (int, map[string]string) {
		var out map[string]string
		req, _ := http.NewRequest("POST", g.ts.URL+"/api/sheet", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if c, out := sheet(`{"antenna":{"name":"whip"}}`); c != 409 || !strings.Contains(out["error"], "sweep") {
		t.Fatalf("no sweep yet: %d %v", c, out)
	}
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	call(t, g.ts, "POST", "/api/ref", "", nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	call(t, g.ts, "POST", "/api/trim/whip-1/log", `{"target":869000000,"lengthMm":82}`, nil)
	for name, body := range map[string]string{
		"no name":   `{"antenna":{"name":"  "}}`,
		"bad photo": `{"antenna":{"name":"x","photo":"javascript:alert(1)"}}`,
		"quote":     `{"antenna":{"name":"x","photo":"data:image/png;base64,AA\" onload=\"x"}}`,
		"length":    `{"antenna":{"name":"x","lengthMm":-3}}`,
		"unknown":   `{"antenna":{"name":"x","colour":"red"}}`,
	} {
		if c, _ := sheet(body); c != 400 {
			t.Errorf("%s: status %d", name, c)
		}
	}
	a := AntennaInfo{Name: "whip-1", Type: "¼λ whip", Band: "869 MHz LoRa EU", Connector: "SMA male", LengthMm: 82,
		Ground: "50 × 50 mm PCB", Cable: "RG316, 10 cm", Maker: "AliExpress", Owner: "bench 2", Notes: "line one\nline <two>", Photo: tinyPNG}
	b, _ := json.Marshal(map[string]any{"antenna": a})
	c, out := sheet(string(b))
	if c != 200 || !strings.HasPrefix(out["url"], "api/reports/whip-1-sheet-") {
		t.Fatalf("sheet: %d %v", c, out)
	}
	resp, err := http.Get(g.ts.URL + "/" + out["url"])
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h := string(html)
	for _, want := range []string{"Antenna sheet", "whip-1", "¼λ whip", "869 MHz LoRa EU", "SIMULATED DATA", "Resonance", "SWR band centre",
		"SWR low edge", "SWR high edge", "Worst in band", "SWR &lt; 2 bandwidth", "Q (estimated)", "aria-label=\"SWR\"", "Smith chart",
		"Return loss (S11)", "Impedance: R solid, X dashed", "- - REF", "shaded: 869 MHz band", "SMA male", "82 mm", "RG316, 10 cm",
		"line &lt;two&gt;", `src="data:image/png;base64,`, "Trim log", "<td class=\"num\">82.0</td>", "paperpick", "#print", "github.com/pix/easyvna-web"} {
		if !strings.Contains(h, want) {
			t.Errorf("sheet lacks %q", want)
		}
	}
	// The record is remembered and listed.
	var got AntennaInfo
	if c := call(t, g.ts, "GET", "/api/antennas/whip-1", "", &got); c != 200 || got.Photo != tinyPNG || got.Connector != "SMA male" || got.Updated.IsZero() {
		t.Fatalf("antenna record: %d %+v", c, got)
	}
	var list []AntennaListEntry
	call(t, g.ts, "GET", "/api/antennas", "", &list)
	if len(list) != 1 || list[0].Name != "whip-1" || !list[0].HasPhoto {
		t.Fatalf("list %+v", list)
	}
	if c := call(t, g.ts, "GET", "/api/antennas/nope", "", nil); c != 404 {
		t.Fatalf("missing antenna: %d", c)
	}
	// Saved with the reports, and part of a backup.
	var reps []ReportInfo
	call(t, g.ts, "GET", "/api/reports", "", &reps)
	if len(reps) != 1 || !strings.HasPrefix(reps[0].File, "whip-1-sheet-") {
		t.Fatalf("reports %+v", reps)
	}
	bk, err := g.s.store.Export("t", time.Now())
	if err != nil || !strings.Contains(string(bk), `"antennas/whip-1.json"`) {
		t.Fatalf("backup lacks the antenna record: %v", err)
	}
}

func TestSheetHoldOverlay(t *testing.T) {
	// Envelope series appear in the SWR plot when HOLD has positions.
	r := &Result{Freq: []float64{1e6, 2e6}, Derived: rf.Derived{SWR: []float64{1.2, 1.4}}}
	p := svgPlot{Wmm: 100, Hmm: 40, Freq: r.Freq, Series: []svgSeries{{Y: []float64{1.1, 1.2}, Color: svgHold, Dash: "2 3"}, {Y: r.Derived.SWR, Color: svgTrace}},
		YMin: 1, YMax: 3, YTicks: []float64{1, 2, 3}, YFmt: func(v float64) string { return "" }}.render()
	if strings.Count(string(p), "<polyline") != 2 || !strings.Contains(string(p), svgHold) {
		t.Fatal("hold series not drawn")
	}
}
