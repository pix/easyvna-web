package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// ---------- Measure ×N: several fresh sweeps of one antenna, averaged ----------
//
// A measure job takes N fresh sweeps (AVG's rolling average is bypassed,
// so each one is independent), averages S11 per point as a vector
// (rf.Average, the same as AVG) and measures the repeatability
// (rf.Repeatability). The average becomes the current result, with its
// repeatability in Result.Multi, so the antenna sheet, the report, HISTORY
// and REF use it like any sweep. For a BATCH capture the average is the
// antenna's reading; for the setup check it is one step.
//
// In "re-seat" mode the job waits between sweeps until the antenna was
// disturbed (unscrewed: open cable; or moved: the trace jumped) and is
// steady again (HOLD's motion detector and N), or until Continue.

// MultiInfo is the repeatability of a Measure ×N result.
type MultiInfo struct {
	rf.Repeat
	Text string `json:"text"` // "average of 5 · resonance σ 0.08 MHz · SWR σ 0.01 · max |ΔΓ| 0.004"
}

// RepeatBrief is the repeatability kept with a batch antenna or a history
// entry (no envelope).
type RepeatBrief struct {
	N         int     `json:"n"`
	Mode      string  `json:"mode"`
	ResSD     float64 `json:"resSD"` // Hz
	SWRSD     float64 `json:"swrSD"`
	MaxDGamma float64 `json:"maxDGamma"`
}

func briefOf(m *MultiInfo) *RepeatBrief {
	if m == nil {
		return nil
	}
	return &RepeatBrief{N: m.N, Mode: m.Mode, ResSD: m.ResSD, SWRSD: m.SWRSD, MaxDGamma: m.MaxDGamma}
}

// repeatText is the one-line description of a Measure ×N result.
func repeatText(r rf.Repeat) string {
	mode := ""
	if r.Mode == rf.RepeatReseat {
		mode = ", re-seated"
	}
	parts := []string{fmt.Sprintf("average of %d%s", r.N, mode)}
	if r.ResN >= 2 {
		parts = append(parts, fmt.Sprintf("resonance σ %.2f MHz", r.ResSD/1e6))
	}
	parts = append(parts, fmt.Sprintf("SWR σ %.3f", r.SWRSD), fmt.Sprintf("max |ΔΓ| %.4f", r.MaxDGamma))
	return strings.Join(parts, " · ")
}

// briefText is repeatText for a RepeatBrief.
func briefText(b *RepeatBrief) string {
	if b == nil {
		return ""
	}
	return repeatText(rf.Repeat{N: b.N, Mode: b.Mode, ResN: 2, ResSD: b.ResSD, SWRSD: b.SWRSD, MaxDGamma: b.MaxDGamma})
}

// measureJob is one Measure ×N run.
type measureJob struct {
	ID       int
	For      string // measure, batch, setup
	N        int
	Reseat   bool
	Done     int
	Status   string // running, done, cancelled, error
	Phase    string // sweeping, or the re-seat detector status (rf.Reseat*)
	Run      int
	Need     int
	Message  string
	Step     string  // setup check step
	Replace  int     // batch: antenna to measure again
	Adjust   int     // batch: capture a new version of this antenna
	Change   string  // batch: what was changed (with Adjust)
	TrimMm   float64 // batch: mm cut (with Adjust)
	Label    string  // batch: label of a new antenna
	Captured int     // batch: antenna ID captured
	cancel   context.CancelFunc
	cont     chan struct{}
}

// MeasureView is the Measure ×N part of State.
type MeasureView struct {
	ID       int    `json:"id"`
	For      string `json:"for"`
	N        int    `json:"n"`
	Reseat   bool   `json:"reseat"`
	Done     int    `json:"done"`
	Status   string `json:"status"`
	Phase    string `json:"phase,omitempty"`
	Run      int    `json:"run,omitempty"`
	Need     int    `json:"need,omitempty"`
	Message  string `json:"message,omitempty"`
	Step     string `json:"step,omitempty"`
	Captured int    `json:"captured,omitempty"`
	Running  bool   `json:"running"`
}

func (s *Server) measureViewLocked() *MeasureView {
	j := s.meas
	if j == nil {
		return nil
	}
	return &MeasureView{ID: j.ID, For: j.For, N: j.N, Reseat: j.Reseat, Done: j.Done, Status: j.Status, Phase: j.Phase,
		Run: j.Run, Need: j.Need, Message: j.Message, Step: j.Step, Captured: j.Captured, Running: j.Status == "running"}
}

// measuringLocked is true while a job runs (AVG, HOLD and auto-capture
// stand aside). s.mu must be held.
func (s *Server) measuringLocked() bool { return s.meas != nil && s.meas.Status == "running" }

const maxMeasureN = 20

// hMeasure starts a job: POST /api/measure {"n":5,"reseat":false,"for":"measure"|"batch"|"setup","step":"A","replace":0,"label":"",
// "adjust":0,"change":"","trimMm":0}.
func (s *Server) hMeasure(w http.ResponseWriter, r *http.Request) {
	var req struct {
		N       int     `json:"n"`
		Reseat  bool    `json:"reseat"`
		For     string  `json:"for"`
		Step    string  `json:"step"`
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
	if req.N < 2 || req.N > maxMeasureN {
		writeErr(w, 400, fmt.Errorf("measure 2 to %d times", maxMeasureN))
		return
	}
	switch req.For {
	case "", "measure":
		req.For = "measure"
	case "batch", "setup":
	default:
		writeErr(w, 400, errors.New("for: measure, batch or setup"))
		return
	}
	s.mu.Lock()
	var err error
	switch {
	case s.measuringLocked():
		err = errors.New("a measurement is already running: wait or cancel it")
	case s.tune != nil:
		err = errors.New("close TUNE first (it sweeps a narrow window)")
	case s.compareOn:
		err = errors.New("close COMPARE first")
	case req.For == "batch" && s.batch.cur == nil:
		err = errors.New("no active batch")
	case req.For == "batch" && req.Replace > 0 && s.batch.cur.index(req.Replace) < 0:
		err = fmt.Errorf("no antenna %d in this batch", req.Replace)
	case req.For == "batch" && req.Adjust > 0 && s.batch.cur.index(req.Adjust) < 0:
		err = fmt.Errorf("no antenna %d in this batch", req.Adjust)
	case req.For == "setup" && req.Step != "A" && req.Step != "B" && req.Step != "C":
		err = errors.New("setup check step: A, B or C")
	case req.For == "setup" && req.Step != "A" && (s.setup == nil || s.setup.a == nil):
		err = errors.New("capture step A (hands off) first")
	}
	if err != nil {
		s.mu.Unlock()
		writeErr(w, 409, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	id := 1
	if s.meas != nil {
		id = s.meas.ID + 1
	}
	j := &measureJob{ID: id, For: req.For, N: req.N, Reseat: req.Reseat, Status: "running", Phase: "sweeping",
		Step: req.Step, Replace: req.Replace, Label: req.Label, Adjust: req.Adjust, Change: req.Change, TrimMm: req.TrimMm, cancel: cancel, cont: make(chan struct{}, 1)}
	s.meas = j
	s.mu.Unlock()
	s.publishState()
	go s.runMeasure(ctx, j)
	writeJSON(w, 200, s.state())
}

func (s *Server) hMeasureCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j := s.meas
	if j == nil || j.Status != "running" {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("no measurement is running"))
		return
	}
	j.cancel()
	s.mu.Unlock()
	writeJSON(w, 200, map[string]string{"message": "cancelling"})
}

// hMeasureContinue takes the next re-seat measurement now.
func (s *Server) hMeasureContinue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j := s.meas
	s.mu.Unlock()
	if j == nil || j.Status != "running" {
		writeErr(w, 409, errors.New("no measurement is running"))
		return
	}
	select {
	case j.cont <- struct{}{}:
	default:
	}
	writeJSON(w, 200, map[string]string{"message": "continuing"})
}

func (s *Server) setJob(j *measureJob, f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
	s.publishState()
}

// runMeasure performs a job. LIVE is paused while it runs and restored.
func (s *Server) runMeasure(ctx context.Context, j *measureJob) {
	wasCont := s.continuous.Swap(false)
	defer func() {
		j.cancel()
		if wasCont {
			s.continuous.Store(true)
			s.kick()
		}
		s.publishState()
	}()
	fail := func(status, msg string) {
		s.setJob(j, func() { j.Status, j.Message, j.Phase = status, msg, "" })
	}
	var sweeps [][]complex128
	var last *Result
	for len(sweeps) < j.N {
		if ctx.Err() != nil {
			fail("cancelled", fmt.Sprintf("cancelled after %d of %d sweeps", len(sweeps), j.N))
			return
		}
		var res *Result
		if j.Reseat && len(sweeps) > 0 {
			hc := s.holdConfig()
			det := rf.NewReseat(hc)
			s.setJob(j, func() {
				j.Phase, j.Run, j.Need = rf.ReseatWaiting, 0, hc.N
				j.Message = fmt.Sprintf("Unscrew and reconnect (or move) the antenna, then hold still: sweep %d of %d is taken when the reading is steady again.", len(sweeps)+1, j.N)
			})
			for res == nil {
				select {
				case <-j.cont:
					r, err := s.measureSweep(ctx)
					if err != nil {
						fail(errStatus(ctx), err.Error())
						return
					}
					res = r
					continue
				default:
				}
				r, err := s.measureSweep(ctx)
				if err != nil {
					fail(errStatus(ctx), err.Error())
					return
				}
				st, run := det.Add(r.complexS11(), isAntennaSweep(r))
				s.setJob(j, func() { j.Phase, j.Run = st, run })
				if st == rf.ReseatReady {
					res = r
				}
			}
		} else {
			r, err := s.measureSweep(ctx)
			if err != nil {
				fail(errStatus(ctx), err.Error())
				return
			}
			res = r
		}
		if !isAntennaSweep(res) {
			fail("error", "no antenna on the cable (|Γ| ≈ 1) or the calibration does not fit: connect the antenna and measure again")
			return
		}
		if last != nil && (len(res.Freq) != len(last.Freq) || res.Freq[0] != last.Freq[0] || res.Config.Profile != last.Config.Profile) {
			fail("error", "the sweep changed during the measurement (another profile was chosen): measure again")
			return
		}
		sweeps = append(sweeps, res.complexS11())
		last = res
		s.setJob(j, func() { j.Done, j.Phase, j.Message = len(sweeps), "sweeping", "" })
	}
	s.finishMeasure(j, last, sweeps)
}

func errStatus(ctx context.Context) string {
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "error"
}

func (s *Server) holdConfig() rf.HoldConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hold.cfg
}

// measureSweep is one fresh sweep of a job (no rolling average).
func (s *Server) measureSweep(ctx context.Context) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.New("cancelled")
	}
	// Cancel acts between sweeps: a scan interrupted half-way would leave
	// the device's reply in the serial stream.
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.sweepOnce(sctx)
}

// finishMeasure averages the sweeps and uses the result.
func (s *Server) finishMeasure(j *measureJob, last *Result, sweeps [][]complex128) {
	cfg := last.Config
	target := 0.0
	if !cfg.Identify && len(cfg.Bands) > 0 {
		target = cfg.Bands[0].Reference()
	}
	mode := rf.RepeatStill
	if j.Reseat {
		mode = rf.RepeatReseat
	}
	avg, rep := rf.Repeatability(last.Freq, sweeps, target, mode)
	res := buildResult(last.Freq, avg, nil, cfg, time.Now(), 0)
	res.AvgN, res.CalApplied = len(sweeps), last.CalApplied
	res.Multi = &MultiInfo{Repeat: rep, Text: repeatText(rep)}
	for _, w := range last.Summary.Warnings {
		if w.Code == "noisy" && !hasWarning(res.Summary.Warnings, "noisy") {
			res.Summary.Warnings = append(res.Summary.Warnings, w)
		}
	}
	s.mu.Lock()
	s.seq++
	res.Seq = s.seq
	s.last = res
	j.Status = "done" // before postProcessLocked: HOLD and BATCH see a normal result
	s.postProcessLocked(res)
	j.Message = res.Multi.Text
	var err error
	switch j.For {
	case "batch":
		b := s.batch.cur
		if b == nil {
			err = errors.New("the batch was closed")
			break
		}
		var a *BatchAntenna
		a, err = s.captureLocked(b, res, avg, capture{Replace: j.Replace, Label: j.Label, Adjust: j.Adjust, Change: j.Change, TrimMm: j.TrimMm})
		if err == nil {
			a.Rep = briefOf(res.Multi)
			err = s.store.SaveBatch(b)
			j.Captured = a.ID
			j.Message = fmt.Sprintf("%s captured: %s", a.Label, res.Multi.Text)
			s.batch.publish = true
		}
	case "setup":
		err = s.setupAddLocked(j.Step, res, sweeps)
	}
	if err != nil {
		j.Status, j.Message = "error", err.Error()
	}
	s.mu.Unlock()
	s.hub.publish("sweep", res)
}

func (s *Server) measuring() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.measuringLocked()
}
