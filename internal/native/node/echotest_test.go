package node

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/echotest"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// echoRig is an engine on a fake radio with the real echo test mode, both
// stepped by a fake clock.
type echoRig struct {
	*rig
	echo *echotest.Mode
}

func newEchoRig(t *testing.T, values map[string]string) *echoRig {
	t.Helper()
	g := &rig{t: t, r: radio.NewFake(), now: t0}
	er := &echoRig{rig: g}
	cfg := quietConfig()
	if values != nil {
		cfg.Modes[mode.EchoTest] = mode.Settings{Values: values}
	}
	e, err := New(cfg, Options{
		Radio: g.r,
		Now:   g.clock,
		Build: func(n string, env mode.Env, s mode.Settings) (mode.Mode, error) {
			if n != mode.EchoTest {
				return modetest.New(n), nil
			}
			m, err := echotest.New(env, s, echotest.Options{Tick: -1})
			er.echo = m
			return m, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	g.e = e
	return er
}

// stepBoth runs n frames of the node and the echo test's playback.
func (g *echoRig) stepBoth(n int) {
	for i := 0; i < n; i++ {
		g.step(1)
		g.echo.Step(g.clock())
	}
}

func sine(amp float64, k int) audio.Frame {
	var f audio.Frame
	for i := range f {
		f[i] = int16(amp * math.Sin(2*math.Pi*500*float64(k*audio.FrameSamples+i)/audio.SampleRate))
	}
	return f
}

// TestEchoTestThroughTheNode talks into the radio, then hears the node key
// the radio and play the recording back about a second later.
func TestEchoTestThroughTheNode(t *testing.T) {
	g := newEchoRig(t, nil)
	g.sel(mode.EchoTest, nil)
	if st := g.e.Status(); st.Active != mode.EchoTest || st.EchoTest != nil {
		t.Fatalf("status %+v", st)
	}

	g.r.SetCarrier(true)
	for i := 0; i < 25; i++ { // 500 ms of speech at -20 dBFS peak
		g.r.Inject(sine(3277, i))
		g.stepBoth(1)
	}
	if lv := g.e.Status().Radio.Level; !lv.COS || lv.PeakDb < -21 || lv.PeakDb > -19 || lv.RMSDb < -24 || lv.RMSDb > -22 {
		t.Fatalf("input level %+v", lv)
	}
	g.r.SetCarrier(false)
	g.stepBoth(2)
	rep := g.e.Status().EchoTest
	if rep == nil || rep.DurationMs != 500 || len(rep.Warnings) != 0 {
		t.Fatalf("report %+v", rep)
	}
	if m := g.e.Status().Mode; m == nil || m.Levels == nil {
		t.Fatal("the mode status has no report")
	}
	if keyings(g.r.PTTLog()) != 0 {
		t.Fatal("keyed before the playback")
	}

	// A second later (the courtesy tone is off) the radio keys and plays.
	deadline := time.Now().Add(5 * time.Second)
	for !g.r.PTT() && time.Now().Before(deadline) {
		g.stepBoth(5)
		time.Sleep(time.Millisecond)
	}
	if !g.r.PTT() {
		t.Fatal("the radio never keyed")
	}
	if waited := g.clock().Sub(t0); waited < 25*audio.FrameDuration+time.Second {
		t.Fatalf("playback began %s in, before the delay", waited)
	}
	g.run(2 * time.Second)
	for i := 0; i < 100; i++ {
		g.stepBoth(1)
	}
	if n := countLoud(g.r.Played()); n < 24 || n > 26 {
		t.Fatalf("%d loud frames played, want about 25", n)
	}
	if g.r.PTT() {
		t.Fatal("still keyed after the hang")
	}
	if keyings(g.r.PTTLog()) != 1 {
		t.Fatalf("keyed %d times", keyings(g.r.PTTLog()))
	}
}

func TestInputLevelGoesQuietWithoutFrames(t *testing.T) {
	g := newRig(t, quietConfig())
	for i := 0; i < 10; i++ {
		g.r.Inject(frame(3000))
		g.step(1)
	}
	if lv := g.e.InputLevel(); lv.PeakDb < -21 || lv.PeakDb > -20 {
		t.Fatalf("level %+v", lv)
	}
	g.run(500 * time.Millisecond)
	if lv := g.e.InputLevel(); lv.PeakDb != audio.FloorDb || lv.RMSDb != audio.FloorDb || lv.COS {
		t.Fatalf("level after silence %+v", lv)
	}
}

// The Install notice follows the echo test's saved codec.
func TestModesNeedsAmbeFollowsTheCodec(t *testing.T) {
	needs := func(g *echoRig) bool {
		for _, mi := range g.e.Modes() {
			if mi.Name == mode.EchoTest {
				return mi.NeedsAmbe
			}
			if mi.Name == mode.BM && !mi.NeedsAmbe {
				t.Error("BrandMeister must always need AMBE")
			}
		}
		t.Fatal("no echo test in the mode list")
		return false
	}
	g := newEchoRig(t, nil)
	if needs(g) {
		t.Error("the unconfigured echo test needs AMBE")
	}
	if err := g.e.SaveMode(mode.EchoTest, mode.Settings{Values: map[string]string{"codec": "ambe"}}); err != nil {
		t.Fatal(err)
	}
	if !needs(g) {
		t.Error("codec ambe does not need AMBE")
	}
	if err := g.e.SaveMode(mode.EchoTest, mode.Settings{Values: map[string]string{"codec": "gsm"}}); err != nil {
		t.Fatal(err)
	}
	if needs(g) {
		t.Error("codec gsm needs AMBE")
	}
}
