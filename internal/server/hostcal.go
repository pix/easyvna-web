package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/cmplx"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
	"github.com/pix/easyvna-web/internal/vna"
)

// ---------- host-side calibration ----------
//
// Devices that send raw data (NanoVNA V2: Info.HostCal) are wrapped in
// hostCal, which gives them the same OPEN / SHORT / LOAD / DONE / SAVE flow
// as a NanoVNA-H: the standards are measured over the profile's range, the
// one-port error terms (directivity e00, source match e11, tracking
// e10e01) are solved per point, and every later sweep is corrected with
// them, interpolated in frequency when the grid differs (TUNE windows).
// THRU (and ISOLN) normalise S21 when measured. SAVE stores the terms in
// the data folder (hostcal/slot-N.json, with their frequency range), and
// RECALL / auto-recall load them, so each profile keeps its own
// calibration exactly as on the H4.

// HostCalTerms is a solved host calibration.
type HostCalTerms struct {
	Start   float64         `json:"start"`
	Stop    float64         `json:"stop"`
	Points  int             `json:"points"`
	Created time.Time       `json:"created"`
	Device  string          `json:"device,omitempty"`
	Freq    []float64       `json:"freq"`
	E00     [][2]float64    `json:"e00"`
	E11     [][2]float64    `json:"e11"`
	E10E01  [][2]float64    `json:"e10e01"`
	Thru    [][2]float64    `json:"thru,omitempty"`  // raw S21 of the THRU
	Isoln   [][2]float64    `json:"isoln,omitempty"` // raw S21 with both ports terminated
	Flags   map[string]bool `json:"flags"`
}

func pack(c []complex128) [][2]float64 {
	if c == nil {
		return nil
	}
	out := make([][2]float64, len(c))
	for i, v := range c {
		out[i] = [2]float64{real(v), imag(v)}
	}
	return out
}

// SolveOSL computes the one-port error terms from raw measurements of
// ideal OPEN (Γ = 1), SHORT (Γ = −1) and LOAD (Γ = 0) standards.
func SolveOSL(open, short, load []complex128) (e00, e11, e10e01 []complex128, err error) {
	n := len(load)
	if len(open) != n || len(short) != n || n == 0 {
		return nil, nil, nil, calErrorf("host calibration: the standards were measured on different grids")
	}
	e00, e11, e10e01 = make([]complex128, n), make([]complex128, n), make([]complex128, n)
	for i := range load {
		e00[i] = load[i]
		a, b := open[i]-load[i], short[i]-load[i]
		if cmplx.Abs(a-b) < 1e-9 {
			return nil, nil, nil, calErrorf("host calibration: OPEN and SHORT look the same at point %d: check the standards", i+1)
		}
		e11[i] = (a + b) / (a - b)
		e10e01[i] = a * (1 - e11[i])
	}
	return e00, e11, e10e01, nil
}

// CorrectOSL applies one-port error terms to a raw reflection.
func CorrectOSL(m, e00, e11, e10e01 complex128) complex128 {
	d := m - e00
	den := e10e01 + e11*d
	if cmplx.Abs(den) < 1e-15 {
		return 0
	}
	return d / den
}

// termsAt interpolates a packed term at f (clamped at the ends).
func termsAt(freq []float64, v [][2]float64, f float64) complex128 {
	n := len(freq)
	if n == 0 || len(v) != n {
		return 0
	}
	if f <= freq[0] {
		return complex(v[0][0], v[0][1])
	}
	if f >= freq[n-1] {
		return complex(v[n-1][0], v[n-1][1])
	}
	i := sort.SearchFloat64s(freq, f)
	f0, f1 := freq[i-1], freq[i]
	t := (f - f0) / (f1 - f0)
	a, b := complex(v[i-1][0], v[i-1][1]), complex(v[i][0], v[i][1])
	return a + complex(t, 0)*(b-a)
}

// CalError is a calibration problem that is not a device failure (the
// server keeps the device connected).
type CalError struct{ msg string }

func (e *CalError) Error() string { return e.msg }

func calErrorf(format string, args ...any) error { return &CalError{fmt.Sprintf(format, args...)} }

// hostCal wraps a raw-data device with host calibration.
type hostCal struct {
	dev   vna.Device
	store *Store
	name  string

	mu      sync.Mutex
	rng     vna.ScanRequest
	std     map[vna.CalStep][]complex128 // measured standards over rng
	stdFreq []float64
	active  *HostCalTerms
	on      bool
}

func newHostCal(dev vna.Device, st *Store, name string) *hostCal {
	return &hostCal{dev: dev, store: st, name: name, std: map[vna.CalStep][]complex128{}}
}

var _ vna.Device = (*hostCal)(nil)

func (h *hostCal) Info(ctx context.Context) (vna.Info, error) { return h.dev.Info(ctx) }

func (h *hostCal) Scan(ctx context.Context, req vna.ScanRequest) (*vna.Sweep, error) {
	sw, err := h.dev.Scan(ctx, req)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	t := h.active
	on := h.on && t != nil
	h.mu.Unlock()
	if !on {
		return sw, nil
	}
	for i, f := range sw.Freq {
		sw.S11[i] = CorrectOSL(sw.S11[i], termsAt(t.Freq, t.E00, f), termsAt(t.Freq, t.E11, f), termsAt(t.Freq, t.E10E01, f))
		if i < len(sw.S21) && len(t.Thru) == len(t.Freq) {
			iso := complex(0, 0)
			if len(t.Isoln) == len(t.Freq) {
				iso = termsAt(t.Freq, t.Isoln, f)
			}
			if d := termsAt(t.Freq, t.Thru, f) - iso; cmplx.Abs(d) > 1e-12 {
				sw.S21[i] = (sw.S21[i] - iso) / d
			}
		}
	}
	return sw, nil
}

// status builds the firmware-like calibration status.
func (h *hostCal) statusLocked() vna.CalStatus {
	st := vna.CalStatus{Known: true, Flags: []string{}}
	for _, s := range []vna.CalStep{vna.CalLoad, vna.CalOpen, vna.CalShort, vna.CalThru, vna.CalIsoln} {
		if h.std[s] != nil {
			st.Flags = append(st.Flags, string(s))
		}
	}
	st.Open, st.Short, st.Load = h.std[vna.CalOpen] != nil, h.std[vna.CalShort] != nil, h.std[vna.CalLoad] != nil
	st.Thru, st.Isoln = h.std[vna.CalThru] != nil, h.std[vna.CalIsoln] != nil
	st.Applied = h.on && h.active != nil
	switch {
	case st.Applied:
		st.Flags = append(st.Flags, "cal'ed")
		st.Raw = fmt.Sprintf("host calibration %s, %d points (EasyVNA Web)", mhzRange(h.active.Start, h.active.Stop), h.active.Points)
	case h.active != nil:
		st.Raw = "host calibration off: raw data"
	default:
		st.Raw = "no host calibration: raw data"
	}
	if len(st.Flags) > 0 && !st.Applied {
		st.Raw += " · measured: " + strings.Join(st.Flags, " ")
	}
	return st
}

func (h *hostCal) CalStatus(ctx context.Context) (vna.CalStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statusLocked(), nil
}

// calAverage is how many sweeps are averaged per standard.
const calAverage = 3

func (h *hostCal) Calibrate(ctx context.Context, step vna.CalStep, rng *vna.ScanRequest) error {
	h.mu.Lock()
	if rng != nil && (step == vna.CalReset || step.Measures()) {
		if h.rng != *rng && step != vna.CalReset {
			// A new range: start the standards again.
			h.std = map[vna.CalStep][]complex128{}
		}
		h.rng = *rng
	}
	req := h.rng
	h.mu.Unlock()
	switch step {
	case vna.CalReset:
		h.mu.Lock()
		h.std, h.active, h.on = map[vna.CalStep][]complex128{}, nil, false
		h.mu.Unlock()
		return nil
	case vna.CalOn, vna.CalOff:
		h.mu.Lock()
		defer h.mu.Unlock()
		if step == vna.CalOn && h.active == nil {
			return calErrorf("no host calibration to switch on")
		}
		h.on = step == vna.CalOn
		return nil
	case vna.CalDone:
		return h.solve()
	}
	if !step.Measures() || req.Points < 2 {
		return calErrorf("host calibration: cannot run %s", step)
	}
	req.S21 = step == vna.CalThru || step == vna.CalIsoln
	var sweeps, sweeps21 [][]complex128
	var freq []float64
	for k := 0; k < calAverage; k++ {
		sw, err := h.dev.Scan(ctx, req) // raw: standards are never corrected
		if err != nil {
			return err
		}
		freq = sw.Freq
		sweeps = append(sweeps, sw.S11)
		sweeps21 = append(sweeps21, sw.S21)
	}
	data := rf.Average(sweeps)
	if req.S21 {
		data = rf.Average(sweeps21)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stdFreq != nil && len(h.stdFreq) != len(freq) {
		h.std = map[vna.CalStep][]complex128{}
	}
	h.stdFreq = freq
	h.std[step] = data
	return nil
}

func (h *hostCal) solve() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	o, s, l := h.std[vna.CalOpen], h.std[vna.CalShort], h.std[vna.CalLoad]
	if o == nil || s == nil || l == nil {
		return calErrorf("host calibration: measure OPEN, SHORT and LOAD first")
	}
	e00, e11, e10e01, err := SolveOSL(o, s, l)
	if err != nil {
		return err
	}
	n := len(h.stdFreq)
	t := &HostCalTerms{Start: h.stdFreq[0], Stop: h.stdFreq[n-1], Points: n, Created: time.Now(), Device: h.name,
		Freq: append([]float64(nil), h.stdFreq...), E00: pack(e00), E11: pack(e11), E10E01: pack(e10e01),
		Thru: pack(h.std[vna.CalThru]), Isoln: pack(h.std[vna.CalIsoln]), Flags: map[string]bool{}}
	for k := range h.std {
		t.Flags[string(k)] = true
	}
	// Keep the range the server asked for (the device grid can differ by
	// a few Hz from rounding).
	if h.rng.Points > 1 {
		t.Start, t.Stop = h.rng.Start, h.rng.Stop
	}
	h.active, h.on = t, true
	return nil
}

func hostCalPath(slot int) string { return fmt.Sprintf("hostcal/slot-%d.json", slot) }

func (h *hostCal) SaveCal(ctx context.Context, slot int) error {
	h.mu.Lock()
	t := h.active
	h.mu.Unlock()
	if t == nil {
		return calErrorf("nothing to save: finish OPEN, SHORT, LOAD and DONE first")
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return h.store.b.WriteFile(hostCalPath(slot), b)
}

func (h *hostCal) RecallCal(ctx context.Context, slot int) error {
	b, err := h.store.b.ReadFile(hostCalPath(slot))
	if err != nil {
		return calErrorf("slot %d has no host calibration yet", slot)
	}
	var t HostCalTerms
	if err := json.Unmarshal(b, &t); err != nil {
		return calErrorf("host calibration in slot %d: %v", slot, err)
	}
	if len(t.Freq) < 2 || len(t.E00) != len(t.Freq) || len(t.E11) != len(t.Freq) || len(t.E10E01) != len(t.Freq) || math.IsNaN(t.Start) {
		return calErrorf("host calibration in slot %d is damaged", slot)
	}
	h.mu.Lock()
	h.active, h.on = &t, true
	h.mu.Unlock()
	return nil
}

func (h *hostCal) Close() error { return h.dev.Close() }
