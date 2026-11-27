// SPDX-License-Identifier: GPL-3.0-or-later

// Package ysf is the System Fusion network (ctl.ModeYSF): a link to one
// YSFReflector-style reflector over UDP, as g4klx YSFGateway
// (YSFClients/YSFGateway/YSFNetwork.cpp) and MMDVMHost (YSFNetwork.cpp)
// speak it.
//
// # Wire format
//
//	poll     "YSFP" + node callsign (10, space padded)          14 bytes, every 5 s
//	unlink   "YSFU" + node callsign (10)                         14 bytes, sent 3 times
//	data     "YSFD" + gateway callsign (10) + source (10) +     155 bytes
//	         destination (10) + counter byte + 120-byte YSF frame
//
// The counter byte is (frame counter mod 128) << 1 with bit 0 set on the
// last frame of a transmission (MMDVMHost CYSFNetwork::write). The reflector
// answers polls with "YSFP" + its name; the link (login ok) is up once any
// packet comes back from the reflector's address. "YSFO"/"YSFI" packets are
// ignored. Wires-X is not implemented.
//
// The YSF frame (sync, FICH, payload) is built and parsed by package
// internal/dv/ysf.
//
// # Voice
//
// The network codec is network.CodecAMBE: VoiceFrame.Data is a 7-byte
// AMBE+2 frame. Transmissions are sent in V/D mode 2 ("DN"): a header frame
// (FI=0, CSD1 = "ALL" + source callsign, CSD2 blank), one communications
// frame per five 20 ms frames (FT=6, FN 0..6 cycling, DCH: FN0 destination,
// FN1 and FN2 source callsign, FN3..5 blank, FN6 zeros), the last one
// padded with AMBE silence, then a terminator (FI=2, end bit set).
//
// Received V/D mode 2 frames give five AMBE frames each. A call starts at a
// header (source callsign from CSD1, else from the network header) or at
// the first communications frame, and ends at a terminator, at a frame with
// the end bit, or after rx_timeout_ms without frames. Voice FR ("VW", DT=3)
// frames are decoded to 11-byte IMBE frames (package imbe layout) and passed
// to the sink as well, with VoiceFrame.Codec set to network.CodecIMBE (DN
// frames are CodecAMBE), so one call may mix both. Transmissions are always
// DN. V/D mode 1 and data FR frames carry no voice for this network.
//
// # Target
//
// Tune links to Target.Host:Target.Port (resolved by the server from
// YSF_Hosts.txt, Room = reflector id). Without a host, Room may be
// "host:port". The default port is 42000. Nothing is linked until the first
// Tune.
//
// # Options (ctl.NetConfig.Options)
//
//	poll_ms        poll interval, default 5000
//	timeout_ms     no reply for this long = link lost (login pending,
//	               "retrying"), default 15000; polls then back off
//	               (doubling) up to 12 poll intervals until a reply
//	rx_timeout_ms  end a received call after this much silence, default 1500
//	tick_ms        housekeeping interval, default 100 (tests)
//
// Identity.Callsign is the node callsign (required).
package ysf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	air "github.com/w1jar/nodespot/internal/dv/ysf"
)

const (
	DefaultPort      = 42000
	DefaultPoll      = 5 * time.Second
	DefaultTimeout   = 15 * time.Second
	DefaultRxTimeout = 1500 * time.Millisecond
	DefaultTick      = 100 * time.Millisecond
	maxBackoffPolls  = 12

	OptPollMS      = "poll_ms"
	OptTimeoutMS   = "timeout_ms"
	OptRxTimeoutMS = "rx_timeout_ms"
	OptTickMS      = "tick_ms"

	PacketLen = 155
	hdrLen    = 35
	unlinkN   = 3

	// TX framing.
	frameTotal = 6 // FT: FN runs 0..6
	csMode     = 2 // FICH CS as MMDVM_CM sends it
	voicePer   = 5 // AMBE frames per YSF frame
)

var destAll = pad("ALL")

// vwCodec is the codec of the frames passed to the sink from Voice FR
// frames (11-byte IMBE); DN frames are AMBE (the zero value).
const vwCodec = network.CodecIMBE

func voiceFrame(call uint64, seq uint32, data []byte, c network.Codec) network.VoiceFrame {
	return network.VoiceFrame{Call: call, Seq: seq, Codec: c, Data: data}
}

func init() {
	network.Register(ctl.ModeYSF, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
}

func pad(s string) string {
	if len(s) > air.CallsignLen {
		return s[:air.CallsignLen]
	}
	return s + strings.Repeat(" ", air.CallsignLen-len(s))
}

func trim(b []byte) string { return strings.TrimRight(string(b), " \x00") }

func optMS(cfg ctl.NetConfig, key string, def time.Duration) time.Duration {
	if v, err := strconv.Atoi(cfg.Options[key]); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return def
}

type rxCall struct {
	id   uint64
	seq  uint32
	last time.Time
	fich air.FICH // last valid FICH
}

type txCall struct {
	id    uint64
	src   string
	count uint8
	comm  int // communications frames sent
	buf   [][air.AMBELen]byte
}

// Net is the YSF network.
type Net struct {
	enabled  bool
	callsign string // padded
	cfg      ctl.NetConfig
	poll     time.Duration
	timeout  time.Duration
	rxTO     time.Duration
	tick     time.Duration

	mu      sync.Mutex
	sink    network.Sink
	conn    *net.UDPConn
	target  *ctl.Target
	addr    *net.UDPAddr
	gen     uint64 // bumps on every tune/unlink
	login   ctl.LoginState
	detail  string
	linked  bool
	lost    bool      // timed out, polling with backoff
	since   time.Time // link start or last reply
	next    time.Time // next poll or resolve retry
	backoff time.Duration
	rx      *rxCall
	tx      *txCall
	nextID  uint64
	pend    []func()
}

// New builds the network.
func New(cfg ctl.NetConfig, id ctl.Identity) (*Net, error) {
	if strings.TrimSpace(id.Callsign) == "" {
		return nil, errors.New("ysf: identity callsign is required")
	}
	return &Net{
		enabled:  cfg.Enabled,
		callsign: pad(strings.ToUpper(strings.TrimSpace(id.Callsign))),
		cfg:      cfg,
		poll:     optMS(cfg, OptPollMS, DefaultPoll),
		timeout:  optMS(cfg, OptTimeoutMS, DefaultTimeout),
		rxTO:     optMS(cfg, OptRxTimeoutMS, DefaultRxTimeout),
		tick:     optMS(cfg, OptTickMS, DefaultTick),
		login:    ctl.LoginOff,
	}, nil
}

func (n *Net) Mode() string         { return ctl.ModeYSF }
func (n *Net) Codec() network.Codec { return network.CodecAMBE }

// Status reports the link.
func (n *Net) Status() ctl.NetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ctl.NetStatus{Mode: ctl.ModeYSF, Available: true, Enabled: n.enabled, Login: n.login, Detail: n.detail}
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

func (n *Net) send(b []byte) {
	if n.conn != nil && n.addr != nil {
		n.conn.WriteToUDP(b, n.addr) //nolint:errcheck // UDP; loss is handled by polling
	}
}

func (n *Net) ctrl(kind string) []byte { return []byte(kind + n.callsign) }

// Run opens the socket and keeps the link until ctx ends.
func (n *Net) Run(ctx context.Context, s network.Sink) error {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return fmt.Errorf("ysf: %w", err)
	}
	n.mu.Lock()
	n.sink, n.conn = s, conn
	if n.target == nil {
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
			n.mu.Lock()
			if n.addr != nil {
				for i := 0; i < unlinkN; i++ {
					n.send(n.ctrl("YSFU"))
				}
			}
			n.endRX()
			n.tx = nil
			n.conn = nil
			n.linked = false
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

// resolveTarget gives the host and port of t with the fallbacks of the
// package doc.
func (n *Net) resolveTarget(t ctl.Target) (string, int, error) {
	host, port := t.Host, t.Port
	if host == "" && strings.Contains(t.Room, ":") {
		h, p, err := net.SplitHostPort(t.Room)
		if err == nil {
			host = h
			port, _ = strconv.Atoi(p)
		}
	}
	if host == "" {
		return "", 0, errors.New("ysf: target has no host")
	}
	if port <= 0 {
		port = DefaultPort
	}
	if port > 65535 {
		return "", 0, errors.New("ysf: bad port")
	}
	return host, port, nil
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

func targetName(t ctl.Target, host string, port int) string {
	switch {
	case t.Name != "":
		return t.Name
	case t.Room != "" && !strings.Contains(t.Room, ":"):
		return t.Room
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// setTarget (mu held) drops the current link and arms a new one; the
// address is resolved by housekeeping.
func (n *Net) setTarget(t ctl.Target, now time.Time) {
	n.dropLink()
	tt := t
	n.target = &tt
	n.gen++
	n.backoff = n.poll
	n.next = now
	n.since = now
	n.setLogin(ctl.LoginPending, "linking")
}

// dropLink (mu held) sends the unlink and forgets the link.
func (n *Net) dropLink() {
	if n.addr != nil {
		for i := 0; i < unlinkN; i++ {
			n.send(n.ctrl("YSFU"))
		}
	}
	n.endRX()
	n.tx = nil
	n.target, n.addr, n.linked, n.lost = nil, nil, false, false
	n.gen++
}

// Tune links to t.
func (n *Net) Tune(ctx context.Context, t ctl.Target) error {
	if _, _, err := n.resolveTarget(t); err != nil {
		return err
	}
	n.mu.Lock()
	n.setTarget(t, time.Now())
	n.unlock()
	n.housekeeping(ctx, time.Now())
	return nil
}

// Unlink drops the link.
func (n *Net) Unlink(context.Context) error {
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
	if n.target == nil || now.Before(n.next) {
		n.unlock()
		return
	}
	if n.addr == nil {
		t, gen := *n.target, n.gen
		host, port, _ := n.resolveTarget(t)
		n.unlock()
		a, err := resolve(ctx, host, port)
		n.mu.Lock()
		if gen != n.gen {
			n.unlock()
			return
		}
		if err != nil {
			n.setLogin(ctl.LoginFailed, "cannot resolve "+host)
			n.retryLater(now)
			n.unlock()
			return
		}
		n.addr = a
		n.since = now
		n.target.Name = targetName(t, host, port)
	}
	if n.conn != nil {
		n.send(n.ctrl("YSFP"))
	}
	if now.Sub(n.since) > n.timeout {
		if !n.lost {
			n.lost, n.linked = true, false
			n.endRX()
			n.backoff = n.poll
		}
		n.setLogin(ctl.LoginPending, ctl.DetailNoReply+n.target.Name+", retrying")
		n.retryLater(now)
	} else {
		n.next = now.Add(n.poll)
	}
	n.unlock()
}

// retryLater (mu held) schedules the next attempt with exponential backoff.
func (n *Net) retryLater(now time.Time) {
	n.next = now.Add(n.backoff)
	n.backoff *= 2
	if m := n.poll * maxBackoffPolls; n.backoff > m {
		n.backoff = m
	}
}

func sameAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}

func (n *Net) onPacket(from *net.UDPAddr, p []byte, now time.Time) {
	n.mu.Lock()
	defer n.unlock()
	if !sameAddr(from, n.addr) || len(p) < 4 {
		return
	}
	n.since = now
	if !n.linked {
		n.linked, n.lost = true, false
		n.backoff = n.poll
		n.next = now.Add(n.poll)
		n.setLogin(ctl.LoginOK, "linked to "+n.target.Name)
	}
	if string(p[:4]) == "YSFD" && len(p) >= PacketLen {
		n.onData(p[:PacketLen], now)
	}
}

func (n *Net) startRX(src string, now time.Time) {
	n.endRX()
	n.nextID++
	c := &rxCall{id: n.nextID, last: now}
	n.rx = c
	h := network.CallHeader{Call: c.id, Src: src}
	if n.target != nil {
		h.Room = n.target.Room
		if h.Room == "" || strings.Contains(h.Room, ":") {
			h.Room = n.target.Name
		}
	}
	if s := n.sink; s != nil {
		n.pend = append(n.pend, func() { s.Header(h) })
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

func (n *Net) voice(data []byte, c network.Codec) {
	f := voiceFrame(n.rx.id, n.rx.seq, data, c)
	n.rx.seq++
	if s := n.sink; s != nil {
		n.pend = append(n.pend, func() { s.Voice(f) })
	}
}

func (n *Net) onData(p []byte, now time.Time) {
	fr := p[hdrLen:]
	end := p[34]&1 != 0
	f, ok := air.DecodeFICH(fr)
	if !ok {
		if n.rx == nil || n.rx.fich.FI != air.FICommunications {
			if end {
				n.endRX()
			}
			return
		}
		f = n.rx.fich // keep going on the last good FICH
		f.FN, f.FT = 1, 0
	}
	switch {
	case f.FI == air.FITerminator || end:
		n.endRX()
		return
	case f.FI == air.FIHeader:
		src := trim(p[14:24])
		if csd1, ok1, _, _ := air.ReadCSD(fr); ok1 && trim(csd1[10:]) != "" {
			src = trim(csd1[10:])
		}
		n.startRX(src, now)
		n.rx.fich = f
		return
	case f.FI != air.FICommunications:
		return
	}
	if n.rx == nil {
		n.startRX(trim(p[14:24]), now)
	}
	n.rx.last = now
	n.rx.fich = f
	switch f.DT {
	case air.DTVDMode2:
		for j := 0; j < voicePer; j++ {
			a, _ := air.ReadVCH(fr, j)
			n.voice(a[:], network.CodecAMBE)
		}
	case air.DTVoiceFRMode:
		for _, j := range air.VWBlocks(f) {
			v, _ := air.ReadVW(fr, j)
			n.voice(v[:], vwCodec)
		}
	}
}

// ---- TX ----

func (n *Net) packet(src string, end bool, count uint8) []byte {
	p := make([]byte, PacketLen)
	copy(p, "YSFD")
	copy(p[4:], n.callsign)
	copy(p[14:], src)
	copy(p[24:], destAll)
	p[34] = (count & 0x7F) << 1
	if end {
		p[34] |= 1
	}
	copy(p[hdrLen:], air.Sync[:])
	return p
}

func (n *Net) fich(fi, fn uint8) air.FICH {
	return air.FICH{FI: fi, CS: csMode, FN: fn, FT: frameTotal, DT: air.DTVDMode2}
}

func (n *Net) sendHeaderOrTerm(fi uint8, end bool) {
	t := n.tx
	p := n.packet(t.src, end, t.count)
	t.count++
	fr := p[hdrLen:]
	n.fich(fi, 0).Encode(fr)
	var csd1, csd2 [20]byte
	copy(csd1[:], destAll+t.src)
	copy(csd2[:], strings.Repeat(" ", 20))
	air.WriteCSD(fr, csd1, csd2)
	n.send(p)
}

func (n *Net) sendComm() {
	t := n.tx
	for len(t.buf) < voicePer {
		t.buf = append(t.buf, air.AMBESilence)
	}
	fn := uint8(t.comm % (frameTotal + 1))
	t.comm++
	p := n.packet(t.src, false, t.count)
	t.count++
	fr := p[hdrLen:]
	n.fich(air.FICommunications, fn).Encode(fr)
	var dch [10]byte
	switch fn {
	case 0:
		copy(dch[:], destAll)
	case 1, 2:
		copy(dch[:], t.src)
	case 3, 4, 5:
		copy(dch[:], strings.Repeat(" ", 10))
	}
	air.WriteDCH(fr, dch)
	for j, a := range t.buf {
		air.WriteVCH(fr, j, a)
	}
	t.buf = t.buf[:0]
	n.send(p)
}

// StartTX sends the header of a transmission.
func (n *Net) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	defer n.unlock()
	if n.addr == nil || n.conn == nil {
		return errors.New("ysf: not linked")
	}
	src := strings.ToUpper(strings.TrimSpace(h.Src))
	if src == "" {
		src = n.callsign
	}
	n.tx = &txCall{id: h.Call, src: pad(src)}
	n.sendHeaderOrTerm(air.FIHeader, false)
	return nil
}

// WriteVoice queues one 7-byte AMBE frame; every fifth completes a frame.
func (n *Net) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != air.AMBELen {
		return fmt.Errorf("ysf: voice frame is %d bytes, want %d", len(f.Data), air.AMBELen)
	}
	n.mu.Lock()
	defer n.unlock()
	if n.tx == nil || n.tx.id != f.Call {
		return nil
	}
	var a [air.AMBELen]byte
	copy(a[:], f.Data)
	n.tx.buf = append(n.tx.buf, a)
	if len(n.tx.buf) == voicePer {
		n.sendComm()
	}
	return nil
}

// EndTX flushes the last frame and sends the terminator.
func (n *Net) EndTX(call uint64) error {
	n.mu.Lock()
	defer n.unlock()
	if n.tx == nil || n.tx.id != call {
		return nil
	}
	if len(n.tx.buf) > 0 {
		n.sendComm()
	}
	n.sendHeaderOrTerm(air.FITerminator, true)
	n.tx = nil
	return nil
}
