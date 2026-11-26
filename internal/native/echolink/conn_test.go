package echolink

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/gsm"
)

// connSetup logs on to a fake directory and returns a config wired to a
// fake station at 127.0.0.2.
type connSetup struct {
	clk *fakeClock
	fd  *fakeDirectory
	st  *fakeStation
	d   *Directory
}

func newConnSetup(t *testing.T, answer bool) *connSetup {
	t.Helper()
	s := &connSetup{clk: newFakeClock(), fd: newFakeDirectory(t, testList), st: newFakeStation(t, "127.0.0.2", answer)}
	cfg := testConfig(s.clk, s.fd.addr())
	cfg.Refresh = 24 * time.Hour // keep the directory timer out of the way
	cfg.Info = "N0CALL-L\nExample City"
	cfg.LocalAudioAddr = "127.0.0.1:0"
	cfg.LocalControlAddr = "127.0.0.1:0"
	cfg.RemoteAudioPort = s.st.audioPort()
	cfg.RemoteControlPort = s.st.ctrlPort()
	cfg.tick = time.Second
	cfg.TalkHang = 2 * time.Second
	d, err := Login(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.d = d
	t.Cleanup(func() { d.Close() })
	return s
}

func (s *connSetup) connect(t *testing.T) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.d.Connect(ctx, "N1CALL")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tone(i int) Frame {
	var f Frame
	for k := range f {
		n := float64(i*FrameSamples + k)
		f[k] = int16(6000 * math.Sin(2*math.Pi*500*n/SampleRate))
	}
	return f
}

func TestConnectHandshake(t *testing.T) {
	s := newConnSetup(t, true)
	c := s.connect(t)
	defer c.Close()

	if ev := nextEvent(t, c, EventConnected); ev.Callsign != "N1CALL" {
		t.Fatalf("connected event %+v", ev)
	}
	if ev := nextEvent(t, c, EventPeer); ev.Callsign != "N1CALL" || ev.Name != "Test Op" {
		t.Fatalf("peer event %+v", ev)
	}
	// The station received our SDES with our identity, then our info.
	sdes := <-s.st.ctrlIn
	if sdes.callsign() != "N0CALL-L" || sdes.name() != "Test Op" || sdes.items[sdesLOC] != "Example City" {
		t.Fatalf("our SDES: %+v", sdes.items)
	}
	select {
	case p := <-s.st.audioIn:
		if tp := parseText(p); !tp.info || tp.text != "N0CALL-L\nExample City" {
			t.Fatalf("info packet %+v", tp)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no info packet")
	}
	st := c.Status()
	if st.State != StateConnected || st.PeerName != "Test Op" || st.Station.ID != 1997 {
		t.Fatalf("status %+v", st)
	}
	// The directory now reports us busy.
	l := s.fd.waitLogons(t, 2)
	if l[1].status != "BUSY" {
		t.Fatalf("logon status %q, want BUSY", l[1].status)
	}
	if ds := s.d.Status(); !ds.Busy || ds.Conn == nil {
		t.Fatalf("directory status %+v", ds)
	}
	// One connection at a time.
	if _, err := s.d.Connect(context.Background(), "*EXAMPLE*"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Connect: %v", err)
	}
	if _, err := s.d.Connect(context.Background(), "N0CALL-L"); err == nil {
		t.Fatal("connected to ourselves")
	}

	// Close sends BYE, closes the channels and reports ONLINE again.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	s.st.waitCounts(t, 1, 1)
	for range c.Events() {
	}
	if _, ok := <-c.Recv(); ok {
		t.Fatal("Recv not closed")
	}
	if c.Err() != nil {
		t.Fatalf("Err after local close: %v", c.Err())
	}
	l = s.fd.waitLogons(t, 3)
	if l[2].status != "ONLINE" {
		t.Fatalf("logon status %q, want ONLINE", l[2].status)
	}
	if err := c.Send(Frame{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
}

func TestSendAudio(t *testing.T) {
	s := newConnSetup(t, true)
	c := s.connect(t)
	defer c.Close()
	<-s.st.audioIn // info packet

	ref := gsm.NewEncoder()
	for i := 0; i < 6; i++ {
		f := tone(i)
		if err := c.Send(f); err != nil {
			t.Fatal(err)
		}
		pcm := [gsm.FrameSamples]int16(f)
		ref.Encode(&pcm)
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	for want := uint16(0); want < 2; want++ {
		select {
		case p := <-s.st.audioIn:
			seq, payload, err := parseAudio(p)
			if err != nil || len(p) != 144 || seq != want || p[0] != 0xC0 {
				t.Fatalf("packet %d: len %d seq %d err %v", want, len(p), seq, err)
			}
			if want == 0 {
				// The first packet must match a reference encoder exactly.
				ref2 := gsm.NewEncoder()
				for i := 0; i < 4; i++ {
					pcm := [gsm.FrameSamples]int16(tone(i))
					b := ref2.Encode(&pcm)
					if string(b[:]) != string(payload[i*33:i*33+33]) {
						t.Fatalf("frame %d differs from the reference encoding", i)
					}
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("packet %d not received", want)
		}
	}
	if st := c.Status(); st.TxPackets != 2 {
		t.Fatalf("TxPackets %d", st.TxPackets)
	}
}

func TestReceiveAudioAndText(t *testing.T) {
	s := newConnSetup(t, true)
	c := s.connect(t)
	defer c.Close()
	nextEvent(t, c, EventPeer)

	enc := gsm.NewEncoder()
	packet := func(seq uint16, base int) []byte {
		var payload []byte
		for i := 0; i < 4; i++ {
			pcm := [gsm.FrameSamples]int16(tone(base + i))
			b := enc.Encode(&pcm)
			payload = append(payload, b[:]...)
		}
		return buildAudio(seq, 0, payload)
	}
	first := packet(10, 0)
	s.st.sendAudio(t, c, first)
	if ev := nextEvent(t, c, EventTalkStart); ev.Callsign != "N1CALL" {
		t.Fatalf("talk start %+v", ev)
	}
	for i := 0; i < 4; i++ {
		select {
		case f := <-c.Recv():
			var peak int16
			for _, v := range f {
				peak = max(peak, v)
			}
			if i > 0 && peak < 1000 {
				t.Fatalf("frame %d is near silent (peak %d)", i, peak)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("frame %d not received", i)
		}
	}
	// A duplicate is dropped; a malformed packet is counted.
	s.st.sendAudio(t, c, first)
	s.st.sendAudio(t, c, first[:50])
	s.st.sendAudio(t, c, []byte("oNDATA\rConference\rN1CALL\rN1CALL-R\x00"))
	if ev := nextEvent(t, c, EventInfo); ev.Text != "Conference\nN1CALL\nN1CALL-R" {
		t.Fatalf("info %+v", ev)
	}
	s.st.sendAudio(t, c, []byte("oNDATAN1CALL-R>hello\r\n"))
	if ev := nextEvent(t, c, EventChat); ev.Callsign != "N1CALL-R" || ev.Text != "hello" {
		t.Fatalf("chat %+v", ev)
	}
	st := c.Status()
	if st.RxPackets != 1 || st.RxBad != 2 || st.Info == "" || !st.Talking {
		t.Fatalf("status %+v", st)
	}
	select {
	case <-c.Recv():
		t.Fatal("duplicate packet was decoded")
	default:
	}

	// Talk stops TalkHang after the last packet.
	s.clk.run(t, 3*time.Second, time.Second)
	if ev := nextEvent(t, c, EventTalkStop); ev.Callsign != "N1CALL" {
		t.Fatalf("talk stop %+v", ev)
	}
	if c.Status().Talking {
		t.Fatal("still talking")
	}
}

func TestKeepaliveAndTimeouts(t *testing.T) {
	t.Run("keepalive then idle timeout", func(t *testing.T) {
		s := newConnSetup(t, true)
		c := s.connect(t)
		s.st.waitCounts(t, 1, 0)
		// The station answers each keepalive, so the link stays up.
		s.clk.run(t, 10*time.Second, time.Second)
		s.st.waitCounts(t, 2, 0)
		s.clk.run(t, 10*time.Second, time.Second)
		s.st.waitCounts(t, 3, 0)
		// Now the station goes silent.
		s.st.mu.Lock()
		s.st.answer = false
		s.st.mu.Unlock()
		time.Sleep(20 * time.Millisecond) // let the last answer arrive
		s.clk.runUntil(t, 60*time.Second, time.Second, c.Done())
		ev := nextEvent(t, c, EventDisconnected)
		if !errors.Is(ev.Err, ErrTimeout) || !errors.Is(c.Err(), ErrTimeout) {
			t.Fatalf("disconnect %+v", ev)
		}
		<-c.finished
		if s.d.Status().Busy {
			t.Fatal("directory still busy")
		}
	})
	t.Run("no answer", func(t *testing.T) {
		s := newConnSetup(t, false)
		errc := make(chan error, 1)
		go func() {
			_, err := s.d.Connect(context.Background(), "N1CALL")
			errc <- err
		}()
		s.st.waitCounts(t, 1, 0)
		s.clk.run(t, DefaultConnectTimeout, time.Second)
		select {
		case err := <-errc:
			if !errors.Is(err, ErrConnectTimeout) {
				t.Fatalf("err = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Connect did not time out")
		}
		// It retried the SDES while waiting and said BYE at the end.
		s.st.waitCounts(t, 3, 1)
		// The slot is free again.
		s.st.mu.Lock()
		s.st.answer = true
		s.st.mu.Unlock()
		c := s.connect(t)
		c.Close()
	})
	t.Run("context canceled", func(t *testing.T) {
		s := newConnSetup(t, false)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			s.st.waitCounts(t, 1, 0)
			cancel()
		}()
		if _, err := s.d.Connect(ctx, "N1CALL"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRemoteBye(t *testing.T) {
	s := newConnSetup(t, true)
	c := s.connect(t)
	s.st.sendCtrl(t, c, buildBYE())
	ev := nextEvent(t, c, EventDisconnected)
	if !errors.Is(ev.Err, ErrRemoteBye) {
		t.Fatalf("disconnect %+v", ev)
	}
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("not done")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, byes := s.st.counts(); byes != 0 {
		t.Fatalf("we answered a BYE with %d BYEs", byes)
	}
}

func TestRefuseIncoming(t *testing.T) {
	s := newConnSetup(t, true)
	c := s.connect(t)
	defer c.Close()
	nextEvent(t, c, EventPeer)

	intruder := newFakeStation(t, "127.0.0.3", false)
	intruder.callsign = "N1CALL-R"
	intruder.sendCtrl(t, c, buildSDES("N1CALL-R", "Caller", ""))
	if ev := nextEvent(t, c, EventRefused); ev.Callsign != "N1CALL-R" {
		t.Fatalf("refused event %+v", ev)
	}
	// Audio from the intruder is ignored.
	intruder.sendAudio(t, c, buildAudio(1, 0, make([]byte, 132)))
	// A second attempt within refuseInterval is counted but not answered.
	intruder.sendCtrl(t, c, buildSDES("N1CALL-R", "Caller", ""))
	nextEvent(t, c, EventRefused)
	select {
	case <-c.Recv():
		t.Fatal("intruder audio was accepted")
	case <-time.After(50 * time.Millisecond):
	}
	st := c.Status()
	if st.Refused != 2 || st.State != StateConnected || st.Talker != "N1CALL" {
		t.Fatalf("status %+v", st)
	}
	// One BYE went back to the intruder's port; the called station got
	// none.
	intruder.waitCounts(t, 0, 1)
	time.Sleep(20 * time.Millisecond)
	if _, byes := intruder.counts(); byes != 1 {
		t.Fatalf("intruder got %d BYEs, want 1", byes)
	}
	if _, byes := s.st.counts(); byes != 0 {
		t.Fatalf("station got %d BYEs, want 0", byes)
	}
}
