package node

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// Receive auto-set: the operator presses a button, keys up a handheld and
// talks for about five seconds; the node measures the raw capture (before
// the PL filter, any gain and the AGC) while the carrier is on, and picks
// the sound chip's capture step.
//
// The step is the lowest one where the expected speech level is at least
// autosetFloor (the AGC target minus its maximum gain plus
// autosetHeadroomDb, so the AGC can always reach its target with some gain
// to spare) and the expected peak is at most autosetPeakDb. Both grow with
// the step, so that is a range of steps; when it is empty (a very peaky
// signal) clipping is the worse fault and the highest step that does not
// clip wins. The expected level at another step is the measured level plus
// the difference of the two steps' gains in dB, from the control's TLV
// (dB) information. A control without it is assumed to give
// autosetFallbackDb per step, which the result flags (Estimated); the next
// transmission's summary shows whether that was right.
//
// There is no transmit counterpart: the node cannot hear what it sends, so
// it has no measurement to set the playback level from.
const (
	autosetWait         = 15 * time.Second // for the carrier
	autosetTalk         = 5 * time.Second  // of speech to measure
	autosetMinSpeech    = 1500 * time.Millisecond
	autosetHeadroomDb   = 6.0
	autosetPeakDb       = -3.0
	autosetFallbackDb   = 1.0
	autosetNoAGCBelowDb = 6.0 // with the AGC off: speech floor is target-6
)

// Auto-set states.
const (
	AutosetIdle      = "idle"
	AutosetWaiting   = "waiting"   // for a transmission
	AutosetMeasuring = "measuring" // a transmission
	AutosetDone      = "done"
	AutosetFailed    = "failed"
)

// AutosetResult is the outcome of a receive auto-set.
type AutosetResult struct {
	Step  int      `json:"step"`
	Steps int      `json:"steps"`
	Db    *float64 `json:"db"` // the gain at Step, null without dB information
	// SpeechDb and PeakDb are the measured speech RMS and peak, dBFS, at
	// the step that was set when measuring.
	SpeechDb float64 `json:"speechDb"`
	PeakDb   float64 `json:"peakDb"`
	// Estimated: the control has no dB information, so the step size was
	// assumed.
	Estimated bool `json:"estimated,omitempty"`
}

// AutosetStatus is the state of the receive auto-set.
type AutosetStatus struct {
	State  string         `json:"state"`
	Error  string         `json:"error,omitempty"`
	Result *AutosetResult `json:"result,omitempty"`
}

// autoRun is a receive auto-set in progress. Guarded by Engine.mu.
type autoRun struct {
	acc       *audio.RxAccum
	deadline  time.Time
	measuring bool
	done      bool // measured; the level is being set
	step0     int
	steps     int
	dbs       []float64
	target    float64
	maxGain   float64
	agcOn     bool
}

// Errors of AutosetRx.
var (
	ErrAutosetNoRadio = errors.New("no radio is connected")
	ErrAutosetNoSteps = errors.New("the sound chip has no receive level control")
	ErrAutosetBusy    = errors.New("an auto-set is already running")
)

// stepDbs returns the gain of every step 0..steps for choosing, and whether
// it is estimated. Unknown (NaN) steps, such as a mute step, count as far
// down.
func stepDbs(dbs []float64, steps int) ([]float64, bool) {
	known := 0
	if len(dbs) == steps+1 {
		for _, d := range dbs {
			if !math.IsNaN(d) && !math.IsInf(d, 0) {
				known++
			}
		}
	}
	out := make([]float64, steps+1)
	if known < 2 {
		for k := range out {
			out[k] = float64(k) * autosetFallbackDb
		}
		return out, true
	}
	for k, d := range dbs {
		if math.IsNaN(d) || math.IsInf(d, 0) {
			d = -200
		}
		out[k] = d
	}
	return out, false
}

// chooseCaptureStep picks the capture step from a measurement taken at step
// cur: speechDb and peakDb are the measured levels, floorDb the lowest
// acceptable speech level. tooQuiet is true when even the highest step does
// not reach the floor without clipping the peak (the step returned is then
// the highest that does not clip, and nothing should be set).
func chooseCaptureStep(dbs []float64, cur int, speechDb, peakDb, floorDb float64) (step int, tooQuiet bool) {
	steps := len(dbs) - 1
	cur = min(max(cur, 0), steps)
	speechAt := func(k int) float64 { return speechDb + dbs[k] - dbs[cur] }
	peakAt := func(k int) float64 { return peakDb + dbs[k] - dbs[cur] }
	lo := -1 // lowest step reaching the floor
	for k := 0; k <= steps; k++ {
		if speechAt(k) >= floorDb {
			lo = k
			break
		}
	}
	hi := -1 // highest step that does not clip
	for k := steps; k >= 0; k-- {
		if peakAt(k) <= autosetPeakDb {
			hi = k
			break
		}
	}
	switch {
	case lo >= 0 && hi >= lo:
		return lo, false
	case hi >= 0 && lo >= 0: // too peaky: do not clip
		return hi, false
	case lo < 0: // too quiet even at the top
		return steps, true
	default: // even the lowest step clips
		return 0, false
	}
}

// AutosetStatus returns the state of the receive auto-set.
func (e *Engine) AutosetStatus() AutosetStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.autoStatusLocked()
}

func (e *Engine) autoStatusLocked() AutosetStatus {
	st := e.autoStat
	if st.State == "" {
		st.State = AutosetIdle
	}
	if a := e.sx.auto; a != nil {
		st.State = AutosetWaiting
		if a.measuring {
			st.State = AutosetMeasuring
		}
	}
	return st
}

// AutosetRx starts a receive auto-set: the node waits up to 15 seconds for
// a local transmission, measures about 5 seconds of it and sets the capture
// level.
func (e *Engine) AutosetRx() (AutosetStatus, error) {
	if e.o.RadioAbsent {
		return AutosetStatus{}, ErrAutosetNoRadio
	}
	lv := e.o.Radio.Levels()
	if lv.CaptureSteps < 1 {
		return AutosetStatus{}, ErrAutosetNoSteps
	}
	dbs, _ := stepDbs(lv.CaptureDbs, lv.CaptureSteps)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sx.auto != nil {
		return AutosetStatus{}, ErrAutosetBusy
	}
	p := e.params
	e.sx.auto = &autoRun{
		acc: audio.NewRxAccum(), deadline: e.now().Add(autosetWait),
		step0: lv.CaptureStep, steps: lv.CaptureSteps, dbs: dbs,
		target: p.rxAGC.TargetDb, maxGain: p.rxAGC.MaxGainDb, agcOn: p.radioAGC,
	}
	e.autoStat = AutosetStatus{}
	return e.autoStatusLocked(), nil
}

// AutosetCancel stops a receive auto-set that waits or measures.
func (e *Engine) AutosetCancel() AutosetStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sx.auto != nil {
		e.sx.auto = nil
		e.autoStat = AutosetStatus{}
	}
	return e.autoStatusLocked()
}

// autosetFail ends the run with a failure. Called with mu.
func (e *Engine) autosetFail(msg string) {
	e.sx.auto = nil
	e.autoStat = AutosetStatus{State: AutosetFailed, Error: msg}
}

// autosetTick fails a run that waited too long for a transmission. Called
// with mu, on every step.
func (e *Engine) autosetTick(now time.Time) {
	if a := e.sx.auto; a != nil && !a.measuring && now.After(a.deadline) {
		e.autosetFail("No transmission heard in 15 seconds. Key up the radio and talk, then try again.")
	}
}

// autosetFrame measures one raw frame of a transmission. Called with mu.
func (e *Engine) autosetFrame(f *audio.Frame) {
	a := e.sx.auto
	if a == nil || a.done {
		return
	}
	a.measuring = true
	a.acc.Add(f, f, 0, 0, false)
	if a.acc.SpeechDuration() >= autosetTalk {
		e.autosetFinish(false)
	}
}

// autosetRFEnd is called when a transmission ends. Called with mu.
func (e *Engine) autosetRFEnd() {
	if a := e.sx.auto; a != nil && a.measuring && !a.done {
		e.autosetFinish(true)
	}
}

// autosetFinish takes the measurement and sets the level in the background
// (setting it saves the config, which waits on locks the step holds).
func (e *Engine) autosetFinish(early bool) {
	a := e.sx.auto
	sum, ok := a.acc.Summary(e.now())
	if early && a.acc.SpeechDuration() < autosetMinSpeech || !ok {
		e.autosetFail("That was too short. Talk for about 5 seconds, then try again.")
		return
	}
	a.done = true
	e.autoStat = AutosetStatus{}
	go e.autosetApply(a, sum)
}

func (e *Engine) autosetApply(a *autoRun, sum audio.RxSummary) {
	fail := func(msg string) {
		e.mu.Lock()
		if e.sx.auto == a {
			e.autosetFail(msg)
		}
		e.mu.Unlock()
	}
	lv := e.o.Radio.Levels()
	if lv.CaptureSteps != a.steps || lv.CaptureStep != a.step0 {
		fail("The receive level was changed while measuring. Try again.")
		return
	}
	floor := a.target - a.maxGain + autosetHeadroomDb
	if !a.agcOn {
		floor = a.target - autosetNoAGCBelowDb
	}
	step, quiet := chooseCaptureStep(a.dbs, a.step0, sum.SpeechDb, sum.PeakDb, floor)
	if quiet {
		fail("Too quiet even at the highest receive level. Check the radio's volume and try again.")
		return
	}
	// The set level is exact on the step grid: step/steps.
	f := float64(step) / float64(a.steps)
	if err := e.Tune(&f, nil, nil); err != nil {
		fail(fmt.Sprintf("Could not set the receive level: %v", err))
		return
	}
	res := &AutosetResult{Step: step, Steps: a.steps, SpeechDb: sum.SpeechDb, PeakDb: sum.PeakDb}
	if len(lv.CaptureDbs) == a.steps+1 && !math.IsNaN(lv.CaptureDbs[step]) {
		d := lv.CaptureDbs[step]
		res.Db = &d
	}
	_, res.Estimated = stepDbs(lv.CaptureDbs, a.steps)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sx.auto != a {
		return // cancelled
	}
	e.sx.auto = nil
	e.autoStat = AutosetStatus{State: AutosetDone, Result: res}
}
