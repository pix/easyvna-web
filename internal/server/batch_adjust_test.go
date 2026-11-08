package server

import (
	"encoding/csv"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// adjBatch: #1 at 866 MHz, #2 at 862.7 MHz, and #2 again after a 0.6 mm
// cut, now at 868.5 MHz.
func adjBatch() *Batch {
	b := testBatch()
	b.LengthMm = 82
	addAnt(b, 866e6, 47, 13)
	addAnt(b, 862.7e6, 47, 13)
	addAnt(b, 868.5e6, 47, 13)
	a := &b.Antennas[2]
	a.AdjustedFrom, a.Change, a.TrimMm, a.Label = 2, "cut 0.6 mm", 0.6, "#2 adj 1"
	return b
}

func groupIDs(b *Batch) [][]int {
	var out [][]int
	for _, g := range b.lotGroups() {
		var ids []int
		for _, k := range g {
			ids = append(ids, b.Antennas[k].ID)
		}
		out = append(out, ids)
	}
	return out
}

func sameGroups(got [][]int, want string) bool {
	b, _ := json.Marshal(got)
	return string(b) == want
}

// Versions: chains, their checks, linking, unlinking and deleting.
func TestBatchVersionChains(t *testing.T) {
	b := adjBatch()
	if g := groupIDs(b); !sameGroups(g, "[[1],[2,3]]") || b.physical() != 2 || b.checkChains() != nil {
		t.Fatalf("groups %v", g)
	}
	if l := b.nextLabel(); l != "#3" {
		t.Fatalf("next label %q", l)
	}
	// Refused chains: itself, a missing capture, two later versions, a loop.
	for name, f := range map[string]func(c *Batch){
		"itself":  func(c *Batch) { c.Antennas[2].AdjustedFrom = 3 },
		"missing": func(c *Batch) { c.Antennas[2].AdjustedFrom = 9 },
		"branch":  func(c *Batch) { addAnt(c, 869e6, 47, 13); c.Antennas[3].AdjustedFrom = 2 },
		"loop":    func(c *Batch) { c.Antennas[1].AdjustedFrom = 3 },
	} {
		c := adjBatch()
		f(c)
		if err := c.checkChains(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		// A bad file never breaks the view: every capture still counts once.
		n := 0
		for _, g := range c.lotGroups() {
			n += len(g)
		}
		if n != len(c.Antennas) {
			t.Fatalf("%s: %d captures in groups", name, n)
		}
	}
	// Link a new capture to antenna #1, then to #2 (it follows #2's
	// current version), then unlink it.
	addAnt(b, 869e6, 47, 13) // ID 4, "#4"
	if _, err := b.linkVersion(4, 1, " bent  10° ", 0); err != nil {
		t.Fatal(err)
	}
	if a := b.Antennas[3]; a.AdjustedFrom != 1 || a.Label != "#1 adj 1" || a.Change != "bent 10°" || !sameGroups(groupIDs(b), "[[1,4],[2,3]]") {
		t.Fatalf("linked: %+v %v", a, groupIDs(b))
	}
	if _, err := b.linkVersion(4, 1, "", 0); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("same antenna twice: %v", err)
	}
	if _, err := b.linkVersion(4, 2, "cut 0.4 mm", 0.4); err != nil || !sameGroups(groupIDs(b), "[[1],[2,3,4]]") || b.Antennas[3].AdjustedFrom != 3 {
		t.Fatalf("relink: %v %v", err, groupIDs(b))
	}
	if b.Antennas[3].Label != "#2 adj 2" {
		t.Fatalf("label %q", b.Antennas[3].Label)
	}
	if _, err := b.linkVersion(2, 4, "", 0); err == nil {
		t.Fatal("a version of its own later version")
	}
	if _, err := b.linkVersion(4, 9, "", 0); err == nil {
		t.Fatal("missing antenna")
	}
	if _, err := b.linkVersion(4, 1, "", math.NaN()); err == nil {
		t.Fatal("NaN trim")
	}
	// Unlink the middle version: the chain closes around it.
	c := cloneBatch(b)
	if _, err := c.unlinkVersion(3); err != nil || !sameGroups(groupIDs(c), "[[1],[2,4],[3]]") || c.Antennas[2].Change != "" || c.checkChains() != nil {
		t.Fatalf("unlink middle: %v %v", err, groupIDs(c))
	}
	// Unlink the first version: the later ones stay one antenna.
	c = cloneBatch(b)
	if _, err := c.unlinkVersion(2); err != nil || !sameGroups(groupIDs(c), "[[1],[2],[3,4]]") || c.Antennas[2].AdjustedFrom != 0 || c.Antennas[2].TrimMm != 0 {
		t.Fatalf("unlink first: %v %v", err, groupIDs(c))
	}
	if _, err := c.unlinkVersion(1); err == nil {
		t.Fatal("unlink a lone capture")
	}
	// Delete the middle version: the next one follows #2 with both trims.
	c = cloneBatch(b)
	if _, err := c.removeCapture(3); err != nil || !sameGroups(groupIDs(c), "[[1],[2,4]]") {
		t.Fatalf("delete middle: %v %v", err, groupIDs(c))
	}
	if a := c.Antennas[2]; a.AdjustedFrom != 2 || math.Abs(a.TrimMm-1.0) > 1e-9 || a.Change != "cut 0.6 mm; cut 0.4 mm" {
		t.Fatalf("merged: %+v", a)
	}
	// Delete the first version: its next version is the antenna as received.
	c = cloneBatch(b)
	if _, err := c.removeCapture(2); err != nil || !sameGroups(groupIDs(c), "[[1],[3,4]]") || c.Antennas[1].AdjustedFrom != 0 || c.Antennas[1].Change != "" {
		t.Fatalf("delete first: %v %v", err, groupIDs(c))
	}
	if c.checkChains() != nil || c.physical() != 2 {
		t.Fatal(c.checkChains())
	}
}

// The lot counts physical antennas by their current version, compares the
// lot as received, and learns MHz/mm from the trims.
func TestBatchVersionStats(t *testing.T) {
	b := adjBatch()
	v := batchView(b, nil, auto)
	sm := v.Summary
	if sm.N != 2 || sm.Captures != 3 || sm.Adjusted != 1 || sm.Stats[0].N != 2 || sm.NRes != 2 {
		t.Fatalf("n %d captures %d adjusted %d stat n %d", sm.N, sm.Captures, sm.Adjusted, sm.Stats[0].N)
	}
	var nos []string
	var cur []bool
	for _, a := range v.Antennas {
		nos, cur = append(nos, a.No), append(cur, a.Current)
	}
	if strings.Join(nos, " ") != "1 2a 2b" || cur[0] != true || cur[1] != false || cur[2] != true {
		t.Fatalf("no %v current %v", nos, cur)
	}
	// Statistics over 866 and 868.5 MHz: the superseded 862.7 is not in them.
	if st := sm.Stats[0]; st.Min < 865 || st.Max > 870 {
		t.Fatalf("resonance %+v", st)
	}
	if len(v.AvgSWR) != len(b.Freq) || len(v.EnvMin) != len(b.Freq) {
		t.Fatal("average / envelope")
	}
	// Every capture has its own status; the superseded one is still judged.
	if v.Antennas[1].Metrics.Tuning.Status == "" || v.Antennas[2].Metrics.Tuning.Status != "tuned" {
		t.Fatalf("%+v", v.Antennas[2].Metrics.Tuning)
	}
	// Before / after.
	if sm.AsReceived == nil || sm.Current == nil || sm.AsReceived.Tuned != 0 || sm.Current.Tuned != 1 || sm.AsReceived.N != 2 {
		t.Fatalf("as received %+v current %+v", sm.AsReceived, sm.Current)
	}
	if !strings.Contains(joined(sm), "As received → after adjustment (#2: 1 of 2 antennas): 2 → 2 of 2 cover the band at SWR < 2.0, 0 → 1 on the TUNE aim, worst SWR ") {
		t.Fatalf("conclusion: %s", joined(sm))
	}
	// The step: resonance +5.8 MHz for 0.6 mm.
	if len(sm.Steps) != 1 || v.Antennas[2].Step == nil {
		t.Fatalf("steps %+v", sm.Steps)
	}
	st := sm.Steps[0]
	if !st.Found || st.ID != 3 || st.From != 2 || st.No != "2b" || math.Abs(st.DRes-5.8e6) > 1e6 || math.Abs(st.MHzPerMm-st.DRes/1e6/0.6) > 1e-9 {
		t.Fatalf("step %+v", st)
	}
	for _, want := range []string{"#2: cut 0.6 mm → resonance +5.", "SWR@target ", "margin "} {
		if !strings.Contains(st.Text, want) {
			t.Fatalf("step text %q lacks %q", st.Text, want)
		}
	}
	// Learnt: about 9.7 MHz/mm (pulled a little towards f/L = 10.6), and the
	// trim advice of every antenna uses it.
	if l := sm.Learnt; l == nil || l.Adjustments != 1 || l.MHzPerMm < 9 || l.MHzPerMm > 10.6 || !strings.Contains(l.Text, "from 1 adjustment") {
		t.Fatalf("learnt %+v", sm.Learnt)
	}
	if !strings.Contains(sm.Slope, "learnt from 1 adjustment in this batch") || v.Antennas[0].Metrics.Tuning.Method != "fit" {
		t.Fatalf("slope %q method %q", sm.Slope, v.Antennas[0].Metrics.Tuning.Method)
	}
	// Unlinked, the same captures are three antennas.
	c := cloneBatch(b)
	c.Antennas[2].AdjustedFrom = 0
	if s2 := batchView(c, nil, auto).Summary; s2.N != 3 || s2.Adjusted != 0 || s2.Learnt != nil || s2.AsReceived != nil || strings.Contains(joined(s2), "As received") {
		t.Fatalf("unlinked %+v", s2)
	}
	// A bend (no trim) is not a MHz/mm sample; trims of two antennas are.
	c = testBatch()
	c.LengthMm = 82
	addAnt(c, 860e6, 47, 13)
	addAnt(c, 864e6, 47, 13) // 2: #1 bent
	addAnt(c, 874e6, 47, 13) // 3: #1 then cut 1 mm
	addAnt(c, 858e6, 47, 13) // 4
	addAnt(c, 863e6, 47, 13) // 5: #4 cut 0.5 mm
	c.Antennas[1].AdjustedFrom, c.Antennas[1].Change = 1, "bent 10°"
	c.Antennas[2].AdjustedFrom, c.Antennas[2].TrimMm = 2, 1
	c.Antennas[4].AdjustedFrom, c.Antennas[4].TrimMm = 4, 0.5
	v = batchView(c, nil, auto)
	if l := v.Summary.Learnt; l == nil || l.Adjustments != 2 || l.MHzPerMm < 9.5 || l.MHzPerMm > 10.6 {
		t.Fatalf("pooled %+v", v.Summary.Learnt)
	}
	if v.Summary.N != 2 || len(v.Summary.Steps) != 3 || v.Summary.Steps[0].MHzPerMm != 0 || v.Summary.Steps[0].Change != "bent 10°" {
		t.Fatalf("steps %+v", v.Summary.Steps)
	}
	// Trims that lowered the resonance teach nothing (and say so).
	c.Antennas[2].TrimMm, c.Antennas[4].TrimMm = -1, -0.5
	if l := batchView(c, nil, auto).Summary.Learnt; l == nil || l.MHzPerMm != 0 || !strings.Contains(l.Text, "did not rise") {
		t.Fatalf("inconsistent %+v", l)
	}
	// A superseded version is never a statistical outlier of the lot.
	c = lotBatch(6, 1e6)
	addAnt(c, 846e6, 47, 13)
	if o := batchView(c, nil, auto).Summary.Stats[0].Outliers; len(o) != 1 || o[0] != 7 {
		t.Fatalf("outlier %v", o)
	}
	addAnt(c, 866e6, 47, 13)
	c.Antennas[7].AdjustedFrom = 7
	v = batchView(c, nil, auto)
	if o := v.Summary.Stats[0].Outliers; len(o) != 0 || v.Summary.N != 7 {
		t.Fatalf("outliers after the fix %v", o)
	}
	for _, a := range v.Antennas {
		if len(a.Metrics.Outliers) > 0 {
			t.Fatalf("%s flagged", a.Label)
		}
	}
}

// The API: an adjusted version captured from the simulator after a trim,
// linking and unlinking, delete + undo, Measure ×N, CSV and JSON round
// trip, and batches without versions.
func TestBatchVersionAPI(t *testing.T) {
	// Not the lot869 scenario: its units are numbered per process and other
	// tests expect the lot from its first unit.
	ts, _, bench := newBenchServer(t, "869")
	units := []string{"869tuned", "869tuned", "869", "869tuned"}
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var v BatchView
	if c := call(t, ts, "POST", "/api/batches", `{"name":"869 adjusted","lengthMm":82}`, &v); c != 200 {
		t.Fatal(c)
	}
	id := v.ID
	type capResp struct {
		Message string    `json:"message"`
		ID      int       `json:"id"`
		Batch   BatchView `json:"batch"`
	}
	var cr capResp
	for i := 1; i <= 3; i++ {
		_ = bench.SetAntenna(units[i-1])
		if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{}`, &cr); c != 200 {
			t.Fatal(c)
		}
	}
	// #3 is on the cable: trim the simulated element 0.5 mm and capture it
	// as an adjusted version of #3.
	var errOut map[string]string
	if c := call(t, ts, "POST", "/api/mock/action", `{"action":"trim","mm":0.5}`, nil); c != 200 {
		t.Fatal(c)
	}
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{"adjust":3,"change":"cut 0.5 mm","trimMm":0.5}`, &cr); c != 200 || cr.ID != 4 {
		t.Fatalf("%d %s", c, cr.Message)
	}
	b := cr.Batch
	if b.Summary.N != 3 || b.Summary.Captures != 4 || b.Next != "#4" || !strings.Contains(cr.Message, "adjusted version") {
		t.Fatalf("n %d captures %d next %q", b.Summary.N, b.Summary.Captures, b.Next)
	}
	a := b.Antennas[3]
	if a.ID != 4 || a.Label != "#3 adj 1" || a.AdjustedFrom != 3 || a.No != "3b" || !a.Current || b.Antennas[2].Current || a.Step == nil {
		t.Fatalf("version %+v", a)
	}
	// The simulated element: f = k/(L+δ), 82 + 6 mm: about 9.9 MHz/mm.
	if d := a.Step.DRes / 1e6; d < 3 || d > 7.5 { // 0.5 mm × 9.9 MHz/mm, with the simulator's noise and drift
		t.Fatalf("Δf %.2f MHz", d)
	}
	if l := b.Summary.Learnt; l == nil || l.MHzPerMm < 7 || l.MHzPerMm > 13 {
		t.Fatalf("learnt %+v", b.Summary.Learnt)
	}
	// Redo keeps it a version.
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{"replace":4}`, &cr); c != 200 || cr.Batch.Antennas[3].AdjustedFrom != 3 || cr.Batch.Summary.N != 3 {
		t.Fatalf("redo %d", c)
	}
	// Adjusting the first version adds after the current one.
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{"adjust":3,"trimMm":0.2}`, &cr); c != 200 || cr.ID != 5 {
		t.Fatal(c)
	}
	if a := cr.Batch.Antennas[4]; a.AdjustedFrom != 4 || a.Label != "#3 adj 2" || a.No != "3c" || cr.Batch.Summary.N != 3 {
		t.Fatalf("chain %+v", a)
	}
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{"adjust":42}`, &errOut); c != 409 {
		t.Fatal(c)
	}
	// A new antenna, marked as the same as #1, then unlinked.
	_ = bench.SetAntenna(units[3])
	if c := call(t, ts, "POST", "/api/batches/"+id+"/capture", `{}`, &cr); c != 200 || cr.ID != 6 || cr.Batch.Antennas[5].Label != "#4" {
		t.Fatalf("%d %+v", c, cr.Batch.Antennas)
	}
	var dr struct {
		Message string    `json:"message"`
		Batch   BatchView `json:"batch"`
	}
	if c := call(t, ts, "PUT", "/api/batches/"+id+"/antennas/6", `{"adjustedFrom":1,"change":"re-soldered"}`, &dr); c != 200 {
		t.Fatal(c)
	}
	if dr.Batch.Summary.N != 3 || dr.Batch.Antennas[1].ID != 6 || dr.Batch.Antennas[1].Label != "#1 adj 1" || dr.Batch.Antennas[1].Change != "re-soldered" {
		t.Fatalf("same as: %+v", dr.Batch.Antennas[1])
	}
	if c := call(t, ts, "PUT", "/api/batches/"+id+"/antennas/6", `{"adjustedFrom":1}`, &errOut); c != 409 || !strings.Contains(errOut["error"], "already") {
		t.Fatalf("%d %v", c, errOut)
	}
	if c := call(t, ts, "PUT", "/api/batches/"+id+"/antennas/6", `{"adjustedFrom":0}`, &dr); c != 200 || dr.Batch.Summary.N != 4 || dr.Batch.Antennas[5].Label != "#4" {
		t.Fatalf("%d %+v", c, dr.Batch.Antennas[5])
	}
	if c := call(t, ts, "PUT", "/api/batches/"+id+"/antennas/6", `{"change":"x"}`, &errOut); c != 409 {
		t.Fatal(c)
	}
	if c := call(t, ts, "PUT", "/api/batches/"+id+"/antennas/5", `{"change":"cut 0.2 mm, filed","trimMm":0.25}`, &dr); c != 200 {
		t.Fatal(c)
	}
	if a := dr.Batch.Antennas[4]; a.Change != "cut 0.2 mm, filed" || a.TrimMm != 0.25 {
		t.Fatalf("%+v", a)
	}
	// Delete the first version of #3: its next version is now the one as
	// received; Undo restores the chain.
	if c := call(t, ts, "DELETE", "/api/batches/"+id+"/antennas/3", "", &dr); c != 200 || dr.Batch.Summary.N != 4 || dr.Batch.Summary.Captures != 5 {
		t.Fatal(c)
	}
	if a := dr.Batch.Antennas[2]; a.ID != 4 || a.AdjustedFrom != 0 || a.Ver != 0 || a.Versions != 2 {
		t.Fatalf("after delete %+v", a)
	}
	if c := call(t, ts, "POST", "/api/batches/"+id+"/undo", "", &dr); c != 200 || dr.Batch.Summary.Captures != 6 || dr.Batch.Antennas[3].AdjustedFrom != 3 {
		t.Fatal(c)
	}
	// Measure ×N as an adjusted version.
	var st State
	if c := call(t, ts, "POST", "/api/measure", `{"n":2,"for":"batch","adjust":1,"change":"bent","trimMm":0}`, &st); c != 200 {
		t.Fatal(c)
	}
	if m := waitMeasure(t, ts, st.Measure.ID); m.Status != "done" || m.Captured != 7 {
		t.Fatalf("%+v", m)
	}
	if c := call(t, ts, "POST", "/api/measure", `{"n":2,"for":"batch","adjust":99}`, &errOut); c != 409 {
		t.Fatal(c)
	}
	call(t, ts, "GET", "/api/batches/"+id, "", &v)
	if a := v.Antennas[1]; a.ID != 7 || a.AdjustedFrom != 1 || a.Change != "bent" || a.Rep == nil || v.Summary.N != 4 {
		t.Fatalf("measured version %+v", a)
	}
	// CSV: the version columns, the adjustments and what was learnt.
	body, _ := get(t, ts, "/api/batches/"+id+"/csv")
	r := csv.NewReader(strings.NewReader(body))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Join(rows[1], ",")
	if !strings.Contains(head, "no,version,current,adjusted_from,change,trim_mm,delta_resonance_mhz") || rows[0][14] != "antennas" || rows[0][15] != "4" || rows[0][17] != "7" {
		t.Fatalf("csv head %v / %v", rows[0], head)
	}
	adj, learnt := 0, false
	for _, row := range rows {
		if row[0] == "4" && (row[len(rows[1])-4] != "3" || row[len(rows[1])-3] != "cut 0.5 mm" || row[len(rows[1])-2] != "0.50" || row[len(rows[1])-7] != "3b") {
			t.Fatalf("csv row %v", row)
		}
		if row[0] == "adjustment" {
			adj++
		}
		learnt = learnt || row[0] == "learnt"
	}
	if adj != 3 || !learnt {
		t.Fatalf("csv adjustments %d learnt %v", adj, learnt)
	}
	// JSON round trip.
	js, _ := get(t, ts, "/api/batches/"+id+"/json")
	var imp struct {
		Batch BatchView `json:"batch"`
	}
	if c := call(t, ts, "POST", "/api/batches/import", js, &imp); c != 200 {
		t.Fatal(c)
	}
	if imp.Batch.ID == id || imp.Batch.Summary.N != 4 || imp.Batch.Summary.Captures != 7 || len(imp.Batch.Summary.Steps) != 3 {
		t.Fatalf("import %+v", imp.Batch.Summary)
	}
	for i, a := range imp.Batch.Antennas {
		w := v.Antennas[i]
		if a.ID != w.ID || a.AdjustedFrom != w.AdjustedFrom || a.Change != w.Change || a.TrimMm != w.TrimMm || a.No != w.No || a.Label != w.Label {
			t.Fatalf("import %d: %+v vs %+v", i, a, w)
		}
	}
	// Refused: versions of a missing capture, in a loop, a bad trim.
	var m map[string]any
	_ = json.Unmarshal([]byte(js), &m)
	ants(m)[1]["adjustedFrom"] = 99
	out, _ := json.Marshal(m)
	if c := call(t, ts, "POST", "/api/batches/import", string(out), &errOut); c != 400 || !strings.Contains(errOut["error"], "not in this batch") {
		t.Fatalf("%d %v", c, errOut)
	}
	_ = json.Unmarshal([]byte(js), &m)
	ants(m)[2]["adjustedFrom"] = 5
	out, _ = json.Marshal(m)
	if c := call(t, ts, "POST", "/api/batches/import", string(out), &errOut); c != 400 || !strings.Contains(errOut["error"], "loop") {
		t.Fatalf("%d %v", c, errOut)
	}
	_ = json.Unmarshal([]byte(js), &m)
	ants(m)[3]["trimMm"] = 5000
	out, _ = json.Marshal(m)
	if c := call(t, ts, "POST", "/api/batches/import", string(out), &errOut); c != 400 || !strings.Contains(errOut["error"], "trim") {
		t.Fatalf("%d %v", c, errOut)
	}
	// A batch without versions (an older file) loads unchanged: every
	// capture is an antenna, and nothing new is written into it.
	old := lotBatch(4, 1e6)
	old.ID = "old-batch"
	data, _ := json.Marshal(BatchExport{Format: BatchFormat, Version: 1, Batch: old})
	if strings.Contains(string(data), "adjustedFrom") || strings.Contains(string(data), "trimMm") {
		t.Fatal("empty version fields are written")
	}
	if c := call(t, ts, "POST", "/api/batches/import", string(data), &imp); c != 200 || imp.Batch.Summary.N != 4 || imp.Batch.Summary.Captures != 4 ||
		imp.Batch.Summary.Adjusted != 0 || imp.Batch.Antennas[2].No != "3" || imp.Batch.Antennas[2].Label != "#3" {
		t.Fatalf("old batch %d %+v", c, imp.Batch.Summary)
	}
}

func ants(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, a := range m["batch"].(map[string]any)["antennas"].([]any) {
		out = append(out, a.(map[string]any))
	}
	return out
}
