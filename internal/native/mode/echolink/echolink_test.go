package echolink

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	el "github.com/w1jar/nodespot/internal/native/echolink"
	"github.com/w1jar/nodespot/internal/native/mode"
)

type fakeConn struct {
	st     el.Station
	recv   chan el.Frame
	events chan el.Event
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex
	err     error
	sent    int
	flushes int
	closed  bool
}

func (c *fakeConn) Recv() <-chan el.Frame   { return c.recv }
func (c *fakeConn) Events() <-chan el.Event { return c.events }
func (c *fakeConn) Done() <-chan struct{}   { return c.done }
func (c *fakeConn) Err() error              { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *fakeConn) Send(el.Frame) error     { c.mu.Lock(); c.sent++; c.mu.Unlock(); return nil }
func (c *fakeConn) Flush() error            { c.mu.Lock(); c.flushes++; c.mu.Unlock(); return nil }
func (c *fakeConn) Station() el.Station     { return c.st }
func (c *fakeConn) Status() el.ConnStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The counters follow what the mode did: a packet per two frames sent.
	return el.ConnStatus{Station: c.st, TxPackets: uint64(c.sent / 2), RxPackets: 5,
		RxLate: 1, RxBad: 2, RxDropped: 3, TxErrors: 4}
}
func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.end(nil)
	return nil
}
func (c *fakeConn) end(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

type fakeDir struct {
	mu     sync.Mutex
	conns  []*fakeConn
	closed bool
}

func (d *fakeDir) Connect(_ context.Context, target string) (Conn, error) {
	if target == "N9NONE" {
		return nil, el.ErrNotFound
	}
	c := &fakeConn{st: el.Station{Callsign: target, Location: "Example Town", ID: 1234567},
		recv: make(chan el.Frame, 10), events: make(chan el.Event, 10), done: make(chan struct{})}
	d.mu.Lock()
	d.conns = append(d.conns, c)
	d.mu.Unlock()
	return c, nil
}
func (d *fakeDir) Close() error      { d.mu.Lock(); d.closed = true; d.mu.Unlock(); return nil }
func (d *fakeDir) Status() el.Status { return el.Status{LoggedIn: true, Server: "dir.example.net"} }
func (d *fakeDir) conn(t *testing.T, n int) *fakeConn {
	t.Helper()
	waitFor(t, "conn", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.conns) >= n })
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[n-1]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type loginGate struct {
	mu    sync.Mutex
	calls int
	cfgs  []el.Config
	errs  []error // returned in order, then success
	open  chan struct{}
	dir   *fakeDir
}

func (g *loginGate) login(ctx context.Context, cfg el.Config) (Directory, error) {
	g.mu.Lock()
	g.calls++
	g.cfgs = append(g.cfgs, cfg)
	var err error
	if len(g.errs) > 0 {
		err, g.errs = g.errs[0], g.errs[1:]
	}
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if g.open != nil {
		select {
		case <-g.open:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.dir, nil
}

func newMode(t *testing.T, g *loginGate, vals map[string]string) *Mode {
	t.Helper()
	s := mode.Settings{Values: map[string]string{KeyPassword: "pw-secret"}}
	for k, v := range vals {
		s.Values[k] = v
	}
	env := mode.Env{Callsign: "n0call"}
	if err := Spec().CheckEnv(env, s); err != nil {
		t.Fatal(err)
	}
	m, err := New(env, s, Options{Login: g.login, Retry: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConfig(t *testing.T) {
	g := &loginGate{dir: &fakeDir{}}
	m := newMode(t, g, map[string]string{KeyServers: "dir1.example.net, dir2.example.net:5200", KeyName: "Pat"})
	if m.cfg.Callsign != "N0CALL-L" || len(m.cfg.Servers) != 2 || m.cfg.Name != "Pat" {
		t.Fatalf("cfg %+v", m.cfg)
	}
	m = newMode(t, g, map[string]string{KeyCallsign: "n1call-r"})
	if m.cfg.Callsign != "N1CALL-R" {
		t.Fatalf("callsign %q", m.cfg.Callsign)
	}
	if err := Spec().Check(mode.Settings{}); err == nil || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("Check: %v", err)
	}
}

func TestPendingTargetAndAudio(t *testing.T) {
	g := &loginGate{dir: &fakeDir{}, open: make(chan struct{})}
	m := newMode(t, g, nil)
	_ = m.Start(context.Background())
	if err := m.Connect(context.Background(), mode.Target{ID: "*example*"}); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.State != mode.StateConnecting || st.Target.ID != "*EXAMPLE*" {
		t.Fatalf("status before login %+v", st)
	}
	close(g.open)
	c := g.dir.conn(t, 1)
	if c.st.Callsign != "*EXAMPLE*" {
		t.Fatalf("called %q", c.st.Callsign)
	}
	waitFor(t, "connected", func() bool { return m.Status().State == mode.StateConnected })
	c.events <- el.Event{Kind: el.EventPeer, Callsign: "*EXAMPLE*", Name: "Conference"}
	c.events <- el.Event{Kind: el.EventTalkStart, Callsign: "N1CALL"}
	var f el.Frame
	f[3] = 7
	c.recv <- f
	select {
	case got := <-m.Recv():
		if got[3] != 7 {
			t.Fatal("frame")
		}
	case <-time.After(time.Second):
		t.Fatal("no audio")
	}
	waitFor(t, "talker", func() bool { tk := m.Status().Talker; return tk != nil && tk.Callsign == "N1CALL" })
	c.events <- el.Event{Kind: el.EventTalkStop}
	waitFor(t, "quiet", func() bool { return !m.Talking() })
	m.Send(f)
	m.Send(f)
	m.EndTx()
	m.EndTx() // a second EndTx without audio does not flush again
	c.mu.Lock()
	sent, fl := c.sent, c.flushes
	c.mu.Unlock()
	if sent != 2 || fl != 1 {
		t.Fatalf("sent %d flushes %d", sent, fl)
	}
	// The end of the transmission updates the link counters at once.
	want := mode.LinkStats{Proto: "echolink", RxVoiceFull: 5, TxVoice: 1, Late: 1, ParseErrors: 2, Dropped: 3, TxErrors: 4}
	if l := m.Status().Link; l == nil || *l != want {
		t.Fatalf("link stats %+v, want %+v", l, want)
	}
	// Remote bye: back to ready, still logged in.
	c.end(el.ErrRemoteBye)
	waitFor(t, "ready", func() bool { return m.Status().State == mode.StateReady })
	// New target, then Stop logs off.
	_ = m.Connect(context.Background(), mode.Target{ID: "N1CALL-L"})
	c2 := g.dir.conn(t, 2)
	waitFor(t, "connected 2", func() bool { return m.Status().State == mode.StateConnected })
	_ = m.Stop()
	if !c2.closed || !g.dir.closed || m.Status().State != mode.StateIdle {
		t.Fatalf("after Stop: conn closed %v dir closed %v state %s", c2.closed, g.dir.closed, m.Status().State)
	}
}

func TestLoginRetryAndBadPassword(t *testing.T) {
	g := &loginGate{dir: &fakeDir{}, errs: []error{errors.New("dial tcp: timeout")}}
	m := newMode(t, g, nil)
	_ = m.Start(context.Background())
	waitFor(t, "ready", func() bool { return m.Status().State == mode.StateReady })
	if g.calls != 2 {
		t.Fatalf("login calls %d", g.calls)
	}
	_ = m.Stop()

	g = &loginGate{dir: &fakeDir{}, errs: []error{el.ErrBadLogon}}
	m = newMode(t, g, nil)
	_ = m.Start(context.Background())
	waitFor(t, "error", func() bool { return m.Status().State == mode.StateError })
	time.Sleep(20 * time.Millisecond)
	if g.calls != 1 {
		t.Fatalf("retried after a refused logon: %d", g.calls)
	}
	if strings.Contains(m.Status().Error, "pw-secret") {
		t.Fatal("password in status")
	}
	_ = m.Stop()
}

func TestNotFound(t *testing.T) {
	g := &loginGate{dir: &fakeDir{}}
	m := newMode(t, g, nil)
	_ = m.Start(context.Background())
	waitFor(t, "ready", func() bool { return m.Status().State == mode.StateReady })
	_ = m.Connect(context.Background(), mode.Target{ID: "N9NONE"})
	waitFor(t, "error", func() bool { return m.Status().State == mode.StateError })
	if !strings.Contains(m.Status().Error, "not found") {
		t.Fatalf("error %q", m.Status().Error)
	}
	_ = m.Stop()
}
