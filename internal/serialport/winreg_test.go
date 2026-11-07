package serialport

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeReg is an in-memory HKLM: keys map to subkeys and values.
type fakeReg struct {
	keys   map[string][]string
	values map[string]map[string]string
}

func (f fakeReg) SubKeys(p string) ([]string, error) {
	if k, ok := f.keys[strings.ToLower(p)]; ok {
		return k, nil
	}
	return nil, errors.New("not found")
}

func (f fakeReg) String(p, name string) (string, error) {
	if v, ok := f.values[strings.ToLower(p)][name]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func (f fakeReg) Values(p string) (map[string]string, error) {
	if v, ok := f.values[strings.ToLower(p)]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}

func newFakeReg() fakeReg {
	usb := strings.ToLower(regUSB)
	f := fakeReg{keys: map[string][]string{}, values: map[string]map[string]string{}}
	add := func(dev, inst, port, friendly string) {
		if !slices.Contains(f.keys[usb], dev) {
			f.keys[usb] = append(f.keys[usb], dev)
		}
		k := usb + `\` + strings.ToLower(dev)
		f.keys[k] = append(f.keys[k], inst)
		f.values[k+`\`+strings.ToLower(inst)] = map[string]string{"FriendlyName": friendly}
		f.values[k+`\`+strings.ToLower(inst)+`\device parameters`] = map[string]string{"PortName": port}
	}
	add("VID_0483&PID_5740", "400", "COM5", "USB Serial Device (COM5)")          // NanoVNA-H4, plugged in
	add("VID_0483&PID_5740", "5&2f1a&0&3", "COM3", "USB Serial Device (COM3)")   // same model, unplugged (phantom)
	add("VID_2886&PID_8044&MI_00", "6&1&0000", "COM11", "XIAO nRF52840")         // composite device, other VID
	add("VID_0483&PID_5740&MI_00", "7&abc&0&0000", "COM10", "Composite NanoVNA") // composite interface form
	add("ROOT_HUB30", "4&1", "", "")
	f.values[strings.ToLower(regSerialComm)] = map[string]string{
		`\Device\USBSER000`: "COM5", `\Device\USBSER001`: "COM11", `\Device\USBSER002`: "COM10",
	}
	return f
}

func TestListWindowsRegistry(t *testing.T) {
	got := ListWindows(newFakeReg())
	var paths []string
	for _, c := range got {
		paths = append(paths, c.Path+"="+c.ID.String())
	}
	want := "COM5=0483:5740,COM10=0483:5740,COM11=2886:8044"
	if strings.Join(paths, ",") != want {
		t.Fatalf("got %v, want %s (phantom COM3 must be skipped, COM10 sorted after COM5)", paths, want)
	}
	if got[0].Serial != "400" || !strings.Contains(got[0].Product, "COM5") {
		t.Errorf("details: %+v", got[0])
	}
}

func TestListWindowsWithoutSerialComm(t *testing.T) {
	f := newFakeReg()
	delete(f.values, strings.ToLower(regSerialComm))
	if got := ListWindows(f); len(got) != 4 {
		t.Errorf("without SERIALCOMM all ports are listed, got %d", len(got))
	}
	if got := ListWindows(fakeReg{}); len(got) != 0 {
		t.Errorf("empty registry: %+v", got)
	}
}

func TestWindowsDevicePath(t *testing.T) {
	for in, want := range map[string]string{"COM5": `\\.\COM5`, "com12": `\\.\COM12`, `\\.\COM3`: `\\.\COM3`, "/dev/ttyACM0": "/dev/ttyACM0"} {
		if got := WindowsDevicePath(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestBusyErrorMessages(t *testing.T) {
	if e := (&BusyError{Path: "COM5"}).Error(); !strings.Contains(e, "COM5 is busy") || !strings.Contains(e, "NanoVNA-Saver") {
		t.Errorf("windows-style busy: %q", e)
	}
}
