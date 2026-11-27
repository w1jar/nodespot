package node

import (
	"errors"
	"math"
	"testing"
	"time"
)

// gainTable is n+1 steps of 1 dB each, 0..n dB.
func gainTable(n int) []float64 {
	t := make([]float64, n+1)
	for i := range t {
		t[i] = float64(i)
	}
	return t
}

func TestChooseCaptureStep(t *testing.T) {
	dbs := gainTable(35)
	floor := -34.0 // -22 target, 18 dB max gain, 6 dB headroom
	tests := []struct {
		name         string
		cur          int
		speech, peak float64
		want         int
		quiet        bool
	}{
		{"quiet speech needs a boost", 10, -43, -40, 19, false},
		{"already enough: the lowest step that is", 10, -30, -20, 6, false},
		{"loud: lowest step, which is well above the floor", 10, -4, -1, 0, false},
		{"peaky: the floor needs +16 dB but the peak allows +10: no clipping wins", 10, -50, -13, 20, false},
		{"too quiet even at the top", 10, -80, -70, 35, true},
		{"top step is just enough", 0, -69, -60, 35, false},
	}
	for _, tc := range tests {
		got, quiet := chooseCaptureStep(dbs, tc.cur, tc.speech, tc.peak, floor)
		if got != tc.want || quiet != tc.quiet {
			t.Errorf("%s: step %d quiet %v, want %d %v", tc.name, got, quiet, tc.want, tc.quiet)
		}
	}
	// Even the lowest step clips.
	if got, _ := chooseCaptureStep(dbs, 10, -2, 0, floor); got != 0 {
		t.Errorf("hot: step %d, want 0", got)
	}
}

func TestStepDbsFallback(t *testing.T) {
	d, est := stepDbs(nil, 4)
	if !est || len(d) != 5 || d[4] != 4*autosetFallbackDb {
		t.Errorf("fallback %v %v", d, est)
	}
	d, est = stepDbs([]float64{math.NaN(), -3, -2, -1}, 3)
	if est || d[0] > -100 || d[3] != -1 {
		t.Errorf("mute step %v %v", d, est)
	}
}

// autoRig is a node whose fake sound chip has capture steps.
func autoRig(t *testing.T, steps int) *rig {
	t.Helper()
	cfg := quietConfig()
	cfg.Station.Advanced.RadioAGC = bp(true)
	g := newRig(t, cfg)
	g.r.SetCaptureSteps(gainTable(steps))
	return g
}

// talkFor keys the receiver and talks a sine with the given peak amplitude
// for d, then unkeys.
func (g *rig) talkFor(d time.Duration, amp float64) {
	g.r.SetCarrier(true)
	for i := 0; i < int(d/20/time.Millisecond); i++ {
		g.r.Inject(sineFrame(500, amp, i))
		g.step(1)
	}
	g.r.SetCarrier(false)
	g.step(3)
}

func (g *rig) autosetWait() AutosetStatus {
	g.t.Helper()
	for i := 0; i < 200; i++ {
		if st := g.e.AutosetStatus(); st.State == AutosetDone || st.State == AutosetFailed {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.t.Fatalf("auto-set did not finish: %+v", g.e.AutosetStatus())
	return AutosetStatus{}
}

func TestAutosetSetsTheStep(t *testing.T) {
	g := autoRig(t, 35)
	if st, err := g.e.AutosetRx(); err != nil || st.State != AutosetWaiting {
		t.Fatalf("start: %+v %v", st, err)
	}
	if _, err := g.e.AutosetRx(); !errors.Is(err, ErrAutosetBusy) {
		t.Errorf("second start: %v", err)
	}
	g.talkFor(6*time.Second, 328) // peak -40 dBFS, speech -43
	st := g.autosetWait()
	if st.State != AutosetDone || st.Result == nil {
		t.Fatalf("status %+v", st)
	}
	r := st.Result
	if r.Step != 27 || r.Steps != 35 || r.Db == nil || *r.Db != 27 || r.Estimated {
		t.Errorf("result %+v", r)
	}
	if math.Abs(r.SpeechDb+43) > 0.5 || math.Abs(r.PeakDb+40) > 0.5 {
		t.Errorf("measured %+v", r)
	}
	if lv := g.r.Levels(); lv.CaptureStep != 27 {
		t.Errorf("chip step %d, want 27", lv.CaptureStep)
	}
}

func TestAutosetFailures(t *testing.T) {
	// No capture control.
	if _, err := newRig(t, quietConfig()).e.AutosetRx(); !errors.Is(err, ErrAutosetNoSteps) {
		t.Errorf("no steps: %v", err)
	}
	// No carrier in 15 seconds.
	g := autoRig(t, 35)
	g.e.AutosetRx()
	g.run(16 * time.Second)
	if st := g.e.AutosetStatus(); st.State != AutosetFailed || st.Error == "" {
		t.Errorf("no carrier: %+v", st)
	}
	// Too short.
	g.e.AutosetRx()
	g.talkFor(time.Second, 3000)
	if st := g.autosetWait(); st.State != AutosetFailed {
		t.Errorf("short: %+v", st)
	}
	// Too quiet even at the highest step (only +10 dB there): nothing is set.
	g = autoRig(t, 10)
	g.e.AutosetRx()
	g.talkFor(5*time.Second, 200) // peak -44, speech -47
	st := g.autosetWait()
	if st.State != AutosetFailed || st.Error == "" {
		t.Errorf("quiet: %+v", st)
	}
	if lv := g.r.Levels(); lv.CaptureStep != 5 {
		t.Errorf("a failed run moved the level to %d", lv.CaptureStep)
	}
	// Cancel leaves it idle and a new run can start.
	g.e.AutosetRx()
	if st := g.e.AutosetCancel(); st.State != AutosetIdle {
		t.Errorf("cancel: %+v", st)
	}
	if _, err := g.e.AutosetRx(); err != nil {
		t.Errorf("restart: %v", err)
	}
}
