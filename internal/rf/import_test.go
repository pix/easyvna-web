package rf

import (
	"bytes"
	"math/cmplx"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) *Measurement {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ImportFile(name, b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func cnear(a, b complex128) bool { return cmplx.Abs(a-b) < 1e-6 }

func TestImportTouchstoneFixtures(t *testing.T) {
	m := readFixture(t, "whip-869.s1p")
	if len(m.Freq) != 5 || m.Freq[2] != 869e6 || !cnear(m.S11[2], complex(0.02, 0.01)) || m.S21 != nil {
		t.Fatalf("whip: %+v", m)
	}
	if m.Format != "Touchstone 1-port (.s1p)" || m.Z0 != 50 || len(m.Notes) != 0 || len(m.Comments) != 3 || !strings.HasPrefix(m.Comments[0], "Hand-written") {
		t.Errorf("whip meta: %q %v %q %q", m.Format, m.Z0, m.Notes, m.Comments)
	}

	m = readFixture(t, "default-ghz-ma.s1p")
	if m.Freq[1] != 868e6 || !cnear(m.S11[0], complex(0, 0.5)) || !cnear(m.S11[1], complex(0, -0.25)) || !cnear(m.S11[2], -0.1) {
		t.Errorf("defaults GHz/MA: %v %v", m.Freq, m.S11)
	}

	m = readFixture(t, "ant-75ohm-db.s1p")
	// 75 Ω load -> Γ50 = 0.2; 50 Ω load (Γ75 = -0.2) -> 0; open stays open.
	if m.Freq[0] != 868e6 || !cnear(m.S11[0], 0.2) || cmplx.Abs(m.S11[1]) > 1e-6 || !cnear(m.S11[2], 1) || m.Z0 != 75 {
		t.Errorf("75 Ω renormalisation: %v %v", m.Freq, m.S11)
	}
	if len(m.Notes) != 1 || !strings.Contains(m.Notes[0], "75 Ω to 50 Ω") {
		t.Errorf("notes: %q", m.Notes)
	}

	m = readFixture(t, "link.s2p")
	if len(m.Freq) != 3 || len(m.S21) != 3 || !cnear(m.S21[1], complex(0.6, 0.4)) || !cnear(m.S11[2], complex(-0.3, 0.25)) {
		t.Fatalf("s2p (noise data must be ignored): %v %v %v", m.Freq, m.S11, m.S21)
	}
	if m.Format != "Touchstone 2-port (.s2p)" || len(m.Notes) != 1 {
		t.Errorf("s2p meta: %q %q", m.Format, m.Notes)
	}

	// 100 Ω series resistor: in 75 Ω S11 = 0.4, S21 = 0.6; in 50 Ω both 0.5.
	m = readFixture(t, "series100-75ohm.s2p")
	for i := range m.Freq {
		if !cnear(m.S11[i], 0.5) || !cnear(m.S21[i], 0.5) {
			t.Fatalf("two-port renormalisation: S11 %v S21 %v", m.S11[i], m.S21[i])
		}
	}

	m = readFixture(t, "v2-oneport.s1p")
	if len(m.Freq) != 3 || m.Freq[1] != 434e6 || !cnear(m.S11[1], complex(0, 0.1)) || !strings.Contains(m.Format, "2.0") {
		t.Errorf("Touchstone 2.0: %+v", m)
	}
}

func TestImportCSVFixtures(t *testing.T) {
	m := readFixture(t, "nanovna-saver.csv")
	if len(m.Freq) != 4 || m.Freq[0] != 860e6 || !cnear(m.S11[1], complex(0.1, -0.05)) || len(m.S21) != 4 || !cnear(m.S21[2], complex(0.03, 0.004)) {
		t.Fatalf("saver csv: %+v", m)
	}

	m = readFixture(t, "app-semicolon-db.csv")
	if len(m.Freq) != 3 || m.Freq[1] != 433.92e6 || !cnear(m.S11[0], complex(0, 0.5)) || !cnear(m.S11[1], 0.1) || m.S21 != nil {
		t.Fatalf("semicolon dB/phase csv: %v %v", m.Freq, m.S11)
	}

	m = readFixture(t, "impedance-rx.csv")
	if !cnear(m.S11[0], 0) || !cnear(m.S11[1], GammaFromZ(complex(25, 25))) || m.Freq[2] != 930e6 {
		t.Errorf("R/X csv: %v %v", m.Freq, m.S11)
	}

	m = readFixture(t, "noheader.csv")
	if len(m.Freq) != 3 || !cnear(m.S11[0], complex(0.2, 0.1)) || len(m.Notes) != 1 {
		t.Errorf("no header: %+v", m)
	}
}

func TestImportCSVRoundTrip(t *testing.T) {
	freq := []float64{863e6, 866.5e6, 870e6, 873e6}
	s11 := []complex128{complex(0.1, -0.2), complex(0.01, 0.02), complex(-0.3, 0.25), complex(0.4, 0.4)}
	s21 := []complex128{complex(0.5, 0.5), complex(0.6, 0.4), complex(0.7, 0.1), complex(0.1, 0.1)}
	var b bytes.Buffer
	if err := WriteCSV(&b, freq, s11, s21); err != nil {
		t.Fatal(err)
	}
	m, err := ImportFile("easyvna.csv", b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for i := range freq {
		if m.Freq[i] != freq[i] || !cnear(m.S11[i], s11[i]) || !cnear(m.S21[i], s21[i]) {
			t.Fatalf("point %d: %v %v %v", i, m.Freq[i], m.S11[i], m.S21[i])
		}
	}
	b.Reset()
	_ = WriteTouchstone(&b, freq, s11, s21, []string{"x"})
	if m, err = ImportFile("x.s2p", b.Bytes()); err != nil || !cnear(m.S21[3], s21[3]) {
		t.Fatalf("s2p round trip: %v", err)
	}
	// Content sniffing when the name says nothing.
	if m, err = ImportFile("measurement.txt", b.Bytes()); err != nil || len(m.S21) != 4 {
		t.Fatalf("sniffed Touchstone: %v", err)
	}
}

func TestImportRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ file, data, want string }{
		"z-params":    {"a.s1p", "# MHz Z RI R 50\n1 50 0\n2 50 0\n3 50 0\n", "only S-parameter"},
		"four-port":   {"a.s4p", "# MHz S RI R 50\n", "4-port"},
		"ragged":      {"a.s1p", "# MHz S RI R 50\n1 0.1 0\n2 0.1\n", "incomplete"},
		"decreasing":  {"a.s1p", "# MHz S RI R 50\n3 0.1 0\n2 0.1 0\n1 0 0\n", "must increase"},
		"not number":  {"a.s1p", "# MHz S RI R 50\n1 0.1 x\n", "not a number"},
		"bad option":  {"a.s1p", "# MHz S XY R 50\n1 0 0\n", "unknown word"},
		"few points":  {"a.s1p", "# MHz S RI R 50\n1 0.1 0\n", "at least 3"},
		"swr only":    {"a.csv", "Frequency (MHz),SWR,Return loss (dB)\n1,1.5,14\n2,1.4,15\n3,1.3,16\n", "no S11"},
		"no freq":     {"a.csv", "S11 Re,S11 Im\n0.1,0\n0.1,0\n0.1,0\n", "no frequency"},
		"text row":    {"a.csv", "Frequency (Hz),S11 Re,S11 Im\n1,0.1,0\nhello,world,!\n", "not a row of numbers"},
		"dup freq":    {"a.csv", "Frequency (Hz),S11 Re,S11 Im\n1,0.1,0\n1,0.1,0\n2,0,0\n", "twice"},
		"empty":       {"a.csv", "", "empty"},
		"keyword v2":  {"a.s1p", "[Version] 2.0\n# MHz S RI R 50\n[Number of Ports] 1\n[Mixed-Mode Order] D2,1\n", "not supported"},
		"ref perport": {"a.s2p", "[Version] 2.0\n# MHz S RI R 50\n[Number of Ports] 2\n[Reference] 50 75\n", "not supported"},
	} {
		_, err := ImportFile(tc.file, []byte(tc.data))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

func TestRenormalize(t *testing.T) {
	// Z = 100 Ω: Γ75 = 25/175, Γ50 = 50/150.
	g := Renormalize(complex(25.0/175, 0), 75)
	if !cnear(g, complex(1.0/3, 0)) {
		t.Errorf("renormalise: %v", g)
	}
	if Renormalize(complex(0.3, 0.1), 50) != complex(0.3, 0.1) {
		t.Error("50 Ω must be unchanged")
	}
}
