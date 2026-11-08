package nanovnah

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
)

// Options tune the driver.
type Options struct {
	PortName string // for Info only
	// MaxPoints overrides the per-scan point limit (0 = auto-detect).
	MaxPoints int
	// CalSettle is how long the device sweeps with a standard attached before
	// `cal <std>` is sent. NanoVNA-D runs its own sweep inside `cal`, but
	// older firmware copies the last continuous-sweep result, so give it time
	// to complete at least one full sweep.
	CalSettle time.Duration
	Mock      bool
	// ReadOnly refuses every command that changes stored device state
	// (cal, save, recall, profile apply). Sweeps are still allowed.
	ReadOnly bool
	// Trace, if set, receives every raw shell exchange, including the
	// identification at connect time.
	Trace func(cmd string, raw []byte)
}

// ErrReadOnly is returned for state-changing commands in read-only mode.
var ErrReadOnly = errors.New("nanovna: read-only mode (cal/save/recall/profile are disabled)")

// Driver implements vna.Device for NanoVNA-H / H4 shell firmware.
type Driver struct {
	mu        sync.Mutex
	sh        *Shell
	t         Transport
	opt       Options
	info      vna.Info
	maxPoints int
	legacy    bool // firmware without scan outmask support
	commands  map[string]bool
}

var (
	_ vna.Device   = (*Driver)(nil)
	_ vna.Profiler = (*Driver)(nil)
)

// Open syncs with the shell on t and identifies the device.
func Open(ctx context.Context, t Transport, opt Options) (*Driver, error) {
	if opt.CalSettle == 0 {
		opt.CalSettle = 1500 * time.Millisecond
	}
	d := &Driver{sh: NewShell(t), t: t, opt: opt}
	d.sh.Trace = opt.Trace
	if err := d.sh.Sync(ctx); err != nil {
		return nil, err
	}
	if err := d.identify(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// Shell exposes the underlying shell (for diagnostics and tests).
func (d *Driver) Shell() *Shell { return d.sh }

func (d *Driver) identify(ctx context.Context) error {
	ver, err := d.sh.Exec(ctx, "version", 3*time.Second)
	if err != nil {
		return fmt.Errorf("identify: %w", err)
	}
	info, err := d.sh.Exec(ctx, "info", 3*time.Second)
	if err != nil && !errors.Is(err, ErrUnknownCommand) {
		return fmt.Errorf("identify: %w", err)
	}
	// `help` tells us about optional commands (e.g. the custom firmware's
	// `profile`). Its absence is not an error.
	if hl, err := d.sh.Exec(ctx, "help", 3*time.Second); err == nil {
		d.commands = ParseHelp(hl)
	} else if !errors.Is(err, ErrUnknownCommand) {
		return fmt.Errorf("identify: %w", err)
	}
	board := ParseInfo(info)
	if board == "" {
		board = "NanoVNA (unknown board)"
	}
	maxPts, slots, maxF := Capabilities(board)
	if d.opt.MaxPoints > 0 {
		maxPts = d.opt.MaxPoints
	}
	d.maxPoints = maxPts
	fork := ParseFork(ver)
	if fork == "" {
		fork = ParseFork(info)
	}
	var verLines []string
	for _, l := range ver {
		if !strings.HasPrefix(strings.TrimSpace(l), "Fork:") {
			verLines = append(verLines, strings.TrimSpace(l))
		}
	}
	d.info = vna.Info{
		Driver:    "nanovna-h",
		Model:     board,
		Version:   strings.Join(verLines, " "),
		Fork:      fork,
		Port:      d.opt.PortName,
		Raw:       info,
		MaxPoints: maxPts,
		MinFreq:   10e3,
		MaxFreq:   maxF,
		CalSlots:  slots,
		TwoPort:   true,
		Mock:      d.opt.Mock,
		DFUReboot: d.commands["reset"],
	}
	return nil
}

// Info implements vna.Device.
func (d *Driver) Info(ctx context.Context) (vna.Info, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inf := d.info
	inf.MaxPoints = d.maxPoints
	return inf, nil
}

// segments splits n points into chunks of at most max points, each with at
// least 2 points, as index ranges [a, b].
func segments(n, max int) [][2]int {
	if max < 2 {
		max = 2
	}
	if n <= max {
		return [][2]int{{0, n - 1}}
	}
	k := (n + max - 1) / max
	out := make([][2]int, 0, k)
	a := 0
	for i := 0; i < k; i++ {
		size := (n - a) / (k - i) // spread the remainder
		if (n-a)%(k-i) != 0 {
			size++
		}
		b := a + size - 1
		out = append(out, [2]int{a, b})
		a = b + 1
	}
	return out
}

// Scan implements vna.Device.
func (d *Driver) Scan(ctx context.Context, req vna.ScanRequest) (*vna.Sweep, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := req.Validate(vna.Info{}); err != nil {
		return nil, err
	}
	sw := &vna.Sweep{Started: time.Now()}
	grid := req.Grid()
	for attempt := 0; ; attempt++ {
		sw.Freq, sw.S11, sw.S21 = nil, nil, nil
		err := d.scanGrid(ctx, grid, req.S21, sw)
		var se *ShellError
		if attempt == 0 && errors.As(err, &se) {
			if n, ok := ParsePointsLimit(se.Line); ok && n != d.maxPoints {
				d.maxPoints = n // learnt the real limit; retry with smaller segments
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		break
	}
	sw.Duration = time.Since(sw.Started)
	return sw, nil
}

func (d *Driver) scanGrid(ctx context.Context, grid []float64, s21 bool, sw *vna.Sweep) error {
	for _, seg := range segments(len(grid), d.maxPoints) {
		start, stop, n := grid[seg[0]], grid[seg[1]], seg[1]-seg[0]+1
		if start == stop { // degenerate 1-point segment cannot happen (min 2), but guard
			stop = start + 1
		}
		var pts []ScanPoint
		var err error
		if d.legacy {
			pts, err = d.legacyScan(ctx, start, stop, n, s21)
		} else {
			pts, err = d.textScan(ctx, start, stop, n, s21)
			if err == nil && len(pts) == 0 {
				// Firmware ignored the outmask: fall back to sweep+data.
				d.legacy = true
				pts, err = d.legacyScan(ctx, start, stop, n, s21)
			}
		}
		if err != nil {
			return err
		}
		if len(pts) != n {
			return fmt.Errorf("scan: asked for %d points, got %d", n, len(pts))
		}
		for _, p := range pts {
			sw.Freq = append(sw.Freq, p.Freq)
			sw.S11 = append(sw.S11, sanitize(p.S11))
			if s21 {
				sw.S21 = append(sw.S21, sanitize(p.S21))
			}
		}
	}
	return nil
}

func sanitize(c complex128) complex128 {
	r, i := real(c), imag(c)
	if math.IsNaN(r) || math.IsInf(r, 0) {
		r = 0
	}
	if math.IsNaN(i) || math.IsInf(i, 0) {
		i = 0
	}
	return complex(r, i)
}

// scanTimeout allows for slow IF bandwidth settings.
func scanTimeout(n int) time.Duration { return 10*time.Second + time.Duration(n)*100*time.Millisecond }

func (d *Driver) textScan(ctx context.Context, start, stop float64, n int, s21 bool) ([]ScanPoint, error) {
	mask := MaskFreq | MaskS11
	if s21 {
		mask |= MaskS21
	}
	cmd := fmt.Sprintf("scan %d %d %d %d", int64(start), int64(stop), n, mask)
	lines, err := d.sh.Exec(ctx, cmd, scanTimeout(n))
	if err != nil {
		return nil, err
	}
	return ParseScan(lines, mask)
}

// legacyScan supports firmware whose `scan` has no outmask: set the sweep,
// let it run, pause and read `frequencies` / `data`.
func (d *Driver) legacyScan(ctx context.Context, start, stop float64, n int, s21 bool) ([]ScanPoint, error) {
	if _, err := d.sh.Exec(ctx, fmt.Sprintf("sweep %d %d %d", int64(start), int64(stop), n), 3*time.Second); err != nil {
		return nil, err
	}
	if _, err := d.sh.Exec(ctx, "resume", 3*time.Second); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(d.opt.CalSettle):
	}
	if _, err := d.sh.Exec(ctx, "pause", 3*time.Second); err != nil {
		return nil, err
	}
	fl, err := d.sh.Exec(ctx, "frequencies", 5*time.Second)
	if err != nil {
		return nil, err
	}
	freqs, err := ParseFrequencies(fl)
	if err != nil {
		return nil, err
	}
	dl, err := d.sh.Exec(ctx, "data 0", 5*time.Second)
	if err != nil {
		return nil, err
	}
	s11, err := ParseData(dl)
	if err != nil {
		return nil, err
	}
	var s21v []complex128
	if s21 {
		dl, err := d.sh.Exec(ctx, "data 1", 5*time.Second)
		if err != nil {
			return nil, err
		}
		if s21v, err = ParseData(dl); err != nil {
			return nil, err
		}
	}
	if len(freqs) != len(s11) || (s21 && len(s21v) != len(s11)) {
		return nil, fmt.Errorf("legacy scan: inconsistent lengths %d/%d/%d", len(freqs), len(s11), len(s21v))
	}
	pts := make([]ScanPoint, len(freqs))
	for i := range freqs {
		pts[i] = ScanPoint{Freq: freqs[i], S11: s11[i]}
		if s21 {
			pts[i].S21 = s21v[i]
		}
	}
	return pts, nil
}

// CalStatus implements vna.Device.
func (d *Driver) CalStatus(ctx context.Context) (vna.CalStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	lines, err := d.sh.Exec(ctx, "cal", 3*time.Second)
	if err != nil {
		return vna.CalStatus{}, err
	}
	return ParseCalStatus(lines), nil
}

// Calibrate implements vna.Device.
func (d *Driver) Calibrate(ctx context.Context, step vna.CalStep, rng *vna.ScanRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opt.ReadOnly {
		return ErrReadOnly
	}
	if rng != nil && (step == vna.CalReset || step.Measures()) {
		n := rng.Points
		if n > d.maxPoints {
			n = d.maxPoints
		}
		cmd := fmt.Sprintf("sweep %d %d %d", int64(rng.Start), int64(rng.Stop), n)
		if _, err := d.sh.Exec(ctx, cmd, 3*time.Second); err != nil {
			return err
		}
	}
	if step.Measures() {
		if _, err := d.sh.Exec(ctx, "resume", 3*time.Second); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.opt.CalSettle):
		}
	}
	_, err := d.sh.Exec(ctx, "cal "+string(step), 60*time.Second)
	return err
}

// SaveCal implements vna.Device.
func (d *Driver) SaveCal(ctx context.Context, slot int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opt.ReadOnly {
		return ErrReadOnly
	}
	if slot < 0 || (d.info.CalSlots > 0 && slot >= d.info.CalSlots) {
		return fmt.Errorf("slot %d out of range 0..%d", slot, d.info.CalSlots-1)
	}
	_, err := d.sh.Exec(ctx, fmt.Sprintf("save %d", slot), 10*time.Second)
	return err
}

// RecallCal implements vna.Device.
func (d *Driver) RecallCal(ctx context.Context, slot int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opt.ReadOnly {
		return ErrReadOnly
	}
	if slot < 0 || (d.info.CalSlots > 0 && slot >= d.info.CalSlots) {
		return fmt.Errorf("slot %d out of range 0..%d", slot, d.info.CalSlots-1)
	}
	_, err := d.sh.Exec(ctx, fmt.Sprintf("recall %d", slot), 10*time.Second)
	return err
}

// SupportsProfiles implements vna.Profiler.
// The EasyVNA (formerly PiX) fork is recognised by `help` listing `profile`
// or by a "Fork:" line mentioning EasyVNA or PiX in version/info.
func (d *Driver) SupportsProfiles() bool { return d.commands["profile"] || IsEasyVNAFork(d.info.Fork) }

// Profiles lists the on-device profiles.
func (d *Driver) Profiles(ctx context.Context) (vna.DeviceProfiles, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.SupportsProfiles() {
		return vna.DeviceProfiles{}, errors.ErrUnsupported
	}
	lines, err := d.sh.Exec(ctx, "profile", 3*time.Second)
	if err != nil {
		return vna.DeviceProfiles{}, err
	}
	return ParseProfiles(lines), nil
}

// ApplyProfile runs `profile <name>` and returns the line the firmware prints.
func (d *Driver) ApplyProfile(ctx context.Context, name string) (vna.DeviceProfile, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opt.ReadOnly {
		return vna.DeviceProfile{}, ErrReadOnly
	}
	if !d.SupportsProfiles() {
		return vna.DeviceProfile{}, errors.ErrUnsupported
	}
	if strings.ContainsAny(name, " \t\r\n") || name == "" {
		return vna.DeviceProfile{}, fmt.Errorf("bad profile name %q", name)
	}
	lines, err := d.sh.Exec(ctx, "profile "+name, 10*time.Second)
	if err != nil {
		return vna.DeviceProfile{}, err
	}
	for _, l := range lines {
		if p, err := ParseProfileLine(l); err == nil && p.Name == name {
			return p, nil
		}
	}
	return vna.DeviceProfile{}, fmt.Errorf("profile %s: unexpected reply %q", name, strings.Join(lines, " | "))
}

// RebootDFU implements vna.DFURebooter: `reset dfu` restarts firmware built
// with DFU support (NanoVNA-D on the H4, EasyVNA) in the STM32 bootloader
// (USB 0483:df11). The serial port then goes away, so no prompt comes back
// and a timeout or transport error is expected. Firmware without DFU
// support simply restarts. It changes nothing stored on the device.
func (d *Driver) RebootDFU(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.commands["reset"] {
		return errors.ErrUnsupported
	}
	_, _ = d.sh.ExecRaw(ctx, "reset dfu", 1500*time.Millisecond)
	return nil
}

// Close resumes the on-device sweep (scan pauses it) and closes the port.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _ = d.sh.Exec(ctx, "resume", time.Second)
	cancel()
	return d.t.Close()
}
