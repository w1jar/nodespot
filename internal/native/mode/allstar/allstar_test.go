package allstar

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/iax2"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/register"
)

// fakeLink is a loopback stand-in for one IAX2 call.
type fakeLink struct {
	recv   chan iax2.Frame
	events chan iax2.Event
	done   chan struct{}
	once   sync.Once

	mu     sync.Mutex
	keys   []bool
	sent   int
	err    error
	hungup bool
}

func newFakeLink() *fakeLink {
	return &fakeLink{recv: make(chan iax2.Frame, 10), events: make(chan iax2.Event, 10), done: make(chan struct{})}
}

func (l *fakeLink) Recv() <-chan iax2.Frame   { return l.recv }
func (l *fakeLink) Events() <-chan iax2.Event { return l.events }
func (l *fakeLink) Done() <-chan struct{}     { return l.done }
func (l *fakeLink) Err() error                { l.mu.Lock(); defer l.mu.Unlock(); return l.err }
func (l *fakeLink) Key(on bool) error {
	l.mu.Lock()
	l.keys = append(l.keys, on)
	l.mu.Unlock()
	return nil
}
func (l *fakeLink) Send(iax2.Frame) { l.mu.Lock(); l.sent++; l.mu.Unlock() }
func (l *fakeLink) Hangup() error {
	l.mu.Lock()
	l.hungup = true
	l.mu.Unlock()
	l.end(nil)
	return nil
}
func (l *fakeLink) end(err error) {
	l.once.Do(func() {
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(l.done)
	})
}

type fakeReg struct {
	mu      sync.Mutex
	ready   bool
	changed chan struct{}
	cfg     register.Config
	since   time.Time
}

func (r *fakeReg) Start(context.Context) error { return nil }
func (r *fakeReg) Stop()                       {}
func (r *fakeReg) Ready() bool                 { r.mu.Lock(); defer r.mu.Unlock(); return r.ready }
func (r *fakeReg) Status() register.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return register.Status{Registered: r.ready, PublicIP: "203.0.113.5", Since: r.since}
}
func (r *fakeReg) Changed() <-chan struct{} { return r.changed }
func (r *fakeReg) set(v bool) {
	r.mu.Lock()
	r.ready = v
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

type harness struct {
	m     *Mode
	reg   *fakeReg
	mu    sync.Mutex
	links []*fakeLink
	dials []iax2.Config
	fail  error
}

func newHarness(t *testing.T, vals map[string]string) *harness {
	t.Helper()
	h := &harness{reg: &fakeReg{changed: make(chan struct{}, 1)}}
	s := mode.Settings{Values: map[string]string{KeyNode: "1999", KeyPassword: "secret1"}}
	for k, v := range vals {
		s.Values[k] = v
	}
	if err := Spec().Check(s); err != nil {
		t.Fatal(err)
	}
	m, err := New(mode.Env{Callsign: "N0CALL"}, s, Options{
		Resolve: func(_ context.Context, node string) (netip.AddrPort, error) {
			if node == "2002" {
				return netip.AddrPort{}, iax2.ErrNodeNotFound
			}
			return netip.MustParseAddrPort("192.0.2.10:4569"), nil
		},
		Dial: func(_ context.Context, cfg iax2.Config) (Link, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.dials = append(h.dials, cfg)
			if h.fail != nil {
				return nil, h.fail
			}
			l := newFakeLink()
			h.links = append(h.links, l)
			return l, nil
		},
		NewRegistrar: func(cfg register.Config) (Registrar, error) {
			h.reg.cfg = cfg
			return h.reg, nil
		},
		RetryMin: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond,
		ReadyPoll: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m = m
	return h
}

func (h *harness) link(t *testing.T, n int) *fakeLink {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // generous for a loaded CI runner
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.links) >= n {
			l := h.links[n-1]
			h.mu.Unlock()
			return l
		}
		h.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no link %d", n)
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // generous for a loaded CI runner
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		vals map[string]string
		want string
	}{
		{map[string]string{KeyNode: "1999"}, ""},
		{map[string]string{KeyNode: "19"}, "4 to 7 digits"},
		{map[string]string{KeyNode: "abcd"}, "4 to 7 digits"},
		{map[string]string{KeyNode: "1999", KeyIntervalS: "30"}, "at least 60"},
		{map[string]string{}, "Node number is required"},
	}
	for _, tt := range tests {
		err := Spec().Check(mode.Settings{Values: tt.vals})
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%v: %v, want %q", tt.vals, err, tt.want)
		}
	}
}

func TestLinkAudioAndTalker(t *testing.T) {
	h := newHarness(t, nil)
	m := h.m
	if err := m.Connect(context.Background(), mode.Target{ID: "2000"}); err == nil {
		t.Fatal("Connect before Start succeeded")
	}
	_ = m.Start(context.Background())
	if err := m.Connect(context.Background(), mode.Target{ID: "1999"}); err == nil {
		t.Fatal("Connect to own node succeeded")
	}
	if err := m.Connect(context.Background(), mode.Target{ID: "2000"}); err != nil {
		t.Fatal(err)
	}
	l := h.link(t, 1)
	waitFor(t, "connected", func() bool { return m.Status().State == mode.StateConnected })
	if c := h.dials[0]; c.LocalNode != "1999" || c.RemoteNode != "2000" || c.Callsign != "N0CALL" || c.Secret != "secret1" {
		t.Fatalf("dial config %+v", c)
	}
	// Far side keys and talks.
	l.events <- iax2.Event{Kind: iax2.EventRxKey, Keyed: true}
	var f audio.Frame
	f[0] = 42
	l.recv <- f
	select {
	case got := <-m.Recv():
		if got[0] != 42 {
			t.Fatal("wrong frame")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no audio")
	}
	waitFor(t, "talking", m.Talking)
	if tk := m.Status().Talker; tk == nil || tk.Target != "2000" {
		t.Fatalf("talker %+v", tk)
	}
	l.events <- iax2.Event{Kind: iax2.EventRxKey, Keyed: false}
	waitFor(t, "unkeyed", func() bool { return !m.Talking() })
	l.events <- iax2.Event{Kind: iax2.EventLinkList, Links: iax2.ParseLinkList("T2001,R2002")}
	waitFor(t, "links", func() bool { return len(m.Status().Links) == 2 })

	// Local TX keys once, unkeys on EndTx. Keying is asynchronous.
	keysOf := func() []bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return append([]bool(nil), l.keys...)
	}
	m.Send(f)
	waitFor(t, "keyed", func() bool { return len(keysOf()) == 1 })
	m.Send(f)
	m.EndTx()
	waitFor(t, "unkeyed link", func() bool { return len(keysOf()) == 2 })
	l.mu.Lock()
	keys, sent := append([]bool(nil), l.keys...), l.sent
	l.mu.Unlock()
	if len(keys) != 2 || !keys[0] || keys[1] || sent != 2 {
		t.Fatalf("keys %v sent %d", keys, sent)
	}

	// Switching target hangs up the old link first.
	if err := m.Connect(context.Background(), mode.Target{ID: "2001"}); err != nil {
		t.Fatal(err)
	}
	l2 := h.link(t, 2)
	if !l.hungup {
		t.Fatal("old link not hung up")
	}
	_ = m.Disconnect(context.Background())
	if !l2.hungup || m.Status().State != mode.StateReady || m.Status().Target != nil {
		t.Fatalf("after disconnect: hung %v status %+v", l2.hungup, m.Status())
	}
	_ = m.Stop()
	if m.Status().State != mode.StateIdle {
		t.Fatal("not idle after Stop")
	}
}

func TestReconnectAndFatal(t *testing.T) {
	h := newHarness(t, nil)
	_ = h.m.Start(context.Background())
	_ = h.m.Connect(context.Background(), mode.Target{ID: "2000"})
	l := h.link(t, 1)
	l.end(iax2.ErrTimeout)
	h.link(t, 2) // redialed
	waitFor(t, "connected again", func() bool { return h.m.Status().State == mode.StateConnected })
	_ = h.m.Disconnect(context.Background())

	// DNS miss: error, no retry.
	_ = h.m.Connect(context.Background(), mode.Target{ID: "2002"})
	waitFor(t, "error", func() bool { return h.m.Status().State == mode.StateError })
	if !strings.Contains(h.m.Status().Error, "not found") {
		t.Fatalf("error %q", h.m.Status().Error)
	}
	h.mu.Lock()
	h.fail = iax2.ErrRejected
	h.mu.Unlock()
	_ = h.m.Connect(context.Background(), mode.Target{ID: "2000"})
	waitFor(t, "rejected", func() bool { return strings.Contains(h.m.Status().Error, "rejected") })
	time.Sleep(40 * time.Millisecond)
	h.mu.Lock()
	n := len(h.dials)
	h.mu.Unlock()
	if n != 3 {
		t.Fatalf("dials %d, want 3 (no retry after reject)", n)
	}
	_ = h.m.Stop()
}

// TestFarEndDrops checks that a disconnect string or a hangup from the far
// node ends the link for good: no redial, no target, mode ready.
func TestFarEndDrops(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"disconnect string", iax2.ErrDisconnected},
		{"hangup", iax2.ErrHungUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			_ = h.m.Start(context.Background())
			_ = h.m.Connect(context.Background(), mode.Target{ID: "2000"})
			l := h.link(t, 1)
			waitFor(t, "connected", func() bool { return h.m.Status().State == mode.StateConnected })
			l.end(tt.err)
			waitFor(t, "ready", func() bool { return h.m.Status().State == mode.StateReady })
			time.Sleep(40 * time.Millisecond) // longer than RetryMax
			h.mu.Lock()
			n := len(h.dials)
			h.mu.Unlock()
			if n != 1 {
				t.Fatalf("dials %d, want 1 (no redial)", n)
			}
			if st := h.m.Status(); st.Target != nil || st.Error == "" {
				t.Fatalf("status %+v", st)
			}
			_ = h.m.Stop()
		})
	}
}

func TestRegistrationGatesLink(t *testing.T) {
	h := newHarness(t, map[string]string{KeyLinkWaitS: "5"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.m.RunBackground(ctx); close(done) }()
	waitFor(t, "registrar", func() bool { return h.m.Status().Registration != nil })
	if h.reg.cfg.Node != "1999" || h.reg.cfg.Password != "secret1" {
		t.Fatalf("registrar config %+v", h.reg.cfg)
	}
	_ = h.m.Start(context.Background())
	_ = h.m.Connect(context.Background(), mode.Target{ID: "2000"})
	time.Sleep(30 * time.Millisecond)
	h.mu.Lock()
	n := len(h.dials)
	h.mu.Unlock()
	if n != 0 {
		t.Fatal("dialed before registration was ready")
	}
	if d := h.m.Status().Detail; d != "waiting for registration" {
		t.Fatalf("detail %q", d)
	}
	h.reg.set(true)
	h.link(t, 1)
	waitFor(t, "registered status", func() bool {
		r := h.m.Status().Registration
		return r != nil && r.Registered && r.PublicIP == "203.0.113.5"
	})
	_ = h.m.Stop()
	cancel()
	<-done
}

func TestNoPasswordNoRegistration(t *testing.T) {
	h := newHarness(t, map[string]string{KeyPassword: ""})
	h.m.password = ""
	done := make(chan struct{})
	go func() { h.m.RunBackground(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunBackground did not return without a password")
	}
}

func TestRegistrationHint(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.m.registrationHint(); got != "" {
		t.Fatalf("no registrar: %q", got)
	}
	h.m.reg = h.reg
	if got := h.m.registrationHint(); got != "" {
		t.Fatalf("never registered: %q", got)
	}
	h.reg.mu.Lock()
	h.reg.since = time.Now().Add(-3*time.Minute - 5*time.Second)
	h.reg.mu.Unlock()
	if got := h.m.registrationHint(); !strings.Contains(got, "registered 3 min ago") || !strings.Contains(got, "~10 min") {
		t.Fatalf("young registration: %q", got)
	}
	h.reg.mu.Lock()
	h.reg.since = time.Now().Add(-11 * time.Minute)
	h.reg.mu.Unlock()
	if got := h.m.registrationHint(); got != "" {
		t.Fatalf("old registration: %q", got)
	}
}

func TestHungUpErrorCarriesHint(t *testing.T) {
	h := newHarness(t, map[string]string{KeyReconnect: "false"})
	h.reg.set(true)
	h.reg.mu.Lock()
	h.reg.since = time.Now().Add(-2 * time.Minute)
	h.reg.mu.Unlock()
	h.m.reg = h.reg
	_ = h.m.Start(context.Background())
	_ = h.m.Connect(context.Background(), mode.Target{ID: "2000"})
	l := h.link(t, 1)
	l.end(fmt.Errorf("%w: Not allowed (cause code 21)", iax2.ErrHungUp))
	waitFor(t, "hint in status", func() bool {
		return strings.Contains(h.m.Status().Detail+h.m.Status().Error, "registered 2 min ago")
	})
}
