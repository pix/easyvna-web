# EasyVNA Web

*by PiX (and the Claude, OpenAI and Deepseek AI band)*

**Plug in a NanoVNA, tap a LoRa band, and see right away whether your antenna is good.** It also helps you trim it, test it and print a report.

**Try it in your browser: <https://pix.github.io/easyvna-web/>** (simulator, no
NanoVNA needed: <https://pix.github.io/easyvna-web/?demo>)

> [!WARNING]
> **Developed with Claude (Anthropic's AI assistant, via Claude Code) under
> the direction of PiX.** It is largely AI-generated. It has been tested in
> simulation and on one NanoVNA-H4 under Linux. The Windows build is
> untested on real hardware unless stated. The browser version (Web Serial)
> has been tested with the simulator and a simulated serial port only. The
> firmware flasher (**Firmware**) has been tested against a simulated STM32
> DFU bootloader only, not on a real NanoVNA yet. Use at your own risk.

> [!CAUTION]
> **NanoVNA V2 / LiteVNA support is EXPERIMENTAL.** Its driver was written
> from the published description of the V2 USB protocol and has only been
> tested against a simulated V2: no real V2, V2 Plus4 or LiteVNA has been
> tried yet.

![EasyVNA Web: the 869 MHz profile with its verdict, a REF overlay and the SWR plot](doc/screenshots/hero.png)

## Why EasyVNA Web

- **One-click LoRa band profiles.** Tap *433*, *869* or *915* for a sweep
  around the band. *Wide* and *Survey* identify an unknown antenna.
- **Verdict at a glance.** Each band gets PASS/OK/FAIL from its worst in-band
  SWR. You also see the resonance and its offset, mismatch loss, return loss
  and Q.
- **TUNE with a trim calculator.** A big live readout says SHORTEN or
  LENGTHEN. The calculator learns your antenna's MHz/mm from each cut, and an
  optional tone lets you trim without looking.
- **HOLD stable positions.** Bend the antenna, hold it, repeat. Only steady
  positions count, so you see how robust the match really is.
- **S21 COMPARE.** Rank antennas against a reference antenna, with no THRU
  calibration needed.
- **Report card.** One button produces a clean A4 or Letter PDF with the verdicts,
  plots, trim log and comparison.
- **Antenna history.** Every sheet and report (or **Log this
  measurement**) keeps a summary per antenna, and EasyVNA Web warns when an
  antenna drifts: resonance moved, SWR worse, or signs of loss such as water
  in the coax.
- **BATCH: test a whole lot.** Measure the antennas of a lot one after
  the other (by hand, with a key, or captured by themselves as you swap
  them). Each one gets a clear *tuned / shorten / lengthen / bad* status
  with the trim to make; the lot gets its mean offset (is the pattern
  length off?), its spread and its outliers, on screen and on a one-page
  batch sheet.
- **Open measurement files.** Load a `.s1p`, `.s2p` or CSV from any VNA
  program and get the same verdict, plots, report and sheet, clearly
  marked *not live*. No NanoVNA needed, so it works in every browser.
- **Firmware update in the browser.** **Firmware** backs up the whole
  flash and installs the bundled EasyVNA firmware (or any `.bin` / `.hex` /
  `.dfu`) over USB, keeping your calibration.
- **Works on a phone** at the bench, in light or dark mode.
- **Runs as a single `.exe` on Windows.** Double-click it and the browser
  opens. Nothing to install, no internet needed.

## Use it in your browser (no install)

Open **<https://pix.github.io/easyvna-web/>** in **Chrome or Edge on a
computer** (Windows, macOS, Linux, ChromeOS), plug in the NanoVNA and press
**Connect**. The browser asks which serial port to use: pick the NanoVNA
(USB `0483:5740`, or `04b4:0008` for a NanoVNA V2 / LiteVNA, experimental).
Next time it reconnects by itself.

* It is the same program, compiled to WebAssembly and packed into **one HTML
  file**. Nothing is installed and nothing is sent anywhere: the page talks
  to the NanoVNA over USB with Web Serial, and your settings, REF traces, trim
  logs, reports and snapshots stay **in this browser** (IndexedDB). Use
  **Export data** / **Import data** in the bar under the title to back them
  up or move them to another computer.
* **No NanoVNA?** <https://pix.github.io/easyvna-web/?demo> runs the built-in
  simulator. It works in every browser, phones included.
* **Firefox, Safari and phones** have no Web Serial, so they cannot reach the
  NanoVNA: USB needs Chrome or Edge on a computer; the simulator works
  everywhere. **Open a measurement file** (next to that notice) works in
  every browser too: load a `.s1p`, `.s2p` or CSV saved by another program
  and get the full analysis, report and sheet.
* **Offline:** **Download for offline use** saves the whole app as
  `easyvna-web.html`. Double-click it later, even without internet; USB works
  from the file too. Each release also has `easyvna-web.html` attached.
* Reports open in a new tab (allow pop-ups for the page); **Download HTML**
  in the report keeps a copy.

## Quick start

### Windows

1. Download `easyvna-web-<version>-windows-amd64.zip` from
   [Releases](https://github.com/pix/easyvna-web/releases). Pick `arm64` for
   Windows on ARM.
2. Unzip it and **double-click `easyvna-web.exe`**. A console window says
   *"EasyVNA Web running at http://127.0.0.1:8080. Close this window to
   quit"*, and your browser opens on that page.
3. Plug in the NanoVNA-H4 by USB, switch it on, and press **Reconnect** if
   it was not found at start. Windows 10/11 needs no driver: the NanoVNA is a
   COM port with USB ID `0483:5740` and is found automatically.

> [!NOTE]
> The `.exe` is not code-signed, so Windows SmartScreen may say *"Windows
> protected your PC"* the first time. Click **More info → Run anyway**.

Your settings, REF traces, trim logs and reports are kept in
`%LOCALAPPDATA%\easyvna-web`.

### macOS (Apple silicon)

```sh
tar xzf easyvna-web-<version>-darwin-arm64.tar.gz
./easyvna-web-<version>-darwin-arm64/easyvna-web
```

The macOS build is provided but untested. The NanoVNA is picked
automatically when it is the only `/dev/cu.usbmodem*` device; otherwise use
`-port /dev/cu.usbmodemXXXX`.

### Linux and other systems: build from source

With [Go](https://go.dev/dl/) 1.27 or newer:

```sh
go install github.com/pix/easyvna-web@latest
easyvna-web
```

On Linux your user needs access to the serial port: see
[USB drivers and permissions](#usb-drivers-and-permissions). Data is kept in
`~/.local/share/easyvna-web`.

### No NanoVNA at hand?

Run `easyvna-web -mock`. It simulates a NanoVNA-H4 and a test antenna, so
you can try every feature. All screenshots on this page were taken that
way.

## USB drivers and permissions

The NanoVNA shows up in two ways: **normal mode** is a USB serial port
(`0483:5740`; V2/LiteVNA `04b4:0008`), used by the program and by the
browser's Web Serial; **DFU mode** is the STM32 bootloader (`0483:df11`,
*STM32 BOOTLOADER*), used only for a firmware update (WebUSB). The FIRMWARE
dialog has the same help, with a tab per system.

### Linux

Install this udev rule once. It gives the logged-in user access to both
modes (`uaccess`) and keeps ModemManager off the serial port:

```sh
sudo tee /etc/udev/rules.d/70-nanovna.rules >/dev/null <<'EOF'
# NanoVNA-H / H4 in normal mode: USB serial (Web Serial, the EasyVNA Web program)
SUBSYSTEM=="tty", ATTRS{idVendor}=="0483", ATTRS{idProduct}=="5740", TAG+="uaccess", ENV{ID_MM_DEVICE_IGNORE}="1"
# NanoVNA V2 / LiteVNA: USB serial
SUBSYSTEM=="tty", ATTRS{idVendor}=="04b4", ATTRS{idProduct}=="0008", TAG+="uaccess", ENV{ID_MM_DEVICE_IGNORE}="1"
# NanoVNA in DFU mode, "STM32 BOOTLOADER" (WebUSB firmware update)
SUBSYSTEM=="usb", ATTRS{idVendor}=="0483", ATTRS{idProduct}=="df11", TAG+="uaccess"
EOF
sudo udevadm control --reload-rules && sudo udevadm trigger
```

Then unplug the NanoVNA and plug it in again. Keep the `70-` prefix: the
file must sort before `73-seat-late.rules` for `uaccess` to apply.
`lsusb -d 0483:` shows which mode the NanoVNA is in.

For the serial port alone, membership of the serial group also works
(`sudo usermod -aG uucp "$USER"` on Arch, `dialout` on Debian, Ubuntu,
Fedora, openSUSE; log out and in again), but it does not cover DFU mode.
Chromium installed as a Snap also needs `sudo snap connect chromium:raw-usb`
for the firmware update.

### macOS and ChromeOS

Nothing to install for either mode. If the NanoVNA is not listed, check
that the system sees it (macOS: *System Report → USB*, or
`system_profiler SPUSBDataType`), and try a data cable (some only charge),
another port, or no hub. On ChromeOS, connect a new USB device to Chrome,
not to Linux.

### Windows

**Normal mode:** nothing to install. Windows 10 and 11 use their built-in
driver; the NanoVNA is a *USB Serial Device (COMx)* under *Device Manager →
Ports*. Do not put WinUSB on it.

**DFU mode:** a browser can only open a USB device that uses the WinUSB
driver, and Windows installs none for the STM32 bootloader (or ST's own
driver, if STM32CubeProgrammer is installed). Once per PC:

1. Put the NanoVNA in DFU mode and plug it in.
2. Run [Zadig](https://zadig.akeo.ie/), then *Options → List All Devices*.
3. Pick **STM32 BOOTLOADER** and check that *USB ID* reads `0483 DF11`. If
   it shows anything else, stop: it is the wrong device.
4. Choose **WinUSB** on the right of the green arrow, then **Replace
   Driver** (or **Install Driver**).

`dfu-util` works with WinUSB too. STM32CubeProgrammer needs ST's driver: to
go back, or if WinUSB landed on the wrong device, open *Device Manager*,
*Uninstall device* with *Attempt to remove the driver* ticked, then unplug
and plug in again.

## Workflow

### 1. Pick a band

Tap **433**, **869** or **915**. Each tap runs one sweep around that band
(**LIVE** keeps sweeping). The band is shaded on the plot. To find out what
an unknown antenna is, use **Wide** (300–1000 MHz) or **Survey**
(50–1500 MHz): every resonance is listed, with its nearest LoRa band and a
trim hint.

![Survey: every resonance with its nearest LoRa band](doc/screenshots/survey-identify.png)

### 2. Calibrate if the strip asks for it

The calibration strip is a green one-liner when the active calibration
matches the profile. Otherwise it opens a one-tap
**OPEN → SHORT → LOAD → DONE → SAVE** flow. Fit the standards at the end of
the cable, where the antenna goes. Each profile has its own calibration slot,
so this is a one-time job per band.

### 3. Read the verdict

Each band gets **PASS** (worst in-band SWR < 1.5), **OK** (< 2) or **FAIL**.
Next to the grade you see the resonance and how far it is from the band,
mismatch loss, return loss and the SWR < 2 bandwidth. **REF** stores a trace
as a dashed overlay with Δf / ΔSWR against it, which also makes a quick
detuning test: free air, then in your hand or the enclosure. A **per band**
line says what the change did in every band in view, e.g. *433: +3.1 MHz ·
869: +12.4 MHz, SWR 1.8→1.3* (in Wide and Survey also for every resonance
dip); TUNE, the sheet and the report show it too. Automatic
warnings catch *no antenna*, *bad calibration*, *lossy "fake"* antennas, a
*noisy measurement* (a loose connector or a damaged cable: the trace
jitters, within a sweep or from one sweep to the next) and a *calibration
that does not match this setup* (|Γ| above 1 over part of the sweep).

![A fake (resistive) antenna is flagged and its verdicts are marked CHECK](doc/screenshots/fake-antenna-warning.png)

### 4. TUNE and trim

**TUNE** opens a full-screen live view. It shows the resonance in large
numbers, the offset to the target with **▼ SHORTEN / ▲ LENGTHEN /
✓ ON TARGET**, and SWR at the target. Enter the element length once. After
each cut, enter how much you cut and tap **Measure & log**. The trim
calculator learns this antenna's MHz/mm and tells you the next cut. Until
it has learnt it (before your first logged cut), it only suggests a
cautious **first cut**: 60 % of the estimate, rounded down to 0.5 mm, shown
as *first cut 4.5 mm (full estimate 7.6 mm)*, because a cut wire cannot be
made longer again. Resonance below the target means the element is too
long (cut); above the target it is too short, and it tells you **not** to
cut. Turn on **Tone** to trim by ear.

![TUNE with the trim calculator](doc/screenshots/tune-trim.png)

### 5. Check robustness with HOLD

Turn on **HOLD**, then bend or move the antenna and hold it still in each
position. Sweeps taken while it moves are rejected. Each steady position is
captured, and you get the **resonance spread** and the worst in-band SWR
across all positions.

![HOLD: steady positions only, with the resonance spread](doc/screenshots/hold.png)

### 6. COMPARE antennas (S21)

Connect a fixed reference antenna to port 1 and the antenna under test to
port 2. Store antenna **A**, then swap in the others: you see the live
in-band **Δ dB vs A** and can add each antenna to a leaderboard. No THRU
calibration is needed, because only differences count. If S21 is too weak,
move the two antennas closer together.

![S21 compare leaderboard](doc/screenshots/compare.png)

### 7. Print the report

**REPORT** makes a printable page; use the browser's *Print → Save as PDF*.
It contains:
* a header with the antenna, device, firmware and calibration state;
* PASS/OK/FAIL per band;
* vector SWR, return-loss and Smith plots;
* resonances, the trim log, HOLD positions and S21 results, each only if
  there is data;
* your notes, and *page X of Y* on every page.

Pick **A4** or **Letter** in the report toolbar. The default follows your
browser's language (Letter for the US, Canada, Mexico…) and your last choice
is remembered; `?paper=letter` in the report URL forces it. A typical
antenna fits on two pages. See the A4 sample:
**[doc/sample-report.pdf](doc/sample-report.pdf)**.

<p align="center"><img src="doc/screenshots/report-print.png" alt="First page of the printed report" width="520"></p>

### 8. PRINT an antenna sheet

**PRINT** makes a one-page datasheet for the antenna you just measured.
Give it a name; type, band, connector, length, ground plane, cable, maker,
owner, notes and a photo are optional. They are remembered for that name.
The sheet has the verdict, the key figures (resonance, SWR at the band
centre and edges, worst in-band SWR, bandwidth, return and mismatch loss,
impedance, Q) and the SWR, Smith, return-loss and impedance graphs, with
REF and HOLD when they are on. It fits on one A4 or Letter page.
**Print sheet** prints it; **Save sheet (HTML)** keeps a copy. Ctrl+P on
the main page also prints every graph at once.

### 9. Follow an antenna with HISTORY

Each antenna sheet and each report keeps a short summary of the
measurement under the antenna's name: date, band, resonance, SWR at the
target and worst in band, return loss, R + jX, Q, calibration state and
whether it was live or a file. **HISTORY** shows them with small graphs of
the resonance and the worst SWR, and **Log this measurement** adds one
without printing anything. The latest measurement is compared with the
earlier ones of the same band, and plain warnings say when the antenna
drifted: *Resonance moved* (more than 0.25 %, at least 1 MHz), *SWR got
worse*, or *Signs of rising loss* (a broader dip with a lower Q, or a higher
resistance at resonance: water in the coax, a corroded connector). The
sheet and the report carry a *previous measurement / drift* line. The
history lives in the antenna's record, so **Export data** includes it, and
**Export history (CSV)** gives a spreadsheet.

### 10. Test a lot with BATCH

**BATCH** measures a lot of "identical" antennas (made or bought together,
e.g. 25 LoRa whips) on one band profile and tells you two things: whether
**each antenna is tuned**, and **how consistent the lot is**.

1. Tap the band (**433**, **869** or **915**), calibrate it, then **BATCH**.
   Name the batch (*869 whip lot A*); the element length (for trim advice
   in mm), a trim log whose MHz/mm was learnt in TUNE, and notes are
   optional. The batch keeps that profile: captures of another sweep are
   refused with *this batch uses the 869 MHz profile … tap 869 first*.
2. Connect each antenna and tap **Capture #n** (or press **C**). Each
   capture keeps the whole sweep, the calibration state and the time; its
   label (#1, #2…) can be changed into a serial number. **Redo** measures
   one again, **×** deletes it (with Undo).
3. Or turn on **Auto-capture** (it starts LIVE): unscrew the antenna,
   screw on the next one and hold still. A new antenna is captured once
   the reading has changed (the open cable is seen, or the trace jumped and
   settled somewhere else) and stayed steady for HOLD's number of sweeps.
   Touching the antenna and letting go, or slow drift, does not capture it
   again.

Each antenna gets a status against TUNE's target (the band reference,
e.g. 869.00 MHz) with TUNE's ✓ ON TARGET tolerance (± 0.15 %:
± 1.30 MHz at 869 MHz): **✓ tuned**, **↓ shorten 0.3 mm** (resonates
low: too long), **↑ lengthen 0.5 mm** (resonates high: too short, do not
cut), or **✗ out of band** / **✗ poor match** when the band is not covered
at SWR < 2. The mm come from the trim calculator: the learnt MHz/mm when
a trim log has it, else the first estimate from the element length with
TUNE's cautious first cut, else a percentage of the length. Next to it:
resonance and offset (MHz and %), SWR at the target, worst SWR in the
band, SWR < 2 bandwidth, return loss and the PASS / OK / FAIL band
verdict.

The lot gets n, mean, standard deviation, min, max and spread of each
figure, the yield, and a plain-words conclusion that separates the
**systematic offset** (*the whole lot resonates 3 MHz low: the pattern is
~0.3 mm too long*) from the **unit-to-unit spread** (σ compared with the
tolerance window). **Statistical outliers** (antennas unlike the rest of
the lot, ◆) are flagged with a robust rule that works from 3 antennas on:
|x − median| > 3.5 × 1.4826 × MAD (the modified z-score; the median and
MAD are not pulled by the odd antenna, and a small repeatability floor
keeps identical antennas from being flagged). An outlier is not the same
as off target: an antenna can be both, either or neither. The view shows
all SWR curves with the lot's min/max envelope and the live trace, and a
resonance strip with the target, the tolerance window, the mean ± 1σ and
one mark per antenna.

**Batch sheet** (or Ctrl+P in BATCH) prints one A4 or Letter page: the
conclusion, the SWR overlay, the resonance strip, the statistics and every
antenna (up to 40 on one page; from 15 on, in two compact columns). **CSV**
gives one row per antenna plus the statistics, **JSON** the whole batch
with every sweep, which **Import a batch** reads back. The REPORT can
include a batch's statistics. Batches are kept with your data
(`batches/`, included in *Export data*). In the simulator
(`?demo&antenna=lot869`, or `-mock -mock-antenna lot869`) **Next
simulated antenna** connects the next unit of a simulated lot.

### 11. Open a measurement file

**Open file** (top right) loads a measurement saved by EasyVNA Web or by
another VNA program: Touchstone `.s1p`, `.s2p` (S11, plus S21 when it is
there) or a CSV export (NanoVNA-Saver / NanoVNA-App style: frequency with
S11 as real/imaginary, dB or magnitude with phase, or R and X). Files made
for another reference impedance (75 Ω…) are converted to 50 Ω; files that
cannot be read are refused with the reason. The file then gets the usual
verdict, plots, REF overlay, report and antenna sheet, under a blue
**FILE · NOT LIVE** bar with its name. Tap **REF** to use the file as the
reference for live sweeps (last month's measurement against today's), and
**Back to live** to sweep again. **Snapshots & export** saves the current
sweep as `.s1p`, `.s2p` (when S21 was measured) or CSV.

### 12. Back up and update the firmware

**Firmware** (top right) backs up and updates the NanoVNA's firmware over
USB, in Chrome or Edge on a computer (WebUSB), in the program and in the
browser version alike. *Tested against a simulated DFU device only.*

1. **Put the NanoVNA in DFU mode.** H4 with stock firmware: power it off,
   hold the jog wheel pushed in and switch it on (the screen stays blank).
   EasyVNA firmware: `reset dfu` in the USB serial shell, CONFIG > DFU, or
   **Reboot into DFU** in the dialog while EasyVNA Web is connected to it.
   Then press **Connect (DFU)** and pick *STM32 BOOTLOADER* (`0483:df11`).
2. **Back up the flash** saves all of it (firmware, calibration,
   configuration) as `nanovna-backup-<model>-<date>.bin`; the last four
   backups are also kept in the browser.
3. **Flash** writes the bundled EasyVNA firmware, a `.bin`, `.hex` or
   `.dfu` file, or restores a full backup. It is checked first (vector
   table, size, H4 image on an H4 only), and a confirmation lists the
   device, the image and the pages to erase. Before the first write to a
   device, a full backup is read and downloaded by itself. Only the 2 KB
   pages the image covers are erased, one by one; an image that would reach
   the calibration and configuration area is refused, and only **Restore a
   full backup** rewrites that area, after an explicit confirmation.
   Everything written is read back and compared. **Start firmware** leaves
   DFU mode.
   **Flash with dfu-util instead** gives the same steps for a terminal and
   a **Download** button for the bundled firmware (also in Firefox and
   Safari, which have no WebUSB).

If it is interrupted, nothing is lost for good: the DFU bootloader is in
ROM, so the NanoVNA can always be put back in DFU mode and flashed again.
A read-protected NanoVNA is refused: removing the protection erases the
whole flash, calibration included. Linux needs a udev rule and Windows the
WinUSB driver for DFU mode; macOS needs nothing: see
[USB drivers and permissions](#usb-drivers-and-permissions).

### On a phone

<p align="center">
<img src="doc/screenshots/phone.png" alt="Main page on a phone" width="260">
&nbsp;&nbsp;
<img src="doc/screenshots/phone-tune.png" alt="TUNE on a phone" width="260">
</p>

## Features

| | |
|---|---|
| **Profiles** | Wide 300–1000, Survey 50–1500, 433, 869 (plus the 869.4–869.65 sub-band), 915 MHz, and a custom range |
| **Verdict** | PASS / OK / FAIL per band, with worst/best SWR, return loss, mismatch loss, resonance offset, SWR < 2 bandwidth and Q. Band edges are interpolated |
| **Identify** | every resonance with SWR, R+jX, bandwidth and Q, the nearest LoRa band, and higher-order modes flagged |
| **Calibration** | one-tap OPEN/SHORT/LOAD/DONE/SAVE per profile, with slot tracking, auto-recall, and a warning when the calibration does not fit the sweep |
| **TUNE** | live narrow sweep, SHORTEN/LENGTHEN, SWR at the target, optional audio tone |
| **Trim calculator** | learns MHz/mm from each cut, cautious first cut (60 %) until it has, "do not cut" when too short, per-antenna logs with undo; the next cut is on the report and the sheet |
| **REF** | one-tap reference trace, Δf / ΔSWR, detuning test, per-band effect of a change (resonance shift and SWR before → after for 433 / 869 / 915 and each dip of a Wide / Survey sweep) in the main view, TUNE, sheet and report |
| **HOLD** | stable-positions envelope with motion rejection and resonance spread |
| **AVG** | averaging over ×1 / 2 / 4 / 8 / 16 sweeps |
| **COMPARE** | S21 against antenna A, live Δ dB in band, leaderboard |
| **Warnings** | no antenna or open cable, reflection above 1 (bad calibration, also over part of the sweep), lossy / fake antenna, noisy measurement (point-to-point jitter and sweep-to-sweep repeatability, 4× real H4 noise; moving the antenna does not count) |
| **History** | per-antenna measurement log (sheet, report, *Log this measurement*), resonance and SWR sparklines, drift warnings (resonance moved, SWR worse, rising loss), previous-measurement line on the sheet and report, CSV export |
| **Batch** | a lot of antennas on one profile: capture by hand, by key or automatically; per-antenna tuned / shorten / lengthen / bad with the trim in mm; mean offset, spread, outliers (median / MAD), yield; SWR overlay and resonance strip; one-page batch sheet; CSV and JSON export, JSON import |
| **Report** | printable A4 / Letter report with vector plots (SWR, return loss, impedance, Smith) and tables, saved in the data folder |
| **Antenna sheet** | PRINT: a one-page datasheet of the antenna (photo, key figures, all four graphs, notes, trim log); its details are remembered per antenna name |
| **Files** | open `.s1p` / `.s2p` / CSV (Touchstone 1 and 2.0, any S format and unit, other reference impedances renormalised to 50 Ω) as a labelled *not live* measurement with the full analysis; export Touchstone `.s1p` / `.s2p`, CSV, named snapshots |
| **Plots** | SWR, return loss, Smith chart, R/X, S21, with a cursor readout |
| **NanoVNA V2 / LiteVNA** | EXPERIMENTAL, untested on hardware: binary protocol driver (USB `04b4:0008`), found automatically (also by Web Serial); the V2 sends raw data, so EasyVNA Web calibrates it with the same OPEN / SHORT / LOAD / DONE / SAVE flow (THRU / ISOLN optional for S21) and keeps each slot in its data folder |
| **Firmware** | back up and flash over USB (WebUSB, STM32 DFU bootloader): bundled EasyVNA firmware, `.bin` / `.hex` / `.dfu`, or a full backup; automatic backup before writing, page-by-page erase, calibration area kept, read-back verify, *Reboot into DFU* over the serial connection |
| **Anywhere** | phone layout, light and dark mode, works offline |

## Options

| flag | default | meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8080` | HTTP address. If 8080 is taken and `-listen` was not given, the next free port is used |
| `-no-browser` | off | do not open the browser at start-up |
| `-port` | auto | serial port, e.g. `COM5`, `/dev/ttyACM0`, `/dev/serial/by-id/usb-nanovna.com_NanoVNA-H4_400-if00`. Auto-detect looks for USB `0483:5740` (NanoVNA-H) and `04b4:0008` (NanoVNA V2 / LiteVNA) |
| `-driver` | `auto` | `nanovna-h` (text shell) or `nanovna-v2` (binary protocol, EXPERIMENTAL); `auto` chooses by USB ID |
| `-readonly` | off | never change anything stored on the device: calibration, save, recall and on-device profile switching are refused. Sweeps still work |
| `-data` | per OS | folder for settings, REF traces, trim logs, compare session, reports and snapshots |
| `-mock` | off | simulate a NanoVNA-H4 (no hardware needed) |
| `-mock-antenna` | `dual` | simulated antenna: `dual`, `869`, `869tuned`, `915`, `dummy`, `open`, `bend`, and `lot433`, `lot869`, `lot915` (each selection connects the next unit of a simulated lot) |
| `-mock-firmware` | `custom` | `custom` simulates the EasyVNA firmware, `stock` plain NanoVNA-D, `v2` a NanoVNA V2 (binary protocol, raw data, host calibration) |
| `-maxpoints` | auto | points per device scan (401 on H4, 101 on H) |
| `-cal-settle` | `1.5s` | time with a standard attached before `cal <std>` (older firmware) |
| `-trace` | off | log every exchange with the device |
| `-version` | | print the version and exit |

## Firmware

EasyVNA Web works with **stock NanoVNA-H / H4 firmware** (NanoVNA-D by
DiSlord and compatible). It works best with the **EasyVNA firmware**
(<https://github.com/pix/easyvna>), which adds the same LoRa profiles on
the device itself, so the device and the browser stay in sync. EasyVNA Web
recognises it automatically, and the device badge then shows **EasyVNA**.

Both firmwares use the same calibration slots, so a calibration saved from
either side is found by the other:

| profile | range (MHz) | points | slot |
|---|---|---|---|
| survey | 50–1500 | 401 | 1 |
| wide | 300–1000 | 401 | 3 |
| 433 | 388.92–478.92 | 401 | 4 |
| 869 | 779–959 | 401 | 5 |
| 915 | 825–1005 | 401 | 6 |
| custom | as saved | as saved | 2 |

**Bundled firmware.** EasyVNA Web carries a copy of the latest EasyVNA
firmware release for the NanoVNA-H4 (currently `EasyVNA-H4-v0.1.1.bin`, in
`web/static/firmware/`), so **Firmware** can install it without a download.
It is the unmodified release binary, checked against the release's
SHA-256 at build time and again before flashing. The EasyVNA firmware is
free software under the GPL-3.0; its source code is at
<https://github.com/pix/easyvna> (the dialog links the exact release tag).
`scripts/update-firmware.sh` fetches the newest release, and the Pages and
release builds run it before building.

Slot 0 is left to you; stock firmware loads it at power-on. Offsets are
measured from 433.92, 869 and 915 MHz. With the EasyVNA firmware, tapping a
profile also makes the device remember it as the last-used profile, which
is a small flash write. `-readonly` avoids that.

### NanoVNA V2 / LiteVNA (EXPERIMENTAL)

A NanoVNA V2 (V2, V2 Plus, V2 Plus4) or a LiteVNA, found as USB
`04b4:0008`, is driven with its binary protocol. **This has not been tested
on real hardware.** These devices send raw, uncalibrated data and keep no
calibration of their own for the computer, so EasyVNA Web calibrates them
itself: the calibration strip works as for the H4 (OPEN → SHORT → LOAD →
DONE → SAVE), the error terms are solved per frequency point, interpolated
for TUNE's narrow windows, and SAVE keeps them per profile in the data
folder (`hostcal/slot-N.json`, also in *Export data*). They are recalled
automatically per profile and when the device reconnects. Try it without
hardware with `-mock-firmware v2` or `?demo&firmware=v2`. Windows may need
the USB serial driver from the V2 maker.

## FAQ and troubleshooting

**"COM5 is busy" / "in use by …"**
Another program has the port open: NanoVNA-Saver, a serial terminal, or a
second copy of EasyVNA Web. Close it, then press **Reconnect**.

**"No NanoVNA found"**
Check that the NanoVNA is switched on and connected with a *data* USB cable
(some cables only charge). On Windows, look under *Device Manager → Ports*
for a "USB Serial Device (COMx)"; if it shows another ID, start with
`-port COMx`. On Linux, check the serial-port permissions (see
[USB drivers and permissions](#usb-drivers-and-permissions)).

**Windows says "Windows protected your PC"**
The program is not code-signed. Click *More info → Run anyway*.

**The calibration strip stays orange**
The active calibration was made for a different frequency range. Calibrate
this profile once with **OPEN → SHORT → LOAD → DONE → SAVE**; after that it
is recalled automatically.

**"Reflection above 1" warning**
The calibration does not match the current setup: a different cable or
adapter, or a stale calibration. Recalibrate at the end of the cable you
measure with.

**"Noisy measurement: check the connector and cable, then recalibrate"**
The trace jitters much more than a NanoVNA normally does (about 0.0016
RMS |ΔΓ|). Tighten the SMA connectors, try another cable or adapter, keep
the cable still, then recalibrate. Bending or moving the antenna does not
trigger it: only random jitter does.

**S21 COMPARE reading is very low or noisy**
Move the reference antenna and the antenna under test closer together (a
few tens of cm), keep the cables still, and use the same placement for
every antenna.

**The browser did not open**
Open the address shown in the console window (normally
http://127.0.0.1:8080) yourself, or use `-no-browser` and bookmark it.

**The browser version does not see the NanoVNA**
Use Chrome or Edge on a computer, press **Connect** and pick the NanoVNA in
the list. If opening fails, another program or tab has the port (close
NanoVNA-Saver, a serial terminal or a second EasyVNA Web tab). On Linux your
user needs access to the serial port, as for the program (see
[USB drivers and permissions](#usb-drivers-and-permissions)).

**Firmware: Connect (DFU) finds nothing, or the device cannot be opened**
Check that the NanoVNA is in DFU mode (blank screen; USB `0483:df11`,
*STM32 BOOTLOADER*). See
[USB drivers and permissions](#usb-drivers-and-permissions) (Linux udev
rule, Windows WinUSB); close STM32CubeProgrammer or dfu-util. Firefox and Safari have no WebUSB.

**"Cannot open this file"**
EasyVNA Web reads S-parameter files only: Touchstone with one or two
ports, or a CSV that has a frequency column and S11 with its phase (real
and imaginary, dB or magnitude with phase, or R and X). SWR or return loss
alone has no phase, so such a CSV is refused. The message names the line
or column it did not understand.

**Where is my data?**
In the browser version, in the browser itself (use **Export data** for a
copy). Otherwise in `%LOCALAPPDATA%\easyvna-web` on Windows, `~/.local/share/easyvna-web` on
Linux, and `~/Library/Application Support/easyvna-web` on macOS. Use `-data`
to choose another folder.

## Development

```sh
go build -o easyvna-web .
go test -race ./... && go vet ./...
scripts/test-js.sh            # firmware flasher unit tests (node)
./easyvna-web -mock
```

Build commands, release and screenshot scripts, the HTTP API, code layout,
NanoVNA shell protocol notes and the (experimental) NanoVNA V2 driver are in
**[doc/DEVELOPMENT.md](doc/DEVELOPMENT.md)**.

## Credits

EasyVNA Web is by PiX (and the Claude, OpenAI and Deepseek AI band).

## License

BSD-3-Clause, see [LICENSE](LICENSE). Copyright (c) 2026, Camille Moncelier
(PiX).

EasyVNA Web is a separate program that talks to the NanoVNA over USB. The
EasyVNA firmware is based on NanoVNA-D and keeps that project's GPL-3.0
license. EasyVNA Web bundles an unmodified binary of it
(`web/static/firmware/`, also inside the program and the HTML page) only
to install it; that binary is distributed under the GPL-3.0, with its
source code at <https://github.com/pix/easyvna>.
