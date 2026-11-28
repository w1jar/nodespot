package mmdvm_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// Test IDs: 1234567 is the radio, talkgroup 9.
const (
	testSrc = 1234567
	testTG  = 9
)

type event struct {
	kind string // header, voice, end
	h    network.CallHeader
	f    network.VoiceFrame
	call uint64
}

type sink struct{ ch chan event }

func newSink() *sink { return &sink{ch: make(chan event, 4096)} }

func (s *sink) Header(h network.CallHeader)  { s.ch <- event{kind: "header", h: h, call: h.Call} }
func (s *sink) Voice(f network.VoiceFrame)   { s.ch <- event{kind: "voice", f: f, call: f.Call} }
func (s *sink) End(call uint64)              { s.ch <- event{kind: "end", call: call} }
func (s *sink) Login(ctl.LoginState, string) {}

func (s *sink) next(t *testing.T, ctx context.Context) event {
	t.Helper()
	select {
	case e := <-s.ch:
		return e
	case <-ctx.Done():
		t.Fatal("timed out waiting for a sink event")
	}
	return event{}
}

func (s *sink) expect(t *testing.T, ctx context.Context, kind string) event {
	t.Helper()
	e := s.next(t, ctx)
	if e.kind != kind {
		t.Fatalf("got %s event, want %s", e.kind, kind)
	}
	return e
}

func fastSettings(m *fake.Modem, duplex bool) mmdvm.Settings {
	return mmdvm.Settings{
		Open:           m.Open,
		Modem:          mmdvm.Config{Duplex: duplex, ColorCode: 1, TXDelay: 100 * time.Millisecond, DMRLevel: 50, RXLevel: 50},
		AllowTX:        true,
		StatusInterval: 2 * time.Millisecond,
		Inactivity:     200 * time.Millisecond,
		BootWait:       time.Millisecond,
		VersionRetry:   5 * time.Millisecond,
		ReplyTimeout:   500 * time.Millisecond,
		Playout:        time.Millisecond,
		TXHang:         time.Hour,
		ReopenDelay:    time.Millisecond,
		RXTimeout:      time.Hour,
	}
}

type harness struct {
	t    *testing.T
	ctx  context.Context
	m    *fake.Modem
	d    *mmdvm.Driver
	s    *sink
	done chan error
	stop context.CancelFunc
}

func start(t *testing.T, o fake.Options, set func(*mmdvm.Settings)) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	m := fake.New(o)
	st := fastSettings(m, true)
	if set != nil {
		set(&st)
	}
	h := &harness{t: t, ctx: ctx, m: m, d: mmdvm.New(st), s: newSink(), done: make(chan error, 1)}
	runCtx, stop := context.WithCancel(ctx)
	h.stop = stop
	go func() { h.done <- h.d.Run(runCtx, h.s) }()
	t.Cleanup(func() {
		stop()
		<-h.done
		cancel()
	})
	return h
}

func (h *harness) waitModem(cond func(fake.State) bool) {
	h.t.Helper()
	if err := h.m.Wait(h.ctx, func() bool { return cond(h.m.State()) }); err != nil {
		h.t.Fatalf("waiting for the modem: %v (state %+v)", err, h.m.State())
	}
}

func (h *harness) waitOpen() {
	h.t.Helper()
	h.waitModem(func(fake.State) bool { return h.d.Status().Open })
}

func voiceCall(ambe []dmr.AMBE, mssync bool) []dmr.TXFrame {
	tx := dmr.NewTX(dmr.TXConfig{Src: testSrc, Dst: testTG, CC: 1, MSSync: mssync})
	fs := tx.Start()
	for _, a := range ambe {
		fs = append(fs, tx.Write(a)...)
	}
	return append(fs, tx.End()...)
}

func testAMBE(n int) []dmr.AMBE {
	out := make([]dmr.AMBE, n)
	for i := range out {
		// 49 bits: bytes 0-5 and the top bit of byte 6.
		out[i] = dmr.AMBE{byte(i), byte(i * 7), 0x5A, byte(255 - i), 0x33, byte(i * 3), 0x80}
	}
	return out
}

func cmdIndex(cmds []mmdvm.Frame, cmd byte, payload ...byte) int {
	for i, f := range cmds {
		if f.Cmd == cmd && (payload == nil || bytes.Equal(f.Payload, payload)) {
			return i
		}
	}
	return -1
}

func TestStartup(t *testing.T) {
	for _, proto := range []int{1, 2} {
		t.Run(map[int]string{1: "v1", 2: "v2"}[proto], func(t *testing.T) {
			freq := mmdvm.Freq{RX: 441000000, TX: 438800000, Power: 100}
			h := start(t, fake.Options{Protocol: proto, Description: "MMDVM_HS_Dual_Hat fake"}, func(s *mmdvm.Settings) {
				s.Freq = freq
			})
			h.waitOpen()
			st := h.d.Status()
			if st.Protocol != proto || st.Description != "MMDVM_HS_Dual_Hat fake" || !st.Duplex {
				t.Errorf("status %+v", st)
			}
			if h.d.Slots() != 2 {
				t.Errorf("slots %d", h.d.Slots())
			}
			h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) > 0 })
			cmds := h.m.Commands()
			want := []byte{mmdvm.CmdGetVersion, mmdvm.CmdSetFreq, mmdvm.CmdSetConfig, mmdvm.CmdGetStatus}
			for i, c := range want {
				if i >= len(cmds) || cmds[i].Cmd != c {
					t.Fatalf("command %d: got %v, want %s", i, cmds, mmdvm.CommandName(c))
				}
			}
			cfg := fastSettings(h.m, true).Modem
			cfg.Modes.DMR = true
			wantCfg, _ := mmdvm.SetConfig(proto, cfg)
			if !bytes.Equal(cmds[2].Payload, wantCfg[3:]) {
				t.Errorf("SET_CONFIG % X, want % X", cmds[2].Payload, wantCfg[3:])
			}
			if !bytes.Equal(cmds[1].Payload, mmdvm.SetFreq(freq)[3:]) {
				t.Errorf("SET_FREQ % X", cmds[1].Payload)
			}
			s := h.m.State()
			if !s.Configured || !s.Duplex || s.ColorCode != 1 {
				t.Errorf("modem %+v", s)
			}
			// Status polling keeps going.
			n := h.m.Count(mmdvm.CmdGetStatus)
			h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) >= n+3 })
			if !h.d.Status().Open || h.d.Status().Space != [2]int{10, 10} {
				t.Errorf("status %+v", h.d.Status())
			}
		})
	}
}

// With POCSAG unset the driver sends TX in that field, and the fake (which
// range-checks like MMDVM_HS) ACKs, so SET_CONFIG follows.
func TestStartupPOCSAGUnset(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) {
		s.Freq = mmdvm.Freq{RX: 441000000, TX: 433500000, Power: 100}
	})
	h.waitOpen()
	h.waitModem(func(s fake.State) bool { return s.Configured })
	i := cmdIndex(h.m.Commands(), mmdvm.CmdSetFreq)
	if i < 0 {
		t.Fatal("no SET_FREQ")
	}
	p := h.m.Commands()[i].Payload
	if len(p) != 14 || binary.LittleEndian.Uint32(p[10:]) != 433500000 {
		t.Errorf("SET_FREQ % X, want TX in the POCSAG field", p)
	}
}

// A frequency MMDVM_HS refuses is NAKed, so the driver never gets as far as
// SET_CONFIG.
func TestStartupBadFreq(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) {
		s.Freq = mmdvm.Freq{RX: 400000000, TX: 400000000, Power: 100}
	})
	h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdSetFreq) > 0 })
	time.Sleep(50 * time.Millisecond)
	if h.m.Count(mmdvm.CmdSetConfig) != 0 {
		t.Error("SET_CONFIG sent after a NAKed SET_FREQ")
	}
}

func TestStartupNoFreq(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	if h.m.Count(mmdvm.CmdSetFreq) != 0 {
		t.Error("SET_FREQ sent with no frequencies")
	}
}

func TestDuplexRefused(t *testing.T) {
	h := start(t, fake.Options{SimplexOnly: true}, nil)
	h.waitModem(func(fake.State) bool { return h.d.Status().Reopens >= 2 })
	st := h.d.Status()
	if st.Open || st.NAKs < 2 || !strings.Contains(st.Error, "duplex") {
		t.Errorf("status %+v", st)
	}
}

func TestNoVersionAnswer(t *testing.T) {
	m := fake.New(fake.Options{})
	m.SetSilent(true)
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) {
		s.Open = m.Open
		s.VersionAttempts = 2
		s.ReplyTimeout = time.Millisecond
		s.VersionRetry = time.Millisecond
	})
	h.m = m
	h.waitModem(func(fake.State) bool { return h.d.Status().Reopens >= 1 })
	if !strings.Contains(h.d.Status().Error, "GET_VERSION") {
		t.Errorf("error %q", h.d.Status().Error)
	}
	if m.Count(mmdvm.CmdGetVersion) < 2 {
		t.Errorf("GET_VERSION sent %d times", m.Count(mmdvm.CmdGetVersion))
	}
}

func TestReceive(t *testing.T) {
	for _, slot := range []int{1, 2} {
		h := start(t, fake.Options{}, nil)
		h.waitOpen()
		ambe := testAMBE(12)
		if err := h.m.InjectFrames(slot, voiceCall(ambe, true)); err != nil {
			t.Fatal(err)
		}
		hd := h.s.expect(t, h.ctx, "header").h
		if hd.SrcID != testSrc || hd.Src != "1234567" || hd.TG != testTG || hd.Slot != slot || hd.Private || hd.Call == 0 {
			t.Errorf("header %+v", hd)
		}
		for i, a := range ambe {
			e := h.s.expect(t, h.ctx, "voice")
			if e.f.Call != hd.Call || e.f.Seq != uint32(i) || e.f.Codec != network.CodecAMBE || !bytes.Equal(e.f.Data, a[:]) {
				t.Fatalf("frame %d: %+v, want % X", i, e.f, a)
			}
		}
		if e := h.s.expect(t, h.ctx, "end"); e.call != hd.Call {
			t.Errorf("end of call %d, want %d", e.call, hd.Call)
		}
	}
}

func TestReceiveLateEntry(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	fs := voiceCall(testAMBE(36), true)
	// Drop the voice LC headers: the call is joined from the embedded LC.
	var voice []dmr.TXFrame
	for _, f := range fs {
		if f.FrameType != dmr.FrameDataSync || f.DTypeVSeq != uint8(dmr.DataVoiceLCHeader) {
			voice = append(voice, f)
		}
	}
	if err := h.m.InjectFrames(2, voice); err != nil {
		t.Fatal(err)
	}
	hd := h.s.expect(t, h.ctx, "header").h
	if hd.SrcID != testSrc || hd.TG != testTG {
		t.Errorf("header %+v", hd)
	}
	n := 0
	for {
		e := h.s.next(t, h.ctx)
		if e.kind == "end" {
			break
		}
		n++
	}
	// The embedded LC completes in burst E of the first superframe; the
	// voice of bursts A-D (12 frames) is dropped, the rest delivered.
	if n == 0 || n >= 36 {
		t.Errorf("%d voice frames delivered after late entry", n)
	}
}

func TestReceiveLost(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	fs := voiceCall(testAMBE(6), true)
	if err := h.m.InjectFrames(1, fs[:len(fs)-1]); err != nil { // no terminator
		t.Fatal(err)
	}
	hd := h.s.expect(t, h.ctx, "header").h
	for range 6 {
		h.s.expect(t, h.ctx, "voice")
	}
	if err := h.m.InjectLost(1); err != nil {
		t.Fatal(err)
	}
	if e := h.s.expect(t, h.ctx, "end"); e.call != hd.Call {
		t.Errorf("end %d", e.call)
	}
}

func TestReceiveTimeout(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) { s.RXTimeout = 20 * time.Millisecond })
	h.waitOpen()
	fs := voiceCall(testAMBE(3), true)
	if err := h.m.InjectFrames(2, fs[:len(fs)-1]); err != nil {
		t.Fatal(err)
	}
	hd := h.s.expect(t, h.ctx, "header").h
	for range 3 {
		h.s.expect(t, h.ctx, "voice")
	}
	if e := h.s.expect(t, h.ctx, "end"); e.call != hd.Call {
		t.Errorf("end %d", e.call)
	}
}

func TestTransmitFlowControl(t *testing.T) {
	h := start(t, fake.Options{Space: 5}, nil)
	h.waitOpen()
	h.waitModem(func(fake.State) bool { return h.d.Status().Space == [2]int{5, 5} })
	if err := h.d.StartTX(network.CallHeader{Call: 7, SrcID: testSrc, TG: testTG, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	for _, a := range testAMBE(30) {
		if err := h.d.WriteVoice(network.VoiceFrame{Call: 7, Data: a[:]}); err != nil {
			t.Fatal(err)
		}
	}
	// Free space 5: four bursts, keeping one free as MMDVMHost does.
	h.waitModem(func(s fake.State) bool { return len(h.m.Sent(2)) >= 4 })
	n := h.m.Count(mmdvm.CmdGetStatus)
	h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) >= n+5 })
	if got := len(h.m.Sent(2)); got != 4 {
		t.Fatalf("%d bursts written into a FIFO with 5 free", got)
	}
	h.m.Drain(2, 4)
	h.waitModem(func(s fake.State) bool { return len(h.m.Sent(2)) >= 8 })
	n = h.m.Count(mmdvm.CmdGetStatus)
	h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) >= n+5 })
	if got := len(h.m.Sent(2)); got != 8 {
		t.Fatalf("%d bursts after draining 4", got)
	}
	if st := h.d.Status(); st.NAKs != 0 {
		t.Errorf("NAKs %d (%s)", st.NAKs, st.Error)
	}
	if len(h.m.Sent(1)) != 0 {
		t.Error("slot 1 written")
	}
	cmds := h.m.Commands()
	mode := cmdIndex(cmds, mmdvm.CmdSetMode, mmdvm.ModeDMR)
	st := cmdIndex(cmds, mmdvm.CmdDMRStart, 1)
	data := cmdIndex(cmds, mmdvm.CmdDMRData2)
	if mode < 0 || st < mode || data < st {
		t.Errorf("SET_MODE DMR at %d, DMR_START 1 at %d, first data at %d", mode, st, data)
	}
	// Duplex transmits BS-sourced sync.
	if s, _ := dmr.MatchSync(&h.m.Sent(2)[0], 0); s != dmr.SyncBSData {
		t.Errorf("first burst sync %X", s)
	}
}

func TestTransmitRoundTrip(t *testing.T) {
	h := start(t, fake.Options{AutoDrain: true}, nil)
	h.waitOpen()
	ambe := testAMBE(14)
	if err := h.d.StartTX(network.CallHeader{Call: 3, SrcID: testSrc, TG: testTG, Slot: 1}); err != nil {
		t.Fatal(err)
	}
	for _, a := range ambe {
		if err := h.d.WriteVoice(network.VoiceFrame{Call: 3, Data: a[:]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.d.EndTX(1); err != nil {
		t.Fatal(err)
	}
	want := len(voiceCall(ambe, false))
	h.waitModem(func(fake.State) bool { return len(h.m.Sent(1)) >= want })
	rx := dmr.NewRX()
	var got []dmr.AMBE
	var lc dmr.LC
	ended := false
	vseq := 0
	for _, b := range h.m.Sent(1) {
		s, _ := dmr.MatchSync(&b, dmr.SyncMaxBitError)
		var res dmr.RXResult
		switch {
		case s == dmr.SyncBSData:
			st, _ := b.SlotType()
			res = rx.Push(dmr.FrameDataSync, uint8(st.DataType), &b)
		case s == dmr.SyncBSVoice:
			vseq = 0
			res = rx.Push(dmr.FrameVoiceSync, 0, &b)
		default:
			vseq++
			res = rx.Push(dmr.FrameVoice, uint8(vseq), &b)
		}
		if res.LCValid {
			lc = res.LC
		}
		if res.HasVoice {
			got = append(got, res.Voice[:]...)
		}
		ended = ended || res.End
	}
	if lc.Src != testSrc || lc.Dst != testTG || !ended {
		t.Errorf("LC %+v, ended %v", lc, ended)
	}
	if len(got) < len(ambe) {
		t.Fatalf("%d frames back, want %d", len(got), len(ambe))
	}
	for i, a := range ambe {
		if got[i] != a {
			t.Fatalf("frame %d: % X, want % X", i, got[i], a)
		}
	}
	for _, a := range got[len(ambe):] {
		if a != dmr.SilenceAMBE {
			t.Errorf("padding % X is not silence", a)
		}
	}
}

func TestTransmitErrors(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) { s.AllowTX = false })
	if err := h.d.StartTX(network.CallHeader{Slot: 1}); !errors.Is(err, mmdvm.ErrTXInhibited) {
		t.Errorf("inhibited StartTX: %v", err)
	}
	h.waitOpen()
	// RX does not start the downlink when TX is not allowed.
	if err := h.m.InjectFrames(1, voiceCall(testAMBE(3), true)); err != nil {
		t.Fatal(err)
	}
	h.s.expect(t, h.ctx, "header")
	for range 3 {
		h.s.expect(t, h.ctx, "voice")
	}
	h.s.expect(t, h.ctx, "end")
	n := h.m.Count(mmdvm.CmdGetStatus)
	h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) >= n+3 })
	if h.m.Count(mmdvm.CmdDMRStart) != 0 {
		t.Error("DMR_START sent with TX inhibited")
	}

	h2 := start(t, fake.Options{}, nil)
	if err := h2.d.StartTX(network.CallHeader{Slot: 1}); !errors.Is(err, mmdvm.ErrNotOpen) {
		t.Errorf("StartTX before open: %v", err)
	}
	h2.waitOpen()
	if err := h2.d.StartTX(network.CallHeader{Slot: 3}); !errors.Is(err, mmdvm.ErrBadSlot) {
		t.Errorf("slot 3: %v", err)
	}
	if err := h2.d.WriteVoice(network.VoiceFrame{Call: 99, Data: make([]byte, 7)}); !errors.Is(err, mmdvm.ErrNoCall) {
		t.Errorf("unknown call: %v", err)
	}
	if err := h2.d.WriteVoice(network.VoiceFrame{Call: 99, Data: make([]byte, 8)}); !errors.Is(err, mmdvm.ErrBadFrame) {
		t.Errorf("8-byte frame: %v", err)
	}
	if err := h2.d.EndTX(2); !errors.Is(err, mmdvm.ErrNoCall) {
		t.Errorf("EndTX idle: %v", err)
	}
}

func TestWakeOnReceive(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	if err := h.m.InjectFrames(2, voiceCall(testAMBE(3), true)); err != nil {
		t.Fatal(err)
	}
	h.waitModem(func(s fake.State) bool { return s.TX && s.Mode == mmdvm.ModeDMR })
}

func TestTXHang(t *testing.T) {
	h := start(t, fake.Options{AutoDrain: true}, func(s *mmdvm.Settings) { s.TXHang = 20 * time.Millisecond })
	h.waitOpen()
	if err := h.d.StartTX(network.CallHeader{Call: 1, SrcID: testSrc, TG: testTG, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	h.waitModem(func(s fake.State) bool { return s.TX })
	if err := h.d.EndTX(2); err != nil {
		t.Fatal(err)
	}
	h.waitModem(func(s fake.State) bool { return !s.TX })
	if cmdIndex(h.m.Commands(), mmdvm.CmdDMRStart, 0) < 0 {
		t.Error("no DMR_START 0")
	}
}

func TestAbort(t *testing.T) {
	h := start(t, fake.Options{Space: 3}, nil)
	h.waitOpen()
	if err := h.d.StartTX(network.CallHeader{Call: 1, SrcID: testSrc, TG: testTG, Slot: 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.d.StartTX(network.CallHeader{Call: 2, SrcID: testSrc, TG: testTG, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	h.waitModem(func(s fake.State) bool { return s.FIFO == [2]int{2, 2} })
	h.d.Abort()
	h.waitModem(func(s fake.State) bool { return s.Aborts == [2]int{1, 1} && !s.TX && s.Mode == mmdvm.ModeIdle })
	if err := h.d.WriteVoice(network.VoiceFrame{Call: 1, Data: make([]byte, 7)}); !errors.Is(err, mmdvm.ErrNoCall) {
		t.Errorf("write after abort: %v", err)
	}
	cmds := h.m.Commands()
	a1 := cmdIndex(cmds, mmdvm.CmdDMRAbort, 1)
	a2 := cmdIndex(cmds, mmdvm.CmdDMRAbort, 2)
	s0 := cmdIndex(cmds, mmdvm.CmdDMRStart, 0)
	if a1 < 0 || a2 < a1 || s0 < a2 {
		t.Errorf("abort order %d %d %d", a1, a2, s0)
	}
	n := len(h.m.Sent(1)) + len(h.m.Sent(2))
	h.m.Drain(1, 3)
	h.m.Drain(2, 3)
	c := h.m.Count(mmdvm.CmdGetStatus)
	h.waitModem(func(fake.State) bool { return h.m.Count(mmdvm.CmdGetStatus) >= c+5 })
	if got := len(h.m.Sent(1)) + len(h.m.Sent(2)); got != n {
		t.Errorf("%d bursts written after abort", got-n)
	}
}

func TestCancelStopsTransmitter(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	if err := h.d.StartTX(network.CallHeader{Call: 1, SrcID: testSrc, TG: testTG, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	h.waitModem(func(s fake.State) bool { return s.TX })
	h.stop()
	if err := <-h.done; err != nil {
		t.Errorf("Run: %v", err)
	}
	h.done <- nil // for the cleanup
	h.waitModem(func(s fake.State) bool { return !s.Connected })
	s := h.m.State()
	if s.TX || s.Mode != mmdvm.ModeIdle || s.Aborts != [2]int{1, 1} {
		t.Errorf("modem after cancel %+v", s)
	}
}

func TestClose(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	_ = h.d.Close()
	if err := <-h.done; err != nil {
		t.Errorf("Run: %v", err)
	}
	h.done <- nil
}

func TestStopsAnswering(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) {
		s.Inactivity = 30 * time.Millisecond
		s.ReplyTimeout = 5 * time.Millisecond
	})
	h.waitOpen()
	if err := h.m.InjectFrames(1, voiceCall(testAMBE(3), true)[:2]); err != nil {
		t.Fatal(err)
	}
	hd := h.s.expect(t, h.ctx, "header")
	h.m.SetSilent(true)
	h.waitModem(func(fake.State) bool { return h.d.Status().Reopens >= 1 })
	if st := h.d.Status(); st.Open || !strings.Contains(st.Error, "stopped answering") {
		t.Errorf("status %+v", st)
	}
	// The call that was being received ends with the link.
	for {
		e := h.s.next(t, h.ctx)
		if e.kind == "end" {
			if e.call != hd.call {
				t.Errorf("end %d", e.call)
			}
			break
		}
	}
	h.m.SetSilent(false)
	h.waitModem(func(s fake.State) bool { return s.Opens >= 2 && h.d.Status().Open })
}

func TestSimplex(t *testing.T) {
	h := start(t, fake.Options{AutoDrain: true}, func(s *mmdvm.Settings) {
		s.Modem.Duplex = false
	})
	h.waitOpen()
	if h.d.Slots() != 1 || h.d.Status().Duplex {
		t.Errorf("slots %d", h.d.Slots())
	}
	if s := h.m.State(); s.Duplex {
		t.Error("modem configured duplex")
	}
	if err := h.d.StartTX(network.CallHeader{Call: 1, SrcID: testSrc, TG: testTG, Slot: 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.d.EndTX(1); err != nil {
		t.Fatal(err)
	}
	want := len(voiceCall(nil, true))
	h.waitModem(func(fake.State) bool { return len(h.m.Sent(2)) >= want })
	if s, _ := dmr.MatchSync(&h.m.Sent(2)[0], 0); s != dmr.SyncMSData {
		t.Errorf("simplex sync %X, want MS data", s)
	}
	if h.m.Count(mmdvm.CmdDMRStart) != 0 || h.m.Count(mmdvm.CmdDMRData1) != 0 {
		t.Error("DMR_START or DMR_DATA1 sent to a simplex modem")
	}
	if err := h.m.InjectFrames(2, voiceCall(testAMBE(3), true)); err != nil {
		t.Fatal(err)
	}
	if hd := h.s.expect(t, h.ctx, "header").h; hd.Slot != 2 {
		t.Errorf("simplex RX slot %d", hd.Slot)
	}
	h.d.Abort()
	h.waitModem(func(fake.State) bool { return cmdIndex(h.m.Commands(), mmdvm.CmdSetMode, mmdvm.ModeIdle) >= 0 })
	if st := h.d.Status(); st.NAKs != 0 {
		t.Errorf("NAKs %d: %s", st.NAKs, st.Error)
	}
}

func TestLoopback(t *testing.T) {
	h := start(t, fake.Options{AutoDrain: true, Loopback: true}, nil)
	h.waitOpen()
	ambe := testAMBE(9)
	if err := h.d.StartTX(network.CallHeader{Call: 5, SrcID: testSrc, TG: testTG, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	for _, a := range ambe {
		if err := h.d.WriteVoice(network.VoiceFrame{Call: 5, Data: a[:]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.d.EndTX(2); err != nil {
		t.Fatal(err)
	}
	hd := h.s.expect(t, h.ctx, "header").h
	if hd.SrcID != testSrc || hd.Slot != 2 {
		t.Errorf("header %+v", hd)
	}
	for i, a := range ambe {
		if e := h.s.expect(t, h.ctx, "voice"); !bytes.Equal(e.f.Data, a[:]) {
			t.Fatalf("frame %d % X", i, e.f.Data)
		}
	}
}

func TestQueueOverflow(t *testing.T) {
	h := start(t, fake.Options{Space: 2}, func(s *mmdvm.Settings) { s.QueueLen = 5 })
	h.waitOpen()
	if err := h.d.StartTX(network.CallHeader{Call: 1, SrcID: testSrc, TG: testTG, Slot: 1}); err != nil {
		t.Fatal(err)
	}
	for _, a := range testAMBE(30) {
		_ = h.d.WriteVoice(network.VoiceFrame{Call: 1, Data: a[:]})
	}
	if h.d.Status().Dropped == 0 {
		t.Error("no bursts dropped from a full queue")
	}
}

func TestOpenSerialErrors(t *testing.T) {
	if _, err := mmdvm.OpenSerial("/dev/null", 1234); err == nil {
		t.Error("baud 1234 accepted")
	}
	if _, err := mmdvm.OpenSerial("/dev/null", 0); err == nil {
		t.Error("/dev/null taken as a serial port")
	}
	if _, err := mmdvm.OpenSerial("/nonexistent/tty", 115200); err == nil {
		t.Error("missing port opened")
	}
}

func TestPTY(t *testing.T) {
	master, path, err := fake.OpenPTY()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer master.Close()
	// Hold the slave open so the master never reads EIO while the driver
	// reopens it.
	if slave, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0); err == nil {
		defer slave.Close()
	}
	m := fake.New(fake.Options{Protocol: 1})
	go m.Serve(master)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := fastSettings(m, true)
	st.Open = nil
	st.Port = path
	d := mmdvm.New(st)
	s := newSink()
	done := make(chan error, 1)
	runCtx, stop := context.WithCancel(ctx)
	go func() { done <- d.Run(runCtx, s) }()
	defer func() {
		stop()
		<-done
	}()
	if err := m.Wait(ctx, func() bool { return d.Status().Open }); err != nil {
		t.Fatalf("over a pty: %v (%+v)", err, d.Status())
	}
	if d.Status().Protocol != 1 {
		t.Errorf("status %+v", d.Status())
	}
	if err := m.InjectFrames(1, voiceCall(testAMBE(3), true)); err != nil {
		t.Fatal(err)
	}
	if hd := s.expect(t, ctx, "header").h; hd.SrcID != testSrc {
		t.Errorf("header %+v", hd)
	}
}
