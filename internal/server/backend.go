package server

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backend is the storage under a Store: a small tree of files addressed by
// slash-separated names relative to the data root, e.g. "settings.json" or
// "refs/ref-869.json". The native build keeps them in a directory
// (DirBackend); the browser build keeps them in memory, mirrored to
// IndexedDB (MemBackend with an OnChange hook).
type Backend interface {
	// ReadFile returns the content of name; a missing file gives an error
	// that matches fs.ErrNotExist.
	ReadFile(name string) ([]byte, error)
	// WriteFile replaces name atomically.
	WriteFile(name string, data []byte) error
	// Remove deletes name; a missing file gives an fs.ErrNotExist error.
	Remove(name string) error
	// Stat describes name.
	Stat(name string) (FileInfo, error)
	// List returns the regular files directly inside dir ("" = the root),
	// sorted by name. A missing dir is not an error.
	List(dir string) ([]FileInfo, error)
	// SetModTime changes the modification time (used when restoring a backup).
	SetModTime(name string, t time.Time) error
}

// FileInfo describes one stored file. Name is the base name.
type FileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// ---------- directory backend (native build) ----------

// DirBackend stores files in a directory on disk.
type DirBackend struct{ Root string }

func (d DirBackend) path(name string) string { return filepath.Join(d.Root, filepath.FromSlash(name)) }

// ReadFile implements Backend.
func (d DirBackend) ReadFile(name string) ([]byte, error) { return os.ReadFile(d.path(name)) }

// WriteFile implements Backend (write to a temporary file, then rename).
func (d DirBackend) WriteFile(name string, data []byte) error {
	p := d.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeAtomic(p, data)
}

// Remove implements Backend.
func (d DirBackend) Remove(name string) error { return os.Remove(d.path(name)) }

// Stat implements Backend.
func (d DirBackend) Stat(name string) (FileInfo, error) {
	fi, err := os.Stat(d.path(name))
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Name: fi.Name(), Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// List implements Backend.
func (d DirBackend) List(dir string) ([]FileInfo, error) {
	ents, err := os.ReadDir(d.path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfo{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return out, nil
}

// SetModTime implements Backend.
func (d DirBackend) SetModTime(name string, t time.Time) error { return os.Chtimes(d.path(name), t, t) }

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- memory backend (browser build, tests) ----------

// MemFile is one file of a MemBackend.
type MemFile struct {
	Name    string // full slash-separated name
	Data    []byte
	ModTime time.Time
}

// MemBackend keeps files in memory. OnChange, if set, is called after every
// change (outside the lock) with the new file, or with Data == nil when the
// file was removed; the browser build mirrors changes to IndexedDB with it.
type MemBackend struct {
	mu       sync.Mutex
	files    map[string]MemFile
	OnChange func(f MemFile, removed bool)
	now      func() time.Time
}

// NewMemBackend creates a memory backend holding initial (may be nil).
func NewMemBackend(initial []MemFile) *MemBackend {
	m := &MemBackend{files: map[string]MemFile{}, now: time.Now}
	for _, f := range initial {
		if n, ok := cleanName(f.Name); ok {
			f.Name = n
			f.Data = append([]byte(nil), f.Data...)
			m.files[n] = f
		}
	}
	return m
}

// cleanName validates a slash-separated relative file name.
func cleanName(name string) (string, bool) {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	c := path.Clean(name)
	if c != name || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func notExist(op, name string) error { return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist} }

// ReadFile implements Backend.
func (m *MemBackend) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[name]
	if !ok {
		return nil, notExist("open", name)
	}
	return append([]byte(nil), f.Data...), nil
}

// WriteFile implements Backend.
func (m *MemBackend) WriteFile(name string, data []byte) error {
	n, ok := cleanName(name)
	if !ok {
		return &fs.PathError{Op: "write", Path: name, Err: fs.ErrInvalid}
	}
	f := MemFile{Name: n, Data: append([]byte(nil), data...), ModTime: m.now()}
	m.mu.Lock()
	m.files[n] = f
	m.mu.Unlock()
	m.changed(f, false)
	return nil
}

func (m *MemBackend) changed(f MemFile, removed bool) {
	if m.OnChange != nil {
		m.OnChange(f, removed)
	}
}

// Remove implements Backend.
func (m *MemBackend) Remove(name string) error {
	m.mu.Lock()
	f, ok := m.files[name]
	delete(m.files, name)
	m.mu.Unlock()
	if !ok {
		return notExist("remove", name)
	}
	m.changed(MemFile{Name: f.Name}, true)
	return nil
}

// Stat implements Backend.
func (m *MemBackend) Stat(name string) (FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[name]
	if !ok {
		return FileInfo{}, notExist("stat", name)
	}
	return FileInfo{Name: path.Base(name), Size: int64(len(f.Data)), ModTime: f.ModTime}, nil
}

// List implements Backend.
func (m *MemBackend) List(dir string) ([]FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FileInfo
	for n, f := range m.files {
		d := path.Dir(n)
		if d == "." {
			d = ""
		}
		if d == dir {
			out = append(out, FileInfo{Name: path.Base(n), Size: int64(len(f.Data)), ModTime: f.ModTime})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SetModTime implements Backend.
func (m *MemBackend) SetModTime(name string, t time.Time) error {
	m.mu.Lock()
	f, ok := m.files[name]
	if ok {
		f.ModTime = t
		m.files[name] = f
	}
	m.mu.Unlock()
	if !ok {
		return notExist("chtimes", name)
	}
	m.changed(f, false)
	return nil
}

// Files returns a copy of every file, sorted by name.
func (m *MemBackend) Files() []MemFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MemFile, 0, len(m.files))
	for _, f := range m.files {
		f.Data = append([]byte(nil), f.Data...)
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
