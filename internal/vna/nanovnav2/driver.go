package nanovnav2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"sync"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
)

// Transport is the byte stream to the device. Read may return (0, nil)
// when nothing arrived within its read timeout (a serial port opened with
// VTIME, or webserial.Conn), which lets the driver honour deadlines.
type Transport interface {
	io.ReadWriteCloser
}

// Options tune the driver.
type Options struct {
	PortName string // for Info only
	Mock     bool
	// SegmentPoints is the number of points asked per hardware sweep
	// (default 101); longer requests are split, which the caller does not see.
	SegmentPoints int
	// Trace, if set, receives every command and the raw reply.
	Trace func(cmd string, raw []byte)
}

// Driver implements vna.Device for the V2 binary protocol. Its data are
// uncalibrated: Info.HostCal asks the caller to calibrate on the host.
type Driver struct {
	mu   sync.Mutex
	t    Transport
	opt  Options
	info vna.Info
	buf  []byte
}

var _ vna.Device = (*Driver)(nil)

// ErrNoReply is returned when the device does not answer in time.
var ErrNoReply = errors.New("nanovna-v2: no reply from the device")

// Open resets the command parser and identifies the device.
func Open(ctx context.Context, t Transport, opt Options) (*Driver, error) {
	if opt.SegmentPoints <= 1 {
		opt.SegmentPoints = 101
	}
	d := &Driver{t: t, opt: opt, buf: make([]byte, 4096)}
	if err := d.write(make([]byte, resetSequenceSize)); err != nil {
		return nil, err
	}
	d.drain(60 * time.Millisecond)
	// INDICATE answers '2' on V2 firmware; a missing answer is not fatal
	// (the register reads below decide).
	indicated := false
	if err := d.write([]byte{OpIndicate}); err != nil {
		return nil, err
	}
	if b, err := d.readN(ctx, 1, time.Now().Add(500*time.Millisecond)); err == nil && b[0] == IndicateReply {
		indicated = true
	}
	d.drain(30 * time.Millisecond)
	reg := func(r byte) (byte, error) {
		if err := d.write(cmdRead(r)); err != nil {
			return 0, err
		}
		b, err := d.readN(ctx, 1, time.Now().Add(time.Second))
		if err != nil {
			return 0, fmt.Errorf("identify (register 0x%02x): %w", r, err)
		}
		// A register read answers exactly one byte; a text shell echoes
		// and prints a prompt.
		if d.pending(25*time.Millisecond) > 0 {
			return 0, errors.New("unexpected extra bytes in the reply")
		}
		return b[0], nil
	}
	var vals [5]byte
	for i, r := range []byte{RegDeviceVariant, RegProtocolVer, RegHardwareRev, RegFirmwareMajor, RegFirmwareMinor} {
		v, err := reg(r)
		if err != nil {
			return nil, fmt.Errorf("not a NanoVNA V2 (no proper answer to the binary protocol): %w", err)
		}
		vals[i] = v
	}
	variant, proto, hw, fwMaj, fwMin := vals[0], vals[1], vals[2], vals[3], vals[4]
	if !indicated && proto != 1 {
		return nil, fmt.Errorf("not a NanoVNA V2 (no INDICATE answer, protocol version %d)", proto)
	}
	if fwMaj == firmwareMajorDFU {
		return nil, errors.New("the NanoVNA V2 is in bootloader (DFU) mode: power-cycle it without holding the button")
	}
	model := "NanoVNA V2"
	if variant != VariantV2 {
		model = fmt.Sprintf("NanoVNA V2 family (variant %d)", variant)
	}
	d.info = vna.Info{
		Driver:       "nanovna-v2",
		Model:        model,
		Version:      fmt.Sprintf("%d.%d", fwMaj, fwMin),
		Port:         opt.PortName,
		Raw:          []string{fmt.Sprintf("variant %d, protocol %d, hardware revision %d, firmware %d.%d (binary protocol, experimental driver)", variant, proto, hw, fwMaj, fwMin)},
		MaxPoints:    opt.SegmentPoints,
		MinFreq:      50e3,
		MaxFreq:      3e9,
		CalSlots:     7,
		TwoPort:      true,
		Mock:         opt.Mock,
		HostCal:      true,
		Experimental: true,
	}
	return d, nil
}

func (d *Driver) write(b []byte) error {
	_, err := d.t.Write(b)
	return err
}

// readN reads exactly n bytes before the deadline.
func (d *Driver) readN(ctx context.Context, n int, deadline time.Time) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("%w (%d of %d bytes)", ErrNoReply, len(out), n)
		}
		want := n - len(out)
		if want > len(d.buf) {
			want = len(d.buf)
		}
		k, err := d.t.Read(d.buf[:want])
		out = append(out, d.buf[:k]...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// pending counts (and discards) the bytes that arrive within dur.
func (d *Driver) pending(dur time.Duration) int {
	n := 0
	end := time.Now().Add(dur)
	for time.Now().Before(end) {
		k, err := d.t.Read(d.buf)
		n += k
		if err != nil || k == 0 {
			return n
		}
	}
	return n
}

// drain discards input for up to dur.
func (d *Driver) drain(dur time.Duration) {
	end := time.Now().Add(dur)
	for time.Now().Before(end) {
		n, err := d.t.Read(d.buf)
		if err != nil || n == 0 {
			return
		}
	}
}

// Info implements vna.Device.
func (d *Driver) Info(ctx context.Context) (vna.Info, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.info, nil
}

// Scan implements vna.Device: segments of SegmentPoints points.
func (d *Driver) Scan(ctx context.Context, req vna.ScanRequest) (*vna.Sweep, error) {
	if err := req.Validate(d.info); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t0 := time.Now()
	grid := req.Grid()
	sw := &vna.Sweep{Started: t0}
	// Even segments (sizes differ by at most one), so none has one point.
	k := (len(grid) + d.opt.SegmentPoints - 1) / d.opt.SegmentPoints
	for i := 0; i < k; i++ {
		a, b := i*len(grid)/k, (i+1)*len(grid)/k
		freq, s11, s21, err := d.sweep(ctx, grid[a:b])
		if err != nil {
			return nil, err
		}
		sw.Freq = append(sw.Freq, freq...)
		sw.S11 = append(sw.S11, s11...)
		if req.S21 {
			sw.S21 = append(sw.S21, s21...)
		}
	}
	sw.Duration = time.Since(t0)
	return sw, nil
}

// sweep measures one segment. The device sweeps continuously and pushes
// one record per point into the FIFO; records are placed by their index,
// starting from the first record with index 0 (anything earlier may belong
// to the previous settings).
func (d *Driver) sweep(ctx context.Context, grid []float64) ([]float64, []complex128, []complex128, error) {
	n := len(grid)
	start := uint64(math.Round(grid[0]))
	step := uint64(0)
	if n > 1 {
		step = uint64(math.Round((grid[n-1] - grid[0]) / float64(n-1)))
	}
	cmds := [][]byte{cmdWrite8(RegSweepStart, start), cmdWrite8(RegSweepStep, step), cmdWrite2(RegSweepPoints, uint16(n)),
		cmdWrite2(RegValuesPerFreq, 1), cmdWrite(RegValuesFIFO, 0)}
	for _, c := range cmds {
		if err := d.write(c); err != nil {
			return nil, nil, nil, err
		}
	}
	freq := make([]float64, n)
	for i := range freq {
		freq[i] = float64(start + uint64(i)*step)
	}
	s11 := make([]complex128, n)
	s21 := make([]complex128, n)
	got := make([]bool, n)
	filled, synced := 0, false
	deadline := time.Now().Add(5*time.Second + time.Duration(4*n)*10*time.Millisecond)
	reads := 0
	for filled < n {
		if time.Now().After(deadline) {
			return nil, nil, nil, fmt.Errorf("nanovna-v2: sweep timed out (%d of %d points)", filled, n)
		}
		k := min(n-filled, MaxFIFORead)
		if !synced {
			k = min(n, MaxFIFORead)
		}
		if err := d.write(cmdReadFIFO(RegValuesFIFO, k)); err != nil {
			return nil, nil, nil, err
		}
		raw, err := d.readN(ctx, k*RecordSize, deadline)
		if d.opt.Trace != nil {
			d.opt.Trace(fmt.Sprintf("READFIFO %d", k), raw)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		reads++
		for off := 0; off+RecordSize <= len(raw); off += RecordSize {
			r := ParseRecord(raw[off : off+RecordSize])
			if r.FreqIndex == 0 {
				synced = true
			}
			if !synced || r.FreqIndex >= n || got[r.FreqIndex] {
				continue
			}
			got[r.FreqIndex] = true
			filled++
			s11[r.FreqIndex], s21[r.FreqIndex] = sanitize(r.S11()), sanitize(r.S21())
		}
		if reads > 4*(n/MaxFIFORead+2)+8 {
			return nil, nil, nil, fmt.Errorf("nanovna-v2: incomplete sweep (%d of %d points after %d reads)", filled, n, reads)
		}
	}
	return freq, s11, s21, nil
}

func sanitize(c complex128) complex128 {
	if cmplx.IsNaN(c) || cmplx.IsInf(c) {
		return 0
	}
	return c
}

// CalStatus implements vna.Device: the device has no calibration of its own.
func (d *Driver) CalStatus(ctx context.Context) (vna.CalStatus, error) {
	return vna.CalStatus{Known: true, Raw: "raw data (the V2 sends uncalibrated values; EasyVNA Web calibrates)"}, nil
}

// ErrHostCal is returned for on-device calibration commands.
var ErrHostCal = errors.New("nanovna-v2: calibration is done by EasyVNA Web, not on the device")

// Calibrate implements vna.Device (not supported on the device).
func (d *Driver) Calibrate(ctx context.Context, step vna.CalStep, rng *vna.ScanRequest) error {
	return ErrHostCal
}

// SaveCal implements vna.Device (not supported on the device).
func (d *Driver) SaveCal(ctx context.Context, slot int) error { return ErrHostCal }

// RecallCal implements vna.Device (not supported on the device).
func (d *Driver) RecallCal(ctx context.Context, slot int) error { return ErrHostCal }

// Close implements vna.Device.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.t.Close()
}
