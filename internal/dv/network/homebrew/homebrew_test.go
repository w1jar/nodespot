package homebrew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
)

const (
	testPW  = "example-pass"
	testRID = 123456701
)

// fakeMaster is an in-process Homebrew master.
type fakeMaster struct {
	t    *testing.T
	conn *net.UDPConn
	pw   string
	salt []byte

	mu       sync.Mutex
	pkts     [][]byte
	times    map[string][]time.Time // RPTL arrival times
	peer     *net.UDPAddr
	noPong   bool
	silent   bool // ignore everything
	authBad  bool
	parrot   bool // play a call back after its terminator
	echo     [][]byte
	rptlSeen int
	done     chan struct{}
}

func newMaster(t *testing.T) *fakeMaster {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMaster{t: t, conn: c, pw: testPW, salt: []byte{1, 2, 3, 4}, done: make(chan struct{}), times: map[string][]time.Time{}}
	go m.loop()
	t.Cleanup(func() { c.Close(); <-m.done })
	return m
}

func (m *fakeMaster) port() int { return m.conn.LocalAddr().(*net.UDPAddr).Port }

func (m *fakeMaster) reply(b []byte) {
	m.mu.Lock()
	p := m.peer
	m.mu.Unlock()
	if p != nil {
		m.conn.WriteToUDP(b, p)
	}
}

func (m *fakeMaster) loop() {
	defer close(m.done)
	buf := make([]byte, 2048)
	for {
		n, from, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		p := append([]byte(nil), buf[:n]...)
		m.mu.Lock()
		m.peer = from
		silent := m.silent
		if !silent {
			m.pkts = append(m.pkts, p)
		} else if hasTag(p, "RPTL") {
			m.rptlSeen++
			m.times["RPTL"] = append(m.times["RPTL"], time.Now())
		}
		noPong, bad := m.noPong, m.authBad
		m.mu.Unlock()
		if silent {
			continue
		}
		id := make([]byte, 4)
		binary.BigEndian.PutUint32(id, testRID)
		switch {
		case hasTag(p, "RPTL"):
			m.mu.Lock()
			m.times["RPTL"] = append(m.times["RPTL"], time.Now())
			m.mu.Unlock()
			m.reply(append([]byte("RPTACK"), m.salt...))
		case hasTag(p, "RPTK"):
			h := sha256.Sum256(append(append([]byte(nil), m.salt...), m.pw...))
			if bad || !bytes.Equal(p[8:], h[:]) {
				m.reply(append([]byte("MSTNAK"), id...))
			} else {
				m.reply(append([]byte("RPTACK"), id...))
			}
		case hasTag(p, "RPTC"), hasTag(p, "RPTO"):
			m.reply(append([]byte("RPTACK"), id...))
		case hasTag(p, "DMRD"):
			m.mu.Lock()
			echo := m.parrot
			m.mu.Unlock()
			if echo {
				// like a parrot: keep the call, play it back after its terminator
				m.mu.Lock()
				m.echo = append(m.echo, p)
				flush := len(p) >= 16 && p[15]&0x30 == 0x20 && p[15]&0x0f == dtTerminator
				var play [][]byte
				if flush {
					play, m.echo = m.echo, nil
				}
				m.mu.Unlock()
				for _, q := range play {
					m.reply(q)
					time.Sleep(2 * time.Millisecond)
				}
			}
		case hasTag(p, "RPTPING"):
			if !noPong {
				m.reply(append([]byte("MSTPONG"), id...))
			}
		}
	}
}

func (m *fakeMaster) setNoPong(v bool)  { m.mu.Lock(); m.noPong = v; m.mu.Unlock() }
func (m *fakeMaster) setSilent(v bool)  { m.mu.Lock(); m.silent = v; m.mu.Unlock() }
func (m *fakeMaster) setAuthBad(v bool) { m.mu.Lock(); m.authBad = v; m.mu.Unlock() }

func (m *fakeMaster) count(tag string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.pkts {
		if hasTag(p, tag) {
			n++
		}
	}
	return n
}

func (m *fakeMaster) all(tag string) [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out [][]byte
	for _, p := range m.pkts {
		if hasTag(p, tag) {
			out = append(out, p)
		}
	}
	return out
}

func (m *fakeMaster) waitCount(tag string, n int) {
	m.t.Helper()
	waitFor(m.t, tag, func() bool { return m.count(tag) >= n })
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	dl := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(dl) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// recSink records sink callbacks.
type recSink struct {
	mu     sync.Mutex
	logins []ctl.LoginState
	hdrs   []network.CallHeader
	voice  []network.VoiceFrame
	ends   []uint64
}

func (s *recSink) Header(h network.CallHeader) {
	s.mu.Lock()
	s.hdrs = append(s.hdrs, h)
	s.mu.Unlock()
}
func (s *recSink) Voice(f network.VoiceFrame) {
	s.mu.Lock()
	s.voice = append(s.voice, f)
	s.mu.Unlock()
}
func (s *recSink) End(c uint64) { s.mu.Lock(); s.ends = append(s.ends, c); s.mu.Unlock() }
func (s *recSink) Login(l ctl.LoginState, _ string) {
	s.mu.Lock()
	s.logins = append(s.logins, l)
	s.mu.Unlock()
}
func (s *recSink) hasLogin(l ctl.LoginState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.logins {
		if x == l {
			return true
		}
	}
	return false
}
func (s *recSink) nLogin(l ctl.LoginState) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.logins {
		if x == l {
			n++
		}
	}
	return n
}
func (s *recSink) nVoice() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.voice) }
func (s *recSink) nEnd() int   { s.mu.Lock(); defer s.mu.Unlock(); return len(s.ends) }
func (s *recSink) nHdr() int   { s.mu.Lock(); defer s.mu.Unlock(); return len(s.hdrs) }

func fastOpts(extra map[string]string) map[string]string {
	o := map[string]string{
		"ping_interval_ms": "20", "login_timeout_ms": "60", "backoff_min_ms": "10",
		"backoff_max_ms": "80", "rx_timeout_ms": "80",
	}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

var testID = ctl.Identity{Callsign: "N0CALL", DMRID: 1234567, RepeaterID: testRID}

func start(t *testing.T, mode string, m *fakeMaster, pw string, extra map[string]string) (*Net, *recSink, context.CancelFunc, chan struct{}) {
	t.Helper()
	n, err := New(ctl.NetConfig{Mode: mode, Enabled: true, Address: "127.0.0.1", Port: m.port(), Password: pw,
		Options: fastOpts(extra)}, testID)
	if err != nil {
		t.Fatal(err)
	}
	s := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return n, s, cancel, done
}

func TestLoginSuccessAndConfigPacket(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeDMR, m, testPW, map[string]string{"options": "TS2=1997;", "location": "Testville", "colour_code": "3"})
	waitFor(t, "login ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	if st := n.Status(); st.Login != ctl.LoginOK || st.Mode != ctl.ModeDMR {
		t.Fatalf("status %+v", st)
	}
	m.waitCount("RPTO", 1)
	rptl := m.all("RPTL")[0]
	if len(rptl) != 8 || binary.BigEndian.Uint32(rptl[4:]) != testRID {
		t.Fatalf("RPTL %x", rptl)
	}
	rptk := m.all("RPTK")[0]
	h := sha256.Sum256(append([]byte{1, 2, 3, 4}, testPW...))
	if len(rptk) != 40 || !bytes.Equal(rptk[8:], h[:]) {
		t.Fatalf("RPTK %x", rptk)
	}
	c := m.all("RPTC")[0]
	if len(c) != 302 {
		t.Fatalf("RPTC len %d", len(c))
	}
	if string(c[8:16]) != "N0CALL  " || string(c[36:38]) != "03" || string(c[8+8+9+9+2+2+8+9+3:][:9]) != "Testville" {
		t.Fatalf("RPTC fields %q", c)
	}
	// unset position as MMDVMHost sends it, simplex slots byte, fixed widths
	if string(c[38:55]) != "0.00000000.000000" || c[97] != '4' || string(c[16:25]) != "000000000" || string(c[34:36]) != "01" {
		t.Fatalf("RPTC position/slots/power %q", c[16:98])
	}
	if got := string(c[len(c)-80 : len(c)-72]); got != "nodespot" {
		t.Fatalf("software %q", got)
	}
	if o := m.all("RPTO")[0]; string(o[8:]) != "TS2=1997;" {
		t.Fatalf("RPTO %q", o)
	}
	if bytes.Contains(c, []byte(testPW)) {
		t.Fatal("password leaked")
	}
}

func TestBadPasswordRetriesWithBackoff(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeDMR, m, "wrong", nil)
	waitFor(t, "failed", func() bool { return s.hasLogin(ctl.LoginFailed) })
	m.waitCount("RPTL", 3)
	st := n.Status()
	if st.Login == ctl.LoginOK {
		t.Fatalf("logged in with bad password")
	}
	if bytes.Contains([]byte(st.Detail), []byte("wrong")) {
		t.Fatal("password in status")
	}
}

func TestLoginTimeoutBackoff(t *testing.T) {
	m := newMaster(t)
	m.setSilent(true)
	_, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "3 attempts", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.rptlSeen >= 4 })
	if !s.hasLogin(ctl.LoginFailed) {
		t.Fatal("no failed state")
	}
	m.mu.Lock()
	ts := m.times["RPTL"]
	m.mu.Unlock()
	// gap grows: attempt gaps include 60ms timeout + backoff 10,20,40...
	g1, g3 := ts[1].Sub(ts[0]), ts[3].Sub(ts[2])
	if g3 <= g1 {
		t.Fatalf("backoff did not grow: %v %v", g1, g3)
	}
	// recover once the master answers; backoff resets.
	m.setSilent(false)
	waitFor(t, "login ok", func() bool { return s.hasLogin(ctl.LoginOK) })
}

func TestKeepaliveAndLoss(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	m.waitCount("RPTPING", 3)
	if p := m.all("RPTPING")[0]; len(p) != 11 || binary.BigEndian.Uint32(p[7:]) != testRID {
		t.Fatalf("ping %x", p)
	}
	if m.count("RPTL") != 1 {
		t.Fatal("relogged while healthy")
	}
	m.setNoPong(true)
	m.waitCount("RPTL", 2)
	waitFor(t, "RPTCL on drop", func() bool { return m.count("RPTCL") >= 1 })
	m.setNoPong(false)
	waitFor(t, "ok again", func() bool { return s.nLogin(ctl.LoginOK) >= 2 })
}

func TestNakRelogin(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	m.reply([]byte("MSTNAK\x00\x00\x00\x00"))
	m.waitCount("RPTL", 2)
	waitFor(t, "ok again", func() bool { return s.nLogin(ctl.LoginOK) >= 2 })
}

func TestMasterClose(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	m.setSilent(true)
	m.reply([]byte("MSTCL\x00\x00\x00\x00"))
	waitFor(t, "failed", func() bool { return n.Status().Login != ctl.LoginOK })
	if !s.hasLogin(ctl.LoginFailed) {
		t.Fatal("expected failed state")
	}
}

func TestShutdownSendsRPTCL(t *testing.T) {
	m := newMaster(t)
	_, s, cancel, done := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	cancel()
	<-done
	m.waitCount("RPTCL", 1)
	if !s.hasLogin(ctl.LoginOff) {
		t.Fatal("no off state")
	}
}

func dmrdTo(m *fakeMaster, _ int) []dmrd {
	var out []dmrd
	for _, p := range m.all("DMRD") {
		if d, ok := parseDMRD(p); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestTuneUnlink(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	if err := n.Tune(context.Background(), ctl.Target{TG: 1997}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "kerchunk", func() bool {
		ds := dmrdTo(m, 0)
		return len(ds) > 0 && ds[len(ds)-1].DType == dtTerminator
	})
	ds := dmrdTo(m, 0)
	if ds[0].Dst != 1997 || ds[0].Slot != 2 || ds[0].Src != 1234567 || ds[0].Repeater != testRID ||
		ds[0].FrameTyp != ftDataSync || ds[0].DType != dtVoiceHeader {
		t.Fatalf("first %+v", ds[0])
	}
	if err := n.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unlink", func() bool {
		ds := dmrdTo(m, 0)
		return ds[len(ds)-1].Dst == 4000 && ds[len(ds)-1].DType == dtTerminator
	})
	// After a relogin the TG is relinked only if still tuned.
	n.Tune(context.Background(), ctl.Target{TG: 1998})
	before := len(dmrdTo(m, 0))
	m.reply([]byte("MSTNAK\x00\x00\x00\x00"))
	waitFor(t, "relink", func() bool {
		ds := dmrdTo(m, 0)
		return len(ds) > before+2 && ds[len(ds)-1].Dst == 1998
	})
}

func TestTuneBeforeLoginAndNoKerchunk(t *testing.T) {
	m := newMaster(t)
	m.setSilent(true)
	n, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	if err := n.Tune(context.Background(), ctl.Target{TG: 1999}); err != nil {
		t.Fatal(err)
	}
	m.setSilent(false)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	waitFor(t, "deferred kerchunk", func() bool { return len(dmrdTo(m, 0)) > 0 })
	if dmrdTo(m, 0)[0].Dst != 1999 {
		t.Fatal("wrong TG")
	}

	m2 := newMaster(t)
	n2, s2, _, _ := start(t, ctl.ModeTGIF, m2, testPW, map[string]string{"tune_kerchunk": "false"})
	waitFor(t, "ok2", func() bool { return s2.hasLogin(ctl.LoginOK) })
	n2.Tune(context.Background(), ctl.Target{TG: 1999})
	time.Sleep(50 * time.Millisecond)
	if m2.count("DMRD") != 0 {
		t.Fatal("kerchunk sent despite option")
	}
}

func ambe(i byte) dmr.AMBE { return dmr.AMBE{i, i + 1, i + 2, i + 3, i + 4, i + 5, 0x80} }

// sendCall plays a call from the master towards the client.
func sendCall(m *fakeMaster, slot int, stream uint32, tg uint32, nframes int, terminate bool) {
	tx := dmr.NewTX(dmr.TXConfig{Src: 1234567, Dst: tg, CC: 1})
	var fs []dmr.TXFrame
	fs = append(fs, tx.Start()...)
	for i := 0; i < nframes; i++ {
		fs = append(fs, tx.Write(ambe(byte(i*7)))...)
	}
	if terminate {
		fs = append(fs, tx.End()...)
	}
	for i, f := range fs {
		d := dmrd{Seq: byte(i), Src: 1234567, Dst: tg, Repeater: testRID, Slot: slot, FrameTyp: int(f.FrameType),
			DType: int(f.DTypeVSeq), Stream: stream, Payload: f.Burst}
		m.reply(d.marshal())
	}
}

func TestRXCall(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	sendCall(m, 1, 77, 1998, 6, true) // wrong slot, filtered
	time.Sleep(30 * time.Millisecond)
	if s.nHdr() != 0 {
		t.Fatal("slot 1 call delivered")
	}
	sendCall(m, 2, 78, 1998, 6, true)
	waitFor(t, "end", func() bool { return s.nEnd() >= 1 })
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hdrs) != 1 || s.hdrs[0].TG != 1998 || s.hdrs[0].SrcID != 1234567 || s.hdrs[0].Src != "1234567" || s.hdrs[0].Slot != 2 {
		t.Fatalf("hdr %+v", s.hdrs)
	}
	if len(s.voice) != 6 {
		t.Fatalf("voice frames %d", len(s.voice))
	}
	for i, f := range s.voice {
		a := ambe(byte(i * 7))
		if f.Call != s.hdrs[0].Call || f.Seq != uint32(i) || !bytes.Equal(f.Data, a[:]) {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if s.ends[0] != s.hdrs[0].Call {
		t.Fatal("end id")
	}
}

func TestRXLostEnd(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	sendCall(m, 2, 90, 1998, 6, false)
	waitFor(t, "hdr", func() bool { return s.nHdr() == 1 })
	waitFor(t, "lost end", func() bool { return s.nEnd() == 1 })
	// a following call works
	sendCall(m, 2, 91, 1999, 3, true)
	waitFor(t, "second", func() bool { return s.nEnd() == 2 && s.nHdr() == 2 })
}

func TestTXCall(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	n.Tune(context.Background(), ctl.Target{TG: 1997})
	waitFor(t, "kerchunk", func() bool { return len(dmrdTo(m, 0)) >= 4 })
	base := len(dmrdTo(m, 0))
	if err := n.StartTX(network.CallHeader{Call: 5, SrcID: 1234567, TG: 1997}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ { // two bursts + one padded
		a := ambe(byte(i))
		if err := n.WriteVoice(network.VoiceFrame{Call: 5, Seq: uint32(i), Data: a[:]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.EndTX(5); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "terminator", func() bool {
		ds := dmrdTo(m, 0)
		return len(ds) > base && ds[len(ds)-1].DType == dtTerminator && ds[len(ds)-1].FrameTyp == ftDataSync
	})
	// The tune kerchunk can still be on the wire when base is read, so
	// keep only the packets of the call's own stream (the last one).
	all := dmrdTo(m, 0)[base:]
	stream := all[len(all)-1].Stream
	var ds []dmrd
	for _, d := range all {
		if d.Stream == stream {
			ds = append(ds, d)
		}
	}
	rx := dmr.NewRX()
	var got []dmr.AMBE
	voiceBursts := 0
	for i, d := range ds {
		if d.Stream != stream || d.Slot != 2 || d.Dst != 1997 || d.Src != 1234567 || d.Repeater != testRID {
			t.Fatalf("packet %d: %+v", i, d)
		}
		if i == 0 && d.Seq != 0 {
			t.Fatalf("first seq %d, want 0", d.Seq)
		}
		if i > 0 && d.Seq != ds[i-1].Seq+1 {
			t.Fatalf("seq gap at %d", i)
		}
		b := dmr.Burst(d.Payload)
		r := rx.Push(dmr.FrameType(d.FrameTyp), uint8(d.DType), &b)
		if r.HasVoice {
			voiceBursts++
			got = append(got, r.Voice[:]...)
		}
	}
	if ds[0].FrameTyp != ftDataSync || ds[0].DType != dtVoiceHeader {
		t.Fatal("no header first")
	}
	if voiceBursts != 3 || len(got) != 9 {
		t.Fatalf("voice bursts %d", voiceBursts)
	}
	for i := 0; i < 7; i++ {
		if got[i] != ambe(byte(i)) {
			t.Fatalf("frame %d mismatch", i)
		}
	}
	// new call -> new stream id
	n.StartTX(network.CallHeader{Call: 6, TG: 1997})
	n.EndTX(6)
	waitFor(t, "second call", func() bool {
		d := dmrdTo(m, 0)
		return d[len(d)-1].Stream != stream
	})
	if err := n.WriteVoice(network.VoiceFrame{Call: 6, Data: make([]byte, 7)}); err == nil {
		t.Fatal("write after end should fail")
	}
}

func TestTXWhenNotLoggedIn(t *testing.T) {
	m := newMaster(t)
	m.setSilent(true)
	n, _, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	if err := n.StartTX(network.CallHeader{Call: 1, TG: 1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTwoConcurrentInstances(t *testing.T) {
	mt, md := newMaster(t), newMaster(t)
	nt, st, _, _ := start(t, ctl.ModeTGIF, mt, testPW, nil)
	nd, sd, _, _ := start(t, ctl.ModeDMR, md, testPW, map[string]string{"slot": "1"})
	waitFor(t, "both ok", func() bool { return st.hasLogin(ctl.LoginOK) && sd.hasLogin(ctl.LoginOK) })
	nt.Tune(context.Background(), ctl.Target{TG: 1997})
	nd.Tune(context.Background(), ctl.Target{TG: 1998})
	waitFor(t, "kerchunks", func() bool { return len(dmrdTo(mt, 0)) >= 2 && len(dmrdTo(md, 0)) >= 2 })
	if d := dmrdTo(mt, 0)[0]; d.Dst != 1997 || d.Slot != 2 {
		t.Fatalf("tgif %+v", d)
	}
	if d := dmrdTo(md, 0)[0]; d.Dst != 1998 || d.Slot != 1 {
		t.Fatalf("dmr %+v", d)
	}
	sendCall(md, 1, 5, 1998, 3, true)
	waitFor(t, "dmr rx", func() bool { return sd.nEnd() == 1 })
	if st.nHdr() != 0 {
		t.Fatal("cross-talk")
	}
	if nt.Mode() != ctl.ModeTGIF || nd.Mode() != ctl.ModeDMR {
		t.Fatal("modes")
	}
}

func TestRegisteredAndConfig(t *testing.T) {
	for _, mode := range []string{ctl.ModeTGIF, ctl.ModeDMR} {
		if !network.Available(mode) {
			t.Fatalf("%s not registered", mode)
		}
	}
	if _, err := network.New(ctl.NetConfig{Mode: ctl.ModeDMR}, testID); err == nil {
		t.Fatal("DMR without address should fail")
	}
	n, err := network.New(ctl.NetConfig{Mode: ctl.ModeTGIF, Password: "x"}, ctl.Identity{DMRID: 1234567})
	if err != nil {
		t.Fatal(err)
	}
	h := n.(*Net)
	if h.addr != "tgif.network:"+strconv.Itoa(62031) || h.rid != 1234567 {
		t.Fatalf("defaults %s %d", h.addr, h.rid)
	}
	if _, err := New(ctl.NetConfig{Mode: ctl.ModeDMR, Address: "node2001.example.net", Options: map[string]string{"slot": "3"}}, testID); err == nil {
		t.Fatal("bad slot")
	}
}

func TestDMRDCodec(t *testing.T) {
	d := dmrd{Seq: 9, Src: 1234567, Dst: 1997, Repeater: testRID, Slot: 2, Private: true, FrameTyp: ftDataSync, DType: 2, Stream: 0xdeadbeef}
	d.Payload[0], d.Payload[32] = 0xaa, 0x55
	b := d.marshal()
	if len(b) != 55 || b[15] != 0x80|0x40|0x20|0x02 {
		t.Fatalf("%x", b)
	}
	if g, ok := parseDMRD(b); !ok || g != d {
		t.Fatalf("roundtrip %+v", g)
	}
	if _, ok := parseDMRD(b[:40]); ok {
		t.Fatal("short accepted")
	}
}

func TestDMRDefaultPassword(t *testing.T) {
	mk := func(mode, pw string) string {
		n, err := New(ctl.NetConfig{Mode: mode, Address: "192.0.2.10", Port: 62031, Password: pw}, testID)
		if err != nil {
			t.Fatal(err)
		}
		return n.cfg.Password
	}
	if got := mk(ctl.ModeDMR, ""); got != "passw0rd" {
		t.Errorf("DMR empty password: %q", got)
	}
	if got := mk(ctl.ModeDMR, testPW); got != testPW {
		t.Errorf("DMR typed password must win: %q", got)
	}
	if got := mk(ctl.ModeTGIF, ""); got != "" {
		t.Errorf("TGIF has no default password: %q", got)
	}
}

func TestRXAnySlotDMR(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil) // card slot 2
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	sendCall(m, 1, 77, 759, 6, true) // the master moved the TG to its own slot
	waitFor(t, "end1", func() bool { return s.nEnd() == 1 })
	sendCall(m, 2, 78, 759, 6, true)
	waitFor(t, "end2", func() bool { return s.nEnd() == 2 })
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hdrs) != 2 || s.hdrs[0].Slot != 1 || s.hdrs[1].Slot != 2 || s.hdrs[0].TG != 759 {
		t.Fatalf("hdrs %+v", s.hdrs)
	}
}

func TestRXTGIFStillFiltersSlot(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	sendCall(m, 1, 77, 1998, 6, true)
	time.Sleep(50 * time.Millisecond)
	if s.nHdr() != 0 {
		t.Fatal("TGIF delivered slot 1")
	}
}

func TestRXOtherSlotDoesNotCutCall(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	sendCall(m, 1, 80, 759, 6, false) // call in progress on slot 1
	waitFor(t, "hdr", func() bool { return s.nHdr() == 1 })
	sendCall(m, 2, 81, 3181, 3, true) // stray stream on slot 2
	time.Sleep(50 * time.Millisecond)
	if s.nHdr() != 1 || s.nEnd() != 0 {
		t.Fatalf("stray slot disturbed the call: hdr %d end %d", s.nHdr(), s.nEnd())
	}
}

func TestRXAnySlotOptionOff(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeDMR, m, testPW, map[string]string{"rx_any_slot": "false"})
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	sendCall(m, 1, 77, 759, 6, true)
	time.Sleep(50 * time.Millisecond)
	if s.nHdr() != 0 {
		t.Fatal("slot 1 delivered with rx_any_slot=false")
	}
}

// sendCallTo is sendCall from our DMR ID's point of view: Dst may be a group
// or a private destination.
func sendCallTo(m *fakeMaster, slot int, stream uint32, dst uint32, private bool, nframes int) {
	tx := dmr.NewTX(dmr.TXConfig{Src: 2345678, Dst: dst, Private: private, CC: 1})
	fs := tx.Start()
	for i := 0; i < nframes; i++ {
		fs = append(fs, tx.Write(ambe(byte(i*7)))...)
	}
	fs = append(fs, tx.End()...)
	for i, f := range fs {
		d := dmrd{Seq: byte(i), Src: 2345678, Dst: dst, Private: private, Repeater: testRID, Slot: slot,
			FrameTyp: int(f.FrameType), DType: int(f.DTypeVSeq), Stream: stream, Payload: f.Burst}
		m.reply(d.marshal())
	}
}

func TestDMRTunedTalkgroupFilter(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	// not tuned: everything plays
	sendCallTo(m, 2, 90, 3100, false, 6)
	waitFor(t, "untuned", func() bool { return s.nEnd() == 1 })
	if err := n.Tune(context.Background(), ctl.Target{TG: 91}); err != nil {
		t.Fatal(err)
	}
	// the previous talkgroup keeps coming: dropped on both slots
	sendCallTo(m, 2, 91, 3100, false, 6)
	sendCallTo(m, 1, 92, 3200, false, 6)
	time.Sleep(100 * time.Millisecond)
	if s.nHdr() != 1 || s.nEnd() != 1 {
		t.Fatalf("other talkgroup played: hdr %d end %d", s.nHdr(), s.nEnd())
	}
	n.mu.Lock()
	active := n.rxCall != nil
	n.mu.Unlock()
	if active {
		t.Fatal("a dropped call holds the receive slot")
	}
	// an endless call of the old talkgroup does not shut the tuned one out
	sendCall(m, 1, 93, 3100, 3, false)
	sendCall(m, 2, 94, 91, 6, true)
	waitFor(t, "tuned", func() bool { return s.nHdr() == 2 && s.nEnd() == 2 })
	// a private call to us passes
	sendCallTo(m, 2, 95, 1234567, true, 6)
	waitFor(t, "private", func() bool { return s.nHdr() == 3 && s.nEnd() == 3 })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hdrs[1].TG != 91 || s.hdrs[2].TG != 1234567 || !s.hdrs[2].Private {
		t.Fatalf("hdrs %+v", s.hdrs)
	}
}

func TestTGIFNotFilteredByTune(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	if err := n.Tune(context.Background(), ctl.Target{TG: 91}); err != nil {
		t.Fatal(err)
	}
	sendCallTo(m, 2, 96, 3100, false, 6)
	waitFor(t, "played", func() bool { return s.nEnd() == 1 })
}

// tags lists the packets the master got, as short tags (DMRD with its
// destination).
func (m *fakeMaster) tags() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, p := range m.pkts {
		switch {
		case hasTag(p, "RPTCL"):
			out = append(out, "RPTCL")
		case hasTag(p, "RPTL"):
			out = append(out, "RPTL")
		case hasTag(p, "DMRD"):
			if d, ok := parseDMRD(p); ok && d.DType == dtVoiceHeader {
				out = append(out, "DMRD:"+strconv.FormatUint(uint64(d.Dst), 10))
			}
		}
	}
	return out
}

func TestDMRRetuneReconnects(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeDMR, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	ctx := context.Background()
	if err := n.Tune(ctx, ctl.Target{TG: 91}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "tune A", func() bool { return len(m.tags()) >= 2 })
	// the same talkgroup again: no reconnect
	if err := n.Tune(ctx, ctl.Target{TG: 91}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "tune A again", func() bool { return len(m.tags()) >= 3 })
	time.Sleep(100 * time.Millisecond)
	if got := m.tags(); strings.Contains(strings.Join(got, " "), "RPTCL") {
		t.Fatalf("same-TG tune reconnected: %v", got)
	}
	failed := s.nLogin(ctl.LoginFailed)
	if err := n.Tune(ctx, ctl.Target{TG: 3100}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "tune B", func() bool {
		got := strings.Join(m.tags(), " ")
		return strings.Contains(got, "RPTCL RPTL DMRD:3100")
	})
	waitFor(t, "ok again", func() bool { return s.nLogin(ctl.LoginOK) >= 2 })
	if s.nLogin(ctl.LoginFailed) != failed {
		t.Fatal("the planned reconnect was reported as a failure")
	}
	// unlink does not reconnect
	if err := n.Unlink(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := strings.Join(m.tags(), " "); strings.Count(got, "RPTCL") != 1 {
		t.Fatalf("%v", got)
	}
}

func TestTGIFIgnoresSlotAndOptions(t *testing.T) {
	m := newMaster(t)
	n, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, map[string]string{"options": "TS2=1997;", "slot": "1"})
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	if got := len(m.all("RPTO")); got != 0 {
		t.Fatalf("TGIF sent %d RPTO", got)
	}
	if err := n.Tune(context.Background(), ctl.Target{TG: 1997}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "kerchunk", func() bool { return len(dmrdTo(m, 0)) > 0 })
	if d := dmrdTo(m, 0)[0]; d.Slot != 2 {
		t.Fatalf("TGIF used slot %d", d.Slot)
	}
}

// A stream the client joins mid-call (no voice LC header, only voice bursts)
// starts a call from the source and talkgroup every DMRD packet carries, and
// the real header arriving later in the same stream does not start another.
func TestRXLateEntryNoHeader(t *testing.T) {
	m := newMaster(t)
	_, s, _, _ := start(t, ctl.ModeTGIF, m, testPW, nil)
	waitFor(t, "ok", func() bool { return s.hasLogin(ctl.LoginOK) })
	const stream, tg = 120, 1998
	tx := dmr.NewTX(dmr.TXConfig{Src: 1234567, Dst: tg, CC: 1})
	hdr := tx.Start()
	var voice []dmr.TXFrame
	for i := 0; i < 12; i++ {
		voice = append(voice, tx.Write(ambe(byte(i*7)))...)
	}
	term := tx.End()
	var seq byte
	play := func(fs []dmr.TXFrame) {
		for _, f := range fs {
			d := dmrd{Seq: seq, Src: 1234567, Dst: tg, Repeater: testRID, Slot: 2, FrameTyp: int(f.FrameType),
				DType: int(f.DTypeVSeq), Stream: stream, Payload: f.Burst}
			seq++
			m.reply(d.marshal())
		}
	}
	play(voice[:2]) // joined mid-call: no header
	waitFor(t, "late call", func() bool { return s.nHdr() == 1 && s.nVoice() > 0 })
	play(hdr) // the real header turns up later in the same stream
	play(voice[2:])
	play(term)
	waitFor(t, "end", func() bool { return s.nEnd() == 1 })
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hdrs) != 1 || s.hdrs[0].SrcID != 1234567 || s.hdrs[0].Src != "1234567" || s.hdrs[0].TG != tg || s.hdrs[0].Slot != 2 {
		t.Fatalf("headers %+v", s.hdrs)
	}
	if s.ends[0] != s.hdrs[0].Call {
		t.Fatal("end id")
	}
}
