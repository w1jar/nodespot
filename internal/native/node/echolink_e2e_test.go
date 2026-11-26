package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
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
	opt         parrotOptions

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

// parrotOptions vary how the parrot greets and plays back.
type parrotOptions struct {
	// greetFrames is the greeting's length (0: three seconds);
	// greetSeq0 its first sequence number.
	greetFrames int
	greetSeq0   uint16
	// quiet is how long the station must be silent before the playback
	// (0: 400 ms).
	quiet time.Duration
	// burst sends the playback this many times faster than real time
	// (0 or 1: real time).
	burst int
	// pauseAfter, with pause, stops the playback for pause after that
	// many packets.
	pauseAfter int
	pause      time.Duration
}

func newParrot(t *testing.T, clientAudio int, opt parrotOptions) *parrot {
	t.Helper()
	if opt.greetFrames == 0 {
		opt.greetFrames = 150
	}
	if opt.quiet == 0 {
		opt.quiet = 400 * time.Millisecond
	}
	if opt.burst < 1 {
		opt.burst = 1
	}
	lo := net.IPv4(127, 0, 0, 1)
	a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
	if err != nil {
		t.Fatal(err)
	}
	p := &parrot{t: t, audio: a, ctrl: c, quiet: opt.quiet, opt: opt, stop: make(chan struct{}),
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
			go p.play(tone(2000, p.opt.greetFrames), p.opt.greetSeq0)
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

// replay sends recorded packets back unchanged at real-time pacing, or
// burst times faster, with the pause the options ask for.
func (p *parrot) replay(pkts [][]byte) {
	tk := time.NewTicker(4 * audio.FrameDuration / time.Duration(p.opt.burst))
	defer tk.Stop()
	for i, b := range pkts {
		if p.opt.pause > 0 && i == p.opt.pauseAfter {
			select {
			case <-p.stop:
				return
			case <-time.After(p.opt.pause):
			}
		}
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
// sequence numbers from seq.
func (p *parrot) play(frames []audio.Frame, seq uint16) {
	enc := gsm.NewEncoder()
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

func newELRig(t *testing.T) *elRig { return newELRigWith(t, parrotOptions{}, nil, nil) }

// newELRigWith is newELRig with a parrot built from opt, the station
// config changed by edit and the node logging to log.
func newELRigWith(t *testing.T, opt parrotOptions, edit func(*Config), log *slog.Logger) *elRig {
	t.Helper()
	clientAudio, clientCtrl := freeUDPPort(t), freeUDPPort(t)
	p := newParrot(t, clientAudio, opt)
	list := "@@@\n1\n*ECHOTEST*\nEcho test server [ON 12:00]\n9999\n127.0.0.1\n+++\n"
	dir := fakeELDirectory(t, list)

	g := &rig{t: t, r: radio.NewFake(), now: t0}
	er := &elRig{rig: g, p: p}
	cfg := quietConfig()
	cfg.Modes[mode.EchoLink] = mode.Settings{Values: map[string]string{
		elmode.KeyCallsign: "N0CALL-L", elmode.KeyPassword: "secret", elmode.KeyName: "Test",
	}}
	if edit != nil {
		edit(cfg)
	}
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
		Log:   log,
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

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *logBuf) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

// echoCase is one way the echo test can go in the field.
type echoCase struct {
	name   string
	parrot parrotOptions
	// keyUp leaves the key-up delay at its default (the other cases turn
	// it off to count frames from the step PTT keys).
	keyUp bool
	// after is how long after the greeting the local station keys, tx
	// how long it talks, and gap a short carrier drop in the middle.
	after, tx, gap time.Duration
}

// TestEchoLinkParrotCases runs the echo test the ways it can go wrong:
// long transmissions, a playback faster than real time, a pause in the
// playback, a transmission right after a greeting numbered above the
// playback, a carrier that drops for a moment. The parrot must get all we
// sent, and all of its playback must come out of the radio, with no
// counter showing a loss.
func TestEchoLinkParrotCases(t *testing.T) {
	if testing.Short() {
		t.Skip("runs in real time")
	}
	cases := []echoCase{
		{name: "ten seconds in real time", after: 200 * time.Millisecond, tx: 10 * time.Second},
		{name: "ten seconds played back in bursts", after: 200 * time.Millisecond, tx: 10 * time.Second,
			parrot: parrotOptions{burst: 4}},
		{name: "pause mid-playback", after: 200 * time.Millisecond, tx: 4 * time.Second,
			parrot: parrotOptions{pauseAfter: 20, pause: time.Second}},
		// The greeting ends at sequence 10, the playback starts at 0,
		// within the reorder window, and comes soon after.
		{name: "first transmission right after the greeting", after: 0, tx: 2 * time.Second,
			parrot: parrotOptions{greetSeq0: 65536 + 10 - 36, quiet: 100 * time.Millisecond}},
		{name: "carrier drops for a moment", after: 200 * time.Millisecond, tx: 4 * time.Second, gap: 60 * time.Millisecond},
		{name: "default key-up delay", keyUp: true, after: 200 * time.Millisecond, tx: 3 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runEchoCase(t, c)
		})
	}
}

func runEchoCase(t *testing.T, c echoCase) {
	var logs logBuf
	log := slog.New(slog.NewTextHandler(&logs, nil))
	edit := func(cfg *Config) {
		if c.keyUp {
			cfg.Station.Advanced.TxOnDelayMS = nil
		}
	}
	g := newELRigWith(t, c.parrot, edit, log)
	g.sel(mode.EchoLink, &mode.Target{ID: "*ECHOTEST*"})
	if !g.liveUntil(5*time.Second, func() bool { return g.mode.State() == mode.StateConnected }) {
		t.Fatalf("not connected: %+v", g.mode.Status())
	}

	// The greeting. Its first frames reach the radio only after the
	// transmitter has been keyed for the key-up delay.
	var pttAt, loudAt time.Time
	talker := ""
	watch := func() {
		if pttAt.IsZero() && g.r.PTT() {
			pttAt = g.clock()
		}
		if loudAt.IsZero() && countLoud(g.r.Played()) > 0 {
			loudAt = g.clock()
		}
		if st := g.e.Status(); st.Mode != nil && st.Mode.Talker != nil {
			talker = st.Mode.Talker.Callsign
		}
	}
	if !g.liveUntil(5*time.Second, func() bool { watch(); return !loudAt.IsZero() }) {
		t.Fatal("the greeting never reached the radio")
	}
	delay := time.Duration(0)
	if c.keyUp {
		delay = DefaultTxOnDelay
	}
	if d := loudAt.Sub(pttAt); d < delay || d > delay+3*audio.FrameDuration {
		t.Errorf("greeting played %v after PTT, want %v", d, delay)
	}
	if !g.liveUntil(8*time.Second, func() bool { watch(); return !g.r.PTT() && !g.mode.Talking() }) {
		t.Fatal("the radio stayed keyed after the greeting")
	}
	if talker != "*ECHOTEST*" {
		t.Errorf("talker during the greeting %q, want *ECHOTEST*", talker)
	}
	greetLoud := countLoud(g.r.Played())
	if want := c.parrot.greetFrames; want == 0 && (greetLoud < 146 || greetLoud > 150) {
		t.Errorf("greeting: %d loud frames on the radio, want 150", greetLoud)
	}
	greeting := len(g.r.Played())
	g.live(c.after, nil)

	// The local station talks, the status shows it.
	g.r.SetCarrier(true)
	k := 0
	feed := func() (audio.Frame, bool) { k++; return sine(3277, k), true }
	half := c.tx / 2
	g.live(half, feed)
	if st := g.e.Status(); !st.Radio.RxActive {
		t.Errorf("status does not show the local transmission: %+v", st.Radio)
	}
	if c.gap > 0 {
		g.r.SetCarrier(false)
		g.live(c.gap, nil)
		g.r.SetCarrier(true)
	}
	g.live(c.tx-half, feed)
	g.r.SetCarrier(false)
	sentFrames := int(c.tx / audio.FrameDuration)

	// The parrot gets all of it and plays it back.
	talker = ""
	if !g.liveUntil(c.tx+5*time.Second, func() bool { watch(); _, _, _, n := g.p.recording(); return n >= 1 }) {
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
	if d := len(rec) - sentFrames; d < -4 || d > 4 {
		t.Errorf("parrot recorded %d frames, want %d", len(rec), sentFrames)
	}
	if !g.liveUntil(c.tx+5*time.Second, func() bool {
		watch()
		return !g.r.PTT() && !g.mode.Talking() && len(g.mode.Recv()) == 0
	}) {
		t.Fatal("the radio stayed keyed after the playback")
	}
	if talker != "*ECHOTEST*" {
		t.Errorf("talker during the playback %q, want *ECHOTEST*", talker)
	}
	played := countLoud(g.r.Played()[greeting:])
	if d := played - len(rec); d < -4 || d > 4 {
		t.Errorf("playback: %d loud frames on the radio, the parrot sent %d", played, len(rec))
	}
	st := g.e.Status()
	if l := st.Mode.Link; l == nil || l.Late != 0 || l.Dropped != 0 || l.ParseErrors != 0 || st.Radio.NetDropped != 0 {
		t.Errorf("counters show a loss: link %+v, radio %+v", l, st.Radio)
	}
	// One line per received transmission: a pause longer than the talk
	// hang splits the playback in two.
	wants := []int{len(seqs)}
	if n := c.parrot.pauseAfter; c.parrot.pause > 0 {
		wants = []int{n, len(seqs) - n}
	}
	for _, n := range wants {
		want := fmt.Sprintf(`msg="echolink: received %d packets (late 0, dropped 0)" station=*ECHOTEST*`, n)
		if out := logs.String(); !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
}
