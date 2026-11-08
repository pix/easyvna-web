package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- BATCH HTTP API ----------

func (s *Server) batchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/batches", s.hBatchList)
	mux.HandleFunc("POST /api/batches", s.hBatchCreate)
	mux.HandleFunc("POST /api/batches/import", s.hBatchImport)
	mux.HandleFunc("POST /api/batch", s.hBatchControl)
	mux.HandleFunc("GET /api/batches/{id}", s.hBatchGet)
	mux.HandleFunc("PUT /api/batches/{id}", s.hBatchEdit)
	mux.HandleFunc("DELETE /api/batches/{id}", s.hBatchDelete)
	mux.HandleFunc("POST /api/batches/{id}/restore", s.hBatchRestore)
	mux.HandleFunc("POST /api/batches/{id}/capture", s.hBatchCapture)
	mux.HandleFunc("PUT /api/batches/{id}/antennas/{n}", s.hBatchAntennaEdit)
	mux.HandleFunc("DELETE /api/batches/{id}/antennas/{n}", s.hBatchAntennaDelete)
	mux.HandleFunc("POST /api/batches/{id}/undo", s.hBatchUndo)
	mux.HandleFunc("GET /api/batches/{id}/csv", s.hBatchCSV)
	mux.HandleFunc("GET /api/batches/{id}/json", s.hBatchJSON)
	mux.HandleFunc("POST /api/batches/{id}/sheet", s.hBatchSheet)
}

// loadBatchFor returns the batch id: the cached active one, or from the
// store. The caller must not hold s.mu.
func (s *Server) loadBatchFor(id string) (*Batch, error) {
	s.mu.Lock()
	if b := s.batch.cur; b != nil && b.ID == id {
		s.mu.Unlock()
		return b, nil
	}
	s.mu.Unlock()
	return s.store.LoadBatch(id)
}

func batchErr(w http.ResponseWriter, err error) {
	code := 400
	if errors.Is(err, fs.ErrNotExist) {
		code, err = 404, errors.New("no such batch")
	}
	writeErr(w, code, err)
}

// viewOf builds the view of b with the version and undo flag (s.mu not held).
func (s *Server) viewOf(b *Batch) BatchView {
	v := s.batchViewOf(b)
	s.mu.Lock()
	v.Version, v.HasUndo = s.batch.version, s.batch.undo[b.ID] != nil
	s.mu.Unlock()
	return v
}

func (s *Server) saveSettingsIf(set *Settings) {
	if set != nil {
		_ = s.store.SaveSettings(*set)
	}
}

func (s *Server) hBatchList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.ListBatches(s.acceptFor))
}

// hBatchCreate makes a batch on the current profile and activates it.
func (s *Server) hBatchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string  `json:"name"`
		Notes    string  `json:"notes"`
		LengthMm float64 `json:"lengthMm"`
		Trim     string  `json:"trim"`
		Link     string  `json:"link"`
		Photo    string  `json:"photo"`
		// Small print under the photo and under the link.
		PhotoCaption string `json:"photoCaption"`
		LinkNote     string `json:"linkNote"`
	}
	if err := readJSONMax(r, &req, maxPhotoBytes+(64<<10)); err != nil {
		writeErr(w, 400, err)
		return
	}
	now := time.Now()
	b := &Batch{ID: newBatchID(now), Name: req.Name, Notes: req.Notes, LengthMm: req.LengthMm, Trim: req.Trim, Link: req.Link, Photo: req.Photo,
		PhotoCaption: cleanSmallPrint(req.PhotoCaption), LinkNote: cleanSmallPrint(req.LinkNote), Created: now, Updated: now, NextID: 1, Antennas: []BatchAntenna{}}
	if err := b.clean(); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	if s.tune != nil {
		cfg = s.tune.Base
	}
	if cfg.Identify || len(cfg.Bands) == 0 {
		s.mu.Unlock()
		writeErr(w, 400, errors.New("a batch needs a band profile: tap 433, 869 or 915 first (Wide and Survey only identify antennas)"))
		return
	}
	b.Profile, b.Start, b.Stop, b.Points, b.Bands = cfg.Profile, cfg.Start, cfg.Stop, cfg.Points, append(b.Bands, cfg.Bands...)
	b.Target = cfg.Bands[0].Reference()
	b.Label = "Custom sweep"
	if p, ok := s.profile(cfg.Profile); ok {
		b.Label = p.Label + " MHz profile"
	}
	s.mu.Unlock()
	if err := s.store.SaveBatch(b); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	set := s.setActiveLocked(b)
	s.mu.Unlock()
	s.saveSettingsIf(set)
	s.publishState()
	writeJSON(w, 200, s.viewOf(b))
}

func (s *Server) hBatchGet(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	writeJSON(w, 200, s.viewOf(b))
}

// hBatchControl: {"active": id or ""} selects the batch BATCH works on;
// {"auto": bool} switches auto-capture.
func (s *Server) hBatchControl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Active *string `json:"active"`
		Auto   *bool   `json:"auto"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	var set *Settings
	msg := ""
	if req.Active != nil {
		var b *Batch
		if *req.Active != "" {
			var err error
			if b, err = s.loadBatchFor(*req.Active); err != nil {
				batchErr(w, err)
				return
			}
		}
		s.mu.Lock()
		set = s.setActiveLocked(b)
		s.mu.Unlock()
		msg = "no active batch"
		if b != nil {
			msg = "batch " + b.Name
		}
	}
	if req.Auto != nil {
		s.mu.Lock()
		bs := s.batch
		if bs.cur == nil {
			s.mu.Unlock()
			writeErr(w, 409, errors.New("open a batch first"))
			return
		}
		bs.auto = *req.Auto
		bs.arr = nil
		if bs.auto {
			bs.arr = newArrivalFor(s.hold.cfg, bs.cur)
			msg = "auto-capture on: connect each antenna, it is captured once steady"
		} else {
			msg = "auto-capture off"
		}
		bs.version++
		s.mu.Unlock()
	}
	s.saveSettingsIf(set)
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": msg, "state": s.state()})
}

// hBatchEdit renames a batch or changes its notes and trim settings.
func (s *Server) hBatchEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string  `json:"name"`
		Notes    *string  `json:"notes"`
		LengthMm *float64 `json:"lengthMm"`
		Trim     *string  `json:"trim"`
		Link     *string  `json:"link"`
		Photo    *string  `json:"photo"`
		// Small print under the photo and under the link.
		PhotoCaption *string `json:"photoCaption"`
		LinkNote     *string `json:"linkNote"`
	}
	if err := readJSONMax(r, &req, maxPhotoBytes+(64<<10)); err != nil {
		writeErr(w, 400, err)
		return
	}
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	s.mu.Lock()
	c := cloneBatch(b)
	if req.Name != nil {
		c.Name = *req.Name
	}
	if req.Notes != nil {
		c.Notes = *req.Notes
	}
	if req.LengthMm != nil {
		c.LengthMm = *req.LengthMm
	}
	if req.Trim != nil {
		c.Trim = *req.Trim
	}
	if req.Link != nil {
		c.Link = *req.Link
	}
	if req.Photo != nil {
		c.Photo = *req.Photo
	}
	if req.PhotoCaption != nil {
		c.PhotoCaption = cleanSmallPrint(*req.PhotoCaption)
	}
	if req.LinkNote != nil {
		c.LinkNote = cleanSmallPrint(*req.LinkNote)
	}
	if err := c.clean(); err != nil {
		s.mu.Unlock()
		writeErr(w, 400, err)
		return
	}
	c.Updated = time.Now()
	if err := s.store.SaveBatch(c); err != nil {
		s.mu.Unlock()
		writeErr(w, 500, err)
		return
	}
	s.batch.undo[b.ID] = cloneBatch(b)
	*b = *c
	s.batch.version++
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.viewOf(b))
}

// hBatchDelete deletes a batch; POST .../restore brings it back.
func (s *Server) hBatchDelete(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	if err := s.store.DeleteBatch(b.ID); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.batch.trash = cloneBatch(b)
	var set *Settings
	if s.batch.cur != nil && s.batch.cur.ID == b.ID {
		set = s.setActiveLocked(nil)
	}
	s.mu.Unlock()
	s.saveSettingsIf(set)
	s.publishState()
	writeJSON(w, 200, map[string]string{"message": "batch " + b.Name + " deleted"})
}

func (s *Server) hBatchRestore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	b := s.batch.trash
	if b == nil || b.ID != r.PathValue("id") {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("nothing to restore"))
		return
	}
	s.batch.trash = nil
	s.mu.Unlock()
	if err := s.store.SaveBatch(b); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	set := s.setActiveLocked(b)
	s.mu.Unlock()
	s.saveSettingsIf(set)
	s.publishState()
	writeJSON(w, 200, s.viewOf(b))
}

// hBatchCapture captures the antenna on the cable: a fresh sweep, checked
// against the batch's profile. {"replace": id} measures that antenna again
// (redo); {"label": "SN 42"} names a new one; {"adjust": id, "change":
// "cut 1 mm", "trimMm": 1} keeps the earlier captures and adds a new,
// adjusted version of the antenna capture id belongs to.
func (s *Server) hBatchCapture(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replace int     `json:"replace"`
		Label   string  `json:"label"`
		Adjust  int     `json:"adjust"`
		Change  string  `json:"change"`
		TrimMm  float64 `json:"trimMm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	s.mu.Lock()
	var set *Settings
	if s.batch.cur == nil || s.batch.cur.ID != b.ID {
		set = s.setActiveLocked(b)
	}
	if err := batchCheck(b, s.last); err != nil && s.last != nil && s.last.Tune != nil {
		s.mu.Unlock()
		writeErr(w, 409, err)
		return
	}
	s.mu.Unlock()
	s.saveSettingsIf(set)
	// Measure now, so the capture is the antenna connected at this moment.
	ctx, cancel := sweepCtx(r)
	res, serr := s.sweepOnce(ctx)
	cancel()
	if serr != nil {
		writeErr(w, http.StatusServiceUnavailable, serr)
		return
	}
	s.mu.Lock()
	var a *BatchAntenna
	if res.Batch != nil && res.Batch.Captured > 0 && req.Replace == 0 && s.batch.cur == b {
		// Auto-capture took this very sweep (as a new antenna): an adjusted
		// version is linked to its antenna instead.
		if req.Adjust > 0 {
			if _, err = b.linkVersion(res.Batch.Captured, req.Adjust, req.Change, req.TrimMm); err == nil {
				err = s.store.SaveBatch(b)
				s.batch.version++
			}
		}
		if err == nil {
			a = &b.Antennas[b.index(res.Batch.Captured)]
		}
	} else {
		a, err = s.captureLocked(b, res, nil, capture{Replace: req.Replace, Label: req.Label, Adjust: req.Adjust, Change: req.Change, TrimMm: req.TrimMm})
		if err == nil && s.batch.arr != nil && s.batch.cur == b {
			s.batch.arr.Captured(res.complexS11())
		}
	}
	s.mu.Unlock()
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	s.publishState()
	msg := fmt.Sprintf("%s captured", a.Label)
	switch {
	case req.Replace > 0:
		msg = fmt.Sprintf("%s measured again", a.Label)
	case req.Adjust > 0:
		msg = fmt.Sprintf("%s captured as an adjusted version", a.Label)
	}
	writeJSON(w, 200, map[string]any{"message": msg, "id": a.ID, "batch": s.viewOf(b)})
}

func antennaID(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return 0, errors.New("bad antenna number")
	}
	return n, nil
}

// hBatchAntennaEdit changes a capture: {"label": "SN 0042"} renames it;
// {"adjustedFrom": id} makes it an adjusted version of the antenna capture
// id belongs to (same physical antenna), {"adjustedFrom": 0} takes it out
// of its antenna's versions; {"change": "bent 10°", "trimMm": 0.5} says
// what was adjusted (with adjustedFrom, or on an adjusted version).
func (s *Server) hBatchAntennaEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label        *string  `json:"label"`
		AdjustedFrom *int     `json:"adjustedFrom"`
		Change       *string  `json:"change"`
		TrimMm       *float64 `json:"trimMm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	n, err := antennaID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	label := ""
	if req.Label != nil {
		if label = strings.TrimSpace(*req.Label); label == "" || len(label) > maxBatchLabel {
			writeErr(w, 400, fmt.Errorf("give the antenna a label (1-%d characters)", maxBatchLabel))
			return
		}
	}
	if req.Label == nil && req.AdjustedFrom == nil && req.Change == nil && req.TrimMm == nil {
		writeErr(w, 400, errors.New("nothing to change"))
		return
	}
	s.changeBatch(w, r, func(b *Batch) (string, error) {
		i := b.index(n)
		if i < 0 {
			return "", fmt.Errorf("no antenna %d in this batch", n)
		}
		var msgs []string
		if req.Label != nil {
			old := b.Antennas[i].Label
			b.Antennas[i].Label = label
			msgs = append(msgs, fmt.Sprintf("%s renamed %s", old, label))
		}
		change, trim := b.Antennas[i].Change, b.Antennas[i].TrimMm
		if req.Change != nil {
			change = *req.Change
		}
		if req.TrimMm != nil {
			trim = *req.TrimMm
		}
		switch {
		case req.AdjustedFrom != nil && *req.AdjustedFrom > 0:
			m, err := b.linkVersion(n, *req.AdjustedFrom, change, trim)
			if err != nil {
				return "", err
			}
			msgs = append(msgs, m)
		case req.AdjustedFrom != nil:
			m, err := b.unlinkVersion(n)
			if err != nil {
				return "", err
			}
			msgs = append(msgs, m)
		case req.Change != nil || req.TrimMm != nil:
			a := &b.Antennas[i]
			if a.AdjustedFrom == 0 {
				return "", fmt.Errorf("%s is not an adjusted version: link it to its antenna first", a.Label)
			}
			c, t, err := cleanAdjust(change, trim)
			if err != nil {
				return "", err
			}
			a.Change, a.TrimMm = c, t
			msgs = append(msgs, a.Label+": adjustment saved")
		}
		return strings.Join(msgs, "; "), nil
	})
}

// hBatchAntennaDelete removes an antenna (undo with POST .../undo). Its
// later version, if any, follows the version before it.
func (s *Server) hBatchAntennaDelete(w http.ResponseWriter, r *http.Request) {
	n, err := antennaID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.changeBatch(w, r, func(b *Batch) (string, error) { return b.removeCapture(n) })
}

// changeBatch applies f to a copy of the batch, saves it and keeps the
// previous version for undo.
func (s *Server) changeBatch(w http.ResponseWriter, r *http.Request, f func(b *Batch) (string, error)) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	s.mu.Lock()
	c := cloneBatch(b)
	msg, err := f(c)
	if err != nil {
		s.mu.Unlock()
		code := 409
		if strings.HasPrefix(err.Error(), "no antenna") {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	c.Updated = time.Now()
	if err := s.store.SaveBatch(c); err != nil {
		s.mu.Unlock()
		writeErr(w, 500, err)
		return
	}
	s.batch.undo[b.ID] = cloneBatch(b)
	*b = *c
	s.batch.version++
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": msg, "batch": s.viewOf(b)})
}

// hBatchUndo restores the batch as it was before its last change.
func (s *Server) hBatchUndo(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	s.mu.Lock()
	prev := s.batch.undo[b.ID]
	if prev == nil {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("nothing to undo"))
		return
	}
	if err := s.store.SaveBatch(prev); err != nil {
		s.mu.Unlock()
		writeErr(w, 500, err)
		return
	}
	delete(s.batch.undo, b.ID)
	*b = *prev
	s.batch.version++
	if s.batch.arr != nil && s.batch.cur == b {
		s.batch.arr = newArrivalFor(s.hold.cfg, b)
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": "restored", "batch": s.viewOf(b)})
}

// ---------- export / import ----------

// BatchFormat identifies a batch export.
const BatchFormat = "easyvna-web-batch"

// BatchExport is the JSON download of a batch: the batch with every sweep
// (what import reads back), and the computed figures for convenience.
type BatchExport struct {
	Format   string    `json:"format"`
	Version  int       `json:"version"`
	Exported time.Time `json:"exported"`
	App      string    `json:"app,omitempty"`
	Batch    *Batch    `json:"batch"`
	Figures  *struct {
		Summary  BatchSummary       `json:"summary"`
		Antennas []BatchAntennaView `json:"antennas"`
	} `json:"figures,omitempty"`
}

func attachmentName(name, suffix, ext string) string {
	f, err := SnapshotFile(name + suffix)
	if err != nil {
		f = "batch" + suffix + ".json"
	}
	return strings.TrimSuffix(f, ".json") + ext
}

func (s *Server) hBatchJSON(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	s.mu.Lock()
	c := cloneBatch(b)
	s.mu.Unlock()
	v := s.batchViewOf(c)
	ex := BatchExport{Format: BatchFormat, Version: 1, Exported: time.Now().UTC(), App: s.opt.AppName, Batch: c}
	ex.Figures = &struct {
		Summary  BatchSummary       `json:"summary"`
		Antennas []BatchAntennaView `json:"antennas"`
	}{v.Summary, v.Antennas}
	for i := range ex.Figures.Antennas {
		ex.Figures.Antennas[i].SWR = nil
	}
	data, err := json.MarshalIndent(ex, "", " ")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, attachmentName(b.Name, "-batch", ".json")))
	_, _ = w.Write(data)
}

// validate checks an imported batch (it never trusts the file).
func (b *Batch) validate() error {
	if err := b.clean(); err != nil {
		return err
	}
	b.PhotoCaption, b.LinkNote = cleanSmallPrint(b.PhotoCaption), cleanSmallPrint(b.LinkNote)
	if b.Profile == "" || b.Start <= 0 || b.Stop <= b.Start || b.Points < 3 || b.Points > 10001 || b.Target <= 0 || len(b.Bands) == 0 {
		return errors.New("the batch has no valid sweep range, target or band")
	}
	if len(b.Antennas) > maxBatchAntennas {
		return fmt.Errorf("too many antennas (%d at most)", maxBatchAntennas)
	}
	ok := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if len(b.Antennas) > 0 && len(b.Freq) != b.Points {
		return errors.New("the batch's frequency points do not match its point count")
	}
	for i, f := range b.Freq {
		if !ok(f) || (i > 0 && f <= b.Freq[i-1]) {
			return errors.New("the batch's frequency points are not increasing")
		}
	}
	seen := map[int]bool{}
	for i := range b.Antennas {
		a := &b.Antennas[i]
		if a.ID < 1 || seen[a.ID] {
			return errors.New("antenna numbers must be unique and positive")
		}
		seen[a.ID] = true
		if a.ID >= b.NextID {
			b.NextID = a.ID + 1
		}
		a.Label = truncate(strings.TrimSpace(a.Label), maxBatchLabel)
		if a.Label == "" {
			a.Label = fmt.Sprintf("#%d", a.ID)
		}
		if len(a.S11) != len(b.Freq) {
			return fmt.Errorf("antenna %s: %d points, the batch has %d", a.Label, len(a.S11), len(b.Freq))
		}
		for _, g := range a.S11 {
			if !ok(g[0]) || !ok(g[1]) || math.Abs(g[0]) > 10 || math.Abs(g[1]) > 10 {
				return fmt.Errorf("antenna %s: bad S11 value", a.Label)
			}
		}
		var err error
		if a.Change, a.TrimMm, err = cleanAdjust(a.Change, a.TrimMm); err != nil {
			return fmt.Errorf("antenna %s: %w", a.Label, err)
		}
		if a.AdjustedFrom < 0 {
			return fmt.Errorf("antenna %s: bad adjustedFrom", a.Label)
		}
	}
	return b.checkChains()
}

// hBatchImport reads a batch export (or a bare batch) back. A batch with
// the same id as a stored one gets a new id.
func (s *Server) hBatchImport(w http.ResponseWriter, r *http.Request) {
	body, err := readAll(r, 32<<20)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	var ex BatchExport
	if err := json.Unmarshal(body, &ex); err != nil {
		writeErr(w, 400, fmt.Errorf("not an EasyVNA Web batch: %w", err))
		return
	}
	b := ex.Batch
	if ex.Format != BatchFormat || b == nil {
		writeErr(w, 400, errors.New("not an EasyVNA Web batch (format field missing)"))
		return
	}
	if ex.Version != 1 {
		writeErr(w, 400, fmt.Errorf("batch version %d is not supported by this version", ex.Version))
		return
	}
	if b.Antennas == nil {
		b.Antennas = []BatchAntenna{}
	}
	if err := b.validate(); err != nil {
		writeErr(w, 400, fmt.Errorf("cannot import this batch: %w", err))
		return
	}
	if _, err := batchPath(b.ID); err != nil || s.batchExists(b.ID) {
		b.ID = newBatchID(time.Now())
	}
	if b.Created.IsZero() {
		b.Created = time.Now()
	}
	b.Updated = time.Now()
	if err := s.store.SaveBatch(b); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	set := s.setActiveLocked(b)
	s.mu.Unlock()
	s.saveSettingsIf(set)
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": fmt.Sprintf("imported %s (%d antennas)", b.Name, len(b.Antennas)), "batch": s.viewOf(b)})
}

func (s *Server) batchExists(id string) bool {
	p, err := batchPath(id)
	if err != nil {
		return false
	}
	_, err = s.store.b.Stat(p)
	return err == nil
}

// hBatchCSV is a spreadsheet: one row per antenna, then the statistics.
func (s *Server) hBatchCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.loadBatchFor(r.PathValue("id"))
	if err != nil {
		batchErr(w, err)
		return
	}
	s.mu.Lock()
	c := cloneBatch(b)
	s.mu.Unlock()
	v := s.batchViewOf(c)
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	f := func(x float64, prec int) string {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return ""
		}
		return strconv.FormatFloat(x, 'f', prec, 64)
	}
	fz := func(x float64, prec int, ok bool) string {
		if !ok {
			return ""
		}
		return f(x, prec)
	}
	_ = cw.Write([]string{"batch", c.Name, "profile", c.Profile, "range", mhzRange(c.Start, c.Stop), "target_mhz", f(c.Target/1e6, 3),
		"tolerance_mhz", f(v.Summary.ToleranceHz/1e6, 3), "acceptance", v.Summary.Window.Text, "grade", v.Summary.Grade,
		"antennas", strconv.Itoa(v.Summary.N), "captures", strconv.Itoa(v.Summary.Captures)})
	_ = cw.Write([]string{"#", "label", "captured", "status", "advice", "resonance_mhz", "offset_mhz", "offset_pct", "swr_at_resonance",
		"swr_at_target", "worst_swr_in_band", "band_swr2_pct", "bw_swr2_mhz", "return_loss_at_target_db", "band_verdict", "cut_mm",
		"outlier_of", "calibration", "auto", "warnings", "band_below_limit_pct", "margin_used_pct", "measured_n", "resonance_sd_mhz",
		"no", "version", "current", "adjusted_from", "change", "trim_mm", "delta_resonance_mhz"})
	for _, a := range v.Antennas {
		m := a.Metrics
		verdict := strings.ToUpper(m.Verdict)
		if verdict == "N/A" {
			verdict = ""
		}
		_ = cw.Write([]string{strconv.Itoa(a.ID), a.Label, a.Time.Format(time.RFC3339), m.Tuning.Status, strings.TrimLeft(m.Tuning.Text, "✓↓↑✗ "),
			fz(m.ResFreq/1e6, 4, m.Found), fz(m.Offset/1e6, 4, m.Found), fz(m.OffsetPct, 3, m.Found), fz(m.ResSWR, 3, m.Found),
			f(m.SWRTarget, 3), fz(m.WorstSWR, 3, m.WorstSWR > 0), f(m.PctBelow2, 1), fz(m.BW2/1e6, 3, m.BW2 > 0 && !m.BW2Open),
			f(m.RLTarget, 2), verdict, fz(m.Tuning.CutMm, 2, m.Tuning.Method != "pct" && m.Found), strings.Join(m.Outliers, " "),
			a.Cal, strconv.FormatBool(a.Auto), strings.Join(a.Warnings, " "), f(m.PctBelowLimit, 1),
			fz(m.Margin.UsedPct, 1, m.Margin.OK), repN(a.Rep), repSD(a.Rep),
			a.No, strconv.Itoa(a.Ver), strconv.FormatBool(a.Current), fz(float64(a.AdjustedFrom), 0, a.AdjustedFrom > 0), a.Change,
			fz(a.TrimMm, 2, a.TrimMm != 0), fz(stepDRes(a.Step), 4, a.Step != nil && a.Step.Found)})
	}
	_ = cw.Write(nil)
	_ = cw.Write([]string{"statistic", "unit", "n", "mean", "sd", "min", "max", "spread", "median", "outliers"})
	for _, st := range v.Summary.Stats {
		var ids []string
		for _, id := range st.Outliers {
			ids = append(ids, "#"+strconv.Itoa(id))
		}
		p := st.Dec + 2
		_ = cw.Write([]string{st.Label, st.Unit, strconv.Itoa(st.N), fz(st.Mean, p, st.N > 0), fz(st.SD, p, st.N > 1), fz(st.Min, p, st.N > 0),
			fz(st.Max, p, st.N > 0), fz(st.Spread, p, st.N > 0), fz(st.Median, p, st.N > 0), strings.Join(ids, " ")})
	}
	_ = cw.Write(nil)
	sm := v.Summary
	_ = cw.Write([]string{"lot", fmt.Sprintf("%d antennas, %d captures: the statistics use the current version of each antenna", sm.N, sm.Captures)})
	for _, st := range sm.Steps {
		_ = cw.Write([]string{"adjustment", st.Text})
	}
	if sm.Learnt != nil {
		_ = cw.Write([]string{"learnt", sm.Learnt.Text, f(sm.Learnt.MHzPerMm, 3)})
	}
	_ = cw.Write([]string{"tuning", sm.TuningLine})
	_ = cw.Write([]string{"yield", fmt.Sprintf("good %d, ok %d, bad %d", sm.Yield["good"], sm.Yield["ok"], sm.Yield["bad"])})
	for _, l := range sm.Conclusion {
		_ = cw.Write([]string{"conclusion", l})
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, attachmentName(b.Name, "-batch", ".csv")))
	_, _ = w.Write(buf.Bytes())
}

func stepDRes(st *AdjustStep) float64 {
	if st == nil {
		return 0
	}
	return st.DRes / 1e6
}

func repN(r *RepeatBrief) string {
	if r == nil {
		return "1"
	}
	return strconv.Itoa(r.N)
}

func repSD(r *RepeatBrief) string {
	if r == nil {
		return ""
	}
	return strconv.FormatFloat(r.ResSD/1e6, 'f', 4, 64)
}

// newArrivalFor starts auto-capture on b, remembering its last capture.
func newArrivalFor(cfg rf.HoldConfig, b *Batch) *rf.Arrival {
	return rf.NewArrival(cfg, lastGamma(b))
}
