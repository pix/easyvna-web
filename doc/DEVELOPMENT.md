# EasyVNA Web: developer notes

Back to the [README](../README.md).

## Build, test, run

```sh
go build -o easyvna-web .                  # Go 1.27+, CGO_ENABLED=0 works everywhere
go test -race ./... && go vet ./...
GOOS=windows GOARCH=amd64 go vet ./...     # also checked in CI
./easyvna-web -mock                        # simulated NanoVNA-H4, no hardware
scripts/dist.sh v0.1.0                     # release archives + SHA256SUMS into dist/
scripts/screenshots.sh mock                # README screenshots + doc/sample-report.pdf
scripts/screenshots.sh hardware [PORT]     # same against a real NanoVNA, server in -readonly
```

`scripts/screenshots.sh` needs node with Playwright (`PLAYWRIGHT_DIR` can
point at a directory containing `node_modules/playwright`), python3 with
Pillow, and optionally `pdftoppm` and `zopflipng`.

The only dependency is `golang.org/x/sys`. Serial ports use raw termios on
Linux and macOS, and the Win32 comm API on Windows. Port detection uses
sysfs on Linux, the registry on Windows (USB VID:PID, cross-checked with
`HARDWARE\DEVICEMAP\SERIALCOMM` to skip unplugged devices), and
`/dev/cu.usbmodem*` on macOS. CI runs vet and tests on Linux, plus tests and
a mock smoke test on Windows. Pushing a `v*` tag builds the release
archives (Windows zips and a macOS tarball).

## HTTP API

| | |
|---|---|
| `GET /api/state` | device, config, calibration, profiles |
| `GET /api/events` | SSE: `state`, `sweep`, `snapshots` |
| `POST /api/connect` | (re)connect |
| `POST /api/profile` `{"id":"869"}` | apply a profile (wide, 433, 869, 915, or custom = range saved in slot 2) |
| `POST /api/config` `{"start","stop","points","s21","portExtPs"}` | custom sweep |
| `POST /api/sweep` | single sweep, returns the result |
| `POST /api/continuous` `{"on":true}` | continuous sweeping (results via SSE) |
| `GET /api/last` | last result |
| `GET /api/export?format=s1p\|s2p\|csv[&snapshot=name]` | download |
| `POST /api/avg {"n":4}` | averaging (1, 2, 4, 8, 16) |
| `POST /api/tune {"on":true,"target":869000000}` / `{"on":false}` | TUNE mode |
| `POST /api/ref`, `DELETE /api/ref`, `POST /api/ref/undo`, `GET /api/ref?profile=` | REF trace per profile |
| `GET /api/trim`, `GET /api/trim/{name}`, `POST /api/trim/{name}/log {"lengthMm"} or {"cutMm"}`, `.../undo`, `.../target`, `.../select` | trim sessions |
| `POST /api/compare {"on"}`, `POST /api/compare/a {"name"}`, `POST /api/compare/add {"name"}`, `POST /api/compare/undo`, `DELETE /api/compare` | S21 compare |
| `POST /api/report {"name","profiles","trim","notes"}`, `GET /api/reports`, `GET /api/reports/{file}[?download=1]` | reports |
| `POST /api/hold {"on"}` / `{"n"}` / `{"sensitivity"}` / `{"clear":true}` / `{"remove":id}` / `{"undo":true}`, `GET /api/hold` | HOLD stable positions |
| `POST /api/mock/antenna {"name"}` | mock only: swap the simulated antenna |
| `GET/POST /api/snapshots`, `GET/DELETE /api/snapshots/{name}` | snapshots |
| `GET /api/cal`, `POST /api/cal/step/{reset,open,short,load,isoln,thru,done,on,off}`, `POST /api/cal/save {"slot"}`, `POST /api/cal/recall {"slot"}` | calibration |
| `PUT /api/profiles/{id}/slot {"slot"}`, `POST /api/settings {"autoRecall"}` | settings |

Example: `curl -s -XPOST localhost:8080/api/sweep | jq .summary.bands`

## Code layout

```
main.go, platform.go         flags, data dir per OS, port choice, browser, wiring
web/                         embedded UI (index.html, app.js, style.css; vanilla JS + canvas)
internal/rf/                 RF math: SWR, RL, mismatch loss, Z, Q, port extension,
                             bandwidth, dips, band verdicts, warnings, REF deltas,
                             TUNE readout, trim learning, Touchstone/CSV
internal/vna/                Device interface (+ optional Profiler)
internal/vna/nanovnah/       NanoVNA-H/H4 shell driver: shell I/O, parser, driver
internal/vna/mock/           byte-level shell simulator + multi-mode antenna model
internal/serialport/         serial ports: termios (Linux/macOS), Win32 comm API (Windows);
                             detection via sysfs / registry / cu.usbmodem; busy-port check
internal/server/             HTTP API, SSE hub, sweep loop, profiles, averaging, TUNE,
                             REF, trim, compare, report (SVG), store
```

## Shell protocol notes (NanoVNA-D `main.c`)

* Commands end with `\r` (`\n` is ignored), the line limit is 64 characters
  and the shell takes at most 4 arguments. Every character is echoed, Enter
  prints `\r\n`, output lines end in `\r\n`, and the prompt is `ch> `.
* An unknown command prints `<name>?`. Bad arguments print `usage: ...`.
  Asking for too many points prints `sweep points exceeds range N`.
* `scan start stop points outmask` takes mask bits 1 = freq, 2 = S11,
  4 = S21, 8 = no cal, 16 = no e-delay, 32 = no S21 offset, 128 = binary.
  Text output is one `freq re im [re im] ` line per point. `scan` pauses the
  on-device sweep; EasyVNA Web sends `resume` when it disconnects.
* `cal` with no arguments prints the set status bits by name:
  `load open short thru isoln Es Er Et cal'ed`, where `cal'ed` means the
  correction is applied.
* If `scan` prints nothing (firmware without outmask), the driver falls back
  to `sweep` + `resume` + `pause` + `frequencies` + `data 0/1`.

## Adding the NanoVNA V2 driver

The V2 (`04b4:0008`) uses a binary register protocol, not a text shell.
EasyVNA Web already detects it and reports that it is not supported. To add it:

1. Create `internal/vna/nanovnav2/` with a type that implements `vna.Device`
   (`internal/vna/vna.go`): `Info`, `Scan`, `CalStatus`, `Calibrate`, `SaveCal`,
   `RecallCal`, `Close`. Reuse `serialport.Open` for the transport.
2. `Scan` has to return frequencies plus S11 (and S21 when `req.S21` is set),
   with any segmentation hidden inside the driver. Everything else (analysis,
   UI, export) works unchanged.
3. The V2 has no on-device SOL calibration over USB. Return a `CalStatus`
   with `Known: false`, and either return an error from `Calibrate` or
   implement host-side SOL error correction inside the driver.
4. In `platform.go`, change `pickPort()` to return the V2 device, and in
   `main.go` open it with the new driver.
5. Test it the same way as the H driver: a scripted transport for the parser
   and a simulator for the driver.

## Tests

They cover the RF math (analytic series-RLC checks, bandwidth interpolation,
band-edge interpolation, dip and harmonic classification, Touchstone
round-trips) and the shell parser (recorded and synthetic transcripts: echo,
stale prompts, unknown or usage errors, trailing spaces, nan). They also run
the driver against a scripted transport and the byte-level simulator
(segmentation, learning the point limit, legacy fallback, cal flow, device
profiles), sysfs detection with a fake tree, Windows registry detection
with a fake registry (phantom ports, composite devices, COM10+), the
per-OS data dir, port choice and port fallback, and the HTTP API end to end
(profiles, cal, REF, TUNE/trim, HOLD, compare, report, snapshots, export,
SSE). Simulations of a trim session and of a hand-bent antenna check that
the trim fit and the HOLD motion rejection work.
