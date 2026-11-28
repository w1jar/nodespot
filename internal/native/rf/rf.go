// Package rf is the node's RF endpoint for a digital radio (an MMDVM
// modem). It owns the radio's slots and routes calls between the radio and
// the mode linked to each slot, passing coded frames through with no
// vocoder (mode.FrameMode). It runs contention (first come first served,
// RF wins ties), the hang time, the TX timeouts, the frame timeout, the
// loop guard and a short prebuffer with silence fill for network jitter.
//
// Slot rules, per slot:
//
//   - Idle: the first call, RF or network, takes the slot.
//   - RF call: a network call on the slot is busy and dropped. The RF call
//     goes to the slot's mode (SendHeader, SendFrame, EndFrames).
//   - Network call, still prebuffering: an RF key-up wins and the network
//     call is dropped.
//   - Network call on the air: an RF key-up is busy and dropped (the
//     handheld cannot hear the network call while it transmits anyway).
//   - Hang: after a call ends the slot stays reserved for Hang for the
//     same source: RF after an RF call, the network after a network call.
//   - TX timeout: an RF call longer than TXTimeout is ended on the network
//     and its remaining frames dropped; a network call longer than
//     TXTimeout is ended on the radio and the slot is held until the
//     network call ends.
//   - Frame timeout: a call with no frame for FrameTimeout ends.
//   - Loop guard: a network call whose source ID is that of the slot's RF
//     call that ended within LoopGuard is dropped, and so is an RF call
//     whose source ID is that of the network call the slot sent to RF
//     within LoopGuard. (While a call is up the other side is busy
//     anyway.)
//
// When Run's context ends, RF calls are ended on their networks and the
// radio's transmitter is stopped (Abort).
package rf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// Defaults.
const (
	DefaultHang         = 500 * time.Millisecond // advanced.hangMs
	DefaultTXTimeout    = 180 * time.Second      // advanced.txTimeoutS
	DefaultFrameTimeout = 300 * time.Millisecond // the DV DefaultTxTimeout
	DefaultLoopGuard    = 2 * time.Second
	DefaultPrebuffer    = 3 // AMBE frames, one burst
	DefaultSimplexSlot  = 2
)

// Errors.
var (
	ErrNoSlot   = errors.New("rf: no such slot")
	ErrLinked   = errors.New("rf: the mode is already linked to another slot")
	ErrNoFrames = errors.New("rf: the mode does not carry AMBE frames")
	ErrRunning  = errors.New("rf: already running")
)

// Config configures an Endpoint. Zero values take the defaults.
type Config struct {
	Hang         time.Duration
	TXTimeout    time.Duration
	FrameTimeout time.Duration
	LoopGuard    time.Duration
	// Prebuffer is how many network frames are held before the radio
	// starts transmitting a network call.
	Prebuffer int
	// SimplexSlot is the slot number of a one-slot radio (default 2).
	SimplexSlot int
	// Tick is the housekeeping interval (20 ms).
	Tick   time.Duration
	Now    func() time.Time
	Logger *slog.Logger
}

func (c *Config) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.Hang, DefaultHang)
	def(&c.TXTimeout, DefaultTXTimeout)
	def(&c.FrameTimeout, DefaultFrameTimeout)
	def(&c.LoopGuard, DefaultLoopGuard)
	def(&c.Tick, audio.FrameDuration)
	if c.Prebuffer <= 0 {
		c.Prebuffer = DefaultPrebuffer
	}
	if c.SimplexSlot != 1 {
		c.SimplexSlot = DefaultSimplexSlot
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
}

// State is what a slot is doing.
type State string

// Slot states.
const (
	StateIdle    State = "idle"
	StateRF      State = "rf"      // an RF call goes to the network
	StatePending State = "pending" // a network call is prebuffering
	StateNet     State = "net"     // a network call is on the air
	StateHang    State = "hang"
)

// SlotStatus is a snapshot of one slot.
type SlotStatus struct {
	Slot  int    `json:"slot"`
	Mode  string `json:"mode,omitempty"`
	State State  `json:"state"`
	// HangRF is the side the hang is reserved for (true: after an RF call).
	HangRF bool      `json:"hangRf,omitempty"`
	SrcID  uint32    `json:"srcId,omitempty"`
	TG     uint32    `json:"tg,omitempty"`
	Since  time.Time `json:"since,omitempty"`

	Busy     uint64 `json:"busy"`     // calls dropped because the slot was in use
	Loops    uint64 `json:"loops"`    // calls dropped by the loop guard
	Timeouts uint64 `json:"timeouts"` // calls cut by the TX timeout
	Filled   uint64 `json:"filled"`   // silence frames sent for missing network frames
	Errors   uint64 `json:"errors"`   // radio or mode calls that failed
}

type rfEvent struct {
	kind  mode.FrameKind
	h     network.CallHeader
	f     network.VoiceFrame
	call  uint64
	voice bool
}

type slot struct {
	n int
	m mode.FrameMode

	state  State
	since  time.Time
	hangRF bool
	until  time.Time // hang end

	// RF call.
	rfCall  uint64 // the radio's call id
	rfHdr   network.CallHeader
	rfStart time.Time
	rfLast  time.Time
	rfNet   bool // the mode took the header
	rfCut   bool // TX timeout: the rest is dropped
	rfGone  uint64

	// Network call.
	netCall  uint64 // the mode's call id
	netHdr   network.CallHeader
	netStart time.Time
	netLast  time.Time
	netCut   bool // TX timeout: held until the network call ends
	pre      [][]byte
	txCall   uint64 // our call id on the radio
	txStart  time.Time
	txFrames int

	// Loop guard.
	lastRFSrc  uint32
	lastRFEnd  time.Time
	lastNetSrc uint32
	lastNetEnd time.Time

	st SlotStatus
}

// Endpoint is the RF endpoint of one digital radio.
type Endpoint struct {
	r   radio.DigitalRadio
	cfg Config
	log *slog.Logger

	rfEv   chan rfEvent
	relink chan struct{}
	calls  chan func()

	mu      sync.Mutex
	slots   []*slot
	running bool
	ids     uint64
	dropped uint64
}

// New returns an endpoint for r. Its slots are 1 and 2 for a two-slot
// radio, or the one SimplexSlot.
func New(r radio.DigitalRadio, cfg Config) *Endpoint {
	cfg.defaults()
	e := &Endpoint{r: r, cfg: cfg, log: cfg.Logger,
		rfEv: make(chan rfEvent, 1024), relink: make(chan struct{}, 1), calls: make(chan func())}
	if r.Slots() >= 2 {
		e.slots = []*slot{{n: 1}, {n: 2}}
	} else {
		e.slots = []*slot{{n: cfg.SimplexSlot}}
	}
	now := cfg.Now()
	for _, s := range e.slots {
		s.state, s.since = StateIdle, now
		s.st.Slot = s.n
	}
	return e
}

// Slots lists the endpoint's slot numbers.
func (e *Endpoint) Slots() []int {
	out := make([]int, len(e.slots))
	for i, s := range e.slots {
		out[i] = s.n
	}
	return out
}

func (e *Endpoint) index(slot int) (int, bool) {
	if len(e.slots) == 1 {
		return 0, slot == 0 || slot == e.slots[0].n
	}
	if slot == 1 || slot == 2 {
		return slot - 1, true
	}
	return 0, false
}

// rfIndex maps the slot of an RF call to a slot index: a one-slot radio
// has only one.
func (e *Endpoint) rfIndex(slot int) (int, bool) {
	if len(e.slots) == 1 {
		return 0, true
	}
	return e.index(slot)
}

// Link links m to slot (nil unlinks). The mode's received calls switch to
// coded frames. A call on the slot ends first. A mode can be linked to
// one slot only, and must carry AMBE.
func (e *Endpoint) Link(slotN int, m mode.FrameMode) error {
	i, ok := e.index(slotN)
	if !ok {
		return ErrNoSlot
	}
	if m != nil {
		if c, ok := m.FrameCodec(); ok && c != network.CodecAMBE {
			return ErrNoFrames
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if m != nil {
		for j, o := range e.slots {
			if j != i && o.m == m {
				return ErrLinked
			}
		}
	}
	s := e.slots[i]
	if s.m == m {
		return nil
	}
	now := e.cfg.Now()
	e.endRF(s, now, false)
	e.endNet(s, now, false)
	e.unlinkLocked(s)
	s.state, s.since = StateIdle, now
	if m == nil {
		e.snap(s)
		return nil
	}
	s.m = m
	m.SetFrames(true)
	drain(m.RecvFrames())
	e.snap(s)
	select {
	case e.relink <- struct{}{}:
	default:
	}
	return nil
}

// drain empties ch of events left over from before.
func drain(ch <-chan mode.FrameEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (e *Endpoint) unlinkLocked(s *slot) {
	if s.m == nil {
		return
	}
	s.m.SetFrames(false)
	s.m = nil
}

// Status is a snapshot of every slot.
func (e *Endpoint) Status() []SlotStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]SlotStatus, len(e.slots))
	for i, s := range e.slots {
		out[i] = s.st
	}
	return out
}

func (e *Endpoint) snap(s *slot) {
	s.st.State, s.st.Since, s.st.HangRF = s.state, s.since, s.hangRF && s.state == StateHang
	s.st.Mode = ""
	if s.m != nil {
		s.st.Mode = s.m.Name()
	}
	switch s.state {
	case StateRF:
		s.st.SrcID, s.st.TG = s.rfHdr.SrcID, s.rfHdr.TG
	case StatePending, StateNet:
		s.st.SrcID, s.st.TG = s.netHdr.SrcID, s.netHdr.TG
	default:
		s.st.SrcID, s.st.TG = 0, 0
	}
}

// rfSink is what the radio reports, queued for the loop.
type rfSink struct{ e *Endpoint }

func (k rfSink) push(ev rfEvent) {
	select {
	case k.e.rfEv <- ev:
	default:
		k.e.mu.Lock()
		k.e.dropped++
		k.e.mu.Unlock()
	}
}

func (k rfSink) Header(h network.CallHeader) {
	k.push(rfEvent{kind: mode.FrameHeader, h: h, call: h.Call})
}
func (k rfSink) Voice(f network.VoiceFrame) {
	f.Data = append([]byte(nil), f.Data...)
	k.push(rfEvent{kind: mode.FrameVoice, f: f, call: f.Call})
}
func (k rfSink) End(call uint64)              { k.push(rfEvent{kind: mode.FrameEnd, call: call}) }
func (k rfSink) Login(ctl.LoginState, string) {}

// Run runs the radio and routes calls until ctx ends. On the way out RF
// calls are ended on their networks, the radio's transmitter is stopped,
// and every slot is unlinked.
func (e *Endpoint) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return ErrRunning
	}
	e.running = true
	e.mu.Unlock()

	rctx, cancel := context.WithCancel(ctx)
	radioDone := make(chan error, 1)
	go func() { radioDone <- e.r.Run(rctx, rfSink{e}) }()
	t := time.NewTicker(e.cfg.Tick)
	defer t.Stop()

	var err error
loop:
	for {
		var net [2]<-chan mode.FrameEvent
		e.mu.Lock()
		for i, s := range e.slots {
			if s.m != nil {
				net[i] = s.m.RecvFrames()
			}
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			break loop
		case err = <-radioDone:
			radioDone = nil
			if err != nil {
				err = fmt.Errorf("rf: radio: %w", err)
			}
			break loop
		case ev := <-e.rfEv:
			e.mu.Lock()
			e.onRF(ev)
			e.mu.Unlock()
		case ev := <-net[0]:
			e.mu.Lock()
			e.onNet(0, net[0], ev)
			e.mu.Unlock()
		case ev := <-net[1]:
			e.mu.Lock()
			e.onNet(1, net[1], ev)
			e.mu.Unlock()
		case <-e.relink:
		case f := <-e.calls:
			e.mu.Lock()
			e.flush(net)
			f()
			e.mu.Unlock()
		case <-t.C:
			e.mu.Lock()
			e.housekeeping()
			e.mu.Unlock()
		}
	}

	e.mu.Lock()
	now := e.cfg.Now()
	for _, s := range e.slots {
		e.endRF(s, now, false)
		s.netCall, s.txCall, s.pre = 0, 0, nil
		e.unlinkLocked(s)
		s.state, s.since = StateIdle, now
		e.snap(s)
	}
	e.running = false
	e.mu.Unlock()
	e.r.Abort()
	cancel()
	if radioDone != nil {
		<-radioDone
	}
	return err
}

// flush handles every event already queued, RF and network. Called with
// e.mu held.
func (e *Endpoint) flush(net [2]<-chan mode.FrameEvent) {
	for {
		select {
		case ev := <-e.rfEv:
			e.onRF(ev)
			continue
		case ev := <-net[0]:
			e.onNet(0, net[0], ev)
			continue
		case ev := <-net[1]:
			e.onNet(1, net[1], ev)
			continue
		default:
		}
		return
	}
}

// do runs f in the loop once every queued event is handled, and waits
// for it. It reports false if the loop is not running.
func (e *Endpoint) do(ctx context.Context, f func()) bool {
	done := make(chan struct{})
	select {
	case e.calls <- func() { f(); close(done) }:
	case <-ctx.Done():
		return false
	}
	<-done
	return true
}

// ---- RF side ----

func (e *Endpoint) onRF(ev rfEvent) {
	now := e.cfg.Now()
	switch ev.kind {
	case mode.FrameHeader:
		i, ok := e.rfIndex(ev.h.Slot)
		if !ok {
			return
		}
		e.rfHeader(e.slots[i], ev.h, now)
	case mode.FrameVoice:
		for _, s := range e.slots {
			if s.state == StateRF && s.rfCall == ev.call {
				e.rfVoice(s, ev.f, now)
				return
			}
		}
	case mode.FrameEnd:
		for _, s := range e.slots {
			if s.state == StateRF && s.rfCall == ev.call {
				e.endRF(s, now, true)
				e.snap(s)
				return
			}
		}
	}
}

func (e *Endpoint) rfHeader(s *slot, h network.CallHeader, now time.Time) {
	if s.m == nil {
		return
	}
	src := h.SrcID
	switch {
	case s.state == StateRF && s.rfCall == h.Call:
		return
	case s.state == StateNet, s.state == StateHang && !s.hangRF:
		s.st.Busy++
		e.log.Info("rf: RF call busy: the slot is the network's", "slot", s.n, "state", s.state, "src", src)
		return
	case src != 0 && src == s.lastNetSrc && now.Sub(s.lastNetEnd) < e.cfg.LoopGuard:
		s.st.Loops++
		e.log.Info("rf: dropped an RF call that is our own transmission", "slot", s.n, "src", src)
		return
	case s.state == StateRF:
		// A new RF call replaces one that never ended.
		e.endRF(s, now, false)
	case s.state == StatePending:
		// RF wins over a network call that has not reached the air.
		e.log.Info("rf: RF call takes the slot from a pending network call", "slot", s.n)
		s.st.Busy++
		e.dropNet(s)
	}
	s.state, s.since = StateRF, now
	s.rfCall, s.rfHdr, s.rfStart, s.rfLast = h.Call, h, now, now
	s.rfCut = false
	s.rfNet = true
	hh := h
	hh.Slot = s.n
	if err := s.m.SendHeader(hh); err != nil {
		s.rfNet = false
		s.st.Errors++
		e.log.Warn("rf: the mode refused the RF call", "slot", s.n, "err", err)
	}
	e.snap(s)
}

func (e *Endpoint) rfVoice(s *slot, f network.VoiceFrame, now time.Time) {
	s.rfLast = now
	if s.rfCut || !s.rfNet {
		return
	}
	if now.Sub(s.rfStart) > e.cfg.TXTimeout {
		s.rfCut = true
		s.st.Timeouts++
		e.log.Info("rf: RF call cut by the TX timeout", "slot", s.n, "src", s.rfHdr.SrcID)
		_ = s.m.EndFrames()
		return
	}
	f.Codec = network.CodecAMBE
	if err := s.m.SendFrame(f); err != nil {
		s.st.Errors++
	}
}

// endRF ends the slot's RF call; hang starts the hang time.
func (e *Endpoint) endRF(s *slot, now time.Time, hang bool) {
	if s.state != StateRF {
		return
	}
	if s.rfNet && !s.rfCut && s.m != nil {
		_ = s.m.EndFrames()
	}
	s.lastRFSrc, s.lastRFEnd = s.rfHdr.SrcID, now
	s.rfGone = s.rfCall
	s.rfCall = 0
	e.toHang(s, now, true, hang)
}

func (e *Endpoint) toHang(s *slot, now time.Time, rf, hang bool) {
	if hang {
		s.state, s.hangRF, s.until = StateHang, rf, now.Add(e.cfg.Hang)
	} else {
		s.state = StateIdle
	}
	s.since = now
}

// ---- network side ----

func (e *Endpoint) onNet(i int, from <-chan mode.FrameEvent, ev mode.FrameEvent) {
	s := e.slots[i]
	if s.m == nil || s.m.RecvFrames() != from {
		return
	}
	now := e.cfg.Now()
	switch ev.Kind {
	case mode.FrameHeader:
		e.netHeader(s, ev.Call, ev.Header, now)
	case mode.FrameVoice:
		if s.netCall != ev.Call || (s.state != StatePending && s.state != StateNet) {
			return
		}
		s.netLast = now
		e.netVoice(s, ev.Frame.Data, now)
	case mode.FrameEnd:
		if s.netCall != ev.Call {
			return
		}
		switch s.state {
		case StatePending, StateNet:
			e.endNet(s, now, true)
		}
		s.netCall = 0
		e.snap(s)
	}
}

func (e *Endpoint) netHeader(s *slot, call uint64, h network.CallHeader, now time.Time) {
	src := h.SrcID
	switch {
	case s.state != StateIdle && (s.state != StateHang || s.hangRF):
		s.st.Busy++
		e.log.Info("rf: network call busy", "slot", s.n, "state", s.state, "src", src)
		return
	case src != 0 && src == s.lastRFSrc && now.Sub(s.lastRFEnd) < e.cfg.LoopGuard:
		s.st.Loops++
		e.log.Info("rf: dropped a network call from the RF talker", "slot", s.n, "src", src)
		return
	}
	s.state, s.since = StatePending, now
	s.netCall, s.netHdr, s.netStart, s.netLast = call, h, now, now
	s.netCut, s.pre, s.txCall, s.txFrames = false, nil, 0, 0
	e.snap(s)
}

func (e *Endpoint) netVoice(s *slot, data []byte, now time.Time) {
	if len(data) != dmr.AMBELen {
		return
	}
	switch s.state {
	case StatePending:
		if s.netCut {
			return
		}
		s.pre = append(s.pre, data)
		if len(s.pre) >= e.cfg.Prebuffer {
			e.startNetTX(s, now)
		}
	case StateNet:
		if s.netCut {
			return
		}
		if now.Sub(s.netStart) > e.cfg.TXTimeout {
			s.netCut = true
			s.st.Timeouts++
			e.log.Info("rf: network call cut by the TX timeout", "slot", s.n, "src", s.netHdr.SrcID)
			if err := e.r.EndTX(s.n); err != nil {
				s.st.Errors++
			}
			return
		}
		e.write(s, data)
	}
}

// startNetTX puts the pending network call on the air with its prebuffer.
func (e *Endpoint) startNetTX(s *slot, now time.Time) {
	e.ids++
	s.txCall = e.ids
	h := s.netHdr
	h.Call, h.Slot = s.txCall, s.n
	if err := e.r.StartTX(h); err != nil {
		s.st.Errors++
		e.log.Warn("rf: the radio refused the network call", "slot", s.n, "err", err)
		// Keep the slot as the network's so the call stays off the air
		// and RF can still take it.
		s.pre = nil
		s.txCall = 0
		s.state, s.since = StatePending, now
		s.netCut = true
		return
	}
	s.state, s.since = StateNet, now
	s.txStart, s.txFrames = now, 0
	for _, d := range s.pre {
		e.write(s, d)
	}
	s.pre = nil
	e.snap(s)
}

func (e *Endpoint) write(s *slot, data []byte) {
	s.txFrames++
	if err := e.r.WriteVoice(network.VoiceFrame{Call: s.txCall, Codec: network.CodecAMBE, Data: data}); err != nil {
		s.st.Errors++
	}
}

// endNet ends the slot's network call on the radio.
func (e *Endpoint) endNet(s *slot, now time.Time, hang bool) {
	switch s.state {
	case StatePending:
		if len(s.pre) > 0 && !s.netCut {
			e.startNetTX(s, now) // a call shorter than the prebuffer
		}
	case StateNet:
	default:
		return
	}
	if s.state == StateNet && !s.netCut {
		if err := e.r.EndTX(s.n); err != nil {
			s.st.Errors++
		}
	}
	s.lastNetSrc, s.lastNetEnd = s.netHdr.SrcID, now
	s.pre, s.txCall = nil, 0
	e.toHang(s, now, false, hang)
}

// dropNet forgets the pending network call: its frames are ignored.
func (e *Endpoint) dropNet(s *slot) {
	s.pre = nil
	s.txCall = 0
	s.state = StateIdle
}

// ---- housekeeping ----

func (e *Endpoint) housekeeping() {
	now := e.cfg.Now()
	for _, s := range e.slots {
		before := s.state
		switch s.state {
		case StateRF:
			if now.Sub(s.rfLast) > e.cfg.FrameTimeout {
				e.endRF(s, now, true)
			}
		case StatePending, StateNet:
			if now.Sub(s.netLast) > e.cfg.FrameTimeout {
				e.endNet(s, now, true)
				s.netCall = 0
				break
			}
			if s.state == StateNet && !s.netCut {
				e.fill(s, now)
			}
		case StateHang:
			if !now.Before(s.until) {
				s.state, s.since = StateIdle, now
			}
		}
		if s.state != before {
			e.snap(s)
		}
	}
}

// fill sends silence when the network has fallen behind the air by more
// than the prebuffer, so the radio does not run dry mid-call.
func (e *Endpoint) fill(s *slot, now time.Time) {
	due := int(now.Sub(s.txStart)/audio.FrameDuration) + 1
	for s.txFrames+e.cfg.Prebuffer < due {
		silence := dmr.SilenceAMBE
		e.write(s, silence[:])
		s.st.Filled++
	}
}
