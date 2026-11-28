package calib_test

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/calib"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// Placeholder UHF frequency (outside the 435 to 438 MHz ban).
const baseRX = 433_500_000

const testSrc = 1234567

func fastSettings(m *fake.Modem) mmdvm.Settings {
	return mmdvm.Settings{
		Open:           m.Open,
		Modem:          mmdvm.Config{ColorCode: 1, RXLevel: 50},
		StatusInterval: 2 * time.Millisecond,
		Inactivity:     time.Second,
		BootWait:       time.Millisecond,
		VersionRetry:   5 * time.Millisecond,
		ReplyTimeout:   500 * time.Millisecond,
		Playout:        time.Millisecond,
		ReopenDelay:    time.Millisecond,
		RXTimeout:      time.Hour,
	}
}

func freqFor(off int) mmdvm.Freq {
	return mmdvm.Freq{RX: uint32(baseRX + off), TX: baseRX}
}

type rig struct {
	t   *testing.T
	ctx context.Context
	m   *fake.Modem
	s   *calib.Session
}

// newRig opens a session on a fake modem whose best receive frequency is
// baseRX+center.
func newRig(t *testing.T, saved int, model *fake.RXErrorModel) *rig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	m := fake.New(fake.Options{})
	if model != nil {
		m.SetRXErrors(model)
	}
	s := calib.NewSession(fastSettings(m), freqFor, mmdvm.FreqRules{UHFOnly: true}, saved)
	if err := s.Start(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &rig{t: t, ctx: ctx, m: m, s: s}
}

func (r *rig) play(n int) {
	r.t.Helper()
	ambe := make([]dmr.AMBE, n)
	for i := range ambe {
		ambe[i] = dmr.AMBE{byte(i), byte(i * 7), 0x5A, byte(255 - i), 0x33, byte(i * 3), 0x80}
	}
	err := r.m.PlayCall(r.ctx, fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: ambe, Interval: time.Millisecond})
	if err != nil {
		r.t.Fatal(err)
	}
}

// noTX fails the test when anything that can transmit reached the modem.
func noTX(t *testing.T, m *fake.Modem) {
	t.Helper()
	for _, c := range []byte{mmdvm.CmdDMRData1, mmdvm.CmdDMRData2, mmdvm.CmdDMRStart, mmdvm.CmdDMRShortLC, mmdvm.CmdDMRAbort} {
		if n := m.Count(c); n != 0 {
			t.Errorf("%s reached the modem %d times", mmdvm.CommandName(c), n)
		}
	}
	for _, f := range m.Commands() {
		if f.Cmd == mmdvm.CmdSetFreq && len(f.Payload) > 9 && f.Payload[9] != 0 {
			t.Errorf("SET_FREQ with RF power %d", f.Payload[9])
		}
	}
}

// setFreqs are the receive frequencies of every SET_FREQ the modem got.
func setFreqs(m *fake.Modem) []uint32 {
	var out []uint32
	for _, f := range m.Commands() {
		if f.Cmd == mmdvm.CmdSetFreq {
			out = append(out, binary.LittleEndian.Uint32(f.Payload[1:]))
		}
	}
	return out
}

type fakeSvc struct {
	mu     sync.Mutex
	active bool
	stopE  error
	calls  []string
	onCall func(name string)
}

func (f *fakeSvc) rec(n string) {
	f.mu.Lock()
	f.calls = append(f.calls, n)
	cb := f.onCall
	f.mu.Unlock()
	if cb != nil {
		cb(n)
	}
}
func (f *fakeSvc) Active() (bool, error) { f.rec("active"); return f.active, nil }
func (f *fakeSvc) Stop() error           { f.rec("stop"); return f.stopE }
func (f *fakeSvc) Start() error          { f.rec("start"); return nil }
func (f *fakeSvc) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// writeConfig saves a node config with an MMDVM modem and returns its path.
func writeConfig(t *testing.T, mut func(*node.Config)) string {
	t.Helper()
	c, err := node.LoadConfig(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.Station.Radio.Preset = "mmdvm"
	cc, pw := 3, 40
	c.Station.Radio.MMDVM = &node.MMDVMConfig{Device: "/dev/ttyAMA0", Baud: 115200, RXFreqHz: baseRX, TXFreqHz: baseRX + 100_000,
		RXOffsetHz: 100, TXOffsetHz: -50, ColorCode: &cc, RFPower: &pw}
	if mut != nil {
		mut(c)
	}
	p := filepath.Join(t.TempDir(), "node.json")
	if err := node.SaveConfig(p, c); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type safeW struct {
	w *strings.Builder
}

var wmu sync.Mutex

func (s *safeW) Write(p []byte) (int, error) {
	wmu.Lock()
	defer wmu.Unlock()
	return s.w.Write(p)
}

func safeString(b *strings.Builder) string {
	wmu.Lock()
	defer wmu.Unlock()
	return b.String()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func manyAMBE(n int) []dmr.AMBE {
	ambe := make([]dmr.AMBE, n)
	for i := range ambe {
		ambe[i] = dmr.AMBE{byte(i), byte(i * 7), 0x5A, byte(255 - i), 0x33, byte(i * 3), 0x80}
	}
	return ambe
}
