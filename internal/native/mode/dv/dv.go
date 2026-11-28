package dv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/imbe"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// DefaultMD380 is the software AMBE vocoder address used when the node gives none.
const DefaultMD380 = "127.0.0.1:2470"

// Timing defaults, the same as the DV module's.
const (
	DefaultRxSilence = 1500 * time.Millisecond // a received call with no voice this long ends
	DefaultTxTimeout = 300 * time.Millisecond  // a transmission with no audio this long ends
	DefaultLoginWait = 12 * time.Second        // how long a connect shows "waiting for login" before an error
	stopWait         = 3 * time.Second
)

// Options are injectable for tests. Zero values use the real thing.
type Options struct {
	// NewNetwork builds the network client; default network.New.
	NewNetwork func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error)
	// AMBE is the AMBE vocoder; default the software AMBE vocoder at Env.MD380.
	AMBE      vocoder.AMBE
	NewIMBE   func() vocoder.IMBE
	NewCodec2 func() vocoder.Codec2
	RxSilence time.Duration
	TxTimeout time.Duration
	LoginWait time.Duration
	// Tick is the housekeeping interval (20 ms).
	Tick time.Duration
}

// netEvent is what the network's Sink reports, queued for the mode's loop.
type netEvent struct {
	gen    uint64
	hdr    *network.CallHeader
	frame  *network.VoiceFrame
	end    bool
	call   uint64
	login  bool
	state  ctl.LoginState
	detail string
}

type job struct {
	call  uint64
	seq   uint32
	codec network.Codec
	pcm   audio.Frame
	data  []byte
	end   bool
	start *network.CallHeader // the first frame of a transmission: StartTX first
	raw   bool                // data is already coded (frame passthrough)
	only  bool                // start only, no voice frame (SendHeader)
}

type sink struct {
	m   *Mode
	gen uint64
}

func (s sink) push(ev netEvent) {
	ev.gen = s.gen
	select {
	case s.m.netEv <- ev:
	default: // never block the network
	}
}

func (s sink) Header(h network.CallHeader) { s.push(netEvent{hdr: &h}) }
func (s sink) Voice(f network.VoiceFrame) {
	f.Data = append([]byte(nil), f.Data...)
	s.push(netEvent{frame: &f})
}
func (s sink) End(c uint64) { s.push(netEvent{end: true, call: c}) }
func (s sink) Login(st ctl.LoginState, d string) {
	s.push(netEvent{login: true, state: st, detail: d})
}

// Mode is one DV network as a node mode.
type Mode struct {
	mode.Base

	d    def
	env  mode.Env
	s    mode.Settings
	cfg  ctl.NetConfig
	id   ctl.Identity
	opt  Options
	ambe vocoder.AMBE
	own  *vocoder.MD380 // closed on Stop when the mode made it

	netEv  chan netEvent
	enc    chan job
	dec    chan job
	frameC chan mode.FrameEvent

	mu       sync.Mutex
	gen      uint64
	n        network.Network
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	loggedIn bool
	loginAt  time.Time // when a connect started waiting for the login
	target   *mode.Target
	nt       ctl.Target
	pending  bool // target set, Tune not yet done (waiting for login)
	nextID   uint64

	// TX (local transmission), guarded by mu.
	tx     uint64 // call id, 0 = none
	txSeq  uint32
	txLast time.Time
	// rfTalk is true while the transmission is an RF call's (SendHeader)
	// and its talker is shown.
	rfTalk bool

	// frames: received calls go to frameC as coded frames, not to the
	// decoder (guarded by mu).
	frames bool

	// RX (network call), owned by the loop goroutine.
	rxCall uint64 // the network's call id
	rxID   uint64 // our call id
	rxLast time.Time
	rxOn   bool
}

var (
	_ mode.Mode      = (*Mode)(nil)
	_ mode.FrameMode = (*Mode)(nil)
)

// New builds the DV mode name from its settings.
func New(name string, env mode.Env, s mode.Settings, opt Options) (*Mode, error) {
	d, ok := lookupDef(name)
	if !ok {
		return nil, fmt.Errorf("dv: unknown mode %q", name)
	}
	cfg, id, err := d.netConfig(env, s)
	if err != nil {
		return nil, err
	}
	if opt.NewNetwork == nil {
		opt.NewNetwork = network.New
	}
	if opt.NewIMBE == nil {
		opt.NewIMBE = func() vocoder.IMBE { return imbe.NewCodec() }
	}
	if opt.NewCodec2 == nil {
		opt.NewCodec2 = func() vocoder.Codec2 { return vocoder.NewCodec2() }
	}
	if opt.RxSilence <= 0 {
		opt.RxSilence = DefaultRxSilence
	}
	if opt.TxTimeout <= 0 {
		opt.TxTimeout = DefaultTxTimeout
	}
	if opt.LoginWait <= 0 {
		opt.LoginWait = DefaultLoginWait
	}
	if opt.Tick <= 0 {
		opt.Tick = audio.FrameDuration
	}
	m := &Mode{d: d, env: env, s: s.Clone(), cfg: cfg, id: id, opt: opt,
		netEv: make(chan netEvent, 512), enc: make(chan job, 64), dec: make(chan job, 64),
		frameC: make(chan mode.FrameEvent, 256)}
	m.Init(d.name, d.title, env.Clock())
	return m, nil
}

func (m *Mode) vocoder() vocoder.AMBE {
	if m.opt.AMBE != nil {
		return m.opt.AMBE
	}
	if m.ambe == nil {
		addr := m.env.MD380
		if addr == "" {
			addr = DefaultMD380
		}
		m.own = vocoder.NewMD380(addr, m.env.MD380Timeout)
		m.ambe = m.own
	}
	return m.ambe
}

// Start builds a fresh network client and runs it. A DMR network logs in
// to its master; the others wait for a target.
func (m *Mode) Start(context.Context) error {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return nil
	}
	n, err := m.opt.NewNetwork(m.cfg, m.id)
	if err != nil {
		m.mu.Unlock()
		m.SetState(mode.StateError, "", err.Error())
		return fmt.Errorf("%s: %w", m.d.title, err)
	}
	// A network that has debug lines (BrandMeister) logs to the node's
	// logger, so they show with the node's -debug flag.
	if l, ok := n.(interface{ SetLogger(*slog.Logger) }); ok {
		l.SetLogger(m.env.Logger())
	}
	m.gen++
	gen := m.gen
	m.n = n
	m.loggedIn = !m.d.login
	if n.Codec() == network.CodecAMBE {
		m.ambe = m.vocoder()
	}
	// The goroutines keep their own copy of the vocoder: Stop clears the
	// field, and a goroutine that outlives Stop's wait must not see nil.
	ambe := m.ambe
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	if m.d.login {
		m.SetState(mode.StateStarting, "logging in", "")
	} else {
		m.SetState(mode.StateReady, "", "")
	}
	m.wg.Add(4)
	go func() {
		defer m.wg.Done()
		if err := n.Run(ctx, sink{m, gen}); err != nil && ctx.Err() == nil {
			m.env.Logger().Warn(m.d.name+": network stopped", "err", err)
			m.SetState(mode.StateError, "", err.Error())
		}
	}()
	go m.loop(ctx, gen)
	go m.encoder(ctx, n, ambe)
	go m.decoder(ctx, ambe)
	return nil
}

// Connect tunes the network to t. A DMR network that has not logged in
// yet tunes once it has.
func (m *Mode) Connect(ctx context.Context, t mode.Target) error {
	nt, err := m.d.netTarget(m.s, m.cfg, t)
	if err != nil {
		return fmt.Errorf("%s: %w", m.d.title, err)
	}
	if t.Name == "" {
		t.Name = nt.Name
	}
	m.mu.Lock()
	if m.cancel == nil {
		m.mu.Unlock()
		return fmt.Errorf("%s: mode is not started", m.d.title)
	}
	n, in := m.n, m.loggedIn
	tt := t
	m.target, m.nt = &tt, nt
	m.pending = !in
	if !in {
		m.loginAt = m.Now()
	}
	m.mu.Unlock()
	m.cutRX()
	m.SetTarget(&tt)
	if !in {
		m.SetState(mode.StateConnecting, "waiting for login", "")
		return nil
	}
	return m.tune(ctx, n, tt, nt)
}

func (m *Mode) tune(ctx context.Context, n network.Network, t mode.Target, nt ctl.Target) error {
	m.SetState(mode.StateConnecting, "linking to "+describe(t), "")
	if err := n.Tune(ctx, nt); err != nil {
		msg := fmt.Sprintf("link to %s: %v", describe(t), err)
		m.SetState(mode.StateError, "", msg)
		return fmt.Errorf("%s: %w", m.d.title, err)
	}
	if m.d.login {
		// A master has no per-talkgroup answer: tuned is connected.
		m.SetState(mode.StateConnected, describe(t), "")
	}
	return nil
}

func describe(t mode.Target) string {
	if _, err := strconv.Atoi(t.ID); err == nil {
		return "TG " + t.ID
	}
	return t.ID
}

// Disconnect unlinks: a DMR network drops its talkgroup, a reflector
// network leaves the reflector.
func (m *Mode) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	n := m.n
	m.target, m.pending = nil, false
	in := m.loggedIn
	m.mu.Unlock()
	m.cutRX()
	m.SetTarget(nil)
	if n == nil {
		return nil
	}
	err := n.Unlink(ctx)
	if in {
		m.SetState(mode.StateReady, "", "")
	} else {
		m.SetState(mode.StateStarting, "logging in", "")
	}
	if err != nil {
		return fmt.Errorf("%s: unlink: %w", m.d.title, err)
	}
	return nil
}

// Stop ends the network session. The next Start builds a new client.
func (m *Mode) Stop() error {
	m.mu.Lock()
	cancel, n := m.cancel, m.n
	tx := m.tx
	m.cancel, m.n, m.tx = nil, nil, 0
	m.target, m.pending, m.loggedIn = nil, false, false
	m.gen++
	var own *vocoder.MD380
	if cancel != nil {
		own = m.own
		m.own, m.ambe = nil, nil
	}
	m.mu.Unlock()
	if cancel == nil {
		return nil
	}
	if tx != 0 && n != nil {
		_ = n.EndTX(tx)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		// Close the vocoder only once nothing can use it.
		if own != nil {
			_ = own.Close()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopWait):
		m.env.Logger().Warn(m.d.name + ": network did not stop in time")
	}
	m.TalkEnd()
	m.Flush()
	m.SetTarget(nil)
	m.SetState(mode.StateIdle, "", "")
	return nil
}

// Send encodes one frame of radio audio for the network, starting a
// transmission on the first. It never blocks: StartTX, the encoding and
// the network writes run on the encoder goroutine, so a slow network
// cannot hold up the node's 20 ms step.
func (m *Mode) Send(f audio.Frame) {
	m.mu.Lock()
	n := m.n
	if n == nil || m.target == nil || m.pending {
		m.mu.Unlock()
		return
	}
	now := m.Now()
	start := m.tx == 0
	if start {
		m.nextID++
		m.tx, m.txSeq = m.nextID, 0
	}
	id := m.tx
	seq := m.txSeq
	m.txSeq++
	m.txLast = now
	j := job{call: id, seq: seq, codec: n.Codec(), pcm: f}
	if start {
		j.start = &network.CallHeader{Call: id, Src: m.id.Callsign, SrcID: m.id.DMRID,
			TG: m.nt.TG, Room: m.nt.Room, Slot: m.nt.Slot, Private: m.nt.Private}
	}
	m.mu.Unlock()
	if start {
		m.cutRX() // the local transmission wins over a received call
	}
	select {
	case m.enc <- j:
	default:
		if start {
			// The header did not make it: start again on the next frame.
			m.mu.Lock()
			if m.tx == id {
				m.tx = 0
			}
			m.mu.Unlock()
		}
	}
}

// EndTx ends the transmission behind the frames already queued. Like Send
// it never blocks.
func (m *Mode) EndTx() {
	m.mu.Lock()
	id := m.tx
	m.tx = 0
	n := m.n
	rfTalk := m.rfTalk
	m.rfTalk = false
	m.mu.Unlock()
	if rfTalk {
		m.TalkEnd()
	}
	if id == 0 {
		return
	}
	select {
	case m.enc <- job{call: id, end: true}:
	default:
		if n != nil {
			go func() { _ = n.EndTX(id) }()
		}
	}
}

func (m *Mode) encoder(ctx context.Context, n network.Network, ambe vocoder.AMBE) {
	defer m.wg.Done()
	im := &imbeState{newf: m.opt.NewIMBE}
	c2 := &c2State{newf: m.opt.NewCodec2}
	var failed uint64 // a call whose StartTX failed: its frames are dropped
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-m.enc:
			if j.call == failed {
				continue
			}
			if j.end {
				if err := n.EndTX(j.call); err != nil {
					m.env.Logger().Warn(m.d.name+": end transmit", "err", err)
				}
				continue
			}
			if j.start != nil {
				if err := n.StartTX(*j.start); err != nil {
					m.env.Logger().Warn(m.d.name+": start transmit", "err", err)
					failed = j.call
					m.mu.Lock()
					if m.tx == j.call {
						m.tx = 0 // the next frame tries again
					}
					m.mu.Unlock()
					continue
				}
			}
			if j.only {
				continue
			}
			pcm := [vocoder.PCMSamples]int16(j.pcm)
			f := network.VoiceFrame{Call: j.call, Seq: j.seq, Codec: j.codec}
			switch {
			case j.raw:
				f.Data = j.data
			case j.codec == network.CodecIMBE:
				b := im.encode(j.call, &pcm)
				f.Data = b[:]
			case j.codec == network.CodecC2:
				b := c2.encode(j.call, &pcm)
				f.Data = b[:]
			default:
				if ambe == nil {
					continue
				}
				b, err := ambe.Encode(ctx, &pcm)
				if err != nil {
					b = vocoder.SilenceAMBE
				}
				f.Data = b[:]
			}
			if err := n.WriteVoice(f); err != nil {
				m.env.Logger().Debug(m.d.name+": write voice", "err", err)
			}
		}
	}
}

func (m *Mode) decoder(ctx context.Context, ambe vocoder.AMBE) {
	defer m.wg.Done()
	im := &imbeState{newf: m.opt.NewIMBE}
	c2 := &c2State{newf: m.opt.NewCodec2}
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-m.dec:
			var pcm [vocoder.PCMSamples]int16
			switch j.codec {
			case network.CodecIMBE:
				var b [vocoder.IMBEBytes]byte
				copy(b[:], j.data)
				pcm = im.decode(j.call, &b)
			case network.CodecC2:
				var b [vocoder.C2Bytes]byte
				copy(b[:], j.data)
				pcm = c2.decode(j.call, &b)
			default:
				var b [vocoder.AMBEBytes]byte
				copy(b[:], j.data)
				if ambe == nil {
					continue
				}
				p, err := ambe.Decode(ctx, b)
				if err != nil {
					continue
				}
				pcm = p
			}
			if m.rxCurrent(j.call) {
				m.Deliver(audio.Frame(pcm))
			}
		}
	}
}

// rxCurrent reports whether call is still the received call (a cut call's
// queued audio is dropped).
func (m *Mode) rxCurrent(id uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rxOn && m.rxID == id
}

func (m *Mode) loop(ctx context.Context, gen uint64) {
	defer m.wg.Done()
	t := time.NewTicker(m.opt.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-m.netEv:
			if ev.gen != gen {
				continue
			}
			m.onNet(ctx, ev)
		case <-t.C:
			m.housekeeping()
		}
	}
}

func (m *Mode) onNet(ctx context.Context, ev netEvent) {
	now := m.Now()
	switch {
	case ev.login:
		m.onLogin(ctx, ev.state, ev.detail)
	case ev.hdr != nil:
		m.mu.Lock()
		busy := m.tx != 0 || m.rxOn
		m.mu.Unlock()
		if busy {
			return
		}
		h := ev.hdr
		m.mu.Lock()
		m.nextID++
		m.rxOn, m.rxCall, m.rxID, m.rxLast = true, h.Call, m.nextID, now
		frames, id := m.frames, m.rxID
		m.mu.Unlock()
		if frames {
			hh := *h
			hh.Call = id
			m.pushFrame(mode.FrameEvent{Kind: mode.FrameHeader, Call: id, Header: hh})
		}
		tgt := h.Room
		if tgt == "" && h.TG != 0 {
			tgt = strconv.FormatUint(uint64(h.TG), 10)
		}
		m.TalkStart(mode.Talker{Callsign: h.Src, ID: h.SrcID, Target: tgt, Since: now})
	case ev.frame != nil:
		f := ev.frame
		m.mu.Lock()
		ok := m.rxOn && m.rxCall == f.Call
		if ok {
			m.rxLast = now
		}
		id, frames := m.rxID, m.frames
		m.mu.Unlock()
		if !ok || codecLen(f.Codec) == 0 || len(f.Data) != codecLen(f.Codec) {
			return
		}
		if frames {
			m.pushFrame(mode.FrameEvent{Kind: mode.FrameVoice, Call: id,
				Frame: network.VoiceFrame{Call: id, Seq: f.Seq, Codec: f.Codec, Data: f.Data}})
			return
		}
		select {
		case m.dec <- job{call: id, seq: f.Seq, codec: f.Codec, data: f.Data}:
		default:
		}
	case ev.end:
		m.mu.Lock()
		ok := m.rxOn && m.rxCall == ev.call
		m.mu.Unlock()
		if ok {
			m.cutRX()
		}
	}
}

func (m *Mode) onLogin(ctx context.Context, st ctl.LoginState, detail string) {
	m.mu.Lock()
	n, tgt, nt, pending := m.n, m.target, m.nt, m.pending
	m.mu.Unlock()
	switch st {
	case ctl.LoginOK:
		if m.d.login {
			m.mu.Lock()
			first := !m.loggedIn
			m.loggedIn, m.pending = true, false
			m.mu.Unlock()
			if first {
				m.env.Logger().Info(m.d.name + ": logged in")
			}
			if tgt != nil && pending {
				_ = m.tune(ctx, n, *tgt, nt)
			} else if tgt == nil {
				m.SetState(mode.StateReady, detail, "")
			}
			return
		}
		if tgt != nil {
			m.SetState(mode.StateConnected, describe(*tgt), "")
		}
	case ctl.LoginPending:
		if m.d.login {
			m.mu.Lock()
			m.loggedIn = false
			if m.target != nil {
				m.pending = true
				m.loginAt = m.Now()
			}
			m.mu.Unlock()
			m.SetState(mode.StateStarting, "logging in", "")
		} else if tgt != nil {
			m.SetState(mode.StateConnecting, "linking to "+describe(*tgt), "")
		}
	case ctl.LoginFailed:
		msg := "login refused"
		if !m.d.login {
			msg = "link failed"
		}
		if detail != "" {
			msg += ": " + detail
		}
		if m.d.login {
			m.mu.Lock()
			m.loggedIn = false
			m.mu.Unlock()
		}
		m.env.Logger().Warn(m.d.name + ": " + msg)
		m.SetState(mode.StateError, "", msg)
	case ctl.LoginOff, ctl.LoginIdle:
		if !m.d.login && tgt == nil {
			m.SetState(mode.StateReady, "", "")
		}
	}
}

// cutRX ends the received call now.
func (m *Mode) cutRX() {
	m.mu.Lock()
	was := m.rxOn
	m.rxOn = false
	frames, id := m.frames, m.rxID
	m.mu.Unlock()
	if was {
		if frames {
			m.pushFrame(mode.FrameEvent{Kind: mode.FrameEnd, Call: id})
		}
		m.TalkEnd()
	}
}

func (m *Mode) housekeeping() {
	now := m.Now()
	m.mu.Lock()
	rxStale := m.rxOn && now.Sub(m.rxLast) > m.opt.RxSilence
	txStale := m.tx != 0 && now.Sub(m.txLast) > m.opt.TxTimeout
	loginLate := m.pending && m.target != nil && !m.loginAt.IsZero() && now.Sub(m.loginAt) > m.opt.LoginWait
	if loginLate {
		m.loginAt = time.Time{}
	}
	m.mu.Unlock()
	if rxStale {
		m.cutRX()
	}
	if txStale {
		m.EndTx()
	}
	if loginLate {
		msg := "not logged in yet"
		if d := m.netDetail(); d != "" {
			msg += ": " + d
		}
		m.SetState(mode.StateConnecting, "waiting for login", msg)
	}
}

func (m *Mode) netDetail() string {
	m.mu.Lock()
	n := m.n
	m.mu.Unlock()
	if n == nil {
		return ""
	}
	return n.Status().Detail
}

// Talking reports whether a network call is being received.
func (m *Mode) Talking() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rxOn
}

// Status adds the network's own detail when the mode has none.
func (m *Mode) Status() mode.Status {
	st := m.Base.Status()
	if st.Detail == "" {
		st.Detail = m.netDetail()
	}
	return st
}

// ---- coded-frame passthrough (mode.FrameMode) ----

// Errors of the frame methods.
var (
	ErrNotStarted = errors.New("dv: mode is not started")
	ErrNotLinked  = errors.New("dv: no link to transmit on")
	ErrNoTX       = errors.New("dv: no transmission")
	ErrCodec      = errors.New("dv: frame codec does not match the network")
	ErrBusy       = errors.New("dv: transmit queue full")
)

// FrameCodec is the network's codec.
func (m *Mode) FrameCodec() (network.Codec, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.n == nil {
		return 0, false
	}
	return m.n.Codec(), true
}

// SetFrames switches received calls to RecvFrames (on) or to decoded audio.
func (m *Mode) SetFrames(on bool) {
	m.mu.Lock()
	m.frames = on
	m.mu.Unlock()
}

// RecvFrames carries received calls as coded frames while SetFrames(true).
func (m *Mode) RecvFrames() <-chan mode.FrameEvent { return m.frameC }

func (m *Mode) pushFrame(ev mode.FrameEvent) {
	select {
	case m.frameC <- ev:
	default:
	}
}

// SendHeader starts a transmission of coded frames. On a DMR network the
// talkgroup and private flag of h are used when h.TG is set (the radio
// user's choice); otherwise the mode's target supplies them. The source
// is h.SrcID and h.Src when set, else the node's own.
func (m *Mode) SendHeader(h network.CallHeader) error {
	m.mu.Lock()
	n := m.n
	switch {
	case n == nil:
		m.mu.Unlock()
		return ErrNotStarted
	case m.pending || (m.target == nil && (h.TG == 0 || !m.d.login)):
		m.mu.Unlock()
		return ErrNotLinked
	}
	prev := m.tx
	m.nextID++
	id := m.nextID
	m.tx, m.txSeq, m.txLast = id, 0, m.Now()
	hdr := network.CallHeader{Call: id, Src: m.id.Callsign, SrcID: m.id.DMRID,
		TG: m.nt.TG, Room: m.nt.Room, Slot: m.nt.Slot, Private: m.nt.Private}
	if h.Src != "" {
		hdr.Src = h.Src
	}
	if h.SrcID != 0 {
		hdr.SrcID = h.SrcID
	}
	if h.TG != 0 && m.d.login {
		hdr.TG, hdr.Private = h.TG, h.Private
	}
	m.mu.Unlock()
	m.cutRX()
	m.rfTalkStart(h, hdr)
	if prev != 0 {
		select {
		case m.enc <- job{call: prev, end: true}:
		default:
			go func() { _ = n.EndTX(prev) }()
		}
	}
	select {
	case m.enc <- job{call: id, start: &hdr, only: true}:
		return nil
	default:
		m.mu.Lock()
		failed := m.tx == id
		if failed {
			m.tx, m.rfTalk = 0, false
		}
		m.mu.Unlock()
		if failed {
			m.TalkEnd()
		}
		return ErrBusy
	}
}

// rfTalkStart shows the RF call's talker (Via "rf"): the DMR ID and
// talkgroup of the call, and the slot it came in on. The callsign is left
// for the lookup by ID, as for a network talker with no callsign. A call
// that follows another one ends it first, so each gets its own entry.
func (m *Mode) rfTalkStart(h, hdr network.CallHeader) {
	m.mu.Lock()
	again := m.rfTalk
	m.rfTalk = true
	m.mu.Unlock()
	if again {
		m.TalkEnd()
	}
	tgt := hdr.Room
	if tgt == "" && hdr.TG != 0 {
		tgt = strconv.FormatUint(uint64(hdr.TG), 10)
	}
	call := ""
	if h.Src != "" && h.Src != strconv.FormatUint(uint64(hdr.SrcID), 10) {
		call = h.Src
	}
	m.TalkStart(mode.Talker{Callsign: call, ID: hdr.SrcID, Target: tgt, Via: "rf", Slot: h.Slot, Since: m.Now()})
}

// SendFrame queues one coded frame of the transmission.
func (m *Mode) SendFrame(f network.VoiceFrame) error {
	m.mu.Lock()
	n := m.n
	if n == nil {
		m.mu.Unlock()
		return ErrNotStarted
	}
	if m.tx == 0 {
		m.mu.Unlock()
		return ErrNoTX
	}
	codec := n.Codec()
	if f.Codec != codec || len(f.Data) != codecLen(codec) {
		m.mu.Unlock()
		return ErrCodec
	}
	id, seq := m.tx, m.txSeq
	m.txSeq++
	m.txLast = m.Now()
	m.mu.Unlock()
	select {
	case m.enc <- job{call: id, seq: seq, codec: codec, data: append([]byte(nil), f.Data...), raw: true}:
		return nil
	default:
		return ErrBusy
	}
}

// EndFrames ends the transmission.
func (m *Mode) EndFrames() error {
	m.EndTx()
	return nil
}
