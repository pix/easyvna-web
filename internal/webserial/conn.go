// Package webserial connects the NanoVNA shell driver to a serial port
// opened by the page with the Web Serial API (browser build). Conn is the
// platform-independent part: the page pushes received bytes into it and it
// hands them to the driver like a serial port with a read timeout.
package webserial

import (
	"errors"
	"io"
	"sync"
	"time"
)

// ErrDisconnected is returned once the port is gone (unplugged, closed by
// the browser, or read error).
var ErrDisconnected = errors.New("NanoVNA disconnected (USB unplugged or port closed); plug it back in and press Connect")

// Conn is a nanovnah.Transport over a push-style byte source and an
// (asynchronous) write function.
type Conn struct {
	// ReadTimeout is how long Read waits before returning (0, nil), like a
	// serial port opened with VTIME, so the shell can honour deadlines.
	ReadTimeout time.Duration

	write   func([]byte) error
	closeFn func() error

	mu      sync.Mutex
	buf     []byte
	err     error // set when the stream ended
	notify  chan struct{}
	done    chan struct{}
	endOnce sync.Once
	clOnce  sync.Once
}

// NewConn creates a connection. write sends bytes to the device and returns
// once they are handed to the port; closeFn releases the port.
func NewConn(write func([]byte) error, closeFn func() error) *Conn {
	return &Conn{ReadTimeout: 100 * time.Millisecond, write: write, closeFn: closeFn,
		notify: make(chan struct{}, 1), done: make(chan struct{})}
}

// Push queues bytes received from the device. It never blocks.
func (c *Conn) Push(b []byte) {
	if len(b) == 0 {
		return
	}
	c.mu.Lock()
	if c.err == nil {
		c.buf = append(c.buf, b...)
	}
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// End marks the end of the input stream; Read returns err (ErrDisconnected
// if nil) once the queued bytes are consumed.
func (c *Conn) End(err error) {
	if err == nil {
		err = ErrDisconnected
	}
	c.endOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *Conn) take(p []byte) (int, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buf) > 0 {
		n := copy(p, c.buf)
		c.buf = c.buf[n:]
		if len(c.buf) == 0 {
			c.buf = nil
		}
		return n, nil, true
	}
	if c.err != nil {
		return 0, c.err, true
	}
	return 0, nil, false
}

// Read implements io.Reader. It returns (0, nil) after ReadTimeout without data.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if n, err, ok := c.take(p); ok {
		return n, err
	}
	t := time.NewTimer(c.ReadTimeout)
	defer t.Stop()
	select {
	case <-c.notify:
	case <-c.done:
	case <-t.C:
		return 0, nil
	}
	n, err, _ := c.take(p)
	return n, err
}

// Write implements io.Writer.
func (c *Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if err := c.write(append([]byte(nil), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close releases the port. Further reads and writes fail with io.ErrClosedPipe.
func (c *Conn) Close() error {
	var err error
	c.clOnce.Do(func() {
		c.End(io.ErrClosedPipe)
		if c.closeFn != nil {
			err = c.closeFn()
		}
	})
	return err
}
