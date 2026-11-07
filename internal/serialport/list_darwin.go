//go:build darwin

package serialport

import (
	"path/filepath"
	"sort"
)

// List returns /dev/cu.usbmodem* devices. Their USB VID:PID is not
// available without cgo (IOKit), so ID is empty; main picks the port when it
// is the only one.
func List() []Candidate {
	m, _ := filepath.Glob("/dev/cu.usbmodem*")
	sort.Strings(m)
	out := make([]Candidate, 0, len(m))
	for _, p := range m {
		out = append(out, Candidate{Path: p})
	}
	return out
}
