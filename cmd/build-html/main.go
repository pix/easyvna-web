// Command build-html writes the single-file browser version of EasyVNA Web
// (see internal/htmlpack). Use scripts/build-html.sh, which builds the
// WebAssembly module first.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
	page, err := htmlpack.Build(htmlpack.Inputs{
		IndexHTML: read(rp("web/static/index.html")), StyleCSS: read(rp("web/static/style.css")), AppJS: read(rp("web/static/app.js")),
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
	fmt.Printf("%s: %d bytes (wasm %d bytes)\n", *out, len(page), len(read(*wasm)))
}
