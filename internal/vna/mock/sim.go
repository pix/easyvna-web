// Package mock simulates a NanoVNA-H4 running NanoVNA-D firmware at the
// byte level: it speaks the same ChibiOS text shell (echo, "\r\n" line
// endings, "ch> " prompt) so the real nanovnah driver runs unmodified on top
// of it. The device under test is a small antenna modelled as a series RLC
// behind a 10 cm coax pigtail.
package mock

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configure the simulated device.
type Options struct {
	Board     string // default "NanoVNA-H 4"
	MaxPoints int    // default 401
	// PointTime is the simulated measurement time per point (default 0.4 ms).
	PointTime time.Duration
	Antenna   Antenna
	Noise     float64 // std-dev of additive noise on S11 re/im (default 0.0008 ≈ 0.0016 RMS |ΔΓ| sweep to sweep, like a real H4)
	Seed      uint64
	// NoEcho disables character echo (some tests use it).
	NoEcho bool
	// Profiles simulates the EasyVNA (PiX) fork: `profile` command and a Fork: line.
	Profiles bool
}

// SimProfile is an on-device profile of the PiX firmware.
type SimProfile struct {
	Name        string
	Start, Stop float64
	Points      int
	Slot        int
}

// FirmwareProfiles mirror the PiX NanoVNA-H4 firmware v2 profile table.
// Slots 0 and 1 are left for the user (stock firmware loads slot 0 at boot).
var FirmwareProfiles = []SimProfile{
	{"wide", 300e6, 1000e6, 401, 3},
	{"433", 388.92e6, 478.92e6, 401, 4},
	{"869", 779e6, 959e6, 401, 5},
	{"915", 825e6, 1005e6, 401, 6},
}

// CustomSlot holds the custom profile; its range is whatever was saved there.
const CustomSlot = 2

// slotData is a saved calibration slot (status bits plus the sweep range).
type slotData struct {
	status      uint32
	start, stop float64
	points      int
}

// Sim is an in-memory NanoVNA shell. It implements io.ReadWriteCloser.
type Sim struct {
	opt Options

	mu     sync.Mutex
	cond   *sync.Cond
	out    bytes.Buffer
	line   []byte
	closed bool
	rng    *rand.Rand
	t0     time.Time

	start, stop float64
	points      int
	paused      bool
	calStatus   uint32
	slots       map[int]slotData
	lastFreq    []float64
	lastS11     []complex128
	lastS21     []complex128
	activeProf  string

	// Log records every command line received (for tests).
	Log []string
}

// Calibration status bits (NanoVNA-D nanovna.h).
const (
	calLoad  = 1 << 0
	calOpen  = 1 << 1
	calShort = 1 << 2
	calThru  = 1 << 3
	calIsoln = 1 << 4
	calEs    = 1 << 5
	calEr    = 1 << 6
	calEt    = 1 << 7
	calApply = 1 << 8
)

var calNames = []string{"load", "open", "short", "thru", "isoln", "Es", "Er", "Et", "cal'ed"}

// New creates a simulator that has just booted with calibration slot 0
// (a full 1-port SOL + thru calibration) loaded.
func New(opt Options) *Sim {
	if opt.Board == "" {
		opt.Board = "NanoVNA-H 4"
	}
	if opt.MaxPoints == 0 {
		opt.MaxPoints = 401
	}
	if opt.PointTime == 0 {
		opt.PointTime = 400 * time.Microsecond
	}
	if opt.Antenna.Name == "" {
		opt.Antenna = DefaultAntenna()
	}
	if opt.Noise == 0 {
		opt.Noise = 0.0008
	}
	if opt.Seed == 0 {
		opt.Seed = uint64(time.Now().UnixNano())
	}
	full := uint32(calLoad | calOpen | calShort | calThru | calIsoln | calEs | calEr | calEt | calApply)
	s := &Sim{
		opt: opt, rng: rand.New(rand.NewPCG(opt.Seed, 1)), t0: time.Now(),
		start: 50e3, stop: 900e6, points: opt.MaxPoints,
		calStatus: full, slots: map[int]slotData{
			0: {full, 50e3, 900e6, opt.MaxPoints},   // user's power-on calibration
			3: {full, 300e6, 1000e6, opt.MaxPoints}, // wide profile already calibrated
		},
	}
	s.cond = sync.NewCond(&s.mu)
	s.out.WriteString("\r\nNanoVNA Shell\r\n" + "ch> ")
	return s
}

// SetAntenna swaps the simulated antenna (as if the user connected another one).
func (s *Sim) SetAntenna(a Antenna) {
	s.mu.Lock()
	s.opt.Antenna = a
	s.mu.Unlock()
}

// Read returns pending output, waiting up to 100 ms (like a serial port with
// VTIME=1). It returns (0, nil) when nothing arrived in that time.
func (s *Sim) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out.Len() == 0 && !s.closed {
		timer := time.AfterFunc(100*time.Millisecond, func() {
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		})
		s.cond.Wait()
		timer.Stop()
	}
	if s.closed {
		return 0, io.EOF
	}
	return s.out.Read(p)
}

// Write feeds characters to the shell's line editor.
func (s *Sim) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	var pending []string
	for _, c := range p {
		switch {
		case c == '\r':
			if !s.opt.NoEcho {
				s.out.WriteString("\r\n")
			}
			pending = append(pending, string(s.line))
			s.line = s.line[:0]
		case c == 0x08 || c == 0x7f:
			if len(s.line) > 0 {
				s.line = s.line[:len(s.line)-1]
				s.out.Write([]byte{0x08, 0x20, 0x08})
			}
		case c < ' ' || len(s.line) >= 63:
			// ignored, like the firmware
		default:
			s.line = append(s.line, c)
			if !s.opt.NoEcho {
				s.out.WriteByte(c)
			}
		}
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	// Execute outside the lock so slow "measurements" don't block Read.
	for _, l := range pending {
		out := s.execute(l)
		s.mu.Lock()
		s.out.WriteString(out)
		s.out.WriteString("ch> ")
		s.cond.Broadcast()
		s.mu.Unlock()
	}
	return len(p), nil
}

// Close makes subsequent reads return io.EOF.
func (s *Sim) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

const nl = "\r\n"

func (s *Sim) execute(line string) string {
	args := strings.Fields(line)
	s.mu.Lock()
	s.Log = append(s.Log, line)
	s.mu.Unlock()
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "version":
		if s.opt.Profiles {
			return s.version() + nl + "Fork: EasyVNA by PiX (vnaview mock)" + nl
		}
		return s.version() + nl
	case "info":
		return strings.Join([]string{
			"Board: " + s.opt.Board,
			s.forkLine(),
			"2019-2024 Copyright NanoVNA.com",
			"based on @DiSlord @edy555 ... source",
			"Licensed under GPL.",
			"  https://github.com/DiSlord/NanoVNA-D",
			"Version: " + s.version() + " [p:" + strconv.Itoa(s.opt.MaxPoints) + ", IF:12k, ADC:384k, Lcd:480x320]",
			"Build Time: simulated by vnaview -mock",
			"Platform: vnaview simulator",
		}, nl) + nl
	case "pause":
		s.mu.Lock()
		s.paused = true
		s.mu.Unlock()
		return ""
	case "resume":
		s.mu.Lock()
		s.paused = false
		s.mu.Unlock()
		return ""
	case "sweep":
		return s.cmdSweep(args[1:])
	case "scan":
		return s.cmdScan(args[1:])
	case "frequencies":
		s.mu.Lock()
		defer s.mu.Unlock()
		var b strings.Builder
		for _, f := range s.lastFreq {
			fmt.Fprintf(&b, "%d"+nl, int64(f))
		}
		return b.String()
	case "data":
		sel := 0
		if len(args) > 1 {
			sel, _ = strconv.Atoi(args[1])
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		arr := s.lastS11
		switch sel {
		case 0:
		case 1:
			arr = s.lastS21
		default:
			return "usage: data [array]" + nl
		}
		var b strings.Builder
		for _, c := range arr {
			fmt.Fprintf(&b, "%f %f"+nl, real(c), imag(c))
		}
		return b.String()
	case "cal":
		return s.cmdCal(args[1:])
	case "help":
		cmds := "help exit info echo systime threads reset version capture vbat tcxo touchcal touchtest pause resume cal save recall trace marker edelay smooth port s21offset freq dac sweep_voltage saveconfig clearconfig data frequencies scan scan_bin sweep test gain power sample bandwidth lcd brightness sd_list sd_read sd_delete"
		if s.opt.Profiles {
			cmds += " profile"
		}
		return "Commands: " + cmds + nl
	case "profile":
		if s.opt.Profiles {
			return s.cmdProfile(args[1:])
		}
	case "save", "recall":
		if len(args) < 2 {
			return fmt.Sprintf("usage: %s 0..%d"+nl, args[0], s.numSlots()-1)
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n >= s.numSlots() {
			return fmt.Sprintf("usage: %s 0..%d"+nl, args[0], s.numSlots()-1)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if args[0] == "save" {
			s.slots[n] = slotData{s.calStatus, s.start, s.stop, s.points}
			return ""
		}
		if st, ok := s.slots[n]; ok {
			s.calStatus, s.start, s.stop, s.points = st.status, st.start, st.stop, st.points
			return ""
		}
		s.calStatus = 0
		return "Err, default load" + nl
	}
	return args[0] + "?" + nl
}

func (s *Sim) cmdProfile(a []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	line := func(p SimProfile) string {
		_, cal := s.slots[p.Slot]
		return fmt.Sprintf("%s %d %d %d %d %d %d"+nl, p.Name, int64(p.Start), int64(p.Stop), p.Points, p.Slot, b(cal), b(s.activeProf == p.Name))
	}
	custom := func() string {
		cs, saved := s.slots[CustomSlot]
		return fmt.Sprintf("custom %d %d %d %d %d %d"+nl, int64(cs.start), int64(cs.stop), cs.points, CustomSlot, b(saved), b(s.activeProf == "custom"))
	}
	if len(a) == 0 {
		var out strings.Builder
		for _, p := range FirmwareProfiles {
			out.WriteString(line(p))
		}
		out.WriteString(custom())
		out.WriteString("boot wide ask=0" + nl)
		return out.String()
	}
	if a[0] == "custom" {
		if cs, ok := s.slots[CustomSlot]; ok {
			s.start, s.stop, s.points, s.calStatus = cs.start, cs.stop, cs.points, cs.status
		}
		s.activeProf = "custom"
		return custom()
	}
	for _, p := range FirmwareProfiles {
		if p.Name == a[0] {
			s.start, s.stop, s.points, s.activeProf = p.Start, p.Stop, p.Points, p.Name
			if st, ok := s.slots[p.Slot]; ok {
				s.calStatus = st.status
			}
			return line(p)
		}
	}
	return "usage: profile [wide|433|869|915|custom]" + nl
}

func (s *Sim) version() string {
	if s.opt.Profiles {
		return "1.2.50-mock"
	}
	return "1.2.40-mock"
}

func (s *Sim) forkLine() string {
	if s.opt.Profiles {
		return "Fork: EasyVNA by PiX (vnaview mock)"
	}
	return "Licensed under GPL."
}

func (s *Sim) numSlots() int {
	if s.opt.MaxPoints > 101 {
		return 7
	}
	return 5
}

func (s *Sim) cmdSweep(a []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(a) == 0 {
		return fmt.Sprintf("%d %d %d"+nl, int64(s.start), int64(s.stop), s.points)
	}
	if len(a) > 3 {
		return "usage: sweep {start(Hz)} [stop(Hz)] [points]" + nl
	}
	v := make([]float64, len(a))
	for i, x := range a {
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return "usage: sweep {start(Hz)} [stop(Hz)] [points]" + nl
		}
		v[i] = f
	}
	s.start = v[0]
	if len(v) > 1 {
		s.stop = v[1]
	}
	if len(v) > 2 {
		p := int(v[2])
		if p < 1 || p > s.opt.MaxPoints {
			p = s.opt.MaxPoints
		}
		s.points = p
	}
	return ""
}

func (s *Sim) cmdScan(a []string) string {
	usage := "usage: scan {start(Hz)} {stop(Hz)} [points] [outmask]" + nl
	if len(a) < 2 || len(a) > 4 {
		return usage
	}
	start, err1 := strconv.ParseFloat(a[0], 64)
	stop, err2 := strconv.ParseFloat(a[1], 64)
	if err1 != nil || err2 != nil {
		return usage
	}
	if start == 0 || stop == 0 || start > stop {
		return "frequency range is invalid" + nl
	}
	points := s.opt.MaxPoints
	if len(a) >= 3 {
		p, err := strconv.Atoi(a[2])
		if err != nil || p <= 0 || p > s.opt.MaxPoints {
			return fmt.Sprintf("sweep points exceeds range %d"+nl, s.opt.MaxPoints)
		}
		points = p
	}
	mask := 0
	if len(a) == 4 {
		m, err := strconv.ParseUint(a[3], 0, 16)
		if err != nil {
			return usage
		}
		mask = int(m)
	}
	freqs := make([]float64, points)
	s11 := make([]complex128, points)
	s21 := make([]complex128, points)
	s.mu.Lock()
	applied := s.calStatus&calApply != 0
	ant := s.opt.Antenna
	s.mu.Unlock()
	t := time.Since(s.t0).Seconds()
	for i := range freqs {
		f := start
		if points > 1 {
			f = start + math.Floor((stop-start)*float64(i)/float64(points-1))
		}
		freqs[i] = f
		g, tr := ant.Measure(f, t)
		if !applied || mask&8 != 0 {
			g = rawError(g, f)
		}
		s11[i] = g + s.noise(s.opt.Noise)
		s21[i] = tr + s.noise(s.opt.Noise/20)
	}
	time.Sleep(time.Duration(points) * s.opt.PointTime)
	s.mu.Lock()
	s.lastFreq, s.lastS11, s.lastS21 = freqs, s11, s21
	s.paused = true // NanoVNA-D pauses the sweep after scan
	s.mu.Unlock()
	if mask&0x80 != 0 {
		return "binary output is not simulated" + nl
	}
	if mask&7 == 0 {
		return ""
	}
	var b strings.Builder
	for i := range freqs {
		if mask&1 != 0 {
			fmt.Fprintf(&b, "%d ", int64(freqs[i]))
		}
		if mask&2 != 0 {
			fmt.Fprintf(&b, "%.9f %.9f ", real(s11[i]), imag(s11[i]))
		}
		if mask&4 != 0 {
			fmt.Fprintf(&b, "%.9f %.9f ", real(s21[i]), imag(s21[i]))
		}
		b.WriteString(nl)
	}
	return b.String()
}

func (s *Sim) cmdCal(a []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(a) == 0 {
		var names []string
		for i, n := range calNames {
			if s.calStatus&(1<<i) != 0 {
				names = append(names, n)
			}
		}
		return strings.Join(names, " ") + nl
	}
	bit := map[string]uint32{"load": calLoad, "open": calOpen, "short": calShort, "thru": calThru, "isoln": calIsoln}
	switch a[0] {
	case "load", "open", "short", "thru", "isoln":
		s.mu.Unlock()
		time.Sleep(time.Duration(s.points) * s.opt.PointTime * 2) // measuring the standard
		s.mu.Lock()
		s.calStatus |= bit[a[0]]
		s.calStatus &^= calApply
		return ""
	case "done":
		s.calStatus |= calEs | calEr | calEt | calApply
		return ""
	case "on":
		s.calStatus |= calApply
		return ""
	case "off":
		s.calStatus &^= calApply
		return ""
	case "reset":
		s.calStatus = 0
		return ""
	}
	return "usage: cal [load|open|short|thru|isoln|done|on|off|reset]" + nl
}

func (s *Sim) noise(sd float64) complex128 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return complex(s.rng.NormFloat64()*sd, s.rng.NormFloat64()*sd)
}

// rawError applies a fixed 1-port error model (directivity, source match,
// tracking) so uncalibrated data visibly differs from calibrated data.
func rawError(g complex128, f float64) complex128 {
	w := 2 * math.Pi * f
	e00 := cmplx.Rect(0.08, -w*0.3e-9)
	e11 := cmplx.Rect(0.12, -w*0.5e-9)
	e10e01 := cmplx.Rect(0.85, -w*1.2e-9)
	return e00 + e10e01*g/(1-e11*g)
}
