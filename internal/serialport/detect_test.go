package serialport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSysfs builds /sys/class/tty/ttyACMn -> device symlinks the way Linux does:
// .../class/tty/ttyACM0/device -> ../../../devices/usb1/1-2/1-2:1.0
func fakeSysfs(t *testing.T, devs map[string][2]string) string {
	t.Helper()
	root := t.TempDir()
	for name, id := range devs {
		usb := filepath.Join(root, "devices", "usb1", name+"-usb")
		iface := filepath.Join(usb, name+":1.0")
		if err := os.MkdirAll(iface, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(usb, "idVendor"), []byte(id[0]+"\n"), 0o644)
		os.WriteFile(filepath.Join(usb, "idProduct"), []byte(id[1]+"\n"), 0o644)
		os.WriteFile(filepath.Join(usb, "product"), []byte("NanoVNA-H4\n"), 0o644)
		tty := filepath.Join(root, "class", "tty", name)
		if err := os.MkdirAll(tty, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(iface, filepath.Join(tty, "device")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestFindByUSBID(t *testing.T) {
	SysfsRoot = fakeSysfs(t, map[string][2]string{
		"ttyACM0": {"2341", "0043"}, // an Arduino
		"ttyACM1": {"0483", "5740"}, // NanoVNA-H4
	})
	DevRoot = "/dev"
	t.Cleanup(func() { SysfsRoot = "/sys" })

	all := List()
	if len(all) != 2 || all[0].Path != "/dev/ttyACM0" || all[0].ID.String() != "2341:0043" {
		t.Fatalf("%+v", all)
	}
	c, ok := Find(IDNanoVNAH)
	if !ok || c.Path != "/dev/ttyACM1" || c.Product != "nanovna-h4" {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := Find(IDNanoVNAV2); ok {
		t.Error("found a V2 that is not there")
	}
}

func TestListEmpty(t *testing.T) {
	SysfsRoot = t.TempDir()
	t.Cleanup(func() { SysfsRoot = "/sys" })
	if l := List(); len(l) != 0 {
		t.Fatalf("%+v", l)
	}
}

func TestHolders(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "ttyACM1")
	os.WriteFile(dev, nil, 0o644)
	link := filepath.Join(root, "by-id-NanoVNA")
	os.Symlink(dev, link)
	mk := func(pid, comm, target string) {
		os.MkdirAll(filepath.Join(root, "proc", pid, "fd"), 0o755)
		os.WriteFile(filepath.Join(root, "proc", pid, "comm"), []byte(comm+"\n"), 0o644)
		os.Symlink(target, filepath.Join(root, "proc", pid, "fd", "3"))
	}
	mk("4242", "minicom", dev)
	mk("4343", "bash", "/dev/pts/1")
	ProcRoot = filepath.Join(root, "proc")
	t.Cleanup(func() { ProcRoot = "/proc" })
	h := Holders(link)
	if len(h) != 1 || h[0].PID != 4242 || h[0].Comm != "minicom" {
		t.Fatalf("%+v", h)
	}
	err := &BusyError{Path: link, Holders: h}
	if !strings.Contains(err.Error(), "minicom (pid 4242)") {
		t.Error(err)
	}
}
