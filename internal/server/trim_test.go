package server

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The trim calculator recommends a cautious first cut until it has learnt
// MHz/mm, in the API, the report and the sheet.
func TestTrimCautiousFirstCut(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	var ts TrimSession
	if c := call(t, g.ts, "POST", "/api/trim/whip/log", `{"lengthMm":82,"target":880000000}`, &ts); c != 200 {
		t.Fatalf("log: %d", c)
	}
	p := ts.Prediction
	if p == nil || !p.Cautious || p.CutMm <= 1 || p.FirstCutMm <= 0 || p.FirstCutMm > 0.6*p.CutMm || !strings.Contains(p.Advice, "First cut") {
		t.Fatalf("first log must be cautious: %+v", p)
	}
	checkDocs := func(want, notWant string) {
		t.Helper()
		var rep map[string]string
		call(t, g.ts, "POST", "/api/report", `{"name":"whip","trim":"whip"}`, &rep)
		b, _ := os.ReadFile(g.dir + "/reports/" + rep["file"])
		call(t, g.ts, "POST", "/api/sheet", `{"antenna":{"name":"whip"}}`, &rep)
		b2, _ := os.ReadFile(g.dir + "/reports/" + rep["file"])
		for name, html := range map[string]string{"report": string(b), "sheet": string(b2)} {
			if !strings.Contains(html, want) || (notWant != "" && strings.Contains(html, notWant)) {
				t.Errorf("%s: want %q, not %q", name, want, notWant)
			}
		}
	}
	checkDocs(fmt.Sprintf("first cut %.1f mm", p.FirstCutMm), "")
	// Cut what it says, log: now MHz/mm is learnt and the full cut is given.
	if c := call(t, g.ts, "POST", "/api/trim/whip/log", fmt.Sprintf(`{"cutMm":%.1f}`, p.FirstCutMm), &ts); c != 200 {
		t.Fatalf("second log: %d", c)
	}
	p = ts.Prediction
	if p.Cautious || p.Method != "fit" || p.FirstCutMm != p.CutMm {
		t.Fatalf("after one cut: %+v", p)
	}
	checkDocs(fmt.Sprintf("cut %.1f mm", p.CutMm), "first cut")
}
