//go:build windows

package serialport

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Port is a serial port opened through the Win32 comm API. Read returns
// (0, nil) after ~100 ms without data, like the Linux VTIME setup, so
// callers can implement deadlines.
type Port struct {
	h      windows.Handle
	path   string
	mu     sync.Mutex
	closed bool
}

// Open opens a COM port (e.g. "COM5") at 115200 8N1 with DTR/RTS raised.
func Open(path string) (*Port, error) {
	dev := WindowsDevicePath(path)
	p16, err := windows.UTF16PtrFromString(dev)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p16, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, windows.ERROR_SHARING_VIOLATION):
			return nil, &BusyError{Path: path}
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
			return nil, fmt.Errorf("%s not found (unplugged, or another COM number: see Device Manager)", path)
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	fail := func(what string, err error) (*Port, error) {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("%s: %s: %w", path, what, err)
	}
	var dcb windows.DCB
	dcb.DCBlength = uint32(unsafe.Sizeof(dcb)) // 28 bytes
	if err := windows.GetCommState(h, &dcb); err != nil {
		return fail("get comm state", err)
	}
	dcb.BaudRate = 115200
	dcb.ByteSize = 8
	dcb.Parity = windows.NOPARITY
	dcb.StopBits = windows.ONESTOPBIT
	// fBinary=1, fDtrControl=ENABLE, fRtsControl=ENABLE; no flow control.
	dcb.Flags = dcbBinary | windows.DTR_CONTROL_ENABLE | rtsControlEnable
	if err := windows.SetCommState(h, &dcb); err != nil {
		return fail("set 115200 8N1", err)
	}
	// Return immediately with what is there, or wait up to 100 ms.
	to := windows.CommTimeouts{
		ReadIntervalTimeout:         0xFFFFFFFF,
		ReadTotalTimeoutMultiplier:  0xFFFFFFFF,
		ReadTotalTimeoutConstant:    100,
		WriteTotalTimeoutMultiplier: 0,
		WriteTotalTimeoutConstant:   2000,
	}
	if err := windows.SetCommTimeouts(h, &to); err != nil {
		return fail("set timeouts", err)
	}
	_ = windows.SetupComm(h, 64<<10, 4<<10)
	_ = windows.EscapeCommFunction(h, windows.SETDTR)
	_ = windows.PurgeComm(h, windows.PURGE_RXCLEAR|windows.PURGE_TXCLEAR)
	return &Port{h: h, path: path}, nil
}

const (
	dcbBinary        = 0x00000001
	rtsControlEnable = 0x00001000
)

func (p *Port) Read(b []byte) (int, error) {
	var n uint32
	if err := windows.ReadFile(p.h, b, &n, nil); err != nil {
		if p.isClosed() {
			return 0, fmt.Errorf("%s closed", p.path)
		}
		return 0, err
	}
	return int(n), nil
}

func (p *Port) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		var n uint32
		if err := windows.WriteFile(p.h, b[total:], &n, nil); err != nil {
			return total, err
		}
		if n == 0 {
			return total, fmt.Errorf("%s: write timeout", p.path)
		}
		total += int(n)
	}
	return total, nil
}

func (p *Port) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Close closes the port.
func (p *Port) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return windows.CloseHandle(p.h)
}
