package mock

import (
	"bytes"
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
