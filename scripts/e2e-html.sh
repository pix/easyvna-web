#!/usr/bin/env bash
# End-to-end test of the single-file browser build in headless Chromium
# (see scripts/e2e-html/e2e.js). Builds dist/easyvna-web.html first.
#   scripts/e2e-html.sh [SCREENSHOT_DIR]
# Needs node with Playwright and its Chromium (PLAYWRIGHT_DIR can point at a
# directory containing node_modules/playwright).
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/build-html.sh
node scripts/e2e-html/e2e.js dist/easyvna-web.html "${1:-}"
