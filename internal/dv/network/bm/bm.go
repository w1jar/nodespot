// Package bm is the BrandMeister network (ctl.ModeBM). It speaks BrandMeister's
// Rewind protocol ("REWIND01") as an Open DMR Terminal (service 0x21, UDP,
// default port 54006), not the Homebrew/MMDVM protocol. The wire format is
// documented in rewind.go; the implementation is original (go-brandmeister
// and DigestPlay, both MIT, were used as protocol references only). The
// session behaviour below matches a capture of a working reference client.
//
// Rewind carries no DMR bursts: a call is three voice headers (a 12 byte Full
// LC: FLCO, destination, source), audio packets of three AMBE frames every
// 60 ms, and a terminator. There is no super header and no talker alias
// handling (DMR_EMBEDDED packets are ignored); a received super header is
// still tolerated. TX audio packets are 27 bytes: three FEC coded 72-bit
// AMBE+2 frames (internal/dv/dmr EncodeAMBE from the 49-bit md380 frames) per
// 60 ms. RX accepts the same 27 byte packets (decoded to 49 bits with
// dmr.DecodeAMBE) and also 21 byte linear packets (three 49-bit frames).
//
// Login: keep-alive (carries the operator's DMR ID, the service and a
// software description) -> server challenge (4 byte salt) -> authentication =
// SHA-256(salt || password) and at once another keep-alive -> the server
// answers with an empty keep-alive = logged in. No configuration packet is
// sent. No registered application ID is needed: the keep-alive number is
// Identity.DMRID. The keep-alive is repeated every 5 s and the server echoes
// each with an empty one. Failed or lost sessions are retried with
// exponential backoff 1 s .. 60 s.
//
// Network options (ctl.NetConfig.Options), all optional:
//
//	client_version   software description text (default "nodespot-dv").
//	keepalive_ms     keep-alive period (default 5000).
//	timeout_ms       session considered lost after this silence (default 15000).
//	login_timeout_ms one login attempt (default 5000).
//	backoff_min_ms   first retry delay (default 1000).
//	backoff_max_ms   retry delay cap (default 60000).
//	rx_timeout_ms    end a received call without terminator after this
//	                 (default 1500).
//
// NetConfig.Address is required (there is no default host; use the master
// host name BrandMeister gave you); Port defaults to 54006. Identity.DMRID
// is required. The password (the one set in BrandMeister SelfCare, hotspot
// security) is never logged and never reported in Status.
//
// Tune: an empty CANCELLING (0x0902) followed by SUBSCRIPTION (0x0901) with
// {7, target}; the type is always 7 (group), also for the parrot and private
// targets. The server echoes both with empty payloads, which are ignored.
// Parrot (TG 9990) is a private call: the subscription is {7, 9990}, TX uses
// a voice header LC with FLCO 0x03 (private), destination 9990, source the
// own DMR ID, and the playback arrives as a private call from 9990 to the own
// DMR ID. Other private targets are handled the same way; that is not
// confirmed by the capture. Unlink tunes TG 4000, BrandMeister's disconnect
// talkgroup.
package bm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
)

// logf writes the engine's log lines (stderr, so the journal); a variable so
// tests can capture it.
var logf = log.Printf

// logRepeat is the least time between two log lines with the same reason.
const logRepeat = time.Minute

// ParrotTG is BrandMeister's echo test target (a private call).
const ParrotTG = 9990

// DisconnectTG is BrandMeister's disconnect talkgroup: tuning it unlinks.
const DisconnectTG = 4000

// Defaults.
const (
	DefaultKeepAlive    = 5 * time.Second
	DefaultTimeout      = 15 * time.Second
	DefaultLoginTimeout = 5 * time.Second
	DefaultBackoffMin   = time.Second
	DefaultBackoffMax   = 60 * time.Second
	DefaultRXTimeout    = 1500 * time.Millisecond
)

var (
	errBadPassword = errors.New("authentication refused")
	errLost        = errors.New("connection lost")
	errLoginTO     = errors.New("login timeout")
)

func init() {
	network.Register(ctl.ModeBM, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
}

// Net is the BrandMeister network.
type Net struct {
	enabled  bool
	addr     string
	password string
	id       ctl.Identity
	version  string

	keepAlive, timeout, loginTO, bMin, bMax, rxTO time.Duration

	mu       sync.Mutex
	conn     *net.UDPConn
	seq      [2]uint32
	running  bool
	login    ctl.LoginState
	detail   string
	logged   bool
	sink     network.Sink
	sub      *subscription // desired subscription
	nextCall uint64

	txCall   uint64
	txActive bool
	txBuf    []byte
	txDst    uint32
}

type subscription struct {
	tg      uint32
	private bool // TX header selection: voice header FLCO 0x03
}

// New builds the network.
func New(cfg ctl.NetConfig, id ctl.Identity) (*Net, error) {
	if cfg.Address == "" {
		return nil, errors.New("bm: address (master host) is required")
	}
	if id.DMRID == 0 {
		return nil, errors.New("bm: your DMR ID is not set (needed to log in as an Open DMR Terminal)")
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 0 || port > 65535 {
		return nil, errors.New("bm: bad port")
	}
	o := cfg.Options
	n := &Net{
		enabled: cfg.Enabled, password: cfg.Password, id: id,
		addr:      net.JoinHostPort(cfg.Address, strconv.Itoa(port)),
		version:   "nodespot-dv",
		keepAlive: DefaultKeepAlive, timeout: DefaultTimeout, loginTO: DefaultLoginTimeout,
		bMin: DefaultBackoffMin, bMax: DefaultBackoffMax, rxTO: DefaultRXTimeout,
		login: ctl.LoginOff, nextCall: 1 << 33,
	}
	if v := o["client_version"]; v != "" {
		n.version = v
	}
	for key, dst := range map[string]*time.Duration{
		"keepalive_ms": &n.keepAlive, "timeout_ms": &n.timeout, "login_timeout_ms": &n.loginTO,
		"backoff_min_ms": &n.bMin, "backoff_max_ms": &n.bMax, "rx_timeout_ms": &n.rxTO,
	} {
		if v, ok := o[key]; ok {
			ms, err := strconv.Atoi(v)
			if err != nil || ms <= 0 {
				return nil, fmt.Errorf("bm: %s must be a positive integer", key)
			}
			*dst = time.Duration(ms) * time.Millisecond
		}
	}
	if n.bMax < n.bMin {
		n.bMax = n.bMin
	}
	return n, nil
}

func (n *Net) Mode() string         { return ctl.ModeBM }
func (n *Net) Codec() network.Codec { return network.CodecAMBE }

// Status reports the login state and the subscription.
func (n *Net) Status() ctl.NetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ctl.NetStatus{Mode: ctl.ModeBM, Available: true, Enabled: n.enabled, Login: n.login, Detail: n.detail}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		cur = max
	}
	return cur
}

func (n *Net) detailLocked(err error) string {
	d := ""
	if err != nil {
		d = err.Error()
	}
	if n.sub != nil {
		t := "TG"
		if n.sub.private {
			t = "private"
		}
		if d != "" {
			d += "; "
		}
		d += fmt.Sprintf("%s %d", t, n.sub.tg)
	}
	return d
}

func (n *Net) setLogin(st ctl.LoginState, err error) {
	n.mu.Lock()
	n.login, n.detail = st, n.detailLocked(err)
	s := n.sink
	d := n.detail
	n.mu.Unlock()
	if s != nil {
		s.Login(st, d)
	}
}

// Run logs in and keeps the session alive until ctx ends.
func (n *Net) Run(ctx context.Context, s network.Sink) error {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return errors.New("bm: already running")
	}
	n.running, n.sink = true, s
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.running, n.logged = false, false
		n.mu.Unlock()
		n.setLogin(ctl.LoginOff, nil)
	}()
	backoff := n.bMin
	var lastMsg string
	var lastAt time.Time
	for ctx.Err() == nil {
		n.setLogin(ctl.LoginPending, nil)
		ok, err := n.session(ctx, s)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			// one line per failed attempt; the same reason at most once a minute
			if msg := err.Error(); msg != lastMsg || time.Since(lastAt) >= logRepeat {
				logf("bm: session to %s failed: %v", n.addr, err)
				lastMsg, lastAt = msg, time.Now()
			}
		}
		if ok {
			backoff = n.bMin
		}
		if errors.Is(err, errBadPassword) {
			n.setLogin(ctl.LoginFailed, err)
		} else {
			n.setLogin(ctl.LoginPending, err)
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
		backoff = nextBackoff(backoff, n.bMax)
	}
	return nil
}

func (n *Net) sendLocked(typ, flags uint16, payload []byte, copies int) error {
	if n.conn == nil {
		return errors.New("bm: not connected")
	}
	idx := flags & flagRealTime1
	b := marshal(typ, flags, n.seq[idx], payload)
	n.seq[idx]++
	var err error
	for i := 0; i < copies; i++ {
		_, err = n.conn.Write(b)
	}
	return err
}

func (n *Net) send(typ, flags uint16, payload []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sendLocked(typ, flags, payload, 1)
}

func (n *Net) sendKeepAlive() error {
	return n.send(typKeepAlive, 0, versionData(n.id.DMRID, n.version))
}

type rxState struct {
	active  bool
	call    uint64
	src     uint32
	dst     uint32
	private bool
	seq     uint32
	last    time.Time
	// endedAt is when the last call ended (stray audio just after it is
	// not a new call).
	endedAt time.Time
}

// strayGrace is how long audio right after a call's end is ignored.
const strayGrace = 200 * time.Millisecond

func (n *Net) session(ctx context.Context, s network.Sink) (loggedIn bool, err error) {
	raddr, err := net.ResolveUDPAddr("udp", n.addr)
	if err != nil {
		return false, err
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return false, err
	}
	n.mu.Lock()
	n.conn, n.seq = conn, [2]uint32{}
	n.mu.Unlock()
	var rx rxState
	defer func() {
		if rx.active {
			s.End(rx.call)
		}
		n.mu.Lock()
		n.conn, n.logged, n.txActive, n.txBuf = nil, false, false, nil
		n.mu.Unlock()
		conn.Close()
	}()

	ch := make(chan packet, 64)
	go func() {
		defer close(ch)
		buf := make([]byte, maxPacket)
		for {
			m, err := conn.Read(buf)
			if err != nil {
				return
			}
			if p, err := unmarshal(buf[:m]); err == nil {
				select {
				case ch <- p:
				default:
				}
			}
		}
	}()

	tick := minDur(n.keepAlive, n.timeout, n.loginTO, n.rxTO) / 5
	if tick < time.Millisecond {
		tick = time.Millisecond
	}
	if tick > 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	tk := time.NewTicker(tick)
	defer tk.Stop()

	start := time.Now()
	lastRx, lastTx := start, start
	retry := n.loginTO / 5
	authSent := 0
	if err := n.sendKeepAlive(); err != nil {
		return false, err
	}
	for {
		select {
		case <-ctx.Done():
			n.send(typClose, 0, nil)
			return loggedIn, nil
		case p, ok := <-ch:
			if !ok {
				return loggedIn, errLost
			}
			now := time.Now()
			lastRx = now
			switch p.typ {
			case typChallenge:
				if loggedIn {
					return true, errors.New("re-challenged")
				}
				if authSent >= 2 {
					return false, errBadPassword
				}
				d := sha256.Sum256(append(append([]byte(nil), p.payload...), n.password...))
				n.send(typAuth, 0, d[:])
				n.sendKeepAlive()
				authSent++
			case typClose:
				if !loggedIn {
					return false, errBadPassword
				}
				return true, errors.New("closed by server")
			case typKeepAlive:
				// The first server keep-alive after the authentication (an
				// empty one) is the login success.
				if !loggedIn && authSent > 0 {
					loggedIn = true
					n.mu.Lock()
					n.logged = true
					if n.sub != nil {
						n.tuneLocked(n.sub.tg)
					}
					n.mu.Unlock()
					n.setLogin(ctl.LoginOK, nil)
				}
			case typSuperHeader, typVoiceHeader, typTerminator, typAudio:
				// typEmbedded (talker alias) and the echoes of CANCELLING and
				// SUBSCRIPTION have no case: ignored.
				if loggedIn {
					n.rxPacket(s, &rx, p, now)
				}
			}
		case <-tk.C:
			now := time.Now()
			if !loggedIn {
				if now.Sub(start) > n.loginTO {
					return false, errLoginTO
				}
				if now.Sub(lastTx) >= retry {
					n.sendKeepAlive()
					lastTx = now
				}
				continue
			}
			if now.Sub(lastRx) > n.timeout {
				return true, errLost
			}
			if now.Sub(lastTx) >= n.keepAlive {
				n.sendKeepAlive()
				lastTx = now
			}
			if rx.active && now.Sub(rx.last) > n.rxTO {
				s.End(rx.call)
				rx = rxState{endedAt: now}
			}
		}
	}
}

func minDur(d ...time.Duration) time.Duration {
	m := d[0]
	for _, x := range d[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func (n *Net) newCall() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nextCall++
	return n.nextCall
}

// startRX emits the header of a received call.
func (n *Net) startRX(s network.Sink, rx *rxState, h network.CallHeader, now time.Time) {
	if rx.active {
		s.End(rx.call)
	}
	h.Call = n.newCall()
	*rx = rxState{active: true, call: h.Call, src: h.SrcID, dst: h.TG, private: h.Private, last: now}
	s.Header(h)
}

func (n *Net) rxPacket(s network.Sink, rx *rxState, p packet, now time.Time) {
	switch p.typ {
	case typSuperHeader:
		// Not sent by the server; tolerated. It starts a call (with the
		// callsign) unless the voice header already did.
		sh, ok := parseSuper(p.payload)
		if !ok {
			return
		}
		if rx.active && rx.src == sh.src && rx.dst == sh.dst {
			rx.last = now
			return
		}
		n.startRX(s, rx, network.CallHeader{Src: sh.srcCall, SrcID: sh.src, TG: sh.dst, Private: sh.typ == superPrivate}, now)
	case typVoiceHeader:
		// A call starts from its voice header (sent three times); the IDs and
		// the private flag come from the LC.
		priv, dst, src, ok := parseLC(p.payload)
		if !ok {
			return
		}
		if rx.active && rx.src == src && rx.dst == dst {
			rx.last = now
			return
		}
		n.startRX(s, rx, network.CallHeader{SrcID: src, TG: dst, Private: priv}, now)
	case typTerminator:
		// It carries the same LC as the header; the call ends.
		if rx.active {
			s.End(rx.call)
			*rx = rxState{endedAt: now}
		}
	case typAudio:
		var fr [3]network.VoiceFrame
		switch len(p.payload) {
		case audioLen: // 3 x 72-bit FEC coded frames
			for i := range fr {
				var d dmr.AMBE72
				copy(d[:], p.payload[i*frame72:])
				a, _ := dmr.DecodeAMBE(d)
				fr[i].Data = a[:]
			}
		case frameLen3: // 3 x 49-bit linear frames
			for i := range fr {
				fr[i].Data = p.payload[i*frameLen : (i+1)*frameLen]
			}
		default:
			return
		}
		if !rx.active {
			if now.Sub(rx.endedAt) < strayGrace {
				return
			}
			// Joined mid-transmission (the headers were missed): the call
			// starts anyway, caller unknown, to the tuned talkgroup. The
			// terminator or the RX timeout ends it.
			var tg uint32
			n.mu.Lock()
			if n.sub != nil {
				tg = n.sub.tg
			}
			n.mu.Unlock()
			n.startRX(s, rx, network.CallHeader{TG: tg}, now)
		}
		rx.last = now
		for i := range fr {
			fr[i].Call, fr[i].Seq = rx.call, rx.seq
			s.Voice(fr[i])
			rx.seq++
		}
	}
}

// tuneLocked sends the tune sequence: an empty CANCELLING, then
// SUBSCRIPTION {7, tg}. The type is always 7, also for the parrot and private
// targets. The server echoes both with empty payloads.
func (n *Net) tuneLocked(tg uint32) error {
	if err := n.sendLocked(typCancelling, 0, nil, 1); err != nil {
		return err
	}
	return n.sendLocked(typSubscription, 0, u32s(sessionGroup, tg), 1)
}

// Tune subscribes to a talkgroup or a private target (the parrot, 9990).
func (n *Net) Tune(ctx context.Context, t ctl.Target) error {
	if t.TG == 0 {
		return errors.New("bm: tune needs a talkgroup")
	}
	// Private calls to targets other than the parrot are not confirmed by
	// the capture; they are handled like the parrot (subscribe {7, target},
	// TX voice header FLCO 0x03).
	sub := &subscription{tg: t.TG, private: t.Private || t.TG == ParrotTG}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sub = sub
	n.detail = n.detailLocked(nil)
	if n.logged {
		return n.tuneLocked(sub.tg)
	}
	return nil
}

// Unlink tunes TG 4000, BrandMeister's disconnect talkgroup. The state shows
// no target.
func (n *Net) Unlink(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sub = nil
	n.detail = n.detailLocked(nil)
	if n.logged {
		return n.tuneLocked(DisconnectTG)
	}
	return nil
}

// StartTX sends the voice header (three times, same sequence number). There
// is no super header.
func (n *Net) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.logged {
		return errors.New("bm: not logged in")
	}
	dst, private := h.TG, h.Private
	if dst == 0 && n.sub != nil {
		dst, private = n.sub.tg, n.sub.private
	}
	if dst == 0 {
		return errors.New("bm: no target to transmit to")
	}
	src := h.SrcID
	if src == 0 {
		src = n.id.DMRID
	}
	private = private || dst == ParrotTG
	n.txCall, n.txActive, n.txBuf, n.txDst = h.Call, true, n.txBuf[:0], dst
	// Three voice headers, each with its own sequence number (as captured).
	lc := voiceHeaderLC(private, dst, src)
	for i := 0; i < 3; i++ {
		if err := n.sendLocked(typVoiceHeader, flagRealTime1, lc, 1); err != nil {
			return err
		}
	}
	return nil
}

// WriteVoice FEC-encodes a 49-bit frame to 72 bits, collects them and sends
// every third as one 27 byte audio packet.
func (n *Net) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != frameLen {
		return fmt.Errorf("bm: frame must be %d bytes", frameLen)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.txActive || f.Call != n.txCall {
		return nil
	}
	var a dmr.AMBE
	copy(a[:], f.Data)
	e := dmr.EncodeAMBE(a)
	n.txBuf = append(n.txBuf, e[:]...)
	if len(n.txBuf) < audioLen {
		return nil
	}
	err := n.sendLocked(typAudio, flagRealTime1, n.txBuf[:audioLen], 1)
	n.txBuf = n.txBuf[:0]
	return err
}

// EndTX flushes a partial packet (padded with AMBE silence) and sends one
// terminator with an empty payload.
func (n *Net) EndTX(call uint64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.txActive || call != n.txCall {
		return nil
	}
	n.txActive = false
	if len(n.txBuf) > 0 {
		for len(n.txBuf) < audioLen {
			n.txBuf = append(n.txBuf, dmr.SilenceAMBE72[:]...)
		}
		n.sendLocked(typAudio, flagRealTime1, n.txBuf, 1)
		n.txBuf = n.txBuf[:0]
	}
	return n.sendLocked(typTerminator, flagRealTime1, nil, 1)
}
