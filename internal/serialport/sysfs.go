package serialport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SysfsRoot can be overridden by tests.
var SysfsRoot = "/sys"

// DevRoot can be overridden by tests.
var DevRoot = "/dev"

// ListSysfs returns all ttyACM* devices with their USB IDs from Linux
// sysfs, sorted by path. It is plain file I/O, so it is testable anywhere.
func ListSysfs() []Candidate {
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

func readAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}
