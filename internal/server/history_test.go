package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func hent(day int, res, worst, q, r float64) HistoryEntry {
	t := time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC)
	return HistoryEntry{Time: t, Measured: t, Kind: "manual", Profile: "869", Band: "869", Range: "779–959 MHz", Source: "live",
		Target: 869e6, ResFreq: res, WorstSWR: worst, Q: q, ResR: r, Cal: "calibrated", Key: fmt.Sprint(day)}
}

func TestDetectDrift(t *testing.T) {
	codes := func(r *DriftReport) string {
		var c []string
		for _, w := range r.Warnings {
			c = append(c, w.Code)
		}
		return strings.Join(c, ",")
	}
	if detectDrift(nil) != nil {
		t.Error("empty history")
	}
	first := detectDrift([]HistoryEntry{hent(1, 868e6, 1.4, 14, 45)})
	if first.Previous != nil || !strings.Contains(first.Line, "First logged") || len(first.Warnings) != 0 {
		t.Errorf("first: %+v", first)
	}
	stable := []HistoryEntry{hent(1, 868e6, 1.40, 14, 45), hent(2, 868.4e6, 1.42, 13.5, 46), hent(3, 867.8e6, 1.38, 14.2, 44)}
	r := detectDrift(append(stable, hent(4, 868.3e6, 1.45, 13.8, 47)))
	if len(r.Warnings) != 0 || !strings.Contains(r.Line, "no drift") || !strings.Contains(r.Line, "868.30") || r.Earlier != 3 {
		t.Errorf("small changes must not warn: %s %+v", codes(r), r)
	}
	// Resonance moved by 4 MHz.
	r = detectDrift(append(stable, hent(4, 872.1e6, 1.45, 14, 45)))
	if codes(r) != "resonance" || !strings.Contains(r.Warnings[0].Text, "up by 4.1 MHz") {
		t.Errorf("shift up: %s %+v", codes(r), r.Warnings)
	}
	r = detectDrift(append(stable, hent(4, 864e6, 1.45, 14, 45)))
	if codes(r) != "resonance" || !strings.Contains(r.Warnings[0].Text, "down by") {
		t.Errorf("shift down: %s", codes(r))
	}
	// SWR worse: 1.4 → 2.3 (grade OK → FAIL is "bad").
	r = detectDrift(append(stable, hent(4, 868e6, 2.3, 14, 45)))
	if codes(r) != "swr" || r.Warnings[0].Level != "bad" || !strings.Contains(r.Line, "drift: swr got worse") {
		t.Errorf("swr: %s %+v", codes(r), r)
	}
	// Water in the coax: broader dip and higher R.
	r = detectDrift(append(stable, hent(4, 868.2e6, 1.48, 8, 68)))
	if codes(r) != "loss" || !strings.Contains(r.Warnings[0].Text, "Q 14 → 8") || !strings.Contains(r.Warnings[0].Text, "45 → 68 Ω") || !strings.Contains(r.Warnings[0].Text, "Water in the coax") {
		t.Errorf("loss: %s %+v", codes(r), r.Warnings)
	}
	// Only the same profile/band is compared.
	other := hent(4, 433.9e6, 1.2, 20, 40)
	other.Profile, other.Band = "433", "433"
	r = detectDrift(append(stable, other))
	if r.Previous != nil || len(r.Warnings) != 0 {
		t.Errorf("433 must not be compared with 869: %+v", r)
	}
	// A different calibration is pointed out next to a warning.
	last := hent(4, 875e6, 1.4, 14, 45)
	last.Cal = "not calibrated"
	r = detectDrift(append(stable, last))
	if codes(r) != "resonance,cal" {
		t.Errorf("cal note: %s", codes(r))
	}
}

func TestHistoryAPI(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var hv HistoryView
	if c := call(t, g.ts, "POST", "/api/antennas/whip/history", "", &hv); c != 409 {
		t.Errorf("log without a sweep: %d", c)
	}
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	if c := call(t, g.ts, "POST", "/api/antennas/whip/history", "", &hv); c != 200 || len(hv.History) != 1 || hv.Drift == nil {
		t.Fatalf("log: %d %+v", c, hv)
	}
	e := hv.History[0]
	if e.Band != "869" || e.Profile != "869" || e.ResFreq < 850e6 || e.ResFreq > 865e6 || e.WorstSWR <= 1 || e.Q <= 0 || e.Source != "live" || e.Cal == "" {
		t.Errorf("entry: %+v", e)
	}
	// The same sweep is not logged twice (sheet after a manual log).
	if c := call(t, g.ts, "POST", "/api/sheet", `{"antenna":{"name":"whip","type":"whip"}}`, nil); c != 200 {
		t.Fatalf("sheet: %d", c)
	}
	call(t, g.ts, "GET", "/api/antennas/whip/history", "", &hv)
	if len(hv.History) != 1 {
		t.Fatalf("duplicate entry: %d", len(hv.History))
	}
	// A detuned antenna later: the sheet and the report show the drift.
	call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"869tuned"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	var rep map[string]string
	call(t, g.ts, "POST", "/api/sheet", `{"antenna":{"name":"whip","type":"whip"}}`, &rep)
	html, _ := os.ReadFile(g.dir + "/reports/" + rep["file"])
	if !strings.Contains(string(html), "Previous measurement") || !strings.Contains(string(html), "Resonance moved") {
		t.Errorf("sheet history line missing")
	}
	call(t, g.ts, "GET", "/api/antennas/whip/history", "", &hv)
	if len(hv.History) != 2 || hv.Drift == nil || len(hv.Drift.Warnings) == 0 || hv.History[1].Kind != "sheet" {
		t.Fatalf("after sheet: %+v", hv)
	}
	var a AntennaInfo
	call(t, g.ts, "GET", "/api/antennas/whip", "", &a)
	if a.Type != "whip" || len(a.History) != 2 {
		t.Errorf("record keeps details and history: %+v", a)
	}
	call(t, g.ts, "POST", "/api/report", `{"name":"whip","profiles":["869"]}`, &rep)
	html, _ = os.ReadFile(g.dir + "/reports/" + rep["file"])
	if !strings.Contains(string(html), "Antenna history:") || !strings.Contains(string(html), "Previous measurement") {
		t.Error("report history line missing")
	}
	// Report of an antenna that has no record yet creates one.
	call(t, g.ts, "POST", "/api/report", `{"name":"new one","profiles":["869"]}`, &rep)
	call(t, g.ts, "GET", "/api/antennas/new%20one/history", "", &hv)
	if len(hv.History) != 1 || hv.History[0].Kind != "report" {
		t.Errorf("report entry: %+v", hv)
	}
	// CSV export and undo.
	resp, err := http.Get(g.ts.URL + "/api/antennas/whip/history.csv")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 3 || !strings.HasPrefix(lines[0], "measured,") || !strings.Contains(resp.Header.Get("Content-Disposition"), "whip-history.csv") {
		t.Errorf("csv: %q", b)
	}
	call(t, g.ts, "POST", "/api/antennas/whip/history/undo", "", &hv)
	if len(hv.History) != 1 {
		t.Errorf("undo: %d", len(hv.History))
	}
	var list []AntennaListEntry
	call(t, g.ts, "GET", "/api/antennas", "", &list)
	if len(list) != 2 {
		t.Errorf("antenna list: %+v", list)
	}
}
