package calib_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/calib"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

func burstEv(t time.Time, voice bool, errs int) mmdvm.RXEvent {
	return mmdvm.RXEvent{Kind: mmdvm.RXBurst, Time: t, Voice: voice, FECErrors: errs}
}

func TestTrackerBER(t *testing.T) {
	tr := calib.NewTracker(150)
	t0 := time.Now()
	tr.Observe(burstEv(t0, false, 0))
	tr.Observe(mmdvm.RXEvent{Kind: mmdvm.RXCallStart, Time: t0, Src: 1234567, Dst: 91})
	for i := 0; i < 20; i++ {
		tr.Observe(burstEv(t0.Add(time.Duration(i+1)*calib.BurstDuration), true, 3))
	}
	tr.Observe(mmdvm.RXEvent{Kind: mmdvm.RXCallEnd, Time: t0})
	c := <-tr.Calls()
	if c.Offset != 150 || c.Src != 1234567 || c.Dst != 91 || c.VoiceBursts != 20 || c.Bursts != 21 || c.Errs != 60 {
		t.Fatalf("call %+v", c)
	}
	ber, ok := c.BER()
	if want := 60.0 / (20 * 141); !ok || ber != want {
		t.Errorf("BER %v, want %v", ber, want)
	}
	if c.Duration != 21*calib.BurstDuration || !c.Usable(time.Second) || c.Usable(2*time.Second) {
		t.Errorf("duration %v usable %v", c.Duration, c.Usable(time.Second))
	}
	if sn := tr.Snapshot(t0); sn.Frames != 21 || sn.Lost != 0 || sn.Last == nil || sn.Live != nil {
		t.Errorf("snapshot %+v", sn)
	}
}

func TestTrackerRolling(t *testing.T) {
	tr := calib.NewTracker(0)
	t0 := time.Now()
	for i := 0; i < 30; i++ { // 1.8 s: errors only in the second half
		e := 0
		if i >= 15 {
			e = 14
		}
		tr.Observe(burstEv(t0.Add(time.Duration(i)*calib.BurstDuration), true, e))
	}
	now := t0.Add(29 * calib.BurstDuration)
	sn := tr.Snapshot(now)
	if !sn.RollingOK || sn.Rolling != 210.0/(17*141) { // the last 1 s holds 17 bursts, 15 with 14 errors
		t.Errorf("rolling %v ok %v", sn.Rolling, sn.RollingOK)
	}
	if sn.Live == nil {
		t.Error("no live call")
	}
	if sn := tr.Snapshot(now.Add(3 * time.Second)); sn.RollingOK {
		t.Error("rolling BER after silence")
	}
}

func TestTrackerLostAndUnusable(t *testing.T) {
	tr := calib.NewTracker(0)
	t0 := time.Now()
	for i := 0; i < 10; i++ {
		e := 2
		if i%2 == 0 {
			e = 30 // one uncorrectable frame and more
		}
		tr.Observe(burstEv(t0.Add(time.Duration(i)*calib.BurstDuration), true, e))
	}
	tr.Observe(mmdvm.RXEvent{Kind: mmdvm.RXLost, Time: t0})
	tr.Observe(mmdvm.RXEvent{Kind: mmdvm.RXCallEnd, Time: t0}) // after the lost: nothing more
	c := <-tr.Calls()
	if !c.Lost || c.Unusable != 5 || c.LostFraction() != 0.5 {
		t.Errorf("%+v fraction %v", c, c.LostFraction())
	}
	if sn := tr.Snapshot(t0); sn.Lost != 1 {
		t.Errorf("lost %d", sn.Lost)
	}
	select {
	case c := <-tr.Calls():
		t.Errorf("a second call %+v", c)
	default:
	}
}

func TestTrackerMissingAndMostlyLost(t *testing.T) {
	tr := calib.NewTracker(0)
	t0 := time.Now()
	for i := 0; i < 20; i++ { // every third burst arrives
		tr.Observe(burstEv(t0.Add(time.Duration(i*3)*calib.BurstDuration), true, 0))
	}
	tr.Tick(t0.Add(time.Minute)) // silence ends it
	c := <-tr.Calls()
	if c.Missing != 38 || c.Usable(time.Second) {
		t.Errorf("missing %d usable %v lost %.2f", c.Missing, c.Usable(time.Second), c.LostFraction())
	}
}

func TestTrackerOffsetChangeMarksCall(t *testing.T) {
	tr := calib.NewTracker(0)
	t0 := time.Now()
	for i := 0; i < 30; i++ {
		tr.Observe(burstEv(t0.Add(time.Duration(i)*calib.BurstDuration), true, 0))
		if i == 10 {
			tr.SetOffset(50)
		}
	}
	tr.Tick(t0.Add(time.Minute))
	if c := <-tr.Calls(); !c.Changed || c.Usable(time.Second) || c.Offset != 0 {
		t.Errorf("%+v", c)
	}
}

func TestAnalyze(t *testing.T) {
	pts := func(bers ...float64) []calib.Point {
		var p []calib.Point
		for i, b := range bers {
			p = append(p, calib.Point{Offset: -1000 + 250*i, BER: b})
		}
		return p
	}
	cases := []struct {
		name         string
		p            []calib.Point
		lo, hi, mid  int
		edge, flat   bool
		bestOffset   int
		thresholdMin float64
	}{
		{"single minimum", pts(.30, .20, .10, .02, .09, .21, .30), -250, -250, -250, false, false, -250, 0},
		{"flat bottom", pts(.30, .12, .02, .02, .02, .03, .15, .30), -500, 250, -125, false, false, -500, 0},
		{"two close minima", pts(.30, .02, .015, .30, .30), -750, -500, -625, false, false, -500, 0},
		{"a far run is not merged", pts(.012, .20, .20, .019, .01, .2), -250, 0, -125, false, false, 0, 0},
		{"at the edge", pts(.01, .01, .02, .20, .30), -1000, -500, -750, true, false, -1000, 0},
		{"all equal", pts(.05, .05, .05, .05), -1000, -250, -625, true, true, -1000, 0},
		{"noise within the margin", pts(.40, .015, .028, .012, .020, .35), -750, 0, -375, false, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := calib.Analyze(c.p)
			if err != nil {
				t.Fatal(err)
			}
			if s.Edge != c.edge || s.Flat != c.flat {
				t.Errorf("edge %v flat %v", s.Edge, s.Flat)
			}
			if c.name != "noise within the margin" && (s.Lo != c.lo || s.Hi != c.hi || s.Center != c.mid || s.Best.Offset != c.bestOffset) {
				t.Errorf("%+v, want %d..%d center %d best %d", s, c.lo, c.hi, c.mid, c.bestOffset)
			}
		})
	}
	if _, err := calib.Analyze(nil); err == nil {
		t.Error("no error without points")
	}
	// Unsorted input.
	s, _ := calib.Analyze([]calib.Point{{Offset: 500, BER: .3}, {Offset: 0, BER: .01}, {Offset: 250, BER: .01}, {Offset: -250, BER: .3}})
	if s.Lo != 0 || s.Hi != 250 || s.Center != 125 {
		t.Errorf("unsorted: %+v", s)
	}
}

func TestPlan(t *testing.T) {
	ok := func(off int) error {
		if off > 1250 {
			return errors.New("too high")
		}
		return nil
	}
	offs, skipped, err := calib.Plan(100, -500, 1500, 500, ok)
	if err != nil || len(offs) != 4 || offs[0] != -400 || offs[3] != 1100 || len(skipped) != 1 {
		t.Errorf("%v %v %v", offs, skipped, err)
	}
	for _, bad := range [][3]int{{0, 0, -1}, {10, 0, 5}, {-100000, 100000, 1}} {
		if _, _, err := calib.Plan(0, bad[0], bad[1], bad[2], nil); err == nil {
			t.Errorf("no error for %v", bad)
		}
	}
	if _, _, err := calib.Plan(0, 0, 100, 50, func(int) error { return errors.New("no") }); err == nil {
		t.Error("no error when nothing is allowed")
	}
}

func TestSystemd(t *testing.T) {
	var ran [][]string
	state := "active"
	svc := calib.Systemd("nodespot-node.service", func(name string, args ...string) ([]byte, error) {
		ran = append(ran, append([]string{name}, args...))
		if args[0] == "is-active" {
			if state == "active" {
				return []byte("active\n"), nil
			}
			return []byte(state + "\n"), errors.New("exit status 3")
		}
		return nil, nil
	})
	if a, err := svc.Active(); !a || err != nil {
		t.Errorf("active %v %v", a, err)
	}
	state = "inactive"
	if a, err := svc.Active(); a || err != nil {
		t.Errorf("inactive %v %v", a, err)
	}
	if err := svc.Stop(); err != nil {
		t.Error(err)
	}
	if err := svc.Start(); err != nil {
		t.Error(err)
	}
	last := ran[len(ran)-2:]
	if strings.Join(last[0], " ") != "systemctl stop nodespot-node.service" || strings.Join(last[1], " ") != "systemctl start nodespot-node.service" {
		t.Errorf("ran %v", ran)
	}
	bad := calib.Systemd("x.service", func(string, ...string) ([]byte, error) {
		return []byte("Failed to stop x.service: denied\n"), errors.New("exit 1")
	})
	if err := bad.Stop(); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("got %v", err)
	}
	none := calib.Systemd("x.service", func(string, ...string) ([]byte, error) { return nil, errors.New("not found") })
	if _, err := none.Active(); err == nil {
		t.Error("no error without systemctl")
	}
}

func TestSetOffsetRetunes(t *testing.T) {
	r := newRig(t, 100, nil)
	if err := r.s.SetOffset(r.ctx, 350); err != nil {
		t.Fatal(err)
	}
	if r.s.Offset() != 350 {
		t.Errorf("offset %d", r.s.Offset())
	}
	got := setFreqs(r.m)
	if len(got) != 2 || got[0] != baseRX+100 || got[1] != baseRX+350 {
		t.Errorf("SET_FREQ receive frequencies %v", got)
	}
	if rx, _ := r.m.RXFreq(); rx != baseRX+350 {
		t.Errorf("modem rx %d", rx)
	}
	if rx, _ := r.m.TunedRXFreq(); rx != baseRX+350 {
		t.Errorf("chip tuned to %d: SET_FREQ never reached it", rx)
	}
	if p, _ := r.m.RXPower(); p != 0 {
		t.Errorf("RF power %d", p)
	}
	if st := r.m.State(); st.Opens != 1 || st.Mode != mmdvm.ModeDMR {
		t.Errorf("modem %+v: retune reopened it or left DMR mode", st)
	}
	if err := r.s.SetOffset(r.ctx, 200_000); err == nil {
		t.Error("an offset beyond the node's limit accepted")
	}
	if r.s.Offset() != 350 || len(setFreqs(r.m)) != 2 {
		t.Error("a refused offset changed something")
	}
	noTX(t, r.m)
}

// A frequency the modem would NAK (here the banned 435 to 438 MHz) is
// stopped before it is sent.
func TestSetOffsetChecksBand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m := fake.New(fake.Options{})
	near := func(off int) mmdvm.Freq { return mmdvm.Freq{RX: uint32(434_950_000 + off), TX: 434_950_000} }
	s := calib.NewSession(fastSettings(m), near, mmdvm.FreqRules{UHFOnly: true}, 0)
	if err := s.Start(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetOffset(ctx, 60_000); err == nil || !strings.Contains(err.Error(), "435") {
		t.Errorf("got %v", err)
	}
	if err := s.SetOffset(ctx, 20_000); err != nil {
		t.Error(err)
	}
	if got := setFreqs(m); len(got) != 2 {
		t.Errorf("SET_FREQ %v", got)
	}
}

func TestSessionNeverTransmits(t *testing.T) {
	r := newRig(t, 0, nil)
	if r.s.Driver().BlockedTX() != 0 {
		t.Fatal("blocked at start")
	}
	if err := r.s.Driver().StartTX(network.CallHeader{Call: 1, Slot: 2}); err == nil {
		t.Error("StartTX worked")
	}
	noTX(t, r.m)
}

func sweepRig(t *testing.T, saved int, model *fake.RXErrorModel) (*rig, calib.SweepOptions, *strings.Builder) {
	r := newRig(t, saved, model)
	var out strings.Builder
	return r, calib.SweepOptions{Out: &out, Prompt: func(int) { r.play(60) }}, &out
}

func runSweep(t *testing.T, r *rig, o calib.SweepOptions, saved, from, to, step int) ([]calib.Point, calib.Suggestion) {
	t.Helper()
	offs, _, err := calib.Plan(saved, from, to, step, r.s.Check)
	if err != nil {
		t.Fatal(err)
	}
	o.Offsets = offs
	pts, err := calib.Sweep(r.ctx, r.s, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != len(offs) {
		t.Fatalf("%d points for %d offsets", len(pts), len(offs))
	}
	s, err := calib.Analyze(pts)
	if err != nil {
		t.Fatal(err)
	}
	return pts, s
}

func within(a, b, d int) bool { return a-b <= d && b-a <= d }

// The true center is 300 Hz above the base; the saved offset is 100.
func TestSweepFindsCenter(t *testing.T) {
	model := &fake.RXErrorModel{CenterHz: baseRX + 300, ErrsPerKHz: 8}
	r, o, out := sweepRig(t, 100, model)
	pts, s := runSweep(t, r, o, 100, -1000, 1000, 250)
	if pts[0].Offset != -900 || pts[len(pts)-1].Offset != 1100 {
		t.Errorf("sweep covers %d to %d, want saved+-1000", pts[0].Offset, pts[len(pts)-1].Offset)
	}
	if !within(s.Center, 300, 125) || s.Edge {
		t.Errorf("suggestion %+v, want about 300", s)
	}
	// BER grows away from the center.
	if pts[0].BER <= pts[4].BER || pts[len(pts)-1].BER <= pts[5].BER {
		t.Errorf("BER does not grow with distance: %+v", pts)
	}
	if !strings.Contains(out.String(), "key up for about 3 s now (offset -900 Hz)") {
		t.Errorf("output %q", out.String())
	}
	noTX(t, r.m)
}

func TestSweepFlatBottom(t *testing.T) {
	// No errors anywhere in 300 +- 500 Hz.
	model := &fake.RXErrorModel{CenterHz: baseRX + 300, DeadbandHz: 500, ErrsPerKHz: 12}
	r, o, _ := sweepRig(t, 0, model)
	pts, s := runSweep(t, r, o, 0, -1500, 1500, 250)
	if s.Best.BER != 0 {
		t.Fatalf("%+v", pts)
	}
	// The good range is the whole flat bottom, not the first zero.
	if s.Lo > -250 || s.Hi < 750 || !within(s.Center, 300, 150) {
		t.Errorf("range %d..%d center %d from %+v", s.Lo, s.Hi, s.Center, pts)
	}
}

func TestSweepNoisy(t *testing.T) {
	model := &fake.RXErrorModel{CenterHz: baseRX - 400, ErrsPerKHz: 9, Noise: 2, DeadbandHz: 250}
	r, o, _ := sweepRig(t, 0, model)
	pts, s := runSweep(t, r, o, 0, -2000, 1000, 250)
	if !within(s.Center, -400, 250) {
		t.Errorf("center %d from %+v", s.Center, pts)
	}
}

func TestSweepIgnoresShortAndStopsEarly(t *testing.T) {
	r := newRig(t, 0, &fake.RXErrorModel{CenterHz: baseRX, ErrsPerKHz: 8})
	var out strings.Builder
	n := 0
	ctx, cancel := context.WithCancel(r.ctx)
	o := calib.SweepOptions{Offsets: []int{0, 250, 500, 750}, Out: &out, Prompt: func(int) {
		n++
		switch n {
		case 1:
			r.play(6) // too short: ignored; then a long one counts
			r.play(60)
		case 2:
			r.play(60)
		case 3:
			cancel() // Ctrl-C at the third step
		}
	}}
	pts, err := calib.Sweep(ctx, r.s, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || pts[0].Offset != 0 || pts[1].Offset != 250 {
		t.Errorf("points %+v", pts)
	}
	if !strings.Contains(out.String(), "ignored a call: ") || !strings.Contains(out.String(), "shorter than 1.0 s") {
		t.Errorf("output %q", out.String())
	}
}

func TestManualKeys(t *testing.T) {
	r := newRig(t, 100, nil)
	var out strings.Builder
	var savedAt []int
	save := func(off int) (int, error) { savedAt = append(savedAt, off); return 100, nil }
	// + ] - [ 0 as saved; then +, s (saves 150), 0 (back to 150), ? q.
	in := strings.NewReader("+]-[0+s0?q")
	saved, err := calib.Manual(r.ctx, r.s, calib.ManualOptions{In: in, Out: &out, Saved: 100, Save: save, Refresh: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{baseRX + 100, baseRX + 150, baseRX + 650, baseRX + 600, baseRX + 100, baseRX + 100, baseRX + 150, baseRX + 150}
	got := setFreqs(r.m)
	if len(got) != len(want) {
		t.Fatalf("SET_FREQ %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SET_FREQ %d: %d, want %d", i, got[i], want[i])
		}
	}
	if len(savedAt) != 1 || savedAt[0] != 150 || saved != 150 {
		t.Errorf("saved %v returned %d", savedAt, saved)
	}
	for _, s := range []string{"saved rxOffsetHz: +100 Hz -> +150 Hz", "Keys:", "offset +150 Hz"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%q", s, out.String())
		}
	}
	noTX(t, r.m)
}

func TestManualArrowsAndLimits(t *testing.T) {
	r := newRig(t, 0, nil)
	var out strings.Builder
	// Up, right (500), an escape not followed by a bracket, then q.
	in := strings.NewReader("\x1b[A\x1b[C\x1bxq")
	if _, err := calib.Manual(r.ctx, r.s, calib.ManualOptions{In: in, Out: &out, Refresh: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if r.s.Offset() != 550 {
		t.Errorf("offset %d", r.s.Offset())
	}
	// 's' with no save function says so; a refused step says why and stays.
	r2 := newRig(t, 99_990, nil)
	var out2 strings.Builder
	if _, err := calib.Manual(r2.ctx, r2.s, calib.ManualOptions{In: strings.NewReader("]sq"), Out: &out2, Refresh: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if r2.s.Offset() != 99_990 || !strings.Contains(out2.String(), "cannot use +100490 Hz") || !strings.Contains(out2.String(), "saving is not available") {
		t.Errorf("offset %d output %q", r2.s.Offset(), out2.String())
	}
}

func TestManualShowsCalls(t *testing.T) {
	r := newRig(t, 0, &fake.RXErrorModel{CenterHz: baseRX + 1000, ErrsPerKHz: 8})
	var out strings.Builder
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = calib.Manual(r.ctx, r.s, calib.ManualOptions{In: pr, Out: &safeW{w: &out}, Refresh: 5 * time.Millisecond})
	}()
	r.play(60)
	waitFor(t, func() bool { return strings.Contains(safeString(&out), "BER 5.7%") }) // 8 errors of 141 bits
	_, _ = pw.Write([]byte("q"))
	<-done
	s := safeString(&out)
	for _, w := range []string{"1234567 -> 91", "offset +0 Hz", "frames "} {
		if !strings.Contains(s, w) {
			t.Errorf("output lacks %q:\n%q", w, s)
		}
	}
}
