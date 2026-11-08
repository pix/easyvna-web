//go:build js && wasm

// Command easyvna-wasm is EasyVNA Web compiled to WebAssembly: the same
// server (API, sweep loop, analysis, reports) running inside the page.
// web/browser/host.js boots it, routes the UI's fetch('api/...') and
// EventSource('api/events') into it, stores data in IndexedDB and opens
// the NanoVNA with Web Serial. scripts/build-html.sh packs everything into
// one HTML file.
package main

import (
	"context"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"syscall/js"
	"time"

	"github.com/pix/easyvna-web/internal/bridge"
	"github.com/pix/easyvna-web/internal/jsutil"
	"github.com/pix/easyvna-web/internal/server"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/mock"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
	"github.com/pix/easyvna-web/internal/vna/nanovnav2"
	"github.com/pix/easyvna-web/internal/webserial"
)

// version is set at build time: -ldflags "-X main.version=v0.2.0".
var version = "dev"

// config is EasyVNAHost.config, filled in by host.js.
type config struct {
	demo, readOnly    bool
	antenna, firmware string
	storeLabel        string
	files             []server.MemFile
}

func readConfig(host js.Value) config {
	c := host.Get("config")
	str := func(k, def string) string {
		if v := c.Get(k); v.Type() == js.TypeString && v.String() != "" {
			return v.String()
		}
		return def
	}
	cfg := config{demo: c.Get("demo").Truthy(), readOnly: c.Get("readOnly").Truthy(),
		antenna: str("antenna", "dual"), firmware: str("firmware", "custom"), storeLabel: str("storeLabel", "this browser")}
	if files := c.Get("files"); files.Type() == js.TypeObject {
		for i := 0; i < files.Length(); i++ {
			f := files.Index(i)
			mt := time.UnixMilli(int64(f.Get("mtime").Float()))
			cfg.files = append(cfg.files, server.MemFile{Name: f.Get("name").String(), Data: jsutil.Bytes(f.Get("data")), ModTime: mt})
		}
	}
	return cfg
}

func main() {
	host := js.Global().Get("EasyVNAHost")
	cfg := readConfig(host)
	logger := log.New(os.Stderr, "easyvna-web: ", 0)

	mem := server.NewMemBackend(cfg.files)
	storage := host.Get("storage")
	mem.OnChange = func(f server.MemFile, removed bool) {
		if removed {
			storage.Call("remove", f.Name)
			return
		}
		storage.Call("put", f.Name, jsutil.Uint8Array(f.Data), float64(f.ModTime.UnixMilli()))
	}
	store := server.NewStoreBackend(mem, cfg.storeLabel)

	opt := server.Options{Store: store, Static: emptyFS{}, Logger: logger, ReadOnly: cfg.readOnly,
		Backup: true, AppName: "EasyVNA Web " + version + " (browser)", HomeURL: "https://pix.github.io/easyvna-web/"}
	if cfg.demo {
		if _, ok := mock.Scenarios[cfg.antenna]; !ok {
			cfg.antenna = "dual"
		}
		bench, _ := mock.NewBench(cfg.antenna, 0, cfg.firmware != "stock")
		opt.MockAntenna, opt.MockScenarios = cfg.antenna, mock.ScenarioNames()
		opt.SetMockAntenna, opt.MockCut, opt.MockStandard, opt.MockAction = bench.SetAntenna, bench.Cut, bench.AttachStandard, bench.Action
		opt.Open = func(ctx context.Context) (vna.Device, error) {
			if cfg.firmware == "v2" {
				return nanovnav2.Open(ctx, bench.NewSimV2(), nanovnav2.Options{PortName: "V2 simulator (in this page)", Mock: true})
			}
			return nanovnah.Open(ctx, bench.NewSim(), nanovnah.Options{PortName: "simulator (in this page)", Mock: true,
				CalSettle: 200 * time.Millisecond, ReadOnly: cfg.readOnly})
		}
	} else {
		adapter := host.Get("serial")
		opt.Open = func(ctx context.Context) (vna.Device, error) {
			conn, name, err := webserial.Open(ctx, adapter)
			if err != nil {
				return nil, err
			}
			var d vna.Device
			if webserial.USBID(name) == "04b4:0008" { // NanoVNA V2 / LiteVNA: binary protocol (experimental)
				d, err = nanovnav2.Open(ctx, conn, nanovnav2.Options{PortName: name})
			} else {
				d, err = nanovnah.Open(ctx, conn, nanovnah.Options{PortName: name, CalSettle: 1500 * time.Millisecond, ReadOnly: cfg.readOnly})
			}
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			return d, nil
		}
	}
	srv := server.New(opt)
	h := srv.Handler()
	go srv.Run(context.Background())

	host.Set("goServe", js.FuncOf(func(this js.Value, args []js.Value) any { return serve(h, args) }))
	host.Set("goStream", js.FuncOf(func(this js.Value, args []js.Value) any { return stream(h, args) }))
	host.Set("goMockShell", js.FuncOf(mockShell))
	host.Call("goReady", version)
	select {}
}

// serve(method, path, headers [[k, v]...], body Uint8Array)
// -> Promise<{status, headers: [[k, v]...], body: Uint8Array}>
func serve(h http.Handler, args []js.Value) any {
	method, path := args[0].String(), args[1].String()
	hdr := http.Header{}
	if hs := args[2]; hs.Type() == js.TypeObject {
		for i := 0; i < hs.Length(); i++ {
			hdr.Add(hs.Index(i).Index(0).String(), hs.Index(i).Index(1).String())
		}
	}
	body := jsutil.Bytes(args[3])
	return jsutil.Promise(func() (any, error) {
		r, err := bridge.Do(context.Background(), h, method, path, hdr, body)
		if err != nil {
			return nil, err
		}
		hs := []any{}
		for k, vs := range r.Header {
			for _, v := range vs {
				hs = append(hs, []any{k, v})
			}
		}
		return map[string]any{"status": r.Status, "headers": hs, "body": jsutil.Uint8Array(r.Body)}, nil
	})
}

// stream(path, onEvent(name, data), onEnd(error message)) -> cancel()
func stream(h http.Handler, args []js.Value) any {
	path, onEvent, onEnd := args[0].String(), args[1], args[2]
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err := bridge.Stream(ctx, h, path, func(e bridge.Event) { onEvent.Invoke(e.Name, e.Data) })
		msg := ""
		if err != nil && ctx.Err() == nil {
			msg = err.Error()
		}
		onEnd.Invoke(msg)
	}()
	var cancelFn js.Func
	cancelFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		cancel()
		cancelFn.Release()
		return nil
	})
	return cancelFn
}

// mockShell(antenna[, kind]) returns a byte-level NanoVNA simulator for
// tests of the Web Serial path: {write(Uint8Array), read() ->
// Promise<Uint8Array|null>, close()}. kind "v2" gives a NanoVNA V2 (binary
// protocol) instead of the H4 text shell. The end-to-end test backs a fake
// navigator.serial port with it, so requestPort -> open ->
// readable/writable -> driver runs for real.
func mockShell(this js.Value, args []js.Value) any {
	ant := "869"
	if len(args) > 0 && args[0].Type() == js.TypeString {
		if _, ok := mock.Scenarios[args[0].String()]; ok {
			ant = args[0].String()
		}
	}
	var sim io.ReadWriteCloser
	if len(args) > 1 && args[1].Type() == js.TypeString && args[1].String() == "v2" {
		sim = mock.NewV2(mock.V2Options{Antenna: mock.Scenarios[ant]()})
	} else {
		sim = mock.New(mock.Options{Antenna: mock.Scenarios[ant](), Profiles: true})
	}
	writes := make(chan []byte, 256)
	closed := make(chan struct{})
	go func() { // keep write order; Sim.Write runs commands synchronously
		for {
			select {
			case b := <-writes:
				_, _ = sim.Write(b)
			case <-closed:
				return
			}
		}
	}()
	isClosed := func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	}
	obj := js.Global().Get("Object").New()
	obj.Set("write", js.FuncOf(func(this js.Value, a []js.Value) any {
		if !isClosed() {
			writes <- jsutil.Bytes(a[0])
		}
		return nil
	}))
	obj.Set("read", js.FuncOf(func(this js.Value, a []js.Value) any {
		return jsutil.Promise(func() (any, error) {
			buf := make([]byte, 4096)
			for !isClosed() {
				n, _ := sim.Read(buf) // (0, io.EOF) = nothing yet
				if n > 0 {
					return jsutil.Uint8Array(buf[:n]), nil
				}
			}
			return nil, nil
		})
	}))
	obj.Set("close", js.FuncOf(func(this js.Value, a []js.Value) any {
		if !isClosed() {
			close(closed)
			_ = sim.Close()
		}
		return nil
	}))
	return obj
}

// emptyFS serves no static files: the UI is part of the page itself.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
