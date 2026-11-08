#!/usr/bin/env bash
# Unit tests of the browser JavaScript that has no DOM dependency: the
# firmware flasher (web/static/flasher.js) against a simulated STM32 DFU
# bootloader (scripts/flasher/dfuse-sim.js). Needs node 18+, nothing else.
set -euo pipefail
cd "$(dirname "$0")/.."
node --check web/static/flasher.js
node --check web/static/app.js
node --check web/browser/host.js
node --test scripts/flasher/
