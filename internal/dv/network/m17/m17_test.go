// SPDX-License-Identifier: GPL-3.0-or-later

package m17

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	proto "github.com/w1jar/nodespot/internal/dv/m17"
	"github.com/w1jar/nodespot/internal/dv/network"
)

// ---- fake reflector ----

type fakeRef struct {
	t    *testing.T
	conn *net.UDPConn
	mu   sync.Mutex
	mode string // "ack", "nack", "silent"
	ackD bool   // answer DISC with the 4-byte ack
	peer *net.UDPAddr
	got  chan []byte
}

func newFakeRef(t *testing.T) *fakeRef {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRef{t: t, conn: c, mode: "ack", ackD: true, got: make(chan []byte, 4096)}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			l, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			p := append([]byte(nil), buf[:l]...)
			f.mu.Lock()
			f.peer = from
			mode, ackD := f.mode, f.ackD
			f.mu.Unlock()
			if l == proto.ConnLen && string(p[:4]) == "CONN" {
				switch mode {
				case "ack":
					c.WriteToUDP([]byte("ACKN"), from)
				case "nack":
					c.WriteToUDP([]byte("NACK"), from)
				}
			}
			if l == proto.AddrLen && string(p[:4]) == "DISC" && ackD {
				c.WriteToUDP([]byte("DISC"), from)
			}
			select {
			case f.got <- p:
			default:
			}
		}
	}()
	return f
}

func (f *fakeRef) port() int { return f.conn.LocalAddr().(*net.UDPAddr).Port }

func (f *fakeRef) setMode(m string) {
	f.mu.Lock()
	f.mode = m
	f.mu.Unlock()
}

func (f *fakeRef) send(p []byte) {
	f.mu.Lock()
	peer := f.peer
	f.mu.Unlock()
	if peer == nil {
		f.t.Fatal("no peer yet")
	}
	f.conn.WriteToUDP(p, peer)
}

func (f *fakeRef) next(match func([]byte) bool, d time.Duration) []byte {
	f.t.Helper()
	tm := time.After(d)
	for {
		select {
		case p := <-f.got:
			if match(p) {
				return p
			}
		case <-tm:
			f.t.Fatal("expected packet not received")
			return nil
		}
	}
}

func kindIs(k string) func([]byte) bool {
	return func(p []byte) bool { return len(p) >= 4 && string(p[:4]) == k }
}

func (f *fakeRef) noneFor(k string, d time.Duration) bool {
	tm := time.After(d)
	for {
		select {
		case p := <-f.got:
			if kindIs(k)(p) {
				return false
			}
		case <-tm:
			return true
		}
	}
}

// ---- recording sink ----

type event struct {
	kind  string
	h     network.CallHeader
	f     network.VoiceFrame
	call  uint64
	login ctl.LoginState
	det   string
}

type sink struct{ ch chan event }

func newSink() *sink                        { return &sink{ch: make(chan event, 4096)} }
func (s *sink) Header(h network.CallHeader) { s.ch <- event{kind: "header", h: h} }
func (s *sink) Voice(f network.VoiceFrame)  { s.ch <- event{kind: "voice", f: f} }
func (s *sink) End(c uint64)                { s.ch <- event{kind: "end", call: c} }
func (s *sink) Login(l ctl.LoginState, d string) {
	s.ch <- event{kind: "login", login: l, det: d}
}

func (s *sink) wait(t *testing.T, match func(event) bool, d time.Duration) event {
	t.Helper()
	tm := time.After(d)
	for {
		select {
		case e := <-s.ch:
			if match(e) {
				return e
			}
		case <-tm:
			t.Fatal("timeout waiting for event")
			return event{}
		}
	}
}

func loginIs(st ctl.LoginState, sub string) func(event) bool {
	return func(e event) bool {
		return e.kind == "login" && e.login == st && strings.Contains(e.det, sub)
	}
}

func kind(k string) func(event) bool { return func(e event) bool { return e.kind == k } }

// ---- setup ----

var fastOpts = map[string]string{
	OptAckTimeoutMS: "30", OptTimeoutMS: "300", OptRxTimeoutMS: "80", OptTickMS: "5", OptDiscWaitMS: "200", OptCAN: "3",
}

type rig struct {
	n   *Net
	ref *fakeRef
	s   *sink
	ctx context.Context
}

func newRig(t *testing.T, mode string, opts map[string]string) *rig {
	t.Helper()
	ref := newFakeRef(t)
	ref.setMode(mode)
	if opts == nil {
		opts = fastOpts
	}
	n, err := New(ctl.NetConfig{Mode: ctl.ModeM17, Enabled: true, Options: opts}, ctl.Identity{Callsign: "N0CALL"})
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &rig{n: n, ref: ref, s: s, ctx: ctx}
}

func (r *rig) tune(t *testing.T, room string) {
	t.Helper()
	if err := r.n.Tune(r.ctx, ctl.Target{Room: room, Host: "127.0.0.1", Port: r.ref.port()}); err != nil {
		t.Fatal(err)
	}
}

func linked(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, "ack", nil)
	r.tune(t, "M17-XXX C")
	r.s.wait(t, loginIs(ctl.LoginOK, "M17-XXX C"), time.Second)
	return r
}

func me(t *testing.T) proto.Address {
	a, err := proto.EncodeCallsign("N0CALL")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// ---- link ----

func TestRegistered(t *testing.T) {
	if _, err := network.New(ctl.NetConfig{Mode: ctl.ModeM17}, ctl.Identity{Callsign: "N0CALL"}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctl.NetConfig{}, ctl.Identity{}); err == nil {
		t.Fatal("no callsign accepted")
	}
}

func TestConnectAckn(t *testing.T) {
	r := newRig(t, "ack", nil)
	if st := r.n.Status(); st.Login != ctl.LoginOff {
		t.Fatalf("status %+v", st)
	}
	r.tune(t, "M17-XXX C")
	p := r.ref.next(kindIs("CONN"), time.Second)
	c, err := proto.UnmarshalControl(p)
	if err != nil || c.Module != 'C' || c.Addr != me(t) {
		t.Fatalf("conn %+v %v", c, err)
	}
	r.s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	if st := r.n.Status(); st.Login != ctl.LoginOK || ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v", st)
	}
}

func TestNack(t *testing.T) {
	r := newRig(t, "nack", nil)
	r.tune(t, "M17-XXX C")
	r.s.wait(t, loginIs(ctl.LoginFailed, "NACK"), time.Second)
	r.ref.next(kindIs("CONN"), time.Second)
	for len(r.ref.got) > 0 {
		<-r.ref.got
	}
	if !r.ref.noneFor("CONN", 300*time.Millisecond) {
		t.Fatal("CONN resent after NACK")
	}
	if st := r.n.Status(); st.Login != ctl.LoginFailed {
		t.Fatalf("status %+v", st)
	}
	// a new tune tries again
	r.ref.setMode("ack")
	r.tune(t, "M17-XXX D")
	r.s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
}

func TestConnRetryBackoff(t *testing.T) {
	r := newRig(t, "silent", nil)
	r.tune(t, "M17-XXX C")
	var at []time.Time
	for i := 0; i < 4; i++ {
		r.ref.next(kindIs("CONN"), time.Second)
		at = append(at, time.Now())
	}
	if d1, d3 := at[2].Sub(at[1]), at[3].Sub(at[2]); d3 < d1 {
		t.Fatalf("no backoff: %v then %v", d1, d3)
	}
	e := r.s.wait(t, loginIs(ctl.LoginPending, "no reply from"), time.Second)
	if !ctl.NotAnswering(e.login, e.det) {
		t.Fatalf("%+v", e)
	}
	r.ref.setMode("ack")
	r.s.wait(t, loginIs(ctl.LoginOK, ""), 2*time.Second)
}

func TestPingPong(t *testing.T) {
	r := linked(t)
	ping := append([]byte("PING"), func() []byte { b := proto.Address(0x1234).Bytes(); return b[:] }()...)
	r.ref.send(ping)
	p := r.ref.next(kindIs("PONG"), time.Second)
	c, err := proto.UnmarshalControl(p)
	if err != nil || c.Addr != me(t) || len(p) != 10 {
		t.Fatalf("pong %x %v", p, err)
	}
}

func TestWatchdogReconnect(t *testing.T) {
	r := linked(t)
	r.ref.setMode("silent")
	e := r.s.wait(t, loginIs(ctl.LoginPending, "no reply from"), 2*time.Second)
	_ = e
	r.ref.next(kindIs("CONN"), time.Second)
	r.ref.setMode("ack")
	r.s.wait(t, loginIs(ctl.LoginOK, ""), 2*time.Second)
}

// PINGs keep the link past the watchdog.
func TestPingsKeepLink(t *testing.T) {
	r := linked(t)
	ping := append([]byte("PING"), make([]byte, 6)...)
	end := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(end) {
		r.ref.send(ping)
		time.Sleep(50 * time.Millisecond)
	}
	if st := r.n.Status(); st.Login != ctl.LoginOK {
		t.Fatalf("status %+v", st)
	}
}

func TestServerDisc(t *testing.T) {
	r := linked(t)
	b := proto.Address(0x1234).Bytes()
	r.ref.send(append([]byte("DISC"), b[:]...))
	r.s.wait(t, loginIs(ctl.LoginFailed, "disconnected by"), time.Second)
	for len(r.ref.got) > 0 {
		<-r.ref.got
	}
	if !r.ref.noneFor("CONN", 400*time.Millisecond) {
		t.Fatal("reconnected after being kicked")
	}
}

func TestClientUnlink(t *testing.T) {
	r := linked(t)
	t0 := time.Now()
	if err := r.n.Unlink(r.ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) > 150*time.Millisecond {
		t.Fatalf("unlink waited for the ack timeout: %v", time.Since(t0))
	}
	p := r.ref.next(kindIs("DISC"), time.Second)
	if c, err := proto.UnmarshalControl(p); err != nil || c.Short || c.Addr != me(t) {
		t.Fatalf("disc %x %v", p, err)
	}
	if st := r.n.Status(); st.Login != ctl.LoginOff {
		t.Fatalf("status %+v", st)
	}
}

func TestUnlinkNoAckTimesOut(t *testing.T) {
	r := linked(t)
	r.ref.mu.Lock()
	r.ref.ackD = false
	r.ref.mu.Unlock()
	t0 := time.Now()
	r.n.Unlink(r.ctx)
	if d := time.Since(t0); d < 150*time.Millisecond || d > time.Second {
		t.Fatalf("unlink took %v", d)
	}
	if st := r.n.Status(); st.Login != ctl.LoginOff {
		t.Fatalf("status %+v", st)
	}
}

func TestRetuneModule(t *testing.T) {
	r := linked(t)
	r.tune(t, "M17-XXX D")
	r.ref.next(kindIs("DISC"), time.Second)
	p := r.ref.next(func(p []byte) bool { return kindIs("CONN")(p) && p[10] == 'D' }, time.Second)
	if len(p) != proto.ConnLen {
		t.Fatalf("conn %x", p)
	}
	r.s.wait(t, loginIs(ctl.LoginOK, "M17-XXX D"), time.Second)
}

func TestTuneErrors(t *testing.T) {
	n, _ := New(ctl.NetConfig{}, ctl.Identity{Callsign: "N0CALL"})
	for _, tg := range []ctl.Target{
		{Room: "M17-XXX C"},                    // no host
		{Host: "192.0.2.1"},                    // no room, no module option
		{Room: "m17-xxx c", Host: "192.0.2.1"}, // bad room
		{Room: "M17-XXX C", Host: "192.0.2.1", Port: 70000},
	} {
		if err := n.Tune(context.Background(), tg); err == nil {
			t.Fatalf("tune %+v succeeded", tg)
		}
	}
}

func TestConfigFallbackTarget(t *testing.T) {
	ref := newFakeRef(t)
	opts := map[string]string{OptModule: "K", OptReflector: "M17-XXX", OptTickMS: "5"}
	n, err := New(ctl.NetConfig{Mode: ctl.ModeM17, Enabled: true, Address: "127.0.0.1", Port: ref.port(), Options: opts}, ctl.Identity{Callsign: "N0CALL"})
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(100 * time.Millisecond)
	if len(ref.got) != 0 {
		t.Fatal("linked at start")
	}
	if err := n.Tune(ctx, ctl.Target{}); err != nil {
		t.Fatal(err)
	}
	if p := ref.next(kindIs("CONN"), time.Second); p[10] != 'K' {
		t.Fatalf("conn %x", p)
	}
}

func TestShutdownSendsDisc(t *testing.T) {
	ref := newFakeRef(t)
	n, _ := New(ctl.NetConfig{Mode: ctl.ModeM17, Enabled: true, Options: fastOpts}, ctl.Identity{Callsign: "N0CALL"})
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	n.Tune(ctx, ctl.Target{Room: "M17-XXX C", Host: "127.0.0.1", Port: ref.port()})
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	cancel()
	<-done
	ref.next(kindIs("DISC"), time.Second)
}

// ---- RX ----

func rxPacket(sid uint16, fn uint16, a, b byte) proto.Stream {
	dst, _ := proto.EncodeCallsign("M17-XXX C")
	src, _ := proto.EncodeCallsign("N1CALL")
	ty, _ := proto.VoiceType(0)
	s := proto.Stream{StreamID: sid, FN: fn, LSF: proto.LSF{Dst: dst, Src: src, Type: ty}}
	for i := 0; i < 8; i++ {
		s.Payload[i], s.Payload[8+i] = a+byte(i), b+byte(i)
	}
	return s
}

func (r *rig) sendStream(s proto.Stream) {
	b := s.Marshal()
	r.ref.send(b[:])
}

func TestRxCall(t *testing.T) {
	r := linked(t)
	r.sendStream(rxPacket(0x4242, 0, 0x10, 0x20))
	h := r.s.wait(t, kind("header"), time.Second).h
	if h.Src != "N1CALL" || h.Room != "M17-XXX C" {
		t.Fatalf("header %+v", h)
	}
	r.sendStream(rxPacket(0x4242, 1, 0x30, 0x40))
	r.sendStream(rxPacket(0x4242, 2|proto.LastFrame, 0x50, 0x60))
	var got [][]byte
	for len(got) < 6 {
		e := r.s.wait(t, kind("voice"), time.Second)
		if e.f.Codec != network.CodecC2 || len(e.f.Data) != 8 || e.f.Call != h.Call || e.f.Seq != uint32(len(got)) {
			t.Fatalf("frame %+v", e.f)
		}
		got = append(got, e.f.Data)
	}
	for i, base := range []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60} {
		if got[i][0] != base || got[i][7] != base+7 {
			t.Fatalf("frame %d = %x", i, got[i])
		}
	}
	if e := r.s.wait(t, kind("end"), time.Second); e.call != h.Call {
		t.Fatalf("end %+v", e)
	}
	// next call gets a new id
	r.sendStream(rxPacket(0x4243, 0, 1, 2))
	if h2 := r.s.wait(t, kind("header"), time.Second).h; h2.Call == h.Call {
		t.Fatal("call id reused")
	}
}

func TestRxCRCErrorCounted(t *testing.T) {
	r := linked(t)
	s := rxPacket(7, 0, 1, 2)
	b := s.Marshal()
	b[53] ^= 0xFF
	r.ref.send(b[:])
	r.s.wait(t, kind("header"), time.Second)
	r.s.wait(t, kind("voice"), time.Second)
	if r.n.CRCErrors() != 1 {
		t.Fatalf("crc errors %d", r.n.CRCErrors())
	}
	// garbage of the right length and a short one are ignored
	r.ref.send(append([]byte("M17 "), make([]byte, 10)...))
	r.ref.send(bytes.Repeat([]byte{1}, 54))
	time.Sleep(50 * time.Millisecond)
}

func TestRxTimeoutEnd(t *testing.T) {
	r := linked(t)
	r.sendStream(rxPacket(9, 0, 1, 2))
	h := r.s.wait(t, kind("header"), time.Second).h
	e := r.s.wait(t, kind("end"), time.Second)
	if e.call != h.Call {
		t.Fatalf("end %+v", e)
	}
}

func TestRxDuplicateAndSecondStream(t *testing.T) {
	r := linked(t)
	r.sendStream(rxPacket(1, 0, 1, 2))
	r.sendStream(rxPacket(1, 0, 1, 2)) // duplicate
	r.sendStream(rxPacket(2, 0, 9, 9)) // other stream while the first is active
	r.s.wait(t, kind("header"), time.Second)
	n := 0
	for {
		select {
		case e := <-r.s.ch:
			if e.kind == "voice" {
				n++
			}
			if e.kind == "header" {
				t.Fatal("second stream started a call")
			}
			continue
		case <-time.After(40 * time.Millisecond):
		}
		break
	}
	if n < 1 {
		t.Fatal("no voice")
	}
}

func TestRxIgnoredWhenNotLinked(t *testing.T) {
	r := newRig(t, "silent", nil)
	r.tune(t, "M17-XXX C")
	r.ref.next(kindIs("CONN"), time.Second)
	r.sendStream(rxPacket(1, 0, 1, 2))
	select {
	case e := <-r.s.ch:
		if e.kind == "header" || e.kind == "voice" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(60 * time.Millisecond):
	}
}

func TestRxEncryptedIgnored(t *testing.T) {
	r := linked(t)
	s := rxPacket(5, 0, 1, 2)
	s.LSF.Type |= 1 << 3
	r.sendStream(s)
	select {
	case e := <-r.s.ch:
		if e.kind == "header" {
			t.Fatal("encrypted stream played")
		}
	case <-time.After(60 * time.Millisecond):
	}
}

// ---- TX ----

func c2(i int) []byte {
	b := make([]byte, 8)
	for k := range b {
		b[k] = byte(i*8 + k + 1)
	}
	return b
}

func (r *rig) collect(t *testing.T, n int) []proto.Stream {
	t.Helper()
	var out []proto.Stream
	for len(out) < n {
		p := r.ref.next(proto.IsStream, time.Second)
		s, err := proto.UnmarshalStream(p)
		if err != nil {
			t.Fatalf("packet: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func TestTxFramingOdd(t *testing.T) {
	r := linked(t)
	if err := r.n.StartTX(network.CallHeader{Call: 1, Src: "N0CALL"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		r.n.WriteVoice(network.VoiceFrame{Call: 1, Codec: network.CodecC2, Data: c2(i)})
	}
	r.n.EndTX(1)
	ps := r.collect(t, 3)
	dst, _ := proto.EncodeCallsign("M17-XXX C")
	ty, _ := proto.VoiceType(3)
	for i, s := range ps {
		if s.StreamID == 0 || s.StreamID != ps[0].StreamID {
			t.Fatalf("stream id %x", s.StreamID)
		}
		if s.LSF.Dst != dst || s.LSF.Src != me(t) || s.LSF.Type != ty || s.LSF.Meta != [14]byte{} {
			t.Fatalf("lsf %+v", s.LSF)
		}
		if s.Counter() != uint16(i) || s.Last() != (i == 2) {
			t.Fatalf("fn[%d] = %#x", i, s.FN)
		}
	}
	if !bytes.Equal(ps[0].Payload[:8], c2(0)) || !bytes.Equal(ps[1].Payload[8:], c2(3)) || !bytes.Equal(ps[2].Payload[:8], c2(4)) {
		t.Fatal("payload mismatch")
	}
	if !bytes.Equal(ps[2].Payload[8:], Silence[:]) {
		t.Fatalf("pad %x", ps[2].Payload[8:])
	}
	// a second call has a fresh stream id and FN restarts
	r.n.StartTX(network.CallHeader{Call: 2})
	r.n.WriteVoice(network.VoiceFrame{Call: 2, Data: c2(0)})
	r.n.WriteVoice(network.VoiceFrame{Call: 2, Data: c2(1)})
	r.n.EndTX(2)
	p2 := r.collect(t, 1)[0]
	if p2.Counter() != 0 || !p2.Last() {
		t.Fatalf("fn %#x", p2.FN)
	}
}

func TestTxEvenAndEmpty(t *testing.T) {
	r := linked(t)
	r.n.StartTX(network.CallHeader{Call: 1})
	for i := 0; i < 4; i++ {
		r.n.WriteVoice(network.VoiceFrame{Call: 1, Data: c2(i)})
	}
	r.n.EndTX(1)
	ps := r.collect(t, 2)
	if ps[0].Last() || !ps[1].Last() || ps[1].Counter() != 1 {
		t.Fatalf("fn %#x %#x", ps[0].FN, ps[1].FN)
	}
	r.n.StartTX(network.CallHeader{Call: 2})
	r.n.EndTX(2)
	if p := r.collect(t, 1)[0]; !p.Last() {
		t.Fatalf("fn %#x", p.FN)
	}
}

func TestTxErrors(t *testing.T) {
	n, _ := New(ctl.NetConfig{}, ctl.Identity{Callsign: "N0CALL"})
	if err := n.StartTX(network.CallHeader{Call: 1}); err == nil {
		t.Fatal("StartTX while unlinked")
	}
	r := linked(t)
	r.n.StartTX(network.CallHeader{Call: 1})
	if err := r.n.WriteVoice(network.VoiceFrame{Call: 1, Data: make([]byte, 7)}); err == nil {
		t.Fatal("short frame accepted")
	}
	if err := r.n.WriteVoice(network.VoiceFrame{Call: 9, Data: c2(0)}); err != nil {
		t.Fatal(err)
	}
}

func TestSuffixAndBroadcastDst(t *testing.T) {
	ref := newFakeRef(t)
	opts := map[string]string{OptSuffix: "B", OptModule: "C", OptTickMS: "5"}
	n, err := New(ctl.NetConfig{Mode: ctl.ModeM17, Enabled: true, Options: opts}, ctl.Identity{Callsign: "n0call"})
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	n.Tune(ctx, ctl.Target{Host: "127.0.0.1", Port: ref.port()})
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	c, _ := proto.UnmarshalControl(ref.next(kindIs("CONN"), time.Second))
	if c.Addr.String() != "N0CALL B" {
		t.Fatalf("callsign %q", c.Addr)
	}
	n.StartTX(network.CallHeader{Call: 1})
	n.EndTX(1)
	st, _ := proto.UnmarshalStream(ref.next(proto.IsStream, time.Second))
	if st.LSF.Dst != proto.AddrBroadcast || st.LSF.Src.String() != "N0CALL B" {
		t.Fatalf("lsf %+v", st.LSF)
	}
}

// A stream the client joins mid-call (the first packet seen has a frame
// number above 0) starts a call from the LSF every packet carries, and the
// frames before it arriving late do not start a second one.
func TestRxLateEntry(t *testing.T) {
	r := linked(t)
	r.sendStream(rxPacket(0x5151, 7, 0x10, 0x20))
	h := r.s.wait(t, kind("header"), time.Second).h
	if h.Src != "N1CALL" || h.Room != "M17-XXX C" {
		t.Fatalf("header %+v", h)
	}
	r.sendStream(rxPacket(0x5151, 8, 0x30, 0x40))
	r.sendStream(rxPacket(0x5151, 3, 0x50, 0x60)) // an earlier packet, late
	r.sendStream(rxPacket(0x5151, 9|proto.LastFrame, 0x70, 0x80))
	for n := 0; n < 6; n++ {
		if e := r.s.wait(t, kind("voice"), time.Second); e.f.Call != h.Call {
			t.Fatalf("frame %+v", e.f)
		}
	}
	if e := r.s.wait(t, kind("end"), time.Second); e.call != h.Call {
		t.Fatalf("end %+v", e)
	}
	select {
	case e := <-r.s.ch:
		t.Fatalf("extra event %+v", e)
	case <-time.After(40 * time.Millisecond):
	}
}
