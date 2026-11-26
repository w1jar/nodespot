package homebrew

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
)

// TGIF defaults (public master).
const (
	TGIFAddress = ctl.TGIFAddress
	TGIFPort    = ctl.TGIFPort

	softwareID = "nodespot"
	packageVer = "nodespot-0.0.1"
)

func init() {
	network.Register(ctl.ModeTGIF, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
	network.Register(ctl.ModeDMR, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg, id)
	})
}

type stage int

const (
	stLogin stage = iota // sent RPTL, want RPTACK(salt)
	stAuth               // sent RPTK, want RPTACK
	stConf               // sent RPTC, want RPTACK
	stOpts               // sent RPTO, want RPTACK
	stReady
)

// Net is one Homebrew master login.
type Net struct {
	cfg  ctl.NetConfig
	mode string
	addr string
	rid  uint32
	call string
	cc   int
	info rptcInfo
	opts string

	kerchunk   bool
	unlinkTG   uint32
	pingEvery  time.Duration
	stepTO     time.Duration
	bkMin      time.Duration
	bkMax      time.Duration
	rxTO       time.Duration
	missedMax  int
	srcDefault uint32
	rxAnySlot  bool // deliver calls of both slots (DMR - Other; TGIF filters on slot)

	relogin chan struct{} // a planned reconnect (DMR - Other retune), capacity 1

	kcMu     sync.Mutex // serialises kerchunks (Tune/Unlink vs the session's relink)
	mu       sync.Mutex
	conn     net.Conn
	sink     network.Sink
	login    ctl.LoginState
	detail   string
	ready    bool
	slot     int
	target   *ctl.Target
	rxCall   *rxState
	rxTimer  *time.Timer
	txCall   *txState
	nextCall uint64
	stream   uint32
	seq      byte
	runCtx   context.Context
}

type rxState struct {
	stream uint32
	slot   int
	id     uint64
	seq    uint32
	rx     *dmr.RX
}

type txState struct {
	call    uint64
	stream  uint32
	src     uint32
	dst     uint32
	private bool
	tx      *dmr.TX
}

// New builds a Homebrew client for TGIF or DMR.
func New(cfg ctl.NetConfig, id ctl.Identity) (*Net, error) {
	n := &Net{cfg: cfg, mode: cfg.Mode, login: ctl.LoginOff, nextCall: 1 << 40, relogin: make(chan struct{}, 1)}
	o := cfg.Options
	host, port := cfg.Address, cfg.Port
	if n.mode == ctl.ModeTGIF {
		if host == "" {
			host = TGIFAddress
		}
		if port == 0 {
			port = TGIFPort
		}
	}
	if n.mode == ctl.ModeDMR && n.cfg.Password == "" {
		n.cfg.Password = ctl.DefaultHomebrewPassword
	}
	if host == "" {
		return nil, errors.New("homebrew: address required")
	}
	if port == 0 {
		port = 62031
	}
	n.addr = net.JoinHostPort(host, strconv.Itoa(port))
	n.rid = uint32(optInt(o, "repeater_id", 0))
	if n.rid == 0 {
		n.rid = id.RepeaterID
	}
	if n.rid == 0 {
		n.rid = id.DMRID
	}
	if n.rid == 0 {
		return nil, errors.New("homebrew: repeater id required")
	}
	n.srcDefault = id.DMRID
	n.call = id.Callsign
	n.slot = optInt(o, "slot", 2)
	if n.mode == ctl.ModeTGIF {
		// TGIF is always timeslot 2 with no master options; a saved or sent
		// slot or options value has no effect.
		n.slot = 2
	}
	if n.slot != 1 && n.slot != 2 {
		return nil, fmt.Errorf("homebrew: bad slot %d", n.slot)
	}
	n.cc = optInt(o, "colour_code", 1)
	if n.mode != ctl.ModeTGIF {
		n.opts = o["options"]
	}
	n.kerchunk = optBool(o, "tune_kerchunk", true)
	n.unlinkTG = uint32(optInt(o, "unlink_tg", 4000))
	n.pingEvery = optMS(o, "ping_interval_ms", 5000)
	n.stepTO = optMS(o, "login_timeout_ms", 5000)
	n.bkMin = optMS(o, "backoff_min_ms", 1000)
	n.bkMax = optMS(o, "backoff_max_ms", 60000)
	n.rxTO = optMS(o, "rx_timeout_ms", 1500)
	n.rxAnySlot = optBool(o, "rx_any_slot", n.mode == ctl.ModeDMR)
	n.missedMax = optInt(o, "missed_pongs", 3)
	n.stream = rand.Uint32()
	n.info = rptcInfo{
		ID: n.rid, Callsign: id.Callsign,
		RxFreq: uint32(optInt(o, "rx_freq", 0)), TxFreq: uint32(optInt(o, "tx_freq", 0)),
		Power: 1, ColourCode: n.cc, Location: o["location"], Description: o["description"],
		Slots: '4', URL: o["url"], Software: softwareID, Package: packageVer,
	}
	return n, nil
}

func optInt(o map[string]string, k string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(o[k])); err == nil {
		return v
	}
	return def
}

func optBool(o map[string]string, k string, def bool) bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(o[k])); err == nil {
		return v
	}
	return def
}

func optMS(o map[string]string, k string, defMS int) time.Duration {
	v := optInt(o, k, defMS)
	if v <= 0 {
		v = defMS
	}
	return time.Duration(v) * time.Millisecond
}

func (n *Net) Mode() string         { return n.mode }
func (n *Net) Codec() network.Codec { return network.CodecAMBE }

// Status never includes the password.
func (n *Net) Status() ctl.NetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ctl.NetStatus{Mode: n.mode, Available: true, Enabled: n.cfg.Enabled, Login: n.login, Detail: n.detail}
}

func (n *Net) setLogin(s ctl.LoginState, d string) {
	n.mu.Lock()
	n.login, n.detail = s, d
	sink := n.sink
	n.mu.Unlock()
	if sink != nil {
		sink.Login(s, d)
	}
}

// Run logs in and keeps the session alive until ctx ends.
func (n *Net) Run(ctx context.Context, s network.Sink) error {
	n.mu.Lock()
	n.sink, n.runCtx = s, ctx
	n.mu.Unlock()
	bk := n.bkMin
	for ctx.Err() == nil {
		ok, err := n.session(ctx)
		if ctx.Err() != nil {
			break
		}
		if ok { // was logged in: restart the backoff
			bk = n.bkMin
		}
		if errors.Is(err, errPlanned) {
			// the operator retuned: log in again at once, quietly
			bk = n.bkMin
			continue
		}
		if errors.Is(err, errClosed) {
			n.setLogin(ctl.LoginFailed, "closed by master")
		} else if err != nil {
			n.setLogin(ctl.LoginFailed, err.Error())
		}
		t := time.NewTimer(bk)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
		if bk *= 2; bk > n.bkMax {
			bk = n.bkMax
		}
	}
	n.setLogin(ctl.LoginOff, "")
	n.mu.Lock()
	n.sink = nil
	n.mu.Unlock()
	return nil
}

var errClosed = errors.New("closed")

// errPlanned ends a session for a planned reconnect.
var errPlanned = errors.New("planned reconnect")

func (n *Net) send(b []byte) error {
	n.mu.Lock()
	c := n.conn
	n.mu.Unlock()
	if c == nil {
		return errors.New("homebrew: not connected")
	}
	_, err := c.Write(b)
	return err
}

func (n *Net) idPkt(tag string) []byte { return append([]byte(tag), be32(n.rid)...) }

// session is one connect+login+keepalive cycle. ok reports whether login
// completed.
func (n *Net) session(ctx context.Context) (ok bool, err error) {
	n.setLogin(ctl.LoginPending, "")
	select { // a request made before this login is moot: the target is set already
	case <-n.relogin:
	default:
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", n.addr)
	if err != nil {
		return false, err
	}
	n.mu.Lock()
	n.conn = c
	n.mu.Unlock()
	pkts := make(chan []byte, 128)
	rctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 2048)
		for {
			m, err := c.Read(buf)
			if err != nil {
				if rctx.Err() != nil {
					return
				}
				// ICMP unreachable etc: keep reading, timeouts handle it.
				time.Sleep(10 * time.Millisecond)
				continue
			}
			p := append([]byte(nil), buf[:m]...)
			select {
			case pkts <- p:
			case <-rctx.Done():
				return
			}
		}
	}()
	defer func() {
		if ok || n.isReady() {
			_ = n.send(n.idPkt("RPTCL"))
		}
		n.mu.Lock()
		n.conn, n.ready = nil, false
		n.txCall = nil
		n.mu.Unlock()
		n.endRX(0)
		stop()
		c.Close()
		wg.Wait()
	}()

	_ = n.send(n.idPkt("RPTL"))
	st := stLogin
	deadline := time.NewTimer(n.stepTO)
	defer deadline.Stop()
	var ping *time.Ticker
	var pingC <-chan time.Time
	defer func() {
		if ping != nil {
			ping.Stop()
		}
	}()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return ok, nil
		case <-deadline.C:
			if st != stReady {
				return ok, fmt.Errorf("login timeout")
			}
		case <-n.relogin:
			if st == stReady {
				return ok, errPlanned
			}
		case <-pingC:
			if missed >= n.missedMax {
				return ok, errors.New("keepalive lost")
			}
			missed++
			_ = n.send(n.idPkt("RPTPING"))
		case p := <-pkts:
			switch {
			case hasTag(p, "MSTNAK"):
				if st == stReady {
					return ok, errors.New("master requested relogin")
				}
				return ok, errors.New("login rejected")
			case hasTag(p, "MSTCL"):
				return ok, errClosed
			case hasTag(p, "MSTPONG"):
				missed = 0
			case hasTag(p, "RPTACK") && st != stReady:
				next, perr := n.advance(st, p)
				if perr != nil {
					return ok, perr
				}
				st = next
				resetTimer(deadline, n.stepTO)
				if st == stReady {
					ok = true
					n.mu.Lock()
					n.ready = true
					n.mu.Unlock()
					n.setLogin(ctl.LoginOK, "")
					ping = time.NewTicker(n.pingEvery)
					pingC = ping.C
					deadline.Stop()
					go n.relink()
				}
			case len(p) >= 4 && string(p[:4]) == "DMRD":
				n.handleDMRD(p)
			}
		}
	}
}

func (n *Net) isReady() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ready
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func hasTag(p []byte, tag string) bool { return len(p) >= len(tag) && string(p[:len(tag)]) == tag }

// advance handles a RPTACK in stage st and sends the next login packet.
func (n *Net) advance(st stage, p []byte) (stage, error) {
	switch st {
	case stLogin:
		if len(p) < 10 {
			return st, errors.New("bad challenge")
		}
		h := sha256.New()
		h.Write(p[6:10])
		h.Write([]byte(n.cfg.Password))
		pkt := append(n.idPkt("RPTK"), h.Sum(nil)...)
		return stAuth, n.send(pkt)
	case stAuth:
		info := n.info
		n.mu.Lock()
		info.Slots = n.slotsByte()
		n.mu.Unlock()
		return stConf, n.send(marshalRPTC(info))
	case stConf:
		if n.opts != "" {
			return stOpts, n.send(append(n.idPkt("RPTO"), n.opts...))
		}
		return stReady, nil
	case stOpts:
		return stReady, nil
	}
	return st, nil
}

// slotsByte: '4' (simplex/hotspot, both slot values accepted by masters).
func (n *Net) slotsByte() byte { return n.info.Slots }

// ---- DMR packets ----

// nextSeq numbers the packets of a stream from 0, as MMDVMHost does.
func (n *Net) nextSeq() byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.seq
	n.seq++
	return s
}

// newStream starts a stream: a new non-zero stream ID and sequence number 0.
func (n *Net) newStream() uint32 {
	n.stream++
	if n.stream == 0 {
		n.stream++
	}
	n.seq = 0
	return n.stream
}

func (n *Net) sendDMRD(d dmrd) error {
	d.Repeater = n.rid
	d.Seq = n.nextSeq()
	return n.send(d.marshal())
}

func (n *Net) failIfNotReady() error {
	if !n.ready {
		return errors.New("homebrew: not logged in")
	}
	return nil
}

// kerchunkTo sends a header and terminator to tg.
func (n *Net) kerchunkTo(tg uint32, slot int, private bool) error {
	// One kerchunk at a time: otherwise two streams share the sequence
	// counter and their frames interleave on the wire.
	n.kcMu.Lock()
	defer n.kcMu.Unlock()
	n.mu.Lock()
	if err := n.failIfNotReady(); err != nil {
		n.mu.Unlock()
		return err
	}
	src := n.srcDefault
	if src == 0 {
		src = n.rid
	}
	tx := n.newTXLocked(0, src, tg, private)
	n.mu.Unlock()
	if err := n.sendFrames(*tx, slot, tx.tx.Start()); err != nil {
		return err
	}
	return n.sendFrames(*tx, slot, tx.tx.End())
}

// relink re-applies the tuned TG after a (re)login: the master forgets
// dynamic TGs when the session ends.
func (n *Net) relink() {
	n.mu.Lock()
	t := n.target
	slot := n.slot
	n.mu.Unlock()
	if t != nil && n.kerchunk {
		_ = n.kerchunkTo(t.TG, slot, t.Private)
	}
}

// Tune remembers the target and, if tune_kerchunk, links the dynamic TG by
// sending a short header+terminator to it.
func (n *Net) Tune(ctx context.Context, t ctl.Target) error {
	n.mu.Lock()
	old := n.target
	tt := t
	n.target = &tt
	if t.Slot == 1 || t.Slot == 2 {
		n.slot = t.Slot
	}
	slot := n.slot
	// DMR+ keeps a talkgroup the operator activated for about 15 minutes and
	// ignores the unlink for it; a fresh login drops it. So a retune from one
	// talkgroup to another logs in again, and relink() tunes the new one.
	reconnect := n.mode == ctl.ModeDMR && n.ready && old != nil && !old.Private && old.TG != 0 &&
		!t.Private && t.TG != old.TG
	n.mu.Unlock()
	if reconnect {
		select {
		case n.relogin <- struct{}{}:
		default:
		}
		return nil
	}
	if !n.kerchunk {
		return nil
	}
	if err := n.kerchunkTo(t.TG, slot, t.Private); err != nil && n.isReady() {
		return err
	}
	return nil // not logged in yet: relink() does it after login
}

// Unlink forgets the target and sends a kerchunk to unlink_tg (default 4000).
func (n *Net) Unlink(ctx context.Context) error {
	n.mu.Lock()
	n.target = nil
	slot := n.slot
	n.mu.Unlock()
	if n.unlinkTG == 0 || !n.isReady() {
		return nil
	}
	return n.kerchunkTo(n.unlinkTG, slot, false)
}
