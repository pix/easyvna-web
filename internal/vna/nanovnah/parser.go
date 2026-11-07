// Package nanovnah drives NanoVNA-H / H4 (and NanoVNA-D firmware) devices
// over the ChibiOS text shell exposed on the USB CDC serial port.
//
// Protocol summary (from the NanoVNA-D firmware, main.c):
//   - commands are a line of text terminated by '\r' ('\n' is ignored);
//   - the shell echoes every accepted character, then "\r\n" on Enter;
//   - output lines end in "\r\n";
//   - after each command the prompt "ch> " (no newline) is printed;
//   - an unknown command prints "<name>?";
//   - bad arguments usually print a line starting with "usage:";
//   - `scan start stop [points] [outmask]` prints, per point,
//     "freq re0 im0 re1 im1 " depending on outmask bits
//     (1 = frequency, 2 = S11, 4 = S21, 8 = no calibration,
//     16 = no electrical delay, 32 = no S21 offset, 128 = binary).
package nanovnah

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"vnaview/internal/vna"
)

// Prompt is the ChibiOS shell prompt printed by NanoVNA firmware.
const Prompt = "ch> "

// Scan output mask bits.
const (
	MaskFreq    = 1 << 0
	MaskS11     = 1 << 1
	MaskS21     = 1 << 2
	MaskNoCal   = 1 << 3
	MaskNoDelay = 1 << 4
	MaskBinary  = 1 << 7
)

// Errors reported by the shell.
var (
	ErrUnknownCommand = errors.New("nanovna: unknown command")
	ErrUsage          = errors.New("nanovna: command rejected")
)

// ShellError carries the firmware's own error text.
type ShellError struct {
	Cmd  string
	Line string
	Kind error
}

func (e *ShellError) Error() string { return fmt.Sprintf("%v: %q -> %q", e.Kind, e.Cmd, e.Line) }
func (e *ShellError) Unwrap() error { return e.Kind }

// SplitResponse turns the raw bytes received after sending cmd (up to and
// including the prompt) into output lines: the echoed command line and the
// trailing prompt are removed, CR/LF normalised and empty lines dropped.
// It returns a *ShellError when the firmware reports an unknown command or a
// usage error.
func SplitResponse(cmd string, raw []byte) ([]string, error) {
	s := string(raw)
	s = strings.TrimSuffix(s, Prompt)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, l)
	}
	cmd = strings.TrimSpace(cmd)
	// Drop the echo. It is normally the first line, but a stale prompt
	// from a previous exchange may precede it.
	for i, l := range lines {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), strings.TrimSpace(Prompt)))
		if t == cmd {
			lines = lines[i+1:]
			break
		}
		if i >= 1 {
			break
		}
	}
	if cmd != "" && len(lines) > 0 {
		name := strings.Fields(cmd)[0]
		first := strings.TrimSpace(lines[0])
		if first == name+"?" {
			return nil, &ShellError{Cmd: cmd, Line: first, Kind: ErrUnknownCommand}
		}
		for _, l := range lines {
			t := strings.TrimSpace(l)
			lt := strings.ToLower(t)
			if strings.HasPrefix(lt, "usage:") || strings.Contains(lt, "exceeds range") ||
				strings.Contains(lt, "is invalid") || strings.HasPrefix(lt, "err") {
				return nil, &ShellError{Cmd: cmd, Line: t, Kind: ErrUsage}
			}
		}
	}
	return lines, nil
}

// ScanPoint is one line of `scan` text output.
type ScanPoint struct {
	Freq float64
	S11  complex128
	S21  complex128
}

// ParseScan parses text `scan` output produced with the given outmask.
func ParseScan(lines []string, mask int) ([]ScanPoint, error) {
	want := 0
	if mask&MaskFreq != 0 {
		want++
	}
	if mask&MaskS11 != 0 {
		want += 2
	}
	if mask&MaskS21 != 0 {
		want += 2
	}
	if want == 0 {
		return nil, nil
	}
	pts := make([]ScanPoint, 0, len(lines))
	for n, l := range lines {
		f := strings.Fields(l)
		if len(f) != want {
			return nil, fmt.Errorf("scan line %d: want %d fields, got %d: %q", n, want, len(f), l)
		}
		var p ScanPoint
		k := 0
		if mask&MaskFreq != 0 {
			v, err := parseFloat(f[0])
			if err != nil {
				return nil, fmt.Errorf("scan line %d: %w", n, err)
			}
			p.Freq = v
			k = 1
		}
		if mask&MaskS11 != 0 {
			c, err := parseComplex(f[k], f[k+1])
			if err != nil {
				return nil, fmt.Errorf("scan line %d: %w", n, err)
			}
			p.S11 = c
			k += 2
		}
		if mask&MaskS21 != 0 {
			c, err := parseComplex(f[k], f[k+1])
			if err != nil {
				return nil, fmt.Errorf("scan line %d: %w", n, err)
			}
			p.S21 = c
		}
		pts = append(pts, p)
	}
	return pts, nil
}

// ParseFrequencies parses `frequencies` output (one integer Hz per line).
func ParseFrequencies(lines []string) ([]float64, error) {
	out := make([]float64, 0, len(lines))
	for n, l := range lines {
		v, err := parseFloat(strings.TrimSpace(l))
		if err != nil {
			return nil, fmt.Errorf("frequencies line %d: %w", n, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// ParseData parses `data N` output ("re im" per line).
func ParseData(lines []string) ([]complex128, error) {
	out := make([]complex128, 0, len(lines))
	for n, l := range lines {
		f := strings.Fields(l)
		if len(f) != 2 {
			return nil, fmt.Errorf("data line %d: want 2 fields, got %q", n, l)
		}
		c, err := parseComplex(f[0], f[1])
		if err != nil {
			return nil, fmt.Errorf("data line %d: %w", n, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// SweepSettings is the output of `sweep` without arguments.
type SweepSettings struct {
	Start, Stop float64
	Points      int
}

// ParseSweep parses "start stop points".
func ParseSweep(lines []string) (SweepSettings, error) {
	if len(lines) == 0 {
		return SweepSettings{}, errors.New("sweep: no output")
	}
	f := strings.Fields(lines[0])
	if len(f) < 2 {
		return SweepSettings{}, fmt.Errorf("sweep: unexpected %q", lines[0])
	}
	var s SweepSettings
	var err error
	if s.Start, err = parseFloat(f[0]); err != nil {
		return s, err
	}
	if s.Stop, err = parseFloat(f[1]); err != nil {
		return s, err
	}
	if len(f) >= 3 {
		if s.Points, err = strconv.Atoi(f[2]); err != nil {
			return s, err
		}
	}
	return s, nil
}

// calFlagNames are the names printed by `cal` (NanoVNA-D items[] order).
var calFlagNames = map[string]bool{
	"load": true, "open": true, "short": true, "thru": true, "isoln": true,
	"Es": true, "Er": true, "Et": true, "cal'ed": true,
}

// ParseCalStatus parses the output of `cal` with no arguments, which lists
// the set status bits by name, e.g. "load open short Es Er Et cal'ed".
// Empty output means no calibration data at all.
func ParseCalStatus(lines []string) vna.CalStatus {
	st := vna.CalStatus{Raw: strings.Join(lines, "\n"), Flags: []string{}}
	known := true
	for _, l := range lines {
		for _, f := range strings.Fields(l) {
			if !calFlagNames[f] {
				known = false
				continue
			}
			st.Flags = append(st.Flags, f)
			switch f {
			case "load":
				st.Load = true
			case "open":
				st.Open = true
			case "short":
				st.Short = true
			case "thru":
				st.Thru = true
			case "isoln":
				st.Isoln = true
			case "cal'ed":
				st.Applied = true
			}
		}
	}
	st.Known = known
	return st
}

// ParsePointsLimit extracts N from "sweep points exceeds range N".
func ParsePointsLimit(line string) (int, bool) {
	i := strings.Index(line, "exceeds range")
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(line[i+len("exceeds range"):])
	if len(f) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.Trim(f[0], " .:[]"))
	if err != nil || n < 2 {
		return 0, false
	}
	return n, true
}

// ParseInfo extracts the board name from `info` output ("Board: NanoVNA-H 4").
func ParseInfo(lines []string) (board string) {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(t, "Board:"); ok {
			return strings.TrimSpace(v)
		}
	}
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

// Capabilities guesses per-model limits from the board name. These are the
// NanoVNA-D defaults; the driver also learns the point limit from the
// firmware's "exceeds range" error.
func Capabilities(board string) (maxPoints, calSlots int, maxFreq float64) {
	b := strings.ToUpper(strings.ReplaceAll(board, " ", ""))
	switch {
	case strings.Contains(b, "NANOVNA-H4"), strings.Contains(b, "H4"):
		return 401, 7, 2.7e9
	case strings.Contains(b, "NANOVNA-H"):
		return 101, 5, 2.7e9 // usable range of an -H is ~1.5 GHz, but firmware accepts more
	}
	return 101, 5, 1.5e9
}

func parseFloat(s string) (float64, error) {
	switch strings.ToLower(s) {
	case "nan", "-nan", "+nan":
		return math.NaN(), nil
	case "inf", "+inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(s, 64)
}

func parseComplex(re, im string) (complex128, error) {
	r, err := parseFloat(re)
	if err != nil {
		return 0, err
	}
	i, err := parseFloat(im)
	if err != nil {
		return 0, err
	}
	return complex(r, i), nil
}

// ParseHelp returns the set of command names listed by `help`
// (NanoVNA-D prints "Commands: version reset ..." possibly over several lines).
func ParseHelp(lines []string) map[string]bool {
	cmds := map[string]bool{}
	for _, l := range lines {
		for _, f := range strings.Fields(l) {
			f = strings.TrimSuffix(f, ",")
			if strings.HasSuffix(f, ":") || f == "" {
				continue
			}
			cmds[f] = true
		}
	}
	return cmds
}

// ParseProfileLine parses one profile record of the PiX firmware's
// `profile` output: "name start_hz stop_hz points slot cal(0|1) active(0|1)".
// The older 6-field form without points is accepted too. The custom line
// ("custom start stop points slot saved active") has the same shape.
func ParseProfileLine(line string) (vna.DeviceProfile, error) {
	f := strings.Fields(line)
	if len(f) != 6 && len(f) != 7 {
		return vna.DeviceProfile{}, fmt.Errorf("profile: want 7 fields, got %q", line)
	}
	if len(f) == 6 { // no points column
		f = append(f[:3], append([]string{"0"}, f[3:]...)...)
	}
	var p vna.DeviceProfile
	var err error
	p.Name = f[0]
	if p.Start, err = strconv.ParseFloat(f[1], 64); err != nil {
		return p, fmt.Errorf("profile start: %w", err)
	}
	if p.Stop, err = strconv.ParseFloat(f[2], 64); err != nil {
		return p, fmt.Errorf("profile stop: %w", err)
	}
	if p.Points, err = strconv.Atoi(f[3]); err != nil {
		return p, fmt.Errorf("profile points: %w", err)
	}
	if p.Slot, err = strconv.Atoi(f[4]); err != nil {
		return p, fmt.Errorf("profile slot: %w", err)
	}
	flag := func(s string) (bool, error) {
		switch s {
		case "0":
			return false, nil
		case "1":
			return true, nil
		}
		return false, fmt.Errorf("profile flag %q not 0/1", s)
	}
	if p.Cal, err = flag(f[5]); err != nil {
		return p, err
	}
	if p.Active, err = flag(f[6]); err != nil {
		return p, err
	}
	return p, nil
}

// ParseBootLine parses "boot <name> ask=<0|1>".
func ParseBootLine(line string) (vna.DeviceBoot, bool) {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "boot" {
		return vna.DeviceBoot{}, false
	}
	b := vna.DeviceBoot{Name: f[1]}
	for _, x := range f[2:] {
		if v, ok := strings.CutPrefix(x, "ask="); ok {
			b.Ask = v == "1"
		}
	}
	return b, true
}

// ParseProfiles parses the `profile` listing: profile records, the custom
// record and the boot line. Unknown lines are skipped.
func ParseProfiles(lines []string) vna.DeviceProfiles {
	out := vna.DeviceProfiles{Profiles: []vna.DeviceProfile{}}
	for _, l := range lines {
		if b, ok := ParseBootLine(l); ok {
			out.Boot = &b
			continue
		}
		p, err := ParseProfileLine(l)
		if err != nil {
			continue
		}
		if p.Name == "custom" {
			out.Custom = &p
			continue
		}
		out.Profiles = append(out.Profiles, p)
	}
	return out
}

// ParseFork returns the text after "Fork:" in version/info output, if any.
func ParseFork(lines []string) string {
	for _, l := range lines {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Fork:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// IsEasyVNAFork reports whether a fork string names the profile firmware:
// "EasyVNA by PiX (...)" (current name) or "PiX (...)" (older builds).
func IsEasyVNAFork(fork string) bool {
	f := strings.ToLower(fork)
	return strings.Contains(f, "easyvna") || strings.Contains(f, "pix")
}
