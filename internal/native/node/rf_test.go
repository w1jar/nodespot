package node

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// frameFake is a fake mode that also carries AMBE frames.
type frameFake struct {
	*modetest.Fake
	ch chan mode.FrameEvent
}

func (f *frameFake) FrameCodec() (network.Codec, bool)   { return network.CodecAMBE, true }
func (f *frameFake) SetFrames(bool)                      {}
func (f *frameFake) RecvFrames() <-chan mode.FrameEvent  { return f.ch }
func (f *frameFake) SendHeader(network.CallHeader) error { return nil }
func (f *frameFake) SendFrame(network.VoiceFrame) error  { return nil }
func (f *frameFake) EndFrames() error                    { return nil }

// fakeRF records how the engine links modes to the RF endpoint.
type fakeRF struct {
	mu    sync.Mutex
	slots []int
	links []string // mode name, "" for an unlink
}

func (r *fakeRF) Slots() []int { return r.slots }
func (r *fakeRF) Link(slot int, m mode.FrameMode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := ""
	if m != nil {
		name = m.Name()
	}
	r.links = append(r.links, name)
	return nil
}
func (r *fakeRF) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.links) == 0 {
		return "(none)"
	}
	return r.links[len(r.links)-1]
}

func rfEngine(t *testing.T, rfl RFLink, m Modem, freq uint32) (*Engine, map[string]*modetest.Fake) {
	t.Helper()
	cfg := quietConfig()
	cfg.Station.Radio = RadioConfig{Preset: "mmdvm", MMDVM: &MMDVMConfig{RXFreqHz: freq}}
	iface, _ := radio.Resolve(radio.PresetMMDVM, radio.Overrides{}, false)
	var mu sync.Mutex
	fakes := map[string]*modetest.Fake{}
	e, err := New(cfg, Options{Radio: radio.NewFake(), RadioInfo: radio.Info{Interface: iface},
		Modem: m, ModemConfig: cfg.Station.Radio.MMDVM.Resolved(), RF: rfl, Now: time.Now,
		Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) {
			mu.Lock()
			defer mu.Unlock()
			f := modetest.New(n)
			fakes[n] = f
			if n == mode.BM {
				return &frameFake{Fake: f, ch: make(chan mode.FrameEvent, 4)}, nil
			}
			return f, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, fakes
}

// The active mode is linked to the RF endpoint when it carries AMBE, and
// unlinked (with a note) when it does not.
func TestEngineLinksRF(t *testing.T) {
	rfl := &fakeRF{slots: []int{2}}
	e, _ := rfEngine(t, rfl, &testModem{st: radio.ModemStatus{Open: true}}, 441000000)
	ctx := context.Background()
	if err := e.Select(ctx, mode.BM, &mode.Target{ID: "91"}); err != nil {
		t.Fatal(err)
	}
	if got := rfl.last(); got != mode.BM {
		t.Errorf("linked %q, want %s", got, mode.BM)
	}
	if n := e.ModemInfo().Notes; len(strings.Join(n, " ")) > 0 && strings.Contains(strings.Join(n, " "), "RF calls are not passed") {
		t.Errorf("notes %v", n)
	}
	if err := e.Select(ctx, mode.AllStar, &mode.Target{ID: "2000"}); err != nil {
		t.Fatal(err)
	}
	if got := rfl.last(); got != "" {
		t.Errorf("linked %q with AllStar active", got)
	}
	notes := strings.Join(e.ModemInfo().Notes, " ")
	if !strings.Contains(notes, "RF calls are not passed to allstar") {
		t.Errorf("notes %q", notes)
	}
	if err := e.Select(ctx, "none", nil); err != nil {
		t.Fatal(err)
	}
	if got := rfl.last(); got != "" {
		t.Errorf("linked %q when idle", got)
	}
}

// An RF talker is heard and shown as via RF, with its slot.
func TestRFTalkerHeardViaRF(t *testing.T) {
	e, fakes := rfEngine(t, &fakeRF{slots: []int{2}}, nil, 441000000)
	if err := e.Select(context.Background(), mode.BM, &mode.Target{ID: "91"}); err != nil {
		t.Fatal(err)
	}
	fakes[mode.BM].KeyTalker(mode.Talker{ID: 1234567, Target: "91", Via: "rf", Slot: 2})
	waitFor(t, func() bool { h := e.Heard(0); return len(h) == 1 && h[0].End.IsZero() })
	h := e.Heard(0)[0]
	if h.Via != "rf" || h.Slot != 2 || h.ID != 1234567 || h.Target != "91" {
		t.Errorf("heard %+v", h)
	}
	if nc := e.NowCalling(); nc == nil || nc.Talker.Via != "rf" || nc.Talker.Slot != 2 {
		t.Errorf("now calling %+v", nc)
	}
	fakes[mode.BM].Unkey()
	waitFor(t, func() bool { h := e.Heard(0); return len(h) == 1 && !h[0].End.IsZero() })
}

func TestModemInfoReceiveStates(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		freq  uint32
		st    radio.ModemStatus
		state string
		note  string
	}{
		{"no frequency", 0, radio.ModemStatus{Open: true}, "detect", "Set the receive frequency"},
		{"not open yet", 441000000, radio.ModemStatus{}, "configured", "simplex on slot 2"},
		{"listening", 441000000, radio.ModemStatus{Open: true}, "listening", "Duplex receive needs transmit"},
		{"receiving", 441000000, radio.ModemStatus{Open: true,
			RXCall: &radio.RXCall{Slot: 2, Src: 1234567, Dst: 91, Since: now}}, "receiving", "later release"},
	}
	for _, c := range cases {
		e, _ := rfEngine(t, nil, &testModem{st: c.st}, c.freq)
		mi := e.ModemInfo()
		if mi.RXState != c.state || mi.Receive != (c.freq != 0) || mi.DetectOnly != (c.freq == 0) {
			t.Errorf("%s: %+v", c.name, mi)
		}
		if !strings.Contains(strings.Join(mi.Notes, " "), c.note) {
			t.Errorf("%s: notes %v lack %q", c.name, mi.Notes, c.note)
		}
		if c.state == "receiving" && (mi.RXCall == nil || mi.RXCall.Src != 1234567 || mi.RXCall.Dst != 91 || mi.RXCall.Slot != 2) {
			t.Errorf("%s: call %+v", c.name, mi.RXCall)
		}
	}
}
