package serialport

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcRoot can be overridden by tests.
var ProcRoot = "/proc"

// Holder is another process that has a device open.
type Holder struct {
	PID  int
	Comm string
}

// Holders lists other processes that currently have path open (Linux /proc
// scan; symlinks such as /dev/serial/by-id/... are resolved first). It is
// best effort: processes we may not inspect are skipped.
func Holders(path string) []Holder {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	self := os.Getpid()
	ents, _ := os.ReadDir(ProcRoot)
	var out []Holder
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(ProcRoot, e.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			l, err := os.Readlink(filepath.Join(ProcRoot, e.Name(), "fd", fd.Name()))
			if err == nil && l == target {
				comm, _ := os.ReadFile(filepath.Join(ProcRoot, e.Name(), "comm"))
				out = append(out, Holder{PID: pid, Comm: strings.TrimSpace(string(comm))})
				break
			}
		}
	}
	return out
}
