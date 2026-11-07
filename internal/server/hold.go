package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pix/easyvna-web/internal/rf"
)

// holdState is the server side of HOLD ("stable positions" envelope).
type holdState struct {
	on          bool
	cfg         rf.HoldConfig
	sensitivity string
	h           *rf.Holder
	version     int               // bumps whenever the positions change
	undo        []rf.HoldPosition // positions before the last clear/remove
	undoOK      bool
}

const maxHoldPositions = 40

// HoldPosView is a position without its trace (sent with every sweep).
type HoldPosView struct {
	ID        int       `json:"id"`
	Time      time.Time `json:"time"`
	ResFreq   float64   `json:"resFreq"`
	MinSWR    float64   `json:"minSWR"`
	BandWorst float64   `json:"bandWorst"`
}

// HoldLive is attached to every sweep while HOLD is on.
type HoldLive struct {
	rf.HoldUpdate
	rf.HoldSummary
	Version int           `json:"version"`
	List    []HoldPosView `json:"list"`
}

// HoldView is the HOLD part of State.
type HoldView struct {
	On          bool   `json:"on"`
	N           int    `json:"n"`
	Sensitivity string `json:"sensitivity"`
	Positions   int    `json:"positions"`
	Version     int    `json:"version"`
	HasUndo     bool   `json:"hasUndo"`
}

func newHoldState() *holdState {
	return &holdState{cfg: rf.DefaultHoldConfig(), sensitivity: "normal"}
}

func (hs *holdState) view() HoldView {
	v := HoldView{On: hs.on, N: hs.cfg.N, Sensitivity: hs.sensitivity, Version: hs.version, HasUndo: hs.undoOK}
	if hs.h != nil {
		v.Positions = len(hs.h.Positions)
	}
	return v
}

// feedLocked runs the motion detector on a sweep. s.mu must be held.
func (s *Server) feedHoldLocked(res *Result) {
	hs := s.hold
	if !hs.on || s.tune != nil {
		return
	}
	cfg := res.Config
	band, ref := rf.Band{}, 0.0
	if !cfg.Identify && len(cfg.Bands) > 0 {
		band, ref = cfg.Bands[0], cfg.Bands[0].Reference()
	}
	if hs.h == nil {
		hs.h = rf.NewHolder(hs.cfg, band, ref)
	}
	hs.h.Band, hs.h.RefFreq, hs.h.Cfg = band, ref, hs.cfg
	before := len(hs.h.Positions)
	var u rf.HoldUpdate
	if before >= maxHoldPositions {
		u = rf.HoldUpdate{Status: "full", Need: hs.cfg.N}
	} else {
		u = hs.h.Add(res.Freq, res.complexS11(), res.Time)
	}
	if len(hs.h.Positions) != before || (u.Status == rf.HoldWaiting && before > 0) {
		hs.version++
	}
	live := &HoldLive{HoldUpdate: u, HoldSummary: hs.h.Summary(), Version: hs.version, List: []HoldPosView{}}
	for _, p := range hs.h.Positions {
		live.List = append(live.List, HoldPosView{ID: p.ID, Time: p.Time, ResFreq: p.ResFreq, MinSWR: p.MinSWR, BandWorst: p.BandWrst})
	}
	res.Hold = live
}

func (s *Server) hHold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On          *bool   `json:"on"`
		N           *int    `json:"n"`
		Sensitivity *string `json:"sensitivity"`
		Clear       bool    `json:"clear"`
		Remove      int     `json:"remove"`
		Undo        bool    `json:"undo"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	hs := s.hold
	msg := ""
	saveUndo := func() {
		if hs.h != nil {
			hs.undo = append([]rf.HoldPosition(nil), hs.h.Positions...)
			hs.undoOK = true
		}
	}
	switch {
	case req.N != nil:
		if *req.N < 2 || *req.N > 10 {
			s.mu.Unlock()
			writeErr(w, 400, errors.New("N must be 2..10"))
			return
		}
		hs.cfg.N = *req.N
		msg = fmt.Sprintf("a position needs %d steady sweeps", *req.N)
	case req.Sensitivity != nil:
		th, ok := rf.HoldSensitivity[*req.Sensitivity]
		if !ok {
			s.mu.Unlock()
			writeErr(w, 400, errors.New("sensitivity must be low, normal or high"))
			return
		}
		hs.cfg.Threshold, hs.sensitivity = th, *req.Sensitivity
		msg = "motion sensitivity " + *req.Sensitivity
	case req.Clear:
		saveUndo()
		if hs.h != nil {
			hs.h.Positions = nil
		}
		hs.version++
		msg = "positions cleared"
	case req.Remove > 0:
		if hs.h == nil {
			break
		}
		saveUndo()
		if !hs.h.Remove(req.Remove) {
			s.mu.Unlock()
			writeErr(w, 404, fmt.Errorf("no position %d", req.Remove))
			return
		}
		hs.version++
		msg = fmt.Sprintf("position %d removed", req.Remove)
	case req.Undo:
		if !hs.undoOK || hs.h == nil {
			s.mu.Unlock()
			writeErr(w, 409, errors.New("nothing to undo"))
			return
		}
		cur := hs.h.Positions
		hs.h.Positions, hs.undo = hs.undo, cur
		hs.version++
		msg = "positions restored"
	case req.On != nil:
		hs.on = *req.On
		if hs.on {
			hs.h = rf.NewHolder(hs.cfg, rf.Band{}, 0)
			hs.undoOK = false
			hs.version++
			msg = "HOLD on: keep the antenna still in each position"
		} else {
			msg = "HOLD off"
		}
	}
	s.mu.Unlock()
	s.publishState()
	writeJSON(w, 200, map[string]any{"message": msg, "state": s.state()})
}

// hHoldGet returns the full positions with their SWR traces.
func (s *Server) hHoldGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := struct {
		Version   int               `json:"version"`
		Positions []rf.HoldPosition `json:"positions"`
	}{Version: s.hold.version, Positions: []rf.HoldPosition{}}
	if s.hold.h != nil {
		out.Positions = append(out.Positions, s.hold.h.Positions...)
	}
	writeJSON(w, 200, out)
}
