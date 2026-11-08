// Command build-html writes the single-file browser version of EasyVNA Web
// (see internal/htmlpack). Use scripts/build-html.sh, which builds the
// WebAssembly module first.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pix/easyvna-web/internal/htmlpack"
)

func main() {
	wasm := flag.String("wasm", "", "GOOS=js GOARCH=wasm build of ./cmd/easyvna-wasm")
	wasmExec := flag.String("wasm-exec", "", "wasm_exec.js of the Go toolchain that built -wasm")
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "dist/easyvna-web.html", "output file")
	version := flag.String("version", "dev", "version shown in the page")
	flag.Parse()
	if *wasm == "" || *wasmExec == "" {
		fmt.Fprintln(os.Stderr, "usage: build-html -wasm app.wasm -wasm-exec wasm_exec.js [-out file] [-version v]")
		os.Exit(2)
	}
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "build-html:", err)
			os.Exit(1)
		}
		return b
	}
	rp := func(p string) string { return filepath.Join(*root, filepath.FromSlash(p)) }
	// The bundled firmware (scripts/update-firmware.sh): the manifest names
	// the file; htmlpack checks its SHA-256.
	manifest := read(rp("web/static/firmware/manifest.json"))
	var fm htmlpack.FirmwareManifest
	if err := json.Unmarshal(manifest, &fm); err != nil || fm.File == "" || strings.ContainsAny(fm.File, `/\`) {
		fmt.Fprintln(os.Stderr, "build-html: bad web/static/firmware/manifest.json:", err)
		os.Exit(1)
	}
	firmware := read(rp("web/static/firmware/" + fm.File))
	page, err := htmlpack.Build(htmlpack.Inputs{
		IndexHTML: read(rp("web/static/index.html")), StyleCSS: read(rp("web/static/style.css")), AppJS: read(rp("web/static/app.js")),
		FlasherJS: read(rp("web/static/flasher.js")), FlasherCSS: read(rp("web/static/flasher.css")),
		FirmwareManifest: manifest, Firmware: firmware,
		HostCSS: read(rp("web/browser/host.css")), HostJS: read(rp("web/browser/host.js")),
		WasmExecJS: read(*wasmExec), Wasm: read(*wasm), Version: *version,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build-html:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, page, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "build-html:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d bytes (wasm %d bytes, firmware %s %d bytes)\n", *out, len(page), len(read(*wasm)), fm.File, len(firmware))
}
