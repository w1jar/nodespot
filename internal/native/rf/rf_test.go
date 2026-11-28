package rf

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// fakeRadio records what the endpoint does on channels. Every call the
// endpoint makes on it happens in the endpoint's loop, so after Endpoint.do
// returns everything is recorded.
type fakeRadio struct {
	slots  int
	runs   chan network.Sink
	runErr chan error
	starts chan network.CallHeader
	writes chan network.VoiceFrame
	ends   chan int
	aborts chan struct{}
}

func newFakeRadio(slots int) *fakeRadio {
	return &fakeRadio{slots: slots, runs: make(chan network.Sink, 1), runErr: make(chan error, 1),
		starts: make(chan network.CallHeader, 16), writes: make(chan network.VoiceFrame, 1024),
		ends: make(chan int, 16), aborts: make(chan struct{}, 16)}
}

func (r *fakeRadio) Slots() int { return r.slots }
func (r *fakeRadio) Run(ctx context.Context, s network.Sink) error {
	r.runs <- s
	select {
	case <-ctx.Done():
		return nil
	case err := <-r.runErr:
		return err
	}
}
func (r *fakeRadio) StartTX(h network.CallHeader) error {
	if h.Slot < 1 || h.Slot > 2 || h.Call == 0 {
		return errors.New("bad header")
	}
	r.starts <- h
	return nil
}
func (r *fakeRadio) WriteVoice(f network.VoiceFrame) error { r.writes <- f; return nil }
func (r *fakeRadio) EndTX(slot int) error                  { r.ends <- slot; return nil }
func (r *fakeRadio) Abort()                                { r.aborts <- struct{}{} }
func (r *fakeRadio) Status() radio.ModemStatus             { return radio.ModemStatus{} }
func (r *fakeRadio) Close() error                          { return nil }

// fakeMode is a FrameMode whose audio methods are never used.
type fakeMode struct {
	mode.Mode // nil: the audio path is not used
	name      string
	codec     network.Codec
	recv      chan mode.FrameEvent
	frames    chan bool
	hdrs      chan network.CallHeader
	voice     chan network.VoiceFrame
	ends      chan struct{}
}

func newFakeMode(name string) *fakeMode {
	return &fakeMode{name: name, codec: network.CodecAMBE, recv: make(chan mode.FrameEvent, 256),
		frames: make(chan bool, 16), hdrs: make(chan network.CallHeader, 16),
		voice: make(chan network.VoiceFrame, 1024), ends: make(chan struct{}, 16)}
}

func (m *fakeMode) Name() string                          { return m.name }
func (m *fakeMode) FrameCodec() (network.Codec, bool)     { return m.codec, true }
func (m *fakeMode) SetFrames(on bool)                     { m.frames <- on }
func (m *fakeMode) RecvFrames() <-chan mode.FrameEvent    { return m.recv }
func (m *fakeMode) SendHeader(h network.CallHeader) error { m.hdrs <- h; return nil }
func (m *fakeMode) SendFrame(f network.VoiceFrame) error {
	m.voice <- f
	return nil
}
func (m *fakeMode) EndFrames() error { m.ends <- struct{}{}; return nil }

func (m *fakeMode) header(call uint64, src uint32) {
	m.recv <- mode.FrameEvent{Kind: mode.FrameHeader, Call: call,
		Header: network.CallHeader{Call: call, Src: "N1CALL", SrcID: src, TG: 91}}
}
func (m *fakeMode) frame(call uint64, i int) {
	m.recv <- mode.FrameEvent{Kind: mode.FrameVoice, Call: call,
		Frame: network.VoiceFrame{Call: call, Seq: uint32(i), Codec: network.CodecAMBE, Data: ambe(i)}}
}
func (m *fakeMode) end(call uint64) { m.recv <- mode.FrameEvent{Kind: mode.FrameEnd, Call: call} }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func ambe(i int) []byte { return []byte{byte(i), byte(i >> 8), 0x22, 0x33, 0x44, 0x55, 0x80} }

type harness struct {
	t      *testing.T
	e      *Endpoint
	r      *fakeRadio
	m      [2]*fakeMode
	clk    *clock
	sink   network.Sink
	ctx    context.Context
	cancel context.CancelFunc
	done   chan error
}

// src is every caller's DMR ID: busy and loop decisions do not need two.
const src = 1234567

// start runs an endpoint on a fake radio with fake modes linked to every
// slot. The clock only moves when the test moves it, and housekeeping
// runs only when the test calls tick.
func start(t *testing.T, slots int, cfg Config) *harness {
	t.Helper()
	h := &harness{t: t, r: newFakeRadio(slots), clk: &clock{t: time.Unix(1_800_000_000, 0)}}
	cfg.Now, cfg.Tick = h.clk.Now, time.Hour
	h.e = New(h.r, cfg)
	for i, n := range h.e.Slots() {
		h.m[i] = newFakeMode("DMR" + string(rune('A'+i)))
		if err := h.e.Link(n, h.m[i]); err != nil {
			t.Fatal(err)
		}
		if on := recv(t, h.m[i].frames, "SetFrames"); !on {
			t.Fatal("SetFrames(false) on link")
		}
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.done = make(chan error, 1)
	go func() { h.done <- h.e.Run(h.ctx) }()
	h.sink = recv(t, h.r.runs, "radio Run")
	t.Cleanup(func() {
		h.cancel()
		recv(t, h.done, "Run to return")
	})
	return h
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func none[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %+v", what, v)
	default:
	}
}

// sync waits until the endpoint has handled every event queued so far.
func (h *harness) sync() {
	h.t.Helper()
	if !h.e.do(h.ctx, func() {}) {
		h.t.Fatal("the endpoint is not running")
	}
}

// tick runs housekeeping once, after the queued events.
func (h *harness) tick() {
	h.t.Helper()
	if !h.e.do(h.ctx, h.e.housekeeping) {
		h.t.Fatal("the endpoint is not running")
	}
}

func (h *harness) status(slot int) SlotStatus {
	h.t.Helper()
	for _, s := range h.e.Status() {
		if s.Slot == slot {
			return s
		}
	}
	h.t.Fatalf("no slot %d", slot)
	return SlotStatus{}
}

func (h *harness) rfHeader(call uint64, slot int, src uint32) {
	h.sink.Header(network.CallHeader{Call: call, Src: "1234567", SrcID: src, TG: 91, Slot: slot})
}

func (h *harness) rfVoice(call uint64, i int) {
	h.sink.Voice(network.VoiceFrame{Call: call, Seq: uint32(i), Codec: network.CodecAMBE, Data: ambe(i)})
}

// netOnAir starts a network call on slot and puts it on the air.
func (h *harness) netOnAir(slot int, call uint64, src uint32) network.CallHeader {
	h.t.Helper()
	m := h.m[slot-1]
	m.header(call, src)
	for i := range DefaultPrebuffer {
		m.frame(call, i)
	}
	st := recv(h.t, h.r.starts, "StartTX")
	for range DefaultPrebuffer {
		recv(h.t, h.r.writes, "WriteVoice")
	}
	h.sync()
	return st
}

func TestRFToNetwork(t *testing.T) {
	h := start(t, 2, Config{})
	m := h.m[1]
	h.rfHeader(10, 2, src)
	hd := recv(t, m.hdrs, "SendHeader")
	if hd.SrcID != src || hd.TG != 91 || hd.Slot != 2 {
		t.Errorf("SendHeader %+v", hd)
	}
	for i := range 9 {
		h.rfVoice(10, i)
	}
	h.rfVoice(99, 50) // another call: ignored
	for i := range 9 {
		f := recv(t, m.voice, "SendFrame")
		if !bytes.Equal(f.Data, ambe(i)) || f.Codec != network.CodecAMBE {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if s := h.status(2); s.State != StateRF || s.SrcID != src || s.Mode != "DMRB" {
		t.Errorf("status %+v", s)
	}
	h.sink.End(10)
	recv(t, m.ends, "EndFrames")
	h.sync()
	if s := h.status(2); s.State != StateHang || !s.HangRF {
		t.Errorf("status after the end %+v", s)
	}
	none(t, m.voice, "SendFrame")
	none(t, h.m[0].hdrs, "slot 1 SendHeader")

	h.clk.add(DefaultHang)
	h.tick()
	if s := h.status(2); s.State != StateIdle {
		t.Errorf("status after the hang %+v", s)
	}
}

func TestNetworkToRF(t *testing.T) {
	h := start(t, 2, Config{})
	m := h.m[0]
	m.header(5, src)
	m.frame(5, 0)
	m.frame(5, 1)
	h.sync()
	none(t, h.r.starts, "StartTX before the prebuffer")
	if s := h.status(1); s.State != StatePending || s.SrcID != src {
		t.Errorf("status %+v", s)
	}
	m.frame(5, 2)
	st := recv(t, h.r.starts, "StartTX")
	if st.Slot != 1 || st.SrcID != src || st.TG != 91 || st.Call == 0 || st.Call == 5 {
		t.Errorf("StartTX %+v", st)
	}
	for i := 3; i < 12; i++ {
		m.frame(5, i)
	}
	m.frame(6, 99) // another call: ignored
	for i := range 12 {
		f := recv(t, h.r.writes, "WriteVoice")
		if f.Call != st.Call || !bytes.Equal(f.Data, ambe(i)) {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	m.end(5)
	if slot := recv(t, h.r.ends, "EndTX"); slot != 1 {
		t.Errorf("EndTX(%d)", slot)
	}
	h.sync()
	none(t, h.r.writes, "WriteVoice")
	if s := h.status(1); s.State != StateHang || s.HangRF {
		t.Errorf("status %+v", s)
	}

	// A call shorter than the prebuffer still goes out.
	h.clk.add(time.Minute)
	h.tick()
	m.header(7, src)
	m.frame(7, 0)
	m.end(7)
	recv(t, h.r.starts, "StartTX")
	if f := recv(t, h.r.writes, "WriteVoice"); !bytes.Equal(f.Data, ambe(0)) {
		t.Errorf("short call frame %+v", f)
	}
	recv(t, h.r.ends, "EndTX")
}

func TestNetworkBusyDuringRF(t *testing.T) {
	h := start(t, 2, Config{})
	h.rfHeader(10, 2, src)
	recv(t, h.m[1].hdrs, "SendHeader")
	h.sync()
	h.m[1].header(5, src)
	for i := range 6 {
		h.m[1].frame(5, i)
	}
	h.sync()
	none(t, h.r.starts, "StartTX")
	if s := h.status(2); s.Busy != 1 || s.State != StateRF {
		t.Errorf("status %+v", s)
	}
	// The other slot is free.
	h.netOnAir(1, 6, src)
	// The RF call carries on.
	h.rfVoice(10, 0)
	recv(t, h.m[1].voice, "SendFrame")
}

func TestRFBusyDuringNetwork(t *testing.T) {
	h := start(t, 2, Config{})
	h.netOnAir(2, 5, src)
	h.rfHeader(10, 2, src)
	h.rfVoice(10, 0)
	h.sync()
	none(t, h.m[1].hdrs, "SendHeader")
	none(t, h.m[1].voice, "SendFrame")
	if s := h.status(2); s.Busy != 1 || s.State != StateNet {
		t.Errorf("status %+v", s)
	}
	// The network call is not cut.
	h.m[1].frame(5, 3)
	recv(t, h.r.writes, "WriteVoice")
	none(t, h.r.ends, "EndTX")
}

func TestRFWins(t *testing.T) {
	h := start(t, 2, Config{})
	m := h.m[1]
	m.header(5, src)
	m.frame(5, 0)
	h.sync()
	h.rfHeader(10, 2, src)
	recv(t, m.hdrs, "SendHeader")
	h.sync()
	for i := 1; i < 6; i++ {
		m.frame(5, i)
	}
	m.end(5)
	h.sync()
	none(t, h.r.starts, "StartTX")
	none(t, h.r.ends, "EndTX")
	if s := h.status(2); s.State != StateRF || s.Busy != 1 {
		t.Errorf("status %+v", s)
	}
}

func TestHang(t *testing.T) {
	h := start(t, 2, Config{Hang: time.Second, LoopGuard: time.Millisecond})
	m := h.m[1]

	// After an RF call the network waits out the hang; RF does not.
	h.rfHeader(10, 2, src)
	recv(t, m.hdrs, "SendHeader")
	h.sink.End(10)
	recv(t, m.ends, "EndFrames")
	h.sync()
	h.clk.add(500 * time.Millisecond)
	h.tick()
	m.header(5, src)
	h.sync()
	if s := h.status(2); s.State != StateHang || s.Busy != 1 {
		t.Errorf("status %+v", s)
	}
	h.rfHeader(11, 2, src)
	recv(t, m.hdrs, "SendHeader")
	h.sink.End(11)
	recv(t, m.ends, "EndFrames")
	h.sync()
	h.clk.add(time.Second)
	h.tick()
	h.clk.add(3 * time.Second) // past the loop guard too
	h.netOnAir(2, 6, src)

	// After a network call RF waits out the hang; the network does not.
	h.m[1].end(6)
	recv(t, h.r.ends, "EndTX")
	h.sync()
	h.rfHeader(12, 2, src)
	h.sync()
	none(t, m.hdrs, "SendHeader")
	if s := h.status(2); s.Busy != 2 {
		t.Errorf("status %+v", s)
	}
	h.netOnAir(2, 7, src)
	m.end(7)
	recv(t, h.r.ends, "EndTX")
	h.sync()
	h.clk.add(time.Second)
	h.tick()
	h.rfHeader(13, 2, src)
	recv(t, m.hdrs, "SendHeader")
}

func TestRFTimeout(t *testing.T) {
	h := start(t, 2, Config{TXTimeout: 10 * time.Second})
	m := h.m[1]
	h.rfHeader(10, 2, src)
	recv(t, m.hdrs, "SendHeader")
	h.rfVoice(10, 0)
	recv(t, m.voice, "SendFrame")
	h.sync()
	h.clk.add(11 * time.Second)
	h.rfVoice(10, 1)
	recv(t, m.ends, "EndFrames")
	h.rfVoice(10, 2)
	h.sink.End(10)
	h.sync()
	none(t, m.voice, "SendFrame after the timeout")
	none(t, m.ends, "a second EndFrames")
	if s := h.status(2); s.Timeouts != 1 || s.State != StateHang {
		t.Errorf("status %+v", s)
	}
}

func TestNetworkTimeout(t *testing.T) {
	h := start(t, 2, Config{TXTimeout: 10 * time.Second, LoopGuard: time.Millisecond})
	m := h.m[1]
	h.netOnAir(2, 5, src)
	h.clk.add(11 * time.Second)
	m.frame(5, 3)
	if slot := recv(t, h.r.ends, "EndTX"); slot != 2 {
		t.Errorf("EndTX(%d)", slot)
	}
	m.frame(5, 4)
	h.sync()
	none(t, h.r.writes, "WriteVoice after the timeout")
	// The slot is held until the network call ends: RF is busy.
	h.rfHeader(10, 2, src)
	h.sync()
	none(t, m.hdrs, "SendHeader")
	if s := h.status(2); s.Timeouts != 1 || s.Busy != 1 || s.State != StateNet {
		t.Errorf("status %+v", s)
	}
	m.end(5)
	h.sync()
	none(t, h.r.ends, "a second EndTX")
	h.clk.add(time.Second)
	h.tick()
	h.rfHeader(11, 2, src)
	recv(t, m.hdrs, "SendHeader")
}

func TestFrameTimeout(t *testing.T) {
	h := start(t, 2, Config{})
	h.rfHeader(10, 2, src)
	recv(t, h.m[1].hdrs, "SendHeader")
	h.netOnAir(1, 5, src)

	// 200 ms with no network frame: silence keeps the radio fed.
	h.clk.add(200 * time.Millisecond)
	h.tick()
	for i := range 5 {
		f := recv(t, h.r.writes, "silence")
		if !bytes.Equal(f.Data, dmr.SilenceAMBE[:]) {
			t.Fatalf("fill %d: %x", i, f.Data)
		}
	}
	none(t, h.r.writes, "more fill")
	none(t, h.r.ends, "EndTX")
	none(t, h.m[1].ends, "EndFrames")
	if s := h.status(1); s.Filled != 5 {
		t.Errorf("status %+v", s)
	}

	// Past 300 ms both calls end.
	h.clk.add(101 * time.Millisecond)
	h.tick()
	recv(t, h.m[1].ends, "EndFrames")
	if slot := recv(t, h.r.ends, "EndTX"); slot != 1 {
		t.Errorf("EndTX(%d)", slot)
	}
	for _, n := range []int{1, 2} {
		if s := h.status(n); s.State != StateHang {
			t.Errorf("slot %d: %+v", n, s)
		}
	}
	// Late frames of the ended calls are ignored.
	h.rfVoice(10, 1)
	h.m[0].frame(5, 9)
	h.sync()
	none(t, h.m[1].voice, "SendFrame")
	none(t, h.r.writes, "WriteVoice")
}

func TestLoopGuard(t *testing.T) {
	h := start(t, 2, Config{})
	m := h.m[1]
	// The network's echo of an RF call is dropped for the loop guard.
	h.rfHeader(10, 2, src)
	recv(t, m.hdrs, "SendHeader")
	h.sink.End(10)
	recv(t, m.ends, "EndFrames")
	h.sync()
	h.clk.add(time.Second)
	h.tick()
	m.header(6, src)
	for i := range 3 {
		m.frame(6, i)
	}
	h.sync()
	none(t, h.r.starts, "StartTX of a looped call")
	if s := h.status(2); s.Loops != 1 || s.Busy != 0 || s.State != StateIdle {
		t.Errorf("status %+v", s)
	}
	// On the other slot the guard does not apply.
	h.netOnAir(1, 7, src)
	h.m[0].end(7)
	recv(t, h.r.ends, "EndTX")

	h.clk.add(1500 * time.Millisecond)
	h.tick()
	if st := h.netOnAir(2, 8, src); st.SrcID != src {
		t.Errorf("StartTX %+v", st)
	}

	// Our own transmission heard back on RF is dropped for the loop guard.
	m.end(8)
	recv(t, h.r.ends, "EndTX")
	h.sync()
	h.clk.add(time.Second)
	h.tick()
	h.rfHeader(12, 2, src)
	h.sync()
	none(t, m.hdrs, "SendHeader of our own transmission")
	if s := h.status(2); s.Loops != 2 {
		t.Errorf("status %+v", s)
	}
	h.clk.add(1500 * time.Millisecond)
	h.tick()
	h.rfHeader(13, 2, src)
	recv(t, m.hdrs, "SendHeader")
}

func TestAbortOnCancel(t *testing.T) {
	h := start(t, 2, Config{})
	h.rfHeader(10, 1, src)
	recv(t, h.m[0].hdrs, "SendHeader")
	h.netOnAir(2, 5, src)
	h.cancel()
	if err := recv(t, h.done, "Run"); err != nil {
		t.Errorf("Run: %v", err)
	}
	h.done <- nil // for the cleanup
	recv(t, h.m[0].ends, "EndFrames")
	recv(t, h.r.aborts, "Abort")
	for i := range h.m {
		if on := recv(t, h.m[i].frames, "SetFrames"); on {
			t.Error("SetFrames(true) on the way out")
		}
	}
	none(t, h.m[1].ends, "EndFrames for a network call")
	for _, s := range h.e.Status() {
		if s.State != StateIdle || s.Mode != "" {
			t.Errorf("slot %+v", s)
		}
	}
	if h.e.do(h.ctx, func() {}) {
		t.Error("do after Run returned")
	}
}

func TestRadioError(t *testing.T) {
	h := start(t, 2, Config{})
	boom := errors.New("boom")
	h.r.runErr <- boom
	if err := recv(t, h.done, "Run"); !errors.Is(err, boom) {
		t.Errorf("Run: %v", err)
	}
	h.done <- nil
	recv(t, h.r.aborts, "Abort")
}

func TestLink(t *testing.T) {
	r := newFakeRadio(2)
	e := New(r, Config{})
	a, b := newFakeMode("A"), newFakeMode("B")
	if err := e.Link(3, a); !errors.Is(err, ErrNoSlot) {
		t.Errorf("slot 3: %v", err)
	}
	if err := e.Link(1, a); err != nil {
		t.Fatal(err)
	}
	recv(t, a.frames, "SetFrames")
	if err := e.Link(2, a); !errors.Is(err, ErrLinked) {
		t.Errorf("a on two slots: %v", err)
	}
	if err := e.Link(1, a); err != nil {
		t.Errorf("relink: %v", err)
	}
	none(t, a.frames, "SetFrames on relink")
	b.codec = network.CodecIMBE
	if err := e.Link(2, b); !errors.Is(err, ErrNoFrames) {
		t.Errorf("IMBE: %v", err)
	}
	b.codec = network.CodecAMBE
	// Leftover events from before the link are dropped.
	b.header(1, src)
	if err := e.Link(2, b); err != nil {
		t.Fatal(err)
	}
	if len(b.recv) != 0 {
		t.Error("leftover events kept")
	}
	if err := e.Link(1, nil); err != nil {
		t.Fatal(err)
	}
	if on := recv(t, a.frames, "SetFrames"); on {
		t.Error("SetFrames(true) on unlink")
	}
	if s := e.Status(); s[0].Mode != "" || s[1].Mode != "B" {
		t.Errorf("status %+v", s)
	}
	if got := e.Slots(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("slots %v", got)
	}
}

func TestLinkDuringCall(t *testing.T) {
	h := start(t, 2, Config{})
	h.rfHeader(10, 2, src)
	recv(t, h.m[1].hdrs, "SendHeader")
	h.sync()
	other := newFakeMode("C")
	if err := h.e.Link(2, other); err != nil {
		t.Fatal(err)
	}
	recv(t, h.m[1].ends, "EndFrames")
	recv(t, h.m[1].frames, "SetFrames")
	recv(t, other.frames, "SetFrames")
	h.clk.add(3 * time.Second) // past the loop guard of the ended RF call
	// The new mode's calls reach the radio.
	other.header(5, src)
	for i := range 3 {
		other.frame(5, i)
	}
	recv(t, h.r.starts, "StartTX")
	h.rfVoice(10, 1)
	h.sync()
	none(t, other.voice, "SendFrame")
}

func TestSimplex(t *testing.T) {
	h := start(t, 1, Config{})
	if got := h.e.Slots(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("slots %v", got)
	}
	h.rfHeader(10, 2, src)
	if hd := recv(t, h.m[0].hdrs, "SendHeader"); hd.Slot != 2 {
		t.Errorf("SendHeader %+v", hd)
	}
	h.sink.End(10)
	recv(t, h.m[0].ends, "EndFrames")
	h.clk.add(time.Minute)
	h.tick()
	if st := h.netOnAir(1, 5, src); st.Slot != 2 {
		t.Errorf("StartTX %+v", st)
	}

	e := New(newFakeRadio(1), Config{SimplexSlot: 1})
	if got := e.Slots(); got[0] != 1 {
		t.Errorf("slots %v", got)
	}
}

func TestRunTwice(t *testing.T) {
	h := start(t, 2, Config{})
	h.sync()
	if err := h.e.Run(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("second Run: %v", err)
	}
}
