package server

import (
	"strconv"

	"github.com/pix/easyvna-web/internal/rf"
)

// Profile is a one-tap sweep setup.
type Profile struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Sub      string    `json:"sub"` // short sub-label for the button
	Start    float64   `json:"start"`
	Stop     float64   `json:"stop"`
	Points   int       `json:"points"`
	Bands    []rf.Band `json:"bands"`
	Identify bool      `json:"identify"` // wide "what is this antenna" sweep
	CalSlot  int       `json:"calSlot"`  // default firmware slot for this profile's calibration
}

// Slot 1 belongs to the SURVEY profile (PiX firmware v5+), slot 0 stays the
// user's own (stock firmware loads it at power-on).

// CustomSlot is the calibration slot of the custom (advanced) range. It
// matches the PiX firmware, which keeps slots 0 and 1 for the user because
// stock firmware loads slot 0 at power-on.
const CustomSlot = 2

// DefaultProfiles are the quick profiles shown at the top of the UI. They
// match the PiX firmware v2 profile table (ranges, points and slots), and
// the same slots are used with stock firmware so a calibration saved from
// either side is found by the other.
func DefaultProfiles() []Profile {
	return []Profile{
		{ID: "wide", Label: "Wide", Sub: "300–1000 MHz · identify", Start: 300e6, Stop: 1000e6, Points: 401,
			Bands: []rf.Band{rf.Band433, rf.Band869, rf.Band915}, Identify: true, CalSlot: 3},
		{ID: "survey", Label: "Survey", Sub: "50–1500 MHz · identify", Start: 50e6, Stop: 1500e6, Points: 401,
			Bands: []rf.Band{rf.Band433, rf.Band869, rf.Band915}, Identify: true, CalSlot: 1},
		{ID: "433", Label: "433", Sub: "388.92–478.92 MHz", Start: 388.92e6, Stop: 478.92e6, Points: 401,
			Bands: []rf.Band{rf.Band433}, CalSlot: 4},
		{ID: "869", Label: "869", Sub: "779–959 MHz", Start: 779e6, Stop: 959e6, Points: 401,
			Bands: []rf.Band{rf.Band869, rf.Band869g3}, CalSlot: 5},
		{ID: "915", Label: "915", Sub: "825–1005 MHz", Start: 825e6, Stop: 1005e6, Points: 401,
			Bands: []rf.Band{rf.Band915}, CalSlot: 6},
	}
}

// mhzRange formats "388.92–478.92 MHz".
func mhzRange(start, stop float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v/1e6, 'f', -1, 64) }
	return f(start) + "–" + f(stop) + " MHz"
}

// bandsOverlapping returns the LoRa bands that overlap [start, stop]; used
// for custom (advanced) sweeps.
func bandsOverlapping(start, stop float64) []rf.Band {
	out := []rf.Band{}
	for _, b := range []rf.Band{rf.Band433, rf.Band869, rf.Band869g3, rf.Band915} {
		if b.Hi > start && b.Lo < stop {
			out = append(out, b)
		}
	}
	return out
}
