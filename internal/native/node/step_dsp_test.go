package node

import (
	"math"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

func sineFrame(hz float64, amp float64, n int) audio.Frame {
	var f audio.Frame
	for i := range f {
		f[i] = int16(math.Round(amp * math.Sin(2*math.Pi*hz*float64(n*audio.FrameSamples+i)/audio.SampleRate)))
	}
	return f
}

func peak(fs []audio.Frame) int {
	p := 0
	for _, f := range fs {
		for _, s := range f {
			if v := int(math.Abs(float64(s))); v > p {
				p = v
			}
		}
	}
	return p
}

// rxSent runs frames from the receiver through the node and returns what
// the mode received.
func rxSent(t *testing.T, cfg *Config, in func(i int) audio.Frame, n int) []audio.Frame {
	t.Helper()
	g := newRig(t, cfg)
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	g.r.SetCarrier(true)
	for i := 0; i < n; i++ {
		g.r.Inject(in(i))
		g.step(1)
	}
	return bm.Sent()
}

func TestStaticGainBothWays(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.RxGainDB = 6.0206
	cfg.Station.Advanced.TxGainDB = -6.0206
	got := rxSent(t, cfg, func(int) audio.Frame { return frame(1000) }, 5)
	if len(got) != 5 || got[2][10] < 1998 || got[2][10] > 2002 {
		t.Fatalf("receive gain: %d frames, sample %v", len(got), got[len(got)-1][10])
	}
	g := newRig(t, cfg)
	g.sel(mode.BM, nil)
	g.fake(mode.BM).Key("N1CALL", frame(2000), frame(2000), frame(2000))
	g.step(4)
	played := g.r.Played()
	if len(played) < 3 || played[1][10] < 998 || played[1][10] > 1002 {
		t.Fatalf("transmit gain: %d frames, %v", len(played), played[1][10])
	}
}

func TestGainIsBeforeAGC(t *testing.T) {
	// With the AGC on, +20 dB of gain in front of it cannot push the output
	// past the limiter ceiling.
	cfg := quietConfig()
	cfg.Station.Advanced.RadioAGC = bp(true)
	cfg.Station.Advanced.RxGainDB = 20
	got := rxSent(t, cfg, func(i int) audio.Frame { return sineFrame(1000, 12000, i) }, 30)
	if p := peak(got); p > 31000 || p == 0 {
		t.Fatalf("peak %d", p)
	}
}

func TestPLFilterAutoAndOverride(t *testing.T) {
	tone := func(i int) audio.Frame { return sineFrame(100, 10000, i) }
	// An SA818 interface (the fake's default): the auto setting is off.
	cfg := quietConfig()
	if p := peak(rxSent(t, cfg, tone, 40)[20:]); p < 9000 {
		t.Fatalf("auto on an SA818 filtered 100 Hz: peak %d", p)
	}
	// Forced on, it is removed.
	cfg = quietConfig()
	cfg.Station.Advanced.PLFilter = bp(true)
	if p := peak(rxSent(t, cfg, tone, 40)[20:]); p > 1000 {
		t.Fatalf("PL filter left 100 Hz at %d", p)
	}
	// 1 kHz passes.
	cfg = quietConfig()
	cfg.Station.Advanced.PLFilter = bp(true)
	if p := peak(rxSent(t, cfg, func(i int) audio.Frame { return sineFrame(1000, 10000, i) }, 40)[20:]); p < 9000 {
		t.Fatalf("PL filter ate 1 kHz: %d", p)
	}
	// A non-SA818 interface: auto is on.
	if !(Advanced{}).PL(false) || (Advanced{}).PL(true) {
		t.Fatal("auto PL default")
	}
	if (Advanced{PLFilter: bp(false)}).PL(false) || !(Advanced{PLFilter: bp(true)}).PL(true) {
		t.Fatal("explicit PL setting ignored")
	}
}

func TestSoftwareEmphasis(t *testing.T) {
	hi := func(i int) audio.Frame { return sineFrame(3000, 6000, i) }
	cfg := quietConfig()
	plain := peak(rxSent(t, cfg, hi, 40)[20:])
	cfg = quietConfig()
	cfg.Station.Advanced.DeEmphasis = true
	de := peak(rxSent(t, cfg, hi, 40)[20:])
	if de >= plain*8/10 {
		t.Fatalf("de-emphasis at 3 kHz: %d against %d", de, plain)
	}
	// Pre-emphasis on the transmit side lifts the highs.
	run := func(pre bool) int {
		cfg := quietConfig()
		cfg.Station.Advanced.PreEmphasis = pre
		g := newRig(t, cfg)
		g.sel(mode.BM, nil)
		var fs []audio.Frame
		for i := 0; i < 30; i++ {
			fs = append(fs, sineFrame(3000, 6000, i))
		}
		g.fake(mode.BM).Key("N1CALL", fs...)
		g.step(40)
		return peak(g.r.Played()[15:30])
	}
	if a, b := run(false), run(true); b <= a*14/10 {
		t.Fatalf("pre-emphasis at 3 kHz: %d against %d", b, a)
	}
}

func TestRxOnDelay(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.RxOnDelayMS = 200
	g := newRig(t, cfg)
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	// A carrier that drops before the delay never counts.
	g.r.SetCarrier(true)
	for i := 0; i < 5; i++ {
		g.r.Inject(frame(500))
		g.step(1)
	}
	g.r.SetCarrier(false)
	g.step(2)
	if n := len(bm.Sent()); n != 0 {
		t.Fatalf("a %d ms blip reached the mode (%d frames)", 100, n)
	}
	// A carrier that stays counts once the delay has passed.
	g.r.SetCarrier(true)
	for i := 0; i < 25; i++ {
		g.r.Inject(frame(500))
		g.step(1)
	}
	if n := len(bm.Sent()); n == 0 || n > 16 {
		t.Fatalf("mode got %d frames, want some after 200 ms", n)
	}
	// While the delay runs, network audio is not pushed aside.
	g2 := newRig(t, cfg)
	g2.sel(mode.BM, nil)
	g2.r.SetCarrier(true)
	g2.fake(mode.BM).Key("N1CALL", frame(900), frame(900))
	g2.step(3)
	if !g2.r.PTT() {
		t.Fatal("a carrier younger than the delay stopped the network audio")
	}
}

func TestRxOnDelayDefaultIsImmediate(t *testing.T) {
	got := rxSent(t, quietConfig(), func(int) audio.Frame { return frame(500) }, 3)
	if len(got) != 3 {
		t.Fatalf("mode got %d frames, want 3", len(got))
	}
}

func TestTxOffDelayExtendsHang(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.TxOffDelayMS = 700
	g := newRig(t, cfg)
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	bm.Key("N1CALL", frame(1000))
	g.step(2)
	bm.Unkey()
	g.run(1180 * time.Millisecond) // hang 500 + delay 700
	if !g.r.PTT() {
		t.Fatal("PTT dropped before hang plus the transmit-off delay")
	}
	g.run(100 * time.Millisecond)
	if g.r.PTT() {
		t.Fatal("PTT still keyed after hang plus the transmit-off delay")
	}
}
