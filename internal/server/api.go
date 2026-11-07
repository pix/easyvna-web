package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
	"github.com/pix/easyvna-web/internal/vna"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", s.hState)
	mux.HandleFunc("GET /api/events", s.hEvents)
	mux.HandleFunc("POST /api/connect", s.hConnect)
	mux.HandleFunc("POST /api/profile", s.hProfile)
	mux.HandleFunc("PUT /api/profiles/{id}/slot", s.hProfileSlot)
	mux.HandleFunc("POST /api/config", s.hConfig)
	mux.HandleFunc("POST /api/settings", s.hSettings)
	mux.HandleFunc("POST /api/sweep", s.hSweep)
	mux.HandleFunc("POST /api/continuous", s.hContinuous)
	mux.HandleFunc("GET /api/last", s.hLast)
	mux.HandleFunc("GET /api/export", s.hExport)
	mux.HandleFunc("GET /api/snapshots", s.hSnapList)
	mux.HandleFunc("POST /api/snapshots", s.hSnapSave)
	mux.HandleFunc("GET /api/snapshots/{name}", s.hSnapGet)
	mux.HandleFunc("DELETE /api/snapshots/{name}", s.hSnapDelete)
	mux.HandleFunc("GET /api/cal", s.hCalStatus)
	mux.HandleFunc("POST /api/cal/step/{step}", s.hCalStep)
	mux.HandleFunc("POST /api/cal/save", s.hCalSave)
	mux.HandleFunc("POST /api/cal/recall", s.hCalRecall)
	mux.HandleFunc("POST /api/avg", s.hAvg)
	mux.HandleFunc("POST /api/tune", s.hTune)
	mux.HandleFunc("POST /api/ref", s.hRefSet)
	mux.HandleFunc("DELETE /api/ref", s.hRefClear)
	mux.HandleFunc("POST /api/ref/undo", s.hRefUndo)
	mux.HandleFunc("GET /api/ref", s.hRefGet)
	mux.HandleFunc("GET /api/trim", s.hTrimList)
	mux.HandleFunc("GET /api/trim/{name}", s.hTrimGet)
	mux.HandleFunc("POST /api/trim/{name}/log", s.hTrimLog)
	mux.HandleFunc("POST /api/trim/{name}/undo", s.hTrimUndo)
	mux.HandleFunc("POST /api/trim/{name}/target", s.hTrimTarget)
	mux.HandleFunc("POST /api/trim/{name}/select", s.hTrimSelect)
	mux.HandleFunc("POST /api/compare", s.hCompareStart)
	mux.HandleFunc("POST /api/compare/a", s.hCompareStoreA)
	mux.HandleFunc("POST /api/compare/add", s.hCompareAdd)
	mux.HandleFunc("POST /api/compare/undo", s.hCompareUndo)
	mux.HandleFunc("DELETE /api/compare", s.hCompareReset)
	mux.HandleFunc("POST /api/report", s.hReport)
	mux.HandleFunc("GET /api/reports", s.hReportList)
	mux.HandleFunc("GET /api/reports/{file}", s.hReportGet)
	mux.HandleFunc("POST /api/mock/antenna", s.hMockAntenna)
	mux.HandleFunc("POST /api/hold", s.hHold)
	mux.HandleFunc("GET /api/hold", s.hHoldGet)
	if s.opt.Backup {
		mux.HandleFunc("GET /api/backup", s.hBackupGet)
		mux.HandleFunc("POST /api/backup", s.hBackupPost)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad JSON: %w", err)
	}
	return nil
}

// readAll reads a request body of at most limit bytes.
func readAll(r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("reading request: %w", err)
	}
	return b, nil
}

func (s *Server) hState(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.state()) }

func (s *Server) hEvents(w http.ResponseWriter, r *http.Request) {
	init := []event{}
	if b, err := json.Marshal(s.state()); err == nil {
		init = append(init, event{"state", b})
	}
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last != nil {
		if b, err := json.Marshal(last); err == nil {
			init = append(init, event{"sweep", b})
		}
	}
	s.hub.serveSSE(w, r, init)
}

func (s *Server) hConnect(w http.ResponseWriter, r *http.Request) {
	s.opMu.Lock()
	s.mu.Lock()
	if s.dev != nil {
		_ = s.dev.Close()
		s.dev, s.info = nil, nil
	}
	s.mu.Unlock()
	_, err := s.device(r.Context())
	s.opMu.Unlock()
	s.publishState()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, 200, s.state())
}

// hProfile switches to a quick profile and, if enabled and known, recalls
// the calibration slot saved for it.
func (s *Server) hProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ID == "custom" {
		s.hCustomProfile(w, r)
		return
	}
	s.mu.Lock()
	p, ok := s.profile(req.ID)
	if !ok {
		s.mu.Unlock()
		writeErr(w, 404, fmt.Errorf("unknown profile %q", req.ID))
		return
	}
	if s.tune != nil { // switching profile ends TUNE
		s.continuous.Store(s.tune.WasContinuous)
		s.tune = nil
	}
	s.applyProfileLocked(p)
	meta, haveMeta := s.settings.Slots[p.CalSlot]
	needRecall := s.settings.AutoRecall && haveMeta && meta.Start == p.Start && meta.Stop == p.Stop &&
		(s.cal.Active == nil || s.cal.Active.Start != p.Start || s.cal.Active.Stop != p.Stop)
	useDevice := s.devProfs != nil && s.deviceHasProfileLocked(p.ID) && !s.opt.ReadOnly
	if s.opt.ReadOnly {
		needRecall = false
	}
	s.mu.Unlock()
	msg := ""
	switch {
	case useDevice:
		// Custom firmware: let the device switch range and recall its slot,
		// so the on-device profile and the web UI agree.
		msg = s.applyDeviceProfile(r.Context(), p)
	case needRecall:
		if err := s.recall(r.Context(), p.CalSlot); err != nil {
			msg = "auto-recall failed: " + err.Error()
		} else {
			msg = fmt.Sprintf("recalled calibration slot %d", p.CalSlot)
		}
	}
	s.publishState()
	writeJSON(w, 200, map[string]any{"state": s.state(), "message": msg})
}

func (s *Server) hProfileSlot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slot int `json:"slot"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	max := 7
	if s.info != nil && s.info.CalSlots > 0 {
		max = s.info.CalSlots
	}
	if req.Slot < 0 || req.Slot >= max {
		s.mu.Unlock()
		writeErr(w, 400, fmt.Errorf("slot must be 0..%d", max-1))
		return
	}
	found := false
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			s.profiles[i].CalSlot, found = req.Slot, true
		}
	}
	if found {
		s.settings.ProfileSlots[id] = req.Slot
	}
	set := s.settings
	s.mu.Unlock()
	if !found {
		writeErr(w, 404, fmt.Errorf("unknown profile %q", id))
		return
	}
	if err := s.store.SaveSettings(set); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.publishState()
	writeJSON(w, 200, s.state())
}

// hConfig sets a custom (advanced) sweep. Missing fields keep their values.
func (s *Server) hConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Start     *float64 `json:"start"`
		Stop      *float64 `json:"stop"`
		Points    *int     `json:"points"`
		S21       *bool    `json:"s21"`
		PortExtPs *float64 `json:"portExtPs"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	c := s.cfg
	rangeChanged := false
	if req.Start != nil && *req.Start != c.Start {
		c.Start, rangeChanged = *req.Start, true
	}
	if req.Stop != nil && *req.Stop != c.Stop {
		c.Stop, rangeChanged = *req.Stop, true
	}
	if req.Points != nil && *req.Points != c.Points {
		c.Points, rangeChanged = *req.Points, true
	}
	if req.S21 != nil {
		c.S21 = *req.S21
	}
	if req.PortExtPs != nil {
		if *req.PortExtPs < -1e5 || *req.PortExtPs > 1e5 {
			s.mu.Unlock()
			writeErr(w, 400, errors.New("port extension out of range (±100 ns)"))
			return
		}
		c.PortExtPs = *req.PortExtPs
	}
	info := vna.Info{}
	if s.info != nil {
		info = *s.info
	}
	if err := c.scanRequest().Validate(info); err != nil {
		s.mu.Unlock()
		writeErr(w, 400, err)
		return
	}
	if rangeChanged {
		c.Profile, c.Identify = "custom", false
		c.Bands = bandsOverlapping(c.Start, c.Stop)
	}
	s.cfg = c
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state())
}

func (s *Server) hSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoRecall *bool `json:"autoRecall"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	if req.AutoRecall != nil {
		s.settings.AutoRecall = *req.AutoRecall
	}
	set := s.settings
	s.mu.Unlock()
	if err := s.store.SaveSettings(set); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.publishState()
	writeJSON(w, 200, s.state())
}

func (s *Server) hSweep(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := s.sweepOnce(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) hContinuous(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.continuous.Store(req.On)
	if req.On {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	s.publishState()
	writeJSON(w, 200, s.state())
}

func (s *Server) hLast(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last == nil {
		writeErr(w, 404, errors.New("no sweep yet"))
		return
	}
	writeJSON(w, 200, last)
}

// hExport serves ?format=s1p|s2p|csv of the last sweep or ?snapshot=name.
func (s *Server) hExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var res *Result
	if name := q.Get("snapshot"); name != "" {
		var err error
		if res, err = s.store.LoadSnapshot(name); err != nil {
			writeErr(w, 404, err)
			return
		}
	} else {
		s.mu.Lock()
		res = s.last
		s.mu.Unlock()
		if res == nil {
			writeErr(w, 404, errors.New("no sweep yet"))
			return
		}
	}
	format := strings.ToLower(q.Get("format"))
	var buf bytes.Buffer
	comments := []string{
		"Exported by EasyVNA Web " + time.Now().Format(time.RFC3339),
		fmt.Sprintf("Measured %s, profile %s, %d points", res.Time.Format(time.RFC3339), res.Config.Profile, len(res.Freq)),
		fmt.Sprintf("Calibration applied: %v; port extension %.1f ps", res.CalApplied, res.Config.PortExtPs),
	}
	if res.Name != "" {
		comments = append(comments, "Snapshot: "+res.Name)
	}
	var err error
	switch format {
	case "s1p", "":
		format = "s1p"
		err = rf.WriteTouchstone(&buf, res.Freq, res.complexS11(), nil, comments)
	case "s2p":
		if len(res.S21) == 0 {
			writeErr(w, 400, errors.New("no S21 in this sweep (enable S21 in Advanced)"))
			return
		}
		err = rf.WriteTouchstone(&buf, res.Freq, res.complexS11(), res.complexS21(), comments)
	case "csv":
		err = rf.WriteCSV(&buf, res.Freq, res.complexS11(), res.complexS21())
	default:
		writeErr(w, 400, fmt.Errorf("unknown format %q", format))
		return
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	base := "easyvna-" + res.Config.Profile + "-" + res.Time.Format("20060102-150405")
	if res.Name != "" {
		f, _ := SnapshotFile(res.Name)
		base = strings.TrimSuffix(f, ".json")
	}
	ctype := "text/plain; charset=utf-8"
	if format == "csv" {
		ctype = "text/csv; charset=utf-8"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, base, format))
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) hSnapList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.ListSnapshots())
}

func (s *Server) hSnapSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last == nil {
		writeErr(w, 409, errors.New("no sweep to save yet"))
		return
	}
	info, err := s.store.SaveSnapshot(req.Name, last)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.hub.publish("snapshots", s.store.ListSnapshots())
	writeJSON(w, 200, info)
}

func (s *Server) hSnapGet(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.LoadSnapshot(r.PathValue("name"))
	if err != nil {
		code := 400
		if errors.Is(err, fs.ErrNotExist) {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) hSnapDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSnapshot(r.PathValue("name")); err != nil {
		code := 400
		if errors.Is(err, os.ErrNotExist) {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	s.hub.publish("snapshots", s.store.ListSnapshots())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) hCalStatus(w http.ResponseWriter, r *http.Request) {
	s.opMu.Lock()
	dev, err := s.device(r.Context())
	if err == nil {
		s.refreshCal(r.Context(), dev)
	}
	s.opMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, 200, s.state().Cal)
}

// hCalStep runs one calibration step over the current sweep range.
// Body: {"fresh": true} on OPEN starts a new flow with `cal reset`.
func (s *Server) hCalStep(w http.ResponseWriter, r *http.Request) {
	if s.opt.ReadOnly {
		writeErr(w, http.StatusForbidden, errReadOnly)
		return
	}
	step, err := vna.ParseCalStep(r.PathValue("step"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	var req struct {
		Fresh bool `json:"fresh"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	// Calibration needs the device to itself.
	s.continuous.Store(false)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	dev, err := s.device(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	rng := cfg.scanRequest()
	if req.Fresh || step == vna.CalReset || s.cal.Flow.Profile != cfg.Profile ||
		s.cal.Flow.Start != cfg.Start || s.cal.Flow.Stop != cfg.Stop {
		s.cal.Flow = CalFlow{Profile: cfg.Profile, Start: cfg.Start, Stop: cfg.Stop, Steps: map[string]string{}}
		req.Fresh = true
	}
	s.cal.Flow.Steps[string(step)] = "running"
	s.cal.Flow.Message = fmt.Sprintf("measuring %s…", strings.ToUpper(string(step)))
	s.mu.Unlock()
	s.setBusy("cal " + string(step))
	defer s.setBusy("")

	if req.Fresh && step != vna.CalReset {
		if err := dev.Calibrate(ctx, vna.CalReset, &rng); err != nil {
			s.calFail(step, err)
			writeErr(w, 500, err)
			return
		}
		s.mu.Lock()
		s.cal.Active = nil
		s.mu.Unlock()
	}
	if err := dev.Calibrate(ctx, step, &rng); err != nil {
		s.calFail(step, err)
		writeErr(w, 500, err)
		return
	}
	s.refreshCal(ctx, dev)
	s.mu.Lock()
	st := s.cal.Status
	ok := true
	switch step {
	case vna.CalOpen:
		ok = st.Open || !st.Known
	case vna.CalShort:
		ok = st.Short || !st.Known
	case vna.CalLoad:
		ok = st.Load || !st.Known
	case vna.CalThru:
		ok = st.Thru || !st.Known
	case vna.CalIsoln:
		ok = st.Isoln || !st.Known
	case vna.CalDone:
		ok = st.Applied || !st.Known
		if ok {
			s.cal.Active = &CalRange{Start: cfg.Start, Stop: cfg.Stop, Points: cfg.Points, Source: "fresh (not saved)"}
		}
	case vna.CalReset:
		s.cal.Active = nil
	case vna.CalOff, vna.CalOn:
	}
	if ok {
		s.cal.Flow.Steps[string(step)] = "done"
		s.cal.Flow.Message = strings.ToUpper(string(step)) + " done"
	} else {
		s.cal.Flow.Steps[string(step)] = "error"
		s.cal.Flow.Message = strings.ToUpper(string(step)) + ": firmware did not report it as measured"
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, s.state().Cal)
}

func (s *Server) calFail(step vna.CalStep, err error) {
	s.mu.Lock()
	s.cal.Flow.Steps[string(step)] = "error"
	s.cal.Flow.Message = fmt.Sprintf("%s failed: %v", strings.ToUpper(string(step)), err)
	s.mu.Unlock()
	s.handleDevErr(err)
}

func (s *Server) hCalSave(w http.ResponseWriter, r *http.Request) {
	if s.opt.ReadOnly {
		writeErr(w, http.StatusForbidden, errReadOnly)
		return
	}
	var req struct {
		Slot *int `json:"slot"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.continuous.Store(false)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	dev, err := s.device(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	slot := CustomSlot
	if p, ok := s.profile(cfg.Profile); ok {
		slot = p.CalSlot
	}
	if req.Slot != nil {
		slot = *req.Slot
	}
	s.mu.Unlock()
	if err := dev.SaveCal(r.Context(), slot); err != nil {
		s.handleDevErr(err)
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	rng := CalRange{Start: cfg.Start, Stop: cfg.Stop, Points: cfg.Points}
	if s.cal.Active != nil {
		rng = *s.cal.Active
	}
	s.settings.Slots[slot] = SlotMeta{Profile: cfg.Profile, Start: rng.Start, Stop: rng.Stop, Points: rng.Points, Saved: time.Now()}
	rng.Source, rng.Slot = fmt.Sprintf("slot %d", slot), &slot
	s.cal.Active = &rng
	s.cal.Flow.Steps["save"] = "done"
	s.cal.Flow.Message = fmt.Sprintf("saved to slot %d", slot)
	set := s.settings
	s.mu.Unlock()
	if err := s.store.SaveSettings(set); err != nil {
		s.log.Printf("settings: %v", err)
	}
	s.syncDeviceProfiles(r.Context(), dev)
	s.publishState()
	writeJSON(w, 200, s.state().Cal)
}

func (s *Server) hCalRecall(w http.ResponseWriter, r *http.Request) {
	if s.opt.ReadOnly {
		writeErr(w, http.StatusForbidden, errReadOnly)
		return
	}
	var req struct {
		Slot int `json:"slot"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.continuous.Store(false)
	if err := s.recall(r.Context(), req.Slot); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.publishState()
	writeJSON(w, 200, s.state().Cal)
}

var errReadOnly = errors.New("read-only mode: calibration, save and recall are disabled (restart without -readonly)")

// deviceHasProfileLocked reports whether the firmware lists profile id
// (e.g. "survey" only exists on newer PiX firmware).
func (s *Server) deviceHasProfileLocked(id string) bool {
	if s.devProfs == nil {
		return false
	}
	for _, p := range s.devProfs.Profiles {
		if p.Name == id {
			return true
		}
	}
	return false
}

// hCustomProfile restores the custom range saved with the custom slot: on
// PiX firmware via `profile custom`, otherwise from what EasyVNA Web recorded
// when it saved that slot (then recalled if auto-recall is on).
func (s *Server) hCustomProfile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	useDevice := s.devProfs != nil && s.devProfs.Custom != nil && !s.opt.ReadOnly
	meta, haveMeta := s.settings.Slots[CustomSlot]
	auto := s.settings.AutoRecall && !s.opt.ReadOnly
	s.mu.Unlock()
	var start, stop float64
	points := 401
	msg := ""
	switch {
	case useDevice:
		s.opMu.Lock()
		dev, err := s.device(r.Context())
		var dp vna.DeviceProfile
		if err == nil {
			dp, err = dev.(vna.Profiler).ApplyProfile(r.Context(), "custom")
		}
		if err == nil {
			s.refreshCal(r.Context(), dev)
			s.syncDeviceProfiles(r.Context(), dev)
		}
		s.opMu.Unlock()
		if err != nil {
			s.handleDevErr(err)
			writeErr(w, 500, err)
			return
		}
		if !dp.Cal || dp.Stop <= dp.Start {
			writeErr(w, 404, fmt.Errorf("no custom range saved in slot %d yet: set one in Advanced, calibrate and save", dp.Slot))
			return
		}
		start, stop = dp.Start, dp.Stop
		if dp.Points > 1 {
			points = dp.Points
		}
		sl := dp.Slot
		s.mu.Lock()
		s.cal.Active = &CalRange{Start: start, Stop: stop, Points: points, Source: fmt.Sprintf("slot %d (device custom)", sl), Slot: &sl}
		s.mu.Unlock()
		msg = fmt.Sprintf("device custom profile applied (%s, slot %d)", mhzRange(start, stop), sl)
	case haveMeta:
		start, stop = meta.Start, meta.Stop
		if meta.Points > 1 {
			points = meta.Points
		}
		if auto {
			if err := s.recall(r.Context(), CustomSlot); err != nil {
				msg = "recall failed: " + err.Error()
			} else {
				msg = fmt.Sprintf("recalled custom slot %d (%s)", CustomSlot, mhzRange(start, stop))
			}
		}
	default:
		writeErr(w, 404, fmt.Errorf("no custom range saved in slot %d yet", CustomSlot))
		return
	}
	s.mu.Lock()
	pe, s21 := s.cfg.PortExtPs, s.cfg.S21
	s.cfg = Config{Profile: "custom", Start: start, Stop: stop, Points: points, S21: s21, PortExtPs: pe,
		Bands: bandsOverlapping(start, stop)}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"state": s.state(), "message": msg})
}

// applyDeviceProfile runs `profile <id>` on custom firmware and records the
// calibration that came with it. Returns a message for the UI.
func (s *Server) applyDeviceProfile(ctx context.Context, p Profile) string {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	dev, err := s.device(ctx)
	if err != nil {
		return err.Error()
	}
	pr, ok := dev.(vna.Profiler)
	if !ok || !pr.SupportsProfiles() {
		return ""
	}
	dp, err := pr.ApplyProfile(ctx, p.ID)
	if err != nil {
		s.handleDevErr(err)
		return "device profile failed: " + err.Error()
	}
	s.refreshCal(ctx, dev)
	s.syncDeviceProfiles(ctx, dev)
	s.mu.Lock()
	defer s.mu.Unlock()
	if dp.Cal {
		sl := dp.Slot
		s.cal.Active = &CalRange{Start: dp.Start, Stop: dp.Stop, Points: dp.Points, Source: fmt.Sprintf("slot %d (device profile)", sl), Slot: &sl}
		return fmt.Sprintf("device profile %s applied, calibration slot %d loaded", dp.Name, dp.Slot)
	}
	return fmt.Sprintf("device profile %s applied; slot %d has no calibration yet", dp.Name, dp.Slot)
}

// recall loads a slot and records its range (if EasyVNA Web saved it).
func (s *Server) recall(ctx context.Context, slot int) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	dev, err := s.device(ctx)
	if err != nil {
		return err
	}
	if err := dev.RecallCal(ctx, slot); err != nil {
		s.handleDevErr(err)
		return err
	}
	s.refreshCal(ctx, dev)
	s.mu.Lock()
	defer s.mu.Unlock()
	sl := slot
	if m, ok := s.settings.Slots[slot]; ok {
		s.cal.Active = &CalRange{Start: m.Start, Stop: m.Stop, Points: m.Points, Source: fmt.Sprintf("slot %d", slot), Slot: &sl}
	} else {
		s.cal.Active = nil // range unknown: not saved by EasyVNA Web
	}
	s.cal.Flow.Message = fmt.Sprintf("recalled slot %d", slot)
	return nil
}
