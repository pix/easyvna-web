package mock

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"math/cmplx"
	"math/rand/v2"
	"sync"
	"time"
)

// SimV2 simulates a NanoVNA V2 at the byte level: the binary register
// protocol (opcodes, registers, the 32-byte FIFO records) with raw,
// uncalibrated detector values, so the nanovnav2 driver and the host-side
// calibration run unmodified on top of it. It is written independently of
// the driver, from the protocol description.
//
// Like the real device it sweeps continuously: clearing the FIFO does not
// restart the sweep, so the first records after a clear may come from the
// middle of it; writing a sweep register restarts it at index 0 after one
// stale record.
type SimV2 struct {
	opt V2Options

	mu     sync.Mutex
	cond   *sync.Cond
	in     []byte
	out    bytes.Buffer
	closed bool
	rng    *rand.Rand
	t0     time.Time

	start, step uint64
	points      int
	idx         int  // next sweep index pushed into the FIFO
	stale       bool // one record of the old settings is still in the pipe
	Log         []string
}

// V2Options configure SimV2.
type V2Options struct {
	Antenna   Antenna
	Noise     float64 // std-dev of noise on raw S11 (default 0.0008)
	Seed      uint64
	PointTime time.Duration // per FIFO record (default 0.1 ms)
	Variant   byte          // register 0xf0 (default 2)
	FWMajor   byte          // default 1
	FWMinor   byte          // default 3
}

// NewV2 powers on a simulated V2.
func NewV2(opt V2Options) *SimV2 {
	if opt.Noise == 0 {
		opt.Noise = 0.0008
	}
	if opt.PointTime == 0 {
		opt.PointTime = 100 * time.Microsecond
	}
	if opt.Variant == 0 {
		opt.Variant = 2
	}
	if opt.FWMajor == 0 {
		opt.FWMajor, opt.FWMinor = 1, 3
	}
	if len(opt.Antenna.Modes) == 0 && !opt.Antenna.Open {
		opt.Antenna = DefaultAntenna()
	}
	s := &SimV2{opt: opt, rng: rand.New(rand.NewPCG(opt.Seed, 0x5eed)), t0: time.Now(),
		start: 200e6, step: 1e6, points: 101}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// SetAntenna swaps the device under test.
func (s *SimV2) SetAntenna(a Antenna) {
	s.mu.Lock()
	s.opt.Antenna = a
	s.mu.Unlock()
}

// Read returns pending output, waiting up to 100 ms; (0, nil) on timeout.
func (s *SimV2) Read(p []byte) (int, error) {
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

// Close makes later reads return io.EOF.
func (s *SimV2) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// cmdLen is the length of a complete command starting with op (0 = need
// more bytes to know).
func cmdLen(b []byte) int {
	switch b[0] {
	case 0x0d, 0x00:
		return 1
	case 0x10, 0x11, 0x12:
		return 2
	case 0x18, 0x20:
		return 3
	case 0x21:
		return 4
	case 0x22:
		return 6
	case 0x23:
		return 10
	case 0x28:
		if len(b) < 3 {
			return 0
		}
		return 3 + int(b[2])
	}
	return 1 // unknown opcode: skipped
}

// Write feeds command bytes.
func (s *SimV2) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	s.in = append(s.in, p...)
	var cmds [][]byte
	for len(s.in) > 0 {
		n := cmdLen(s.in)
		if n == 0 || len(s.in) < n {
			break
		}
		cmds = append(cmds, append([]byte(nil), s.in[:n]...))
		s.in = s.in[n:]
	}
	s.mu.Unlock()
	for _, c := range cmds {
		out := s.exec(c)
		s.mu.Lock()
		s.out.Write(out)
		s.cond.Broadcast()
		s.mu.Unlock()
	}
	return len(p), nil
}

func (s *SimV2) reg(a byte) byte {
	switch a {
	case 0xf0:
		return s.opt.Variant
	case 0xf1:
		return 1
	case 0xf2:
		return 2
	case 0xf3:
		return s.opt.FWMajor
	case 0xf4:
		return s.opt.FWMinor
	case 0x20:
		return byte(s.points)
	case 0x21:
		return byte(s.points >> 8)
	}
	return 0
}

func (s *SimV2) exec(c []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch c[0] {
	case 0x0d:
		s.Log = append(s.Log, "INDICATE")
		return []byte{'2'}
	case 0x10:
		return []byte{s.reg(c[1])}
	case 0x11:
		return []byte{s.reg(c[1]), s.reg(c[1] + 1)}
	case 0x12:
		return []byte{s.reg(c[1]), s.reg(c[1] + 1), s.reg(c[1] + 2), s.reg(c[1] + 3)}
	case 0x20, 0x21, 0x22, 0x23:
		v := make([]byte, 8)
		copy(v, c[2:])
		val := binary.LittleEndian.Uint64(v)
		switch c[1] {
		case 0x00:
			s.start = val
		case 0x10:
			s.step = val
		case 0x20:
			s.points = max(1, int(val&0xffff))
		case 0x30: // clear the FIFO; the sweep goes on where it is
			s.Log = append(s.Log, "CLEAR")
			return nil
		default:
			return nil
		}
		s.Log = append(s.Log, "SWEEP")
		s.idx, s.stale = 0, true
	case 0x18:
		n := int(c[2])
		if c[1] != 0x30 || n == 0 {
			return nil
		}
		s.Log = append(s.Log, "READFIFO")
		out := make([]byte, 0, 32*n)
		ant := s.opt.Antenna
		t := time.Since(s.t0).Seconds()
		for k := 0; k < n; k++ {
			i := s.idx
			if s.stale { // one record measured with the old settings
				s.stale = false
				i = s.points / 2
			} else {
				s.idx = (s.idx + 1) % s.points
			}
			f := float64(s.start + uint64(i)*s.step)
			out = append(out, s.record(ant, f, t, i)...)
		}
		s.mu.Unlock()
		time.Sleep(time.Duration(n) * s.opt.PointTime)
		s.mu.Lock()
		return out
	}
	return nil
}

// record builds one FIFO record: the forward wave has an arbitrary,
// frequency-dependent amplitude and phase (like the real detector), and
// the reflected and transmitted waves go through a raw error model.
func (s *SimV2) record(ant Antenna, f, t float64, idx int) []byte {
	g, tr := ant.Measure(f, t)
	g = RawErrorV2(g, f) + complex(s.rng.NormFloat64()*s.opt.Noise, s.rng.NormFloat64()*s.opt.Noise)
	tr = tr*cmplx.Rect(0.9, -2*math.Pi*f*0.7e-9) + complex(s.rng.NormFloat64()*s.opt.Noise/20, s.rng.NormFloat64()*s.opt.Noise/20)
	fwd := cmplx.Rect(2.0e6*(1+0.3*math.Sin(f/97e6)), 2*math.Pi*f*1.9e-9)
	b := make([]byte, 32)
	put := func(k int, v float64) { binary.LittleEndian.PutUint32(b[4*k:], uint32(int32(math.Round(v)))) }
	put(0, real(fwd))
	put(1, imag(fwd))
	put(2, real(fwd*g))
	put(3, imag(fwd*g))
	put(4, real(fwd*tr))
	put(5, imag(fwd*tr))
	binary.LittleEndian.PutUint16(b[24:], uint16(idx))
	return b
}

// RawErrorV2 is the simulated V2's 1-port error model (directivity, source
// match, tracking): its raw data need a host calibration.
func RawErrorV2(g complex128, f float64) complex128 {
	w := 2 * math.Pi * f
	e00 := cmplx.Rect(0.06+0.02*f/1e9, -w*0.4e-9)
	e11 := cmplx.Rect(0.10, -w*0.6e-9)
	e10e01 := cmplx.Rect(0.8, -w*1.5e-9)
	return e00 + e10e01*g/(1-e11*g)
}
