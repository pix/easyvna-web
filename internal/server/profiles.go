package server

import "vnaview/internal/rf"

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

// DefaultProfiles are the quick profiles shown at the top of the UI. Each
// band profile is centred on its band with ±50 MHz so the dip is visible
// even when the antenna is somewhat detuned.
func DefaultProfiles() []Profile {
	return []Profile{
		{ID: "wide", Label: "Wide", Sub: "300–1000 MHz · identify", Start: 300e6, Stop: 1000e6, Points: 401,
			Bands: []rf.Band{rf.Band433, rf.Band869, rf.Band915}, Identify: true, CalSlot: 0},
		{ID: "433", Label: "433", Sub: "384–484 MHz", Start: 384e6, Stop: 484e6, Points: 401,
			Bands: []rf.Band{rf.Band433}, CalSlot: 1},
		{ID: "869", Label: "869", Sub: "819–919 MHz", Start: 819e6, Stop: 919e6, Points: 401,
			Bands: []rf.Band{rf.Band869, rf.Band869g3}, CalSlot: 2},
		{ID: "915", Label: "915", Sub: "865–965 MHz", Start: 865e6, Stop: 965e6, Points: 401,
			Bands: []rf.Band{rf.Band915}, CalSlot: 3},
	}
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
