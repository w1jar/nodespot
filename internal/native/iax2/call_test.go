package iax2

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type dialResult struct {
	c   *Call
	err error
}

func startDial(t *testing.T, p *fakePeer, clk *fakeClock, mod func(*Config)) chan dialResult {
	t.Helper()
	cfg := Config{
		LocalNode: "1999", RemoteNode: "2000", Addr: p.addr,
		Secret: "s3cret", Callsign: "N0CALL", Clock: clk,
	}
	if mod != nil {
		mod(&cfg)
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, err := Dial(context.Background(), cfg)
		ch <- dialResult{c, err}
	}()
	return ch
}

func waitDial(t *testing.T, ch chan dialResult) dialResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(waitLong):
		t.Fatal("dial did not return")
		return dialResult{}
	}
}

// answer runs ACCEPT and ANSWER after NEW and turns on auto mode.
func answer(t *testing.T, p *fakePeer, ch chan dialResult) *Call {
	t.Helper()
	var ies IEs
	ies.AddU32(IEFormat, FormatULaw)
	ies.AddFormat2(IEFormat2, uint64(FormatULaw))
	p.sendIEs(CmdAccept, ies)
	p.send(TypeControl, CtlAnswer, nil)
	p.setAuto(true)
	r := waitDial(t, ch)
	if r.err != nil {
		t.Fatalf("dial: %v", r.err)
	}
	t.Cleanup(func() {
		select {
		case <-r.c.Done():
		default:
			go r.c.Hangup()
		}
	})
	return r.c
}

// simpleCall dials without token or auth and answers.
func simpleCall(t *testing.T, mod func(*Config)) (*Call, *fakePeer, *fakeClock) {
	t.Helper()
	p := newFakePeer(t)
	clk := newFakeClock()
	ch := startDial(t, p, clk, mod)
	p.acceptNew(p.expect(TypeIAX, CmdNew))
	c := answer(t, p, ch)
	p.expectText(TextNewKey1)
	return c, p, clk
}

func nextEvent(t *testing.T, c *Call, kind EventKind) Event {
	t.Helper()
	deadline := time.After(waitLong)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatal("events closed")
			}
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("no event kind %d", kind)
		}
	}
}

func waitDone(t *testing.T, c *Call) error {
	t.Helper()
	select {
	case <-c.Done():
		return c.Err()
	case <-time.After(waitLong):
		t.Fatal("call did not end")
		return nil
	}
}

func ulawOf(v int16, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = ULawEncode(v)
	}
	return b
}

func TestCallFullSession(t *testing.T) {
	p := newFakePeer(t)
	clk := newFakeClock()
	ch := startDial(t, p, clk, nil)

	// Call token round: the first NEW carries an empty token.
	n1 := p.expect(TypeIAX, CmdNew)
	ies, err := ParseIEs(n1.Data)
	if err != nil {
		t.Fatal(err)
	}
	if tok, ok := ies.Get(IECallToken); !ok || len(tok) != 0 {
		t.Fatalf("first NEW token %q %v", tok, ok)
	}
	var tokIE IEs
	tokIE.AddString(IECallToken, "1997-token")
	tb, _ := tokIE.Encode()
	p.raw(FullFrame{SrcCall: 1, DstCall: n1.SrcCall, TS: n1.TS, ISeq: 1, Type: TypeIAX, Subclass: CmdCallToken, Data: tb})

	n2 := p.expect(TypeIAX, CmdNew)
	if n2.OSeq != 0 || n2.ISeq != 0 || n2.SrcCall != n1.SrcCall || n2.DstCall != 0 {
		t.Fatalf("second NEW %v", n2)
	}
	ies, _ = ParseIEs(n2.Data)
	want := map[uint8]string{
		IECalledNumber: "2000", IECallingNumber: "1999", IEUsername: "radio",
		IECallingName: "N0CALL", IECallToken: "1997-token",
	}
	for ie, v := range want {
		if got := ies.String(ie); got != v {
			t.Errorf("NEW IE %d = %q, want %q", ie, got, v)
		}
	}
	if v, _ := ies.U16(IEVersion); v != 2 {
		t.Errorf("version %d", v)
	}
	if f, _ := ies.Format(); f != uint64(FormatULaw) {
		t.Errorf("format %x", f)
	}
	if c, _ := ies.U32(IECapability); c != FormatULaw {
		t.Errorf("capability %x", c)
	}
	p.acceptNew(n2)

	// MD5 auth.
	var auth IEs
	auth.AddU16(IEAuthMethods, AuthMD5|AuthPlaintext)
	auth.AddString(IEChallenge, "123456789")
	auth.AddString(IEUsername, "radio")
	p.sendIEs(CmdAuthReq, auth)
	rep := p.expect(TypeIAX, CmdAuthRep)
	ies, _ = ParseIEs(rep.Data)
	sum := md5.Sum([]byte("123456789s3cret"))
	if got := ies.String(IEMD5Result); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("md5 result %q", got)
	}
	if _, ok := ies.Get(IEPassword); ok {
		t.Fatal("password sent although MD5 was offered")
	}
	p.mu.Lock()
	p.iseq = rep.OSeq + 1
	p.mu.Unlock()

	c := answer(t, p, ch)
	p.expectText(TextNewKey1)
	if port := c.LocalAddr().Port(); port == 0 || port == DefaultPort {
		t.Fatalf("bound port %d", port)
	}

	// Peer handshake text and link list.
	p.send(TypeText, 0, []byte(TextNewKey1+"\x00"))
	p.send(TypeText, 0, []byte("L T2001,R2002\x00"))
	ev := nextEvent(t, c, EventLinkList)
	wantLinks := []LinkEntry{{ModeTransceive, "2001"}, {ModeReceive, "2002"}}
	if !reflect.DeepEqual(ev.Links, wantLinks) || !reflect.DeepEqual(c.Links(), wantLinks) {
		t.Fatalf("links %v / %v", ev.Links, c.Links())
	}
	p.send(TypeText, 0, []byte("M 2000 0 hello\x00"))
	if ev := nextEvent(t, c, EventText); ev.Text.Kind != TextKindMessage || ev.Text.Body != "hello" {
		t.Fatalf("text event %+v", ev)
	}

	// Our empty link list goes out shortly after answer.
	clk.Advance(200 * time.Millisecond)
	p.expectText("L ")

	// Far side keys up: one full voice frame, then mini frames.
	p.sendTS(TypeVoice, FormatULaw, 70000, ulawOf(1000, 160))
	p.mini(uint16(70020&0xffff), ulawOf(2000, 160))
	p.mini(uint16(70040&0xffff), ulawOf(3000, 160))
	if ev := nextEvent(t, c, EventRxKey); !ev.Keyed {
		t.Fatal("not keyed")
	}
	if !c.RxKeyed() {
		t.Fatal("RxKeyed false")
	}
	var got []int16
	for i := 0; i < 200 && len(got) < 3; i++ {
		clk.Advance(FrameDuration)
		select {
		case f := <-c.Recv():
			got = append(got, f[0])
		case <-time.After(250 * time.Millisecond):
		}
	}
	wantV := []int16{ULawDecode(ULawEncode(1000)), ULawDecode(ULawEncode(2000)), ULawDecode(ULawEncode(3000))}
	if !reflect.DeepEqual(got, wantV) {
		t.Fatalf("played %v, want %v", got, wantV)
	}
	p.send(TypeControl, CtlRadioUnkey, nil)
	if ev := nextEvent(t, c, EventRxKey); ev.Keyed {
		t.Fatal("still keyed")
	}

	// We key up: full voice frame first, then mini frames, then UNKEY.
	var tx Frame
	for i := range tx {
		tx[i] = 500
	}
	c.Send(tx)
	c.Send(tx)
	if err := c.Key(true); err != nil {
		t.Fatal(err)
	}
	v := p.expect(TypeVoice, FormatULaw)
	if v.TS%20 != 0 || len(v.Data) != 160 || ULawDecode(v.Data[0]) != ULawDecode(ULawEncode(500)) {
		t.Fatalf("voice full %v", v)
	}
	clk.Advance(FrameDuration)
	m := p.expectMini()
	if uint32(m.TS) != (v.TS+20)&0xffff || ULawDecode(m.Data[5]) != ULawDecode(ULawEncode(500)) {
		t.Fatalf("mini ts %d after %d", m.TS, v.TS)
	}
	clk.Advance(FrameDuration)
	if m := p.expectMini(); m.Data[0] != 0xff {
		t.Fatalf("idle keyed frame is not silence: %x", m.Data[0])
	}
	if err := c.Key(false); err != nil {
		t.Fatal(err)
	}
	p.expect(TypeControl, CtlRadioUnkey)
	p.sync() // let our UNKEY be acknowledged before the clock jumps

	// Keepalives: LAGRQ at 10 s, PING at 20 s, answered by the peer.
	clk.Advance(10 * time.Second)
	p.expect(TypeIAX, CmdLagRq)
	p.sync() // let the LAGRP arrive before the clock jumps again
	clk.Advance(10 * time.Second)
	p.expect(TypeIAX, CmdPing)

	// Peer's PING and LAGRQ are answered with their timestamps.
	p.sendTS(TypeIAX, CmdPing, 123456, nil)
	if f := p.expect(TypeIAX, CmdPong); f.TS != 123456 {
		t.Fatalf("pong ts %d", f.TS)
	}
	p.sendTS(TypeIAX, CmdLagRq, 123999, nil)
	if f := p.expect(TypeIAX, CmdLagRp); f.TS != 123999 {
		t.Fatalf("lagrp ts %d", f.TS)
	}

	// Hangup waits for the peer's ACK.
	if err := c.Hangup(); err != nil {
		t.Fatal(err)
	}
	h := p.expect(TypeIAX, CmdHangup)
	ies, _ = ParseIEs(h.Data)
	if ies.String(IECause) == "" {
		t.Error("hangup without cause")
	}
	if err := waitDone(t, c); err != nil {
		t.Fatalf("err after hangup: %v", err)
	}
	if _, ok := <-c.Recv(); ok {
		t.Fatal("recv not closed")
	}
	s := c.Stats()
	if s.TxVoice != 3 || s.RxVoice != 3 {
		t.Fatalf("stats %+v", s)
	}
}

func TestDialRetransmitsThenTimesOut(t *testing.T) {
	p := newFakePeer(t)
	clk := newFakeClock()
	ch := startDial(t, p, clk, func(c *Config) { c.DialTimeout = time.Minute })
	n := p.expect(TypeIAX, CmdNew)
	if n.Retrans {
		t.Fatal("first NEW marked as retransmission")
	}
	clk.Advance(initialRTO)
	r := p.expect(TypeIAX, CmdNew)
	if !r.Retrans || r.OSeq != n.OSeq || r.TS != n.TS {
		t.Fatalf("retransmission %v of %v", r, n)
	}
	clk.Advance(30 * time.Second)
	res := waitDial(t, ch)
	if !errors.Is(res.err, ErrTimeout) {
		t.Fatalf("err %v", res.err)
	}
}

func TestDialTimeoutWithoutAnswer(t *testing.T) {
	p := newFakePeer(t)
	clk := newFakeClock()
	ch := startDial(t, p, clk, func(c *Config) { c.DialTimeout = 5 * time.Second })
	p.acceptNew(p.expect(TypeIAX, CmdNew))
	p.setAuto(true)
	var ies IEs
	ies.AddU32(IEFormat, FormatULaw)
	p.sendIEs(CmdAccept, ies) // never answered
	time.Sleep(50 * time.Millisecond)
	clk.Advance(6 * time.Second)
	if res := waitDial(t, ch); !errors.Is(res.err, ErrTimeout) {
		t.Fatalf("err %v", res.err)
	}
	p.expect(TypeIAX, CmdHangup)
}

func TestDialFailures(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(*Config)
		reply func(p *fakePeer, n *FullFrame)
		want  error
		text  string
	}{
		{
			name: "reject before call exists",
			reply: func(p *fakePeer, n *FullFrame) {
				var ies IEs
				ies.AddString(IECause, "No authority found")
				b, _ := ies.Encode()
				p.raw(FullFrame{SrcCall: 0, DstCall: n.SrcCall, TS: n.TS, ISeq: 1, Type: TypeIAX, Subclass: CmdReject, Data: b})
			},
			want: ErrRejected, text: "No authority found",
		},
		{
			name: "reject after auth",
			reply: func(p *fakePeer, n *FullFrame) {
				var ies IEs
				ies.AddString(IECause, "Bad secret")
				ies.AddU8(IECauseCode, 21)
				p.sendIEs(CmdReject, ies)
			},
			want: ErrRejected, text: "Bad secret (cause code 21)",
		},
		{
			name: "auth without secret",
			mod:  func(c *Config) { c.Secret = "" },
			reply: func(p *fakePeer, n *FullFrame) {
				var ies IEs
				ies.AddU16(IEAuthMethods, AuthMD5)
				ies.AddString(IEChallenge, "1")
				p.sendIEs(CmdAuthReq, ies)
			},
			want: ErrAuth,
		},
		{
			name: "RSA only",
			reply: func(p *fakePeer, n *FullFrame) {
				var ies IEs
				ies.AddU16(IEAuthMethods, AuthRSA)
				p.sendIEs(CmdAuthReq, ies)
			},
			want: ErrAuth,
		},
		{
			name: "gsm chosen",
			reply: func(p *fakePeer, n *FullFrame) {
				var ies IEs
				ies.AddU32(IEFormat, FormatGSM)
				p.sendIEs(CmdAccept, ies)
			},
			want: ErrFormat,
		},
		{
			name: "busy",
			reply: func(p *fakePeer, n *FullFrame) {
				p.send(TypeControl, CtlBusy, nil)
			},
			want: ErrRejected,
		},
		{
			name: "token loop",
			reply: func(p *fakePeer, n *FullFrame) {
				var ies IEs
				ies.AddString(IECallToken, "x")
				b, _ := ies.Encode()
				for i := 0; i < 3; i++ {
					p.raw(FullFrame{SrcCall: 1, DstCall: n.SrcCall, TS: n.TS, ISeq: 1, Type: TypeIAX, Subclass: CmdCallToken, Data: b})
					if i < 2 {
						p.expect(TypeIAX, CmdNew)
					}
				}
			},
			want: ErrRejected,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakePeer(t)
			ch := startDial(t, p, newFakeClock(), tc.mod)
			n := p.expect(TypeIAX, CmdNew)
			p.acceptNew(n)
			tc.reply(p, n)
			res := waitDial(t, ch)
			if !errors.Is(res.err, tc.want) {
				t.Fatalf("err %v, want %v", res.err, tc.want)
			}
			if tc.text != "" && !strings.Contains(res.err.Error(), tc.text) {
				t.Fatalf("err %q lacks %q", res.err, tc.text)
			}
		})
	}
}

func TestPlaintextAuthOnlyWhenDemanded(t *testing.T) {
	p := newFakePeer(t)
	ch := startDial(t, p, newFakeClock(), nil)
	p.acceptNew(p.expect(TypeIAX, CmdNew))
	var ies IEs
	ies.AddU16(IEAuthMethods, AuthPlaintext)
	p.sendIEs(CmdAuthReq, ies)
	rep := p.expect(TypeIAX, CmdAuthRep)
	got, _ := ParseIEs(rep.Data)
	if got.String(IEPassword) != "s3cret" {
		t.Fatalf("password %q", got.String(IEPassword))
	}
	if _, ok := got.Get(IEMD5Result); ok {
		t.Fatal("md5 sent")
	}
	p.mu.Lock()
	p.iseq = rep.OSeq + 1
	p.mu.Unlock()
	answer(t, p, ch)
}

func TestDialContextCancel(t *testing.T) {
	p := newFakePeer(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		_, err := Dial(ctx, Config{LocalNode: "1999", RemoteNode: "2000", Addr: p.addr, Clock: newFakeClock()})
		ch <- err
	}()
	p.acceptNew(p.expect(TypeIAX, CmdNew))
	cancel()
	select {
	case err := <-ch:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(waitLong):
		t.Fatal("dial did not return")
	}
	p.expect(TypeIAX, CmdHangup)
}

func TestDialValidates(t *testing.T) {
	cases := []Config{
		{LocalNode: "", RemoteNode: "2000"},
		{LocalNode: "1999", RemoteNode: "20x0"},
		{LocalNode: "1999", RemoteNode: "2000"}, // no address
	}
	for i, cfg := range cases {
		if _, err := Dial(context.Background(), cfg); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestPeerHangup(t *testing.T) {
	c, p, _ := simpleCall(t, nil)
	var ies IEs
	ies.AddString(IECause, "Normal clearing")
	ies.AddU8(IECauseCode, 16)
	p.sendIEs(CmdHangup, ies)
	err := waitDone(t, c)
	if !errors.Is(err, ErrHungUp) || !strings.Contains(err.Error(), "Normal clearing (cause code 16)") {
		t.Fatalf("err %v", err)
	}
	if a := p.expect(TypeIAX, CmdAck); a.DstCall != p.call {
		t.Fatalf("ack %v", a)
	}
}

func TestPeerDisconnectText(t *testing.T) {
	c, p, clk := simpleCall(t, nil)
	p.send(TypeText, 0, []byte(TextDisconnect+"\x00"))
	p.expect(TypeIAX, CmdHangup)
	// The call ends when our HANGUP is acknowledged (the peer auto-acks) or,
	// failing that, once the hangup linger runs out. Under CI load the ACK
	// can lose the race, and the fake clock never reaches the linger on its
	// own, so give the ACK a moment and then let the linger end the call.
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		clk.Advance(hangupLinger + time.Second)
	}
	if err := waitDone(t, c); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("err %v", err)
	}
}

func TestDuplicateFrameAckedOnce(t *testing.T) {
	c, p, _ := simpleCall(t, nil)
	p.mu.Lock()
	f := FullFrame{SrcCall: p.call, DstCall: p.remote, TS: 5000, OSeq: p.oseq, ISeq: p.iseq,
		Type: TypeText, Data: []byte("M 2000 0 once\x00")}
	p.oseq++
	p.mu.Unlock()
	p.raw(f)
	f.Retrans = true
	p.raw(f)
	if ev := nextEvent(t, c, EventText); ev.Text.Body != "once" {
		t.Fatalf("event %+v", ev)
	}
	// Both copies are acknowledged.
	for i := 0; i < 2; i++ {
		if a := p.expect(TypeIAX, CmdAck); a.TS != 5000 {
			t.Fatalf("ack ts %d", a.TS)
		}
	}
	p.send(TypeText, 0, []byte("M 2000 0 next\x00"))
	if ev := nextEvent(t, c, EventText); ev.Text.Body != "next" {
		t.Fatalf("duplicate delivered: %+v", ev)
	}
}

func TestGapSendsVNAK(t *testing.T) {
	_, p, _ := simpleCall(t, nil)
	p.mu.Lock()
	p.oseq++ // skip one sequence number
	p.mu.Unlock()
	p.send(TypeText, 0, []byte("M 2000 0 x\x00"))
	p.expect(TypeIAX, CmdVNAK)
}

func TestPeerVNAKRetransmits(t *testing.T) {
	c, p, _ := simpleCall(t, func(c *Config) {})
	p.setAuto(false) // leave our text unacknowledged
	if err := c.SendText("M 1999 0 hi"); err != nil {
		t.Fatal(err)
	}
	p.expectText("M 1999 0 hi")
	p.send(TypeIAX, CmdVNAK, nil)
	f := p.expect(TypeText, 0)
	if !f.Retrans {
		t.Fatalf("not a retransmission: %v", f)
	}
}

func TestPeerTimeout(t *testing.T) {
	c, p, clk := simpleCall(t, func(c *Config) { c.PingInterval = time.Hour; c.LagInterval = time.Hour; c.LinkListTime = time.Hour })
	clk.Advance(200 * time.Millisecond)
	p.expectText("L ") // acked automatically
	p.setAuto(false)
	// Advance in steps until the call gives up. A late ack of the link
	// list can refresh the peer's last-heard time after the first step, so
	// a single fixed advance is not guaranteed to cross the timeout.
	for i := 0; i < 20; i++ {
		clk.Advance(10 * time.Second)
		select {
		case <-c.Done():
			i = 20
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err := waitDone(t, c); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v", err)
	}
}

func TestRxKeyLinger(t *testing.T) {
	c, p, clk := simpleCall(t, func(c *Config) { c.RxLinger = 100 * time.Millisecond })
	p.sendTS(TypeVoice, FormatULaw, 1000, ulawOf(0, 160))
	if ev := nextEvent(t, c, EventRxKey); !ev.Keyed {
		t.Fatal("not keyed")
	}
	clk.Advance(120 * time.Millisecond)
	if ev := nextEvent(t, c, EventRxKey); ev.Keyed {
		t.Fatal("still keyed after linger")
	}
}

func TestRedundantKeying(t *testing.T) {
	c, p, clk := simpleCall(t, nil)
	p.send(TypeText, 0, []byte(TextNewKey+"\x00"))
	p.expectText(TextNewKey)
	// In this mode voice alone does not key; RADIO_KEY does.
	p.send(TypeControl, CtlRadioKey, nil)
	if ev := nextEvent(t, c, EventRxKey); !ev.Keyed {
		t.Fatal("not keyed")
	}
	if err := c.Key(true); err != nil {
		t.Fatal(err)
	}
	p.expect(TypeControl, CtlRadioKey)
	clk.Advance(redundantTxTime + FrameDuration)
	p.expect(TypeControl, CtlRadioKey) // repeated every 2 s
	if err := c.Key(false); err != nil {
		t.Fatal(err)
	}
	p.expect(TypeControl, CtlRadioUnkey)
}

func TestSetLinkList(t *testing.T) {
	c, p, clk := simpleCall(t, nil)
	if err := c.SetLinkList([]LinkEntry{{ModeTransceive, "1998"}}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(200 * time.Millisecond)
	p.expectText("L T1998")
	if err := c.SetLinkList([]LinkEntry{{ModeLocal, "1998"}}); err == nil {
		t.Fatal("bad mode accepted")
	}
}

func TestIncomingNewRefused(t *testing.T) {
	c, _, _ := simpleCall(t, nil)
	other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer other.Close()
	var ies IEs
	ies.AddString(IECalledNumber, "1999")
	b, _ := ies.Encode()
	n := FullFrame{SrcCall: 99, TS: 1, Type: TypeIAX, Subclass: CmdNew, Data: b}
	pkt, _ := n.Append(nil)
	if _, err := other.WriteToUDPAddrPort(pkt, c.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	other.SetReadDeadline(time.Now().Add(waitLong))
	buf := make([]byte, 1500)
	k, _, err := other.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := Decode(buf[:k])
	if err != nil || f == nil || f.Type != TypeIAX || f.Subclass != CmdReject || f.DstCall != 99 {
		t.Fatalf("reply %v %v", f, err)
	}
	if c.Stats().Rejected != 1 {
		t.Fatal("not counted")
	}
	select {
	case <-c.Done():
		t.Fatal("refusing a call ended ours")
	default:
	}
}

// peerBatch builds n text frames with consecutive sequence numbers
// starting at the peer's next one and advances the peer past them, without
// sending anything.
func peerBatch(p *fakePeer, n int) []FullFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	var fs []FullFrame
	for i := 0; i < n; i++ {
		fs = append(fs, FullFrame{SrcCall: p.call, DstCall: p.remote, TS: uint32(9000 + 20*i),
			OSeq: p.oseq, ISeq: p.iseq, Type: TypeText,
			Data: []byte(fmt.Sprintf("M 2000 0 m%d\x00", i))})
		p.oseq++
	}
	return fs
}

func waitStats(t *testing.T, c *Call, ok func(Stats) bool) Stats {
	t.Helper()
	deadline := time.Now().Add(waitLong)
	for {
		if st := c.Stats(); ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats never matched: %+v", c.Stats())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The field storm: a full frame the client cannot use (a subclass byte of
// 0xff) used to be dropped before sequence counting, so iseq never passed
// it, the peer retransmitted it and everything behind it forever, and the
// client answered each copy with a VNAK. Now it is counted and ACKed, the
// retransmitted copies (R bit set) are only ACKed, and voice flows.
func TestStormUnusableFrameThenVoice(t *testing.T) {
	c, p, clk := simpleCall(t, nil)
	p.send(TypeControl, SubclassNone, nil) // header-only, unusable
	p.send(TypeNull, 0, nil)
	p.send(TypeCNG, 3, []byte{1})
	p.send(TypeDTMFBeg, '5', nil)
	p.send(TypeText, 0, []byte(TextNewKey1+"\x00"))
	p.send(TypeText, 0, []byte("L T2001,R2002\x00"))
	var vfs []FullFrame
	p.mu.Lock()
	vf := FullFrame{SrcCall: p.call, DstCall: p.remote, TS: 70000, OSeq: p.oseq, ISeq: p.iseq,
		Type: TypeVoice, Subclass: FormatULaw, Data: ulawOf(1000, 160)}
	p.oseq++
	p.mu.Unlock()
	vfs = append(vfs, vf)
	p.raw(vf)
	if ev := nextEvent(t, c, EventLinkList); len(ev.Links) != 2 {
		t.Fatalf("links %v", ev.Links)
	}
	p.sync()

	// The peer's retransmit queue is not drained yet in this storm: replay
	// the voice frame with the R bit many times. Each is a duplicate.
	vf.Retrans = true
	for i := 0; i < 100; i++ {
		p.raw(vf)
	}
	p.sync()
	st := c.Stats()
	if st.VNAKSent != 0 || st.ParseErrors != 0 {
		t.Fatalf("VNAKs %d, parse errors %d", st.VNAKSent, st.ParseErrors)
	}
	if st.RxVoiceFull != 1 || st.RxVoice != 1 {
		t.Fatalf("voice frames: %+v", st)
	}
	var got []int16
	for i := 0; i < 200 && len(got) < 1; i++ {
		clk.Advance(FrameDuration)
		select {
		case f := <-c.Recv():
			got = append(got, f[0])
		case <-time.After(250 * time.Millisecond):
		}
	}
	if len(got) != 1 || got[0] != ULawDecode(ULawEncode(1000)) {
		t.Fatalf("played %v", got)
	}
}

// A frame that reaches us only as a retransmission (R bit set, the
// original was lost) still advances the sequence.
func TestRetransmitBitFramesAreCounted(t *testing.T) {
	c, p, _ := simpleCall(t, nil)
	for i, f := range peerBatch(p, 3) {
		f.Retrans = true
		p.raw(f)
		if ev := nextEvent(t, c, EventText); ev.Text.Body != fmt.Sprintf("m%d", i) {
			t.Fatalf("event %+v", ev)
		}
	}
	p.sync()
	if st := c.Stats(); st.VNAKSent != 0 || st.Dropped != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// Old frames are acknowledged with their timestamp and never VNAKed.
func TestDuplicatesGetAckNotVNAK(t *testing.T) {
	c, p, _ := simpleCall(t, nil)
	fs := peerBatch(p, 1)
	p.raw(fs[0])
	nextEvent(t, c, EventText)
	fs[0].Retrans = true
	const n = 40
	for i := 0; i < n; i++ {
		p.raw(fs[0])
	}
	acks := 0
	for acks < n+1 {
		if a := p.expect(TypeIAX, CmdAck); a.TS == fs[0].TS {
			acks++
		}
	}
	p.sync()
	if st := c.Stats(); st.VNAKSent != 0 || st.Dropped != n {
		t.Fatalf("stats %+v", st)
	}
}

// A gap earns one VNAK however many frames arrive behind it, a second one
// only after the rate limit, and the stream recovers in order once the
// missing frame arrives.
func TestGapVNAKBoundedAndRecovers(t *testing.T) {
	c, p, clk := simpleCall(t, nil)
	fs := peerBatch(p, 31) // fs[0] is the one that goes missing
	for _, f := range fs[1:] {
		p.raw(f)
	}
	st := waitStats(t, c, func(s Stats) bool { return s.Dropped >= 30 })
	if st.VNAKSent != 1 || st.Gaps != 1 {
		t.Fatalf("one gap, one VNAK expected: %+v", st)
	}
	p.expect(TypeIAX, CmdVNAK)
	// Past the rate limit the next frame of the same gap asks once more.
	clk.Advance(200 * time.Millisecond)
	p.raw(fs[1])
	st = waitStats(t, c, func(s Stats) bool { return s.Dropped >= 31 })
	if st.VNAKSent != 2 || st.Gaps != 1 {
		t.Fatalf("second VNAK expected: %+v", st)
	}
	// The peer retransmits its queue in order.
	for _, f := range fs {
		f.Retrans = true
		p.raw(f)
	}
	for i := range fs {
		if ev := nextEvent(t, c, EventText); ev.Text.Body != fmt.Sprintf("m%d", i) {
			t.Fatalf("event %d: %+v", i, ev)
		}
	}
	if st := c.Stats(); st.VNAKSent != 2 {
		t.Fatalf("stats %+v", st)
	}
}

// A peer that VNAKs over and over gets one retransmission per interval.
func TestPeerVNAKRetransmitRateLimited(t *testing.T) {
	c, p, _ := simpleCall(t, nil)
	p.setAuto(false)
	if err := c.SendText("M 1999 0 hi"); err != nil {
		t.Fatal(err)
	}
	p.expectText("M 1999 0 hi")
	for i := 0; i < 20; i++ {
		p.send(TypeIAX, CmdVNAK, nil)
	}
	st := waitStats(t, c, func(s Stats) bool { return s.VNAKRecv >= 20 })
	if st.Retransmits != 1 {
		t.Fatalf("retransmits %d", st.Retransmits)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestDebugLogsFramesAndDrops(t *testing.T) {
	var buf lockedBuf
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, p, _ := simpleCall(t, func(cfg *Config) { cfg.Logger = lg })
	fs := peerBatch(p, 1)
	p.raw(fs[0])
	nextEvent(t, c, EventText)
	p.raw(fs[0])
	waitStats(t, c, func(s Stats) bool { return s.Dropped >= 1 })
	// The drop is counted before its log line is written.
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(buf.String(), "iax2 drop") && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	out := buf.String()
	for _, want := range []string{"iax2 rx", "iax2 tx", "iax2 drop", "reason=duplicate", "type=7"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestInfoLevelLogsNothing(t *testing.T) {
	var buf lockedBuf
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	simpleCall(t, func(cfg *Config) { cfg.Logger = lg })
	if buf.String() != "" {
		t.Fatalf("unexpected log: %s", buf.String())
	}
}
