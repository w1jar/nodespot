package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"math"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	el "github.com/w1jar/nodespot/internal/native/echolink"
	"github.com/w1jar/nodespot/internal/native/gsm"
	"github.com/w1jar/nodespot/internal/native/mode"
	elmode "github.com/w1jar/nodespot/internal/native/mode/echolink"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// This file runs the EchoLink echo test scenario from the field end to end:
// the node logs in to a loopback directory, calls a fake parrot over real
// localhost UDP, hears its greeting on the radio, then a local station
// talks into the receiver and the parrot plays the recording back.

// elDir adapts *echolink.Directory to the mode's Directory interface.
type elDir struct{ *el.Directory }

func (d elDir) Connect(ctx context.Context, target string) (elmode.Conn, error) {
	c, err := d.Directory.Connect(ctx, target)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// fakeELDirectory answers logons with OK and station lists with list.
func fakeELDirectory(t *testing.T, list string) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(c)
				b, err := r.ReadByte()
				if err != nil {
					return
				}
				switch b {
				case 'l':
					for crs := 0; crs < 3; {
						b, err := r.ReadByte()
						if err != nil {
							return
						}
						if b == '\r' {
							crs++
						}
					}
					_, _ = c.Write([]byte("OK2.6\n"))
				case 's':
					_, _ = c.Write([]byte(list))
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

// freeUDPPort returns a loopback UDP port that was free a moment ago.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// parrot is a fake EchoLink echo test server. Like the real one it greets a
// station that sends it an SDES, records the GSM audio the station sends to
// its audio port, and plays the recording back once the station has been
// silent for quiet. It sends to the station's well-known ports, as EchoLink
// peers do, which here are clientAudio and clientCtrl.
type parrot struct {
	t           *testing.T
	audio, ctrl *net.UDPConn
	clientAudio netip.AddrPort
	quiet       time.Duration

	mu       sync.Mutex
	greeted  bool
	packets  [][]byte // RTP packets received from the station
	seqs     []uint16
	lastRx   time.Time
	pcm      []audio.Frame // decoded recording of the last talk spurt
	played   int           // recordings played back
	badFrame int
	stop     chan struct{}
}

func newParrot(t *testing.T, clientAudio int) *parrot {
	t.Helper()
	lo := net.IPv4(127, 0, 0, 1)
	a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
	if err != nil {
		t.Fatal(err)
	}
	p := &parrot{t: t, audio: a, ctrl: c, quiet: 400 * time.Millisecond, stop: make(chan struct{}),
		clientAudio: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(clientAudio))}
	go p.readCtrl()
	go p.readAudio()
	go p.loop()
	t.Cleanup(func() { close(p.stop); a.Close(); c.Close() })
	return p
}

func (p *parrot) audioPort() int { return p.audio.LocalAddr().(*net.UDPAddr).Port }
func (p *parrot) ctrlPort() int  { return p.ctrl.LocalAddr().(*net.UDPAddr).Port }

// sdes is an RR+SDES compound packet naming the parrot.
func parrotSDES() []byte {
	b := []byte{0xC0, 201, 0, 1, 0, 0, 0, 0}
	s := []byte{0xC1, 202, 0, 0, 0, 0, 0, 0}
	for _, it := range []struct {
		typ byte
		v   string
	}{{1, "CALLSIGN"}, {2, "*ECHOTEST*     Echo test"}, {3, "CALLSIGN"}, {4, "08:30"}} {
		s = append(s, it.typ, byte(len(it.v)))
		s = append(s, it.v...)
	}
	s = append(s, 0)
	for len(s)%4 != 0 {
		s = append(s, 0)
	}
	binary.BigEndian.PutUint16(s[2:], uint16(len(s)/4-1))
	return append(b, s...)
}

func (p *parrot) readCtrl() {
	buf := make([]byte, 2048)
	for {
		n, from, err := p.ctrl.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if n < 8 || buf[1] != 201 {
			continue
		}
		_, _ = p.ctrl.WriteToUDPAddrPort(parrotSDES(), from)
		p.mu.Lock()
		greet := !p.greeted
		p.greeted = true
		p.mu.Unlock()
		if greet {
			go p.play(tone(2000, 150)) // a three-second greeting
		}
	}
}

func (p *parrot) readAudio() {
	buf := make([]byte, 2048)
	for {
		n, _, err := p.audio.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		b := append([]byte(nil), buf[:n]...)
		if len(b) >= 6 && string(b[:6]) == "oNDATA" {
			continue
		}
		p.mu.Lock()
		p.packets = append(p.packets, b)
		if len(b) >= 4 {
			p.seqs = append(p.seqs, binary.BigEndian.Uint16(b[2:]))
		}
		p.lastRx = time.Now()
		p.mu.Unlock()
	}
}

// loop plays a recording back once the station has gone quiet. Like a
// simple parrot it sends back the very packets it recorded, so the playback
// carries the station's own RTP sequence numbers, which start again near
// zero and are lower than those of the greeting the station heard last.
func (p *parrot) loop() {
	dec := gsm.NewDecoder()
	tk := time.NewTicker(10 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tk.C:
		}
		p.mu.Lock()
		if len(p.packets) == 0 || time.Since(p.lastRx) < p.quiet {
			p.mu.Unlock()
			continue
		}
		pkts := p.packets
		p.packets = nil
		p.mu.Unlock()
		var rec []audio.Frame
		bad := 0
		for _, b := range pkts {
			if len(b) != 144 || b[0] != 0xC0 || b[1] != 3 {
				bad++
				continue
			}
			for off := 12; off+gsm.FrameBytes <= len(b); off += gsm.FrameBytes {
				var pcm [gsm.FrameSamples]int16
				if err := dec.Decode(b[off:off+gsm.FrameBytes], &pcm); err != nil {
					bad++
					continue
				}
				rec = append(rec, audio.Frame(pcm))
			}
		}
		p.mu.Lock()
		p.pcm, p.badFrame = rec, bad
		p.mu.Unlock()
		p.replay(pkts)
	}
}

// replay sends recorded packets back unchanged at real-time pacing.
func (p *parrot) replay(pkts [][]byte) {
	tk := time.NewTicker(4 * audio.FrameDuration)
	defer tk.Stop()
	for _, b := range pkts {
		_, _ = p.audio.WriteToUDPAddrPort(b, p.clientAudio)
		select {
		case <-p.stop:
			return
		case <-tk.C:
		}
	}
	p.mu.Lock()
	p.played++
	p.mu.Unlock()
}

// play sends frames to the station as GSM RTP at real-time pacing, with
// sequence numbers from zero.
func (p *parrot) play(frames []audio.Frame) {
	enc := gsm.NewEncoder()
	var seq uint16
	tk := time.NewTicker(4 * audio.FrameDuration)
	defer tk.Stop()
	for i := 0; i+4 <= len(frames); i += 4 {
		pkt := make([]byte, 12, 144)
		pkt[0], pkt[1] = 0xC0, 3
		binary.BigEndian.PutUint16(pkt[2:], seq)
		seq++
		for k := 0; k < 4; k++ {
			pcm := [gsm.FrameSamples]int16(frames[i+k])
			b := enc.Encode(&pcm)
			pkt = append(pkt, b[:]...)
		}
		_, _ = p.audio.WriteToUDPAddrPort(pkt, p.clientAudio)
		select {
		case <-p.stop:
			return
		case <-tk.C:
		}
	}
}

func (p *parrot) recording() (frames []audio.Frame, seqs []uint16, bad, played int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]audio.Frame(nil), p.pcm...), append([]uint16(nil), p.seqs...), p.badFrame, p.played
}

// tone is n frames of a 500 Hz sine at amplitude amp.
func tone(amp float64, n int) []audio.Frame {
	out := make([]audio.Frame, n)
	for k := range out {
		out[k] = sine(amp, k)
	}
	return out
}

func rmsOf(fs []audio.Frame) float64 {
	var sum float64
	n := 0
	for _, f := range fs {
		for _, s := range f {
			sum += float64(s) * float64(s)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(n))
}

// elRig is an engine on a fake radio with the real EchoLink mode connected
// to a parrot over loopback UDP.
type elRig struct {
	*rig
	p    *parrot
	mode *elmode.Mode
}

func newELRig(t *testing.T) *elRig {
	t.Helper()
	clientAudio, clientCtrl := freeUDPPort(t), freeUDPPort(t)
	p := newParrot(t, clientAudio)
	list := "@@@\n1\n*ECHOTEST*\nEcho test server [ON 12:00]\n9999\n127.0.0.1\n+++\n"
	dir := fakeELDirectory(t, list)

	g := &rig{t: t, r: radio.NewFake(), now: t0}
	er := &elRig{rig: g, p: p}
	cfg := quietConfig()
	cfg.Modes[mode.EchoLink] = mode.Settings{Values: map[string]string{
		elmode.KeyCallsign: "N0CALL-L", elmode.KeyPassword: "secret", elmode.KeyName: "Test",
	}}
	login := func(ctx context.Context, c el.Config) (elmode.Directory, error) {
		c.Servers = []string{dir}
		c.LocalAudioAddr = "127.0.0.1:" + strconv.Itoa(clientAudio)
		c.LocalControlAddr = "127.0.0.1:" + strconv.Itoa(clientCtrl)
		c.RemoteAudioPort, c.RemoteControlPort = p.audioPort(), p.ctrlPort()
		d, err := el.Login(ctx, c)
		if err != nil {
			return nil, err
		}
		return elDir{d}, nil
	}
	e, err := New(cfg, Options{
		Radio: g.r,
		Now:   g.clock,
		Build: func(n string, env mode.Env, s mode.Settings) (mode.Mode, error) {
			if n != mode.EchoLink {
				return modetest.New(n), nil
			}
			m, err := elmode.New(env, s, elmode.Options{Login: login})
			er.mode = m
			return m, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	g.e = e
	return er
}

// live runs the engine in real time for d, feeding the receiver from rx
// while the carrier is on.
func (g *elRig) live(d time.Duration, rx func() (audio.Frame, bool)) {
	end := time.Now().Add(d)
	next := time.Now()
	for time.Now().Before(end) {
		if rx != nil {
			if f, ok := rx(); ok {
				g.r.Inject(f)
			}
		}
		g.step(1)
		next = next.Add(audio.FrameDuration)
		if w := time.Until(next); w > 0 {
			time.Sleep(w)
		}
	}
}

// liveUntil is live until cond holds or the time runs out.
func (g *elRig) liveUntil(d time.Duration, cond func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		g.live(20*time.Millisecond, nil)
	}
	return cond()
}

// TestEchoLinkParrotThroughTheNode is the field scenario: connected to the
// echo test server, the greeting plays, then the local station talks and
// unkeys, and the recording comes back on the radio.
func TestEchoLinkParrotThroughTheNode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs in real time")
	}
	g := newELRig(t)
	g.sel(mode.EchoLink, &mode.Target{ID: "*ECHOTEST*"})
	if !g.liveUntil(5*time.Second, func() bool { return g.mode.State() == mode.StateConnected }) {
		t.Fatalf("not connected: %+v", g.mode.Status())
	}

	// The greeting keys the radio, then the node unkeys.
	if !g.liveUntil(3*time.Second, func() bool { return g.r.PTT() }) {
		t.Fatal("the greeting never keyed the radio")
	}
	if !g.liveUntil(8*time.Second, func() bool { return !g.r.PTT() && !g.mode.Talking() }) {
		t.Fatal("the radio stayed keyed after the greeting")
	}
	greeting := len(g.r.Played())
	if n := countLoud(g.r.Played()); n < 140 {
		t.Fatalf("greeting: %d loud frames on the radio", n)
	}
	g.live(200*time.Millisecond, nil)

	// The local station talks for two seconds and unkeys.
	g.r.SetCarrier(true)
	k := 0
	g.live(2*time.Second, func() (audio.Frame, bool) { k++; return sine(3277, k), true })
	if st := g.e.Status(); !st.Radio.RxActive {
		t.Errorf("status does not show the local transmission: %+v", st.Radio)
	}
	g.r.SetCarrier(false)
	g.live(40*time.Millisecond, nil)
	if st := g.mode.Status(); st.Link == nil || st.Link.TxVoice < 20 || st.Link.Proto != "echolink" {
		t.Errorf("link stats after the transmission: %+v", st.Link)
	}

	// The parrot gets about two seconds of the tone and plays it back.
	if !g.liveUntil(6*time.Second, func() bool { _, _, _, n := g.p.recording(); return n >= 1 }) {
		_, seqs, _, _ := g.p.recording()
		t.Fatalf("the parrot played nothing back; it got %d packets", len(seqs))
	}
	rec, seqs, bad, _ := g.p.recording()
	if bad != 0 {
		t.Errorf("%d bad packets or frames at the parrot", bad)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Errorf("sequence %d after %d", seqs[i], seqs[i-1])
		}
	}
	if len(rec) < 90 || len(rec) > 110 {
		t.Errorf("parrot recorded %d frames, want about 100", len(rec))
	}
	if r := rmsOf(rec[10 : len(rec)-10]); r < 1000 || r > 4000 {
		t.Errorf("recorded RMS %.0f, want about 2300 (a 3277 peak sine)", r)
	}
	// The playback ends on the radio and the node unkeys.
	if !g.liveUntil(5*time.Second, func() bool { return !g.r.PTT() && !g.mode.Talking() }) {
		t.Fatal("the radio stayed keyed after the playback")
	}
	played := g.r.Played()[greeting:]
	if st := g.mode.Status(); st.Link == nil || st.Link.RxVoiceFull < 60 || st.Link.ParseErrors != 0 || st.Link.Late != 0 {
		t.Errorf("link stats after the playback: %+v", st.Link)
	}
	if n := countLoud(played); n < 80 {
		t.Fatalf("playback: %d loud frames on the radio, want about 100", n)
	}
	if r := rmsOf(played); r < 300 {
		t.Errorf("playback RMS %.0f is too quiet", r)
	}
}
