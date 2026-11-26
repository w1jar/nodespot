package echolink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/gsm"
)

// Connection errors.
var (
	// ErrBusy means a connection is already up; the node runs one EchoLink
	// connection at a time.
	ErrBusy = errors.New("echolink: already connected")
	// ErrClosed means the connection or directory has been closed.
	ErrClosed = errors.New("echolink: closed")
	// ErrConnectTimeout means the station never answered.
	ErrConnectTimeout = errors.New("echolink: station did not answer")
	// ErrTimeout means the station went silent on an established connection.
	ErrTimeout = errors.New("echolink: connection timed out")
	// ErrRemoteBye means the station ended the connection.
	ErrRemoteBye = errors.New("echolink: station disconnected")
)

// State is a connection's state.
type State int

// Connection states.
const (
	StateConnecting State = iota
	StateConnected
	StateClosed
)

// String returns the state's name.
func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	default:
		return "closed"
	}
}

// EventKind says what an Event reports.
type EventKind int

// Event kinds.
const (
	// EventConnected: the station answered.
	EventConnected EventKind = iota
	// EventDisconnected: the connection ended; Err holds the reason, nil
	// after a local Close. It is the last event before the channel closes.
	EventDisconnected
	// EventTalkStart: audio started arriving; Callsign is the talker.
	EventTalkStart
	// EventTalkStop: audio stopped arriving.
	EventTalkStop
	// EventInfo: station info text arrived in Text.
	EventInfo
	// EventChat: a chat line from Callsign arrived in Text.
	EventChat
	// EventPeer: the station's SDES identity (Callsign, Name) arrived or
	// changed.
	EventPeer
	// EventRefused: a station we did not call tried to connect and was
	// sent a BYE; Callsign is its SDES callsign if it sent one.
	EventRefused
)

// Event is something that happened on a connection, for display.
type Event struct {
	Kind     EventKind
	Time     time.Time
	Callsign string
	Name     string
	Text     string
	Err      error
}

// ConnStatus is a snapshot of a connection for the UI.
type ConnStatus struct {
	State    State
	Station  Station // the station called, as listed in the directory
	PeerName string  // operator name from the station's SDES
	Talking  bool    // audio is arriving
	Talker   string  // callsign shown as talking
	Info     string  // last station info text
	Started  time.Time
	Since    time.Time // when the station answered; zero while connecting
	LastRx   time.Time
	Err      string // why the connection closed

	TxPackets uint64
	RxPackets uint64
	RxDropped uint64 // frames dropped because Recv was not drained
	RxBad     uint64 // undecodable or out-of-order packets
	Refused   uint64 // connection attempts from other stations
}

// Conn is one outgoing EchoLink connection.
type Conn struct {
	cfg     Config
	station Station
	audio   *net.UDPConn
	ctrl    *net.UDPConn
	rAudio  netip.AddrPort
	rCtrl   netip.AddrPort

	recv      chan Frame
	events    chan Event
	connected chan struct{}
	done      chan struct{} // closed when the connection stops
	finished  chan struct{} // closed when goroutines exit and channels close
	closeOnce sync.Once
	wg        sync.WaitGroup
	onClose   func(*Conn)

	dec *gsm.Decoder // used only by the audio reader

	sendMu  sync.Mutex
	enc     *gsm.Encoder
	pending []byte
	seq     uint16
	ts      uint32

	mu        sync.Mutex
	st        ConnStatus
	err       error
	chClosed  bool
	lastAudio time.Time
	haveSeq   bool
	lastSeq   uint16
	refusedAt map[netip.Addr]time.Time
}

// dial binds the local sockets, calls the station and waits for its answer.
func dial(ctx context.Context, cfg Config, st Station, onClose func(*Conn)) (*Conn, error) {
	if !st.Addr.IsValid() {
		return nil, fmt.Errorf("echolink: %s has no address", st.Callsign)
	}
	audio, err := listen(cfg.LocalAudioAddr)
	if err != nil {
		return nil, err
	}
	ctrl, err := listen(cfg.LocalControlAddr)
	if err != nil {
		audio.Close()
		return nil, err
	}
	now := cfg.Clock.Now()
	c := &Conn{
		cfg:       cfg,
		station:   st,
		audio:     audio,
		ctrl:      ctrl,
		rAudio:    netip.AddrPortFrom(st.Addr, uint16(cfg.RemoteAudioPort)),
		rCtrl:     netip.AddrPortFrom(st.Addr, uint16(cfg.RemoteControlPort)),
		recv:      make(chan Frame, defaultRecvBuffer),
		events:    make(chan Event, defaultEventBuffer),
		connected: make(chan struct{}),
		done:      make(chan struct{}),
		finished:  make(chan struct{}),
		onClose:   onClose,
		dec:       gsm.NewDecoder(),
		enc:       gsm.NewEncoder(),
		refusedAt: make(map[netip.Addr]time.Time),
	}
	c.st = ConnStatus{State: StateConnecting, Station: st, Talker: st.Callsign, Started: now}

	c.wg.Add(3)
	go c.readLoop(c.audio, c.handleAudio)
	go c.readLoop(c.ctrl, c.handleControl)
	go c.timerLoop(now)
	c.sendSDES()

	select {
	case <-c.connected:
		if c.cfg.Info != "" {
			c.write(c.audio, c.rAudio, buildInfo(c.cfg.Info))
		}
		return c, nil
	case <-c.done:
		<-c.finished
		return nil, fmt.Errorf("echolink: connect to %s: %w", st.Callsign, c.Err())
	case <-ctx.Done():
		c.closeWith(ctx.Err())
		<-c.finished
		return nil, fmt.Errorf("echolink: connect to %s: %w", st.Callsign, ctx.Err())
	}
}

func listen(addr string) (*net.UDPConn, error) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("echolink: local address %q: %w", addr, err)
	}
	c, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return nil, fmt.Errorf("echolink: bind %s: %w", addr, err)
	}
	return c, nil
}

// Station returns the station this connection called.
func (c *Conn) Station() Station { return c.station }

// Recv returns the channel of decoded audio frames from the station. Frames
// are dropped, not queued, when the channel is full. It is closed after the
// connection ends.
func (c *Conn) Recv() <-chan Frame { return c.recv }

// Events returns the channel of display events. Events are dropped when the
// channel is full. It is closed after the connection ends.
func (c *Conn) Events() <-chan Event { return c.events }

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended: nil while it is up or after a local
// Close.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Status returns a snapshot for the UI.
func (c *Conn) Status() ConnStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// Send encodes one frame of local audio. Every FramesPerPacket frames are sent
// as one RTP packet. Send is safe to call from one goroutine at a time.
func (c *Conn) Send(f Frame) error {
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	pcm := [gsm.FrameSamples]int16(f)
	b := c.enc.Encode(&pcm)
	c.pending = append(c.pending, b[:]...)
	if len(c.pending) < audioPayload {
		return nil
	}
	return c.sendPendingLocked()
}

// Flush pads a partly filled packet with silence and sends it, so the end of
// a transmission is not held back. It does nothing if no frames are waiting.
func (c *Conn) Flush() error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if len(c.pending) == 0 {
		return nil
	}
	var silence [gsm.FrameSamples]int16
	for len(c.pending) < audioPayload {
		b := c.enc.Encode(&silence)
		c.pending = append(c.pending, b[:]...)
	}
	return c.sendPendingLocked()
}

func (c *Conn) sendPendingLocked() error {
	p := buildAudio(c.seq, c.ts, c.pending)
	c.pending = c.pending[:0]
	c.seq++
	c.ts += FramesPerPacket * FrameSamples
	if err := c.write(c.audio, c.rAudio, p); err != nil {
		return err
	}
	c.mu.Lock()
	c.st.TxPackets++
	c.mu.Unlock()
	return nil
}

// SendChat sends a one-line chat message to the station.
func (c *Conn) SendChat(msg string) error {
	return c.write(c.audio, c.rAudio, buildChat(c.cfg.Callsign, msg))
}

// Close ends the connection with a BYE and waits for its goroutines.
func (c *Conn) Close() error {
	c.closeWith(nil)
	<-c.finished
	return nil
}

func (c *Conn) write(sock *net.UDPConn, to netip.AddrPort, p []byte) error {
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	if _, err := sock.WriteToUDPAddrPort(p, to); err != nil {
		select {
		case <-c.done:
			return ErrClosed
		default:
		}
		return fmt.Errorf("echolink: send to %s: %w", to, err)
	}
	return nil
}

func (c *Conn) sendSDES() {
	_ = c.write(c.ctrl, c.rCtrl, buildSDES(c.cfg.Callsign, c.cfg.Name, c.cfg.Location))
}

// emitLocked queues an event without blocking. c.mu must be held.
func (c *Conn) emitLocked(ev Event) {
	if c.chClosed {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = c.cfg.Clock.Now()
	}
	select {
	case c.events <- ev:
	default:
	}
}

// closeWith ends the connection once, recording reason.
func (c *Conn) closeWith(reason error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = reason
		c.st.State = StateClosed
		if reason != nil {
			c.st.Err = reason.Error()
		}
		if c.st.Talking {
			c.st.Talking = false
			c.emitLocked(Event{Kind: EventTalkStop, Callsign: c.st.Talker})
		}
		c.emitLocked(Event{Kind: EventDisconnected, Err: reason})
		c.mu.Unlock()

		if !errors.Is(reason, ErrRemoteBye) {
			_, _ = c.ctrl.WriteToUDPAddrPort(buildBYE(), c.rCtrl)
		}
		close(c.done)
		c.audio.Close()
		c.ctrl.Close()
		go func() {
			c.wg.Wait()
			c.mu.Lock()
			c.chClosed = true
			close(c.recv)
			close(c.events)
			c.mu.Unlock()
			if c.onClose != nil {
				c.onClose(c)
			}
			close(c.finished)
		}()
	})
}

func (c *Conn) readLoop(sock *net.UDPConn, handle func([]byte, netip.AddrPort)) {
	defer c.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, from, err := sock.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-c.done:
			default:
				c.closeWith(fmt.Errorf("echolink: receive: %w", err))
			}
			return
		}
		handle(buf[:n], netip.AddrPortFrom(from.Addr().Unmap(), from.Port()))
	}
}

// fromPeerLocked records traffic from the station and completes the
// connection on its first packet. c.mu must be held.
func (c *Conn) fromPeerLocked(now time.Time) {
	c.st.LastRx = now
	if c.st.State == StateConnecting {
		c.st.State = StateConnected
		c.st.Since = now
		close(c.connected)
		c.emitLocked(Event{Kind: EventConnected, Time: now, Callsign: c.station.Callsign})
	}
}

func (c *Conn) handleAudio(p []byte, from netip.AddrPort) {
	if from.Addr() != c.station.Addr {
		return // audio from a station we did not call
	}
	now := c.cfg.Clock.Now()
	if isText(p) {
		t := parseText(p)
		c.mu.Lock()
		c.fromPeerLocked(now)
		if t.info {
			c.st.Info = t.text
			c.emitLocked(Event{Kind: EventInfo, Time: now, Callsign: c.st.Talker, Text: t.text})
		} else {
			c.emitLocked(Event{Kind: EventChat, Time: now, Callsign: t.callsign, Text: t.text})
		}
		c.mu.Unlock()
		return
	}
	seq, payload, err := parseAudio(p)
	c.mu.Lock()
	c.fromPeerLocked(now)
	if err != nil {
		c.st.RxBad++
		c.mu.Unlock()
		return
	}
	// Drop duplicates and late packets; a large jump back is taken as the
	// sender restarting its sequence.
	if c.haveSeq {
		if d := int16(seq - c.lastSeq); d <= 0 && d > -1000 {
			c.st.RxBad++
			c.mu.Unlock()
			return
		}
	}
	c.haveSeq = true
	c.lastSeq = seq
	c.st.RxPackets++
	c.lastAudio = now
	if !c.st.Talking {
		c.st.Talking = true
		c.emitLocked(Event{Kind: EventTalkStart, Time: now, Callsign: c.st.Talker})
	}
	c.mu.Unlock()

	frames := make([]Frame, 0, len(payload)/gsm.FrameBytes)
	var pcm [gsm.FrameSamples]int16
	for off := 0; off+gsm.FrameBytes <= len(payload); off += gsm.FrameBytes {
		if err := c.dec.Decode(payload[off:off+gsm.FrameBytes], &pcm); err != nil {
			continue
		}
		frames = append(frames, Frame(pcm))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if bad := len(payload)/gsm.FrameBytes - len(frames); bad > 0 {
		c.st.RxBad += uint64(bad)
	}
	for _, f := range frames {
		if c.chClosed {
			return
		}
		select {
		case c.recv <- f:
		default:
			c.st.RxDropped++
		}
	}
}

func (c *Conn) handleControl(p []byte, from netip.AddrPort) {
	info, err := parseRTCP(p)
	if err != nil && !info.sdes && !info.bye {
		return
	}
	now := c.cfg.Clock.Now()
	if from.Addr() != c.station.Addr {
		c.refuse(info, from, now)
		return
	}
	if info.bye {
		c.closeWith(ErrRemoteBye)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fromPeerLocked(now)
	if !info.sdes {
		return
	}
	call, name := info.callsign(), info.name()
	if call == "" {
		return
	}
	// The talker follows the SDES callsign. For a direct call this is the
	// station itself; a conference may announce other names. That a
	// conference does so is inferred, not documented.
	if call != c.st.Talker || name != c.st.PeerName {
		c.st.Talker = call
		c.st.PeerName = name
		c.emitLocked(Event{Kind: EventPeer, Time: now, Callsign: call, Name: name})
	}
}

// refuse answers a connection attempt from a station we did not call with a
// BYE to the port it came from, at most once per address every
// refuseInterval. Only SDES is answered, and the BYE is smaller than it, so
// this cannot amplify traffic.
func (c *Conn) refuse(info rtcpInfo, src netip.AddrPort, now time.Time) {
	from := src.Addr()
	if !info.sdes || info.bye {
		return
	}
	c.mu.Lock()
	c.st.Refused++
	last, seen := c.refusedAt[from]
	send := !seen || now.Sub(last) >= refuseInterval
	if send {
		if len(c.refusedAt) >= 64 {
			c.refusedAt = make(map[netip.Addr]time.Time)
		}
		c.refusedAt[from] = now
	}
	c.emitLocked(Event{Kind: EventRefused, Time: now, Callsign: info.callsign()})
	c.mu.Unlock()
	if send {
		_ = c.write(c.ctrl, src, buildBYE())
	}
}

const refuseInterval = 10 * time.Second

func (c *Conn) timerLoop(start time.Time) {
	defer c.wg.Done()
	nextKeepalive := start.Add(c.cfg.Keepalive)
	for {
		select {
		case <-c.done:
			return
		case <-c.cfg.Clock.After(c.cfg.tick):
		}
		now := c.cfg.Clock.Now()
		c.mu.Lock()
		var reason error
		switch c.st.State {
		case StateConnecting:
			if now.Sub(start) >= c.cfg.ConnectTimeout {
				reason = ErrConnectTimeout
			}
		case StateConnected:
			if now.Sub(c.st.LastRx) >= c.cfg.IdleTimeout {
				reason = ErrTimeout
			}
		}
		if reason == nil && c.st.Talking && now.Sub(c.lastAudio) >= c.cfg.TalkHang {
			c.st.Talking = false
			c.emitLocked(Event{Kind: EventTalkStop, Time: now, Callsign: c.st.Talker})
		}
		c.mu.Unlock()
		if reason != nil {
			c.closeWith(reason)
			return
		}
		if !now.Before(nextKeepalive) {
			c.sendSDES()
			nextKeepalive = now.Add(c.cfg.Keepalive)
		}
	}
}
