package serialport

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RegistryView is the read-only slice of the Windows registry (HKLM) that
// port detection needs. It is an interface so the logic is testable on any OS.
type RegistryView interface {
	// SubKeys lists the subkey names of path.
	SubKeys(path string) ([]string, error)
	// String reads a REG_SZ value.
	String(path, name string) (string, error)
	// Values reads all REG_SZ values of path (name -> data).
	Values(path string) (map[string]string, error)
}

const (
	regUSB        = `SYSTEM\CurrentControlSet\Enum\USB`
	regSerialComm = `HARDWARE\DEVICEMAP\SERIALCOMM`
)

var reVIDPID = regexp.MustCompile(`(?i)^VID_([0-9A-F]{4})&PID_([0-9A-F]{4})(&MI_[0-9A-F]{2})?$`)

// ListWindows finds USB serial ports in the registry:
// HKLM\SYSTEM\CurrentControlSet\Enum\USB\VID_xxxx&PID_yyyy[&MI_zz]\<instance>\Device Parameters\PortName.
// Windows keeps those keys for devices that were unplugged, so only ports
// listed under HKLM\HARDWARE\DEVICEMAP\SERIALCOMM (present right now) are
// returned when that key is readable.
func ListWindows(r RegistryView) []Candidate {
	present := map[string]bool{}
	if vals, err := r.Values(regSerialComm); err == nil {
		for _, port := range vals {
			present[strings.ToUpper(port)] = true
		}
	}
	devs, _ := r.SubKeys(regUSB)
	var out []Candidate
	seen := map[string]bool{}
	for _, dev := range devs {
		m := reVIDPID.FindStringSubmatch(dev)
		if m == nil {
			continue
		}
		id := USBID{strings.ToLower(m[1]), strings.ToLower(m[2])}
		insts, _ := r.SubKeys(regUSB + `\` + dev)
		for _, inst := range insts {
			base := regUSB + `\` + dev + `\` + inst
			port, err := r.String(base+`\Device Parameters`, "PortName")
			if err != nil || port == "" {
				continue
			}
			if len(present) > 0 && !present[strings.ToUpper(port)] {
				continue // phantom: device not plugged in
			}
			if seen[strings.ToUpper(port)] {
				continue
			}
			seen[strings.ToUpper(port)] = true
			name, _ := r.String(base, "FriendlyName")
			out = append(out, Candidate{Path: strings.ToUpper(port), ID: id, Product: name, Serial: inst})
		}
	}
	sort.Slice(out, func(i, j int) bool { return comNumber(out[i].Path) < comNumber(out[j].Path) })
	return out
}

func comNumber(p string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(p), "COM"))
	if err != nil {
		return 1 << 30
	}
	return n
}

var reCOM = regexp.MustCompile(`(?i)^COM[0-9]+$`)

// WindowsDevicePath turns "COM5" into `\\.\COM5` (required for COM10 and up,
// harmless below); other paths are returned unchanged.
func WindowsDevicePath(p string) string {
	if reCOM.MatchString(p) {
		return `\\.\` + strings.ToUpper(p)
	}
	return p
}
