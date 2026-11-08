package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

func newTestServer(t *testing.T, profiles bool) (*httptest.Server, *Server) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context) (vna.Device, error) {
		sim := mock.New(mock.Options{PointTime: time.Microsecond, Seed: 7, Profiles: profiles})
		return nanovnah.Open(ctx, sim, nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("ui")}},
		Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); <-done })
	return ts, s
}

func call(t *testing.T, ts *httptest.Server, method, path, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		b, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func TestSweepProfilesAndAnalysis(t *testing.T) {
	ts, _ := newTestServer(t, false)
	var res Result
	if c := call(t, ts, "POST", "/api/sweep", "", &res); c != 200 {
		t.Fatalf("sweep: %d", c)
	}
	if res.Config.Profile != "wide" || len(res.Freq) != 401 || len(res.Summary.Dips) != 2 {
		t.Fatalf("wide sweep: profile %s, %d points, dips %+v", res.Config.Profile, len(res.Freq), res.Summary.Dips)
	}
	if res.Summary.Dips[1].HarmonicN != 2 {
		t.Errorf("mock 2nd mode not flagged: %+v", res.Summary.Dips[1])
	}
	var pr struct {
		State State `json:"state"`
	}
	if c := call(t, ts, "POST", "/api/profile", `{"id":"869"}`, &pr); c != 200 || pr.State.Config.Start != 779e6 || pr.State.Config.Stop != 959e6 {
		t.Fatalf("profile: %d %+v", c, pr.State.Config)
	}
	call(t, ts, "POST", "/api/sweep", "", &res)
	if len(res.Summary.Bands) != 2 || res.Summary.Bands[0].Band.Name != "869" || res.Summary.Bands[0].Verdict == "" {
		t.Fatalf("869 bands: %+v", res.Summary.Bands)
	}
	if c := call(t, ts, "POST", "/api/profile", `{"id":"2400"}`, nil); c != 404 {
		t.Errorf("unknown profile: %d", c)
	}
	// Custom range switches to the custom profile with overlapping bands.
	var st State
	if c := call(t, ts, "POST", "/api/config", `{"start":850000000,"stop":950000000,"points":201}`, &st); c != 200 {
		t.Fatalf("config: %d", c)
	}
	if st.Config.Profile != "custom" || len(st.Config.Bands) != 3 {
		t.Errorf("custom: %+v", st.Config)
	}
	if c := call(t, ts, "POST", "/api/config", `{"start":9,"stop":5}`, nil); c != 400 {
		t.Errorf("invalid config accepted: %d", c)
	}
}

func TestCalibrationSaveAndAutoRecall(t *testing.T) {
	ts, _ := newTestServer(t, false) // stock firmware path: sweep + recall
	call(t, ts, "POST", "/api/profile", `{"id":"433"}`, nil)
	var cal CalState
	for _, step := range []string{"open", "short", "load", "done"} {
		body := "{}"
		if step == "open" {
			body = `{"fresh":true}`
		}
		if c := call(t, ts, "POST", "/api/cal/step/"+step, body, &cal); c != 200 {
			t.Fatalf("%s: %d", step, c)
		}
		if cal.Flow.Steps[step] != "done" {
			t.Fatalf("%s: flow %+v", step, cal.Flow)
		}
	}
	if !cal.Status.Applied || cal.Active == nil || cal.Active.Start != 388.92e6 {
		t.Fatalf("after done: %+v", cal)
	}
	if c := call(t, ts, "POST", "/api/cal/save", "{}", &cal); c != 200 || cal.Active.Slot == nil || *cal.Active.Slot != 4 {
		t.Fatalf("save: %d %+v", c, cal.Active)
	}
	// Switch away and back: the 433 slot must be recalled automatically.
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	var pr struct {
		State   State  `json:"state"`
		Message string `json:"message"`
	}
	call(t, ts, "POST", "/api/profile", `{"id":"433"}`, &pr)
	// Active was still 433 (869 has no saved slot), so no recall needed...
	if pr.State.Cal.Matches == nil || !*pr.State.Cal.Matches {
		t.Fatalf("433 cal should match: %+v", pr.State.Cal)
	}
	if c := call(t, ts, "POST", "/api/cal/step/bogus", "{}", nil); c != 400 {
		t.Errorf("bogus step: %d", c)
	}
	// Explicit recall of slot 4 records its range.
	if c := call(t, ts, "POST", "/api/cal/recall", `{"slot":4}`, &cal); c != 200 || cal.Active == nil || cal.Active.Stop != 478.92e6 {
		t.Fatalf("recall: %d %+v", c, cal.Active)
	}
}

func TestDeviceProfilesDriveCalibration(t *testing.T) {
	ts, _ := newTestServer(t, true) // custom firmware with `profile`
	var st State
	call(t, ts, "POST", "/api/connect", "", &st)
	if st.DeviceProfiles == nil || len(st.DeviceProfiles.Profiles) != 4 || st.DeviceProfiles.Custom == nil || st.Info == nil || !strings.Contains(st.Info.Fork, "EasyVNA") {
		t.Fatalf("device profiles: %+v", st.DeviceProfiles)
	}
	var pr struct {
		State   State  `json:"state"`
		Message string `json:"message"`
	}
	call(t, ts, "POST", "/api/profile", `{"id":"wide"}`, &pr)
	if !strings.Contains(pr.Message, "device profile wide applied, calibration slot 3 loaded") {
		t.Errorf("message %q", pr.Message)
	}
	if pr.State.Cal.Active == nil || *pr.State.Cal.Active.Slot != 3 || pr.State.Cal.Matches == nil || !*pr.State.Cal.Matches {
		t.Errorf("active cal: %+v", pr.State.Cal)
	}
	call(t, ts, "POST", "/api/profile", `{"id":"915"}`, &pr)
	if !strings.Contains(pr.Message, "slot 6 has no calibration") || *pr.State.Cal.Matches {
		t.Errorf("915: %q %+v", pr.Message, pr.State.Cal)
	}
	// Custom: nothing saved in slot 2 yet.
	if c := call(t, ts, "POST", "/api/profile", `{"id":"custom"}`, nil); c != 404 {
		t.Errorf("custom before save: %d", c)
	}
	// Calibrate and save a custom range; it lands in slot 2 and comes back via `profile custom`.
	call(t, ts, "POST", "/api/config", `{"start":850000000,"stop":950000000,"points":201}`, nil)
	for _, step := range []string{"open", "short", "load", "done"} {
		call(t, ts, "POST", "/api/cal/step/"+step, `{"fresh":`+fmt.Sprint(step == "open")+`}`, nil)
	}
	var cal CalState
	if call(t, ts, "POST", "/api/cal/save", "{}", &cal); cal.Active == nil || *cal.Active.Slot != 2 {
		t.Fatalf("custom save slot: %+v", cal.Active)
	}
	call(t, ts, "POST", "/api/profile", `{"id":"wide"}`, nil)
	if c := call(t, ts, "POST", "/api/profile", `{"id":"custom"}`, &pr); c != 200 || pr.State.Config.Start != 850e6 ||
		pr.State.Config.Points != 201 || pr.State.Config.Profile != "custom" || !*pr.State.Cal.Matches {
		t.Errorf("custom restore: %d %q %+v", c, pr.Message, pr.State.Config)
	}
}

func TestStockCustomSlotRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t, false)
	call(t, ts, "POST", "/api/config", `{"start":850000000,"stop":950000000,"points":201}`, nil)
	for _, step := range []string{"open", "short", "load", "done"} {
		call(t, ts, "POST", "/api/cal/step/"+step, `{"fresh":`+fmt.Sprint(step == "open")+`}`, nil)
	}
	var cal CalState
	call(t, ts, "POST", "/api/cal/save", "{}", &cal)
	if cal.Active == nil || *cal.Active.Slot != CustomSlot {
		t.Fatalf("stock custom save: %+v", cal.Active)
	}
	call(t, ts, "POST", "/api/profile", `{"id":"wide"}`, nil)
	var pr struct {
		State   State  `json:"state"`
		Message string `json:"message"`
	}
	if c := call(t, ts, "POST", "/api/profile", `{"id":"custom"}`, &pr); c != 200 || pr.State.Config.Stop != 950e6 || !strings.Contains(pr.Message, "recalled custom slot 2") {
		t.Errorf("stock custom restore: %d %q", c, pr.Message)
	}
}

func TestSnapshotsAndExport(t *testing.T) {
	ts, _ := newTestServer(t, false)
	if c := call(t, ts, "POST", "/api/snapshots", `{"name":"x"}`, nil); c != 409 {
		t.Errorf("snapshot before sweep: %d", c)
	}
	call(t, ts, "POST", "/api/sweep", "", nil)
	var info SnapshotInfo
	if c := call(t, ts, "POST", "/api/snapshots", `{"name":"whip / before trim"}`, &info); c != 200 || info.File != "whip_before_trim.json" {
		t.Fatalf("save: %d %+v", c, info)
	}
	var list []SnapshotInfo
	call(t, ts, "GET", "/api/snapshots", "", &list)
	if len(list) != 1 || list[0].Name != "whip / before trim" || list[0].Points != 401 {
		t.Fatalf("list: %+v", list)
	}
	var snap Result
	if c := call(t, ts, "GET", "/api/snapshots/whip%20%2F%20before%20trim", "", &snap); c != 200 || len(snap.Derived.SWR) != 401 {
		t.Fatalf("get: %d", c)
	}
	for _, q := range []string{"format=s1p", "format=csv", "format=s1p&snapshot=whip%20%2F%20before%20trim"} {
		resp, err := http.Get(ts.URL + "/api/export?" + q)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", q, err, resp.Status)
		}
		sc := bufio.NewScanner(resp.Body)
		n := 0
		for sc.Scan() {
			if l := sc.Text(); l != "" && !strings.HasPrefix(l, "!") && !strings.HasPrefix(l, "#") {
				n++
			}
		}
		resp.Body.Close()
		if want := 401; (strings.Contains(q, "csv") && n != want+1) || (!strings.Contains(q, "csv") && n != want) {
			t.Errorf("%s: %d data lines", q, n)
		}
		if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
			t.Errorf("%s: disposition %q", q, cd)
		}
	}
	if c := call(t, ts, "GET", "/api/export?format=s2p", "", nil); c != 400 {
		t.Errorf("s2p without S21: %d", c)
	}
	call(t, ts, "POST", "/api/config", `{"s21":true}`, nil)
	call(t, ts, "POST", "/api/sweep", "", nil)
	if c := call(t, ts, "GET", "/api/export?format=s2p", "", nil); c != 200 {
		// body is not JSON on success; call() only decodes when out != nil
		t.Errorf("s2p with S21: %d", c)
	}
	if c := call(t, ts, "DELETE", "/api/snapshots/whip%20%2F%20before%20trim", "", nil); c != 200 {
		t.Errorf("delete: %d", c)
	}
	if c := call(t, ts, "GET", "/api/snapshots/nope", "", nil); c != 404 {
		t.Errorf("missing snapshot: %d", c)
	}
}

func TestSSEStreamsSweeps(t *testing.T) {
	ts, _ := newTestServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	call(t, ts, "POST", "/api/continuous", `{"on":true}`, nil)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	sweeps := 0
	for sc.Scan() && sweeps < 3 {
		if sc.Text() == "event: sweep" {
			sweeps++
		}
	}
	call(t, ts, "POST", "/api/continuous", `{"on":false}`, nil)
	if sweeps < 3 {
		t.Fatalf("got %d sweep events", sweeps)
	}
}

func TestSnapshotFileNames(t *testing.T) {
	for in, want := range map[string]string{"a b": "a_b.json", "../../etc/passwd": "etc_passwd.json", "x.json": "x.json.json"} {
		if got, err := SnapshotFile(in); err != nil || got != want {
			t.Errorf("SnapshotFile(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "   ", "///", strings.Repeat("x", 81)} {
		if _, err := SnapshotFile(bad); err == nil {
			t.Errorf("SnapshotFile(%q) accepted", bad)
		}
	}
}

// "Reboot into DFU" (FIRMWARE dialog): `reset dfu` is sent, the device is
// dropped and live sweeping stops.
func TestRebootIntoDFU(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var sims []*mock.Sim
	open := func(ctx context.Context) (vna.Device, error) {
		sim := mock.New(mock.Options{PointTime: time.Microsecond, Seed: 7})
		sims = append(sims, sim)
		return nanovnah.Open(ctx, sim, nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{}, Logger: log.New(io.Discard, "", 0)})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	var state struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
		Info      *struct {
			DFUReboot bool `json:"dfuReboot"`
		} `json:"info"`
	}
	if code := call(t, ts, "POST", "/api/dfu", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("not connected: got %d", code)
	}
	if code := call(t, ts, "POST", "/api/connect", "", &state); code != 200 || !state.Connected || state.Info == nil || !state.Info.DFUReboot {
		t.Fatalf("connect: %d %+v", code, state)
	}
	s.continuous.Store(true)
	if code := call(t, ts, "POST", "/api/dfu", "", &state); code != 200 || state.Connected || !strings.Contains(state.Error, "DFU") {
		t.Fatalf("dfu: %d %+v", code, state)
	}
	if s.continuous.Load() {
		t.Error("live sweeping not stopped")
	}
	sent := false
	for _, l := range sims[0].Log {
		sent = sent || l == "reset dfu"
	}
	if !sent {
		t.Errorf("reset dfu not sent: %q", sims[0].Log)
	}
}

// Disconnect gives the NanoVNA back (`resume`, port closed) and nothing
// reopens it until Connect.
func TestDisconnect(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var sims []*mock.Sim
	open := func(ctx context.Context) (vna.Device, error) {
		sim := mock.New(mock.Options{PointTime: time.Microsecond, Seed: 7})
		sims = append(sims, sim)
		return nanovnah.Open(ctx, sim, nanovnah.Options{PortName: "mock", Mock: true, CalSettle: time.Millisecond})
	}
	s := New(Options{Open: open, Store: st, Static: fstest.MapFS{}, Logger: log.New(io.Discard, "", 0)})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	var state struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	}
	if code := call(t, ts, "POST", "/api/connect", "", &state); code != 200 || !state.Connected {
		t.Fatalf("connect: %d %+v", code, state)
	}
	if code := call(t, ts, "POST", "/api/sweep", "", nil); code != 200 {
		t.Fatalf("sweep: %d", code)
	}
	s.continuous.Store(true)
	if code := call(t, ts, "POST", "/api/disconnect", "", &state); code != 200 || state.Connected || !strings.Contains(state.Error, "Press Connect") {
		t.Fatalf("disconnect: %d %+v", code, state)
	}
	if s.continuous.Load() {
		t.Error("live sweeping not stopped")
	}
	log0 := sims[0].Log
	if len(log0) == 0 || log0[len(log0)-1] != "resume" {
		t.Errorf("last command is not resume: %q", log0)
	}
	if code := call(t, ts, "POST", "/api/sweep", "", nil); code == 200 {
		t.Error("a sweep reopened the port after Disconnect")
	}
	if len(sims) != 1 {
		t.Errorf("port reopened %d times after Disconnect", len(sims)-1)
	}
	if code := call(t, ts, "POST", "/api/connect", "", &state); code != 200 || !state.Connected {
		t.Fatalf("connect again: %d %+v", code, state)
	}
}
