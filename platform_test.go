package main

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pix/easyvna-web/internal/serialport"
)

func TestDefaultDataDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := func() (string, error) { return "/home/u", nil }
	none := func(string) bool { return false }
	if d := defaultDataDir("windows", env(map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}), home, none); d != filepath.Join(`C:\Users\u\AppData\Local`, "easyvna-web") {
		t.Errorf("windows: %q", d)
	}
	if d := defaultDataDir("darwin", env(nil), home, none); d != filepath.Join("/home/u", "Library", "Application Support", "easyvna-web") {
		t.Errorf("darwin: %q", d)
	}
	if d := defaultDataDir("linux", env(nil), home, none); d != filepath.Join("/home/u", ".local", "share", "easyvna-web") {
		t.Errorf("linux: %q", d)
	}
	if d := defaultDataDir("linux", env(map[string]string{"XDG_DATA_HOME": "/x"}), home, none); d != filepath.Join("/x", "easyvna-web") {
		t.Errorf("xdg: %q", d)
	}
	legacy := func(p string) bool { return strings.HasSuffix(p, "vnaview") }
	if d := defaultDataDir("linux", env(nil), home, legacy); d != filepath.Join("/home/u", ".local", "share", "vnaview") {
		t.Errorf("legacy dir not reused: %q", d)
	}
	nohome := func() (string, error) { return "", errors.New("no home") }
	if d := defaultDataDir("linux", env(nil), nohome, none); d != "easyvna-web-data" {
		t.Errorf("fallback: %q", d)
	}
}

func TestPickPort(t *testing.T) {
	nano := serialport.Candidate{Path: "COM5", ID: serialport.IDNanoVNAH}
	v2 := serialport.Candidate{Path: "COM7", ID: serialport.IDNanoVNAV2}
	xiao := serialport.Candidate{Path: "COM11", ID: serialport.USBID{VID: "2886", PID: "8044"}}
	if p, k, err := pickPort("windows", []serialport.Candidate{xiao, nano}); err != nil || p != "COM5" || k != kindH {
		t.Errorf("windows: %q %q %v", p, k, err)
	}
	if p, k, err := pickPort("windows", []serialport.Candidate{v2, xiao, nano}); err != nil || p != "COM5" || k != kindH {
		t.Errorf("H wins over V2: %q %q %v", p, k, err)
	}
	if _, _, err := pickPort("windows", nil); err == nil || !strings.Contains(err.Error(), "COM port") || !strings.Contains(err.Error(), "04b4:0008") {
		t.Errorf("windows none: %v", err)
	}
	if _, _, err := pickPort("linux", []serialport.Candidate{xiao}); err == nil || !strings.Contains(err.Error(), "-port") {
		t.Errorf("linux other device: %v", err)
	}
	if p, k, err := pickPort("linux", []serialport.Candidate{xiao, {Path: "/dev/ttyACM1", ID: serialport.IDNanoVNAV2}}); err != nil || p != "/dev/ttyACM1" || k != kindV2 {
		t.Errorf("v2: %q %q %v", p, k, err)
	}
	mac := []serialport.Candidate{{Path: "/dev/cu.usbmodem401"}}
	if p, k, err := pickPort("darwin", mac); err != nil || p != "/dev/cu.usbmodem401" || k != kindH {
		t.Errorf("darwin single: %q %v", p, err)
	}
	if _, _, err := pickPort("darwin", append(mac, serialport.Candidate{Path: "/dev/cu.usbmodem1101"})); err == nil {
		t.Error("darwin ambiguous accepted")
	}
}

func TestListenFallback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	addr := busy.Addr().String()
	if _, err := listenFallback(addr, false, 5); err == nil {
		t.Fatal("explicit -listen must not silently move")
	}
	ln, err := listenFallback(addr, true, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().String() == addr {
		t.Error("did not move to another port")
	}
}

func TestBrowserBits(t *testing.T) {
	if h := browserHost("0.0.0.0:8080"); h != "127.0.0.1:8080" {
		t.Errorf("host %q", h)
	}
	if h := browserHost("127.0.0.1:8081"); h != "127.0.0.1:8081" {
		t.Errorf("host %q", h)
	}
	name, args, err := browserCommand("windows", "http://127.0.0.1:8080")
	if err != nil || name != "rundll32" || len(args) != 2 || args[0] != "url.dll,FileProtocolHandler" {
		t.Errorf("windows browser: %s %v %v", name, args, err)
	}
	if n, _, _ := browserCommand("darwin", "u"); n != "open" {
		t.Errorf("darwin browser %q", n)
	}
	if quitHint("windows") != "Close this window to quit." {
		t.Error("quit hint")
	}
}
