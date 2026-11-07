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
| `-port` | auto | serial device; auto-detect looks for USB `0483:5740` on `/dev/ttyACM*` |
| `-mock` | off | simulate a NanoVNA-H4 (the real shell protocol is spoken by the simulator) |
| `-mock-antenna` | `dual` | `dual` (433 whip, slightly long, with a 2nd mode near 850 MHz), `869` (whip slightly long), `915` (tuned) |
| `-mock-firmware` | `custom` | `custom` simulates the PiX fork (`profile` command, `Fork: PiX` line), `stock` plain NanoVNA-D |
| `-data` | `$XDG_DATA_HOME/vnaview` or `~/.local/share/vnaview` | snapshots and settings |
| `-maxpoints` | auto | points per device scan (auto: 401 on H4, 101 on H, learnt from the firmware's error otherwise) |
| `-cal-settle` | `1.5s` | time the device sweeps with a standard attached before `cal <std>` (older firmware copies the last live sweep) |
| `-trace` | off | log every shell exchange |

Linux only for serial access (raw termios via `syscall`). Your user needs
access to the tty (group `uucp` on Arch, `dialout` on Debian/Ubuntu).

## Using it

1. **Pick a profile** (one tap sweeps once):
   * **Wide** (300 to 1000 MHz) identifies the antenna. It lists every resonance
     dip with frequency, SWR, return loss, R+jX and SWR<2 bandwidth, says which
     LoRa band it is in or how far off it is, and flags dips at about n times a
     lower one as higher-order modes.
   * **433** (388.92 to 478.92), **869** (779 to 959) and **915** (825 to 1005)
     contain the band with enough span to see a detuned dip. The band is
     shaded, and 869 also reports the 869.4 to 869.65 MHz sub-band.
   * Resonance offsets are measured from 433.92, 869 and 915 MHz, the same
     reference the firmware uses.
   * Profile ranges, points and slots match the PiX firmware v2 table:

     | profile | range (MHz) | points | slot |
     |---|---|---|---|
     | wide | 300–1000 | 401 | 3 |
     | 433 | 388.92–478.92 | 401 | 4 |
     | 869 | 779–959 | 401 | 5 |
     | 915 | 825–1005 | 401 | 6 |
     | custom | as saved | as saved | 2 |

     Slots 0 and 1 are left for the user, because stock firmware loads slot 0
     at power-on. vnaview uses the same slots on stock firmware, so a
     calibration saved from either side is found by the other.
2. **Read the verdict.** Each band gets GOOD (worst in-band SWR < 1.5), OK (< 2)
   or BAD (otherwise). The verdict shows worst and best SWR with the frequency of
   each, worst-case return loss, and % of the band below SWR 2. It also shows the
   resonance frequency, with its offset from the band edge and from the centre.
   Band edges are interpolated, so a coarse sweep cannot hide a bad edge.
   Trimming hints ("too long: shorten by about 1.7 %") assume a length-tuned
   monopole, dipole or whip, where a resonance below the band means the
   radiator is too long.
3. **Plots.** The main SWR plot has the band shaded, SWR 1.5 and 2 lines, labels
   on resonances and a cursor readout (hover or tap; the arrow keys move it).
   The Smith chart, return loss and R/X plots are in tabs.
4. **Calibrate per profile.** Each sweep range needs its own calibration. Fit
   the standards at the end of the pigtail, then tap **OPEN, SHORT, LOAD,
   DONE, SAVE**. OPEN starts a new calibration (`cal reset`). The save slot
   defaults to the profile's slot (see the table; custom ranges use slot 2)
   and can be changed. The strip shows the firmware's `cal` flags. It also shows which
   range the active calibration was made for, and warns when it does not
   match the current sweep. With *auto-recall* on, switching profile recalls
   the slot vnaview saved for it.
5. **Snapshots and export** (collapsed). Save a named snapshot (stored as JSON
   under `<data>/snapshots/`) and overlay it as a dashed reference trace.
   Export the last sweep or any snapshot as Touchstone `.s1p` or CSV, or as
   `.s2p` when S21 was measured.
6. **Advanced sweep** (collapsed) has manual start, stop and points (requests
   over the device limit are split into several scans), an optional S21
   2-port measurement, and software port extension (cable length and VF) to
   de-embed a cable after calibration. A custom range calibrated and saved
   in slot 2 can be restored with *Load saved custom range*, which runs
   `profile custom` on PiX firmware or `recall 2` on stock firmware.

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
| `GET/POST /api/snapshots`, `GET/DELETE /api/snapshots/{name}` | snapshots |
| `GET /api/cal`, `POST /api/cal/step/{reset,open,short,load,isoln,thru,done,on,off}`, `POST /api/cal/save {"slot"}`, `POST /api/cal/recall {"slot"}` | calibration |
| `PUT /api/profiles/{id}/slot {"slot"}`, `POST /api/settings {"autoRecall"}` | settings |

Example: `curl -s -XPOST localhost:8080/api/sweep | jq .summary.bands`

## Layout

```
main.go                      flags, device auto-detect, wiring
web/                         embedded UI (index.html, app.js, style.css; vanilla JS + canvas)
internal/rf/                 RF math: SWR, RL, Z, port extension, bandwidth, dips,
                             band verdicts, Touchstone/CSV
internal/vna/                Device interface (+ optional Profiler)
internal/vna/nanovnah/       NanoVNA-H/H4 shell driver: shell I/O, parser, driver
internal/vna/mock/           byte-level shell simulator + multi-mode antenna model
internal/serialport/         raw termios port, sysfs VID:PID detection
internal/server/             HTTP API, SSE hub, sweep loop, profiles, snapshots/settings store
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
