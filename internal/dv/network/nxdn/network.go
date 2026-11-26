package nxdn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/dv/nxdn"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
)

// DefaultPort is the usual NXDN reflector port (NXDN_Hosts.txt).
const DefaultPort = 41400

// Option keys; see the package documentation.
const (
	OptID           = "nxdn_id"
	OptRAN          = "nxdn_ran"
	OptListen       = "listen"
	OptPollMS       = "poll_interval_ms"
	OptMissedPolls  = "missed_polls"
	OptBackoffMinMS = "backoff_min_ms"
	OptBackoffMaxMS = "backoff_max_ms"
	OptRxTimeoutMS  = "rx_timeout_ms"
)

const (
	burst      = 3 // polls/unlinks sent back to back, as NXDNGateway does
	pollLen    = 17
	dataLen    = 43
	callIDBase = 3 << 32
)

func init() {
	network.Register(ctl.ModeNXDN, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
}

type rxCall struct {
	open bool
	id   uint64
	src  uint16
	last time.Time
	seq  uint32
}

type txCall struct {
	open bool
	id   uint64
	l3   nxdn.Layer3
	pos  int
	buf  [][7]byte
}

// Net is one NXDN reflector client.
type Net struct {
	cfg      ctl.NetConfig
	callsign [10]byte
	id       uint16
	ran      int
	listen   string

	poll, timeout, bMin, bMax, rxTimeout time.Duration
	tick                                 time.Duration
	now                                  func() time.Time

	mu       sync.Mutex
	conn     *net.UDPConn
	sink     network.Sink
	running  bool
	linked   bool
	tg       uint16
	addr     netip.AddrPort
	login    ctl.LoginState
	detail   string
	linkAt   time.Time // start of the current wait for a reply
	lastRx   time.Time
	nextPoll time.Time
	backoff  time.Duration
	rx       rxCall
	tx       txCall
	nextCall uint64
}

// New builds the client from its config.
func New(cfg ctl.NetConfig, id ctl.Identity) (*Net, error) {
	n := &Net{cfg: cfg, listen: "0.0.0.0:0", now: time.Now, login: ctl.LoginOff, nextCall: callIDBase}
	call := strings.ToUpper(strings.TrimSpace(id.Callsign))
	if call == "" {
		return nil, errors.New("nxdn: identity callsign is required")
	}
	for i := range n.callsign {
		n.callsign[i] = ' '
	}
	copy(n.callsign[:], call)
	o := cfg.Options
	if v := strings.TrimSpace(o[OptID]); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 1 || x > 65535 {
			return nil, fmt.Errorf("nxdn: option %s must be 1..65535", OptID)
		}
		n.id = uint16(x)
	}
	if v := strings.TrimSpace(o[OptRAN]); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 0 || x > 63 {
			return nil, fmt.Errorf("nxdn: option %s must be 0..63", OptRAN)
		}
		n.ran = x
	}
	if v := strings.TrimSpace(o[OptListen]); v != "" {
		n.listen = v
	}
	ms := func(key string, def int) time.Duration {
		if x, err := strconv.Atoi(strings.TrimSpace(o[key])); err == nil && x > 0 {
			return time.Duration(x) * time.Millisecond
		}
		return time.Duration(def) * time.Millisecond
	}
	n.poll = ms(OptPollMS, 5000)
	n.bMin = ms(OptBackoffMinMS, 1000)
	n.bMax = ms(OptBackoffMaxMS, 30000)
	n.rxTimeout = ms(OptRxTimeoutMS, 1500)
	missed := 3
	if x, err := strconv.Atoi(strings.TrimSpace(o[OptMissedPolls])); err == nil && x > 0 {
		missed = x
	}
	n.timeout = time.Duration(missed) * n.poll
	n.tick = n.poll / 8
	if m := n.bMin / 2; m < n.tick {
		n.tick = m
	}
	if n.tick > 100*time.Millisecond {
		n.tick = 100 * time.Millisecond
	}
	if n.tick < time.Millisecond {
		n.tick = time.Millisecond
	}
	return n, nil
}

func (n *Net) Mode() string         { return ctl.ModeNXDN }
func (n *Net) Codec() network.Codec { return network.CodecAMBE }

// Status reports the link state. It never includes secrets (there are none).
func (n *Net) Status() ctl.NetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ctl.NetStatus{Mode: ctl.ModeNXDN, Available: true, Enabled: n.cfg.Enabled, Login: n.login, Detail: n.detail}
}

// events are sink calls collected under the lock and run after it is released.
type events []func()

func (e events) run() {
	for _, f := range e {
		f()
	}
}

func (n *Net) loginLocked(ev *events, st ctl.LoginState, detail string) {
	if n.login == st && n.detail == detail {
		return
	}
	n.login, n.detail = st, detail
	if s := n.sink; s != nil {
		*ev = append(*ev, func() { s.Login(st, detail) })
	}
}

// ---- datagrams ----

func (n *Net) ctlPacket(kind byte, tg uint16) []byte {
	b := make([]byte, pollLen)
	copy(b, "NXDN")
	b[4] = kind
	copy(b[5:15], n.callsign[:])
	b[15], b[16] = byte(tg>>8), byte(tg)
	return b
}

func (n *Net) writeLocked(b []byte, to netip.AddrPort) {
	if n.conn == nil || !to.IsValid() {
		return
	}
	n.conn.WriteToUDPAddrPort(b, to) // UDP: errors are retried by the next poll
}

func (n *Net) sendBurstLocked(kind byte, tg uint16, to netip.AddrPort) {
	p := n.ctlPacket(kind, tg)
	for i := 0; i < burst; i++ {
		n.writeLocked(p, to)
	}
}

func (n *Net) dataPacket(src, dst uint16, flags byte, payload [nxdn.PayloadLen]byte) []byte {
	b := make([]byte, dataLen)
	copy(b, "NXDND")
	b[5], b[6] = byte(src>>8), byte(src)
	b[7], b[8] = byte(dst>>8), byte(dst)
	b[9] = flags
	copy(b[10:], payload[:])
	return b
}

// ---- Run ----

// Run opens the socket and runs the poll and timeout machinery until ctx ends.
func (n *Net) Run(ctx context.Context, s network.Sink) error {
	la, err := net.ResolveUDPAddr("udp4", n.listen)
	if err != nil {
		return fmt.Errorf("nxdn: listen address: %w", err)
	}
	conn, err := net.ListenUDP("udp4", la)
	if err != nil {
		return fmt.Errorf("nxdn: %w", err)
	}
	n.mu.Lock()
	n.conn, n.sink, n.running = conn, s, true
	if n.linked {
		now := n.now()
		n.startLinkLocked(now)
	}
	n.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 200)
		for {
			l, from, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			n.handle(buf[:l], from)
		}
	}()

	t := time.NewTicker(n.tick)
	defer t.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-t.C:
			n.onTick()
		}
	}
	var ev events
	n.mu.Lock()
	if n.linked {
		n.sendBurstLocked('U', n.tg, n.addr)
	}
	n.endRxLocked(&ev)
	n.tx = txCall{}
	n.running = false
	n.conn = nil
	n.mu.Unlock()
	conn.Close()
	wg.Wait()
	ev.run()
	return nil
}

// startLinkLocked sends the poll burst and arms the retry timers.
func (n *Net) startLinkLocked(now time.Time) {
	n.sendBurstLocked('P', n.tg, n.addr)
	n.linkAt = now
	n.backoff = n.bMin
	n.nextPoll = now.Add(n.backoff)
	n.backoff = min(n.backoff*2, n.bMax)
}

func (n *Net) onTick() {
	var ev events
	n.mu.Lock()
	now := n.now()
	if n.linked {
		switch {
		case n.login == ctl.LoginOK && now.Sub(n.lastRx) > n.timeout:
			n.loginLocked(&ev, ctl.LoginPending, ctl.DetailNoReply+"reflector, retrying")
			n.linkAt = now
			n.backoff = n.bMin
			n.nextPoll = now
		case n.login == ctl.LoginPending && now.Sub(n.linkAt) > n.timeout:
			n.loginLocked(&ev, ctl.LoginFailed, ctl.DetailNoReply+"reflector, retrying")
		}
		if !now.Before(n.nextPoll) {
			n.writeLocked(n.ctlPacket('P', n.tg), n.addr)
			if n.login == ctl.LoginOK {
				n.nextPoll = now.Add(n.poll)
			} else {
				n.nextPoll = now.Add(n.backoff)
				n.backoff = min(n.backoff*2, n.bMax)
			}
		}
	}
	if n.rx.open && now.Sub(n.rx.last) > n.rxTimeout {
		n.endRxLocked(&ev)
	}
	n.mu.Unlock()
	ev.run()
}

// ---- receive ----

func (n *Net) endRxLocked(ev *events) {
	if !n.rx.open {
		return
	}
	id, s := n.rx.id, n.sink
	n.rx = rxCall{}
	if s != nil {
		*ev = append(*ev, func() { s.End(id) })
	}
}

func (n *Net) startRxLocked(ev *events, src uint16, group bool, now time.Time) {
	n.nextCall++
	n.rx = rxCall{open: true, id: n.nextCall, src: src, last: now}
	h := network.CallHeader{
		Call: n.rx.id, Src: strconv.Itoa(int(src)), SrcID: uint32(src),
		TG: uint32(n.tg), Room: strconv.Itoa(int(n.tg)), Private: !group,
	}
	if s := n.sink; s != nil {
		*ev = append(*ev, func() { s.Header(h) })
	}
}

func (n *Net) handle(p []byte, from netip.AddrPort) {
	var ev events
	n.mu.Lock()
	n.handleLocked(&ev, p, from)
	n.mu.Unlock()
	ev.run()
}

func (n *Net) handleLocked(ev *events, p []byte, from netip.AddrPort) {
	if !n.linked || from.Addr().Unmap() != n.addr.Addr() || from.Port() != n.addr.Port() {
		return
	}
	now := n.now()
	switch {
	case len(p) == pollLen && string(p[:5]) == "NXDNP":
		if uint16(p[15])<<8|uint16(p[16]) != n.tg {
			return
		}
		n.aliveLocked(ev, now)
	case len(p) == dataLen && string(p[:5]) == "NXDND":
		n.aliveLocked(ev, now)
		n.dataLocked(ev, p, now)
	}
}

func (n *Net) aliveLocked(ev *events, now time.Time) {
	n.lastRx = now
	if n.login != ctl.LoginOK {
		n.backoff = n.bMin
		n.nextPoll = now.Add(n.poll)
		n.loginLocked(ev, ctl.LoginOK, "TG "+strconv.Itoa(int(n.tg)))
	}
}

func (n *Net) dataLocked(ev *events, p []byte, now time.Time) {
	src := uint16(p[5])<<8 | uint16(p[6])
	dst := uint16(p[7])<<8 | uint16(p[8])
	flags := p[9]
	if dst != n.tg || flags&nxdn.FlagData != 0 {
		return
	}
	pr, err := nxdn.ParsePayload(p[10:])
	if err != nil {
		return
	}
	group := flags&nxdn.FlagGroup != 0
	switch pr.Kind {
	case nxdn.KindVCall:
		if n.rx.open && n.rx.src != src {
			n.endRxLocked(ev)
		}
		if !n.rx.open {
			n.startRxLocked(ev, src, group, now)
		}
		n.rx.last = now
	case nxdn.KindVoice:
		if n.rx.open && n.rx.src != src {
			n.endRxLocked(ev) // new talker without a TX_REL
		}
		if !n.rx.open {
			n.startRxLocked(ev, src, group, now) // late entry
		}
		n.rx.last = now
		n.framesLocked(ev, pr.Frames)
		if flags&nxdn.FlagEnd != 0 {
			n.endRxLocked(ev)
		}
	case nxdn.KindTxRel:
		if n.rx.open {
			n.framesLocked(ev, pr.Frames)
			n.endRxLocked(ev)
		}
	}
}

func (n *Net) framesLocked(ev *events, frames [][7]byte) {
	s := n.sink
	if s == nil {
		return
	}
	for _, f := range frames {
		vf := network.VoiceFrame{Call: n.rx.id, Seq: n.rx.seq, Data: append([]byte(nil), f[:]...)}
		n.rx.seq++
		*ev = append(*ev, func() { s.Voice(vf) })
	}
}

// ---- tune / unlink ----

func parseTG(t ctl.Target) (uint16, error) {
	v := int64(t.TG)
	if v == 0 {
		x, err := strconv.ParseInt(strings.TrimSpace(t.Room), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("nxdn: invalid talkgroup %q", t.Room)
		}
		v = x
	}
	if v < 1 || v > 65535 {
		return 0, fmt.Errorf("nxdn: talkgroup %d out of range", v)
	}
	return uint16(v), nil
}

func (n *Net) resolve(ctx context.Context, t ctl.Target) (netip.AddrPort, error) {
	host, port := t.Host, t.Port
	if host == "" {
		host = n.cfg.Address
	}
	if port == 0 {
		port = n.cfg.Port
	}
	if port == 0 {
		port = DefaultPort
	}
	if host == "" {
		return netip.AddrPort{}, errors.New("nxdn: no reflector address")
	}
	if port < 1 || port > 65535 {
		return netip.AddrPort{}, fmt.Errorf("nxdn: invalid port %d", port)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.Unmap().Is4() {
			return netip.AddrPort{}, errors.New("nxdn: only IPv4 reflectors are supported")
		}
		return netip.AddrPortFrom(a.Unmap(), uint16(port)), nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return netip.AddrPort{}, fmt.Errorf("nxdn: cannot resolve %s", host)
	}
	return netip.AddrPortFrom(ips[0].Unmap(), uint16(port)), nil
}

// Tune links to the reflector TG named by t.
func (n *Net) Tune(ctx context.Context, t ctl.Target) error {
	tg, err := parseTG(t)
	if err != nil {
		return err
	}
	addr, err := n.resolve(ctx, t)
	if err != nil {
		return err
	}
	var ev events
	n.mu.Lock()
	if n.linked && (n.tg != tg || n.addr != addr) {
		n.sendBurstLocked('U', n.tg, n.addr)
	}
	n.endRxLocked(&ev)
	n.tx = txCall{}
	n.linked, n.tg, n.addr = true, tg, addr
	n.lastRx = time.Time{}
	n.login, n.detail = "", "" // force the pending event
	n.loginLocked(&ev, ctl.LoginPending, "linking TG "+strconv.Itoa(int(tg)))
	if n.conn != nil {
		n.startLinkLocked(n.now())
	} // else Run sends the burst when the socket opens
	n.mu.Unlock()
	ev.run()
	return nil
}

// Unlink sends NXDNU and stops polling.
func (n *Net) Unlink(ctx context.Context) error {
	var ev events
	n.mu.Lock()
	if n.linked {
		n.sendBurstLocked('U', n.tg, n.addr)
	}
	n.linked = false
	n.endRxLocked(&ev)
	n.tx = txCall{}
	n.loginLocked(&ev, ctl.LoginOff, "")
	n.mu.Unlock()
	ev.run()
	return nil
}

// ---- transmit ----

// StartTX sends the call header (VCALL). It needs a link and nxdn_id.
func (n *Net) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case !n.linked:
		return errors.New("nxdn: not linked to a reflector")
	case n.id == 0:
		return fmt.Errorf("nxdn: option %s is required to transmit", OptID)
	case n.conn == nil:
		return errors.New("nxdn: network not running")
	}
	group := !h.Private
	n.tx = txCall{open: true, id: h.Call, l3: nxdn.NewVCall(n.id, n.tg, group)}
	n.writeLocked(n.dataPacket(n.id, n.tg, flagsFor(group, nxdn.FlagStart), nxdn.BuildHeader(n.tx.l3, n.ran)), n.addr)
	return nil
}

func flagsFor(group bool, extra byte) byte {
	if group {
		extra |= nxdn.FlagGroup
	}
	return extra
}

func (n *Net) sendVoiceLocked() {
	t := &n.tx
	var f [4][7]byte
	copy(f[:], t.buf)
	pl := nxdn.BuildVoice(t.l3, n.ran, t.pos, f)
	t.pos = (t.pos + 1) & 3
	t.buf = t.buf[:0]
	n.writeLocked(n.dataPacket(n.id, n.tg, flagsFor(t.l3.IsGroup(), 0), pl), n.addr)
}

// WriteVoice queues one AMBE+2 frame; every fourth frame goes out as one
// 80 ms voice packet.
func (n *Net) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != vocoder.AMBEBytes {
		return fmt.Errorf("nxdn: voice frame is %d bytes, want %d", len(f.Data), vocoder.AMBEBytes)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.tx.open || n.tx.id != f.Call {
		return nil
	}
	var a [7]byte
	copy(a[:], f.Data)
	a[6] &= 0x80 // only bit 49 belongs to the frame
	n.tx.buf = append(n.tx.buf, a)
	if len(n.tx.buf) == 4 {
		n.sendVoiceLocked()
	}
	return nil
}

// EndTX flushes a partial voice packet (padded with silence) and sends the
// trailer (TX_REL).
func (n *Net) EndTX(call uint64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.tx.open || n.tx.id != call {
		return nil
	}
	if len(n.tx.buf) > 0 {
		var sil [7]byte
		copy(sil[:], vocoder.SilenceAMBE[:])
		sil[6] &= 0x80
		for len(n.tx.buf) < 4 {
			n.tx.buf = append(n.tx.buf, sil)
		}
		n.sendVoiceLocked()
	}
	group := n.tx.l3.IsGroup()
	n.writeLocked(n.dataPacket(n.id, n.tg, flagsFor(group, nxdn.FlagEnd), nxdn.BuildHeader(n.tx.l3.TxRel(), n.ran)), n.addr)
	n.tx = txCall{}
	return nil
}
