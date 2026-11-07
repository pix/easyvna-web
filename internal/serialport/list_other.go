//go:build !linux && !windows && !darwin

package serialport

// List is not implemented on this platform; use -port.
func List() []Candidate { return nil }
