// Package serialport opens USB CDC serial ports in raw mode and finds
// NanoVNA devices by USB VID:PID: via sysfs on Linux, via the registry on
// Windows, by /dev/cu.usbmodem* name on macOS (no VID:PID without cgo).
package serialport

import (
	"strconv"
	"strings"
)

// USBID is a USB vendor/product pair (lower-case hex, as in sysfs).
type USBID struct{ VID, PID string }

func (u USBID) String() string { return u.VID + ":" + u.PID }

// Known device IDs.
var (
	// NanoVNA-H, -H4 and NanoVNA-D firmware enumerate as the STM32 virtual COM port.
	IDNanoVNAH = USBID{"0483", "5740"}
	// NanoVNA V2 / V2 Plus / V2 Plus4 and LiteVNA (binary protocol,
	// experimental driver internal/vna/nanovnav2).
	IDNanoVNAV2 = USBID{"04b4", "0008"}
)

// Candidate is a serial device with its USB identity.
type Candidate struct {
	Path    string // /dev/ttyACM0, COM5, /dev/cu.usbmodem401
	ID      USBID
	Product string // product / friendly name, if known
	Serial  string
}

// Find returns the first candidate matching one of ids.
func Find(ids ...USBID) (Candidate, bool) {
	for _, c := range List() {
		for _, id := range ids {
			if c.ID == id {
				return c, true
			}
		}
	}
	return Candidate{}, false
}

// BusyError means another program has the port open; EasyVNA Web backs off
// instead of fighting over it.
type BusyError struct {
	Path    string
	Holders []Holder // known holders (Linux only)
}

func (e *BusyError) Error() string {
	if len(e.Holders) == 0 {
		return e.Path + " is busy: another program (NanoVNA-Saver, a terminal, ...) has it open. Close it, then press Reconnect"
	}
	var b strings.Builder
	for i, h := range e.Holders {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(h.Comm + " (pid " + strconv.Itoa(h.PID) + ")")
	}
	return e.Path + " is in use by " + b.String() + "; close it there, then press Reconnect"
}
