//go:build darwin

package serialport

import (
	"fmt"
	"io"
	"sync"

	"golang.org/x/sys/unix"
)

// Port is a raw-mode serial port (macOS termios). Read returns (0, nil)
// after ~100 ms with no data (VMIN=0, VTIME=1). Untested on real hardware.
type Port struct {
	fd     int
	mu     sync.Mutex
	closed bool
}

// Open opens path (e.g. /dev/cu.usbmodem401) at 115200 8N1 raw.
func Open(path string) (*Port, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.EBUSY {
			return nil, &BusyError{Path: path}
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("%s: not a tty: %w", path, err)
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	t.Ispeed, t.Ospeed = unix.B115200, unix.B115200
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 0, 1
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("%s: set raw mode: %w", path, err)
	}
	_ = unix.IoctlSetInt(fd, unix.TIOCEXCL, 0)
	// Back to blocking mode so VTIME applies.
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Port{fd: fd}, nil
}

func (p *Port) Read(b []byte) (int, error) {
	for {
		n, err := unix.Read(p.fd, b)
		if err == unix.EINTR {
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
		n, err := unix.Write(p.fd, b[total:])
		if err == unix.EINTR || err == unix.EAGAIN {
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
	return unix.Close(p.fd)
}
