package rf

import (
	"math"
	"sort"
)

// ---------- batch statistics ----------
//
// BATCH mode measures several antennas of one lot and shows how consistent
// the lot is. Stats summarises one metric over the lot; Outliers flags the
// antennas that do not fit the others.

// Stat summarises a sample. SD is the sample standard deviation (n − 1);
// it is 0 for fewer than two values. NaN and ±Inf values are ignored.
type Stat struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	SD     float64 `json:"sd"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Spread float64 `json:"spread"` // max − min
	Median float64 `json:"median"`
	MAD    float64 `json:"mad"` // median absolute deviation from the median
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func medianOf(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Describe computes the statistics of the finite values of x.
func Describe(x []float64) Stat {
	var v []float64
	for _, a := range x {
		if finite(a) {
			v = append(v, a)
		}
	}
	st := Stat{N: len(v)}
	if st.N == 0 {
		return st
	}
	st.Min, st.Max = v[0], v[0]
	sum := 0.0
	for _, a := range v {
		sum += a
		st.Min, st.Max = math.Min(st.Min, a), math.Max(st.Max, a)
	}
	st.Mean = sum / float64(st.N)
	if st.N > 1 {
		ss := 0.0
		for _, a := range v {
			ss += (a - st.Mean) * (a - st.Mean)
		}
		st.SD = math.Sqrt(ss / float64(st.N-1))
	}
	st.Spread = st.Max - st.Min
	st.Median = medianOf(v)
	dev := make([]float64, len(v))
	for i, a := range v {
		dev[i] = math.Abs(a - st.Median)
	}
	st.MAD = medianOf(dev)
	return st
}

// Outlier rule: the modified z-score of Iglewicz and Hoaglin,
// |x − median| / (1.4826 · MAD) > 3.5. The median and the MAD are not
// pulled by the outlier itself (a mean and a standard deviation are: with
// 5 antennas, one bad one inflates σ so much that it never stands 3σ out),
// and 1.4826 · MAD estimates σ for normal data. For small lots two guards
// keep it sensible:
//   - fewer than OutlierMinN values: nothing is flagged (with two antennas
//     there is no telling which one is odd);
//   - the scale is at least floor, the measurement repeatability of the
//     metric: when most antennas are identical the MAD is 0 and any tiny
//     difference would otherwise count as infinitely far out.
const (
	OutlierZ    = 3.5
	OutlierMAD  = 1.4826
	OutlierMinN = 3
)

// Outliers flags the values of x that are outliers among the finite values
// of x (non-finite values are never flagged). floor is the smallest scale
// (same unit as x) treated as a real difference.
func Outliers(x []float64, floor float64) []bool {
	out := make([]bool, len(x))
	st := Describe(x)
	if st.N < OutlierMinN {
		return out
	}
	scale := math.Max(OutlierMAD*st.MAD, floor)
	if scale <= 0 {
		return out
	}
	for i, a := range x {
		out[i] = finite(a) && math.Abs(a-st.Median) > OutlierZ*scale
	}
	return out
}

// ---------- automatic capture of the next antenna ----------
//
// Arrival decides when a new antenna of a batch is on the cable and steady,
// so it can be captured without touching the screen. It reuses HOLD's
// motion detector (consecutive sweeps compared by RMS |ΔΓ| against the
// same threshold, N agreeing sweeps make a steady reading) and adds what
// "a new antenna" means:
//
//   - The reading must have changed since the last capture: either the
//     cable was seen open (no antenna: the usual way, unscrewing one antenna
//     and screwing on the next), or the trace jumped (a sweep rejected as
//     moving) and settled on a reading that differs from the last captured
//     antenna by more than ArrivalChange × threshold.
//   - It must then be steady for N sweeps, and look like an antenna (the
//     caller says whether the sweep has an "open cable" or "reflection
//     above 1" warning).
//
// A hand that touches the antenna and lets go makes the trace jump and
// settle back on the same reading: not captured. Slow drift (temperature)
// never makes a sweep "moving", so it cannot trigger a capture either. Two
// truly identical antennas are still captured one after the other, because
// swapping them shows the open cable in between.

// ArrivalChange is how much more than the motion threshold a steady
// reading must differ from the last capture, when the cable was not seen
// open in between.
const ArrivalChange = 2.5

// Arrival statuses.
const (
	ArrWaiting   = "waiting"   // first sweep
	ArrNoAntenna = "noantenna" // open cable: ready for the next antenna
	ArrMoving    = "moving"    // the trace is changing
	ArrSettling  = "settling"  // steady run in progress
	ArrReady     = "ready"     // a new antenna is steady: capture it now
	ArrSame      = "same"      // steady, but the same antenna as the last capture
	ArrStill     = "still"     // steady at the last capture: connect the next antenna
)

// ArrivalUpdate is what one sweep did.
type ArrivalUpdate struct {
	Status string  `json:"status"`
	Run    int     `json:"run"`
	Need   int     `json:"need"`
	Diff   float64 `json:"diff"` // RMS |ΔΓ| to the previous sweep
	// Gamma is the average of the N steady sweeps (set with ArrReady).
	Gamma []complex128 `json:"-"`
}

// Arrival is the auto-capture state machine. Not safe for concurrent use.
type Arrival struct {
	Cfg      HoldConfig
	last     []complex128 // smoothed reading of the last captured antenna (nil: none yet)
	prev     []complex128
	run      [][]complex128
	key      string
	removed  bool // the cable was seen open since the last capture
	departed bool // the trace jumped since the last capture
	settled  bool // the current steady run was already decided
}

// NewArrival creates the detector. last is the reading of the last captured
// antenna (nil if the batch is empty). When it is set, the next steady
// reading is compared with it, so switching auto-capture on with the last
// captured antenna still connected does not capture it twice.
func NewArrival(cfg HoldConfig, last []complex128) *Arrival {
	if cfg.N < 2 {
		cfg.N = 2
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = HoldSensitivity["normal"]
	}
	a := &Arrival{Cfg: cfg}
	a.Captured(last)
	a.departed, a.settled = last != nil, false
	return a
}

// Captured tells the detector that g was just captured (manually or
// automatically): the next capture needs a new antenna.
func (a *Arrival) Captured(g []complex128) {
	if g != nil {
		a.last = smooth3(g)
	} else {
		a.last = nil
	}
	a.removed, a.departed, a.settled = false, false, true
}

// Add feeds one sweep. antenna is false when the sweep shows no antenna
// (open cable) or an impossible reflection.
func (a *Arrival) Add(freq []float64, s11 []complex128, antenna bool) ArrivalUpdate {
	u := ArrivalUpdate{Need: a.Cfg.N}
	if k := gridKey(freq); k != a.key {
		a.key, a.prev, a.run = k, nil, nil
		if len(a.last) != len(s11) {
			a.last = nil
		}
	}
	cur := append([]complex128(nil), s11...)
	if !antenna {
		a.removed, a.departed, a.settled = true, true, false
		a.prev, a.run = cur, nil
		u.Status = ArrNoAntenna
		return u
	}
	if a.prev == nil || a.run == nil {
		a.prev, a.run = cur, [][]complex128{cur}
		u.Status, u.Run = ArrWaiting, 1
		if a.removed {
			u.Status = ArrSettling
		}
		return u
	}
	u.Diff = rmsDiff(cur, a.prev)
	a.prev = cur
	jump := u.Diff > a.Cfg.Threshold
	if jump || rmsDiff(cur, a.run[0]) > 1.5*a.Cfg.Threshold { // a jump, or slow creep
		// Only a jump counts as a possible swap: creep is drift.
		a.run, a.settled, a.departed = [][]complex128{cur}, false, a.departed || jump
		u.Status, u.Run = ArrMoving, 1
		return u
	}
	a.run = append(a.run, cur)
	u.Run = len(a.run)
	if a.settled {
		u.Status = ArrStill
		return u
	}
	if len(a.run) < a.Cfg.N {
		u.Status = ArrSettling
		return u
	}
	steady := Average(a.run[len(a.run)-a.Cfg.N:])
	a.settled = true
	isNew := a.last == nil || a.removed ||
		(a.departed && rmsDiff(smooth3(steady), a.last) > ArrivalChange*a.Cfg.Threshold)
	if !isNew {
		a.departed = false
		u.Status = ArrSame
		return u
	}
	u.Status, u.Gamma = ArrReady, steady
	return u
}
