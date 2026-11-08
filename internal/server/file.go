package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// FileSource describes a measurement opened from a file. Such a result is
// shown with the usual analysis but is never live: no device was involved.
type FileSource struct {
	File    string    `json:"file"`              // base name of the file
	Format  string    `json:"format"`            // e.g. "Touchstone 1-port (.s1p)"
	Loaded  time.Time `json:"loaded"`            // when it was opened
	Z0      float64   `json:"z0"`                // reference impedance in the file
	Notes   []string  `json:"notes,omitempty"`   // what was done while reading it
	Profile string    `json:"profile,omitempty"` // profile whose range the file matches
}

// maxFileBytes bounds an uploaded measurement file.
const maxFileBytes = 32 << 20

// fileConfigLocked builds the analysis setup for a file: the bands of a
// profile with the same range, else every LoRa band for a wide file
// (identify), else the bands it overlaps.
func (s *Server) fileConfigLocked(freq []float64, s21 bool) (Config, string) {
	start, stop := freq[0], freq[len(freq)-1]
	cfg := Config{Profile: "file", Start: start, Stop: stop, Points: len(freq), S21: s21, Avg: 1, PortExtPs: 0}
	for _, p := range s.profiles {
		if math.Abs(p.Start-start) <= 0.002*p.Start && math.Abs(p.Stop-stop) <= 0.002*p.Stop {
			cfg.Bands, cfg.Identify = p.Bands, p.Identify
			return cfg, p.ID
		}
	}
	centre := (start + stop) / 2
	cfg.Bands = bandsOverlapping(start, stop)
	if (stop-start)/centre >= 0.4 || len(cfg.Bands) > 2 {
		cfg.Bands, cfg.Identify = append([]rf.Band(nil), rf.LoRaBands...), true
	}
	return cfg, ""
}

// hFile opens a measurement file (POST /api/file {"name", "data"}): a
// Touchstone .s1p / .s2p or a CSV export. It becomes the current result,
// labelled with the file name, and live sweeping stops.
func (s *Server) hFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxFileBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, 400, fmt.Errorf("bad request (file too large?): %w", err))
		return
	}
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(req.Name), "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = "measurement"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	m, err := rf.ImportFile(name, []byte(req.Data))
	if err != nil {
		writeErr(w, 400, fmt.Errorf("%s: %w", name, err))
		return
	}
	s.mu.Lock()
	if s.tune != nil {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("close TUNE first, then open the file"))
		return
	}
	if s.compareOn {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("close COMPARE first, then open the file"))
		return
	}
	s.continuous.Store(false)
	now := time.Now()
	cfg, prof := s.fileConfigLocked(m.Freq, m.S21 != nil)
	cfg.Accept = s.settings.AcceptanceFor(prof)
	if prof == "" {
		cfg.Accept = s.settings.Accept
	}
	res := buildResult(m.Freq, m.S11, m.S21, cfg, now, 0)
	res.Source = &FileSource{File: name, Format: m.Format, Loaded: now, Z0: m.Z0, Notes: m.Notes, Profile: prof}
	s.seq++
	res.Seq = s.seq
	s.last = res
	s.lastByProfile["file"] = res
	if ref := s.refs[s.cfg.Profile]; ref != nil {
		res.Ref = s.refDeltaLocked(res, ref)
	}
	s.mu.Unlock()
	s.publishState()
	s.hub.publish("sweep", res)
	writeJSON(w, 200, res)
}

// fileLabel is how a file measurement is named in reports and sheets.
func fileLabel(src *FileSource) string { return "File " + src.File + " (not live)" }
