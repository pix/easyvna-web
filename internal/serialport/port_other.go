//go:build !linux

package serialport

import "errors"

// Port is unavailable on this platform.
type Port struct{}

// Open is only implemented on Linux.
func Open(path string) (*Port, error) {
	return nil, errors.New("serial ports are only supported on Linux")
}

func (p *Port) Read(b []byte) (int, error)  { return 0, errors.New("unsupported") }
func (p *Port) Write(b []byte) (int, error) { return 0, errors.New("unsupported") }
func (p *Port) Close() error                { return nil }
