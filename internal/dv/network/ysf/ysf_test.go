// SPDX-License-Identifier: GPL-3.0-or-later

package ysf

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	air "github.com/w1jar/nodespot/internal/dv/ysf"
)

// ---- fake reflector ----

type fakeRef struct {
	t     *testing.T
	conn  *net.UDPConn
	mu    sync.Mutex
	reply bool
	peer  *net.UDPAddr
	got   chan []byte
}

func newFakeRef(t *testing.T) *fakeRef {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRef{t: t, conn: c, reply: true, got: make(chan []byte, 1024)}
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
			r := f.reply
			f.mu.Unlock()
			if r && string(p[:4]) == "YSFP" {
				c.WriteToUDP([]byte("YSFPREF TEST "), from)
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

func (f *fakeRef) setReply(r bool) {
	f.mu.Lock()
	f.reply = r
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

// next returns the next received packet whose type is kind.
func (f *fakeRef) next(kind string, d time.Duration) []byte {
	f.t.Helper()
	tm := time.After(d)
	for {
		select {
		case p := <-f.got:
			if string(p[:4]) == kind {
				return p
			}
		case <-tm:
			f.t.Fatalf("no %s packet", kind)
			return nil
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
	OptPollMS: "20", OptTimeoutMS: "120", OptRxTimeoutMS: "80", OptTickMS: "5",
}

func start(t *testing.T) (*Net, *fakeRef, *sink) {
	t.Helper()
	return startReply(t, true)
}

// startReply tunes to a fake reflector that answers polls only when reply is set.
func startReply(t *testing.T, reply bool) (*Net, *fakeRef, *sink) {
	t.Helper()
	ref := newFakeRef(t)
	ref.setReply(reply)
	n, err := New(ctl.NetConfig{Mode: ctl.ModeYSF, Enabled: true, Options: fastOpts},
		ctl.Identity{Callsign: "N0CALL", DMRID: 1234567})
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := n.Tune(ctx, ctl.Target{Room: "12345", Host: "127.0.0.1", Port: ref.port()}); err != nil {
		t.Fatal(err)
	}
	return n, ref, s
}

func TestRegistered(t *testing.T) {
	if _, err := network.New(ctl.NetConfig{Mode: ctl.ModeYSF}, ctl.Identity{Callsign: "N0CALL"}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginPollUnlink(t *testing.T) {
	n, ref, s := start(t)
	p := ref.next("YSFP", time.Second)
	if string(p) != "YSFPN0CALL    " {
		t.Fatalf("poll %q", p)
	}
	s.wait(t, loginIs(ctl.LoginOK, "12345"), time.Second)
	if st := n.Status(); st.Login != ctl.LoginOK || st.Mode != ctl.ModeYSF {
		t.Fatalf("status %+v", st)
	}
	ref.next("YSFP", time.Second) // keepalive continues

	n.Unlink(context.Background())
	for i := 0; i < 3; i++ {
		if u := ref.next("YSFU", time.Second); string(u) != "YSFUN0CALL    " {
			t.Fatalf("unlink %q", u)
		}
	}
	s.wait(t, loginIs(ctl.LoginOff, ""), time.Second)
}

func TestTimeoutBackoffReconnect(t *testing.T) {
	_, ref, s := start(t)
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	ref.setReply(false)
	s.wait(t, loginIs(ctl.LoginPending, "retrying"), time.Second)
	// Polls back off: the gaps grow.
	for len(ref.got) > 0 {
		<-ref.got
	}
	ref.next("YSFP", time.Second)
	t0 := time.Now()
	ref.next("YSFP", time.Second)
	g1 := time.Since(t0)
	t0 = time.Now()
	ref.next("YSFP", 2*time.Second)
	g2 := time.Since(t0)
	if g2 <= g1 {
		t.Errorf("no backoff: %v then %v", g1, g2)
	}
	ref.setReply(true)
	s.wait(t, loginIs(ctl.LoginOK, ""), 3*time.Second)
}

// A reflector that never answers: after the grace (timeout_ms) the login says so.
func TestNotAnsweringAfterTune(t *testing.T) {
	n, ref, s := startReply(t, false)
	e := s.wait(t, loginIs(ctl.LoginPending, "no reply from"), time.Second)
	if !ctl.NotAnswering(e.login, e.det) {
		t.Fatalf("event %+v is not 'not answering'", e)
	}
	if st := n.Status(); !ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v", st)
	}
	ref.setReply(true) // a reply clears it
	s.wait(t, loginIs(ctl.LoginOK, ""), 3*time.Second)
	if st := n.Status(); ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v after reply", st)
	}
}

// A reflector that answers never reports it, and nothing tuned never does.
func TestAnsweringNeverNotAnswering(t *testing.T) {
	n, _, s := start(t)
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	time.Sleep(300 * time.Millisecond) // well past the 120 ms grace
	if st := n.Status(); st.Login != ctl.LoginOK || ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v", st)
	}
	n.Unlink(context.Background())
	if st := n.Status(); ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("unlinked: %+v", st)
	}
}

func TestTuneNoHost(t *testing.T) {
	n, _ := New(ctl.NetConfig{}, ctl.Identity{Callsign: "N0CALL"})
	if err := n.Tune(context.Background(), ctl.Target{Room: "12345"}); err == nil {
		t.Fatal("tune without host succeeded")
	}
}

// ---- RX ----

func refPacket(src string, end bool, cnt uint8) []byte {
	p := make([]byte, PacketLen)
	copy(p, "YSFD")
	copy(p[4:], pad("REF"))
	copy(p[14:], pad(src))
	copy(p[24:], pad("ALL"))
	p[34] = cnt << 1
	if end {
		p[34] |= 1
	}
	copy(p[hdrLen:], air.Sync[:])
	return p
}

func headerPacket(fi uint8, src string, end bool) []byte {
	p := refPacket("NETSRC", end, 0)
	fr := p[hdrLen:]
	air.FICH{FI: fi, CS: 2, FT: 6, DT: air.DTVDMode2}.Encode(fr)
	var c1, c2 [20]byte
	copy(c1[:], pad("ALL")+pad(src))
	copy(c2[:], strings.Repeat(" ", 20))
	air.WriteCSD(fr, c1, c2)
	return p
}

func ambe(i int) [7]byte {
	var a [7]byte
	for k := range a {
		a[k] = byte(i*31 + k*7 + 1)
	}
	a[6] &= 0x80 // 49 bits
	return a
}

func commPacket(fn uint8, base int) []byte {
	p := refPacket("NETSRC", false, fn+1)
	fr := p[hdrLen:]
	air.FICH{FI: air.FICommunications, CS: 2, FN: fn, FT: 6, DT: air.DTVDMode2}.Encode(fr)
	for j := 0; j < 5; j++ {
		air.WriteVCH(fr, j, ambe(base+j))
	}
	return p
}

func TestRXCall(t *testing.T) {
	_, ref, s := start(t)
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	ref.send(headerPacket(air.FIHeader, "N0CALL", false))
	h := s.wait(t, kind("header"), time.Second).h
	if h.Src != "N0CALL" || h.Room != "12345" {
		t.Fatalf("header %+v", h)
	}
	ref.send(commPacket(0, 0))
	ref.send(commPacket(1, 5))
	for i := 0; i < 10; i++ {
		f := s.wait(t, kind("voice"), time.Second).f
		want := ambe(i)
		if f.Call != h.Call || f.Seq != uint32(i) || !bytes.Equal(f.Data, want[:]) {
			t.Fatalf("frame %d: %+v want %x", i, f, want)
		}
	}
	ref.send(headerPacket(air.FITerminator, "N0CALL", true))
	if e := s.wait(t, kind("end"), time.Second); e.call != h.Call {
		t.Fatalf("end %d", e.call)
	}
}

func TestRXLostEnd(t *testing.T) {
	_, ref, s := start(t)
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	ref.send(commPacket(0, 0)) // late entry: no header
	h := s.wait(t, kind("header"), time.Second).h
	if h.Src != "NETSRC" {
		t.Fatalf("src %q", h.Src)
	}
	t0 := time.Now()
	if e := s.wait(t, kind("end"), time.Second); e.call != h.Call {
		t.Fatalf("end %d", e.call)
	}
	if d := time.Since(t0); d < 60*time.Millisecond {
		t.Errorf("ended after %v", d)
	}
}

func TestRXVoiceFR(t *testing.T) {
	_, ref, s := start(t)
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	p := refPacket("NETSRC", false, 1)
	fr := p[hdrLen:]
	air.FICH{FI: air.FICommunications, CS: 2, FN: 2, FT: 6, DT: air.DTVoiceFRMode}.Encode(fr)
	var v [11]byte
	for j := 0; j < 5; j++ {
		for k := range v {
			v[k] = byte(j*13 + k*5)
		}
		v[10] &= 0xFE // u7 is 7 bits
		air.WriteVW(fr, j, v)
	}
	ref.send(p)
	s.wait(t, kind("header"), time.Second)
	for j := 0; j < 5; j++ {
		f := s.wait(t, kind("voice"), time.Second).f
		if len(f.Data) != 11 || f.Data[0] != byte(j*13) {
			t.Fatalf("vw %d: %x", j, f.Data)
		}
	}
}

// ---- TX ----

func TestTXFraming(t *testing.T) {
	n, ref, s := start(t)
	s.wait(t, loginIs(ctl.LoginOK, ""), time.Second)
	if err := n.WriteVoice(network.VoiceFrame{Data: make([]byte, 11)}); err == nil {
		t.Fatal("11-byte frame accepted")
	}
	if err := n.StartTX(network.CallHeader{Call: 7, Src: "n0call"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		a := ambe(i)
		n.WriteVoice(network.VoiceFrame{Call: 7, Seq: uint32(i), Data: a[:]})
	}
	n.EndTX(7)

	check := func(p []byte, cnt uint8, end bool) []byte {
		t.Helper()
		if len(p) != PacketLen || string(p[4:14]) != "N0CALL    " || string(p[14:24]) != "N0CALL    " ||
			string(p[24:34]) != "ALL       " || p[34] != cnt<<1|b(end) || !bytes.Equal(p[35:40], air.Sync[:]) {
			t.Fatalf("packet %d: %q", cnt, p[:35])
		}
		return p[hdrLen:]
	}
	fr := check(ref.next("YSFD", time.Second), 0, false)
	f, ok := air.DecodeFICH(fr)
	if !ok || f.FI != air.FIHeader || f.DT != air.DTVDMode2 || f.CS != 2 || f.FT != 6 {
		t.Fatalf("header fich %+v %v", f, ok)
	}
	c1, ok1, _, ok2 := air.ReadCSD(fr)
	if !ok1 || !ok2 || string(c1[:]) != "ALL       N0CALL    " {
		t.Fatalf("csd %q", c1)
	}
	for k := 0; k < 2; k++ {
		fr = check(ref.next("YSFD", time.Second), uint8(k+1), false)
		f, ok = air.DecodeFICH(fr)
		if !ok || f.FI != air.FICommunications || f.FN != uint8(k) {
			t.Fatalf("comm fich %+v", f)
		}
		d, ok := air.ReadDCH(fr)
		want := []string{"ALL       ", "N0CALL    "}[k]
		if !ok || string(d[:]) != want {
			t.Fatalf("dch %q", d)
		}
		for j := 0; j < 5; j++ {
			want := air.AMBESilence
			if i := k*5 + j; i < 7 {
				want = ambe(i)
			}
			if a, _ := air.ReadVCH(fr, j); a != want {
				t.Fatalf("vch %d/%d: %x want %x", k, j, a, want)
			}
		}
	}
	fr = check(ref.next("YSFD", time.Second), 3, true)
	if f, ok = air.DecodeFICH(fr); !ok || f.FI != air.FITerminator {
		t.Fatalf("term fich %+v", f)
	}
}

func b(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// A stored address is not linked at start (the config no longer carries one):
// nothing is sent until the first Tune.
func TestNoLinkAtStart(t *testing.T) {
	ref := newFakeRef(t)
	n, err := New(ctl.NetConfig{Mode: ctl.ModeYSF, Enabled: true, Address: "127.0.0.1", Port: ref.port(), Options: fastOpts},
		ctl.Identity{Callsign: "N0CALL", DMRID: 1234567})
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(150 * time.Millisecond)
	if len(ref.got) != 0 {
		t.Fatal("linked at start")
	}
	if err := n.Tune(ctx, ctl.Target{Room: "12345", Host: "127.0.0.1", Port: ref.port()}); err != nil {
		t.Fatal(err)
	}
	ref.next("YSFP", time.Second)
}
