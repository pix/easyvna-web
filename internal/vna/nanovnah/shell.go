package nanovnah

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Transport is the byte stream to the device. Read may return (0, nil)
// periodically when no data is available (a serial port opened with a
// VTIME read timeout does this), which lets Shell honour deadlines.
type Transport interface {
	io.ReadWriteCloser
}

// Shell executes commands on the NanoVNA text shell. It is not safe for
// concurrent use; Driver serialises access.
type Shell struct {
	t   Transport
	buf []byte
	// Trace, if set, receives every raw exchange (for debugging).
	Trace func(cmd string, raw []byte)
}

// NewShell wraps a transport.
func NewShell(t Transport) *Shell { return &Shell{t: t, buf: make([]byte, 4096)} }

// maxResponse bounds memory if the device misbehaves.
const maxResponse = 4 << 20

// ErrTimeout is returned when no prompt arrives in time.
var ErrTimeout = errors.New("nanovna: timeout waiting for prompt")

// Sync gets the shell into a known state: it sends an empty line and waits
// for the prompt, discarding any banner or stale output.
func (s *Shell) Sync(ctx context.Context) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := s.t.Write([]byte("\r")); err != nil {
			return err
		}
		_, err := s.readUntilPrompt(ctx, time.Now().Add(1500*time.Millisecond))
		if err == nil {
			// Swallow anything that trails a doubled prompt.
			s.drain(150 * time.Millisecond)
			return nil
		}
		last = err
		if !errors.Is(err, ErrTimeout) {
			return err
		}
	}
	return fmt.Errorf("no NanoVNA shell prompt: %w", last)
}

// Exec sends one command and returns its output lines.
func (s *Shell) Exec(ctx context.Context, cmd string, timeout time.Duration) ([]string, error) {
	raw, err := s.ExecRaw(ctx, cmd, timeout)
	if err != nil {
		return nil, err
	}
	return SplitResponse(cmd, raw)
}

// ExecRaw sends one command and returns the raw bytes up to and including
// the prompt.
func (s *Shell) ExecRaw(ctx context.Context, cmd string, timeout time.Duration) ([]byte, error) {
	if len(cmd) > 63 {
		return nil, fmt.Errorf("nanovna: command too long (%d > 63 chars)", len(cmd))
	}
	if _, err := s.t.Write([]byte(cmd + "\r")); err != nil {
		return nil, err
	}
	raw, err := s.readUntilPrompt(ctx, time.Now().Add(timeout))
	if s.Trace != nil {
		s.Trace(cmd, raw)
	}
	if err != nil {
		return raw, fmt.Errorf("%q: %w", cmd, err)
	}
	return raw, nil
}

func (s *Shell) readUntilPrompt(ctx context.Context, deadline time.Time) ([]byte, error) {
	var acc bytes.Buffer
	prompt := []byte(Prompt)
	for {
		if bytes.HasSuffix(acc.Bytes(), prompt) {
			return acc.Bytes(), nil
		}
		if err := ctx.Err(); err != nil {
			return acc.Bytes(), err
		}
		if time.Now().After(deadline) {
			return acc.Bytes(), ErrTimeout
		}
		if acc.Len() > maxResponse {
			return acc.Bytes(), errors.New("nanovna: response too large")
		}
		n, err := s.t.Read(s.buf)
		if n > 0 {
			acc.Write(s.buf[:n])
		}
		if err != nil {
			return acc.Bytes(), err
		}
	}
}

// drain discards input for d.
func (s *Shell) drain(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		n, err := s.t.Read(s.buf)
		if err != nil || n == 0 {
			return
		}
	}
}
