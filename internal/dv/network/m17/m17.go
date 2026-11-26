// SPDX-License-Identifier: GPL-3.0-or-later

// Package m17 is the M17 network (ctl.ModeM17): a client of an M17 reflector
// (mrefd, urfd style) over UDP, port 17000 by default. Packet layouts are in
// package internal/dv/m17; the reflector side was cross-checked against
// mrefd (see that package's documentation).
//
// # Link state machine
//
//	idle        nothing tuned; login off
//	connecting  CONN(callsign, module) sent, waiting for ACKN; login pending.
//	            Resent with doubling backoff (ack_timeout_ms .. 15x). After
//	            the first unanswered CONN the detail is "no reply from ...,
//	            retrying" (ctl.NotAnswering)
//	linked      ACKN received (or a PING, which a reflector only sends to a
//	            linked client); login ok
//	refused     NACK received: login failed "refused by <name> (NACK)";
//	            nothing is sent again until the next Tune
//	kicked      server-initiated 10-byte DISC: login failed "disconnected by
//	            <name>"; no reconnect until the next Tune
//
// linked falls back to connecting (with a fresh CONN) when neither a PING,
// a PONG, nor stream data has come from the reflector for timeout_ms (30 s).
// Every PING is answered with a PONG carrying the node callsign. Tune (a new
// module or reflector) sends DISC to the old link, waits briefly for its
// 4-byte acknowledgement, then starts connecting. Unlink and Run's shutdown
// do the same and end in idle.
//
// # RX
//
// "M17 " stream packets from the linked reflector are parsed with
// m17.UnmarshalStream. A CRC mismatch is counted (CRCErrors) and the packet
// is still used, because many senders write a zero CRC. Packets are grouped
// by StreamID; each carries two 8-byte Codec2 3200 frames, passed to the
// sink as network.CodecC2 frames. A call starts at the first packet of a
// stream (CallHeader.Src is the SRC callsign, Room the DST) and ends at the
// last-frame bit of FN or after rx_timeout_ms without packets. A second
// stream while one is active is ignored until the first has been silent for
// 200 ms. Encrypted streams and non-3200 voice types carry nothing that can
// be played and are ignored.
//
// # TX
//
// Frames from the router are paired into 54-byte packets with a random
// non-zero StreamID per call, FN counting from 0, TYPE = m17.VoiceType(can),
// META zero and the last-frame bit on the final packet. An odd final frame is
// padded with the Codec2 silence frame. SRC is the node callsign (plus the
// optional suffix). DST is "M17-XXX C" (reflector designator, space, module),
// the form M17_inet describes and legacy reflectors need to forward the
// stream; mrefd rewrites any DST beginning "M17-" to @ALL and routes by the
// linked module. If the reflector designator is not known (no Room in the
// tune, no "reflector" option) DST is the broadcast address.
//
// # Target and options
//
// Tune links to Target.Host:Target.Port (default NetConfig.Address:Port,
// then port 17000) and the module from Target.Room "M17-XXX C"; without a
// Room the module comes from the "module" option. Nothing is linked until
// the first Tune.
//
//	module         default module A-Z when the tune has no Room
//	reflector      designator M17-XXX for DST when the tune has no Room
//	can            channel access number 0..15, default 0
//	suffix         appended to the node callsign ("N0CALL B")
//	ack_timeout_ms first CONN retry delay, default 2000
//	timeout_ms     silence that means a dead reflector, default 30000
//	rx_timeout_ms  end a received call after this much silence, default 1500
//	disc_wait_ms   wait for the DISC acknowledgement, default 500
//	tick_ms        housekeeping interval, default 100 (tests)
//
// Identity.Callsign is the node callsign (required).
package m17

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	proto "github.com/w1jar/nodespot/internal/dv/m17"
	"github.com/w1jar/nodespot/internal/dv/network"
)

const (
	DefaultPort       = 17000
	DefaultAckTimeout = 2 * time.Second
	DefaultTimeout    = 30 * time.Second
	DefaultRxTimeout  = 1500 * time.Millisecond
	DefaultDiscWait   = 500 * time.Millisecond
	DefaultTick       = 100 * time.Millisecond
	maxBackoffAcks    = 15
	streamSwitchIdle  = 200 * time.Millisecond

	OptModule       = "module"
	OptReflector    = "reflector"
	OptCAN          = "can"
	OptSuffix       = "suffix"
	OptAckTimeoutMS = "ack_timeout_ms"
	OptTimeoutMS    = "timeout_ms"
	OptRxTimeoutMS  = "rx_timeout_ms"
	OptDiscWaitMS   = "disc_wait_ms"
	OptTickMS       = "tick_ms"
)

// Silence is the Codec2 3200 silence frame used to pad an odd last frame.
var Silence = [proto.CodecFrame]byte{0x01, 0x00, 0x09, 0x43, 0x9c, 0xe4, 0x21, 0x08}

func init() {
	network.Register(ctl.ModeM17, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
}

func optMS(cfg ctl.NetConfig, key string, def time.Duration) time.Duration {
	if v, err := strconv.Atoi(cfg.Options[key]); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return def
}

type state int

const (
	stIdle state = iota
	stConnecting
	stLinked
	stRefused
	stKicked
)

type link struct {
	host      string
	port      int
	name      string // shown in status
	reflector string // "M17-XXX" or ""
	module    byte
}

type rxCall struct {
	id       uint64
	stream   uint16
	seq      uint32
	last     time.Time
	lastFN   uint16
	haveFN   bool
	endedFor bool
}

type txCall struct {
	id      uint64
	stream  uint16
	fn      uint16
	buf     [][proto.CodecFrame]byte // 0 or 1 frame waiting for its pair
	pending *proto.Stream            // full packet held back until we know whether it is the last
}

// Net is the M17 network.
type Net struct {
	enabled  bool
	cfg      ctl.NetConfig
	self     string // node callsign with suffix
	addrSelf proto.Address
	can      uint8
	ackTO    time.Duration
	timeout  time.Duration
	rxTO     time.Duration
	discTO   time.Duration
	tick     time.Duration

	crcErrors atomic.Uint64

	mu      sync.Mutex
	sink    network.Sink
	conn    *net.UDPConn
	tgt     *link
	addr    *net.UDPAddr
	gen     uint64
	st      state
	login   ctl.LoginState
	detail  string
	heard   time.Time // last PING/PONG/ACKN/stream from the reflector
	next    time.Time // next CONN or resolve attempt
	backoff time.Duration
	tries   int
	rx      *rxCall
	tx      *txCall
	nextID  uint64
	discAck chan struct{}
	pend    []func()
}

// New builds the network.
func New(cfg ctl.NetConfig, id ctl.Identity) (*Net, error) {
	self, err := proto.NodeCallsign(id.Callsign, cfg.Options[OptSuffix])
	if err != nil {
		return nil, fmt.Errorf("m17: node callsign: %w", err)
	}
	a, _ := proto.EncodeCallsign(self)
	var can uint8
	if v := cfg.Options[OptCAN]; v != "" {
		c, err := strconv.Atoi(v)
		if err != nil || c < 0 || c > 15 {
			return nil, fmt.Errorf("m17: can %q out of range 0..15", v)
		}
		can = uint8(c)
	}
	if _, err := proto.VoiceType(can); err != nil {
		return nil, err
	}
	return &Net{
		enabled:  cfg.Enabled,
		cfg:      cfg,
		self:     self,
		addrSelf: a,
		can:      can,
		ackTO:    optMS(cfg, OptAckTimeoutMS, DefaultAckTimeout),
		timeout:  optMS(cfg, OptTimeoutMS, DefaultTimeout),
		rxTO:     optMS(cfg, OptRxTimeoutMS, DefaultRxTimeout),
		discTO:   optMS(cfg, OptDiscWaitMS, DefaultDiscWait),
		tick:     optMS(cfg, OptTickMS, DefaultTick),
		login:    ctl.LoginOff,
	}, nil
}

func (n *Net) Mode() string         { return ctl.ModeM17 }
func (n *Net) Codec() network.Codec { return network.CodecC2 }

// CRCErrors is the number of received stream packets whose CRC did not match.
func (n *Net) CRCErrors() uint64 { return n.crcErrors.Load() }

// Status reports the link.
func (n *Net) Status() ctl.NetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ctl.NetStatus{Mode: ctl.ModeM17, Available: true, Enabled: n.enabled, Login: n.login, Detail: n.detail}
}

// unlock releases mu and then makes the sink calls queued under it.
func (n *Net) unlock() {
	p := n.pend
	n.pend = nil
	n.mu.Unlock()
	for _, f := range p {
		f()
	}
}

func (n *Net) setLogin(st ctl.LoginState, d string) {
	if st == n.login && d == n.detail {
		return
	}
	n.login, n.detail = st, d
	if s := n.sink; s != nil {
		n.pend = append(n.pend, func() { s.Login(st, d) })
	}
}

func (n *Net) send(c proto.Control) {
	b, err := c.Marshal()
	if err != nil {
		return
	}
	n.sendRaw(b)
}

func (n *Net) sendRaw(b []byte) {
	if n.conn != nil && n.addr != nil {
		n.conn.WriteToUDP(b, n.addr) //nolint:errcheck // UDP; loss is handled by retry and the watchdog
	}
}

// Run opens the socket and keeps the link until ctx ends.
func (n *Net) Run(ctx context.Context, s network.Sink) error {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return fmt.Errorf("m17: %w", err)
	}
	n.mu.Lock()
	n.sink, n.conn = s, conn
	if n.tgt == nil {
		n.setLogin(ctl.LoginOff, "")
	}
	n.next = time.Time{}
	n.unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			l, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			n.onPacket(from, buf[:l], time.Now())
		}
	}()

	t := time.NewTicker(n.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			n.discAndWait(context.Background())
			n.mu.Lock()
			n.dropLink()
			n.conn = nil
			n.setLogin(ctl.LoginOff, "")
			n.unlock()
			conn.Close()
			<-done
			return nil
		case now := <-t.C:
			n.housekeeping(ctx, now)
		}
	}
}

// resolveTarget gives the link for t with the fallbacks of the package doc.
func (n *Net) resolveTarget(t ctl.Target) (link, error) {
	var l link
	if t.Room != "" {
		r, m, err := proto.ParseTarget(t.Room)
		if err != nil {
			return l, err
		}
		l.reflector, l.module = r, m
	} else {
		mod := n.cfg.Options[OptModule]
		if len(mod) != 1 || !proto.ValidModule(mod[0]) {
			return l, errors.New("m17: target has no module (Room \"M17-XXX C\" or the module option)")
		}
		l.module = mod[0]
		if r := n.cfg.Options[OptReflector]; r != "" {
			if _, _, err := proto.ParseTarget(r + " " + mod); err != nil {
				return l, err
			}
			l.reflector = r
		}
	}
	l.host, l.port = t.Host, t.Port
	if l.host == "" {
		l.host = n.cfg.Address
	}
	if l.port <= 0 {
		l.port = n.cfg.Port
	}
	if l.port <= 0 {
		l.port = DefaultPort
	}
	if l.host == "" {
		return l, errors.New("m17: target has no host")
	}
	if l.port > 65535 {
		return l, errors.New("m17: bad port")
	}
	switch {
	case t.Name != "":
		l.name = t.Name
	case l.reflector != "":
		l.name = l.reflector + " " + string(l.module)
	default:
		l.name = net.JoinHostPort(l.host, strconv.Itoa(l.port)) + " " + string(l.module)
	}
	return l, nil
}

func resolve(ctx context.Context, host string, port int) (*net.UDPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return &net.UDPAddr{IP: ip, Port: port}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			return &net.UDPAddr{IP: ip.IP, Port: port}, nil
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	return &net.UDPAddr{IP: ips[0].IP, Port: port}, nil
}

// dropLink (mu held) forgets the link without telling the reflector.
func (n *Net) dropLink() {
	n.endRX()
	n.tx = nil
	n.tgt, n.addr, n.st = nil, nil, stIdle
	n.gen++
}

// discAndWait sends DISC when a link is up or being set up and waits up to
// disc_wait_ms for the reflector's 4-byte acknowledgement. It does not change
// the state.
func (n *Net) discAndWait(ctx context.Context) {
	n.mu.Lock()
	if n.addr == nil || n.conn == nil || (n.st != stLinked && n.st != stConnecting) {
		n.mu.Unlock()
		return
	}
	ch := make(chan struct{}, 1)
	n.discAck = ch
	n.send(proto.Disc(n.addrSelf))
	n.mu.Unlock()
	t := time.NewTimer(n.discTO)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
	n.mu.Lock()
	n.discAck = nil
	n.mu.Unlock()
}

// Tune links to t, unlinking from the current reflector or module first.
func (n *Net) Tune(ctx context.Context, t ctl.Target) error {
	l, err := n.resolveTarget(t)
	if err != nil {
		return err
	}
	n.discAndWait(ctx)
	now := time.Now()
	n.mu.Lock()
	n.dropLink()
	n.tgt = &l
	n.st = stConnecting
	n.backoff, n.tries, n.next = n.ackTO, 0, now
	n.heard = now
	n.setLogin(ctl.LoginPending, "linking to "+l.name)
	n.unlock()
	n.housekeeping(ctx, now)
	return nil
}

// Unlink sends DISC, waits briefly for the acknowledgement and drops the link.
func (n *Net) Unlink(ctx context.Context) error {
	n.discAndWait(ctx)
	n.mu.Lock()
	n.dropLink()
	n.setLogin(ctl.LoginOff, "")
	n.unlock()
	return nil
}

func (n *Net) housekeeping(ctx context.Context, now time.Time) {
	n.mu.Lock()
	if n.rx != nil && now.Sub(n.rx.last) > n.rxTO {
		n.endRX()
	}
	if n.tgt == nil || (n.st != stConnecting && n.st != stLinked) {
		n.unlock()
		return
	}
	if n.st == stLinked {
		if now.Sub(n.heard) <= n.timeout {
			n.unlock()
			return
		}
		// watchdog: the reflector is gone, link again
		n.st = stConnecting
		n.endRX()
		n.tx = nil
		n.tries = 1 // already known silent
		n.backoff, n.next = n.ackTO, now
	}
	if now.Before(n.next) {
		n.unlock()
		return
	}
	if n.addr == nil {
		l, gen := *n.tgt, n.gen
		n.unlock()
		a, err := resolve(ctx, l.host, l.port)
		n.mu.Lock()
		if gen != n.gen {
			n.unlock()
			return
		}
		if err != nil {
			n.setLogin(ctl.LoginFailed, "cannot resolve "+l.host)
			n.retryLater(now)
			n.unlock()
			return
		}
		n.addr = a
	}
	n.send(proto.Conn(n.addrSelf, n.tgt.module))
	if n.tries > 0 {
		n.setLogin(ctl.LoginPending, ctl.DetailNoReply+n.tgt.name+", retrying")
	} else {
		n.setLogin(ctl.LoginPending, "linking to "+n.tgt.name)
	}
	n.tries++
	n.retryLater(now)
	n.unlock()
}

// retryLater (mu held) schedules the next CONN with exponential backoff.
func (n *Net) retryLater(now time.Time) {
	n.next = now.Add(n.backoff)
	n.backoff *= 2
	if m := n.ackTO * maxBackoffAcks; n.backoff > m {
		n.backoff = m
	}
}

func sameAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}

func (n *Net) onPacket(from *net.UDPAddr, p []byte, now time.Time) {
	n.mu.Lock()
	defer n.unlock()
	if !sameAddr(from, n.addr) || n.tgt == nil {
		return
	}
	if proto.IsStream(p) {
		if n.st == stLinked {
			n.heard = now
			n.onStream(p, now)
		}
		return
	}
	c, err := proto.UnmarshalControl(p)
	if err != nil {
		return
	}
	switch c.Kind {
	case proto.KindACKN:
		if n.st == stConnecting {
			n.setLinked(now)
		}
	case proto.KindNACK:
		if n.st == stConnecting {
			n.st = stRefused
			n.setLogin(ctl.LoginFailed, "refused by "+n.tgt.name+" (NACK)")
		}
	case proto.KindPING:
		// a reflector pings only clients it has linked: a lost ACKN does not matter
		if n.st == stConnecting {
			n.setLinked(now)
		}
		if n.st == stLinked {
			n.heard = now
		}
		n.send(proto.Pong(n.addrSelf))
	case proto.KindPONG:
		if n.st == stLinked {
			n.heard = now
		}
	case proto.KindDISC:
		if c.Short {
			if n.discAck != nil {
				select {
				case n.discAck <- struct{}{}:
				default:
				}
			}
			return
		}
		if n.st == stLinked || n.st == stConnecting {
			n.endRX()
			n.tx = nil
			n.st = stKicked
			n.setLogin(ctl.LoginFailed, "disconnected by "+n.tgt.name)
		}
	}
}

func (n *Net) setLinked(now time.Time) {
	n.st = stLinked
	n.heard = now
	n.tries = 0
	n.backoff = n.ackTO
	n.setLogin(ctl.LoginOK, "linked to "+n.tgt.name)
}

// ---- RX ----

func (n *Net) startRX(s *proto.Stream, now time.Time) {
	n.endRX()
	n.nextID++
	n.rx = &rxCall{id: n.nextID, stream: s.StreamID, last: now}
	src, dst := s.LSF.Src.String(), s.LSF.Dst.String()
	if s.LSF.Dst == proto.AddrBroadcast {
		dst = "@ALL"
	}
	h := network.CallHeader{Call: n.rx.id, Src: src, Room: dst}
	if sk := n.sink; sk != nil {
		n.pend = append(n.pend, func() { sk.Header(h) })
	}
}

func (n *Net) endRX() {
	if n.rx == nil {
		return
	}
	id := n.rx.id
	n.rx = nil
	if s := n.sink; s != nil {
		n.pend = append(n.pend, func() { s.End(id) })
	}
}

func (n *Net) onStream(p []byte, now time.Time) {
	s, err := proto.UnmarshalStream(p)
	if err != nil {
		if !errors.Is(err, proto.ErrStreamCRC) {
			return
		}
		n.crcErrors.Add(1)
	}
	if !s.LSF.Type.IsVoice() || s.LSF.Type.Encrypted() || s.StreamID == 0 {
		return
	}
	if n.rx != nil && n.rx.stream != s.StreamID {
		if now.Sub(n.rx.last) < streamSwitchIdle {
			return
		}
		n.endRX()
	}
	if n.rx == nil {
		n.startRX(&s, now)
	}
	r := n.rx
	r.last = now
	fn := s.Counter()
	// drop a duplicate or a packet that arrived late
	if r.haveFN && int16(fn-r.lastFN) <= 0 {
		return
	}
	r.lastFN, r.haveFN = fn, true
	if sk := n.sink; sk != nil {
		for i := 0; i < 2; i++ {
			d := make([]byte, proto.CodecFrame)
			copy(d, s.Payload[i*proto.CodecFrame:])
			f := network.VoiceFrame{Call: r.id, Seq: r.seq, Codec: network.CodecC2, Data: d}
			n.pend = append(n.pend, func() { sk.Voice(f) })
			r.seq++
		}
	}
	if s.Last() {
		n.endRX()
	}
}

// ---- TX ----

func (n *Net) newStreamID() uint16 {
	var b [2]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return uint16(time.Now().UnixNano()) | 1
		}
		if id := binary.BigEndian.Uint16(b[:]); id != 0 {
			return id
		}
	}
}

func (n *Net) lsf() proto.LSF {
	var l proto.LSF
	l.Src = n.addrSelf
	l.Dst = proto.AddrBroadcast
	if t := n.tgt; t != nil && t.reflector != "" {
		if s, err := proto.FormatTarget(t.reflector, t.module); err == nil {
			if a, err := proto.EncodeCallsign(s); err == nil {
				l.Dst = a
			}
		}
	}
	l.Type, _ = proto.VoiceType(n.can)
	return l
}

func (n *Net) sendStream(s *proto.Stream) {
	b := s.Marshal()
	n.sendRaw(b[:])
}

// flushPending sends the held-back packet; with last it is the final one.
func (t *txCall) flush(n *Net, last bool) {
	if t.pending == nil {
		return
	}
	if last {
		t.pending.FN |= proto.LastFrame
	}
	n.sendStream(t.pending)
	t.pending = nil
}

// StartTX opens a transmission. The packets go out as frames arrive.
func (n *Net) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	defer n.unlock()
	if n.st != stLinked || n.addr == nil || n.conn == nil {
		return errors.New("m17: not linked")
	}
	n.tx = &txCall{id: h.Call, stream: n.newStreamID()}
	return nil
}

func (t *txCall) packet(n *Net, a, b []byte) *proto.Stream {
	s := &proto.Stream{StreamID: t.stream, LSF: n.lsf(), FN: t.fn}
	t.fn = proto.NextFN(t.fn)
	copy(s.Payload[:], a)
	copy(s.Payload[proto.CodecFrame:], b)
	return s
}

// WriteVoice queues one 8-byte Codec2 frame; every second one completes a
// packet. A completed packet is held until the next one is complete or the
// call ends, so that the last packet can carry the last-frame bit.
func (n *Net) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != proto.CodecFrame {
		return fmt.Errorf("m17: voice frame is %d bytes, want %d", len(f.Data), proto.CodecFrame)
	}
	n.mu.Lock()
	defer n.unlock()
	t := n.tx
	if t == nil || t.id != f.Call {
		return nil
	}
	var a [proto.CodecFrame]byte
	copy(a[:], f.Data)
	t.buf = append(t.buf, a)
	if len(t.buf) == 2 {
		t.flush(n, false)
		t.pending = t.packet(n, t.buf[0][:], t.buf[1][:])
		t.buf = t.buf[:0]
	}
	return nil
}

// EndTX sends the rest of the call with the last-frame bit.
func (n *Net) EndTX(call uint64) error {
	n.mu.Lock()
	defer n.unlock()
	t := n.tx
	if t == nil || t.id != call {
		return nil
	}
	n.tx = nil
	if len(t.buf) == 1 {
		t.flush(n, false)
		t.pending = t.packet(n, t.buf[0][:], Silence[:])
	} else if t.pending == nil {
		// no voice at all: a lone silent packet closes the stream
		t.pending = t.packet(n, Silence[:], Silence[:])
	}
	t.flush(n, true)
	return nil
}

func (n *Net) String() string { return strings.ToLower(ctl.ModeM17) + " " + n.self }
