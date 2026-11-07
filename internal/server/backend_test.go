package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backends returns a fresh directory backend and memory backend.
func backends(t *testing.T) map[string]Backend {
	return map[string]Backend{"dir": DirBackend{Root: t.TempDir()}, "mem": NewMemBackend(nil)}
}

func TestBackendContract(t *testing.T) {
	for name, b := range backends(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := b.ReadFile("settings.json"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing read: %v", err)
			}
			if err := b.Remove("nope.json"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing remove: %v", err)
			}
			if l, err := b.List("snapshots"); err != nil || len(l) != 0 {
				t.Fatalf("missing dir list: %v %v", l, err)
			}
			for _, f := range []string{"settings.json", "refs/ref-869.json", "refs/ref-433.json", "refs/a/deep.json"} {
				if err := b.WriteFile(f, []byte("{}"+f)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := b.ReadFile("refs/ref-869.json")
			if err != nil || string(got) != "{}refs/ref-869.json" {
				t.Fatalf("read back %q %v", got, err)
			}
			l, _ := b.List("refs")
			if len(l) != 2 || l[0].Name != "ref-433.json" || l[1].Name != "ref-869.json" || l[0].Size != int64(len("{}refs/ref-433.json")) {
				t.Fatalf("list refs = %+v", l)
			}
			root, _ := b.List("")
			if len(root) != 1 || root[0].Name != "settings.json" {
				t.Fatalf("list root = %+v", root)
			}
			when := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
			if err := b.SetModTime("settings.json", when); err != nil {
				t.Fatal(err)
			}
			fi, err := b.Stat("settings.json")
			if err != nil || !fi.ModTime.Equal(when) || fi.Name != "settings.json" {
				t.Fatalf("stat %+v %v", fi, err)
			}
			if err := b.Remove("refs/ref-433.json"); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Stat("refs/ref-433.json"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("stat after remove: %v", err)
			}
		})
	}
}

func TestMemBackendOnChangeAndNames(t *testing.T) {
	m := NewMemBackend([]MemFile{{Name: "settings.json", Data: []byte("x")}, {Name: "../evil", Data: []byte("y")}})
	if len(m.Files()) != 1 {
		t.Fatalf("bad initial names must be dropped: %+v", m.Files())
	}
	var log []string
	m.OnChange = func(f MemFile, removed bool) {
		if removed {
			log = append(log, "rm "+f.Name)
		} else {
			log = append(log, "put "+f.Name+"="+string(f.Data))
		}
	}
	_ = m.WriteFile("trim/a.json", []byte("1"))
	_ = m.Remove("trim/a.json")
	_ = m.Remove("trim/a.json") // missing: no callback
	if err := m.WriteFile("/abs", nil); err == nil {
		t.Fatal("absolute name accepted")
	}
	if err := m.WriteFile("a/../b", nil); err == nil {
		t.Fatal("unclean name accepted")
	}
	if strings.Join(log, ";") != "put trim/a.json=1;rm trim/a.json" {
		t.Fatalf("OnChange log %q", log)
	}
}

// A store on either backend behaves the same; the directory backend keeps
// the historical on-disk layout.
func TestStoreOnBackends(t *testing.T) {
	dir := t.TempDir()
	disk, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mem := NewStoreBackend(NewMemBackend(nil), "this browser")
	if mem.Dir() != "this browser" || disk.Dir() != dir {
		t.Fatal("Dir label")
	}
	for name, st := range map[string]*Store{"dir": disk, "mem": mem} {
		t.Run(name, func(t *testing.T) {
			r := &Result{Time: time.Now(), Config: Config{Profile: "869", Start: 1, Stop: 2}, Freq: []float64{1, 2}}
			if _, err := st.SaveSnapshot("my whip", r); err != nil {
				t.Fatal(err)
			}
			if l := st.ListSnapshots(); len(l) != 1 || l[0].Name != "my whip" || l[0].File != "my_whip.json" {
				t.Fatalf("snapshots %+v", l)
			}
			if err := st.SaveRef("869", r, nil); err != nil {
				t.Fatal(err)
			}
			if refs := st.LoadRefs(); len(refs) != 1 || refs["869"] == nil || !st.HasRefPrev("869") {
				t.Fatalf("refs %+v", refs)
			}
			if p, err := st.LoadRefPrev("869"); err != nil || p != nil {
				t.Fatalf("prev %v %v", p, err)
			}
			if err := st.SaveReportFile("a-1.html", []byte("<p>r")); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveReportFile("../x.html", nil); err == nil {
				t.Fatal("bad report name accepted")
			}
			if b, err := st.ReadReportFile("a-1.html"); err != nil || string(b) != "<p>r" {
				t.Fatalf("report %q %v", b, err)
			}
			if _, err := st.ReadReportFile("missing.html"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing report: %v", err)
			}
			if l := st.ListReports(); len(l) != 1 || l[0].Size != 4 {
				t.Fatalf("reports %+v", l)
			}
			if err := st.DeleteSnapshot("my whip"); err != nil {
				t.Fatal(err)
			}
			if err := st.DeleteSnapshot("my whip"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("second delete: %v", err)
			}
		})
	}
	for _, f := range []string{"refs/ref-869.json", "refs/ref-869.prev.json", "reports/a-1.html"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("on-disk layout: %v", err)
		}
	}
}

func TestBackupRoundTrip(t *testing.T) {
	src := NewStoreBackend(NewMemBackend(nil), "src")
	set := src.LoadSettings()
	set.AutoRecall = false
	set.ProfileSlots["869"] = 5
	if err := src.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	r := &Result{Time: time.Now(), Config: Config{Profile: "433"}, Freq: []float64{1}}
	_ = src.SaveRef("433", r, nil)
	_ = src.SaveTrim(&TrimSession{Name: "whip", Target: 869e6})
	_ = src.SaveReportFile("whip-20250101-000000.html", []byte("<html>é</html>"))
	_ = src.Backend().WriteFile("snapshots/bin.json", []byte{0xff, 0xfe, 0})
	old := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	_ = src.Backend().SetModTime("reports/whip-20250101-000000.html", old)
	_ = src.Backend().WriteFile("stray.txt", []byte("not exported"))

	data, err := src.Export("EasyVNA Web test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("stray")) {
		t.Fatal("unknown file exported")
	}
	for _, dst := range []*Store{NewStoreBackend(NewMemBackend(nil), "dst"), mustStore(t)} {
		n, err := dst.Import(data)
		if err != nil || n != 6 { // settings, ref + ref prev marker, trim, report, snapshot
			t.Fatalf("import n=%d err=%v", n, err)
		}
		if s := dst.LoadSettings(); s.AutoRecall || s.ProfileSlots["869"] != 5 {
			t.Fatalf("settings %+v", s)
		}
		if dst.LoadRefs()["433"] == nil || len(dst.ListTrim()) != 1 {
			t.Fatal("refs/trim not restored")
		}
		rl := dst.ListReports()
		if len(rl) != 1 || !rl[0].Time.Equal(old) {
			t.Fatalf("report mod time not restored: %+v", rl)
		}
		if b, _ := dst.Backend().ReadFile("snapshots/bin.json"); !bytes.Equal(b, []byte{0xff, 0xfe, 0}) {
			t.Fatalf("binary content %v", b)
		}
	}
}

func mustStore(t *testing.T) *Store {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestParseBackupRejects(t *testing.T) {
	cases := map[string]string{
		"not json":     `{`,
		"wrong format": `{"format":"x","version":1}`,
		"version":      `{"format":"easyvna-web-backup","version":2}`,
		"traversal":    `{"format":"easyvna-web-backup","version":1,"files":[{"name":"../x.json","data":""}]}`,
		"unknown dir":  `{"format":"easyvna-web-backup","version":1,"files":[{"name":"bin/x.json","data":""}]}`,
		"wrong ext":    `{"format":"easyvna-web-backup","version":1,"files":[{"name":"reports/x.js","data":""}]}`,
		"hidden":       `{"format":"easyvna-web-backup","version":1,"files":[{"name":"refs/.x.json","data":""}]}`,
		"encoding":     `{"format":"easyvna-web-backup","version":1,"files":[{"name":"refs/x.json","encoding":"rot13","data":""}]}`,
		"bad base64":   `{"format":"easyvna-web-backup","version":1,"files":[{"name":"refs/x.json","encoding":"base64","data":"!!"}]}`,
	}
	for name, in := range cases {
		if _, err := ParseBackup([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseBackup([]byte(`{"format":"easyvna-web-backup","version":1,"files":[]}`)); err != nil {
		t.Errorf("empty backup rejected: %v", err)
	}
}

func TestBackupAPI(t *testing.T) {
	g := newRig(t, "869", false, false)
	// Not registered unless Options.Backup is set (the native build).
	if resp, err := http.Get(g.ts.URL + "/api/backup"); err != nil || resp.StatusCode == 200 {
		t.Fatalf("backup route on native build: %v %v", resp.StatusCode, err)
	}
	g.s.opt.Backup = true
	ts := httptest.NewServer(g.s.Handler())
	defer ts.Close()
	srv := ts.URL
	if c := call(t, ts, "POST", "/api/sweep", "", nil); c != 200 {
		t.Fatalf("sweep %d", c)
	}
	if c := call(t, ts, "POST", "/api/ref", "", nil); c != 200 {
		t.Fatalf("ref %d", c)
	}
	resp, err := http.Get(srv + "/api/backup")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("export: %v", err)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "easyvna-web-backup-") {
		t.Fatalf("content-disposition %q", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var bk Backup
	if err := json.Unmarshal(body, &bk); err != nil || len(bk.Files) == 0 {
		t.Fatalf("export body: %v", err)
	}
	resp, err = http.Post(srv+"/api/backup", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("import: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	resp, _ = http.Post(srv+"/api/backup", "application/json", strings.NewReader(`{"format":"nope"}`))
	if resp.StatusCode != 400 {
		t.Fatalf("bad import status %d", resp.StatusCode)
	}
	resp.Body.Close()
}
