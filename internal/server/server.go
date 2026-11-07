// Package server implements the EasyVNA Web HTTP API, the sweep loop and the
// server-sent event stream that feeds the embedded web UI.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
	"github.com/pix/easyvna-web/internal/vna"
	"github.com/pix/easyvna-web/internal/vna/nanovnah"
)

// Opener connects to a device (real or mock).
type Opener func(ctx context.Context) (vna.Device, error)

// Config is the active sweep configuration.
type Config struct {
	Profile   string    `json:"profile"` // profile id or "custom"
	Start     float64   `json:"start"`
	Stop      float64   `json:"stop"`
	Points    int       `json:"points"`
	S21       bool      `json:"s21"`
	Bands     []rf.Band `json:"bands"`
	Identify  bool      `json:"identify"`
	PortExtPs float64   `json:"portExtPs"` // one-way port extension delay, ps
	Avg       int       `json:"avg"`       // average N sweeps (1 = off)
}

func (c Config) scanRequest() vna.ScanRequest {
	return vna.ScanRequest{Start: c.Start, Stop: c.Stop, Points: c.Points, S21: c.S21}
}

// CalRange describes the range the active calibration was made for.
type CalRange struct {
	Start  float64 `json:"start"`
	Stop   float64 `json:"stop"`
	Points int     `json:"points"`
	Source string  `json:"source"` // "slot N" or "fresh (not saved)"
	Slot   *int    `json:"slot,omitempty"`
}

// CalFlow tracks the quick OPEN→SHORT→LOAD→DONE flow of this session.
type CalFlow struct {
	Profile string            `json:"profile"`
	Start   float64           `json:"start"`
	Stop    float64           `json:"stop"`
	Steps   map[string]string `json:"steps"` // step -> "running" | "done" | "error"
	Message string            `json:"message"`
}

// CalState is everything the UI shows about calibration.
type CalState struct {
	Status   vna.CalStatus `json:"status"`
	Active   *CalRange     `json:"active,omitempty"` // nil when unknown (e.g. boot-time slot)
	Matches  *bool         `json:"matches,omitempty"`
	Flow     CalFlow       `json:"flow"`
	Settings Settings      `json:"settings"`
}

// State is pushed to the UI on every change.
type State struct {
	Connected  bool      `json:"connected"`
	Info       *vna.Info `json:"info,omitempty"`
	Error      string    `json:"error,omitempty"`
	Continuous bool      `json:"continuous"`
	Busy       string    `json:"busy,omitempty"`
	Config     Config    `json:"config"`
	Cal        CalState  `json:"cal"`
	Profiles   []Profile `json:"profiles"`
	DataDir    string    `json:"dataDir"`
	Seq        int64     `json:"seq"`
	Points     []int     `json:"pointChoices"`
	Antenna    string    `json:"mockAntenna,omitempty"`
	// DeviceProfiles is set when the firmware has the `profile` command.
	DeviceProfiles *vna.DeviceProfiles `json:"deviceProfiles,omitempty"`
	ReadOnly       bool                `json:"readOnly"`
	MockScenarios  []string            `json:"mockScenarios,omitempty"`
	Refs           map[string]RefInfo  `json:"refs"`
	Tune           TuneView            `json:"tune"`
	Compare        CompareView         `json:"compare"`
	TrimActive     string              `json:"trimActive,omitempty"`
	ReportChoices  []ReportChoice      `json:"reportChoices"`
	Hold           HoldView            `json:"hold"`
}

// Result is one processed sweep, as sent to the UI and stored in snapshots.
type Result struct {
	Name       string       `json:"name,omitempty"`
	Seq        int64        `json:"seq"`
	Time       time.Time    `json:"time"`
	DurationMs float64      `json:"durationMs"`
	Config     Config       `json:"config"`
	CalApplied bool         `json:"calApplied"`
	Freq       []float64    `json:"freq"`
	S11        [][2]float64 `json:"s11"`
	S21        [][2]float64 `json:"s21,omitempty"`
	Derived    rf.Derived   `json:"derived"`
	Summary    rf.Summary   `json:"summary"`
	AvgN       int          `json:"avgN,omitempty"`    // sweeps averaged into this result
	Ref        *rf.RefDelta `json:"ref,omitempty"`     // comparison with the profile's REF trace
	Tune       *rf.TuneInfo `json:"tune,omitempty"`    // TUNE view readout
	Compare    *CompareLive `json:"compare,omitempty"` // S21 compare against stored antenna A
	Hold       *HoldLive    `json:"hold,omitempty"`    // stable-positions HOLD
	// Source is set for a measurement opened from a file (not live).
	Source *FileSource `json:"source,omitempty"`
}

func (r *Result) complexS11() []complex128 { return toComplex(r.S11) }
func (r *Result) complexS21() []complex128 {
	if len(r.S21) == 0 {
		return nil
	}
	return toComplex(r.S21)
}

func toComplex(v [][2]float64) []complex128 {
	out := make([]complex128, len(v))
	for i, p := range v {
		out[i] = complex(p[0], p[1])
	}
	return out
}

// Options configure the server.
type Options struct {
	Open        Opener
	Store       *Store
	Static      fs.FS
	MockAntenna string
	Logger      *log.Logger
	// ReadOnly disables everything that changes stored device state
	// (calibration, save, recall, on-device profile switching).
	ReadOnly bool
	// SetMockAntenna swaps the simulated antenna (mock mode only).
	SetMockAntenna func(name string) error
	// MockCut shortens the simulated element by mm (mock mode only), so the
	// trim calculator can be exercised without hardware.
	MockCut       func(mm float64)
	MockScenarios []string
	// Backup enables GET/POST /api/backup (export / import every stored
	// file as one JSON document). The browser build turns it on.
	Backup bool
	// AppName is recorded in backups and printed on sheets, e.g.
	// "EasyVNA Web v0.2.0".
	AppName string
	// HomeURL is printed in the antenna sheet footer (default: the
	// project page on GitHub).
	HomeURL string
}

// Server is the EasyVNA Web application.
type Server struct {
	opt   Options
	hub   *hub
	store *Store
	log   *log.Logger

	opMu   sync.Mutex // serialises device operations (sweeps vs calibration)
	connMu sync.Mutex // serialises connecting

	mu       sync.Mutex
	dev      vna.Device
	info     *vna.Info
	lastErr  string
	busy     string
	cfg      Config
	cal      CalState
	settings Settings
	profiles []Profile
	last     *Result
	seq      int64
	devProfs *vna.DeviceProfiles

	avgBuf, avgBuf21 [][]complex128
	avgKey           string
	lastByProfile    map[string]*Result
	tune             *tuneState
	refs             map[string]*Result
	compare          *CompareSession
	compareOn        bool
	compareS21Prev   bool
	hold             *holdState

	continuous atomic.Bool
	wake       chan struct{}
}

// New creates a server. Call Run to start the sweep loop.
func New(opt Options) *Server {
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	s := &Server{opt: opt, hub: newHub(), store: opt.Store, log: opt.Logger, wake: make(chan struct{}, 1)}
	s.settings = s.store.LoadSettings()
	s.profiles = DefaultProfiles()
	for i := range s.profiles {
		if v, ok := s.settings.ProfileSlots[s.profiles[i].ID]; ok {
			s.profiles[i].CalSlot = v
		}
	}
	s.applyProfileLocked(s.profiles[0])
	s.cfg.Avg = 1
	s.cal.Flow = CalFlow{Steps: map[string]string{}}
	s.lastByProfile = map[string]*Result{}
	s.refs = s.store.LoadRefs()
	s.compare = s.store.LoadCompare()
	s.hold = newHoldState()
	return s
}

func (s *Server) applyProfileLocked(p Profile) {
	pe, avg := s.cfg.PortExtPs, s.cfg.Avg
	s.cfg = Config{Profile: p.ID, Start: p.Start, Stop: p.Stop, Points: p.Points, Bands: p.Bands,
		Identify: p.Identify, PortExtPs: pe, S21: s.cfg.S21, Avg: avg}
}

func (s *Server) profile(id string) (Profile, bool) {
	for _, p := range s.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// Run drives continuous sweeping until ctx is cancelled, then closes the device.
func (s *Server) Run(ctx context.Context) {
	go func() {
		// Try to connect at start-up so the UI shows the device immediately.
		if _, err := s.device(ctx); err != nil {
			s.log.Printf("device: %v", err)
		}
		s.publishState()
	}()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if s.dev != nil {
				_ = s.dev.Close()
				s.dev = nil
			}
			s.mu.Unlock()
			return
		case <-s.wake:
		}
		for s.continuous.Load() && ctx.Err() == nil {
			if _, err := s.sweepOnce(ctx); err != nil {
				s.log.Printf("sweep: %v", err)
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
				}
			}
		}
	}
}

// device returns the connected device, connecting if necessary.
func (s *Server) device(ctx context.Context) (vna.Device, error) {
	s.connMu.Lock() // never open the port twice concurrently
	defer s.connMu.Unlock()
	s.mu.Lock()
	dev := s.dev
	s.mu.Unlock()
	if dev != nil {
		return dev, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dev, err := s.opt.Open(cctx)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return nil, err
	}
	info, err := dev.Info(cctx)
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	st, _ := dev.CalStatus(cctx)
	s.mu.Lock()
	s.dev, s.info, s.lastErr = dev, &info, ""
	s.cal.Status = st // range of a boot-time calibration is unknown (Active stays nil)
	s.mu.Unlock()
	s.syncDeviceProfiles(cctx, dev)
	s.log.Printf("connected: %s %s (%s), max %d points/scan", info.Model, info.Version, info.Port, info.MaxPoints)
	return dev, nil
}

// syncDeviceProfiles reads on-device profiles (PiX firmware only) and
// adopts their ranges, points and slots so the UI and the device agree.
func (s *Server) syncDeviceProfiles(ctx context.Context, dev vna.Device) {
	pr, ok := dev.(vna.Profiler)
	if !ok || !pr.SupportsProfiles() {
		s.mu.Lock()
		s.devProfs = nil
		s.mu.Unlock()
		return
	}
	dps, err := pr.Profiles(ctx)
	if err != nil {
		s.log.Printf("device profiles: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devProfs = &dps
	for _, dp := range dps.Profiles {
		for i := range s.profiles {
			p := &s.profiles[i]
			if p.ID != dp.Name {
				continue
			}
			p.Start, p.Stop, p.CalSlot = dp.Start, dp.Stop, dp.Slot
			if dp.Points > 1 {
				p.Points = dp.Points
			}
			p.Sub = mhzRange(dp.Start, dp.Stop)
			if p.Identify {
				p.Sub += " · identify"
			}
			if s.cfg.Profile == p.ID {
				s.cfg.Start, s.cfg.Stop, s.cfg.Points = p.Start, p.Stop, p.Points
			}
			if dp.Active && dp.Cal && s.cal.Active == nil {
				sl := dp.Slot
				s.cal.Active = &CalRange{Start: dp.Start, Stop: dp.Stop, Points: dp.Points, Source: fmt.Sprintf("slot %d (device profile)", sl), Slot: &sl}
			}
		}
	}
}

// handleDevErr drops the device on transport errors so the next operation
// reconnects (e.g. after the NanoVNA was unplugged and plugged back in).
func (s *Server) handleDevErr(err error) {
	if err == nil {
		return
	}
	var se *nanovnah.ShellError
	s.mu.Lock()
	s.lastErr = err.Error()
	if !errors.As(err, &se) && !errors.Is(err, context.Canceled) && s.dev != nil {
		_ = s.dev.Close()
		s.dev, s.info = nil, nil
	}
	s.mu.Unlock()
	s.publishState()
}

func (s *Server) setBusy(b string) {
	s.mu.Lock()
	s.busy = b
	s.mu.Unlock()
	s.publishState()
}

// sweepOnce performs and publishes one sweep.
func (s *Server) sweepOnce(ctx context.Context) (*Result, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	dev, err := s.device(ctx)
	if err != nil {
		s.publishState()
		return nil, err
	}
	s.mu.Lock()
	cfg := s.cfg
	calApplied := s.cal.Status.Applied
	s.mu.Unlock()
	sw, err := dev.Scan(ctx, cfg.scanRequest())
	if err != nil {
		s.handleDevErr(err)
		return nil, err
	}
	res := s.process(sw, cfg)
	res.CalApplied = calApplied
	s.mu.Lock()
	s.seq++
	res.Seq = s.seq
	s.last = res
	s.postProcessLocked(res)
	hadErr := s.lastErr != ""
	s.lastErr = ""
	s.mu.Unlock()
	if hadErr {
		s.publishState()
	}
	s.hub.publish("sweep", res)
	return res, nil
}

// process applies port extension and computes derived data and analysis.
func (s *Server) process(sw *vna.Sweep, cfg Config) *Result {
	tau := cfg.PortExtPs * 1e-12
	n := len(sw.Freq)
	s11 := make([]complex128, n)
	var s21 []complex128
	if len(sw.S21) == n && cfg.S21 {
		s21 = make([]complex128, n)
	}
	for i, f := range sw.Freq {
		s11[i] = rf.PortExtension(sw.S11[i], f, tau)
		if s21 != nil {
			s21[i] = rf.TransmissionExtension(sw.S21[i], f, tau)
		}
	}
	s11, s21, avgN := s.average(cfg, sw.Freq, s11, s21)
	r := buildResult(sw.Freq, s11, s21, cfg, sw.Started, sw.Duration)
	r.AvgN = avgN
	return r
}

func buildResult(freq []float64, s11, s21 []complex128, cfg Config, t time.Time, dur time.Duration) *Result {
	d := rf.Derive(s11, s21)
	sum, _ := rf.Analyze(freq, d, cfg.Bands)
	r := &Result{Time: t, DurationMs: float64(dur.Microseconds()) / 1000, Config: cfg, Freq: freq,
		S11: make([][2]float64, len(s11)), Derived: d, Summary: sum}
	for i, c := range s11 {
		r.S11[i] = [2]float64{real(c), imag(c)}
	}
	if s21 != nil {
		r.S21 = make([][2]float64, len(s21))
		for i, c := range s21 {
			r.S21[i] = [2]float64{real(c), imag(c)}
		}
	}
	return r
}

// state snapshots the current state.
func (s *Server) state() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{
		Connected: s.dev != nil, Info: s.info, Error: s.lastErr, Continuous: s.continuous.Load(),
		Busy: s.busy, Config: s.cfg, Cal: s.cal, Profiles: append([]Profile(nil), s.profiles...),
		DataDir: s.store.Dir(), Seq: s.seq, Antenna: s.opt.MockAntenna, DeviceProfiles: s.devProfs,
		Points: []int{101, 201, 401, 801, 1601},
	}
	st.Cal.Settings = s.settings
	st.ReadOnly, st.MockScenarios, st.TrimActive = s.opt.ReadOnly, s.opt.MockScenarios, s.settings.TrimActive
	st.Refs = map[string]RefInfo{}
	for k, r := range s.refs {
		st.Refs[k] = RefInfo{Time: r.Time, Points: len(r.Freq), Start: r.Config.Start, Stop: r.Config.Stop, HasUndo: s.store.HasRefPrev(k)}
		if r.Source != nil {
			ri := st.Refs[k]
			ri.File = r.Source.File
			st.Refs[k] = ri
		}
	}
	if s.tune != nil {
		st.Tune = TuneView{Active: true, Target: s.tune.Target, Start: s.cfg.Start, Stop: s.cfg.Stop}
	}
	st.Compare = s.compareViewLocked()
	st.ReportChoices = s.reportChoicesLocked()
	st.Hold = s.hold.view()
	if a := s.cal.Active; a != nil {
		m := a.Start == s.cfg.Start && a.Stop == s.cfg.Stop
		st.Cal.Matches = &m
	}
	return st
}

func (s *Server) publishState() { s.hub.publish("state", s.state()) }

// refreshCal re-reads the firmware calibration status.
func (s *Server) refreshCal(ctx context.Context, dev vna.Device) {
	st, err := dev.CalStatus(ctx)
	if err != nil {
		s.handleDevErr(err)
		return
	}
	s.mu.Lock()
	s.cal.Status = st
	s.mu.Unlock()
}

// Handler returns the HTTP handler (API + static UI).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("GET /", http.FileServerFS(s.opt.Static))
	return logRequests(s.log, mux)
}

func logRequests(l *log.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		if r.Method != http.MethodGet {
			l.Printf("%s %s", r.Method, r.URL.Path)
		}
	})
}

func fmtMHz(f float64) string { return fmt.Sprintf("%.3f MHz", f/1e6) }
