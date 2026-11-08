// Command easyvna-web (EasyVNA Web) serves a web UI for measuring antennas with a NanoVNA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pix/easyvna-web/internal/serialport"
	"github.com/pix/easyvna-web/internal/server"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
	"github.com/pix/easyvna-web/internal/vna/nanovnav2"
	"github.com/pix/easyvna-web/web"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	port := flag.String("port", "", "serial device, e.g. COM5 or /dev/ttyACM0 (default: auto-detect NanoVNA-H/H4 by USB ID 0483:5740, NanoVNA V2 / LiteVNA by 04b4:0008)")
	driver := flag.String("driver", "auto", "device driver: auto (by USB ID), nanovna-h (text shell) or nanovna-v2 (binary protocol, EXPERIMENTAL)")
	noBrowser := flag.Bool("no-browser", false, "do not open the web browser at start-up")
	useMock := flag.Bool("mock", false, "simulate a NanoVNA-H4 with a test antenna (no hardware needed)")
	mockFW := flag.String("mock-firmware", "custom", "mock firmware: custom (EasyVNA/PiX fork: `profile` command, Fork: line), stock (plain NanoVNA-D) or v2 (NanoVNA V2 binary protocol, raw data, host calibration)")
	mockAnt := flag.String("mock-antenna", "dual", "mock antenna scenario: "+strings.Join(scenarioNames(), ", "))
	dataDir := flag.String("data", defaultDataDir(runtime.GOOS, os.Getenv, os.UserHomeDir, dirExists), "directory for settings, REF traces, trim logs, reports and snapshots")
	maxPoints := flag.Int("maxpoints", 0, "override points per device scan (0 = auto: 401 on H4, 101 on H)")
	calSettle := flag.Duration("cal-settle", 1500*time.Millisecond, "time the device sweeps with a standard attached before `cal <std>`")
	trace := flag.Bool("trace", false, "log every shell exchange (debugging)")
	readOnly := flag.Bool("readonly", false, "never change stored device state: no cal/save/recall/profile switching (sweeps only)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("easyvna-web", version, "- EasyVNA Web by PiX (and the Claude, OpenAI and Deepseek AI band)")
		return
	}

	logger := log.New(os.Stderr, "easyvna-web: ", log.LstdFlags)
	listenSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			listenSet = true
		}
	})
	store, err := server.NewStore(*dataDir)
	if err != nil {
		fatal(logger, "data dir: %v", err)
	}

	var open server.Opener
	var setMock func(string) error
	var mockCut func(float64)
	var mockStd func(string)
	var mockAction func(string) error
	antennaName := ""
	if *useMock {
		if _, ok := mock.Scenarios[*mockAnt]; !ok {
			fatal(logger, "unknown -mock-antenna %q (have %s)", *mockAnt, strings.Join(scenarioNames(), ", "))
		}
		if *mockFW != "custom" && *mockFW != "stock" && *mockFW != "v2" {
			fatal(logger, "-mock-firmware must be custom, stock or v2")
		}
		bench, err := mock.NewBench(*mockAnt, *maxPoints, *mockFW == "custom")
		if err != nil {
			fatal(logger, "%v", err)
		}
		antennaName = *mockAnt
		setMock, mockCut, mockStd, mockAction = bench.SetAntenna, bench.Cut, bench.AttachStandard, bench.Action
		open = func(ctx context.Context) (vna.Device, error) {
			if *mockFW == "v2" {
				return openV2(ctx, bench.NewSimV2(), nanovnav2.Options{PortName: "mock (V2 simulator)", Mock: true}, *trace, logger)
			}
			return openDriver(ctx, bench.NewSim(), nanovnah.Options{PortName: "mock", Mock: true, MaxPoints: *maxPoints,
				CalSettle: 200 * time.Millisecond, ReadOnly: *readOnly}, *trace, logger)
		}
	} else {
		if *driver != "auto" && *driver != kindH && *driver != kindV2 {
			fatal(logger, "-driver must be auto, nanovna-h or nanovna-v2")
		}
		open = func(ctx context.Context) (vna.Device, error) {
			path, kind := *port, *driver
			if path == "" {
				p, k, err := detect()
				if err != nil {
					return nil, err
				}
				path = p
				if kind == "auto" {
					kind = k
				}
			}
			if kind == "auto" {
				kind = kindByPath(path)
			}
			sp, err := serialport.Open(path)
			if err != nil {
				return nil, err
			}
			var d vna.Device
			if kind == kindV2 {
				d, err = openV2(ctx, sp, nanovnav2.Options{PortName: path}, *trace, logger)
			} else {
				d, err = openDriver(ctx, sp, nanovnah.Options{PortName: path, MaxPoints: *maxPoints, CalSettle: *calSettle, ReadOnly: *readOnly}, *trace, logger)
			}
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
		ReadOnly: *readOnly, SetMockAntenna: setMock, MockScenarios: scen, MockCut: mockCut, MockStandard: mockStd, MockAction: mockAction, AppName: "EasyVNA Web " + version})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listenFallback(*listen, !listenSet, 20)
	if err != nil {
		fatal(logger, "listen: %v (another copy already running? use -listen 127.0.0.1:9090)", err)
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
	url := "http://" + browserHost(ln.Addr().String())
	logger.Printf("EasyVNA Web %s serving %s  [%s]  data: %s", version, url, mode, *dataDir)
	fmt.Fprintf(os.Stderr, "\n  EasyVNA Web running at %s\n  %s\n\n", url, quitHint(runtime.GOOS))
	if !*noBrowser {
		if err := openBrowser(runtime.GOOS, url); err != nil {
			logger.Printf("could not open a browser (%v): open %s yourself", err, url)
		}
	}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(logger, "%v", err)
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

func openV2(ctx context.Context, t nanovnav2.Transport, opt nanovnav2.Options, trace bool, l *log.Logger) (vna.Device, error) {
	if trace {
		opt.Trace = func(cmd string, raw []byte) { l.Printf("v2 %s -> %d bytes", cmd, len(raw)) }
	}
	return nanovnav2.Open(ctx, t, opt)
}

func detect() (string, string, error) {
	return pickPort(runtime.GOOS, serialport.List())
}

// kindByPath looks up the driver of an explicit -port by its USB ID
// (NanoVNA-H when unknown).
func kindByPath(path string) string {
	for _, c := range serialport.List() {
		if c.Path == path {
			if k := kindOf(c.ID); k != "" {
				return k
			}
		}
	}
	return kindH
}

func scenarioNames() []string { return mock.ScenarioNames() }
