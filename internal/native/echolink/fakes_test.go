package echolink

import (
	"bufio"
	"bytes"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manual clock. After channels fire when Advance passes them.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, fakeWaiter{f.now.Add(d), ch})
	return ch
}

// Advance moves time forward and fires the waiters that are due.
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.at.After(f.now) {
			w.ch <- f.now
		} else {
			kept = append(kept, w)
		}
	}
	f.waiters = kept
}

func (f *fakeClock) hasWaiterWithin(d time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.waiters {
		if !w.at.After(f.now.Add(d)) {
			return true
		}
	}
	return false
}

// step waits until a timer is armed within d, then advances by d.
func (f *fakeClock) step(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f.hasWaiterWithin(d) {
		if time.Now().After(deadline) {
			t.Fatalf("no timer armed within %v", d)
		}
		time.Sleep(time.Millisecond)
	}
	f.Advance(d)
}

// run advances total in steps of d, waiting for a timer before each step.
func (f *fakeClock) run(t *testing.T, total, d time.Duration) {
	t.Helper()
	for el := time.Duration(0); el < total; el += d {
		f.step(t, d)
	}
}

// runUntil is run that stops early once done is closed.
func (f *fakeClock) runUntil(t *testing.T, total, d time.Duration, done <-chan struct{}) {
	t.Helper()
	for el := time.Duration(0); el < total; el += d {
		deadline := time.Now().Add(2 * time.Second)
		for !f.hasWaiterWithin(d) {
			select {
			case <-done:
				return
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("no timer armed within %v", d)
			}
			time.Sleep(time.Millisecond)
		}
		f.Advance(d)
	}
}

// fakeDirectory is a loopback directory server.
type fakeDirectory struct {
	t      *testing.T
	ln     net.Listener
	mu     sync.Mutex
	logons []string // raw logon commands
	lists  int      // station list requests
	reply  string   // logon reply
	list   string   // station list reply
}

func newFakeDirectory(t *testing.T, list string) *fakeDirectory {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDirectory{t: t, ln: ln, reply: "OK2.6\n", list: list}
	go d.serve()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *fakeDirectory) addr() string { return d.ln.Addr().String() }

func (d *fakeDirectory) serve() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			return
		}
		go d.handle(c)
	}
}

func (d *fakeDirectory) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	first, err := r.ReadByte()
	if err != nil {
		return
	}
	switch first {
	case 'l':
		var cmd bytes.Buffer
		cmd.WriteByte('l')
		for crs := 0; crs < 3; {
			b, err := r.ReadByte()
			if err != nil {
				return
			}
			cmd.WriteByte(b)
			if b == '\r' {
				crs++
			}
		}
		d.mu.Lock()
		d.logons = append(d.logons, cmd.String())
		reply := d.reply
		d.mu.Unlock()
		_, _ = c.Write([]byte(reply))
	case 's':
		d.mu.Lock()
		d.lists++
		list := d.list
		d.mu.Unlock()
		_, _ = c.Write([]byte(list))
	}
}

// logon is a parsed logon command.
type logon struct {
	callsign, password, status, location string
}

func parseLogon(t *testing.T, raw string) logon {
	t.Helper()
	parts := strings.Split(raw, "\r")
	if len(parts) != 4 || !strings.HasPrefix(parts[0], "l") {
		t.Fatalf("malformed logon %q", raw)
	}
	cp := strings.SplitN(parts[0][1:], "\xac\xac", 2)
	if len(cp) != 2 {
		t.Fatalf("logon without separator: %q", raw)
	}
	status := parts[1]
	if i := strings.IndexAny(status, "0123456789"); i >= 0 {
		status = status[:i]
	}
	return logon{callsign: cp[0], password: cp[1], status: status, location: parts[2]}
}

func (d *fakeDirectory) logonList(t *testing.T) []logon {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []logon
	for _, raw := range d.logons {
		out = append(out, parseLogon(t, raw))
	}
	return out
}

// waitLogons waits until at least n logons have arrived.
func (d *fakeDirectory) waitLogons(t *testing.T, n int) []logon {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		l := d.logonList(t)
		if len(l) >= n {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d logons, want %d", len(l), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stationList renders a station list reply.
func stationList(entries ...[4]string) string {
	var b strings.Builder
	b.WriteString("@@@\n")
	b.WriteString(strconv.Itoa(len(entries)) + "\n")
	for _, e := range entries {
		for _, f := range e {
			b.WriteString(f + "\n")
		}
	}
	b.WriteString("+++\n")
	return b.String()
}

// fakeStation is an EchoLink peer on a loopback UDP pair.
type fakeStation struct {
	t        *testing.T
	audio    *net.UDPConn
	ctrl     *net.UDPConn
	callsign string
	name     string
	answer   bool // reply to SDES

	mu        sync.Mutex
	ctrlFrom  netip.AddrPort
	audioFrom netip.AddrPort // where the client's audio-port packets come from
	sdes      int
	byes      int
	audioIn   chan []byte // RTP packets
	textIn    chan []byte // oNDATA packets
	ctrlIn    chan rtcpInfo
}

func newFakeStation(t *testing.T, ip string, answer bool) *fakeStation {
	t.Helper()
	ua, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip)})
	if err != nil {
		t.Fatal(err)
	}
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip)})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeStation{
		t: t, audio: ua, ctrl: uc, callsign: "N1CALL", name: "Test Op", answer: answer,
		audioIn: make(chan []byte, 100), textIn: make(chan []byte, 100), ctrlIn: make(chan rtcpInfo, 100),
	}
	go s.readAudio()
	go s.readCtrl()
	t.Cleanup(func() { ua.Close(); uc.Close() })
	return s
}

func (s *fakeStation) audioPort() int { return s.audio.LocalAddr().(*net.UDPAddr).Port }
func (s *fakeStation) ctrlPort() int  { return s.ctrl.LocalAddr().(*net.UDPAddr).Port }

func (s *fakeStation) readAudio() {
	buf := make([]byte, 2048)
	for {
		n, from, err := s.audio.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.audioFrom = from
		s.mu.Unlock()
		ch := s.audioIn
		if isText(buf[:n]) {
			ch = s.textIn
		}
		select {
		case ch <- append([]byte(nil), buf[:n]...):
		default:
		}
	}
}

func (s *fakeStation) readCtrl() {
	buf := make([]byte, 2048)
	for {
		n, from, err := s.ctrl.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		info, _ := parseRTCP(buf[:n])
		s.mu.Lock()
		s.ctrlFrom = from
		if info.sdes {
			s.sdes++
		}
		if info.bye {
			s.byes++
		}
		answer := s.answer
		s.mu.Unlock()
		select {
		case s.ctrlIn <- info:
		default:
		}
		if info.sdes && answer {
			_, _ = s.ctrl.WriteToUDPAddrPort(buildSDES(s.callsign, s.name, ""), from)
		}
	}
}

// sendAudio sends p from the station's audio port to the client's audio
// socket.
func (s *fakeStation) sendAudio(t *testing.T, c *Conn, p []byte) {
	t.Helper()
	to := c.audio.LocalAddr().(*net.UDPAddr).AddrPort()
	to = netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), to.Port())
	if _, err := s.audio.WriteToUDPAddrPort(p, to); err != nil {
		t.Fatal(err)
	}
}

// sendCtrl sends p from the station's control port to the client.
func (s *fakeStation) sendCtrl(t *testing.T, c *Conn, p []byte) {
	t.Helper()
	to := c.ctrl.LocalAddr().(*net.UDPAddr).AddrPort()
	to = netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), to.Port())
	if _, err := s.ctrl.WriteToUDPAddrPort(p, to); err != nil {
		t.Fatal(err)
	}
}

func (s *fakeStation) counts() (sdes, byes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sdes, s.byes
}

func (s *fakeStation) waitCounts(t *testing.T, sdes, byes int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a, b := s.counts()
		if a >= sdes && b >= byes {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("station saw %d SDES and %d BYE, want %d and %d", a, b, sdes, byes)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// nextEvent waits for an event of kind k, skipping others.
func nextEvent(t *testing.T, c *Conn, k EventKind) Event {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("events closed while waiting for kind %d", k)
			}
			if ev.Kind == k {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for event kind %d", k)
		}
	}
}
