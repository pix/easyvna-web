package webserial

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

func TestConnReadTimeoutAndOrder(t *testing.T) {
	c := NewConn(func([]byte) error { return nil }, nil)
	c.ReadTimeout = 20 * time.Millisecond
	buf := make([]byte, 4)
	start := time.Now()
	if n, err := c.Read(buf); n != 0 || err != nil || time.Since(start) < 15*time.Millisecond {
		t.Fatalf("empty read: %d %v after %v", n, err, time.Since(start))
	}
	c.Push([]byte("hello"))
	c.Push([]byte(" world"))
	var got []byte
	for len(got) < 11 {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q", got)
	}
	// Bytes queued before End are still delivered, then the error.
	c.Push([]byte("ab"))
	c.End(nil)
	c.Push([]byte("lost"))
	if n, err := c.Read(buf); n != 2 || err != nil {
		t.Fatalf("tail: %d %v", n, err)
	}
	if _, err := c.Read(buf); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("after end: %v", err)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("write after end: %v", err)
	}
}

// Regression: bytes pushed before a Read leave a wake-up behind; a later
// Read must not take that stale wake-up for data and return early (the
// shell's drain would stop and replies would shift by one command).
func TestConnStaleWakeup(t *testing.T) {
	c := NewConn(func([]byte) error { return nil }, nil)
	c.ReadTimeout = 50 * time.Millisecond
	c.Push([]byte("ch> "))
	if n, _ := c.Read(make([]byte, 16)); n != 4 {
		t.Fatalf("n=%d", n)
	}
	go func() { time.Sleep(15 * time.Millisecond); c.Push([]byte("late")) }()
	b := make([]byte, 16)
	n, err := c.Read(b)
	if err != nil || string(b[:n]) != "late" {
		t.Fatalf("got %q %v: returned on a stale wake-up", b[:n], err)
	}
}

func TestConnWakesBlockedReader(t *testing.T) {
	c := NewConn(func([]byte) error { return nil }, nil)
	c.ReadTimeout = 5 * time.Second
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 8)
		n, _ := c.Read(b)
		got <- string(b[:n])
	}()
	time.Sleep(10 * time.Millisecond)
	c.Push([]byte("ch> "))
	select {
	case s := <-got:
		if s != "ch> " {
			t.Fatalf("%q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("reader not woken by Push")
	}
	go func() { time.Sleep(10 * time.Millisecond); c.End(errors.New("gone")) }()
	if _, err := c.Read(make([]byte, 1)); err == nil || err.Error() != "gone" {
		t.Fatalf("end: %v", err)
	}
}

func TestConnClose(t *testing.T) {
	closed := 0
	c := NewConn(func([]byte) error { return nil }, func() error { closed++; return nil })
	_ = c.Close()
	_ = c.Close()
	if closed != 1 {
		t.Fatalf("closeFn called %d times", closed)
	}
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("read after close: %v", err)
	}
}

// The NanoVNA driver runs on a Conn whose other end is the byte-level
// simulator, pumped the way the page's Web Serial reader loop does it.
func TestDriverOverConn(t *testing.T) {
	sim := mock.New(mock.Options{PointTime: time.Microsecond, Seed: 3, Profiles: true})
	var wmu sync.Mutex
	var unplugged atomic.Bool
	unplug := func() error { unplugged.Store(true); return sim.Close() }
	c := NewConn(func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := sim.Write(b)
		return err
	}, unplug)
	go func() { // reader loop: device -> page -> Conn.Push, in small chunks
		b := make([]byte, 64)
		for {
			n, err := sim.Read(b) // (0, io.EOF) also means "nothing yet"
			c.Push(b[:n])
			if err != nil && unplugged.Load() {
				c.End(nil)
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := nanovnah.Open(ctx, c, nanovnah.Options{PortName: "web serial", CalSettle: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sw, err := d.Scan(ctx, vna.ScanRequest{Start: 800e6, Stop: 900e6, Points: 101})
	if err != nil || len(sw.S11) != 101 {
		t.Fatalf("scan: %v", err)
	}
	// Unplug: the simulator stops, the pump ends the Conn, the driver errors.
	_ = unplug()
	if _, err := d.Scan(ctx, vna.ScanRequest{Start: 800e6, Stop: 900e6, Points: 11}); err == nil {
		t.Fatal("scan after unplug succeeded")
	}
}
