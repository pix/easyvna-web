package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pix/easyvna-web/internal/server"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

func TestPath(t *testing.T) {
	for _, ok := range []string{"/api/state", "/api/export?format=s1p&snapshot=a%20b"} {
		if _, err := Path(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "api/state", "/index.html", "/api\\x", "/api/x#y", "//evil/api/x"} {
		if _, err := Path(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDoRecordsResponse(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Got", fmt.Sprintf("%s %s %s %d %s", r.Method, r.URL.Path, r.URL.Query().Get("q"), r.ContentLength, r.Header.Get("Content-Type")))
		w.WriteHeader(201)
		_, _ = w.Write(b)
	})
	r, err := Do(context.Background(), h, "POST", "/api/x?q=1", http.Header{"content-type": {"application/json"}}, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != 201 || string(r.Body) != `{"a":1}` || r.Header.Get("X-Got") != "POST /api/x 1 7 application/json" {
		t.Fatalf("%d %q %q", r.Status, r.Body, r.Header.Get("X-Got"))
	}
	// No body and no explicit status: 200, ContentLength 0.
	r, _ = Do(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 {
			t.Error("content length")
		}
	}), "", "/api/y", nil, nil)
	if r.Status != 200 {
		t.Fatalf("status %d", r.Status)
	}
}

func TestParseEvent(t *testing.T) {
	cases := []struct {
		in   string
		want Event
		ok   bool
	}{
		{"event: state\ndata: {\"a\":1}", Event{"state", `{"a":1}`}, true},
		{"data: x\ndata: y", Event{"message", "x\ny"}, true},
		{": keepalive", Event{}, false},
		{"event:sweep\r\ndata:{}", Event{"sweep", "{}"}, true},
	}
	for _, c := range cases {
		got, ok := ParseEvent(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q -> %+v %v", c.in, got, ok)
		}
	}
}

func TestStreamSplitsEvents(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: a\ndata: 1\n\n: keepalive\n\nevent: b\nda")
		fl.Flush()
		_, _ = io.WriteString(w, "ta: 2\n\nevent: c\ndata: 3")
	})
	var got []string
	err := Stream(context.Background(), h, "/api/events", func(e Event) { got = append(got, e.Name+"="+e.Data) })
	if err != nil || strings.Join(got, ",") != "a=1,b=2,c=3" {
		t.Fatalf("%v %v", got, err)
	}
	err = Stream(context.Background(), http.NotFoundHandler(), "/api/events", func(Event) { t.Error("event from a 404") })
	if !errors.Is(err, ErrStatus) {
		t.Fatalf("404: %v", err)
	}
}

// The real server, driven entirely through the bridge, as the browser
// build does it: SSE state/sweep events and JSON API calls.
func TestServerThroughBridge(t *testing.T) {
	bench, _ := mock.NewBench("869", 0, true)
	open := func(ctx context.Context) (vna.Device, error) {
		return nanovnah.Open(ctx, bench.NewSim(), nanovnah.Options{PortName: "simulator", Mock: true, CalSettle: time.Millisecond})
	}
	srv := server.New(server.Options{Open: open, Store: server.NewStoreBackend(server.NewMemBackend(nil), "this browser"),
		Static: fstest.MapFS{}, Logger: log.New(io.Discard, "", 0), Backup: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	h := srv.Handler()

	events := make(chan Event, 64)
	sctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- Stream(sctx, h, "/api/events", func(e Event) {
			select {
			case events <- e:
			default:
			}
		})
	}()
	waitFor := func(name string) Event {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case e := <-events:
				if e.Name == name {
					return e
				}
			case <-deadline:
				t.Fatalf("no %s event", name)
			}
		}
	}
	waitFor("state")
	r, err := Do(ctx, h, "POST", "/api/profile", http.Header{"Content-Type": {"application/json"}}, []byte(`{"id":"869"}`))
	if err != nil || r.Status != 200 {
		t.Fatalf("profile: %v %d %s", err, r.Status, r.Body)
	}
	r, _ = Do(ctx, h, "POST", "/api/sweep", nil, nil)
	if r.Status != 200 {
		t.Fatalf("sweep: %d %s", r.Status, r.Body)
	}
	var sw struct {
		Freq []float64 `json:"freq"`
	}
	if err := json.Unmarshal([]byte(waitFor("sweep").Data), &sw); err != nil || len(sw.Freq) != 401 {
		t.Fatalf("sweep event: %v %d", err, len(sw.Freq))
	}
	r, _ = Do(ctx, h, "GET", "/api/export?format=csv", nil, nil)
	if r.Status != 200 || !strings.Contains(r.Header.Get("Content-Disposition"), ".csv") {
		t.Fatalf("export: %d %v", r.Status, r.Header)
	}
	r, _ = Do(ctx, h, "GET", "/api/backup", nil, nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), server.BackupFormat) {
		t.Fatalf("backup: %d", r.Status)
	}
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end on cancel")
	}
}
