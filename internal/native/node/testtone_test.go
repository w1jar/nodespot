package node

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

func rmsDb(fs []audio.Frame) float64 {
	var sum float64
	n := 0
	for _, f := range fs {
		for _, s := range f {
			sum += float64(s) * float64(s)
			n++
		}
	}
	if n == 0 || sum == 0 {
		return -90
	}
	return 20 * math.Log10(math.Sqrt(sum/float64(n))/32768)
}

func TestTestTonePlaysAndEnds(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.ModeAGC = bp(true)
	cfg.Station.Advanced.TxAGCTarget = -16
	cfg.Station.Advanced.TxOnDelayMS = intp(100)
	g := newRig(t, cfg)
	if err := g.e.TestTone(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !g.e.Status().Radio.TestTone {
		t.Fatal("status does not show the tone")
	}
	g.step(1)
	if !g.r.PTT() {
		t.Fatal("the tone did not key the radio")
	}
	g.run(2200 * time.Millisecond)
	played := g.r.Played()
	// Silent for the key-up delay, then the tone at the network-to-radio
	// AGC target (RMS).
	if loud(played[0]) || loud(played[3]) {
		t.Fatal("the tone did not wait for the key-up delay")
	}
	mid := played[20:80]
	if db := rmsDb(mid); math.Abs(db-(-16)) > 0.5 {
		t.Fatalf("tone RMS %.1f dBFS, want -16", db)
	}
	// 1 kHz: 8 samples per cycle, so 20 zero crossings per 20 ms frame.
	f := mid[0]
	cross := 0
	for i := 1; i < len(f); i++ {
		if (f[i-1] < 0) != (f[i] < 0) {
			cross++
		}
	}
	if cross < 39 || cross > 41 {
		t.Fatalf("%d zero crossings in a frame, want 40", cross)
	}
	if g.e.Status().Radio.TestTone {
		t.Fatal("the tone did not end")
	}
	g.run(time.Second) // hang
	if g.r.PTT() {
		t.Fatal("PTT still keyed after the tone and hang")
	}
}

func TestTestToneStopsEarly(t *testing.T) {
	g := newRig(t, quietConfig())
	if err := g.e.TestTone(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	g.step(10)
	g.e.StopTestTone()
	g.step(1)
	n := len(g.r.Played())
	g.step(5)
	if countLoud(g.r.Played()[n:]) != 0 {
		t.Fatal("tone still playing after Stop")
	}
	if g.e.Status().Radio.TestTone {
		t.Fatal("status still shows the tone")
	}
	g.e.StopTestTone() // nothing playing: no error, no panic
}

func TestTestToneRefused(t *testing.T) {
	cases := []struct {
		name  string
		setup func(g *rig)
		d     time.Duration
		want  error
	}{
		{"too long", func(*rig) {}, 11 * time.Second, ErrToneLength},
		{"too short", func(*rig) {}, 500 * time.Millisecond, ErrToneLength},
		{"receiving", func(g *rig) { g.r.SetCarrier(true); g.step(1) }, 0, ErrToneReceiving},
		{"mode talking", func(g *rig) {
			g.sel(mode.BM, nil)
			g.fake(mode.BM).Key("N1CALL")
		}, 0, ErrToneTalking},
		{"transmitting", func(g *rig) {
			g.sel(mode.BM, nil)
			g.fake(mode.BM).Key("N1CALL", frame(100), frame(100))
			g.step(1)
			g.fake(mode.BM).Unkey()
			g.step(2) // in the hang time
		}, 0, ErrToneTransmitting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, quietConfig())
			c.setup(g)
			if err := g.e.TestTone(c.d); !errors.Is(err, c.want) {
				t.Fatalf("err %v, want %v", err, c.want)
			}
		})
	}
}

func TestTestToneGivesWay(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, nil)
	if err := g.e.TestTone(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	g.step(5)
	g.fake(mode.BM).Key("N1CALL", frame(100))
	g.step(1)
	if g.e.Status().Radio.TestTone {
		t.Fatal("the network talking did not stop the tone")
	}
}

func TestTestToneOp(t *testing.T) {
	g, s, _ := ctlRig(t)
	if r := do(t, s, OpTestTone, map[string]any{"seconds": 11}); r.OK {
		t.Fatal("11 s accepted")
	}
	if r := do(t, s, OpTestTone, map[string]any{"seconds": 3}); !r.OK {
		t.Fatal(r.Error)
	}
	if !g.e.Status().Radio.TestTone {
		t.Fatal("no tone")
	}
	if r := do(t, s, OpTestTone, map[string]any{"stop": true}); !r.OK {
		t.Fatal(r.Error)
	}
	if g.e.Status().Radio.TestTone {
		t.Fatal("tone not stopped")
	}
}

// The two AGCs level to their own targets.
func TestAGCSplitTargets(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.RadioAGC = bp(true)
	cfg.Station.Advanced.ModeAGC = bp(true)
	cfg.Station.Advanced.RxAGCTarget = -30
	cfg.Station.Advanced.TxAGCTarget = -12
	tone := func(i int) audio.Frame { return sineFrame(1000, 3000, i) } // about -23 dBFS RMS
	sent := rxSent(t, cfg, tone, 150)
	if db := rmsDb(sent[100:]); math.Abs(db-(-30)) > 1.5 {
		t.Fatalf("radio to network at %.1f dBFS, want -30", db)
	}
	g := newRig(t, cfg)
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	bm.Key("N1CALL")
	for i := 0; i < 150; i++ {
		bm.Deliver(tone(i))
		g.step(1)
	}
	if db := rmsDb(g.r.Played()[100:140]); math.Abs(db-(-12)) > 1.5 {
		t.Fatalf("network to radio at %.1f dBFS, want -12", db)
	}
}
