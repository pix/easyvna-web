#!/usr/bin/env bash
# End-to-end test of the single-file browser build in headless Chromium
# (see scripts/e2e-html/e2e.js). Builds dist/easyvna-web.html first, and the
# native program, whose FIRMWARE dialog (go:embed firmware) is tested too.
# E2E_ONLY=flasher runs only the FIRMWARE dialog checks, E2E_ONLY=batch only BATCH.
#   scripts/e2e-html.sh [SCREENSHOT_DIR]
# Needs node with Playwright and its Chromium (PLAYWRIGHT_DIR can point at a
# directory containing node_modules/playwright).
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/build-html.sh
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
go build -o "$tmp/easyvna-web" .
NATIVE_BIN="$tmp/easyvna-web" node scripts/e2e-html/e2e.js dist/easyvna-web.html "${1:-}"
