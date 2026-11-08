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
scripts/build-html.sh [VERSION]            # browser version: dist/easyvna-web.html (one file)
scripts/e2e-html.sh [SHOT_DIR]             # its end-to-end test in headless Chromium
scripts/test-js.sh                         # flasher unit tests (node, no dependency)
scripts/update-firmware.sh [TAG]           # bundle the latest EasyVNA firmware release
GOOS=js GOARCH=wasm go vet ./...           # the WebAssembly target (also checked in CI)
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

## Browser version (WebAssembly, one HTML file)

The page at <https://pix.github.io/easyvna-web/> is the same Go program
compiled with `GOOS=js GOARCH=wasm` and packed into one self-contained
HTML file. It loads nothing from the network: no CDN, fonts or separate
`.wasm`. It works from https and from `file://` in Chrome and Edge,
including Web Serial (checked: `file://` is a secure context there).

```
cmd/easyvna-wasm/      the program: server.New(...) with a memory store and a Web Serial
                       or simulator opener; exports goServe / goStream / goMockShell
web/browser/host.js    boots it and gives it what the OS gives the native program
web/browser/host.css   loader and host bar
internal/bridge/       in-process HTTP: one request into the http.Handler (Do), and the
                       SSE endpoint split into events (Stream)
internal/webserial/    nanovnah.Transport over bytes pushed by the page (Conn: VTIME-like
                       read timeout, end of stream), plus the syscall/js binding
internal/jsutil/       await a promise, copy bytes, return a promise (js/wasm only)
internal/htmlpack/     builds the page (cmd/build-html), verifies it can rebuild itself
```

How the pieces fit:

* **API.** `host.js` replaces `fetch` for URLs under `api/` and
  `EventSource('api/events')`. They go into the Go `http.Handler` running in
  the page (`goServe`, `goStream`), so `app.js` is unchanged apart from two
  hooks (`HOST.beforeConnect`, `HOST.openReport`) that are inert in the
  native program. Links into the API (exports, saved reports) are
  intercepted: downloads become Blob downloads, and reports and sheets open
  as `blob:` pages.
* **Storage.** `server.Store` sits on a `Backend`. Natively it is the data
  directory; in the browser it is a `MemBackend` loaded from IndexedDB at
  start-up and mirrored back on every change. The databases are
  `easyvna-web` and `easyvna-web-demo` (simulator). `/api/backup`
  (browser only, `Options.Backup`) exports and imports every file as one
  JSON document; the host bar's *Export data* / *Import data* use it.
* **USB.** *Connect* calls `navigator.serial.requestPort` (filters
  `0483:5740` and `04b4:0008`) inside the click. The Go side asks `EasyVNAHost.serial.open`
  for the port: the chosen one, else a previously granted one, which is
  how it reconnects after a reload or a replug. Then it opens it at
  115200 baud and pushes everything read into a `webserial.Conn` that the
  normal `nanovnah` driver uses. Unplugging ends the stream and the
  server drops the device. Opening errors suggest another program has the
  port.
* **Simulator.** `?demo` uses `mock.Bench`, as `-mock` does. Other query
  options: `?readonly`, `?antenna=869`, `?firmware=stock`, `?firmware=v2` (NanoVNA V2 simulator).
* **The file.** `htmlpack` writes a fixed skeleton plus `<script>`/`<style>`
  blocks: the UI body, `app.js`, `style.css`, `host.*`, `wasm_exec.js` from
  the Go toolchain, and the module as gzip + base64 (decoded with
  `DecompressionStream`). The HTML parser keeps the text of such blocks
  byte for byte, and the skeleton is stored as JSON in one of them, so the
  page can rebuild its own exact file. That is the hosted page's
  *Download for offline use*, hidden on `file://`. Sources are normalised
  to LF first; `.gitattributes` keeps LF in checkouts.
* **Size.** The module is about 15 MB, 3.7 MB gzipped (most of it is
  `net/http`); the page is about 5.5 MB (the bundled firmware, gzipped, about 0.15 MB of it). `wasm-opt -Oz` saves under 1 %
  after gzip, so it is not used.

`scripts/e2e-html.sh` covers it over `file://` and a local Pages-like
server:
* the simulator flows, Ctrl+P and the antenna sheet (one page on A4 and
  Letter), the report, Export/Import data and reload persistence;
* BATCH on a simulated lot (`?demo&antenna=lot869`): captures by button,
  key and auto-capture, rename, redo, delete + undo, the refusal on another
  profile, CSV / JSON export, JSON import, the batch print (exactly two
  pages on A4 and Letter with 5, 8 and 20 antennas, one trace per antenna
  plus the average, labels of the resonance strip neither overlapping nor
  clipped), the report section, reload; adjusted versions (trim in the
  simulator, *Adjusted…*, *Same as…*, unlink and delete with Undo, n, the
  delta line, as received vs after, the learnt MHz/mm, the two-page print
  with 8 antennas incl. 3 adjusted and with 20, CSV, JSON export and
  import) (`E2E_ONLY=batch` runs only those);
* acceptance, Measure ×N and the setup check (`E2E_ONLY=tolerance`): the
  SWR limit and tolerance change the verdicts, TUNE's aim and the batch
  statuses and are printed; Auto window; Measure ×5 with its progress,
  cancel and re-seat; batch capture ×3 (repeatability vs spread); the setup
  check on the `cm869` (cable radiates) and `869tuned` (quiet) antennas;
  photo caption and link note (200 characters, escaped, one page, default,
  reload, Export data);
* Web Serial through a fake `navigator.serial` whose port is backed by the
  Go simulator (`goMockShell`): request, open, driver, unplug, replug,
  reconnect after reload;
* opening measurement files (an exported `.s1p` again, a CSV with S21),
  their export, and "Back to live";
* the no-Web-Serial notice and opening a file from it, and the offline copy
  (byte-identical, runs from disk);
* the FIRMWARE dialog with a fake `navigator.usb` whose device is the
  simulated DfuSe bootloader of `scripts/flasher/dfuse-sim.js`: backup
  (bytes == flash), flashing the bundled EasyVNA image (only its pages
  erased, calibration untouched, verified), a user `.hex` with the automatic
  backup, refusals (save-area overlap, H4 image on a 128 KB NanoVNA-H),
  verify failure, restore of a full backup, read protection, no WebUSB,
  *Reboot into DFU* over the fake serial port; and the same flasher flow in
  the native program (`-mock`, firmware served from `go:embed`).
  `E2E_ONLY=flasher` runs only those.

It fails on any console error, and on any request other than the page itself.

Publishing: `.github/workflows/pages.yml` builds and deploys on every push
to `main` (Settings → Pages → Source: *GitHub Actions*). The release
workflow also attaches `easyvna-web.html` to each release.

## Firmware flasher (FIRMWARE dialog)

`web/static/flasher.js` is plain browser code shared by the program's page
and the single-file HTML (which inlines it, plus `firmware/manifest.json`
and the firmware, gzip + base64, read back by `EasyVNAHost.firmware()`).
It talks WebUSB to the STM32 system bootloader (`0483:df11`, DfuSe):

* the memory layout comes from the "@Internal Flash" alternate setting's
  name (`/0x08000000/128*0002Kg`: 256 KB = NanoVNA-H4 / F303, `064*`:
  128 KB = NanoVNA-H / F072), the transfer size from the DFU functional
  descriptor (2048 if absent);
* DNLOAD block 0 carries set-address (0x21) and page-erase (0x41); data
  blocks are 2 + n at pointer + n × transfer size; UPLOAD after a
  set-address and an ABORT; GETSTATUS is polled honouring bwPollTimeout,
  dfuERROR cleared with CLRSTATUS; leaving is set-address 0x08000000 then a
  zero-length DNLOAD;
* the firmware area ends at 0x08023800 on the H4 and 0x08018000 on the H
  (`SAVE_PROP_CONFIG_ADDR` in the firmware's `hardware.h`): a normal image
  must end there; only a full-size backup restore writes the save area.
  Never a mass erase, never read-unprotect (0x92).

`scripts/flasher/dfuse-sim.js` simulates that bootloader as a WebUSB device
(DFU state machine, poll timeouts, erase-before-write flash, F303 / F072,
read protection, a corrupted byte, empty interface names, no functional
descriptor); `scripts/flasher/flasher.test.js` (node --test) and the e2e
use it.

## HTTP API

| | |
|---|---|
| `GET /api/state` | device, config, calibration, profiles |
| `GET /api/events` | SSE: `state`, `sweep`, `snapshots` |
| `POST /api/connect` | (re)connect |
| `POST /api/dfu` | send `reset dfu` (FIRMWARE dialog's *Reboot into DFU*), drop the connection |
| `POST /api/profile` `{"id":"869"}` | apply a profile (wide, 433, 869, 915, or custom = range saved in slot 2) |
| `POST /api/config` `{"start","stop","points","s21","portExtPs"}` | custom sweep |
| `POST /api/sweep` | single sweep, returns the result |
| `POST /api/continuous` `{"on":true}` | continuous sweeping (results via SSE) |
| `GET /api/last` | last result |
| `GET /api/export?format=s1p\|s2p\|csv[&snapshot=name]` | download |
| `POST /api/file {"name","data"}` | open a measurement file (`.s1p`, `.s2p`, CSV) as the current result, `source` set, LIVE stopped |
| `POST /api/avg {"n":4}` | averaging (1, 2, 4, 8, 16) |
| `POST /api/acceptance {"profile","scope":"profile"\|"default","accept":{"swrLimit","tol","tolPct","tolMHz"}}` or `{"profile","reset":true}` | acceptance setting (band SWR limit, resonance tolerance auto / tight / normal / loose / custom), in `settings.json`; re-grades the results in memory |
| `POST /api/measure {"n","reseat","for":"measure"\|"batch"\|"setup","step","replace","label","adjust","change","trimMm"}`, `POST /api/measure/cancel`, `POST /api/measure/continue` | Measure ×N (progress in `state.measure`): the vector average becomes the last result with `multi` (repeatability); `batch` captures it into the active batch, `setup` is a setup-check step |
| `GET /api/setup`, `POST /api/setup {"reset":true}` | setup check (steps A / B / C, verdict); also in `state.setup` |
| `POST /api/mock/action {"action":"hand-on"\|"hand-off"\|"reseat"}` or `{"action":"trim","mm":0.5}` | mock only: a hand on the cable (the `cm869` antenna's cable radiates), unscrew and reconnect the antenna, shorten the connected element |
| `POST /api/tune {"on":true,"target":869000000}` / `{"on":false}` | TUNE mode |
| `POST /api/ref`, `DELETE /api/ref`, `POST /api/ref/undo`, `GET /api/ref?profile=` | REF trace per profile |
| `GET /api/trim`, `GET /api/trim/{name}`, `POST /api/trim/{name}/log {"lengthMm"} or {"cutMm"}`, `.../undo`, `.../target`, `.../select` | trim sessions |
| `POST /api/compare {"on"}`, `POST /api/compare/a {"name"}`, `POST /api/compare/add {"name"}`, `POST /api/compare/undo`, `DELETE /api/compare` | S21 compare |
| `POST /api/report {"name","profiles","trim","notes","batch","photo","link","photoCaption","linkNote"}`, `GET /api/reports`, `GET /api/reports/{file}[?download=1]` | reports |
| `POST /api/hold {"on"}` / `{"n"}` / `{"sensitivity"}` / `{"clear":true}` / `{"remove":id}` / `{"undo":true}`, `GET /api/hold` | HOLD stable positions |
| `POST /api/mock/antenna {"name"}` | mock only: swap the simulated antenna |
| `GET/POST /api/snapshots`, `GET/DELETE /api/snapshots/{name}` | snapshots |
| `GET /api/cal`, `POST /api/cal/step/{reset,open,short,load,isoln,thru,done,on,off}`, `POST /api/cal/save {"slot"}`, `POST /api/cal/recall {"slot"}` | calibration |
| `PUT /api/profiles/{id}/slot {"slot"}`, `POST /api/settings {"autoRecall","linkNoteDefault"}` | settings |
| `POST /api/sheet {"antenna":{name,type,band,connector,lengthMm,ground,cable,maker,owner,notes,link,photo,photoCaption,linkNote}}` | one-page antenna sheet from the last sweep (saved with the reports); the antenna record is remembered |
| `GET /api/antennas`, `GET /api/antennas/{name}` | remembered antenna records (`antennas/<name>.json`) |
| `GET /api/antennas/{name}/history`, `POST /api/antennas/{name}/history` (log the current result), `POST .../history/undo`, `GET .../history.csv` | antenna history (kept in the antenna record) with the drift report |
| `GET /api/batches`, `POST /api/batches {"name","notes","lengthMm","trim","link","photo","photoCaption","linkNote"}` (on the current band profile) | BATCH: list, create (and make it active) |
| `POST /api/batch {"active":id}` / `{"auto":bool}` | choose the active batch, switch auto-capture |
| `GET/PUT/DELETE /api/batches/{id}`, `POST .../restore` | a batch with every figure (per-antenna tuning, statistics, conclusion); rename / notes / length / trim log; delete and restore |
| `POST /api/batches/{id}/capture {"label"}` or `{"replace":n}` or `{"adjust":n,"change","trimMm"}`, `PUT/DELETE /api/batches/{id}/antennas/{n}`, `POST .../undo` | capture (a fresh sweep, refused on another profile), redo, an adjusted version of antenna n (kept with the earlier ones); PUT `{"label"}` renames, `{"adjustedFrom":m}` makes it a version of m's antenna (0 unlinks), `{"change","trimMm"}` edits the adjustment; delete (a version's chain is joined), undo the last change |
| `GET /api/batches/{id}/csv`, `GET .../json`, `POST /api/batches/import`, `POST .../sheet` | exports, import, one-page batch sheet (saved with the reports); `POST /api/report {"batch":id}` adds the statistics to the report |
| `GET /api/backup`, `POST /api/backup` | browser version only: every stored file as one JSON document |

Example: `curl -s -XPOST localhost:8080/api/sweep | jq .summary.bands`

## Code layout

```
main.go, platform.go         flags, data dir per OS, port choice, browser, wiring
web/                         embedded UI (index.html, app.js, style.css; vanilla JS + canvas),
                             flasher.js / flasher.css (FIRMWARE: WebUSB, STM32 DfuSe) and
                             firmware/ (bundled EasyVNA release + manifest.json)
scripts/flasher/             simulated DfuSe bootloader + flasher unit tests (node --test)
internal/rf/                 RF math: SWR, RL, mismatch loss, Z, Q, port extension,
                             bandwidth, dips, band verdicts, warnings, REF deltas,
                             TUNE readout, trim learning, Touchstone/CSV
internal/vna/                Device interface (+ optional Profiler)
internal/vna/nanovnah/       NanoVNA-H/H4 shell driver: shell I/O, parser, driver
internal/vna/nanovnav2/      NanoVNA V2 / LiteVNA binary protocol driver (EXPERIMENTAL)
internal/vna/mock/           byte-level shell and V2 simulators + multi-mode antenna model
internal/serialport/         serial ports: termios (Linux/macOS), Win32 comm API (Windows);
                             detection via sysfs / registry / cu.usbmodem; busy-port check
internal/qr/                 QR code encoder (byte mode, versions 1-40): the link's QR code on
                             printed sheets, checked against libqrencode output in testdata/
internal/server/             HTTP API, SSE hub, sweep loop, profiles, averaging, TUNE,
                             REF, trim, compare, report and antenna sheet (SVG), BATCH
                             (batch*.go: lots, tuning per antenna, statistics, sheet), store
                             (Backend: directory or memory), backup
cmd/easyvna-wasm/, web/browser/, internal/{bridge,webserial,jsutil,htmlpack}/
                             browser version (see above)
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

## NanoVNA V2 driver (EXPERIMENTAL)

`internal/vna/nanovnav2` speaks the V2 binary register protocol (USB
`04b4:0008`: NanoVNA V2 / V2 Plus / V2 Plus4, LiteVNA). It was written from
the published protocol description (protocol facts only) and tested against
`mock.SimV2` only. The package comment lists the opcodes, registers and the
32-byte FIFO record. Notes:

* `Open` sends eight NOPs, INDICATE (`0x0d`, answer `'2'`) and reads the
  variant, protocol, hardware and firmware registers; a text-shell device
  (echo, prompt) or silence is refused, and firmware major `0xff` means DFU.
* `Scan` splits the request into even segments of at most 101 points. For
  each: WRITE8 start and step, WRITE2 points and values per frequency,
  clear the FIFO, then READFIFO (≤ 255 records per request) until every
  index is filled. Records are only used from the first index 0 on, because
  the device sweeps continuously.
* Data are raw: S11 = rev0 / fwd0, S21 = rev1 / fwd0. `Info.HostCal` makes
  the server wrap the device in `server.hostCal`, which gives it the usual
  calibration flow (`Calibrate`, `SaveCal`, `RecallCal`) with one-port
  OSL error terms solved per point (`SolveOSL`, `CorrectOSL`), optional
  THRU / ISOLN normalisation of S21, linear interpolation for other grids,
  and slots stored as `hostcal/slot-N.json`. Host-cal errors are `CalError`
  and do not drop the device.
* `mock.SimV2` implements the protocol independently (continuous sweep,
  a stale record after a settings change, raw error model, noise);
  `Bench.AttachStandard` swaps in ideal standards while the server measures
  a calibration step (`Options.MockStandard`). Use `-mock-firmware v2` or
  `?demo&firmware=v2`; `goMockShell(antenna, "v2")` backs the e2e fake Web
  Serial port.
* Detection: `pickPort` returns the driver kind (`nanovna-h` wins when both
  are plugged in); `-driver` overrides it; Web Serial asks for both IDs and
  the wasm program picks the driver from the port's USB ID.

## Tests

They cover the RF math (analytic series-RLC checks, bandwidth interpolation,
band-edge interpolation, dip and harmonic classification, Touchstone
round-trips, Touchstone and CSV import from hand-written fixtures in
`internal/rf/testdata`: units, RI/MA/DB, 75 Ω renormalisation of one- and
two-port data, Touchstone 2.0, noise data, decimal commas, R/X columns, and
the refusals) and the shell parser (recorded and synthetic transcripts: echo,
stale prompts, unknown or usage errors, trailing spaces, nan). They also run
the driver against a scripted transport and the byte-level simulator
(segmentation, learning the point limit, legacy fallback, cal flow, device
profiles), sysfs detection with a fake tree, Windows registry detection
with a fake registry (phantom ports, composite devices, COM10+), the
per-OS data dir, port choice and port fallback, and the HTTP API end to end
(profiles, cal, REF, TUNE/trim, HOLD, compare, report, snapshots, export,
SSE, BATCH). Links: `CleanLink` (http/https only; tracking parameters
removed; Amazon `/dp/<ASIN>` and the AliExpress parameters kept from the
pasted link; nothing added), `LinkText` and the QR code on the sheet,
report and batch sheet. BATCH has tests of the lot statistics and the outlier rule
(n = 1, 2, identical values, non-finite values), the tuning status and trim
advice, the conclusion, the next-antenna detector (hand touch, drift,
swaps), persistence and the whole API on the simulated lot; adjusted
versions: the chains (missing capture, loop, two later versions), link,
unlink, delete with its splice and Undo, the current version in every lot
figure (n, outliers), the lot as received, MHz/mm learnt from the trims
(`rf.LearnSlope`, pooled logs), CSV / JSON round trip and older batches. Acceptance: the
SWR-limit mapping, the presets and the Auto window (synthetic curves), the
API and its persistence in `settings.json` / *Export data*; the
margin-based lot grades (n = 2 with its confidence interval, 0.7 % offset
with a wide margin, near the margin, failures, repeatability vs spread);
the batch print's two pages and the strip's label layout. Measure ×N: the
vector average and repeatability, the re-seat detector, cancel and
continue, batch capture; the setup check thresholds and both simulated
cables. Simulations of a trim session and of a hand-bent antenna check that
the trim fit and the HOLD motion rejection work.
