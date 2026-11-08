package nanovnav2

import (
	"bytes"
	"context"
	"io"
	"math/cmplx"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
)

func TestRecordRoundTrip(t *testing.T) {
	r := Record{Fwd0: complex(2e6, -1e6), Rev0: complex(-123456, 7), Rev1: complex(1, -2), FreqIndex: 517}
	b := EncodeRecord(r)
	if len(b) != RecordSize {
		t.Fatal(len(b))
	}
	if g := ParseRecord(b); g != r {
		t.Fatalf("round trip: %+v", g)
	}
	if s := r.S11(); cmplx.Abs(s-r.Rev0/r.Fwd0) > 1e-15 {
		t.Error("S11")
	}
	if !cmplx.IsNaN(Record{}.S11()) {
		t.Error("zero forward wave must give NaN")
	}
	if got := cmdWrite8(RegSweepStep, 0x0102030405060708); !bytes.Equal(got, []byte{0x23, 0x10, 8, 7, 6, 5, 4, 3, 2, 1}) {
		t.Errorf("WRITE8: % x", got)
	}
	if got := cmdWrite2(RegSweepPoints, 401); !bytes.Equal(got, []byte{0x21, 0x20, 0x91, 0x01}) {
		t.Errorf("WRITE2: % x", got)
	}
}

func openSim(t *testing.T, a mock.Antenna) (*Driver, *mock.SimV2) {
	t.Helper()
	sim := mock.NewV2(mock.V2Options{Antenna: a, Seed: 1, PointTime: time.Microsecond})
	d, err := Open(context.Background(), sim, Options{PortName: "sim", Mock: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, sim
}

func TestOpenAndScanSimulator(t *testing.T) {
	ant := mock.Scenarios["869"]()
	ant.Drift = 0
	d, _ := openSim(t, ant)
	info, _ := d.Info(context.Background())
	if info.Driver != "nanovna-v2" || info.Model != "NanoVNA V2" || info.Version != "1.3" || !info.HostCal || !info.Experimental || !info.TwoPort {
		t.Fatalf("info: %+v", info)
	}
	req := vna.ScanRequest{Start: 779e6, Stop: 959e6, Points: 401, S21: true}
	sw, err := d.Scan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Freq) != 401 || len(sw.S11) != 401 || len(sw.S21) != 401 || sw.Freq[0] != 779e6 || sw.Freq[400] < 958.99e6 || sw.Freq[400] > 959.01e6 {
		t.Fatalf("grid: %d points %v..%v", len(sw.Freq), sw.Freq[0], sw.Freq[len(sw.Freq)-1])
	}
	for i := 1; i < len(sw.Freq); i++ {
		if sw.Freq[i] <= sw.Freq[i-1] {
			t.Fatalf("frequencies not increasing at %d", i)
		}
	}
	// Raw data: the antenna through the simulated V2 error model.
	for _, i := range []int{0, 150, 400} {
		g, _ := ant.Measure(sw.Freq[i], 0)
		want := mock.RawErrorV2(g, sw.Freq[i])
		if cmplx.Abs(sw.S11[i]-want) > 0.01 {
			t.Errorf("point %d: raw S11 %v, want %v", i, sw.S11[i], want)
		}
	}
	// Without S21, none is returned; a small sweep works too.
	sw, err = d.Scan(context.Background(), vna.ScanRequest{Start: 860e6, Stop: 870e6, Points: 11})
	if err != nil || len(sw.Freq) != 11 || sw.S21 != nil {
		t.Fatalf("small sweep: %v %+v", err, sw)
	}
	if err := d.Calibrate(context.Background(), vna.CalOpen, nil); err != ErrHostCal {
		t.Errorf("on-device cal must be refused: %v", err)
	}
	if _, err := d.Scan(context.Background(), vna.ScanRequest{Start: 1e6, Stop: 4e9, Points: 11}); err == nil {
		t.Error("out-of-range sweep accepted")
	}
}

// scripted answers fixed bytes per command (for refusals).
type scripted struct {
	mu    sync.Mutex
	out   bytes.Buffer
	reply func(cmd []byte) []byte
}

func (s *scripted) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(s.reply(p))
	return len(p), nil
}
func (s *scripted) Read(p []byte) (int, error) {
	s.mu.Lock()
	n, _ := s.out.Read(p)
	s.mu.Unlock()
	if n == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	return n, nil
}
func (s *scripted) Close() error { return nil }

var _ io.ReadWriteCloser = (*scripted)(nil)

func TestOpenRefusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A text-shell NanoVNA echoes and prints a prompt: not a V2.
	shell := &scripted{reply: func(p []byte) []byte { return append(append([]byte(nil), p...), []byte("\r\nch> ")...) }}
	if _, err := Open(ctx, shell, Options{}); err == nil || !strings.Contains(err.Error(), "not a NanoVNA V2") && !strings.Contains(err.Error(), "no reply") {
		t.Errorf("shell device: %v", err)
	}
	silent := &scripted{reply: func(p []byte) []byte { return nil }}
	if _, err := Open(ctx, silent, Options{}); err == nil || !strings.Contains(err.Error(), "not a NanoVNA V2") {
		t.Errorf("silent device: %v", err)
	}
	dfu := &scripted{reply: func(p []byte) []byte {
		switch {
		case len(p) == 1 && p[0] == OpIndicate:
			return []byte{'2'}
		case len(p) == 2 && p[0] == OpRead && p[1] == RegFirmwareMajor:
			return []byte{0xff}
		case len(p) == 2 && p[0] == OpRead:
			return []byte{2}
		}
		return nil
	}}
	if _, err := Open(ctx, dfu, Options{}); err == nil || !strings.Contains(err.Error(), "DFU") {
		t.Errorf("DFU: %v", err)
	}
}
