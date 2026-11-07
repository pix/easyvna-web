package nanovnah

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"vnaview/internal/vna"
	"vnaview/internal/vna/mock"
)

// scripted is a Transport that answers commands from a table, echoing like
// the firmware and handing out its output in small chunks to exercise
// response reassembly.
type scripted struct {
	mu      sync.Mutex
	replies map[string]string // command -> output (without echo/prompt)
	out     bytes.Buffer
	line    []byte
	sent    []string
	chunk   int
}

func (s *scripted) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range p {
		if c != '\r' {
			s.line = append(s.line, c)
			continue
		}
		cmd := string(s.line)
		s.line = nil
		s.sent = append(s.sent, cmd)
		s.out.WriteString(cmd + "\r\n")
		if cmd != "" {
			r, ok := s.replies[cmd]
			if !ok {
				r = strings.Fields(cmd)[0] + "?\r\n"
			}
			s.out.WriteString(r)
		}
		s.out.WriteString("ch> ")
	}
	return len(p), nil
}

func (s *scripted) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out.Len() == 0 {
		return 0, nil // like VTIME expiry
	}
	n := s.chunk
	if n == 0 || n > len(p) {
		n = len(p)
	}
	return s.out.Read(p[:n])
}

func (s *scripted) Close() error { return nil }

func baseReplies() map[string]string {
	return map[string]string{
		"version": "1.2.40\r\n",
		"info":    "Board: NanoVNA-H\r\nVersion: 1.2.40\r\n",
		"help":    "Commands: help version info scan sweep cal save recall pause resume data frequencies\r\n",
		"cal":     "load open short Es Er Et cal'ed\r\n",
		"resume":  "",
	}
}

func TestOpenIdentifiesHOverScriptedTranscript(t *testing.T) {
	tr := &scripted{replies: baseReplies(), chunk: 5}
	d, err := Open(context.Background(), tr, Options{PortName: "/dev/ttyACM0"})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := d.Info(context.Background())
	if info.Model != "NanoVNA-H" || info.Version != "1.2.40" || info.MaxPoints != 101 || info.CalSlots != 5 {
		t.Errorf("%+v", info)
	}
	if d.SupportsProfiles() {
		t.Error("stock firmware must not report profiles")
	}
	if _, err := d.Profiles(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Profiles on stock: %v", err)
	}
	st, err := d.CalStatus(context.Background())
	if err != nil || !st.Applied {
		t.Errorf("cal: %+v %v", st, err)
	}
}

func TestScanSegmentsAndParses(t *testing.T) {
	r := baseReplies()
	// 5 points (100,115,130,145,160 Hz) with a 3-point limit → two scans.
	r["scan 100 130 3 3"] = "100 0.1 0.0 \r\n115 0.2 0.0 \r\n130 0.3 0.0 \r\n"
	r["scan 145 160 2 3"] = "145 0.4 0.0 \r\n160 0.5 0.0 \r\n"
	tr := &scripted{replies: r, chunk: 7}
	d, err := Open(context.Background(), tr, Options{MaxPoints: 3})
	if err != nil {
		t.Fatal(err)
	}
	sw, err := d.Scan(context.Background(), vna.ScanRequest{Start: 100, Stop: 160, Points: 5})
	if err != nil {
		t.Fatalf("%v (sent %q)", err, tr.sent)
	}
	if len(sw.Freq) != 5 || sw.Freq[3] != 145 || sw.S11[4] != 0.5 || sw.S21 != nil {
		t.Fatalf("%+v", sw)
	}
}

func TestSegments(t *testing.T) {
	for _, c := range []struct {
		n, max int
		want   [][2]int
	}{
		{101, 101, [][2]int{{0, 100}}},
		{401, 101, [][2]int{{0, 100}, {101, 200}, {201, 300}, {301, 400}}},
		{202, 101, [][2]int{{0, 100}, {101, 201}}},
		{203, 101, [][2]int{{0, 67}, {68, 135}, {136, 202}}},
	} {
		got := segments(c.n, c.max)
		if len(got) != len(c.want) {
			t.Errorf("segments(%d,%d) = %v", c.n, c.max, got)
			continue
		}
		total := 0
		for i, s := range got {
			if s != c.want[i] || s[1]-s[0]+1 > c.max || s[1]-s[0]+1 < 2 {
				t.Errorf("segments(%d,%d) = %v, want %v", c.n, c.max, got, c.want)
				break
			}
			total += s[1] - s[0] + 1
		}
		if total != c.n {
			t.Errorf("segments(%d,%d) cover %d points", c.n, c.max, total)
		}
	}
}

func TestLegacyFallbackWhenScanHasNoOutmask(t *testing.T) {
	r := baseReplies()
	r["scan 100 200 3 3"] = "" // old firmware: prints nothing
	r["sweep 100 200 3"] = ""
	r["pause"] = ""
	r["frequencies"] = "100\r\n150\r\n200\r\n"
	r["data 0"] = "0.1 0.2\r\n0.3 0.4\r\n0.5 0.6\r\n"
	tr := &scripted{replies: r}
	d, err := Open(context.Background(), tr, Options{CalSettle: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sw, err := d.Scan(context.Background(), vna.ScanRequest{Start: 100, Stop: 200, Points: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Freq) != 3 || sw.Freq[1] != 150 || sw.S11[2] != complex(0.5, 0.6) {
		t.Fatalf("%+v", sw)
	}
	if !d.legacy {
		t.Error("legacy mode not latched")
	}
}

func TestShellTimeout(t *testing.T) {
	tr := &silent{}
	sh := NewShell(tr)
	_, err := sh.Exec(context.Background(), "version", 150*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
	if _, err := sh.Exec(context.Background(), strings.Repeat("x", 64), time.Second); err == nil {
		t.Error("over-long command accepted")
	}
}

type silent struct{}

func (silent) Read(p []byte) (int, error)  { time.Sleep(10 * time.Millisecond); return 0, nil }
func (silent) Write(p []byte) (int, error) { return len(p), nil }
func (silent) Close() error                { return nil }

// --- driver against the byte-level simulator ---

func openSim(t *testing.T, opt mock.Options, dopt Options) (*Driver, *mock.Sim) {
	t.Helper()
	opt.PointTime = time.Microsecond
	opt.Seed = 1
	sim := mock.New(opt)
	dopt.CalSettle = time.Millisecond
	d, err := Open(context.Background(), sim, dopt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, sim
}

func TestDriverOverSimulator(t *testing.T) {
	d, sim := openSim(t, mock.Options{}, Options{})
	info, _ := d.Info(context.Background())
	if info.Model != "NanoVNA-H 4" || info.MaxPoints != 401 {
		t.Fatalf("%+v", info)
	}
	sw, err := d.Scan(context.Background(), vna.ScanRequest{Start: 300e6, Stop: 1000e6, Points: 1001, S21: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Freq) != 1001 || len(sw.S21) != 1001 || sw.Freq[0] != 300e6 || sw.Freq[1000] != 1000e6 {
		t.Fatalf("len %d/%d f0 %v fN %v", len(sw.Freq), len(sw.S21), sw.Freq[0], sw.Freq[len(sw.Freq)-1])
	}
	for i := 1; i < len(sw.Freq); i++ {
		if sw.Freq[i] <= sw.Freq[i-1] {
			t.Fatalf("frequencies not increasing at %d", i)
		}
	}
	scans := 0
	for _, l := range sim.Log {
		if strings.HasPrefix(l, "scan ") {
			scans++
		}
	}
	if scans != 3 {
		t.Errorf("1001 points at 401/scan should take 3 scans, took %d", scans)
	}
}

func TestDriverLearnsPointLimit(t *testing.T) {
	// The board claims H4 but the firmware only allows 101 points.
	d, _ := openSim(t, mock.Options{MaxPoints: 101}, Options{})
	sw, err := d.Scan(context.Background(), vna.ScanRequest{Start: 819e6, Stop: 919e6, Points: 401})
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Freq) != 401 || d.maxPoints != 101 {
		t.Fatalf("len %d max %d", len(sw.Freq), d.maxPoints)
	}
}

func TestCalibrationFlowOverSimulator(t *testing.T) {
	d, sim := openSim(t, mock.Options{}, Options{})
	ctx := context.Background()
	rng := &vna.ScanRequest{Start: 819e6, Stop: 919e6, Points: 401}
	for _, s := range []vna.CalStep{vna.CalReset, vna.CalOpen, vna.CalShort, vna.CalLoad} {
		if err := d.Calibrate(ctx, s, rng); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	st, _ := d.CalStatus(ctx)
	if !st.Open || !st.Short || !st.Load || st.Applied {
		t.Fatalf("before done: %+v", st)
	}
	if err := d.Calibrate(ctx, vna.CalDone, nil); err != nil {
		t.Fatal(err)
	}
	st, _ = d.CalStatus(ctx)
	if !st.Applied {
		t.Fatalf("after done: %+v", st)
	}
	if err := d.SaveCal(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveCal(ctx, 9); err == nil {
		t.Error("slot 9 accepted on a 7-slot device")
	}
	if err := d.RecallCal(ctx, 5); err == nil {
		t.Error("recall of empty slot should report the firmware error")
	}
	joined := strings.Join(sim.Log, "|")
	for _, want := range []string{"sweep 819000000 919000000 401", "cal reset", "resume", "cal open", "cal done", "save 2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestDeviceProfilesOverSimulator(t *testing.T) {
	d, sim := openSim(t, mock.Options{Profiles: true}, Options{})
	if !d.SupportsProfiles() {
		t.Fatal("EasyVNA fork not detected")
	}
	info, _ := d.Info(context.Background())
	if !IsEasyVNAFork(info.Fork) || strings.Contains(info.Version, "Fork") {
		t.Errorf("info: %+v", info)
	}
	ctx := context.Background()
	dp, err := d.Profiles(ctx)
	if err != nil || len(dp.Profiles) != 4 || dp.Profiles[2].Name != "869" || dp.Profiles[2].Slot != 5 ||
		!dp.Profiles[0].Cal || dp.Custom == nil || dp.Custom.Cal || dp.Boot == nil {
		t.Fatalf("%+v %v", dp, err)
	}
	p, err := d.ApplyProfile(ctx, "433")
	if err != nil || p.Start != 388.92e6 || p.Points != 401 || !p.Active || p.Cal {
		t.Fatalf("%+v %v", p, err)
	}
	// Save a custom range into slot 2, then `profile custom` restores it.
	rng := &vna.ScanRequest{Start: 850e6, Stop: 950e6, Points: 201}
	for _, s := range []vna.CalStep{vna.CalReset, vna.CalOpen, vna.CalShort, vna.CalLoad, vna.CalDone} {
		if err := d.Calibrate(ctx, s, rng); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SaveCal(ctx, 2); err != nil {
		t.Fatal(err)
	}
	d.ApplyProfile(ctx, "wide")
	c, err := d.ApplyProfile(ctx, "custom")
	if err != nil || c.Start != 850e6 || c.Stop != 950e6 || c.Points != 201 || !c.Cal || !c.Active {
		t.Fatalf("custom: %+v %v", c, err)
	}
	if _, err := d.ApplyProfile(ctx, "none"); err == nil {
		t.Error("removed `profile none` accepted")
	}
	_ = sim
}

func TestStockFirmwareHasNoProfiles(t *testing.T) {
	d, _ := openSim(t, mock.Options{}, Options{})
	info, _ := d.Info(context.Background())
	if d.SupportsProfiles() || info.Fork != "" {
		t.Errorf("stock detected as fork: %+v", info)
	}
}

func TestCloseResumesAndReadEOF(t *testing.T) {
	sim := mock.New(mock.Options{PointTime: time.Microsecond})
	d, err := Open(context.Background(), sim, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if _, err := sim.Read(make([]byte, 8)); err != io.EOF {
		t.Errorf("read after close: %v", err)
	}
	if sim.Log[len(sim.Log)-1] != "resume" {
		t.Errorf("last command %q, want resume", sim.Log[len(sim.Log)-1])
	}
}

func TestReadOnlyRefusesStateChanges(t *testing.T) {
	d, sim := openSim(t, mock.Options{Profiles: true}, Options{ReadOnly: true})
	ctx := context.Background()
	if err := d.Calibrate(ctx, vna.CalOpen, nil); !errors.Is(err, ErrReadOnly) {
		t.Errorf("cal: %v", err)
	}
	if err := d.SaveCal(ctx, 3); !errors.Is(err, ErrReadOnly) {
		t.Errorf("save: %v", err)
	}
	if err := d.RecallCal(ctx, 3); !errors.Is(err, ErrReadOnly) {
		t.Errorf("recall: %v", err)
	}
	if _, err := d.ApplyProfile(ctx, "869"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("profile: %v", err)
	}
	if _, err := d.Scan(ctx, vna.ScanRequest{Start: 800e6, Stop: 900e6, Points: 11}); err != nil {
		t.Errorf("scan must still work: %v", err)
	}
	for _, l := range sim.Log {
		for _, bad := range []string{"cal ", "save", "recall", "profile 869"} {
			if strings.HasPrefix(l, bad) {
				t.Errorf("read-only driver sent %q", l)
			}
		}
	}
}
