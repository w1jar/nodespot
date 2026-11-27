// SPDX-License-Identifier: GPL-3.0-or-later

package p25

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
)

// testdata/vectors.txt: imbeNN are 18 synthetic IMBE frames, p25rec the
// records MMDVMHost CP25Network::writeLDU1/writeLDU2 sent for them with
// the LC below (see testdata/README.md).
var vecLC = LC{LCF: 0, MFId: 0, Src: 1234567, Dst: 1997, AlgID: AlgClear}

func loadVectors(t *testing.T) (frames [][IMBELen]byte, recs [][]byte) {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.txt")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		v, err := hex.DecodeString(f[1])
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasPrefix(f[0], "imbe"):
			var a [IMBELen]byte
			copy(a[:], v)
			frames = append(frames, a)
		case f[0] == "p25rec":
			recs = append(recs, v)
		}
	}
	if len(frames) != superframe || len(recs) != superframe+1 {
		t.Fatalf("vectors: %d frames %d records", len(frames), len(recs))
	}
	return
}

func TestRecordVectors(t *testing.T) {
	frames, recs := loadVectors(t)
	for i := 0; i < superframe; i++ {
		if got := Record(i, frames[i], &vecLC); !bytes.Equal(got, recs[i]) {
			t.Errorf("record %d:\n got %x\nwant %x", i, got, recs[i])
		}
		var lc LC
		j, f, ok := ParseRecord(recs[i], &lc)
		if !ok || j != i || f != frames[i] {
			t.Errorf("parse %d: %d %x %v", i, j, f, ok)
		}
	}
	if !bytes.Equal(EndRecord(), recs[superframe]) {
		t.Errorf("end %x", EndRecord())
	}
	var lc LC
	for _, r := range recs[:superframe] {
		ParseRecord(r, &lc)
	}
	if lc != vecLC {
		t.Errorf("lc %+v", lc)
	}
}

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
			if r && p[0] == TypePoll {
				c.WriteToUDP(p, from) // P25Reflector echoes the poll
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
	f.conn.WriteToUDP(p, peer)
}

func (f *fakeRef) drain() {
	for len(f.got) > 0 {
		<-f.got
	}
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
			f.t.Fatal("no packet")
			return nil
		}
	}
}

func typ(t byte) func([]byte) bool { return func(p []byte) bool { return p[0] == t } }
func voiceRec(p []byte) bool       { return p[0] != TypePoll && p[0] != TypeUnlink }

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
	return func(e event) bool { return e.kind == "login" && e.login == st && strings.Contains(e.det, sub) }
}

func kind(k string) func(event) bool { return func(e event) bool { return e.kind == k } }

// waitLong bounds waits for something that should happen. They return as
// soon as it does; the bound only has to outlast a slow, loaded CI runner.
const waitLong = 10 * time.Second

// lossOpts has a short link timeout, for the tests about losing the link.
// The default for the others is long enough that a stall under -race on a
// loaded runner does not drop the link or end a call mid-test.
var lossOpts = map[string]string{OptPollMS: "50", OptTimeoutMS: "120"}

func start(t *testing.T, opts map[string]string) (*Net, *fakeRef, *sink) {
	t.Helper()
	return startReply(t, opts, true)
}

// startReply tunes to a fake reflector that answers polls only when reply is set;
// it waits for the link only when it does.
func startReply(t *testing.T, opts map[string]string, reply bool) (*Net, *fakeRef, *sink) {
	t.Helper()
	o := map[string]string{OptPollMS: "20", OptTimeoutMS: "2000", OptRxTimeoutMS: "300", OptTickMS: "5"}
	for k, v := range opts {
		o[k] = v
	}
	ref := newFakeRef(t)
	ref.setReply(reply)
	n, err := New(ctl.NetConfig{Mode: ctl.ModeP25, Enabled: true, Options: o},
		ctl.Identity{Callsign: "N0CALL", DMRID: 1234567})
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{ch: make(chan event, 4096)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := n.Tune(ctx, ctl.Target{Room: "1997", Host: "127.0.0.1", Port: ref.port()}); err != nil {
		t.Fatal(err)
	}
	if reply {
		s.wait(t, loginIs(ctl.LoginOK, "TG 1997"), waitLong)
	}
	return n, ref, s
}

func TestRegistered(t *testing.T) {
	n, err := network.New(ctl.NetConfig{Mode: ctl.ModeP25}, ctl.Identity{Callsign: "N0CALL"})
	if err != nil || n.Codec() != network.CodecIMBE {
		t.Fatal(err)
	}
}

func TestBadOptions(t *testing.T) {
	n, _ := New(ctl.NetConfig{}, ctl.Identity{Callsign: "N0CALL"})
	if err := n.Tune(context.Background(), ctl.Target{TG: 1997}); err == nil {
		t.Fatal("tune without host")
	}
}

func TestLoginPollUnlink(t *testing.T) {
	n, ref, s := start(t, nil)
	if p := ref.next(typ(TypePoll), waitLong); string(p[1:]) != "N0CALL    " || len(p) != 11 {
		t.Fatalf("poll %q", p)
	}
	if st := n.Status(); st.Login != ctl.LoginOK {
		t.Fatalf("status %+v", st)
	}
	n.Tune(context.Background(), ctl.Target{TG: UnlinkTG}) // TG 9999 = unlink
	for i := 0; i < 3; i++ {
		if p := ref.next(typ(TypeUnlink), waitLong); string(p) != "\xf1N0CALL    " {
			t.Fatalf("unlink %q", p)
		}
	}
	s.wait(t, loginIs(ctl.LoginOff, ""), waitLong)
}

func TestTimeoutBackoffReconnect(t *testing.T) {
	_, ref, s := start(t, lossOpts)
	ref.setReply(false)
	s.wait(t, loginIs(ctl.LoginPending, "retrying"), waitLong)
	ref.drain()
	// Compare the first gap with the third (four times as long), so
	// scheduling jitter on a loaded runner cannot invert them.
	ref.next(typ(TypePoll), waitLong)
	var gaps []time.Duration
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		ref.next(typ(TypePoll), waitLong)
		gaps = append(gaps, time.Since(t0))
	}
	if g1, g3 := gaps[0], gaps[2]; g3 <= g1 {
		t.Errorf("no backoff: %v then %v", g1, g3)
	}
	ref.setReply(true)
	s.wait(t, loginIs(ctl.LoginOK, ""), waitLong)
}

// A reflector that never answers: after the grace (timeout_ms) the login says so.
func TestNotAnsweringAfterTune(t *testing.T) {
	n, ref, s := startReply(t, lossOpts, false)
	e := s.wait(t, loginIs(ctl.LoginPending, "no reply from"), waitLong)
	if !ctl.NotAnswering(e.login, e.det) {
		t.Fatalf("event %+v is not 'not answering'", e)
	}
	if st := n.Status(); !ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v", st)
	}
	ref.setReply(true) // a reply clears it
	s.wait(t, loginIs(ctl.LoginOK, ""), waitLong)
	if st := n.Status(); ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v after reply", st)
	}
}

// A reflector that answers never reports it, and nothing tuned never does.
func TestAnsweringNeverNotAnswering(t *testing.T) {
	// a grace short enough to pass a few times over, but not so short that
	// one stall of a loaded runner looks like a silent reflector
	n, _, _ := start(t, map[string]string{OptTimeoutMS: "500"})
	time.Sleep(1500 * time.Millisecond) // well past the 500 ms grace
	if st := n.Status(); st.Login != ctl.LoginOK || ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("status %+v", st)
	}
	n.Tune(context.Background(), ctl.Target{TG: UnlinkTG})
	if st := n.Status(); ctl.NotAnswering(st.Login, st.Detail) {
		t.Fatalf("unlinked: %+v", st)
	}
}

func TestRXCall(t *testing.T) {
	_, ref, s := start(t, nil)
	frames, recs := loadVectors(t)
	for _, r := range recs { // a full superframe and the terminator
		ref.send(r)
	}
	h := s.wait(t, kind("header"), waitLong).h
	if h.SrcID != 1234567 || h.TG != 1997 || h.Src != "" || h.Private {
		t.Fatalf("header %+v", h)
	}
	for i := 0; i < superframe; i++ {
		f := s.wait(t, kind("voice"), waitLong).f
		if f.Call != h.Call || f.Seq != uint32(i) || !bytes.Equal(f.Data, frames[i][:]) {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if e := s.wait(t, kind("end"), waitLong); e.call != h.Call {
		t.Fatalf("end %d", e.call)
	}
}

func TestRXLostEnd(t *testing.T) {
	_, ref, s := start(t, nil)
	_, recs := loadVectors(t)
	for _, r := range recs[ldu : ldu+3] { // late entry in LDU2: no source id
		ref.send(r)
	}
	time.Sleep(20 * time.Millisecond)
	for _, r := range recs[:ldu] {
		ref.send(r)
	}
	h := s.wait(t, kind("header"), waitLong).h
	if h.TG != 1997 {
		t.Fatalf("header %+v", h)
	}
	if e := s.wait(t, kind("end"), waitLong); e.call != h.Call {
		t.Fatalf("end %d", e.call)
	}
}

func TestTXFraming(t *testing.T) {
	n, ref, _ := start(t, nil)
	frames, recs := loadVectors(t)
	if err := n.WriteVoice(network.VoiceFrame{Data: make([]byte, 7)}); err == nil {
		t.Fatal("7-byte frame accepted")
	}
	if err := n.StartTX(network.CallHeader{Call: 3, Src: "N0CALL"}); err != nil {
		t.Fatal(err)
	}
	ref.drain()
	for i := 0; i < superframe+2; i++ {
		f := frames[i%superframe]
		n.WriteVoice(network.VoiceFrame{Call: 3, Seq: uint32(i), Data: f[:]})
	}
	n.EndTX(3)
	for i := 0; i < superframe; i++ {
		if p := ref.next(voiceRec, waitLong); !bytes.Equal(p, recs[i]) {
			t.Fatalf("record %d:\n got %x\nwant %x", i, p, recs[i])
		}
	}
	// Second superframe: two frames, then silence to the end of LDU1.
	for i := 0; i < ldu; i++ {
		f := IMBESilence
		if i < 2 {
			f = frames[i]
		}
		want := Record(i, f, &vecLC)
		if p := ref.next(voiceRec, waitLong); !bytes.Equal(p, want) {
			t.Fatalf("pad record %d: %x", i, p)
		}
	}
	if p := ref.next(voiceRec, waitLong); !bytes.Equal(p, EndRecord()) {
		t.Fatalf("terminator %x", p)
	}
}

// A stored address is not linked at start (the config no longer carries one):
// nothing is sent until the first Tune.
func TestNoLinkAtStart(t *testing.T) {
	ref := newFakeRef(t)
	n, err := New(ctl.NetConfig{Mode: ctl.ModeP25, Enabled: true, Address: "127.0.0.1", Port: ref.port(),
		Options: map[string]string{OptPollMS: "20", OptTickMS: "5"}}, ctl.Identity{Callsign: "N0CALL", DMRID: 1234567})
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{ch: make(chan event, 4096)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(150 * time.Millisecond)
	if len(ref.got) != 0 {
		t.Fatal("linked at start")
	}
	if err := n.Tune(ctx, ctl.Target{Room: "1997", Host: "127.0.0.1", Port: ref.port()}); err != nil {
		t.Fatal(err)
	}
	s.wait(t, loginIs(ctl.LoginOK, "TG 1997"), waitLong)
}
