// Command vnaview serves a web UI for measuring antennas with a NanoVNA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"vnaview/internal/serialport"
	"vnaview/internal/server"
	"vnaview/internal/vna"
	"vnaview/internal/vna/mock"
	"vnaview/internal/vna/nanovnah"
	"vnaview/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	port := flag.String("port", "", "serial device (default: auto-detect NanoVNA-H/H4 by USB ID 0483:5740 on /dev/ttyACM*)")
	useMock := flag.Bool("mock", false, "simulate a NanoVNA-H4 with a test antenna (no hardware needed)")
	mockFW := flag.String("mock-firmware", "custom", "mock firmware: custom (EasyVNA/PiX fork: `profile` command, Fork: line) or stock (plain NanoVNA-D)")
	mockAnt := flag.String("mock-antenna", "dual", "mock antenna scenario: "+strings.Join(scenarioNames(), ", "))
	dataDir := flag.String("data", defaultDataDir(), "directory for snapshots and settings")
	maxPoints := flag.Int("maxpoints", 0, "override points per device scan (0 = auto: 401 on H4, 101 on H)")
	calSettle := flag.Duration("cal-settle", 1500*time.Millisecond, "time the device sweeps with a standard attached before `cal <std>`")
	trace := flag.Bool("trace", false, "log every shell exchange (debugging)")
	readOnly := flag.Bool("readonly", false, "never change stored device state: no cal/save/recall/profile switching (sweeps only)")
	flag.Parse()

	logger := log.New(os.Stderr, "vnaview: ", log.LstdFlags)
	store, err := server.NewStore(*dataDir)
	if err != nil {
		logger.Fatalf("data dir: %v", err)
	}

	var open server.Opener
	var setMock func(string) error
	var mockCut func(float64)
	antennaName := ""
	if *useMock {
		_, ok := mock.Scenarios[*mockAnt]
		if !ok {
			logger.Fatalf("unknown -mock-antenna %q (have %s)", *mockAnt, strings.Join(scenarioNames(), ", "))
		}
		if *mockFW != "custom" && *mockFW != "stock" {
			logger.Fatalf("-mock-firmware must be custom or stock")
		}
		antennaName = *mockAnt
		var mu sync.Mutex
		current, curName := (*mock.Sim)(nil), *mockAnt
		cutTotal := 0.0
		setMock = func(name string) error {
			mkNew, ok := mock.Scenarios[name]
			if !ok {
				return fmt.Errorf("unknown mock antenna %q", name)
			}
			mu.Lock()
			defer mu.Unlock()
			curName, cutTotal = name, 0
			if current != nil {
				current.SetAntenna(mkNew())
			}
			return nil
		}
		// Simulated element: f0 = k/(L+δ) with L = 82 mm and δ = 6 mm end
		// effect, so the plain ratio formula is a bit off and the trim
		// calculator has something to learn.
		const mockLen, mockDelta = 82.0, 6.0
		mockCut = func(mm float64) {
			mu.Lock()
			defer mu.Unlock()
			cutTotal += mm
			a := mock.Scenarios[curName]()
			if len(a.Modes) > 0 {
				k := a.Modes[0].F0 * (mockLen + mockDelta)
				a.Modes[0].F0 = k / (mockLen - cutTotal + mockDelta)
			}
			if current != nil {
				current.SetAntenna(a)
			}
		}
		open = func(ctx context.Context) (vna.Device, error) {
			mu.Lock()
			sim := mock.New(mock.Options{Antenna: mock.Scenarios[curName](), MaxPoints: *maxPoints, Profiles: *mockFW == "custom"})
			current = sim
			mu.Unlock()
			return openDriver(ctx, sim, nanovnah.Options{PortName: "mock", Mock: true, MaxPoints: *maxPoints,
				CalSettle: 200 * time.Millisecond, ReadOnly: *readOnly}, *trace, logger)
		}
	} else {
		open = func(ctx context.Context) (vna.Device, error) {
			path := *port
			if path == "" {
				p, err := detect()
				if err != nil {
					return nil, err
				}
				path = p
			}
			sp, err := serialport.Open(path)
			if err != nil {
				return nil, err
			}
			d, err := openDriver(ctx, sp, nanovnah.Options{PortName: path, MaxPoints: *maxPoints, CalSettle: *calSettle, ReadOnly: *readOnly}, *trace, logger)
			if err != nil {
				sp.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			return d, nil
		}
	}

	var scen []string
	if *useMock {
		scen = scenarioNames()
	}
	srv := server.New(server.Options{Open: open, Store: store, Static: web.Static(), MockAntenna: antennaName, Logger: logger,
		ReadOnly: *readOnly, SetMockAntenna: setMock, MockScenarios: scen, MockCut: mockCut})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Fatalf("listen: %v", err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() { srv.Run(ctx); close(done) }()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	mode := "hardware"
	if *readOnly {
		mode += ", READ-ONLY"
	}
	if *useMock {
		mode = "mock (" + antennaName + ")"
	}
	logger.Printf("serving http://%s  [%s]  data: %s", ln.Addr(), mode, *dataDir)
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
	<-done
}

func openDriver(ctx context.Context, t nanovnah.Transport, opt nanovnah.Options, trace bool, l *log.Logger) (vna.Device, error) {
	if trace {
		opt.Trace = func(cmd string, raw []byte) {
			if len(raw) > 300 {
				raw = append(raw[:300:300], []byte("...")...)
			}
			l.Printf("shell %q -> %q", cmd, raw)
		}
	}
	return nanovnah.Open(ctx, t, opt)
}

func detect() (string, error) {
	if c, ok := serialport.Find(serialport.IDNanoVNAH); ok {
		return c.Path, nil
	}
	if c, ok := serialport.Find(serialport.IDNanoVNAV2); ok {
		return "", fmt.Errorf("found a NanoVNA V2 (%s, %s): its binary protocol is not implemented yet", c.Path, c.ID)
	}
	var seen []string
	for _, c := range serialport.List() {
		seen = append(seen, fmt.Sprintf("%s (%s)", c.Path, c.ID))
	}
	if len(seen) == 0 {
		return "", errors.New("no NanoVNA found: no /dev/ttyACM* devices (is it plugged in and switched on?)")
	}
	return "", fmt.Errorf("no NanoVNA (USB %s) found among %s; use -port", serialport.IDNanoVNAH, strings.Join(seen, ", "))
}

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "vnaview")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "share", "vnaview")
	}
	return "vnaview-data"
}

func scenarioNames() []string {
	var n []string
	for k := range mock.Scenarios {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
