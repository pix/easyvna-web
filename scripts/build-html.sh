#!/usr/bin/env bash
# Build the single-file browser version: dist/easyvna-web.html (Go compiled
# to WebAssembly + the web UI, everything inline; no server, no network).
#   scripts/build-html.sh [VERSION]      # default: git describe
# OUT=path overrides the output file.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="${OUT:-dist/easyvna-web.html}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o "$tmp/easyvna-web.wasm" ./cmd/easyvna-wasm
goroot="$(go env GOROOT)"
exec_js="$goroot/lib/wasm/wasm_exec.js"           # Go 1.24+
[ -f "$exec_js" ] || exec_js="$goroot/misc/wasm/wasm_exec.js"
mkdir -p "$(dirname "$OUT")"
go run ./cmd/build-html -wasm "$tmp/easyvna-web.wasm" -wasm-exec "$exec_js" -version "$VERSION" -out "$OUT"
