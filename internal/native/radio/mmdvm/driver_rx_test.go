package mmdvm_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// rxFreq is the receive-only test frequency pair, a placeholder in UHF with
// a transmit offset (which receive-only must still send, power 0).
var rxFreq = mmdvm.Freq{RX: 441_000_000, TX: 446_000_000, Power: 100}

func rxStart(t *testing.T, o fake.Options) *harness {
	t.Helper()
	return start(t, o, func(s *mmdvm.Settings) {
		s.RXOnly = true
		s.AllowTX = true      // must be forced off
		s.Modem.Duplex = true // must be forced off
		s.Freq = rxFreq
	})
}

func noTX(t *testing.T, h *harness) {
	t.Helper()
	for _, c := range []byte{mmdvm.CmdDMRData1, mmdvm.CmdDMRData2, mmdvm.CmdDMRStart, mmdvm.CmdDMRShortLC} {
		if n := h.m.Count(c); n != 0 {
			t.Errorf("%s reached the modem %d times", mmdvm.CommandName(c), n)
		}
	}
}

func TestRXOnlySetup(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) > 0 })
	cmds := h.m.Commands()
	want := []byte{mmdvm.CmdGetVersion, mmdvm.CmdSetFreq, mmdvm.CmdSetConfig, mmdvm.CmdSetMode, mmdvm.CmdGetStatus}
	for i, c := range want {
		if i >= len(cmds) || cmds[i].Cmd != c {
			t.Fatalf("command %d: got %v, want %s", i, cmds, mmdvm.CommandName(c))
		}
	}
	f := rxFreq
	f.Power = 0
	if !bytes.Equal(cmds[1].Payload, mmdvm.SetFreq(f)[3:]) {
		t.Errorf("SET_FREQ % X, want % X (power 0)", cmds[1].Payload, mmdvm.SetFreq(f)[3:])
	}
	cfg := fastSettings(h.m, false).Modem
	cfg.Modes.DMR = true
	wantCfg, _ := mmdvm.SetConfig(1, cfg)
	if !bytes.Equal(cmds[2].Payload, wantCfg[3:]) {
		t.Errorf("SET_CONFIG % X, want simplex % X", cmds[2].Payload, wantCfg[3:])
	}
	if cmds[2].Payload[0]&0x80 == 0 {
		t.Errorf("simplex bit not set in % X", cmds[2].Payload)
	}
	if !bytes.Equal(cmds[3].Payload, []byte{mmdvm.ModeDMR}) {
		t.Errorf("SET_MODE % X", cmds[3].Payload)
	}
	s := h.m.State()
	if s.Duplex || s.Mode != mmdvm.ModeDMR || s.ColorCode != 1 {
		t.Errorf("modem %+v", s)
	}
	st := h.d.Status()
	if st.Duplex || !st.Receive || h.d.Slots() != 1 {
		t.Errorf("status %+v", st)
	}
	if err := h.d.StartTX(network.CallHeader{Call: 1, Slot: 2}); err == nil {
		t.Error("StartTX worked in receive-only")
	}
	noTX(t, h)
}

// The write filter drops transmit frames whatever asks for them.
func TestRXOnlyWriteFilter(t *testing.T) {
	for _, c := range []byte{mmdvm.CmdDMRData1, mmdvm.CmdDMRData2, mmdvm.CmdDMRStart, mmdvm.CmdDMRShortLC} {
		if !mmdvm.TXCommand(c) {
			t.Errorf("%s is not filtered", mmdvm.CommandName(c))
		}
	}
	for _, c := range []byte{mmdvm.CmdGetVersion, mmdvm.CmdGetStatus, mmdvm.CmdSetConfig, mmdvm.CmdSetFreq,
		mmdvm.CmdSetMode, mmdvm.CmdDMRAbort} {
		if mmdvm.TXCommand(c) {
			t.Errorf("%s is filtered", mmdvm.CommandName(c))
		}
	}
	var b bytes.Buffer
	n := mmdvm.FilterWrite(&b, mmdvm.DMRData(2, 0x20, &dmr.Burst{}), mmdvm.DMRStart(true), mmdvm.GetStatus())
	if n != 2 || !bytes.Equal(b.Bytes(), mmdvm.GetStatus()) {
		t.Errorf("dropped %d, wrote % X", n, b.Bytes())
	}
}

func TestRXOnlyCall(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	ambe := testAMBE(6)
	call := fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: ambe, Interval: time.Millisecond}
	if err := h.m.PlayCall(h.ctx, call); err != nil {
		t.Fatal(err)
	}
	e := h.s.expect(t, h.ctx, "header")
	if e.h.SrcID != testSrc || e.h.TG != 91 || e.h.Slot != 2 || e.h.Private {
		t.Errorf("header %+v", e.h)
	}
	for i, a := range ambe {
		v := h.s.expect(t, h.ctx, "voice")
		if v.call != e.call || !bytes.Equal(v.f.Data, a[:]) {
			t.Fatalf("voice %d: % X, want % X", i, v.f.Data, a[:])
		}
	}
	// End pads the superframe with silence; the end event comes last.
	for {
		x := h.s.next(t, h.ctx)
		if x.kind == "end" {
			if x.call != e.call {
				t.Errorf("end of call %d, want %d", x.call, e.call)
			}
			break
		}
	}
	noTX(t, h)
	if h.d.BlockedTX() != 0 {
		t.Errorf("filter dropped %d frames", h.d.BlockedTX())
	}
}

func TestRXOnlyCallStatusAndLost(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	call := fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: testAMBE(6), Interval: time.Millisecond, Lost: true}
	if err := h.m.PlayCall(h.ctx, call); err != nil {
		t.Fatal(err)
	}
	e := h.s.expect(t, h.ctx, "header")
	for {
		x := h.s.next(t, h.ctx)
		if x.kind == "end" {
			if x.call != e.call {
				t.Errorf("end of call %d", x.call)
			}
			break
		}
	}
	if c := h.d.Status().RXCall; c != nil {
		t.Errorf("call still shown after DMR_LOST: %+v", c)
	}
}

func TestRXOnlyCallShownWhileReceiving(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	call := fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: testAMBE(30), Interval: 5 * time.Millisecond}
	go func() { _ = h.m.PlayCall(h.ctx, call) }()
	h.s.expect(t, h.ctx, "header")
	c := h.d.Status().RXCall
	if c == nil || c.Src != testSrc || c.Dst != 91 || c.Slot != 2 {
		t.Errorf("rx call %+v", c)
	}
}

// The fake hears nothing in duplex until it transmits, as the firmware.
func TestFakeDuplexHearsNothing(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) { s.Freq = rxFreq })
	h.waitOpen()
	err := h.m.PlayCall(h.ctx, fake.RXCall{Src: testSrc, Dst: 91, AMBE: testAMBE(3)})
	if !errors.Is(err, fake.ErrNoRX) {
		t.Errorf("got %v", err)
	}
}

// A NAK of SET_FREQ shows the reason and the band list, and the driver
// retries after the reopen delay without crashing.
func TestRXOnlyFreqNAK(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) {
		s.RXOnly = true
		s.Freq = mmdvm.Freq{RX: 436_000_000, TX: 436_000_000}
		s.ReopenDelay = 20 * time.Millisecond
	})
	deadline, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	for {
		st := h.d.Status()
		if st.Reopens >= 2 {
			if st.Open || !strings.Contains(st.Error, "SET_FREQ") || !strings.Contains(st.Error, "420 to 475") {
				t.Fatalf("status error %q", st.Error)
			}
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("no retry: %+v", st)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if h.m.Count(mmdvm.CmdSetConfig) != 0 || h.m.Count(mmdvm.CmdSetMode) != 0 {
		t.Error("setup went on after a NAK")
	}
	noTX(t, h)
}

func TestRetune(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	f := mmdvm.Freq{RX: 441_000_250, TX: 446_000_000, Power: 100} // power must be sent as 0
	if err := h.d.Retune(h.ctx, f); err != nil {
		t.Fatal(err)
	}
	f.Power = 0
	if got := h.m.State(); !bytes.Equal(got.Freq, mmdvm.SetFreq(f)[3:]) || got.Mode != mmdvm.ModeDMR {
		t.Errorf("modem freq % X mode %d, want % X in DMR", got.Freq, got.Mode, mmdvm.SetFreq(f)[3:])
	}
	if !bytes.Equal(h.m.State().Tuned, mmdvm.SetFreq(f)[3:]) {
		t.Errorf("tuned % X: the retune did not reach the chip", h.m.State().Tuned)
	}
	// Setup (freq, config, DMR), then idle, new frequency, config, DMR.
	var seq []byte
	for _, c := range h.m.Commands() {
		if c.Cmd == mmdvm.CmdSetMode || c.Cmd == mmdvm.CmdSetFreq || c.Cmd == mmdvm.CmdSetConfig {
			seq = append(seq, c.Cmd)
		}
	}
	want := []byte{mmdvm.CmdSetFreq, mmdvm.CmdSetConfig, mmdvm.CmdSetMode,
		mmdvm.CmdSetMode, mmdvm.CmdSetFreq, mmdvm.CmdSetConfig, mmdvm.CmdSetMode}
	if !bytes.Equal(seq, want) {
		t.Errorf("commands % X, want % X", seq, want)
	}
	if h.m.Count(mmdvm.CmdGetVersion) != 1 || h.m.State().Opens != 1 {
		t.Error("retune reopened the modem")
	}
	// A call is still heard.
	if err := h.m.PlayCall(h.ctx, fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: testAMBE(3), Interval: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	h.s.expect(t, h.ctx, "header")
	noTX(t, h)
}

// A NAKed frequency is an error for the caller, leaves the modem on the old
// frequency in DMR mode and does not close it.
func TestRetuneNAK(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	err := h.d.Retune(h.ctx, mmdvm.Freq{RX: 436_000_000, TX: 436_000_000})
	if err == nil || !strings.Contains(err.Error(), "SET_FREQ") || !strings.Contains(err.Error(), "420 to 475") {
		t.Fatalf("got %v", err)
	}
	st := h.m.State()
	if st.Mode != mmdvm.ModeDMR || !h.d.Status().Open || st.Opens != 1 {
		t.Errorf("modem %+v, open %v", st, h.d.Status().Open)
	}
	f := rxFreq
	f.Power = 0
	if !bytes.Equal(st.Freq, mmdvm.SetFreq(f)[3:]) {
		t.Errorf("freq % X changed", st.Freq)
	}
	if err := h.d.Retune(h.ctx, mmdvm.Freq{RX: 441_000_500, TX: 446_000_000}); err != nil {
		t.Errorf("a good retune after a NAK: %v", err)
	}
}

func TestRetuneNeedsRXOnly(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) { s.Freq = rxFreq })
	if err := h.d.Retune(h.ctx, rxFreq); !errors.Is(err, mmdvm.ErrRetune) {
		t.Errorf("got %v", err)
	}
}

func TestRXMonitor(t *testing.T) {
	var mu sync.Mutex
	var evs []mmdvm.RXEvent
	h := rxStart2(t, func(e mmdvm.RXEvent) { mu.Lock(); evs = append(evs, e); mu.Unlock() })
	h.waitOpen()
	call := fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: testAMBE(6), Interval: time.Millisecond}
	if err := h.m.PlayCall(h.ctx, call); err != nil {
		t.Fatal(err)
	}
	for { // the end event is last
		if h.s.next(t, h.ctx).kind == "end" {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var bursts, voice, starts, ends, errs int
	for _, e := range evs {
		switch e.Kind {
		case mmdvm.RXBurst:
			bursts++
			if e.Voice {
				voice++
				errs += e.FECErrors
			}
		case mmdvm.RXCallStart:
			starts++
			if e.Src != testSrc || e.Dst != 91 || e.Slot != 2 {
				t.Errorf("start %+v", e)
			}
		case mmdvm.RXCallEnd:
			ends++
		}
	}
	if voice != 2 || starts != 1 || ends != 1 || errs != 0 || bursts < voice+3 {
		t.Errorf("bursts %d voice %d starts %d ends %d errs %d", bursts, voice, starts, ends, errs)
	}
}

func rxStart2(t *testing.T, mon func(mmdvm.RXEvent)) *harness {
	return start(t, fake.Options{}, func(s *mmdvm.Settings) {
		s.RXOnly = true
		s.Freq = rxFreq
		s.RXMonitor = mon
	})
}

// The firmware only stores SET_FREQ; idle then DMR does not reprogram the
// chip (MMDVM_HS SerialPort.cpp setMode, IO.cpp setFreq). The driver's
// Retune adds SET_CONFIG and does.
func TestOldRetuneSequenceDoesNotTune(t *testing.T) {
	m := fake.New(fake.Options{})
	port, err := m.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n := 0
	send := func(b []byte) {
		t.Helper()
		n++
		if _, err := port.Write(b); err != nil {
			t.Fatal(err)
		}
		if err := m.Wait(ctx, func() bool { return len(m.Commands()) >= n }); err != nil {
			t.Fatal(err)
		}
	}
	f1 := mmdvm.Freq{RX: 441_000_000, TX: 446_000_000}
	f2 := mmdvm.Freq{RX: 441_000_500, TX: 446_000_000}
	cfg, err := mmdvm.SetConfig(1, mmdvm.Config{Modes: mmdvm.Modes{DMR: true}, ColorCode: 1})
	if err != nil {
		t.Fatal(err)
	}
	tuned := func() uint32 { rx, _ := m.TunedRXFreq(); return rx }
	send(mmdvm.SetFreq(f1))
	if tuned() != 0 {
		t.Fatal("SET_FREQ alone tuned the chip")
	}
	send(cfg)
	send(mmdvm.SetMode(mmdvm.ModeDMR))
	if tuned() != f1.RX {
		t.Fatalf("tuned %d after SET_CONFIG", tuned())
	}
	send(mmdvm.SetMode(mmdvm.ModeIdle))
	send(mmdvm.SetFreq(f2))
	send(mmdvm.SetMode(mmdvm.ModeDMR))
	if tuned() != f1.RX {
		t.Errorf("idle, SET_FREQ, DMR moved the chip to %d", tuned())
	}
	send(cfg)
	if tuned() != f2.RX {
		t.Errorf("SET_CONFIG left the chip on %d", tuned())
	}
}

func TestRetuneTunesTheChip(t *testing.T) {
	h := rxStart(t, fake.Options{})
	h.waitOpen()
	if rx, _ := h.m.TunedRXFreq(); rx != rxFreq.RX {
		t.Fatalf("tuned %d after setup", rx)
	}
	if err := h.d.Retune(h.ctx, mmdvm.Freq{RX: 441_000_500, TX: 446_000_000}); err != nil {
		t.Fatal(err)
	}
	if rx, _ := h.m.TunedRXFreq(); rx != 441_000_500 {
		t.Errorf("tuned %d after Retune", rx)
	}
}
