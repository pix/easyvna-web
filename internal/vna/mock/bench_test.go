package mock

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestBenchCutAndSwap(t *testing.T) {
	if _, err := NewBench("nope", 0, false); err == nil {
		t.Fatal("unknown scenario accepted")
	}
	b, err := NewBench("869", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if b.Antenna() != "869" || len(ScenarioNames()) != len(Scenarios) {
		t.Fatal("names")
	}
	sim := b.NewSim()
	f0 := Scenarios["869"]().Modes[0].F0
	b.Cut(2)
	got := sim.opt.Antenna.Modes[0].F0
	want := f0 * (benchLen + benchDelta) / (benchLen - 2 + benchDelta)
	if got != want || got <= f0 {
		t.Fatalf("cut: f0 %.0f -> %.0f, want %.0f", f0, got, want)
	}
	if err := b.SetAntenna("915"); err != nil {
		t.Fatal(err)
	}
	if sim.opt.Antenna.Name != Scenarios["915"]().Name || b.Antenna() != "915" {
		t.Fatal("swap did not reach the open simulator")
	}
	if err := b.SetAntenna("bogus"); err == nil {
		t.Fatal("bogus antenna accepted")
	}
	// A new simulator speaks the EasyVNA firmware when asked to.
	s2 := b.NewSim()
	_, _ = s2.Write([]byte("version\r"))
	var out bytes.Buffer
	buf := make([]byte, 256)
	for i := 0; i < 20 && !strings.Contains(out.String(), "version\r\n"); i++ {
		n, _ := s2.Read(buf)
		out.Write(buf[:n])
	}
	if !strings.Contains(out.String(), "ch> ") {
		t.Fatalf("no prompt: %q", out.String())
	}
}

func TestLotUnits(t *testing.T) {
	b, err := NewBench("lot869", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	sim := b.NewSim()
	first := sim.opt.Antenna
	if first.Name != b.base.Name || first.Modes[0].F0 == 0 {
		t.Fatal("bench does not keep its unit")
	}
	// Selecting the lot again connects the next unit; cutting keeps the unit.
	_ = b.SetAntenna("lot869")
	second := sim.opt.Antenna
	if second.Name == first.Name || second.Modes[0].F0 == first.Modes[0].F0 {
		t.Fatalf("same unit twice: %s", second.Name)
	}
	b.Cut(1)
	if sim.opt.Antenna.Name != second.Name || sim.opt.Antenna.Modes[0].F0 <= second.Modes[0].F0 {
		t.Fatal("cut changed the unit")
	}
	if b.base.Modes[0].F0 != second.Modes[0].F0 {
		t.Fatal("cut modified the stored unit")
	}
	// The lot: biased low, spread of a few tenths of a percent, an outlier
	// every seventh unit; neighbours differ (BATCH auto-capture sees a
	// new antenna).
	var sum, sum2 float64
	n := 0
	for k := 1; k <= 70; k++ {
		off := LotOffset(k)
		if k%7 == 3 {
			if off > -0.025 {
				t.Fatalf("unit %d should be an outlier: %.4f", k, off)
			}
			continue
		}
		if off < -0.012 || off > 0.008 {
			t.Fatalf("unit %d offset %.4f out of range", k, off)
		}
		sum += off
		sum2 += off * off
		n++
	}
	mean := sum / float64(n)
	sd := math.Sqrt(sum2/float64(n) - mean*mean)
	if math.Abs(mean-LotBias) > 0.001 || sd < 0.0006 || sd > 0.002 {
		t.Fatalf("lot mean %.4f sd %.4f", mean, sd)
	}
	for _, band := range LotBands {
		a := LotAntenna(band, 1)
		if !strings.Contains(a.Name, band) || len(a.Modes) != 2 {
			t.Fatalf("%s: %+v", band, a)
		}
	}
}
