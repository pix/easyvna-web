// Package bridge runs the EasyVNA Web HTTP handler in-process, without a
// network: the browser build routes the page's fetch('api/...') calls and
// its EventSource('api/events') stream through Do and Stream.
package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Response is the result of one in-process request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// recorder is a minimal http.ResponseWriter.
type recorder struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}

// Path checks an API path ("/api/state?x=1") passed in from the page.
func Path(p string) (string, error) {
	if !strings.HasPrefix(p, "/api/") || strings.Contains(p, "\\") || strings.Contains(p, "#") {
		return "", fmt.Errorf("bridge: not an API path: %q", p)
	}
	return p, nil
}

func newRequest(ctx context.Context, method, target string, header http.Header, body []byte) (*http.Request, error) {
	p, err := Path(target)
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://easyvna.invalid"+p, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = "easyvna.invalid"
	req.RemoteAddr = "browser"
	req.RequestURI = p
	for k, v := range header {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	if len(body) == 0 {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	return req, nil
}

// Do runs one request through h and returns the whole response.
func Do(ctx context.Context, h http.Handler, method, target string, header http.Header, body []byte) (*Response, error) {
	req, err := newRequest(ctx, method, target, header, body)
	if err != nil {
		return nil, err
	}
	rec := &recorder{header: http.Header{}}
	h.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return &Response{Status: rec.status, Header: rec.header, Body: rec.body.Bytes()}, nil
}

// Event is one server-sent event.
type Event struct {
	Name string
	Data string
}

// sseWriter is a streaming http.ResponseWriter + http.Flusher that splits
// the text/event-stream output into events.
type sseWriter struct {
	mu      sync.Mutex
	status  int
	header  http.Header
	buf     bytes.Buffer
	onEvent func(Event)
}

func (w *sseWriter) Header() http.Header { return w.header }

func (w *sseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *sseWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	w.mu.Lock()
	w.buf.Write(b)
	evs := w.parseLocked(false)
	w.mu.Unlock()
	for _, e := range evs {
		w.onEvent(e)
	}
	return len(b), nil
}

func (w *sseWriter) Flush() {}

// parseLocked removes complete events ("...\n\n") from the buffer.
func (w *sseWriter) parseLocked(final bool) []Event {
	if w.status != http.StatusOK {
		return nil // error responses are reported by Stream
	}
	var out []Event
	for {
		b := w.buf.Bytes()
		i := bytes.Index(b, []byte("\n\n"))
		if i < 0 {
			if final && len(bytes.TrimSpace(b)) > 0 {
				i = len(b)
			} else {
				return out
			}
		}
		frame := string(b[:i])
		w.buf.Next(min(i+2, len(b)))
		if e, ok := ParseEvent(frame); ok {
			out = append(out, e)
		}
	}
}

// ParseEvent decodes one event block of a text/event-stream. Comment-only
// blocks (keep-alives) return ok == false.
func ParseEvent(frame string) (Event, bool) {
	e := Event{Name: "message"}
	var data []string
	has := false
	for _, line := range strings.Split(strings.ReplaceAll(frame, "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			e.Name = value
		case "data":
			data = append(data, value)
			has = true
		}
	}
	e.Data = strings.Join(data, "\n")
	return e, has
}

// ErrStatus is returned by Stream when the handler answers with an error.
var ErrStatus = errors.New("bridge: stream refused")

// Stream runs a long-lived event-stream request through h, calling onEvent
// for every event, until ctx is cancelled or the handler returns.
func Stream(ctx context.Context, h http.Handler, target string, onEvent func(Event)) error {
	req, err := newRequest(ctx, http.MethodGet, target, http.Header{"Accept": {"text/event-stream"}}, nil)
	if err != nil {
		return err
	}
	w := &sseWriter{header: http.Header{}, onEvent: onEvent}
	h.ServeHTTP(w, req)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status != 0 && w.status != http.StatusOK {
		return fmt.Errorf("%w: %d %s", ErrStatus, w.status, strings.TrimSpace(w.buf.String()))
	}
	for _, e := range w.parseLocked(true) {
		w.onEvent(e)
	}
	return ctx.Err()
}
