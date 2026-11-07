#!/usr/bin/env bash
# Regenerate doc/screenshots/*.png.
#   scripts/screenshots.sh mock                 # simulator, full set
#   scripts/screenshots.sh hardware [PORT]      # real NanoVNA, read-only
# Needs node with playwright (set PLAYWRIGHT_DIR to a directory containing
# node_modules/playwright if it is not installed globally).
# Hardware mode starts the server with -readonly: it only sweeps, it never
# calibrates, saves, recalls or switches on-device profiles. It refuses to
# run when another program holds the port.
set -euo pipefail
MODE="${1:-mock}"
PORT="${2:-}"
cd "$(dirname "$0")/.."
BIN="$(mktemp -d)/easyvna-web"
DATA="$(mktemp -d)"
go build -o "$BIN" .
LISTEN=127.0.0.1:18200
case "$MODE" in
  mock) "$BIN" -mock -mock-antenna 869 -no-browser -listen "$LISTEN" -data "$DATA" & ;;
  hardware) "$BIN" -readonly -no-browser -listen "$LISTEN" -data "$DATA" ${PORT:+-port "$PORT"} & ;;
  *) echo "usage: $0 mock|hardware [port]" >&2; exit 2 ;;
esac
PID=$!
trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null; rm -rf "$DATA" "$(dirname "$BIN")"' EXIT
for _ in $(seq 1 50); do curl -fs "http://$LISTEN/api/state" >/dev/null 2>&1 && break; sleep 0.2; done
BASE="http://$LISTEN/" OUT="doc/screenshots" MODE="$MODE" node scripts/screenshots/shots.js
