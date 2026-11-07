// Package vna defines the device-independent interface that vnaview uses to
// talk to a vector network analyser. Each hardware family (NanoVNA-H/H4 text
// shell, NanoVNA V2 binary protocol, ...) implements Device in its own
// sub-package.
package vna

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Info describes a connected device and its capabilities.
type Info struct {
	Driver    string   `json:"driver"`         // e.g. "nanovna-h"
	Model     string   `json:"model"`          // e.g. "NanoVNA-H 4"
	Version   string   `json:"version"`        // firmware version string
	Fork      string   `json:"fork,omitempty"` // e.g. "EasyVNA by PiX (...)" from a "Fork:" line
	Port      string   `json:"port"`           // serial device or "mock"
	Raw       []string `json:"raw"`            // raw identification lines (e.g. `info` output)
	MaxPoints int      `json:"maxPoints"`
	MinFreq   float64  `json:"minFreq"` // Hz
	MaxFreq   float64  `json:"maxFreq"` // Hz
	CalSlots  int      `json:"calSlots"`
	TwoPort   bool     `json:"twoPort"` // can measure S21
	Mock      bool     `json:"mock"`
}

// CalStatus is the calibration state as reported by the firmware.
type CalStatus struct {
	Known   bool     `json:"known"`   // false if the firmware does not report it
	Raw     string   `json:"raw"`     // raw firmware text
	Flags   []string `json:"flags"`   // e.g. ["load","open","short","Es","Er","Et","cal'ed"]
	Applied bool     `json:"applied"` // error correction is being applied
	Open    bool     `json:"open"`
	Short   bool     `json:"short"`
	Load    bool     `json:"load"`
	Thru    bool     `json:"thru"`
	Isoln   bool     `json:"isoln"`
}

// CalStep is one step of the SOLT calibration flow.
type CalStep string

// Calibration steps understood by Device.Calibrate.
const (
	CalReset CalStep = "reset"
	CalOpen  CalStep = "open"
	CalShort CalStep = "short"
	CalLoad  CalStep = "load"
	CalIsoln CalStep = "isoln"
	CalThru  CalStep = "thru"
	CalDone  CalStep = "done"
	CalOn    CalStep = "on"
	CalOff   CalStep = "off"
)

// Measures reports whether the step takes a measurement of a standard.
func (s CalStep) Measures() bool {
	switch s {
	case CalOpen, CalShort, CalLoad, CalIsoln, CalThru:
		return true
	}
	return false
}

// ParseCalStep validates a step name.
func ParseCalStep(s string) (CalStep, error) {
	switch c := CalStep(s); c {
	case CalReset, CalOpen, CalShort, CalLoad, CalIsoln, CalThru, CalDone, CalOn, CalOff:
		return c, nil
	}
	return "", fmt.Errorf("unknown calibration step %q", s)
}

// ScanRequest is a linear frequency sweep.
type ScanRequest struct {
	Start  float64 `json:"start"` // Hz
	Stop   float64 `json:"stop"`  // Hz
	Points int     `json:"points"`
	S21    bool    `json:"s21"`
}

// Validate checks the request against basic sanity limits and, if non-zero,
// the device frequency range.
func (r ScanRequest) Validate(info Info) error {
	switch {
	case r.Points < 2:
		return errors.New("need at least 2 points")
	case r.Points > 10001:
		return errors.New("too many points (max 10001)")
	case r.Start <= 0 || r.Stop <= r.Start:
		return errors.New("start must be > 0 and below stop")
	case info.MinFreq > 0 && r.Start < info.MinFreq:
		return fmt.Errorf("start below device minimum %.0f Hz", info.MinFreq)
	case info.MaxFreq > 0 && r.Stop > info.MaxFreq:
		return fmt.Errorf("stop above device maximum %.0f Hz", info.MaxFreq)
	}
	return nil
}

// Grid returns the linear frequency grid (Hz, integer-rounded) for r.
func (r ScanRequest) Grid() []float64 {
	g := make([]float64, r.Points)
	for i := range g {
		f := r.Start + (r.Stop-r.Start)*float64(i)/float64(r.Points-1)
		g[i] = float64(int64(f + 0.5))
	}
	return g
}

// Sweep is one measured sweep. S21 is nil unless requested.
type Sweep struct {
	Freq     []float64
	S11      []complex128
	S21      []complex128
	Started  time.Time
	Duration time.Duration
}

// Device is implemented by every VNA driver. Implementations must be safe
// for concurrent use (they typically serialise access internally).
type Device interface {
	// Info returns identification and capabilities.
	Info(ctx context.Context) (Info, error)
	// Scan performs one sweep. Drivers split requests larger than the
	// hardware point limit into several segments transparently.
	Scan(ctx context.Context, req ScanRequest) (*Sweep, error)
	// CalStatus returns the firmware calibration state.
	CalStatus(ctx context.Context) (CalStatus, error)
	// Calibrate runs one calibration step. For CalReset and measurement
	// steps, rng (if non-nil) sets the device's own sweep range first, which
	// is the range the firmware calibrates over.
	Calibrate(ctx context.Context, step CalStep, rng *ScanRequest) error
	// SaveCal stores the current calibration in a firmware slot.
	SaveCal(ctx context.Context, slot int) error
	// RecallCal loads a calibration slot.
	RecallCal(ctx context.Context, slot int) error
	// Close releases the device (and returns it to its normal UI mode).
	Close() error
}

// ErrNotConnected is returned when no device is available.
var ErrNotConnected = errors.New("no VNA connected")

// DeviceProfile is an on-device sweep profile (EasyVNA by PiX fork of the NanoVNA-H4
// firmware, `profile` command): a named range tied to a calibration slot.
type DeviceProfile struct {
	Name   string  `json:"name"`
	Start  float64 `json:"start"`
	Stop   float64 `json:"stop"`
	Points int     `json:"points"`
	Slot   int     `json:"slot"`
	Cal    bool    `json:"cal"`    // the slot holds calibration data ("saved" for custom)
	Active bool    `json:"active"` // this profile is currently applied
}

// DeviceBoot is the firmware's boot-profile setting ("boot <name> ask=<0|1>").
type DeviceBoot struct {
	Name string `json:"name"`
	Ask  bool   `json:"ask"`
}

// DeviceProfiles is the full `profile` listing.
type DeviceProfiles struct {
	Profiles []DeviceProfile `json:"profiles"`         // wide, 433, 869, 915
	Custom   *DeviceProfile  `json:"custom,omitempty"` // range stored with the custom slot
	Boot     *DeviceBoot     `json:"boot,omitempty"`
}

// Profiler is optionally implemented by devices whose firmware has
// on-device profiles. SupportsProfiles reports whether the connected
// firmware actually has them (stock firmware does not).
type Profiler interface {
	SupportsProfiles() bool
	Profiles(ctx context.Context) (DeviceProfiles, error)
	// ApplyProfile applies wide, 433, 869, 915 or custom.
	ApplyProfile(ctx context.Context, name string) (DeviceProfile, error)
}
