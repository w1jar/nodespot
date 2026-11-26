package radio

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/radio/alsa"
	"github.com/w1jar/nodespot/internal/native/radio/cm108"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

// Compile-time checks that the real devices fit the interfaces.
var (
	_ Radio       = (*Fake)(nil)
	_ FrameSource = (*alsa.Capture)(nil)
	_ FrameSink   = (*alsa.Playback)(nil)
	_ Keyer       = (*cm108.Device)(nil)
	_ Mixer       = (*alsa.Mixer)(nil)
)

type fakeSource struct {
	ch     chan Frame
	closed chan struct{}
	once   sync.Once
}

func newFakeSource() *fakeSource {
	return &fakeSource{ch: make(chan Frame, 200), closed: make(chan struct{})}
}
func (s *fakeSource) ReadFrame() (Frame, error) {
	select {
	case f := <-s.ch:
		return f, nil
	case <-s.closed:
		return Frame{}, alsa.ErrClosed
	}
}
func (s *fakeSource) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

type fakeSink struct {
	mu     sync.Mutex
	frames []Frame
	stops  int
	closed bool
	block  chan struct{} // when non-nil, WriteFrame waits on it
}

func (s *fakeSink) WriteFrame(f Frame) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	s.frames = append(s.frames, f)
	s.mu.Unlock()
	return nil
}
func (s *fakeSink) Stop() error  { s.mu.Lock(); s.stops++; s.mu.Unlock(); return nil }
func (s *fakeSink) Close() error { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }
func (s *fakeSink) got() ([]Frame, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Frame(nil), s.frames...), s.stops
}

type fakeKeyer struct {
	mu      sync.Mutex
	ptt     bool
	log     []bool
	cos     bool
	changes chan bool
	closed  bool
	failSet bool
}

func (k *fakeKeyer) SetPTT(on bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failSet {
		return errors.New("hid write failed")
	}
	k.ptt = on
	k.log = append(k.log, on)
	return nil
}
func (k *fakeKeyer) PTT() bool            { k.mu.Lock(); defer k.mu.Unlock(); return k.ptt }
func (k *fakeKeyer) COS() bool            { k.mu.Lock(); defer k.mu.Unlock(); return k.cos }
func (k *fakeKeyer) Changes() <-chan bool { return k.changes }
func (k *fakeKeyer) Close() error         { k.mu.Lock(); k.closed = true; k.mu.Unlock(); return nil }

type fakeMixer struct{ c, p float64 }

func (m *fakeMixer) SetCaptureVolume(v float64) error  { m.c = v; return nil }
func (m *fakeMixer) SetPlaybackVolume(v float64) error { m.p = v; return nil }
func (m *fakeMixer) CaptureVolume() (float64, error)   { return m.c, nil }
func (m *fakeMixer) PlaybackVolume() (float64, error)  { return m.p, nil }
func (m *fakeMixer) Close() error                      { return nil }

func frameOf(v int16) Frame {
	var f Frame
	for i := range f {
		f[i] = v
	}
	return f
}

func newCore() (Radio, *fakeSource, *fakeSink, *fakeKeyer, *fakeMixer) {
	src, sink := newFakeSource(), &fakeSink{}
	key := &fakeKeyer{changes: make(chan bool, 4)}
	mix := &fakeMixer{}
	return New(Parts{Capture: src, Playback: sink, PTT: key, Mixer: mix}), src, sink, key, mix
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCoreCapture(t *testing.T) {
	r, src, _, _, _ := newCore()
	defer r.Close()
	src.ch <- frameOf(100)
	src.ch <- frameOf(200)
	for _, want := range []int16{100, 200} {
		select {
		case f := <-r.Capture():
			if f[0] != want || f[159] != want {
				t.Fatalf("frame = %d, want %d", f[0], want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no frame")
		}
	}
	waitFor(t, "rx meter", func() bool { return r.Levels().Rx.Frames == 2 })
}

func TestCoreCaptureDropsWhenSlow(t *testing.T) {
	r, src, _, _, _ := newCore()
	defer r.Close()
	for i := 0; i < captureQueue+20; i++ {
		src.ch <- frameOf(1)
	}
	waitFor(t, "drops", func() bool { return r.Levels().DroppedRx >= 20 })
}

func TestCorePlayAndPTT(t *testing.T) {
	r, _, sink, key, _ := newCore()
	defer r.Close()
	if err := r.SetPTT(true); err != nil {
		t.Fatal(err)
	}
	if !r.PTT() {
		t.Error("PTT not set")
	}
	r.Play(frameOf(7))
	r.Play(frameOf(8))
	waitFor(t, "frames played", func() bool { f, _ := sink.got(); return len(f) == 2 })
	if f, _ := sink.got(); f[0][0] != 7 || f[1][0] != 8 {
		t.Errorf("order wrong: %d %d", f[0][0], f[1][0])
	}
	_, stops := sink.got()
	if err := r.SetPTT(false); err != nil {
		t.Fatal(err)
	}
	if _, s := sink.got(); s <= stops {
		t.Error("unkeying did not stop the sink")
	}
	if got := key.log; len(got) != 2 || !got[0] || got[1] {
		t.Errorf("PTT log = %v", got)
	}
	waitFor(t, "tx meter", func() bool { return r.Levels().Tx.Frames == 2 })
}

func TestCorePlayIdleStops(t *testing.T) {
	r, _, sink, _, _ := newCore()
	defer r.Close()
	r.Play(frameOf(1))
	waitFor(t, "idle stop", func() bool { _, s := sink.got(); return s >= 1 })
}

func TestCorePlayNeverBlocks(t *testing.T) {
	src, sink := newFakeSource(), &fakeSink{block: make(chan struct{})}
	key := &fakeKeyer{changes: make(chan bool)}
	r := New(Parts{Capture: src, Playback: sink, PTT: key})
	done := make(chan struct{})
	go func() {
		for i := 0; i < playQueue*3; i++ {
			r.Play(frameOf(1))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Play blocked")
	}
	if r.Levels().DroppedTx == 0 {
		t.Error("no drops counted")
	}
	close(sink.block)
	r.Close()
}

func TestCoreCOS(t *testing.T) {
	r, _, _, key, _ := newCore()
	defer r.Close()
	key.mu.Lock()
	key.cos = true
	key.mu.Unlock()
	key.changes <- true
	select {
	case v := <-r.COS():
		if !v {
			t.Error("want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no COS event")
	}
	if !r.Carrier() {
		t.Error("Carrier() false")
	}
}

func TestCoreVolumes(t *testing.T) {
	r, _, _, _, mix := newCore()
	defer r.Close()
	if err := r.SetVolumes(0.25, 0.75); err != nil {
		t.Fatal(err)
	}
	if mix.c != 0.25 || mix.p != 0.75 {
		t.Errorf("mixer = %v %v", mix.c, mix.p)
	}
	if l := r.Levels(); l.CaptureVolume != 0.25 || l.PlaybackVolume != 0.75 {
		t.Errorf("levels = %+v", l)
	}
	// No mixer at all.
	r2 := New(Parts{Capture: newFakeSource(), Playback: &fakeSink{}, PTT: &fakeKeyer{changes: make(chan bool)}})
	defer r2.Close()
	if err := r2.SetVolumes(1, 1); err == nil {
		t.Error("expected error without a mixer")
	}
	if l := r2.Levels(); l.CaptureVolume != -1 {
		t.Errorf("CaptureVolume = %v, want -1", l.CaptureVolume)
	}
}

func TestCoreCloseUnkeysAndClosesChannels(t *testing.T) {
	r, src, sink, key, _ := newCore()
	r.SetPTT(true)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if key.PTT() {
		t.Error("still keyed after Close")
	}
	if !key.closed || !sink.closed {
		t.Error("devices not closed")
	}
	select {
	case _, ok := <-r.Capture():
		if ok {
			t.Error("capture channel open")
		}
	case <-time.After(time.Second):
		t.Fatal("capture channel not closed")
	}
	if _, ok := <-r.COS(); ok {
		t.Error("COS channel open")
	}
	r.Close() // idempotent
	_ = src
}

func TestCoreSetPTTError(t *testing.T) {
	r, _, _, key, _ := newCore()
	key.failSet = true
	if err := r.SetPTT(true); err == nil {
		t.Error("expected error")
	}
	key.failSet = false
	r.Close()
}

func TestFake(t *testing.T) {
	f := NewFake()
	if f.Carrier() || f.PTT() {
		t.Fatal("fresh fake should be idle")
	}
	f.Inject(frameOf(5))
	if got := <-f.Capture(); got[0] != 5 {
		t.Errorf("captured %d", got[0])
	}
	f.SetCarrier(true)
	f.SetCarrier(true) // no duplicate event
	if v := <-f.COS(); !v {
		t.Error("want carrier on")
	}
	select {
	case v := <-f.COS():
		t.Errorf("duplicate COS event %v", v)
	default:
	}
	if !f.Carrier() {
		t.Error("Carrier false")
	}
	f.SetPTT(true)
	f.Play(frameOf(9))
	f.SetPTT(false)
	if p := f.Played(); len(p) != 1 || p[0][3] != 9 {
		t.Errorf("played = %v", len(p))
	}
	if log := f.PTTLog(); len(log) != 2 || !log[0] || log[1] {
		t.Errorf("ptt log = %v", log)
	}
	f.SetVolumes(0.1, 0.2)
	if l := f.Levels(); l.CaptureVolume != 0.1 || l.PlaybackVolume != 0.2 || l.Rx.Frames != 1 || l.Tx.Frames != 1 {
		t.Errorf("levels = %+v", l)
	}
	for i := 0; i < captureQueue+3; i++ {
		f.Inject(frameOf(1))
	}
	if f.Levels().DroppedRx == 0 {
		t.Error("no drops counted")
	}
	f.Close()
	f.Inject(frameOf(1)) // after close: ignored, no panic
	f.Play(frameOf(1))
	f.SetCarrier(false)
	f.Close()
}

func TestOpenNoRadio(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "bus", "usb", "devices"), 0o755)
	// A preset that needs a chip fails with ErrNoRadio.
	for _, p := range []Preset{PresetSHARI, PresetURI} {
		_, info, err := Open(Config{SysRoot: root, DevRoot: root, Preset: p})
		if !errors.Is(err, ErrNoRadio) || info.Present || info.Detected {
			t.Fatalf("%s: info %+v err %v, want ErrNoRadio", p, info, err)
		}
		if info.Interface.Preset != p {
			t.Errorf("%s: interface %+v", p, info.Interface)
		}
	}
	// No preset and nothing plugged in is a radioless node, not an error.
	for _, p := range []Preset{PresetAuto, PresetNone} {
		r, info, err := Open(Config{SysRoot: root, DevRoot: root, Preset: p})
		if err != nil || info.Present || info.Interface.Preset != PresetNone {
			t.Fatalf("%q: info %+v err %v", p, info, err)
		}
		if _, ok := r.(*Fake); !ok {
			t.Errorf("%q: radio is %T, want *Fake", p, r)
		}
		r.Close()
	}
	if _, _, err := Open(Config{SysRoot: root, DevRoot: root, Preset: "bogus"}); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestSavedSA818(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sa818.json")
	cfg := Config{SA818StatePath: path}
	if _, err := SavedSA818(cfg); !errors.Is(err, sa818.ErrNoState) {
		t.Fatalf("err = %v", err)
	}
	s := sa818.DefaultSettings()
	s.RxMHz, s.TxMHz = 146.52, 146.52
	if err := sa818.SaveState(path, sa818.State{Settings: s}); err != nil {
		t.Fatal(err)
	}
	st, err := SavedSA818(cfg)
	if err != nil || st.Settings != s {
		t.Fatalf("got %+v, %v", st, err)
	}
	if (Config{}).statePath() != "/etc/nodespot/sa818.json" {
		t.Error("default state path changed")
	}
}

type boostMixer struct {
	fakeMixer
	on bool
}

func (m *boostMixer) BoostAvailable() bool   { return true }
func (m *boostMixer) Boost() (bool, error)   { return m.on, nil }
func (m *boostMixer) SetBoost(on bool) error { m.on = on; return nil }

func TestCoreBoost(t *testing.T) {
	r, _, _, _, _ := newCore()
	defer r.Close()
	if l := r.Levels(); l.BoostAvailable {
		t.Fatal("boost reported on a mixer without it")
	}
	if err := r.(Booster).SetBoost(true); !errors.Is(err, ErrNoBoost) {
		t.Fatalf("SetBoost without a control: %v", err)
	}
	m := &boostMixer{}
	r2 := New(Parts{Capture: newFakeSource(), Playback: &fakeSink{}, PTT: &fakeKeyer{changes: make(chan bool, 4)}, Mixer: m})
	defer r2.Close()
	if err := r2.(Booster).SetBoost(true); err != nil || !m.on {
		t.Fatalf("SetBoost: %v, on %v", err, m.on)
	}
	if l := r2.Levels(); !l.BoostAvailable || !l.Boost {
		t.Fatalf("levels %+v", l)
	}
}
