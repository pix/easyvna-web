# vnaview

A small Go program with an embedded web UI for checking LoRa antennas
(433 / 869 / 915 MHz) on a NanoVNA. It sweeps S11, grades the match per band,
finds resonances, and calibrates from the browser. It is a single binary with
no CDN or network dependencies, so it works offline.

Target hardware: **NanoVNA-H4 / NanoVNA-H** running NanoVNA-D (DiSlord) based
firmware, over the USB CDC text shell (USB `0483:5740`).

## Run

```sh
go build -o vnaview .
./vnaview                 # auto-detect the NanoVNA, serve http://127.0.0.1:8080
./vnaview -mock           # no hardware: simulated H4 + test antenna
```

Then open http://127.0.0.1:8080.

| flag | default | meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8080` | HTTP address |
| `-port` | auto | serial device, e.g. `/dev/serial/by-id/usb-nanovna.com_NanoVNA-H4_400-if00`; auto-detect looks for USB `0483:5740` on `/dev/ttyACM*` (sysfs only, other ports are never opened) |
| `-readonly` | off | never change stored device state: cal, save, recall and on-device profile switching are refused (in the server and the driver); sweeps still work |
| `-mock` | off | simulate a NanoVNA-H4 (the real shell protocol is spoken by the simulator) |
| `-mock-antenna` | `dual` | `dual` (433 whip, slightly long, with a 2nd mode near 850 MHz), `869` (whip slightly long), `869tuned`, `915` (tuned), `dummy` (lossy resistor posing as an antenna), `open` (no antenna), `bend` (whip being bent by hand: a new still position every 6 s with a motion burst in between). Switch live under *Device* in the UI |
| `-mock-firmware` | `custom` | `custom` simulates the PiX fork (`profile` command, `Fork: PiX` line), `stock` plain NanoVNA-D |
| `-data` | `$XDG_DATA_HOME/vnaview` or `~/.local/share/vnaview` | settings, REF traces, trim logs, compare session, reports, snapshots |
| `-maxpoints` | auto | points per device scan (auto: 401 on H4, 101 on H, learnt from the firmware's error otherwise) |
| `-cal-settle` | `1.5s` | time the device sweeps with a standard attached before `cal <std>` (older firmware copies the last live sweep) |
| `-trace` | off | log every shell exchange |

Linux only for serial access (raw termios via `syscall`). Your user needs
access to the tty (group `uucp` on Arch, `dialout` on Debian/Ubuntu).

## Using it

The page reads top to bottom. Profile → calibration (only shown when
needed) → big action tiles → verdict → SWR plot. Every tile shows its state.
Overwrites ask first and say what they replace, and cheap actions offer
**Undo** in the toast.

1. **Profiles** (one tap sweeps once): **Wide** 300–1000 MHz and **Survey**
   50–1500 MHz identify an antenna. They list every dip with SWR, mismatch
   loss, RL, R+jX, SWR<2 bandwidth and Q, plus the nearest LoRa band and a
   trim hint. Higher-order modes are flagged. **433 / 869 / 915** grade the
   band.

   | profile | range (MHz) | points | slot |
   |---|---|---|---|
   | survey | 50–1500 | 401 | 1 |
   | wide | 300–1000 | 401 | 3 |
   | 433 | 388.92–478.92 | 401 | 4 |
   | 869 | 779–959 | 401 | 5 |
   | 915 | 825–1005 | 401 | 6 |
   | custom | as saved | as saved | 2 |

   Slot 0 is left for the user, because stock firmware loads it at power-on.
   The same slots are used with stock firmware, so a cal saved from either
   side is found by the other. Offsets are measured from 433.92, 869 and
   915 MHz.
2. **Calibration strip:** a green one-liner when the active calibration
   matches this profile. Otherwise it opens the one-tap
   **OPEN → SHORT → LOAD → DONE → SAVE** flow. Fit the standards at the end
   of the pigtail.
3. **Tiles:**
   * **SWEEP**: one sweep.
   * **LIVE**: continuous sweeping.
   * **TUNE**: full-screen trimming (below).
   * **REF**: one tap stores the trace as a dashed reference (tap again to
     replace, × to clear, both undoable). A row then shows **Δf** of the
     resonance, **ΔSWR** at the band reference and the change in band worst
     SWR. Detuning test: REF in free air, then hand or enclosure, then
     read Δf.
   * **HOLD**: a *stable positions* envelope for bend and hand-effect tests.
     * A sweep whose RMS |ΔΓ| to the previous one exceeds the motion
       threshold is shown as "moving…" and rejected. Sensitivity low, normal
       or high is 0.010, 0.006 or 0.004. A static antenna on the real H4
       measures about 0.0016 between sweeps; a 1-point (450 kHz at 869)
       resonance shift is about 0.004.
     * When N consecutive sweeps agree (default 3, adjustable 2–5), their
       complex average, lightly smoothed, is captured as one position, and a
       toast says "position 4 captured".
     * A stable run that matches an existing position is not added again.
       Slow creep is caught by also comparing against the start of the run.
     * The envelope is min/max over positions only. The panel shows the
       number of positions, the **resonance spread** (min–max and Δ MHz)
       and the worst in-band SWR across positions.
     * Tap a position to highlight its trace, or × to remove it. Clear and
       remove can be undone.
   * **AVG**: tap to cycle the complex average over ×1/2/4/8/16 sweeps.
   * **COMPARE** and **REPORT**: below.
4. **Automatic warnings:** "No antenna?" when |Γ| ≈ 1 everywhere, "Reflection
   above 1" when the calibration does not fit, and "Looks lossy / resistive
   (fake?)" when SWR is low and flat across a wide sweep with little
   reactance swing. While a warning is up, verdicts show **CHECK** instead of
   GOOD.
5. **Verdict:** GOOD (worst in-band SWR < 1.5), OK (< 2) or BAD. Each card
   shows worst SWR, its **mismatch loss**, RL, best SWR, % of the band under
   SWR 2, and the resonance with its offset and Q. Mismatch loss
   (−10·log10(1−|Γ|²)) is also in the cursor readout, the dip tables, TUNE
   and the report.
6. **Plots:** SWR with band shading, REF and HOLD. Smith, return loss, R/X
   and S21 are in tabs.

### TUNE and the trim calculator

TUNE opens a full-screen view that runs a fast, narrow live sweep (101
points, inside the profile's calibrated range, re-centred automatically).
It shows the resonance, the offset to the target with **▼ SHORTEN / ▲
LENGTHEN / ✓ ON TARGET** (resonance below target = too long), SWR at target
(colour-graded), RL and mismatch loss. **Tone** (off by default) plays a
pitch that rises as SWR at the target falls, so you can trim without
looking.

Trim calculator: enter the element length once and tap **Start**. The first
prediction is L_new = L × f_res / f_target. After each cut, enter the cut
(prefilled at 80 % of the suggestion) and tap **Measure & log**, which takes
a fresh sweep. vnaview learns this antenna's MHz/mm with a regularised fit:
the textbook slope f/L is the prior, and logged cuts take over once they
move the resonance clearly beyond the measurement scatter. The big CUT
number always uses the live resonance, so it agrees with the readout above
it. Logs are stored per antenna in `<data>/trim/`, with undo of the last
entry and a sparkline and table. In `-mock`, logging a cut also shortens the
simulated element (f = k/(L+δ), δ = 6 mm), so the learning can be watched
without hardware.

### S21 COMPARE

A two-step wizard with large step text. Port 1 goes to a fixed reference
antenna and port 2 to antenna A; tap **Store A**. Then swap port 2 to each
candidate. It shows the live in-band **Δ dB vs A** (power average) in large
type, worst and best Δ, and a Δ trace, and **Add to leaderboard** records
each antenna. The measurement is relative only, so no THRU calibration is
needed; keep the geometry fixed. Replacing A or starting over asks first and
can be undone. The session is stored in `<data>/compare.json`.

### REPORT

Pick a name, the profile sweeps to include (the latest of each), the trim
log and optional notes. You get a printable HTML page, which prints to PDF
with A4 margins and needs no JavaScript. It contains:
* the verdict per band with mismatch loss;
* SWR, RL and Smith plots for each sweep, as inline SVG;
* resonances with BW and Q;
* the trim log;
* the S21 compare leaderboard.

Reports are saved in `<data>/reports/` and listed in the dialog for
download.

### Advanced, snapshots, device

* *Advanced*: manual start, stop and points; S21; port extension; restore
  the custom range saved in slot 2.
* *Snapshots & export*: named snapshots and .s1p/.s2p/CSV export.
* *Device*: device info and profiles. In mock mode it also has the mock
  antenna switch.

### On-device profiles (PiX firmware fork)

vnaview treats the device as the PiX fork when version/info prints a
`Fork: PiX (...)` line or when `help` lists `profile`. The device badge then
shows "PiX fork". `profile` with no argument prints:

```
wide 300000000 1000000000 401 3 1 0      name start stop points slot cal active
...
custom 850000000 950000000 201 2 1 0     custom start stop points slot saved active
boot 869 ask=1
```

On connect, vnaview adopts the device's ranges, points and slots. Tapping a
profile runs `profile <wide|433|869|915|custom>`, so the device and the
browser agree. Stock firmware falls back to `scan` with explicit ranges plus
`recall N`, using the same slots. Calibration always uses stock commands:
`cal reset|open|short|load|done`, then `save N`.

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

## Layout

```
main.go                      flags, device auto-detect, wiring
web/                         embedded UI (index.html, app.js, style.css; vanilla JS + canvas)
internal/rf/                 RF math: SWR, RL, mismatch loss, Z, Q, port extension,
                             bandwidth, dips, band verdicts, warnings, REF deltas,
                             TUNE readout, trim learning, Touchstone/CSV
internal/vna/                Device interface (+ optional Profiler)
internal/vna/nanovnah/       NanoVNA-H/H4 shell driver: shell I/O, parser, driver
internal/vna/mock/           byte-level shell simulator + multi-mode antenna model
internal/serialport/         raw termios port (TIOCEXCL), sysfs VID:PID detection,
                             refuses ports held by another process
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
  on-device sweep; vnaview sends `resume` when it disconnects.
* `cal` with no arguments prints the set status bits by name:
  `load open short thru isoln Es Er Et cal'ed`, where `cal'ed` means the
  correction is applied.
* If `scan` prints nothing (firmware without outmask), the driver falls back
  to `sweep` + `resume` + `pause` + `frequencies` + `data 0/1`.

## Adding the NanoVNA V2 driver

The V2 (`04b4:0008`) uses a binary register protocol, not a text shell.
vnaview already detects it and reports that it is not supported. To add it:

1. Create `internal/vna/nanovnav2/` with a type that implements `vna.Device`
   (`internal/vna/vna.go`): `Info`, `Scan`, `CalStatus`, `Calibrate`, `SaveCal`,
   `RecallCal`, `Close`. Reuse `serialport.Open` for the transport.
2. `Scan` has to return frequencies plus S11 (and S21 when `req.S21` is set),
   with any segmentation hidden inside the driver. Everything else (analysis,
   UI, export) works unchanged.
3. The V2 has no on-device SOL calibration over USB. Return a `CalStatus`
   with `Known: false`, and either return an error from `Calibrate` or
   implement host-side SOL error correction inside the driver.
4. In `main.go`, change `detect()` to return the V2 device and open it with
   the new driver.
5. Test it the same way as the H driver: a scripted transport for the parser
   and a simulator for the driver.

## Tests

```sh
go vet ./... && go test -race ./...
```

They cover the RF math (analytic series-RLC checks, bandwidth interpolation,
band-edge interpolation, dip and harmonic classification, Touchstone
round-trips) and the shell parser (recorded and synthetic transcripts: echo,
stale prompts, unknown or usage errors, trailing spaces, nan). They also run
the driver against a scripted transport and the byte-level simulator
(segmentation, learning the point limit, legacy fallback, cal flow, device
profiles), sysfs detection with a fake tree, and the HTTP API end to end
(profiles, cal save/auto-recall, device profiles, snapshots, export, SSE).
