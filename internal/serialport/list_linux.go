//go:build linux

package serialport

// List returns the serial devices that may be a NanoVNA.
func List() []Candidate { return ListSysfs() }
