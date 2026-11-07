package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type event struct {
	name string
	data []byte
}

// hub fans out server-sent events to all connected browsers.
type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

func newHub() *hub { return &hub{subs: map[chan event]struct{}{}} }

func (h *hub) subscribe() chan event {
	ch := make(chan event, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// publish JSON-encodes v and sends it to every subscriber. Slow subscribers
// lose events rather than blocking the sweep loop.
func (h *hub) publish(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- event{name, b}:
		default:
		}
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// serveSSE streams events; initial is sent first (state and last sweep).
func (h *hub) serveSSE(w http.ResponseWriter, r *http.Request, initial []event) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := h.subscribe()
	defer h.unsubscribe(ch)
	write := func(e event) bool {
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
		fl.Flush()
		return err == nil
	}
	for _, e := range initial {
		if !write(e) {
			return
		}
	}
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if !write(e) {
				return
			}
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
