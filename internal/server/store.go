package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SlotMeta records what EasyVNA Web saved into a firmware calibration slot.
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
	b   Backend
	mu  sync.Mutex
}

// storeDirs are the sub-directories of the data directory.
var storeDirs = []string{"snapshots", "refs", "trim", "reports", "antennas"}

// NewStore creates the data directory layout.
func NewStore(dir string) (*Store, error) {
	for _, sub := range storeDirs {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir, b: DirBackend{Root: dir}}, nil
}

// NewStoreBackend creates a store on another backend (the browser build
// uses a MemBackend). label is what Dir reports, e.g. "this browser".
func NewStoreBackend(b Backend, label string) *Store { return &Store{dir: label, b: b} }

// Dir returns the data directory (or the label of a non-disk backend).
func (st *Store) Dir() string { return st.dir }

// Backend returns the storage backend.
func (st *Store) Backend() Backend { return st.b }

// listMatch lists the files of dir whose base name matches pattern.
func (st *Store) listMatch(dir, pattern string) []FileInfo {
	all, _ := st.b.List(dir)
	var out []FileInfo
	for _, f := range all {
		if ok, _ := path.Match(pattern, f.Name); ok {
			out = append(out, f)
		}
	}
	return out
}

// LoadSettings returns stored settings or defaults.
func (st *Store) LoadSettings() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := Settings{ProfileSlots: map[string]int{}, Slots: map[int]SlotMeta{}, AutoRecall: true}
	b, err := st.b.ReadFile("settings.json")
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
	return st.b.WriteFile("settings.json", b)
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
	if err := st.b.WriteFile("snapshots/"+file, b); err != nil {
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
	b, err := st.b.ReadFile("snapshots/" + file)
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
	return st.b.Remove("snapshots/" + file)
}

// ListSnapshots returns all snapshots, newest first.
func (st *Store) ListSnapshots() []SnapshotInfo {
	out := []SnapshotInfo{}
	for _, f := range st.listMatch("snapshots", "*.json") {
		b, err := st.b.ReadFile("snapshots/" + f.Name)
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
		out = append(out, SnapshotInfo{Name: r.Name, File: f.Name, Time: r.Time, Profile: r.Config.Profile,
			Points: len(r.Freq), Start: r.Config.Start, Stop: r.Config.Stop})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// ---------- REF traces (one per profile, previous kept for undo) ----------

func (st *Store) refPath(profile string, prev bool) string {
	f, _ := SnapshotFile("ref-" + profile)
	if prev {
		f = strings.TrimSuffix(f, ".json") + ".prev.json"
	}
	return "refs/" + f
}

func (st *Store) readResult(name string) (*Result, error) {
	b, err := st.b.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (st *Store) writeResult(name string, r *Result) error {
	if r == nil {
		err := st.b.Remove(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return st.b.WriteFile(name, b)
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
	} else if err := st.b.WriteFile(st.refPath(profile, true), []byte("null")); err != nil {
		return err
	}
	return st.writeResult(st.refPath(profile, false), cur)
}

// LoadRefPrev returns the undo copy (nil Result = "there was no REF").
func (st *Store) LoadRefPrev(profile string) (*Result, error) {
	b, err := st.b.ReadFile(st.refPath(profile, true))
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
	for _, f := range st.listMatch("refs", "ref-*.json") {
		if strings.HasSuffix(f.Name, ".prev.json") {
			continue
		}
		if r, err := st.readResult("refs/" + f.Name); err == nil && r != nil {
			out[r.Config.Profile] = r
		}
	}
	return out
}

// HasRefPrev reports whether an undo copy exists.
func (st *Store) HasRefPrev(profile string) bool {
	_, err := st.b.Stat(st.refPath(profile, true))
	return err == nil
}

// ---------- S21 compare session ----------

func (st *Store) comparePath(prev bool) string {
	if prev {
		return "compare.prev.json"
	}
	return "compare.json"
}

// SaveCompare stores the session (nil clears it) and prev as the undo copy.
func (st *Store) SaveCompare(cur, prev *CompareSession) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	pb, err := json.Marshal(prev)
	if err != nil {
		return err
	}
	if err := st.b.WriteFile(st.comparePath(true), pb); err != nil {
		return err
	}
	if cur == nil {
		err := st.b.Remove(st.comparePath(false))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return st.b.WriteFile(st.comparePath(false), b)
}

func (st *Store) readCompare(name string) (*CompareSession, error) {
	b, err := st.b.ReadFile(name)
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
	_, err := st.b.Stat(st.comparePath(true))
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
	return "trim/" + f, nil
}

// LoadTrim reads a trim session.
func (st *Store) LoadTrim(name string) (*TrimSession, error) {
	p, err := st.trimPath(name)
	if err != nil {
		return nil, err
	}
	b, err := st.b.ReadFile(p)
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
	return st.b.WriteFile(p, b)
}

// ListTrim lists trim sessions, most recently updated first.
func (st *Store) ListTrim() []TrimInfo {
	out := []TrimInfo{}
	for _, f := range st.listMatch("trim", "*.json") {
		b, err := st.b.ReadFile("trim/" + f.Name)
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

// ReportFileName builds "<name>-YYYYMMDD-HHMMSS.html" for a report.
func ReportFileName(name string, t time.Time) (string, error) {
	f, err := SnapshotFile(name)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(f, ".json") + "-" + t.Format("20060102-150405") + ".html", nil
}

// SaveReportFile writes a report under the given (validated) file name.
func (st *Store) SaveReportFile(file string, html []byte) error {
	p, err := reportName(file)
	if err != nil {
		return err
	}
	return st.b.WriteFile(p, html)
}

// ReadReportFile returns a saved report by file name.
func (st *Store) ReadReportFile(file string) ([]byte, error) {
	p, err := reportName(file)
	if err != nil {
		return nil, err
	}
	return st.b.ReadFile(p)
}

// ErrBadReportName is returned for report file names that are not plain
// "<name>.html" files.
var ErrBadReportName = errors.New("bad report name")

// reportName validates a report file name and returns its store name.
func reportName(file string) (string, error) {
	if file != path.Base(file) || file != filepath.Base(file) || !strings.HasSuffix(file, ".html") || strings.HasPrefix(file, ".") {
		return "", ErrBadReportName
	}
	return "reports/" + file, nil
}

// ListReports lists saved reports, newest first.
func (st *Store) ListReports() []ReportInfo {
	out := []ReportInfo{}
	for _, f := range st.listMatch("reports", "*.html") {
		out = append(out, ReportInfo{File: f.Name, Time: f.ModTime, Size: f.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}
