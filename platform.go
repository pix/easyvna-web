package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/pix/easyvna-web/internal/serialport"
)

// defaultDataDir picks the per-user data directory:
//   - Windows: %LOCALAPPDATA%\easyvna-web
//   - macOS:   ~/Library/Application Support/easyvna-web
//   - others:  $XDG_DATA_HOME/easyvna-web or ~/.local/share/easyvna-web
//     (an existing ~/.local/share/vnaview from earlier builds is reused).
func defaultDataDir(goos string, getenv func(string) string, home func() (string, error), exists func(string) bool) string {
	switch goos {
	case "windows":
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "easyvna-web")
		}
		if d := getenv("APPDATA"); d != "" {
			return filepath.Join(d, "easyvna-web")
		}
	case "darwin":
		if h, err := home(); err == nil {
			return filepath.Join(h, "Library", "Application Support", "easyvna-web")
		}
	default:
		base := getenv("XDG_DATA_HOME")
		if base == "" {
			if h, err := home(); err == nil {
				base = filepath.Join(h, ".local", "share")
			}
		}
		if base != "" {
			dir, legacy := filepath.Join(base, "easyvna-web"), filepath.Join(base, "vnaview")
			if !exists(dir) && exists(legacy) {
				return legacy
			}
			return dir
		}
	}
	return "easyvna-web-data"
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Device kinds: which driver opens a port.
const (
	kindH  = "nanovna-h"  // text shell (NanoVNA-H / H4, NanoVNA-D firmware)
	kindV2 = "nanovna-v2" // binary protocol (NanoVNA V2, LiteVNA), experimental
)

// kindOf returns the driver for a USB ID ("" when unknown).
func kindOf(id serialport.USBID) string {
	switch id {
	case serialport.IDNanoVNAH:
		return kindH
	case serialport.IDNanoVNAV2:
		return kindV2
	}
	return ""
}

// pickPort chooses the NanoVNA serial port from the candidates and the
// driver for it. A NanoVNA-H wins over a V2 when both are plugged in.
func pickPort(goos string, cands []serialport.Candidate) (string, string, error) {
	for _, kind := range []string{kindH, kindV2} {
		for _, c := range cands {
			if kindOf(c.ID) == kind {
				return c.Path, kind, nil
			}
		}
	}
	if goos == "darwin" { // no VID:PID on macOS without cgo: accept a lone usbmodem
		var unknown []string
		for _, c := range cands {
			if c.ID == (serialport.USBID{}) {
				unknown = append(unknown, c.Path)
			}
		}
		if len(unknown) == 1 {
			return unknown[0], kindH, nil
		}
		if len(unknown) > 1 {
			return "", "", fmt.Errorf("several USB serial ports (%s): choose one with -port (and -driver nanovna-v2 for a V2)", strings.Join(unknown, ", "))
		}
	}
	var seen []string
	for _, c := range cands {
		seen = append(seen, fmt.Sprintf("%s (%s)", c.Path, c.ID))
	}
	if len(seen) == 0 {
		switch goos {
		case "windows":
			return "", "", errors.New("no NanoVNA found: no COM port with USB ID 0483:5740 (NanoVNA-H) or 04b4:0008 (V2). Is it plugged in and switched on? (Windows 10/11 needs no driver; check Device Manager > Ports)")
		case "darwin":
			return "", "", errors.New("no NanoVNA found: no /dev/cu.usbmodem* device (is it plugged in and switched on?)")
		}
		return "", "", errors.New("no NanoVNA found: no /dev/ttyACM* devices (is it plugged in and switched on?)")
	}
	return "", "", fmt.Errorf("no NanoVNA (USB %s or %s) found among %s; use -port", serialport.IDNanoVNAH, serialport.IDNanoVNAV2, strings.Join(seen, ", "))
}

// listenFallback listens on addr; if that port is taken and fallback is
// allowed (the user did not pass -listen), it tries the next ports.
func listenFallback(addr string, fallback bool, tries int) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil || !fallback {
		return ln, err
	}
	host, p, serr := net.SplitHostPort(addr)
	port, perr := strconv.Atoi(p)
	if serr != nil || perr != nil {
		return nil, err
	}
	for i := 1; i <= tries; i++ {
		if ln2, err2 := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port+i))); err2 == nil {
			return ln2, nil
		}
	}
	return nil, err
}

// browserHost turns a listen address into something a browser can open.
func browserHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func quitHint(goos string) string {
	if goos == "windows" {
		return "Close this window to quit."
	}
	return "Press Ctrl+C to quit."
}

// browserCommand returns the command that opens url in the default browser.
func browserCommand(goos, url string) (string, []string, error) {
	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	case "darwin":
		return "open", []string{url}, nil
	case "linux", "freebsd", "openbsd", "netbsd":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return "", nil, errors.New("no graphical session")
		}
		return "xdg-open", []string{url}, nil
	}
	return "", nil, fmt.Errorf("unsupported OS %s", goos)
}

func openBrowser(goos, url string) error {
	name, args, err := browserCommand(goos, url)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// fatal logs and exits. On Windows the console of a double-clicked .exe
// closes immediately, so wait for Enter to keep the message readable.
func fatal(l *log.Logger, format string, args ...any) {
	l.Printf(format, args...)
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "\nPress Enter to close this window.")
		_, _ = fmt.Fscanln(os.Stdin)
	}
	os.Exit(1)
}
