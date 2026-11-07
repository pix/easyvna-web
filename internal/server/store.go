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
	TrimActive   string           `json:"trimActive,omitempty"`
}

// Store manages the data directory: settings and snapshots.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates the data directory layout.
func NewStore(dir string) (*Store, error) {
	for _, sub := range []string{"snapshots", "refs", "trim", "reports"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
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

// ---------- REF traces (one per profile, previous kept for undo) ----------

func (st *Store) refPath(profile string, prev bool) string {
	f, _ := SnapshotFile("ref-" + profile)
	if prev {
		f = strings.TrimSuffix(f, ".json") + ".prev.json"
	}
	return filepath.Join(st.dir, "refs", f)
}

func (st *Store) readResult(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (st *Store) writeResult(path string, r *Result) error {
	if r == nil {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// SaveRef stores cur as the REF of profile (nil clears it) and prev as the
// undo copy. An empty prev is recorded as a marker so undo can clear again.
func (st *Store) SaveRef(profile string, cur, prev *Result) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if prev != nil {
		if err := st.writeResult(st.refPath(profile, true), prev); err != nil {
			return err
		}
	} else if err := writeAtomic(st.refPath(profile, true), []byte("null")); err != nil {
		return err
	}
	return st.writeResult(st.refPath(profile, false), cur)
}

// LoadRefPrev returns the undo copy (nil Result = "there was no REF").
func (st *Store) LoadRefPrev(profile string) (*Result, error) {
	b, err := os.ReadFile(st.refPath(profile, true))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "null" {
		return nil, nil
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// LoadRefs loads all stored REF traces keyed by profile.
func (st *Store) LoadRefs() map[string]*Result {
	out := map[string]*Result{}
	files, _ := filepath.Glob(filepath.Join(st.dir, "refs", "ref-*.json"))
	for _, f := range files {
		if strings.HasSuffix(f, ".prev.json") {
			continue
		}
		if r, err := st.readResult(f); err == nil && r != nil {
			out[r.Config.Profile] = r
		}
	}
	return out
}

// HasRefPrev reports whether an undo copy exists.
func (st *Store) HasRefPrev(profile string) bool {
	_, err := os.Stat(st.refPath(profile, true))
	return err == nil
}

// ---------- S21 compare session ----------

func (st *Store) comparePath(prev bool) string {
	if prev {
		return filepath.Join(st.dir, "compare.prev.json")
	}
	return filepath.Join(st.dir, "compare.json")
}

// SaveCompare stores the session (nil clears it) and prev as the undo copy.
func (st *Store) SaveCompare(cur, prev *CompareSession) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	pb, err := json.Marshal(prev)
	if err != nil {
		return err
	}
	if err := writeAtomic(st.comparePath(true), pb); err != nil {
		return err
	}
	if cur == nil {
		err := os.Remove(st.comparePath(false))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return writeAtomic(st.comparePath(false), b)
}

func (st *Store) readCompare(path string) (*CompareSession, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c *CompareSession
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadCompare returns the saved session or nil.
func (st *Store) LoadCompare() *CompareSession {
	c, _ := st.readCompare(st.comparePath(false))
	return c
}

// LoadComparePrev returns the undo copy (may be nil = "no session").
func (st *Store) LoadComparePrev() (*CompareSession, error) {
	return st.readCompare(st.comparePath(true))
}

// HasComparePrev reports whether an undo copy exists.
func (st *Store) HasComparePrev() bool {
	_, err := os.Stat(st.comparePath(true))
	return err == nil
}

// ---------- trim sessions ----------

// TrimInfo is a listing entry.
type TrimInfo struct {
	Name    string    `json:"name"`
	Target  float64   `json:"target"`
	Entries int       `json:"entries"`
	Updated time.Time `json:"updated"`
}

func (st *Store) trimPath(name string) (string, error) {
	f, err := SnapshotFile(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(st.dir, "trim", f), nil
}

// LoadTrim reads a trim session.
func (st *Store) LoadTrim(name string) (*TrimSession, error) {
	p, err := st.trimPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var t TrimSession
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	if t.Entries == nil {
		t.Entries = []TrimEntry{}
	}
	return &t, nil
}

// SaveTrim writes a trim session.
func (st *Store) SaveTrim(t *TrimSession) error {
	p, err := st.trimPath(t.Name)
	if err != nil {
		return err
	}
	cp := *t
	cp.Prediction = nil
	b, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p, b)
}

// ListTrim lists trim sessions, most recently updated first.
func (st *Store) ListTrim() []TrimInfo {
	files, _ := filepath.Glob(filepath.Join(st.dir, "trim", "*.json"))
	out := []TrimInfo{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var t TrimSession
		if json.Unmarshal(b, &t) != nil {
			continue
		}
		info := TrimInfo{Name: t.Name, Target: t.Target, Entries: len(t.Entries), Updated: t.Created}
		if n := len(t.Entries); n > 0 {
			info.Updated = t.Entries[n-1].Time
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// ---------- reports ----------

// ReportInfo is a listing entry.
type ReportInfo struct {
	File string    `json:"file"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// SaveReport writes a report and returns its file name.
func (st *Store) SaveReport(name string, html []byte) (string, error) {
	f, err := SnapshotFile(name)
	if err != nil {
		return "", err
	}
	file := strings.TrimSuffix(f, ".json") + "-" + time.Now().Format("20060102-150405") + ".html"
	return file, writeAtomic(filepath.Join(st.dir, "reports", file), html)
}

// ReportPath validates a report file name and returns its path.
func (st *Store) ReportPath(file string) (string, error) {
	if file != filepath.Base(file) || !strings.HasSuffix(file, ".html") || strings.HasPrefix(file, ".") {
		return "", errors.New("bad report name")
	}
	return filepath.Join(st.dir, "reports", file), nil
}

// ListReports lists saved reports, newest first.
func (st *Store) ListReports() []ReportInfo {
	files, _ := filepath.Glob(filepath.Join(st.dir, "reports", "*.html"))
	out := []ReportInfo{}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			out = append(out, ReportInfo{File: filepath.Base(f), Time: fi.ModTime(), Size: fi.Size()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}
