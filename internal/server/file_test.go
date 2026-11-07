package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pix/easyvna-web/internal/rf"
)

func fileBody(t *testing.T, name, data string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"name": name, "data": data})
	return string(b)
}

func TestOpenMeasurementFile(t *testing.T) {
	g := newRig(t, "869", false, false)
	call(t, g.ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, g.ts, "POST", "/api/continuous", `{"on":true}`, nil)

	// A file written by EasyVNA itself for the 869 profile range.
	var live Result
	call(t, g.ts, "POST", "/api/continuous", `{"on":false}`, nil)
	call(t, g.ts, "POST", "/api/sweep", "", &live)
	call(t, g.ts, "POST", "/api/ref", "", nil) // REF of the 869 profile
	resp, err := http.Get(g.ts.URL + "/api/export?format=s1p")
	if err != nil {
		t.Fatal(err)
	}
	s1p, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var res Result
	if c := call(t, g.ts, "POST", "/api/file", fileBody(t, `C:\data\whip 869.s1p`, string(s1p)), &res); c != 200 {
		t.Fatalf("open: %d", c)
	}
	if res.Source == nil || res.Source.File != "whip 869.s1p" || res.Source.Profile != "869" || res.Config.Profile != "file" {
		t.Fatalf("source: %+v cfg %+v", res.Source, res.Config)
	}
	if len(res.Freq) != 401 || len(res.Summary.Bands) != 2 || res.Summary.Bands[0].Band.Name != "869" || res.CalApplied {
		t.Fatalf("analysis: %d points, bands %+v", len(res.Freq), res.Summary.Bands)
	}
	if res.Ref == nil || res.Ref.DF == nil || abs(*res.Ref.DF) > 0.5e6 {
		t.Errorf("a file of the REF sweep must compare to REF with Δf ≈ 0: %+v", res.Ref)
	}
	var st State
	call(t, g.ts, "GET", "/api/state", "", &st)
	if st.Continuous {
		t.Error("opening a file must stop LIVE")
	}
	var choices []ReportChoice
	for _, c := range st.ReportChoices {
		if c.File {
			choices = append(choices, c)
		}
	}
	if len(choices) != 1 || choices[0].Profile != "file" || !strings.Contains(choices[0].Label, "whip 869.s1p") {
		t.Errorf("report choice: %+v", st.ReportChoices)
	}

	// Export of the file measurement says where it came from.
	resp, _ = http.Get(g.ts.URL + "/api/export?format=s1p")
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(out), "Opened from file whip 869.s1p") || !strings.Contains(resp.Header.Get("Content-Disposition"), "whip_869-easyvna.s1p") {
		t.Errorf("export of a file: %s / %s", resp.Header.Get("Content-Disposition"), strings.SplitN(string(out), "\n", 4)[:3])
	}

	// Report and sheet label it.
	var rep map[string]string
	if c := call(t, g.ts, "POST", "/api/report", `{"name":"whip","profiles":["file"]}`, &rep); c != 200 {
		t.Fatalf("report: %d %v", c, rep)
	}
	html, _ := os.ReadFile(g.dir + "/reports/" + rep["file"])
	if !strings.Contains(string(html), "File whip 869.s1p (not live)") || !strings.Contains(string(html), "opened from the file") {
		t.Error("report does not label the file measurement")
	}
	if c := call(t, g.ts, "POST", "/api/sheet", `{"antenna":{"name":"whip"}}`, &rep); c != 200 {
		t.Fatalf("sheet: %d", c)
	}
	html, _ = os.ReadFile(g.dir + "/reports/" + rep["file"])
	if !strings.Contains(string(html), "FROM FILE · NOT LIVE") || strings.Contains(string(html), "NOT CALIBRATED") {
		t.Error("sheet does not label the file measurement")
	}

	// REF from a file goes to the current profile.
	var msg msgState
	if c := call(t, g.ts, "POST", "/api/ref", "", &msg); c != 200 || !strings.Contains(msg.Message, "whip 869.s1p") || msg.State.Refs["869"].File != "whip 869.s1p" {
		t.Fatalf("REF from file: %d %+v", c, msg)
	}
	call(t, g.ts, "POST", "/api/sweep", "", &live)
	if live.Source != nil || live.Ref == nil || live.Ref.RefFile != "whip 869.s1p" {
		t.Errorf("live sweep after a file: source %+v ref %+v", live.Source, live.Ref)
	}

	// A wide CSV becomes an identify view; errors are explained.
	var b strings.Builder
	freq, s11 := []float64{}, []complex128{}
	for f := 300e6; f <= 1000e6; f += 5e6 {
		freq = append(freq, f)
		s11 = append(s11, rf.GammaFromZ(complex(50, (f-868e6)/1e6)))
	}
	_ = rf.WriteCSV(&b, freq, s11, nil)
	if c := call(t, g.ts, "POST", "/api/file", fileBody(t, "wide.csv", b.String()), &res); c != 200 || !res.Config.Identify || len(res.Summary.Dips) != 1 {
		t.Errorf("wide csv: %d identify=%v dips %+v", c, res.Config.Identify, res.Summary.Dips)
	}
	var e map[string]string
	if c := call(t, g.ts, "POST", "/api/file", fileBody(t, "bad.s1p", "# MHz Y RI R 50\n1 0 0\n"), &e); c != 400 || !strings.Contains(e["error"], "bad.s1p") {
		t.Errorf("bad file: %d %v", c, e)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
