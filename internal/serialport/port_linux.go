//go:build linux

package serialport

import (
	"fmt"
	"io"
	"sync"
	"syscall"
	"unsafe"
)

// Port is a raw-mode serial port. Read returns (0, nil) after ~100 ms with
// no data (VMIN=0, VTIME=1), which lets callers implement deadlines.
type Port struct {
	fd     int
	mu     sync.Mutex
	closed bool
}

// Open opens path at 115200 8N1 raw. (USB CDC ignores the baud rate, but a
// real UART bridge does not.)
func Open(path string) (*Port, error) {
	if h := Holders(path); len(h) > 0 {
		return nil, &BusyError{Path: path, Holders: h}
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.EBUSY {
			return nil, &BusyError{Path: path}
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var t syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, unsafe.Pointer(&t)); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("%s: not a tty: %w", path, err)
	}
	// cfmakeraw
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.CSTOPB | crtscts | cbaud
	t.Cflag |= syscall.CS8 | syscall.CREAD | syscall.CLOCAL | syscall.B115200
	t.Ispeed, t.Ospeed = syscall.B115200, syscall.B115200
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1
	if err := ioctl(fd, syscall.TCSETS, unsafe.Pointer(&t)); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("%s: set raw mode: %w", path, err)
	}
	// Exclusive mode: further open() calls by other programs fail with EBUSY
	// while we hold the port.
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocexcl, 0)
	// Flush stale input (TCFLSH, TCIFLUSH).
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflsh, 0)
	return &Port{fd: fd}, nil
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func (p *Port) Read(b []byte) (int, error) {
	for {
		n, err := syscall.Read(p.fd, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 && p.isClosed() {
			return 0, io.EOF
		}
		return n, nil
	}
}

func (p *Port) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := syscall.Write(p.fd, b[total:])
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return total, err
		}
		total += n
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
	return syscall.Close(p.fd)
}

// Constants missing from package syscall (values for x86/arm Linux).
const (
	crtscts  = 0x80000000
	cbaud    = 0x100f
	tcflsh   = 0x540b
	tiocexcl = 0x540c
)
