package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// rig is an engine on a fake radio with fake modes and a fake time.
type rig struct {
	t     *testing.T
	e     *Engine
	r     *radio.Fake
	mu    sync.Mutex
	now   time.Time
	fakes map[string]*modetest.Fake
}

func off() *bool { b := false; return &b }

func intp(v int) *int { return &v }

// quietConfig is a station with the AGCs and ID off, so a
// test turns on only what it checks.
func quietConfig() *Config {
	return &Config{
		Version: ConfigVersion,
		Station: Station{
			Callsign: "N0CALL",
			ID:       IDConfig{Enabled: off()},
			// No key-up delay: tests count frames from the step PTT keys.
			Advanced: Advanced{ModeAGC: off(), RadioAGC: off(), TxOnDelayMS: intp(0)},
		},
		Modes: map[string]mode.Settings{
			mode.AllStar: {Values: map[string]string{"node": "1999"}},
			mode.BM:      {Values: map[string]string{"dmr_id": "1234567"}},
		},
	}
}

func newRig(t *testing.T, cfg *Config) *rig { return newRigAt(t, cfg, "") }

// newRigAt is newRig with the remembered tunes kept in the file tunePath,
// so a second rig on the same path is a restart.
func newRigAt(t *testing.T, cfg *Config, tunePath string) *rig {
	t.Helper()
	g := &rig{t: t, r: radio.NewFake(), now: t0, fakes: map[string]*modetest.Fake{}}
	e, err := New(cfg, Options{
		Radio:    g.r,
		TunePath: tunePath,
		Now:      g.clock,
		Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			f := modetest.New(n)
			g.fakes[n] = f
			return f, nil
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
	return g
}

func (g *rig) clock() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.now }

func (g *rig) fake(n string) *modetest.Fake {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.fakes[n]
	if f == nil {
		g.t.Fatalf("mode %s was not built", n)
	}
	return f
}

// step runs n frames.
func (g *rig) step(n int) {
	for i := 0; i < n; i++ {
		g.mu.Lock()
		g.now = g.now.Add(audio.FrameDuration)
		now := g.now
		g.mu.Unlock()
		g.e.Step(now)
	}
}

// run runs d worth of frames.
func (g *rig) run(d time.Duration) { g.step(int(d / audio.FrameDuration)) }

func (g *rig) sel(n string, t *mode.Target) {
	g.t.Helper()
	if err := g.e.Select(context.Background(), n, t); err != nil {
		g.t.Fatalf("select %s: %v", n, err)
	}
}

func frame(v int16) audio.Frame {
	var f audio.Frame
	for i := range f {
		f[i] = v
	}
	return f
}

func loud(f audio.Frame) bool {
	for _, s := range f {
		if s != 0 {
			return true
		}
	}
	return false
}

func countLoud(fs []audio.Frame) int {
	n := 0
	for _, f := range fs {
		if loud(f) {
			n++
		}
	}
	return n
}

// keyings counts how many times the transmitter keyed.
func keyings(log []bool) int {
	n := 0
	for _, on := range log {
		if on {
			n++
		}
	}
	return n
}

func TestOneModeAtATime(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.AllStar, &mode.Target{ID: "2000"})
	as := g.fake(mode.AllStar)
	if !as.Running() {
		t.Fatal("allstar not started")
	}
	g.sel(mode.BM, &mode.Target{ID: "91"})
	bm := g.fake(mode.BM)
	if as.Running() {
		t.Fatal("allstar still running after switching to bm")
	}
	if _, stopped, _, _ := as.Counts(); stopped != 1 {
		t.Fatalf("allstar stopped %d times, want 1", stopped)
	}
	if !bm.Running() || len(bm.Connects()) != 1 || bm.Connects()[0].ID != "91" {
		t.Fatalf("bm running=%v connects=%v", bm.Running(), bm.Connects())
	}
	if st := g.e.Status(); st.Active != mode.BM || st.Mode == nil || st.Mode.State != mode.StateConnected {
		t.Fatalf("status = %+v", st)
	}

	// The old mode's audio never reaches the radio.
	as.Key("N1CALL", frame(1000), frame(1000))
	g.step(10)
	if g.r.PTT() || len(g.r.Played()) != 0 {
		t.Fatal("audio from an inactive mode reached the radio")
	}

	// The radio's audio goes to the active mode only.
	if g.e.Status().Radio.RxActive {
		t.Fatal("RxActive without a carrier")
	}
	g.r.SetCarrier(true)
	for i := 0; i < 5; i++ {
		g.r.Inject(frame(500))
		g.step(1)
	}
	if !g.e.Status().Radio.RxActive {
		t.Fatal("the status does not show the local transmission")
	}
	g.r.SetCarrier(false)
	g.step(1)
	if g.e.Status().Radio.RxActive {
		t.Fatal("RxActive after the carrier dropped")
	}
	if len(bm.Sent()) != 5 || len(as.Sent()) != 0 {
		t.Fatalf("bm got %d frames, allstar %d; want 5 and 0", len(bm.Sent()), len(as.Sent()))
	}
	if _, _, _, ends := bm.Counts(); ends != 1 {
		t.Fatalf("bm EndTx %d, want 1", ends)
	}

	// Idle.
	g.sel("", nil)
	if bm.Running() || g.e.Status().Active != "" {
		t.Fatal("bm still active after going idle")
	}
	if a := g.e.Config().Station.Active; a != nil {
		t.Fatalf("idle not saved: %+v", a)
	}
}

func TestSelectSavesAndRestores(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "tune.json")
	g := newRigAt(t, quietConfig(), tp)
	g.sel(mode.BM, &mode.Target{ID: "91"})
	cfg := g.e.Config()
	if a := cfg.Station.Active; a == nil || a.Mode != mode.BM || a.Target != nil {
		t.Fatalf("active = %+v (the target belongs to the tune, not the config)", cfg.Station.Active)
	}
	if tn, ok := g.e.tuneOf(mode.BM); !ok || tn.Target == nil || tn.Target.ID != "91" || tn.Unlinked {
		t.Fatalf("tune = %+v", tn)
	}
	g2 := newRigAt(t, cfg, tp)
	bm := g2.fake(mode.BM)
	if !bm.Running() || len(bm.Connects()) != 1 || bm.Connects()[0].ID != "91" {
		t.Fatal("active mode and target not restored")
	}
	// Reconnecting the same mode keeps it running.
	if err := g2.e.Connect(context.Background(), mode.Target{ID: "3100"}); err != nil {
		t.Fatal(err)
	}
	if started, stopped, _, _ := bm.Counts(); started != 1 || stopped != 0 {
		t.Fatalf("started %d stopped %d, want 1 0", started, stopped)
	}
	if err := g2.e.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tn, ok := g2.e.tuneOf(mode.BM); !ok || tn.Target != nil || !tn.Unlinked {
		t.Fatalf("after disconnect tune = %+v", tn)
	}
	// An explicit disconnect survives a restart: the mode comes back
	// active but not linked.
	g3 := newRigAt(t, g2.e.Config(), tp)
	if bm3 := g3.fake(mode.BM); !bm3.Running() || len(bm3.Connects()) != 0 {
		t.Fatalf("restart after an unlink: running=%v connects=%v", bm3.Running(), bm3.Connects())
	}
}

// Every mode remembers its own last target and reconnects to it when it is
// activated, unless the operator disconnected it.
func TestReactivateReconnectsLastTarget(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.AllStar, &mode.Target{ID: "2000"})
	g.sel(mode.BM, &mode.Target{ID: "91"})
	g.sel(mode.AllStar, nil)
	as := g.fake(mode.AllStar)
	if c := as.Connects(); len(c) != 2 || c[1].ID != "2000" {
		t.Fatalf("allstar connects %+v, want a second connect to 2000", c)
	}
	g.sel(mode.BM, nil)
	if c := g.fake(mode.BM).Connects(); len(c) != 2 || c[1].ID != "91" {
		t.Fatalf("bm connects %+v", c)
	}
	// Selecting the mode that is already active does not link again.
	g.sel(mode.BM, nil)
	if c := g.fake(mode.BM).Connects(); len(c) != 2 {
		t.Fatalf("bm connects %+v after a repeated select", c)
	}
	// An explicit disconnect forgets the link; a later activation stays unlinked.
	if err := g.e.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.sel(mode.AllStar, nil)
	g.sel(mode.BM, nil)
	if c := g.fake(mode.BM).Connects(); len(c) != 2 {
		t.Fatalf("bm reconnected after an explicit disconnect: %+v", c)
	}
	if st := g.e.Status(); st.Active != mode.BM || st.Mode == nil || st.Mode.Target != nil {
		t.Fatalf("status %+v", st)
	}
	// A new explicit connect remembers again.
	g.sel(mode.BM, &mode.Target{ID: "3100"})
	g.sel(mode.AllStar, nil)
	g.sel(mode.BM, nil)
	if c := g.fake(mode.BM).Connects(); c[len(c)-1].ID != "3100" {
		t.Fatalf("bm connects %+v", c)
	}
}

// A link that drops, or a connect that fails, is not an unlink.
func TestDropAndFailureKeepTheTune(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.AllStar, &mode.Target{ID: "2000"})
	// The mode ends the link by itself, as AllStar does when the far
	// node disconnects us.
	_ = g.fake(mode.AllStar).Disconnect(context.Background())
	time.Sleep(20 * time.Millisecond)
	if tn, _ := g.e.tuneOf(mode.AllStar); tn.Target == nil || tn.Target.ID != "2000" || tn.Unlinked {
		t.Fatalf("tune after a drop = %+v", tn)
	}
	// A connect the mode refuses is not remembered.
	as := g.fake(mode.AllStar)
	as.ConnectFn = func(mode.Target) error { return errors.New("no such node") }
	if err := g.e.Connect(context.Background(), mode.Target{ID: "9999"}); err == nil {
		t.Fatal("a refused connect should fail")
	}
	if tn, _ := g.e.tuneOf(mode.AllStar); tn.Target == nil || tn.Target.ID != "2000" {
		t.Fatalf("tune after a refused connect = %+v", tn)
	}
	// A remembered target that no longer connects does not stop the mode
	// from becoming active.
	g.sel(mode.BM, nil)
	if err := g.e.Select(context.Background(), mode.AllStar, nil); err != nil {
		t.Fatalf("activating with a stale tune: %v", err)
	}
	if g.e.Status().Active != mode.AllStar {
		t.Fatal("allstar not active")
	}
	if tn, _ := g.e.tuneOf(mode.AllStar); tn.Target == nil || tn.Target.ID != "2000" {
		t.Fatalf("the stale tune was lost: %+v", tn)
	}
}

// An old config's target in Station.Active seeds the mode's tune once.
func TestMigrateActiveTarget(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "tune.json")
	cfg := quietConfig()
	cfg.Station.Active = &Selection{Mode: mode.BM, Target: &mode.Target{ID: "91"}}
	g := newRigAt(t, cfg, tp)
	if c := g.fake(mode.BM).Connects(); len(c) != 1 || c[0].ID != "91" {
		t.Fatalf("connects %+v", c)
	}
	if a := g.e.Config().Station.Active; a == nil || a.Mode != mode.BM || a.Target != nil {
		t.Fatalf("active = %+v", a)
	}
	// An existing tune wins over the old target.
	if err := os.WriteFile(tp, []byte(`{"version":1,"modes":{"bm":{"unlinked":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = quietConfig()
	cfg.Station.Active = &Selection{Mode: mode.BM, Target: &mode.Target{ID: "91"}}
	g = newRigAt(t, cfg, tp)
	if c := g.fake(mode.BM).Connects(); len(c) != 0 {
		t.Fatalf("the old target overrode the remembered unlink: %+v", c)
	}
}

func TestTuneFileSurvivesBadContent(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "tune.json")
	if err := os.WriteFile(tp, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newRigAt(t, quietConfig(), tp)
	g.sel(mode.BM, &mode.Target{ID: "91"})
	m, err := loadTunes(tp)
	if err != nil || m[mode.BM].Target == nil || m[mode.BM].Target.ID != "91" {
		t.Fatalf("tunes %+v, %v", m, err)
	}
	fi, err := os.Stat(tp)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("tune file %v %v", fi, err)
	}
}

func TestPTTAndHang(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, &mode.Target{ID: "91"})
	bm := g.fake(mode.BM)
	bm.Key("N1CALL", frame(1000), frame(2000), frame(3000))
	g.step(1)
	if !g.r.PTT() {
		t.Fatal("PTT not keyed for network audio")
	}
	g.step(2)
	bm.Unkey()
	played := g.r.Played()
	if len(played) != 3 || played[0][0] != 1000 || played[2][0] != 3000 {
		t.Fatalf("played %d frames: %v...", len(played), played[0][0])
	}
	// The hang holds the transmitter for 500 ms, then it drops.
	g.run(480 * time.Millisecond)
	if !g.r.PTT() {
		t.Fatal("PTT dropped before the hang ran out")
	}
	g.run(40 * time.Millisecond)
	if g.r.PTT() {
		t.Fatal("PTT still keyed after the hang")
	}
	if log := g.r.PTTLog(); keyings(log) != 1 {
		t.Fatalf("PTT log %v", log)
	}
	if len(bm.Sent()) != 0 {
		t.Fatal("the radio's own transmit audio was sent to the mode")
	}
}

func TestReceiverHasPriority(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	g.r.SetCarrier(true)
	bm.Key("N1CALL", frame(1000))
	g.step(10)
	if g.r.PTT() {
		t.Fatal("keyed over a local station")
	}
	g.r.SetCarrier(false)
	g.step(1)
	if !g.r.PTT() {
		t.Fatal("network still talking but not keyed once the local station stopped")
	}
}

func TestNoKeyAfterLocalTransmission(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	g.r.SetCarrier(true)
	for i := 0; i < 10; i++ {
		g.r.Inject(frame(700))
		g.step(1)
	}
	g.r.SetCarrier(false)
	g.run(2 * time.Second)
	if keyings(g.r.PTTLog()) != 0 || g.r.PTT() {
		t.Fatalf("PTT log %v, want the radio never keyed", g.r.PTTLog())
	}
	if countLoud(g.r.Played()) != 0 {
		t.Fatal("audio played on the radio after a local transmission")
	}
	if len(bm.Sent()) != 10 {
		t.Fatalf("mode got %d frames, want 10", len(bm.Sent()))
	}
}

func TestTimeouts(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.TxTimeoutS = 2
	g := newRig(t, cfg)
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)

	// The network keys the transmitter too long: it is cut off until the
	// network stops.
	bm.Key("N1CALL")
	g.run(time.Second)
	if !g.r.PTT() {
		t.Fatal("not keyed")
	}
	g.run(1600 * time.Millisecond)
	if g.r.PTT() || !g.e.Status().Radio.TxLocked {
		t.Fatalf("not timed out: ptt=%v status=%+v", g.r.PTT(), g.e.Status().Radio)
	}
	g.run(3 * time.Second)
	if g.r.PTT() {
		t.Fatal("rekeyed while still locked out")
	}
	bm.Unkey()
	g.step(1)
	bm.Key("N1CALL")
	g.step(1)
	if !g.r.PTT() {
		t.Fatal("not keyed after the network stopped and started again")
	}
	bm.Unkey()
	g.run(time.Second)

	// A local station transmits too long: the mode hears the end.
	g.r.SetCarrier(true)
	for i := 0; i < 150; i++ {
		g.r.Inject(frame(100))
		g.step(1)
	}
	sent := len(bm.Sent())
	if sent != 100 {
		t.Fatalf("mode got %d frames, want 100 (2 s)", sent)
	}
	if _, _, _, ends := bm.Counts(); ends != 1 {
		t.Fatalf("EndTx %d, want 1 at the timeout", ends)
	}
	if !g.e.Status().Radio.RxLocked {
		t.Fatal("receive not locked")
	}
	g.r.SetCarrier(false)
	g.step(1)
	g.r.SetCarrier(true)
	g.r.Inject(frame(100))
	g.step(1)
	if len(bm.Sent()) != sent+1 {
		t.Fatal("receive still locked after the carrier dropped")
	}
}

func idConfig() *Config {
	cfg := quietConfig()
	cfg.Station.ID = IDConfig{} // defaults: on, 600 s, tail 60 s, end 60 s
	cfg.Station.Advanced.TxTimeoutS = 1800
	return cfg
}

func TestIDTiming(t *testing.T) {
	type burst struct{ at, length time.Duration }
	cases := []struct {
		name   string
		bursts []burst
		until  time.Duration
		want   []idReason // in order
		// keyings is how many times the transmitter keys in all.
		keyings int
	}{
		{name: "idle is silent", until: 30 * time.Minute, keyings: 0},
		{
			name: "end ID after activity", bursts: []burst{{0, 5 * time.Second}},
			until: 3 * time.Minute, want: []idReason{idEnd}, keyings: 2,
		},
		{
			name: "deadline over a long transmission", bursts: []burst{{0, 11 * time.Minute}},
			until: 13 * time.Minute, want: []idReason{idDeadline, idEnd}, keyings: 2,
		},
		{
			// Bursts with short gaps: the ID rides the hang of the last
			// one once the tail window opens; nothing is sent after it, so
			// no further ID is owed.
			name: "tail", bursts: func() []burst {
				var b []burst
				for at := time.Duration(0); at < 570*time.Second; at += 40 * time.Second {
					b = append(b, burst{at, 10 * time.Second})
				}
				return b
			}(),
			until: 12 * time.Minute, want: []idReason{idTail},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, idConfig())
			g.sel(mode.BM, nil)
			bm := g.fake(mode.BM)
			evs, stop := g.e.Subscribe()
			defer stop()
			var got []idReason
			collect := func() {
				for {
					select {
					case ev := <-evs:
						if ev.Kind == EventID {
							got = append(got, idReason(ev.Text))
						}
					default:
						return
					}
				}
			}
			start := g.clock()
			for g.clock().Sub(start) < c.until {
				el := g.clock().Sub(start)
				talk := false
				for _, b := range c.bursts {
					if el >= b.at && el < b.at+b.length {
						talk = true
					}
				}
				if talk && !bm.Talking() {
					bm.Key("N1CALL")
				} else if !talk && bm.Talking() {
					bm.Unkey()
				}
				g.step(1)
				collect()
			}
			if len(got) != len(c.want) {
				t.Fatalf("IDs %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("IDs %v, want %v", got, c.want)
				}
			}
			if c.keyings != 0 || len(c.want) == 0 {
				if n := keyings(g.r.PTTLog()); n != c.keyings {
					t.Fatalf("keyed %d times, want %d", n, c.keyings)
				}
			}
			if g.r.PTT() {
				t.Fatal("still keyed at the end")
			}
			if len(bm.Sent()) != 0 {
				t.Fatal("ID audio reached the mode")
			}
			if len(c.want) > 0 && countLoud(g.r.Played()) == 0 {
				t.Fatal("no ID audio played")
			}
			if g.e.Status().ID.Owed {
				t.Fatal("ID still owed at the end")
			}
		})
	}
}

func TestTailIDKeepsCarrier(t *testing.T) {
	g := newRig(t, idConfig())
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	// One burst inside the tail window: talk 545 s after the first.
	bm.Key("N1CALL")
	g.run(5 * time.Second)
	bm.Unkey()
	// Keep it owed: talk again within the end-quiet time, every 50 s.
	for i := 0; i < 11; i++ {
		g.run(45 * time.Second)
		bm.Key("N1CALL")
		g.run(5 * time.Second)
		bm.Unkey()
		if g.e.Status().ID.Why == string(idTail) {
			break
		}
	}
	keysBefore := keyings(g.r.PTTLog())
	g.run(5 * time.Second) // the hang, then the ID on it
	if why := g.e.Status().ID.Why; why != string(idTail) {
		t.Fatalf("last ID %q, want tail", why)
	}
	if keyings(g.r.PTTLog()) != keysBefore {
		t.Fatal("the tail ID keyed the transmitter again instead of riding the hang")
	}
}

func TestIDNeverDuringLocalTransmission(t *testing.T) {
	g := newRig(t, idConfig())
	g.sel(mode.BM, nil)
	bm := g.fake(mode.BM)
	bm.Key("N1CALL")
	g.run(5 * time.Second)
	bm.Unkey()
	g.run(time.Second)
	// A local station transmits through the end-ID time.
	g.r.SetCarrier(true)
	for i := 0; i < 5000; i++ { // 100 s
		g.r.Inject(frame(300))
		g.step(1)
		if g.r.PTT() {
			t.Fatal("keyed over a local station")
		}
	}
	for _, f := range bm.Sent() {
		if f[0] != 300 {
			t.Fatal("ID audio reached the mode")
		}
	}
	g.r.SetCarrier(false)
	g.run(5 * time.Second)
	if g.e.Status().ID.Why == "" {
		t.Fatal("no ID once the local station stopped")
	}
}

func TestHeardAndNowCalling(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, &mode.Target{ID: "91"})
	bm := g.fake(mode.BM)
	bm.Key("N1CALL")
	if nc := g.e.NowCalling(); nc == nil || nc.Talker.Callsign != "N1CALL" || nc.Mode != mode.BM {
		t.Fatalf("now calling = %+v", nc)
	}
	waitFor(t, func() bool { h := g.e.Heard(0); return len(h) == 1 && h[0].End.IsZero() })
	bm.Unkey()
	waitFor(t, func() bool { h := g.e.Heard(0); return len(h) == 1 && !h[0].End.IsZero() })
	if g.e.NowCalling() != nil {
		t.Fatal("still calling")
	}
	// An inactive mode's calls are not heard.
	as := g.fake(mode.AllStar)
	as.Key("N1CALL")
	time.Sleep(20 * time.Millisecond)
	if len(g.e.Heard(0)) != 1 {
		t.Fatal("an inactive mode's call was logged")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // generous for a loaded CI runner
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// bgFake is a fake with background work, like AllStar registration.
type bgFake struct {
	*modetest.Fake
	ran chan struct{}
}

func (b *bgFake) RunBackground(ctx context.Context) { close(b.ran); <-ctx.Done() }

func TestBackgroundRunsWhileInactive(t *testing.T) {
	ran := make(chan struct{})
	e, err := New(quietConfig(), Options{
		Radio: radio.NewFake(),
		Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) {
			if n == mode.AllStar {
				return &bgFake{Fake: modetest.New(n), ran: ran}, nil
			}
			return modetest.New(n), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("background work did not start")
	}
	if e.Status().Active != "" {
		t.Fatal("a mode became active by itself")
	}
}

func TestRunDropsPTT(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, nil)
	g.fake(mode.BM).Key("N1CALL")
	clk := audio.NewFakeClock(t0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.e.Run(ctx, clk); close(done) }()
	for i := 0; i < 5; i++ {
		clk.Tick()
	}
	if !g.r.PTT() {
		t.Fatal("not keyed")
	}
	cancel()
	<-done
	if g.r.PTT() {
		t.Fatal("PTT still keyed after Run returned")
	}
}

func TestUnkeyRetriedAfterFailure(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, &mode.Target{ID: "91"})
	bm := g.fake(mode.BM)
	bm.Key("N1CALL", frame(1000))
	g.step(2)
	bm.Unkey()
	if !g.r.PTT() {
		t.Fatal("not keyed")
	}
	g.r.FailPTT(errors.New("hid write failed"))
	g.run(600 * time.Millisecond) // past the hang
	if !g.r.PTT() {
		t.Fatal("fake radio unkeyed although SetPTT failed")
	}
	if st := g.e.Status(); st.Radio.PTT || !st.Radio.PTTStuck {
		t.Fatalf("radio status %+v", st.Radio)
	}
	g.r.FailPTT(nil)
	g.step(1)
	if g.r.PTT() {
		t.Fatal("unkey not retried")
	}
	if st := g.e.Status(); st.Radio.PTTStuck {
		t.Fatal("still marked stuck")
	}
}

func TestPlayoutPrebuffer(t *testing.T) {
	tests := []struct {
		name   string
		frames int
		steps  int // steps before the first frame plays
	}{
		{"enough queued", 2, 1},
		{"one frame waits for more", 1, 4}, // 60 ms after it arrived
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newRig(t, quietConfig())
			g.sel(mode.BM, &mode.Target{ID: "91"})
			bm := g.fake(mode.BM)
			var fs []audio.Frame
			for i := 0; i < tt.frames; i++ {
				fs = append(fs, frame(1000))
			}
			bm.Key("N1CALL", fs...)
			for i := 1; i <= tt.steps; i++ {
				g.step(1)
				played := g.r.Played()
				got := len(played) > 0 && played[len(played)-1][0] == 1000
				if want := i == tt.steps; got != want {
					t.Fatalf("step %d: played audio %v, want %v", i, got, want)
				}
			}
			bm.Unkey()
		})
	}
}

func TestRadioWiringChangeAsksForRestart(t *testing.T) {
	tests := []struct {
		name string
		edit func(*RadioConfig)
		want bool
	}{
		{"volume only", func(r *RadioConfig) { v := 0.7; r.RxVolume = &v }, false},
		{"COS polarity", func(r *RadioConfig) { v := true; r.COSActiveLow = &v }, true},
		{"PTT pin", func(r *RadioConfig) { r.PTTGPIO = 4 }, true},
		{"COS source", func(r *RadioConfig) { r.COSSource = "gpio1" }, true},
		{"preset", func(r *RadioConfig) { r.Preset = "uri" }, true},
		{"preset that was already chosen", func(r *RadioConfig) { r.Preset = "shari" }, false},
		{"override equal to the preset", func(r *RadioConfig) { r.PTTGPIO = 3; v := false; r.COSActiveLow = &v }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := make(chan struct{}, 1)
			g := &rig{t: t, r: radio.NewFake(), now: t0, fakes: map[string]*modetest.Fake{}}
			cfg := quietConfig()
			cfg.Modes = nil
			e, err := New(cfg, Options{Radio: g.r, Now: g.clock,
				RadioChanged: func() { called <- struct{}{} },
				Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) {
					return modetest.New(n), nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			st := e.Config().Station
			tt.edit(&st.Radio)
			if err := e.SaveStation(st); err != nil {
				t.Fatal(err)
			}
			// RadioChanged runs in its own goroutine: give it long when it
			// is expected, and a moment to show up when it is not
			wait := 100 * time.Millisecond
			if tt.want {
				wait = 10 * time.Second
			}
			got := false
			select {
			case <-called:
				got = true
			case <-time.After(wait):
			}
			if got != tt.want || e.Status().RestartRequired != tt.want {
				t.Fatalf("restart asked %v, status %v, want %v", got, e.Status().RestartRequired, tt.want)
			}
		})
	}
}

// TestKeyUpDelay: network audio waits for the key-up delay after PTT, so
// the first word reaches a transmitter that is up; nothing is lost and
// the order is kept.
func TestKeyUpDelay(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Advanced.TxOnDelayMS = nil // the default, 500 ms
	g := newRig(t, cfg)
	g.sel(mode.BM, &mode.Target{ID: "91"})
	bm := g.fake(mode.BM)
	var fs []audio.Frame
	for i := 0; i < 20; i++ {
		fs = append(fs, frame(int16(1000+i)))
	}
	bm.Key("N1CALL", fs...)
	g.step(1)
	if !g.r.PTT() {
		t.Fatal("PTT not keyed for network audio")
	}
	keyed := g.clock()
	var first time.Time
	for i := 0; i < 50 && first.IsZero(); i++ {
		g.step(1)
		if countLoud(g.r.Played()) > 0 {
			first = g.clock()
		}
	}
	if d := first.Sub(keyed); first.IsZero() || d < DefaultTxOnDelay || d > DefaultTxOnDelay+audio.FrameDuration {
		t.Fatalf("first frame played %v after PTT, want %v", d, DefaultTxOnDelay)
	}
	g.run(time.Second)
	bm.Unkey()
	var got []int16
	for _, f := range g.r.Played() {
		if loud(f) {
			got = append(got, f[0])
		}
	}
	if len(got) != 20 || got[0] != 1000 || got[19] != 1019 {
		t.Fatalf("played %v", got)
	}
	// A transmission that starts while the transmitter is still keyed
	// (the hang) plays at once.
	g.run(100 * time.Millisecond)
	bm.Key("N1CALL", frame(2000), frame(2001))
	g.step(1)
	if p := g.r.Played(); p[len(p)-1][0] != 2000 {
		t.Fatal("audio held although the transmitter was already up")
	}
	bm.Unkey()
}

// TestNetDroppedAndRxEvents: network audio thrown away while a local
// station has the receiver is counted, and the start and end of a local
// transmission going to the mode are published (the Dashboard's "Local
// station").
func TestNetDroppedAndRxEvents(t *testing.T) {
	g := newRig(t, quietConfig())
	g.sel(mode.BM, &mode.Target{ID: "91"})
	bm := g.fake(mode.BM)
	evs, stop := g.e.Subscribe()
	defer stop()
	g.r.SetCarrier(true)
	g.step(1)
	bm.Key("N1CALL", frame(1000), frame(1001), frame(1002), frame(1003), frame(1004))
	g.step(10)
	if st := g.e.Status().Radio; !st.RxActive || st.NetDropped != 5 {
		t.Fatalf("radio %+v, want rxActive and 5 frames not played", st)
	}
	g.r.SetCarrier(false)
	bm.Unkey()
	g.step(2)
	if st := g.e.Status().Radio; st.RxActive {
		t.Fatalf("radio %+v, want rxActive off", st)
	}
	var rx []bool
	for len(evs) > 0 {
		if ev := <-evs; ev.Kind == EventRx && ev.On != nil {
			rx = append(rx, *ev.On)
		}
	}
	if len(rx) != 2 || !rx[0] || rx[1] {
		t.Fatalf("rx events %v, want [true false]", rx)
	}
	// A mode change starts the count again.
	g.sel(mode.AllStar, nil)
	if n := g.e.Status().Radio.NetDropped; n != 0 {
		t.Fatalf("netDropped %d after a mode change", n)
	}
}
