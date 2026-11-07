package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SlotMeta records what vnaview saved into a firmware calibration slot.
// (The shell cannot report a slot's frequency range without recalling it.)
type SlotMeta struct {
	Profile string    `json:"profile"`
	Start   float64   `json:"start"`
	Stop    float64   `json:"stop"`
	Points  int       `json:"points"`
	Saved   time.Time `json:"saved"`
}

// Settings are persisted in <data>/settings.json.
type Settings struct {
	ProfileSlots map[string]int   `json:"profileSlots"`
	Slots        map[int]SlotMeta `json:"slots"`
	AutoRecall   bool             `json:"autoRecall"`
}

// Store manages the data directory: settings and snapshots.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates the data directory layout.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir returns the data directory.
func (st *Store) Dir() string { return st.dir }

// LoadSettings returns stored settings or defaults.
func (st *Store) LoadSettings() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := Settings{ProfileSlots: map[string]int{}, Slots: map[int]SlotMeta{}, AutoRecall: true}
	b, err := os.ReadFile(filepath.Join(st.dir, "settings.json"))
	if err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.ProfileSlots == nil {
		s.ProfileSlots = map[string]int{}
	}
	if s.Slots == nil {
		s.Slots = map[int]SlotMeta{}
	}
	return s
}

// SaveSettings writes settings atomically.
func (st *Store) SaveSettings(s Settings) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(st.dir, "settings.json"), b)
}

var nameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SnapshotFile maps a user-facing snapshot name to a safe file name.
func SnapshotFile(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || len(n) > 80 {
		return "", errors.New("snapshot name must be 1-80 characters")
	}
	f := strings.Trim(nameRe.ReplaceAllString(n, "_"), "._")
	if f == "" {
		return "", errors.New("snapshot name has no usable characters")
	}
	return f + ".json", nil
}

// SnapshotInfo is a directory listing entry.
type SnapshotInfo struct {
	Name    string    `json:"name"`
	File    string    `json:"file"`
	Time    time.Time `json:"time"`
	Profile string    `json:"profile"`
	Points  int       `json:"points"`
	Start   float64   `json:"start"`
	Stop    float64   `json:"stop"`
}

// SaveSnapshot stores r under name (overwriting).
func (st *Store) SaveSnapshot(name string, r *Result) (SnapshotInfo, error) {
	file, err := SnapshotFile(name)
	if err != nil {
		return SnapshotInfo{}, err
	}
	cp := *r
	cp.Name = strings.TrimSpace(name)
	b, err := json.Marshal(&cp)
	if err != nil {
		return SnapshotInfo{}, err
	}
	if err := writeAtomic(filepath.Join(st.dir, "snapshots", file), b); err != nil {
		return SnapshotInfo{}, err
	}
	return infoOf(&cp, file), nil
}

func infoOf(r *Result, file string) SnapshotInfo {
	return SnapshotInfo{Name: r.Name, File: file, Time: r.Time, Profile: r.Config.Profile,
		Points: len(r.Freq), Start: r.Config.Start, Stop: r.Config.Stop}
}

// LoadSnapshot reads a snapshot by name.
func (st *Store) LoadSnapshot(name string) (*Result, error) {
	file, err := SnapshotFile(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(st.dir, "snapshots", file))
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", file, err)
	}
	return &r, nil
}

// DeleteSnapshot removes a snapshot.
func (st *Store) DeleteSnapshot(name string) error {
	file, err := SnapshotFile(name)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(st.dir, "snapshots", file))
}

// ListSnapshots returns all snapshots, newest first.
func (st *Store) ListSnapshots() []SnapshotInfo {
	files, _ := filepath.Glob(filepath.Join(st.dir, "snapshots", "*.json"))
	out := []SnapshotInfo{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r struct {
			Name   string    `json:"name"`
			Time   time.Time `json:"time"`
			Config Config    `json:"config"`
			Freq   []float64 `json:"freq"`
		}
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		out = append(out, SnapshotInfo{Name: r.Name, File: filepath.Base(f), Time: r.Time, Profile: r.Config.Profile,
			Points: len(r.Freq), Start: r.Config.Start, Stop: r.Config.Stop})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
