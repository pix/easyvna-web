package nanovnah

import (
	"errors"
	"reflect"
	"testing"
)

// Transcripts below follow the NanoVNA-D shell exactly: the command is
// echoed, Enter prints "\r\n", output lines end in "\r\n", then "ch> ".

func TestSplitResponseStripsEchoAndPrompt(t *testing.T) {
	raw := []byte("version\r\n1.2.40\r\nch> ")
	lines, err := SplitResponse("version", raw)
	if err != nil || !reflect.DeepEqual(lines, []string{"1.2.40"}) {
		t.Fatalf("got %q, %v", lines, err)
	}
}

func TestSplitResponseEmptyOutput(t *testing.T) {
	lines, err := SplitResponse("pause", []byte("pause\r\nch> "))
	if err != nil || len(lines) != 0 {
		t.Fatalf("got %q, %v", lines, err)
	}
}

func TestSplitResponseStalePromptBeforeEcho(t *testing.T) {
	// Leftover prompt from a previous exchange precedes the echo.
	raw := []byte("\r\nch> sweep\r\n50000 900000000 101\r\nch> ")
	lines, err := SplitResponse("sweep", raw)
	if err != nil || !reflect.DeepEqual(lines, []string{"50000 900000000 101"}) {
		t.Fatalf("got %q, %v", lines, err)
	}
}

func TestSplitResponseNoEcho(t *testing.T) {
	lines, err := SplitResponse("version", []byte("1.2.40\r\nch> "))
	if err != nil || !reflect.DeepEqual(lines, []string{"1.2.40"}) {
		t.Fatalf("got %q, %v", lines, err)
	}
}

func TestSplitResponseErrors(t *testing.T) {
	cases := []struct {
		cmd, raw string
		kind     error
	}{
		{"frobnicate 1", "frobnicate 1\r\nfrobnicate?\r\nch> ", ErrUnknownCommand},
		{"scan 1", "scan 1\r\nusage: scan {start(Hz)} {stop(Hz)} [points] [outmask]\r\nch> ", ErrUsage},
		{"scan 900 100 11 3", "scan 900 100 11 3\r\nfrequency range is invalid\r\nch> ", ErrUsage},
		{"scan 1 2 1001 3", "scan 1 2 1001 3\r\nsweep points exceeds range 401\r\nch> ", ErrUsage},
		{"recall 3", "recall 3\r\nErr, default load\r\nch> ", ErrUsage},
	}
	for _, c := range cases {
		_, err := SplitResponse(c.cmd, []byte(c.raw))
		var se *ShellError
		if !errors.Is(err, c.kind) || !errors.As(err, &se) {
			t.Errorf("%q: err = %v, want %v", c.cmd, err, c.kind)
		}
	}
}

func TestParseScanMasks(t *testing.T) {
	// Real firmware prints a trailing space before CRLF.
	raw := "scan 863000000 870000000 3 7\r\n" +
		"863000000 0.123456 -0.234567 0.000100 0.000200 \r\n" +
		"866500000 0.010000 0.020000 0.000300 -0.000400 \r\n" +
		"870000000 -0.300000 0.250000 0.000500 0.000600 \r\n" +
		"ch> "
	lines, err := SplitResponse("scan 863000000 870000000 3 7", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	pts, err := ParseScan(lines, MaskFreq|MaskS11|MaskS21)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 || pts[1].Freq != 866.5e6 || pts[0].S11 != complex(0.123456, -0.234567) || pts[2].S21 != complex(0.0005, 0.0006) {
		t.Fatalf("%+v", pts)
	}
	// S11 only, no frequency.
	pts, err = ParseScan([]string{"0.5 -0.5 "}, MaskS11)
	if err != nil || pts[0].S11 != complex(0.5, -0.5) || pts[0].Freq != 0 {
		t.Fatalf("%+v %v", pts, err)
	}
	// Field count mismatch is an error.
	if _, err := ParseScan([]string{"863000000 0.1"}, MaskFreq|MaskS11); err == nil {
		t.Error("short line accepted")
	}
	if _, err := ParseScan([]string{"863000000 0.1 zz"}, MaskFreq|MaskS11); err == nil {
		t.Error("garbage accepted")
	}
	// nan from the firmware parses (driver sanitises it).
	if pts, err := ParseScan([]string{"1 nan -nan"}, MaskFreq|MaskS11); err != nil || len(pts) != 1 {
		t.Errorf("nan: %v", err)
	}
}

func TestParseFrequenciesAndData(t *testing.T) {
	f, err := ParseFrequencies([]string{"50000", "450025000", "900000000"})
	if err != nil || !reflect.DeepEqual(f, []float64{50000, 450025000, 900000000}) {
		t.Fatalf("%v %v", f, err)
	}
	d, err := ParseData([]string{"0.100000 -0.200000", "1.000000 0.000000"})
	if err != nil || d[0] != complex(0.1, -0.2) || d[1] != 1 {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := ParseData([]string{"0.1"}); err == nil {
		t.Error("bad data accepted")
	}
}

func TestParseSweep(t *testing.T) {
	s, err := ParseSweep([]string{"50000 900000000 101"})
	if err != nil || s.Start != 50000 || s.Stop != 900e6 || s.Points != 101 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := ParseSweep(nil); err == nil {
		t.Error("empty accepted")
	}
}

func TestParseCalStatus(t *testing.T) {
	st := ParseCalStatus([]string{"load open short Es Er Et cal'ed"})
	if !st.Known || !st.Applied || !st.Open || !st.Short || !st.Load || st.Thru || st.Isoln {
		t.Errorf("%+v", st)
	}
	st = ParseCalStatus(nil)
	if !st.Known || st.Applied || len(st.Flags) != 0 {
		t.Errorf("empty: %+v", st)
	}
	st = ParseCalStatus([]string{"something unexpected"})
	if st.Known {
		t.Errorf("unknown text must not be Known: %+v", st)
	}
}

func TestParsePointsLimit(t *testing.T) {
	if n, ok := ParsePointsLimit("sweep points exceeds range 101"); !ok || n != 101 {
		t.Errorf("%d %v", n, ok)
	}
	if _, ok := ParsePointsLimit("usage: scan"); ok {
		t.Error("false positive")
	}
}

func TestParseInfoAndCapabilities(t *testing.T) {
	info := []string{"Board: NanoVNA-H 4", "2019-2024 Copyright NanoVNA.com", "Version: 1.2.40"}
	b := ParseInfo(info)
	if b != "NanoVNA-H 4" {
		t.Fatalf("board %q", b)
	}
	if p, slots, _ := Capabilities(b); p != 401 || slots != 7 {
		t.Errorf("H4: %d %d", p, slots)
	}
	if p, slots, _ := Capabilities("NanoVNA-H"); p != 101 || slots != 5 {
		t.Errorf("H: %d %d", p, slots)
	}
}

func TestParseHelp(t *testing.T) {
	cmds := ParseHelp([]string{"Commands: help exit info version pause resume cal save recall", "data frequencies scan sweep profile"})
	for _, c := range []string{"scan", "cal", "profile", "help"} {
		if !cmds[c] {
			t.Errorf("missing %s", c)
		}
	}
	if cmds["Commands:"] {
		t.Error("header counted as command")
	}
}

func TestParseProfilesV2(t *testing.T) {
	// PiX firmware v2 `profile` listing.
	raw := "profile\r\n" +
		"wide 300000000 1000000000 401 3 1 0\r\n" +
		"433 388920000 478920000 401 4 0 0\r\n" +
		"869 779000000 959000000 401 5 1 1\r\n" +
		"915 825000000 1005000000 401 6 0 0\r\n" +
		"custom 850000000 950000000 201 2 1 0\r\n" +
		"boot 869 ask=1\r\n" +
		"ch> "
	lines, err := SplitResponse("profile", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	dp := ParseProfiles(lines)
	if len(dp.Profiles) != 4 {
		t.Fatalf("%+v", dp)
	}
	p := dp.Profiles[2]
	if p.Name != "869" || p.Start != 779e6 || p.Stop != 959e6 || p.Points != 401 || p.Slot != 5 || !p.Cal || !p.Active {
		t.Errorf("869: %+v", p)
	}
	if dp.Profiles[1].Start != 388.92e6 || dp.Profiles[3].Stop != 1005e6 {
		t.Errorf("ranges: %+v", dp.Profiles)
	}
	if dp.Custom == nil || dp.Custom.Slot != 2 || dp.Custom.Points != 201 || !dp.Custom.Cal || dp.Custom.Start != 850e6 {
		t.Errorf("custom: %+v", dp.Custom)
	}
	if dp.Boot == nil || dp.Boot.Name != "869" || !dp.Boot.Ask {
		t.Errorf("boot: %+v", dp.Boot)
	}
	// Tolerates the older 6-field form and rejects junk.
	if p, err := ParseProfileLine("869 819000000 919000000 2 1 1"); err != nil || p.Slot != 2 || p.Points != 0 || !p.Active {
		t.Errorf("6-field: %+v %v", p, err)
	}
	if _, err := ParseProfileLine("869 779000000 959000000 401 5 1 x"); err == nil {
		t.Error("bad flag accepted")
	}
	if _, ok := ParseBootLine("boot"); ok {
		t.Error("short boot line accepted")
	}
}

func TestParseFork(t *testing.T) {
	if f := ParseFork([]string{"1.2.50", "Fork: PiX (profiles v2)"}); f != "PiX (profiles v2)" || !IsPiXFork(f) {
		t.Errorf("fork %q", f)
	}
	if f := ParseFork([]string{"Board: NanoVNA-H 4", "Version: 1.2.50"}); f != "" || IsPiXFork(f) {
		t.Errorf("stock fork %q", f)
	}
}

// Recorded from a real NanoVNA-H4 (firmware 1.2.50) on 2026-11-07.
func TestRecordedH4Transcripts(t *testing.T) {
	lines, err := SplitResponse("cal", []byte("cal\r\nload Es Er cal'ed \r\nch> "))
	if err != nil {
		t.Fatal(err)
	}
	st := ParseCalStatus(lines)
	if !st.Known || !st.Applied || !st.Load || st.Open || st.Short {
		t.Errorf("cal: %+v", st)
	}
	raw := "scan 819000000 919000000 401 3\r\n819000000 -0.528883168 0.906740096 \r\n819250000 -0.528345632 0.909487744 \r\n819500000 -0.528859968 0.911260544 \r\nch> "
	lines, err = SplitResponse("scan 819000000 919000000 401 3", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	pts, err := ParseScan(lines, MaskFreq|MaskS11)
	if err != nil || len(pts) != 3 || pts[1].Freq != 819250000 || pts[2].S11 != complex(-0.528859968, 0.911260544) {
		t.Fatalf("%+v %v", pts, err)
	}
	if lines, err := SplitResponse("resume", []byte("resume\r\nch> ")); err != nil || len(lines) != 0 {
		t.Errorf("resume: %q %v", lines, err)
	}
}
