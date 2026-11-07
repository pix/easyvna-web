package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// BackupFormat identifies an EasyVNA Web data backup (one JSON file with
// every stored file: settings, REF traces, trim logs, compare session,
// reports and snapshots). The browser build uses it for Export / Import.
const BackupFormat = "easyvna-web-backup"

// Backup is the JSON document written by Store.Export.
type Backup struct {
	Format  string       `json:"format"`
	Version int          `json:"version"`
	Created time.Time    `json:"created"`
	App     string       `json:"app,omitempty"`
	Files   []BackupFile `json:"files"`
}

// BackupFile is one stored file. Data is the text itself, or base64 when
// Encoding is "base64" (for content that is not valid UTF-8).
type BackupFile struct {
	Name     string    `json:"name"`
	ModTime  time.Time `json:"modTime"`
	Encoding string    `json:"encoding,omitempty"`
	Data     string    `json:"data"`
}

// backupRootFiles are the files kept at the top of the data directory.
var backupRootFiles = []string{"settings.json", "compare.json", "compare.prev.json"}

// backupNameOK reports whether name belongs to the data layout: a root file
// or a .json/.html file directly inside one of the known sub-directories.
func backupNameOK(name string) bool {
	for _, f := range backupRootFiles {
		if name == f {
			return true
		}
	}
	n, ok := cleanName(name)
	if !ok || n != name {
		return false
	}
	dir, base := path.Split(name)
	if base == "" || strings.HasPrefix(base, ".") || strings.ContainsAny(base, " \t\r\n") {
		return false
	}
	switch strings.TrimSuffix(dir, "/") {
	case "snapshots", "refs", "trim", "antennas":
		return strings.HasSuffix(base, ".json")
	case "reports":
		return strings.HasSuffix(base, ".html")
	}
	return false
}

// Export returns every stored file as a Backup JSON document.
func (st *Store) Export(app string, now time.Time) ([]byte, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	bk := Backup{Format: BackupFormat, Version: 1, Created: now.UTC(), App: app, Files: []BackupFile{}}
	add := func(name string, fi FileInfo) error {
		b, err := st.b.ReadFile(name)
		if err != nil {
			return err
		}
		f := BackupFile{Name: name, ModTime: fi.ModTime.UTC()}
		if utf8.Valid(b) {
			f.Data = string(b)
		} else {
			f.Encoding, f.Data = "base64", base64.StdEncoding.EncodeToString(b)
		}
		bk.Files = append(bk.Files, f)
		return nil
	}
	for _, n := range backupRootFiles {
		if fi, err := st.b.Stat(n); err == nil {
			if err := add(n, fi); err != nil {
				return nil, err
			}
		}
	}
	for _, d := range storeDirs {
		list, err := st.b.List(d)
		if err != nil {
			return nil, err
		}
		for _, fi := range list {
			if name := d + "/" + fi.Name; backupNameOK(name) {
				if err := add(name, fi); err != nil {
					return nil, err
				}
			}
		}
	}
	return json.MarshalIndent(bk, "", " ")
}

// ParseBackup decodes and validates a backup document.
func ParseBackup(data []byte) (*Backup, error) {
	var bk Backup
	if err := json.Unmarshal(data, &bk); err != nil {
		return nil, fmt.Errorf("not an EasyVNA Web backup: %w", err)
	}
	if bk.Format != BackupFormat {
		return nil, errors.New("not an EasyVNA Web backup (format field missing)")
	}
	if bk.Version != 1 {
		return nil, fmt.Errorf("backup version %d is not supported by this version", bk.Version)
	}
	for _, f := range bk.Files {
		if !backupNameOK(f.Name) {
			return nil, fmt.Errorf("backup contains an unexpected file %q", f.Name)
		}
		switch f.Encoding {
		case "":
		case "base64":
			if _, err := base64.StdEncoding.DecodeString(f.Data); err != nil {
				return nil, fmt.Errorf("backup file %q: %w", f.Name, err)
			}
		default:
			return nil, fmt.Errorf("backup file %q: unknown encoding %q", f.Name, f.Encoding)
		}
	}
	return &bk, nil
}

// Import writes every file of a backup into the store, replacing files of
// the same name and keeping the others. It returns the number of files
// written. The running server keeps its loaded state; restart (reload the
// page) to pick up imported settings, REF traces and the compare session.
func (st *Store) Import(data []byte) (int, error) {
	bk, err := ParseBackup(data)
	if err != nil {
		return 0, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, f := range bk.Files {
		b := []byte(f.Data)
		if f.Encoding == "base64" {
			b, _ = base64.StdEncoding.DecodeString(f.Data)
		}
		if err := st.b.WriteFile(f.Name, b); err != nil {
			return n, err
		}
		if !f.ModTime.IsZero() {
			_ = st.b.SetModTime(f.Name, f.ModTime)
		}
		n++
	}
	return n, nil
}

// hBackupGet serves GET /api/backup (browser build only).
func (s *Server) hBackupGet(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	b, err := s.store.Export(s.opt.AppName, now)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="easyvna-web-backup-%s.json"`, now.Format("20060102-150405")))
	_, _ = w.Write(b)
}

// hBackupPost serves POST /api/backup with a backup document as the body.
func (s *Server) hBackupPost(w http.ResponseWriter, r *http.Request) {
	body, err := readAll(r, 64<<20)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	n, err := s.store.Import(body)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"files": n, "message": fmt.Sprintf("imported %d files", n)})
}
