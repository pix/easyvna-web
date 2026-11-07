// Package serialport opens USB CDC serial ports in raw mode and finds
// NanoVNA devices by USB VID:PID via sysfs (Linux).
package serialport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// USBID is a USB vendor/product pair (lower-case hex, as in sysfs).
type USBID struct{ VID, PID string }

func (u USBID) String() string { return u.VID + ":" + u.PID }

// Known device IDs.
var (
	// NanoVNA-H, -H4 and NanoVNA-D firmware enumerate as the STM32 virtual COM port.
	IDNanoVNAH = USBID{"0483", "5740"}
	// NanoVNA V2 / V2Plus4 (binary protocol; no driver yet).
	IDNanoVNAV2 = USBID{"04b4", "0008"}
)

// Candidate is a serial device with its USB identity.
type Candidate struct {
	Path    string // /dev/ttyACM0
	ID      USBID
	Product string // sysfs "product" string, if any
	Serial  string
}

// SysfsRoot can be overridden by tests.
var SysfsRoot = "/sys"

// DevRoot can be overridden by tests.
var DevRoot = "/dev"

// List returns all ttyACM* devices with their USB IDs, sorted by path.
func List() []Candidate {
	matches, _ := filepath.Glob(filepath.Join(SysfsRoot, "class", "tty", "ttyACM*"))
	var out []Candidate
	for _, m := range matches {
		name := filepath.Base(m)
		c := Candidate{Path: filepath.Join(DevRoot, name)}
		// .../ttyACM0/device -> USB interface dir; its parent is the USB device.
		ifDir, err := filepath.EvalSymlinks(filepath.Join(m, "device"))
		if err == nil {
			usb := filepath.Dir(ifDir)
			c.ID = USBID{readAttr(usb, "idVendor"), readAttr(usb, "idProduct")}
			c.Product = readAttr(usb, "product")
			c.Serial = readAttr(usb, "serial")
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
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

func readAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}
