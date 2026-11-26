package nxdn

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	dvn "github.com/w1jar/nodespot/internal/dv/nxdn"
)

type sink struct {
	mu     sync.Mutex
	hdrs   []network.CallHeader
	voice  []network.VoiceFrame
	ends   []uint64
	logins []ctl.LoginState
}

func (s *sink) Header(h network.CallHeader) { s.mu.Lock(); s.hdrs = append(s.hdrs, h); s.mu.Unlock() }
func (s *sink) Voice(f network.VoiceFrame)  { s.mu.Lock(); s.voice = append(s.voice, f); s.mu.Unlock() }
func (s *sink) End(c uint64)                { s.mu.Lock(); s.ends = append(s.ends, c); s.mu.Unlock() }
func (s *sink) Login(st ctl.LoginState, _ string) {
	s.mu.Lock()
	s.logins = append(s.logins, st)
	s.mu.Unlock()
}
func (s *sink) counts() (h, v, e int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hdrs), len(s.voice), len(s.ends)
}
func (s *sink) hasLogin(st ctl.LoginState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.logins {
		if x == st {
			return true
		}
	}
	return false
}

// reflector is an in-process fake reflector.
type reflector struct {
	c      *net.UDPConn
	mu     sync.Mutex
	echo   bool
	pkts   [][]byte
	client *net.UDPAddr
}

func newReflector(t *testing.T, echo bool) *reflector {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	r := &reflector{c: c, echo: echo}
	go func() {
		buf := make([]byte, 200)
		for {
			l, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			p := append([]byte(nil), buf[:l]...)
			r.mu.Lock()
			r.pkts = append(r.pkts, p)
			r.client = from
			echo := r.echo
			r.mu.Unlock()
			if echo && l == pollLen && string(p[:5]) == "NXDNP" {
				c.WriteToUDP(p, from)
			}
		}
	}()
	t.Cleanup(func() { c.Close() })
	return r
}

func (r *reflector) setEcho(v bool) { r.mu.Lock(); r.echo = v; r.mu.Unlock() }
func (r *reflector) port() int      { return r.c.LocalAddr().(*net.UDPAddr).Port }
func (r *reflector) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.pkts {
		if len(p) >= 5 && string(p[:5]) == kind {
			n++
		}
	}
	return n
}
func (r *reflector) all(kind string) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]byte
	for _, p := range r.pkts {
		if len(p) >= 5 && string(p[:5]) == kind {
			out = append(out, p)
		}
	}
	return out
}
func (r *reflector) send(t *testing.T, p []byte) {
	r.mu.Lock()
	to := r.client
	r.mu.Unlock()
	if to == nil {
		t.Fatal("no client yet")
	}
	r.c.WriteToUDP(p, to)
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	dl := time.Now().Add(3 * time.Second)
	for time.Now().Before(dl) {
		if f() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func opts(extra map[string]string) map[string]string {
	o := map[string]string{
		OptID: "1234", OptListen: "127.0.0.1:0", OptPollMS: "60", OptMissedPolls: "3",
		OptBackoffMinMS: "10", OptBackoffMaxMS: "40", OptRxTimeoutMS: "80",
	}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

func start(t *testing.T, o map[string]string) (*Net, *sink, context.CancelFunc, chan struct{}) {
	n, err := New(ctl.NetConfig{Enabled: true, Options: o}, ctl.Identity{Callsign: "n0call"})
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	eventually(t, "run", func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.running })
	return n, s, cancel, done
}

func tune(t *testing.T, n *Net, r *reflector, tg int) {
	t.Helper()
	if err := n.Tune(context.Background(), ctl.Target{Host: "127.0.0.1", Port: r.port(), TG: uint32(tg)}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginSuccessAndPollFormat(t *testing.T) {
	r := newReflector(t, true)
	n, s, cancel, done := start(t, opts(nil))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1999)
	eventually(t, "login ok", func() bool { return n.Status().Login == ctl.LoginOK })
	if !s.hasLogin(ctl.LoginPending) {
		t.Fatal("no pending event")
	}
	p := r.all("NXDNP")[0]
	want := append([]byte("NXDNPN0CALL    "), 0x07, 0xCF)
	if string(p) != string(want) {
		t.Fatalf("poll %q want %q", p, want)
	}
	// The reflector can answer the first poll of the burst before the
	// other two have arrived.
	eventually(t, "poll burst", func() bool { return r.count("NXDNP") >= 3 })
	// periodic keepalive
	c0 := r.count("NXDNP")
	eventually(t, "keepalive", func() bool { return r.count("NXDNP") > c0 })
}

func TestRoomAsTGAndConfigAddress(t *testing.T) {
	r := newReflector(t, true)
	o := opts(nil)
	n, err := New(ctl.NetConfig{Address: "127.0.0.1", Port: r.port(), Options: o}, ctl.Identity{Callsign: "N0CALL"})
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	defer func() { cancel(); <-done }()
	eventually(t, "run", func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.running })
	if err := n.Tune(ctx, ctl.Target{Room: "9990"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })
	if err := n.Tune(ctx, ctl.Target{Room: "abc"}); err == nil {
		t.Fatal("bad room accepted")
	}
	if err := n.Tune(ctx, ctl.Target{TG: 70000, Host: "127.0.0.1"}); err == nil {
		t.Fatal("bad tg accepted")
	}
}

func TestLoginTimeoutBackoff(t *testing.T) {
	r := newReflector(t, false)
	n, _, cancel, done := start(t, opts(map[string]string{OptPollMS: "40"}))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1999)
	eventually(t, "failed", func() bool { return n.Status().Login == ctl.LoginFailed })
	// retries continue (burst of 3 + at least 3 more)
	eventually(t, "retries", func() bool { return r.count("NXDNP") >= 6 })
	// reflector appears: recovers
	r.setEcho(true)
	eventually(t, "recover", func() bool { return n.Status().Login == ctl.LoginOK })
}

func TestKeepaliveLossAndReconnect(t *testing.T) {
	r := newReflector(t, true)
	n, s, cancel, done := start(t, opts(nil))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1999)
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })
	r.setEcho(false)
	eventually(t, "pending", func() bool { return n.Status().Login == ctl.LoginPending })
	eventually(t, "failed", func() bool { return n.Status().Login == ctl.LoginFailed })
	r.setEcho(true)
	eventually(t, "ok again", func() bool { return n.Status().Login == ctl.LoginOK })
	if !s.hasLogin(ctl.LoginFailed) {
		t.Fatal("no failed event")
	}
}

func notAnswering(n *Net) bool { st := n.Status(); return ctl.NotAnswering(st.Login, st.Detail) }

// A reflector that never answers: after the grace (missed_polls * poll) the
// status says so, a reply clears it, a reflector that answers never shows it.
func TestNotAnswering(t *testing.T) {
	r := newReflector(t, false)
	n, _, cancel, done := start(t, opts(nil))
	defer func() { cancel(); <-done }()
	if notAnswering(n) {
		t.Fatal("not answering before any tune")
	}
	tune(t, n, r, 1999)
	eventually(t, "not answering", func() bool { return notAnswering(n) })
	r.setEcho(true)
	eventually(t, "cleared", func() bool { return n.Status().Login == ctl.LoginOK })
	if notAnswering(n) {
		t.Fatal("still not answering after a reply")
	}
	time.Sleep(300 * time.Millisecond) // well past the 180 ms grace
	if n.Status().Login != ctl.LoginOK || notAnswering(n) {
		t.Fatalf("answering reflector reported silent: %+v", n.Status())
	}
	r.setEcho(false) // goes silent later
	eventually(t, "silent again", func() bool { return notAnswering(n) })
	n.Unlink(context.Background())
	eventually(t, "unlinked", func() bool { return !notAnswering(n) })
}

func TestUnlinkAndRetune(t *testing.T) {
	r := newReflector(t, true)
	n, _, cancel, done := start(t, opts(nil))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1998)
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })
	tune(t, n, r, 1999)
	eventually(t, "unlink on retune", func() bool { return r.count("NXDNU") >= 3 })
	u := r.all("NXDNU")[0]
	if u[15] != 0x07 || u[16] != 0xCE { // 1998
		t.Fatalf("unlink TG % x", u[15:])
	}
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })
	if err := n.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second unlink", func() bool { return r.count("NXDNU") >= 6 })
	if n.Status().Login != ctl.LoginOff {
		t.Fatal("login not off")
	}
	c := r.count("NXDNP")
	time.Sleep(150 * time.Millisecond)
	if r.count("NXDNP") != c {
		t.Fatal("polling continued after unlink")
	}
}

func dgram(src, dst uint16, flags byte, pl [33]byte) []byte {
	n := &Net{}
	return n.dataPacket(src, dst, flags, pl)
}

func TestRxCall(t *testing.T) {
	r := newReflector(t, true)
	n, s, cancel, done := start(t, opts(nil))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1999)
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })

	l3 := dvn.NewVCall(4321, 1999, true)
	r.send(t, dgram(4321, 1999, dvn.FlagGroup|dvn.FlagStart, dvn.BuildHeader(l3, 0)))
	var fr [4][7]byte
	for i := range fr {
		fr[i] = [7]byte{byte(i + 1), 2, 3, 4, 5, 6, 0x80}
	}
	r.send(t, dgram(4321, 1999, dvn.FlagGroup, dvn.BuildVoice(l3, 0, 0, fr)))
	r.send(t, dgram(4321, 1999, dvn.FlagGroup, dvn.BuildVoice(l3, 0, 1, fr)))
	r.send(t, dgram(4321, 1999, dvn.FlagGroup|dvn.FlagEnd, dvn.BuildHeader(l3.TxRel(), 0)))
	eventually(t, "end", func() bool { _, _, e := s.counts(); return e == 1 })
	h, v, _ := s.counts()
	if h != 1 || v != 8 {
		t.Fatalf("headers %d voice %d", h, v)
	}
	if s.hdrs[0].Src != "4321" || s.hdrs[0].TG != 1999 || s.hdrs[0].Private {
		t.Fatalf("header %+v", s.hdrs[0])
	}
	if string(s.voice[0].Data) != string(fr[0][:]) || s.voice[5].Seq != 5 {
		t.Fatalf("voice %+v", s.voice[0])
	}
	// wrong destination ignored
	r.send(t, dgram(4321, 1998, dvn.FlagGroup|dvn.FlagStart, dvn.BuildHeader(l3, 0)))
	time.Sleep(50 * time.Millisecond)
	if h2, _, _ := s.counts(); h2 != 1 {
		t.Fatal("foreign TG accepted")
	}
}

func TestRxLostEndAndLateEntry(t *testing.T) {
	r := newReflector(t, true)
	n, s, cancel, done := start(t, opts(nil))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1999)
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })
	l3 := dvn.NewVCall(4321, 1999, true)
	var fr [4][7]byte
	r.send(t, dgram(4321, 1999, dvn.FlagGroup, dvn.BuildVoice(l3, 0, 2, fr))) // no header: late entry
	eventually(t, "lost end", func() bool { h, v, e := s.counts(); return h == 1 && v == 4 && e == 1 })
}

func TestTxFraming(t *testing.T) {
	r := newReflector(t, true)
	n, _, cancel, done := start(t, opts(map[string]string{OptRAN: "5"}))
	defer func() { cancel(); <-done }()
	tune(t, n, r, 1999)
	eventually(t, "ok", func() bool { return n.Status().Login == ctl.LoginOK })
	if err := n.StartTX(network.CallHeader{Call: 7}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ { // one full packet + 2 frames
		if err := n.WriteVoice(network.VoiceFrame{Call: 7, Data: []byte{byte(i), 1, 2, 3, 4, 5, 0x80}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.WriteVoice(network.VoiceFrame{Call: 7, Data: []byte{1}}); err == nil {
		t.Fatal("short frame accepted")
	}
	if err := n.EndTX(7); err != nil {
		t.Fatal(err)
	}
	eventually(t, "4 data packets", func() bool { return r.count("NXDND") == 4 })
	d := r.all("NXDND")
	for _, p := range d {
		if len(p) != dataLen || p[5] != 0x04 || p[6] != 0xD2 || p[7] != 0x07 || p[8] != 0xCF {
			t.Fatalf("bad datagram header % x", p[:10])
		}
	}
	flags := []byte{d[0][9], d[1][9], d[2][9], d[3][9]}
	if string(flags) != string([]byte{0x05, 0x01, 0x01, 0x09}) {
		t.Fatalf("flags % x", flags)
	}
	pr, err := dvn.ParsePayload(d[1][10:])
	if err != nil || pr.Kind != dvn.KindVoice || pr.Frames[0][0] != 0 || pr.Frames[3][0] != 3 {
		t.Fatalf("voice 1 %+v %v", pr, err)
	}
	if pr.SACCH.RAN() != 5 {
		t.Fatalf("ran %d", pr.SACCH.RAN())
	}
	pr, _ = dvn.ParsePayload(d[2][10:])
	if pr.Frames[0][0] != 4 || pr.Frames[1][0] != 5 {
		t.Fatalf("partial packet %+v", pr.Frames)
	}
	if pr, _ = dvn.ParsePayload(d[0][10:]); pr.Kind != dvn.KindVCall || pr.L3.Src() != 1234 || pr.L3.Dst() != 1999 {
		t.Fatalf("header %+v", pr)
	}
	if pr, _ = dvn.ParsePayload(d[3][10:]); pr.Kind != dvn.KindTxRel {
		t.Fatalf("trailer %+v", pr)
	}
}

func TestTxNeedsIDAndLink(t *testing.T) {
	o := opts(nil)
	delete(o, OptID)
	n, err := New(ctl.NetConfig{Options: o}, ctl.Identity{Callsign: "N0CALL"})
	if err != nil {
		t.Fatal(err)
	}
	if n.StartTX(network.CallHeader{Call: 1}) == nil {
		t.Fatal("tx while unlinked")
	}
	if _, err := New(ctl.NetConfig{Options: map[string]string{OptID: "0"}}, ctl.Identity{Callsign: "N0CALL"}); err == nil {
		t.Fatal("id 0 accepted")
	}
	if _, err := New(ctl.NetConfig{}, ctl.Identity{}); err == nil {
		t.Fatal("empty callsign accepted")
	}
	_ = strconv.Itoa
}

func TestRegistered(t *testing.T) {
	if _, err := network.New(ctl.NetConfig{Mode: ctl.ModeNXDN}, ctl.Identity{Callsign: "N0CALL"}); err != nil {
		t.Fatal(err)
	}
}
