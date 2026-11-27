// SPDX-License-Identifier: GPL-3.0-or-later

// Package p25 is the P25 network (ctl.ModeP25): a link to one
// P25Reflector-style reflector over UDP, as g4klx P25Gateway
// (P25Clients/P25Gateway/P25Network.cpp) and MMDVMHost (P25Network.cpp)
// speak it. The record layouts below follow MMDVMHost CP25Network
// writeLDU1/writeLDU2 (GPL-2.0-or-later, Jonathan Naylor G4KLX) closely.
//
// # Wire format
//
//	poll    0xF0 + node callsign (10, space padded)   11 bytes, every 5 s
//	unlink  0xF1 + node callsign (10)                 11 bytes, sent 3 times
//	voice   one UDP packet per 20 ms IMBE frame, a record per frame:
//
//	  LDU1   type  len  IMBE at  other fields
//	  0x62   22   10      [1..9] = 02 02 0C 0B 12 64 00 00 80 (fixed)
//	  0x63   14   1
//	  0x64   17   5       [1] LCF, [2] MFId
//	  0x65   17   5       [1..3] destination (talkgroup), 24-bit big-endian
//	  0x66   17   5       [1..3] source id, 24-bit big-endian
//	  0x67   17   5       [1..3] F0 9D 6A (RS parity, constant)
//	  0x68   17   5       [1..3] 19 D4 26
//	  0x69   17   5       [1..3] E0 EB 7B
//	  0x6A   16   4       [1..2] low speed data, [3] 02
//
//	  LDU2   0x6B..0x73 the same lengths; 0x6D..0x6F carry the message
//	  indicator (3 bytes each), 0x70 [1] algorithm id (0x80 clear) and
//	  [2..3] key id, 0x71/0x72 constants AC B8 A4 / 9B DC 75, 0x73 LSD.
//
//	terminator  0x80 + 16 zero bytes
//
// Records of 17 bytes end in 0x02, as MMDVMHost's templates do. The
// reflector echoes polls; the link (login ok) is up once any packet comes
// back from its address.
//
// # Voice
//
// The network codec is network.CodecIMBE: VoiceFrame.Data is the 11-byte
// IMBE frame of package imbe (u0..u7 MSB first), which is the byte layout
// of the records. EndTX pads the last LDU with IMBE silence and sends the
// terminator. RX calls start once the source id (record 0x66) is heard or
// after nine frames, and end at the terminator or after rx_timeout_ms.
// The source callsign is not carried; CallHeader.Src stays empty and
// SrcID is the P25 unit id.
//
// # Target
//
// Tune takes the talkgroup from Target.TG, else from Room (decimal), and
// the reflector from Target.Host:Target.Port (resolved by the server from
// P25Hosts.txt). The default port is 41000. Talkgroup 9999 without a host
// unlinks, as in P25Gateway. Nothing is linked until the first Tune.
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
package p25

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
)

const (
	DefaultPort      = 41000
	DefaultPoll      = 5 * time.Second
	DefaultTimeout   = 15 * time.Second
	DefaultRxTimeout = 1500 * time.Millisecond
	DefaultTick      = 100 * time.Millisecond
	maxBackoffPolls  = 12

	OptPollMS      = "poll_ms"
	OptTimeoutMS   = "timeout_ms"
	OptRxTimeoutMS = "rx_timeout_ms"
	OptTickMS      = "tick_ms"

	UnlinkTG    = 9999
	IMBELen     = 11
	callsignLen = 10
	unlinkN     = 3
	ldu         = 9 // frames per LDU
	superframe  = 2 * ldu

	TypePoll   = 0xF0
	TypeUnlink = 0xF1
	TypeEnd    = 0x80
	typeLDU1   = 0x62
	typeLDU2   = 0x6B

	AlgClear = 0x80
	LCFGroup = 0x00
	LCFUnit  = 0x03 // unit to unit
)

// IMBESilence is an IMBE silence frame (package imbe layout).
var IMBESilence = [IMBELen]byte{0x04, 0x0C, 0xFD, 0x7B, 0xFB, 0x7D, 0xF2, 0x7B, 0x3D, 0x9E, 0x44}

// recLen and imbeOff by frame index 0..17 (0x62 + i, or 0x6B + i - 9).
var (
	recLen  = [ldu]int{22, 14, 17, 17, 17, 17, 17, 17, 16}
	imbeOff = [ldu]int{10, 1, 5, 5, 5, 5, 5, 5, 4}
	rec62   = [9]byte{0x02, 0x02, 0x0C, 0x0B, 0x12, 0x64, 0x00, 0x00, 0x80}
	consts1 = [3][3]byte{{0xF0, 0x9D, 0x6A}, {0x19, 0xD4, 0x26}, {0xE0, 0xEB, 0x7B}}
	consts2 = [2][3]byte{{0xAC, 0xB8, 0xA4}, {0x9B, 0xDC, 0x75}}
)

// LC is the link control and encryption sync carried in the records.
type LC struct {
	LCF   uint8
	MFId  uint8
	Dst   uint32 // talkgroup or unit id, 24 bits
	Src   uint32 // 24 bits
	MI    [9]byte
	AlgID uint8
	KID   uint16
	LSD   [2]byte
}

func put24(b []byte, v uint32) { b[0], b[1], b[2] = byte(v>>16), byte(v>>8), byte(v) }
func get24(b []byte) uint32    { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

// Record builds the record of superframe index i (0..17) carrying imbe.
func Record(i int, imbe [IMBELen]byte, lc *LC) []byte {
	k := i % ldu
	r := make([]byte, recLen[k])
	if i < ldu {
		r[0] = typeLDU1 + byte(k)
	} else {
		r[0] = typeLDU2 + byte(k)
	}
	if recLen[k] == 17 || k == 1 {
		r[len(r)-1] = 0x02
	}
	switch {
	case k == 0:
		copy(r[1:], rec62[:])
	case k == 1: // IMBE only
	case k == 8:
		copy(r[1:3], lc.LSD[:])
		r[3] = 0x02
	case i < ldu:
		switch k {
		case 2:
			r[1], r[2] = lc.LCF, lc.MFId
		case 3:
			put24(r[1:], lc.Dst)
		case 4:
			put24(r[1:], lc.Src)
		default:
			copy(r[1:4], consts1[k-5][:])
		}
	default:
		switch k {
		case 2, 3, 4:
			copy(r[1:4], lc.MI[(k-2)*3:])
		case 5:
			r[1] = lc.AlgID
			r[2], r[3] = byte(lc.KID>>8), byte(lc.KID)
		default:
			copy(r[1:4], consts2[k-6][:])
		}
	}
	copy(r[imbeOff[k]:], imbe[:])
	return r
}

// ParseRecord returns the superframe index and IMBE frame of a voice
// record, and fills the fields it carries into lc. ok is false for
// anything else.
func ParseRecord(r []byte, lc *LC) (i int, imbe [IMBELen]byte, ok bool) {
	if len(r) == 0 {
		return
	}
	switch t := r[0]; {
	case t >= typeLDU1 && t < typeLDU1+ldu:
		i = int(t - typeLDU1)
	case t >= typeLDU2 && t < typeLDU2+ldu:
		i = ldu + int(t-typeLDU2)
	default:
		return
	}
	k := i % ldu
	if len(r) < imbeOff[k]+IMBELen {
		return
	}
	copy(imbe[:], r[imbeOff[k]:])
	if lc != nil {
		switch {
		case k == 8:
			copy(lc.LSD[:], r[1:3])
		case k == 0 || k == 1:
		case i < ldu:
			switch k {
			case 2:
				lc.LCF, lc.MFId = r[1], r[2]
			case 3:
				lc.Dst = get24(r[1:])
			case 4:
				lc.Src = get24(r[1:])
			}
		default:
			switch k {
			case 2, 3, 4:
				copy(lc.MI[(k-2)*3:], r[1:4])
			case 5:
				lc.AlgID, lc.KID = r[1], uint16(r[2])<<8|uint16(r[3])
			}
		}
	}
	return i, imbe, true
}

// EndRecord is the terminator record.
func EndRecord() []byte {
	r := make([]byte, 17)
	r[0] = TypeEnd
	return r
}

func init() {
	network.Register(ctl.ModeP25, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
}

func pad(s string) string {
	if len(s) > callsignLen {
		return s[:callsignLen]
	}
	return s + strings.Repeat(" ", callsignLen-len(s))
}

func optMS(cfg ctl.NetConfig, key string, def time.Duration) time.Duration {
	if v, err := strconv.Atoi(cfg.Options[key]); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return def
}

type rxCall struct {
	id      uint64 // 0 until the header went out
	seq     uint32
	last    time.Time
	lc      LC
	srcSeen bool
	held    [][IMBELen]byte
}

type txCall struct {
	id uint64
	n  int // frames sent
	lc LC
}

// Net is the P25 network.
type Net struct {
	enabled  bool
	callsign string // padded
	srcID    uint32
	cfg      ctl.NetConfig
	poll     time.Duration
	timeout  time.Duration
	rxTO     time.Duration
	tick     time.Duration

	mu      sync.Mutex
	sink    network.Sink
	conn    *net.UDPConn
	target  *ctl.Target
	tg      uint32
	addr    *net.UDPAddr
	gen     uint64
	login   ctl.LoginState
	detail  string
	linked  bool
	lost    bool
	since   time.Time
	next    time.Time
	backoff time.Duration
	rx      *rxCall
	tx      *txCall
	nextID  uint64
	pend    []func()
}

// New builds the network.
func New(cfg ctl.NetConfig, id ctl.Identity) (*Net, error) {
	if strings.TrimSpace(id.Callsign) == "" {
		return nil, errors.New("p25: identity callsign is required")
	}
	return &Net{
		enabled:  cfg.Enabled,
		callsign: pad(strings.ToUpper(strings.TrimSpace(id.Callsign))),
		srcID:    id.DMRID & 0xFFFFFF,
		cfg:      cfg,
		poll:     optMS(cfg, OptPollMS, DefaultPoll),
		timeout:  optMS(cfg, OptTimeoutMS, DefaultTimeout),
		rxTO:     optMS(cfg, OptRxTimeoutMS, DefaultRxTimeout),
		tick:     optMS(cfg, OptTickMS, DefaultTick),
		login:    ctl.LoginOff,
	}, nil
}

func (n *Net) Mode() string         { return ctl.ModeP25 }
func (n *Net) Codec() network.Codec { return network.CodecIMBE }

// Status reports the link.
func (n *Net) Status() ctl.NetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ctl.NetStatus{Mode: ctl.ModeP25, Available: true, Enabled: n.enabled, Login: n.login, Detail: n.detail}
}

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

func (n *Net) ctrl(t byte) []byte { return append([]byte{t}, n.callsign...) }

// Run opens the socket and keeps the link until ctx ends.
func (n *Net) Run(ctx context.Context, s network.Sink) error {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return fmt.Errorf("p25: %w", err)
	}
	n.mu.Lock()
	n.sink, n.conn = s, conn
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

func (n *Net) resolveTarget(t ctl.Target) (host string, port int, tg uint32, err error) {
	tg = t.TG
	if tg == 0 && t.Room != "" {
		v, err := strconv.ParseUint(strings.TrimSpace(t.Room), 10, 32)
		if err != nil {
			return "", 0, 0, fmt.Errorf("p25: bad talkgroup %q", t.Room)
		}
		tg = uint32(v)
	}
	if tg > 0xFFFFFF {
		return "", 0, 0, errors.New("p25: talkgroup out of range")
	}
	host, port = t.Host, t.Port
	if host == "" {
		return "", 0, 0, errors.New("p25: target has no host")
	}
	if port <= 0 {
		port = DefaultPort
	}
	if port > 65535 {
		return "", 0, 0, errors.New("p25: bad port")
	}
	return host, port, tg, nil
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
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			return &net.UDPAddr{IP: ip.IP, Port: port}, nil
		}
	}
	return &net.UDPAddr{IP: ips[0].IP, Port: port}, nil
}

func targetName(t ctl.Target, tg uint32, host string, port int) string {
	switch {
	case t.Name != "":
		return t.Name
	case tg != 0:
		return "TG " + strconv.FormatUint(uint64(tg), 10)
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (n *Net) setTarget(t ctl.Target, tg uint32, now time.Time) {
	n.dropLink()
	tt := t
	n.target, n.tg = &tt, tg
	n.gen++
	n.backoff = n.poll
	n.next = now
	n.since = now
	n.setLogin(ctl.LoginPending, "linking")
}

func (n *Net) dropLink() {
	if n.addr != nil {
		for i := 0; i < unlinkN; i++ {
			n.send(n.ctrl(TypeUnlink))
		}
	}
	n.endRX()
	n.tx = nil
	n.target, n.addr, n.linked, n.lost = nil, nil, false, false
	n.gen++
}

// Tune links to t.
func (n *Net) Tune(ctx context.Context, t ctl.Target) error {
	if t.Host == "" && (t.TG == UnlinkTG || strings.TrimSpace(t.Room) == strconv.Itoa(UnlinkTG)) {
		return n.Unlink(ctx)
	}
	_, _, tg, err := n.resolveTarget(t)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.setTarget(t, tg, time.Now())
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
		host, port, tg, _ := n.resolveTarget(t)
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
		n.target.Name = targetName(t, tg, host, port)
	}
	if n.conn != nil {
		n.send(n.ctrl(TypePoll))
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
	if !sameAddr(from, n.addr) || len(p) == 0 {
		return
	}
	n.since = now
	if !n.linked {
		n.linked, n.lost = true, false
		n.backoff = n.poll
		n.next = now.Add(n.poll)
		n.setLogin(ctl.LoginOK, "linked to "+n.target.Name)
	}
	switch p[0] {
	case TypePoll, TypeUnlink:
		return
	case TypeEnd:
		n.endRX()
		return
	}
	if n.rx == nil {
		n.rx = &rxCall{}
	}
	c := n.rx
	_, imbe, ok := ParseRecord(p, &c.lc)
	if !ok {
		return
	}
	c.last = now
	if p[0] == typeLDU1+4 {
		c.srcSeen = true
	}
	if c.id == 0 {
		c.held = append(c.held, imbe)
		if !c.srcSeen && len(c.held) < ldu {
			return
		}
		n.nextID++
		c.id = n.nextID
		h := network.CallHeader{Call: c.id, SrcID: c.lc.Src, TG: c.lc.Dst, Private: c.lc.LCF == LCFUnit}
		if h.TG == 0 {
			h.TG = n.tg
		}
		if n.target != nil {
			h.Room = n.target.Name
		}
		if s := n.sink; s != nil {
			n.pend = append(n.pend, func() { s.Header(h) })
		}
		for _, f := range c.held {
			n.voice(f)
		}
		c.held = nil
		return
	}
	n.voice(imbe)
}

func (n *Net) voice(f [IMBELen]byte) {
	v := network.VoiceFrame{Call: n.rx.id, Seq: n.rx.seq, Codec: network.CodecIMBE, Data: append([]byte(nil), f[:]...)}
	n.rx.seq++
	if s := n.sink; s != nil {
		n.pend = append(n.pend, func() { s.Voice(v) })
	}
}

func (n *Net) endRX() {
	c := n.rx
	n.rx = nil
	if c == nil || c.id == 0 {
		return
	}
	if s := n.sink; s != nil {
		n.pend = append(n.pend, func() { s.End(c.id) })
	}
}

// ---- TX ----

// StartTX begins a transmission; the LC goes out with the voice records.
func (n *Net) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	defer n.unlock()
	if n.addr == nil || n.conn == nil {
		return errors.New("p25: not linked")
	}
	tg := h.TG
	if tg == 0 {
		tg = n.tg
	}
	src := n.srcID
	if src == 0 {
		src = h.SrcID & 0xFFFFFF
	}
	lc := LC{LCF: LCFGroup, Dst: tg & 0xFFFFFF, Src: src, AlgID: AlgClear}
	if h.Private {
		lc.LCF = LCFUnit
	}
	n.tx = &txCall{id: h.Call, lc: lc}
	return nil
}

func (n *Net) sendFrame(f [IMBELen]byte) {
	t := n.tx
	n.send(Record(t.n%superframe, f, &t.lc))
	t.n++
}

// WriteVoice sends one 11-byte IMBE frame.
func (n *Net) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != IMBELen {
		return fmt.Errorf("p25: voice frame is %d bytes, want %d", len(f.Data), IMBELen)
	}
	n.mu.Lock()
	defer n.unlock()
	if n.tx == nil || n.tx.id != f.Call {
		return nil
	}
	var a [IMBELen]byte
	copy(a[:], f.Data)
	n.sendFrame(a)
	return nil
}

// EndTX completes the last LDU with silence and sends the terminator.
func (n *Net) EndTX(call uint64) error {
	n.mu.Lock()
	defer n.unlock()
	if n.tx == nil || n.tx.id != call {
		return nil
	}
	for n.tx.n%ldu != 0 {
		n.sendFrame(IMBESilence)
	}
	n.send(EndRecord())
	n.tx = nil
	return nil
}
