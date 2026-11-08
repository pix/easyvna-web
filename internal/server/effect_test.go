package server

import (
	stdhtml "html"
	"os"
	"strings"
	"testing"
)

// With a REF stored, every result says what the change did per band, in
// the main view, in TUNE, on the sheet and in the report.
func TestPerBandEffect(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	call(t, g.ts, "POST", "/api/ref", "", nil)
	call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"869tuned"}`, nil)
	var res Result
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if res.Ref == nil || len(res.Ref.Bands) == 0 || res.Ref.Bands[0].Label != "869" || res.Ref.Bands[0].DF == nil || *res.Ref.Bands[0].DF < 8e6 {
		t.Fatalf("869 effect: %+v", res.Ref)
	}
	if !strings.HasPrefix(res.Ref.Effect, "869: +1") || !strings.Contains(res.Ref.Effect, "SWR ") {
		t.Errorf("effect text: %q", res.Ref.Effect)
	}
	var rep map[string]string
	call(t, g.ts, "POST", "/api/sheet", `{"antenna":{"name":"w"}}`, &rep)
	sheet, _ := os.ReadFile(g.dir + "/reports/" + rep["file"])
	call(t, g.ts, "POST", "/api/report", `{"name":"w","profiles":["869"]}`, &rep)
	report, _ := os.ReadFile(g.dir + "/reports/" + rep["file"])
	for name, html := range map[string][]byte{"sheet": sheet, "report": report} {
		if h := stdhtml.UnescapeString(string(html)); !strings.Contains(h, "Change vs REF") || !strings.Contains(h, "869: +1") {
			t.Errorf("%s: per-band line missing", name)
		}
	}
	// TUNE results carry it too.
	call(t, g.ts, "POST", "/api/tune", `{"on":true}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if res.Tune == nil || res.Ref == nil || !strings.HasPrefix(res.Ref.Effect, "869:") {
		t.Errorf("TUNE effect: %+v", res.Ref)
	}
	call(t, g.ts, "POST", "/api/tune", `{"on":false}`, nil)
	// Identify sweeps list the dips.
	call(t, g.ts, "POST", "/api/mock/antenna", `{"name":"dual"}`, nil)
	call(t, g.ts, "POST", "/api/profile", `{"id":"wide"}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", nil)
	call(t, g.ts, "POST", "/api/ref", "", nil)
	g.s.opt.MockCut(2)
	call(t, g.ts, "POST", "/api/sweep", "", &res)
	if res.Ref == nil || !strings.Contains(res.Ref.Effect, "433: +") {
		t.Errorf("wide effect: %+v", res.Ref)
	}
}
