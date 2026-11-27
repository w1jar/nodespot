package allstar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/iax2"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/register"
)

// Link is the part of *iax2.Call the mode uses, so tests can fake it.
type Link interface {
	Recv() <-chan iax2.Frame
	Events() <-chan iax2.Event
	Done() <-chan struct{}
	Err() error
	Key(on bool) error
	Send(f iax2.Frame)
	Hangup() error
}

// Registrar is the part of *register.Registrar the mode uses.
type Registrar interface {
	Start(ctx context.Context) error
	Stop()
	Ready() bool
	Status() register.Status
	Changed() <-chan struct{}
}

// Options are injectable for tests. Zero values use the real network.
type Options struct {
	// Resolve maps a node number to its IAX2 address.
	Resolve func(ctx context.Context, node string) (netip.AddrPort, error)
	// Dial places the call.
	Dial func(ctx context.Context, cfg iax2.Config) (Link, error)
	// NewRegistrar builds the registrar.
	NewRegistrar func(cfg register.Config) (Registrar, error)
	// RetryMin and RetryMax bound the reconnect delay (5 s and 60 s).
	RetryMin, RetryMax time.Duration
	// ReadyPoll is how often a waiting link checks the registrar (1 s).
	ReadyPoll time.Duration
}

// Mode is the AllStar mode.
type Mode struct {
	mode.Base

	env      mode.Env
	node     string
	password string
	callsign string
	doReg    bool
	regURL   string
	interval time.Duration
	linkWait time.Duration
	retry    bool
	iaxPort  int
	opt      Options

	mu      sync.Mutex
	started bool
	reg     Registrar
	cancel  context.CancelFunc // the current link attempt
	wg      sync.WaitGroup
	link    Link
	keyed   bool
	keyc    chan bool // the wanted key state for the current link's keyer
}

var _ mode.Mode = (*Mode)(nil)
var _ mode.Background = (*Mode)(nil)

// New builds the mode from its settings.
func New(env mode.Env, s mode.Settings, opt Options) (*Mode, error) {
	m := &Mode{env: env, opt: opt}
	m.Init(mode.AllStar, "AllStar", env.Clock())
	var err error
	m.node = s.Get(KeyNode, "")
	m.password = s.Get(KeyPassword, "")
	m.callsign = env.Callsign
	if m.doReg, err = s.Bool(KeyRegister, true); err != nil {
		return nil, err
	}
	m.regURL = s.Get(KeyRegisterURL, register.DefaultURL)
	iv, err := s.Int(KeyIntervalS, 60)
	if err != nil {
		return nil, err
	}
	m.interval = time.Duration(iv) * time.Second
	lw, err := s.Int(KeyLinkWaitS, defaultLinkWait)
	if err != nil {
		return nil, err
	}
	m.linkWait = time.Duration(lw) * time.Second
	if m.retry, err = s.Bool(KeyReconnect, true); err != nil {
		return nil, err
	}
	if m.iaxPort, err = s.Int(KeyIAXPort, 0); err != nil {
		return nil, err
	}
	if m.opt.Resolve == nil {
		r := &iax2.Resolver{DNS: net.DefaultResolver, Domain: s.Get(KeyNodeDomain, "")}
		m.opt.Resolve = r.Resolve
	}
	if m.opt.Dial == nil {
		m.opt.Dial = func(ctx context.Context, cfg iax2.Config) (Link, error) {
			c, err := iax2.Dial(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return c, nil
		}
	}
	if m.opt.NewRegistrar == nil {
		m.opt.NewRegistrar = func(cfg register.Config) (Registrar, error) {
			return register.New(cfg)
		}
	}
	if m.opt.RetryMin <= 0 {
		m.opt.RetryMin = 5 * time.Second
	}
	if m.opt.RetryMax <= 0 {
		m.opt.RetryMax = time.Minute
	}
	if m.opt.ReadyPoll <= 0 {
		m.opt.ReadyPoll = time.Second
	}
	return m, nil
}

// RunBackground keeps the node registered until ctx ends. Without a
// password, or with registration turned off, it does nothing.
func (m *Mode) RunBackground(ctx context.Context) {
	if !m.doReg || m.password == "" {
		return
	}
	cfg := register.Config{
		Node: m.node, Password: m.password, Port: m.iaxPort,
		URL: m.regURL, Interval: m.interval,
		LookupHost: net.DefaultResolver.LookupHost,
	}
	r, err := m.opt.NewRegistrar(cfg)
	if err != nil {
		m.env.Logger().Warn("allstar: registration not started", "err", err)
		m.SetRegistration(&mode.Registration{LastError: err.Error()})
		return
	}
	if err := r.Start(ctx); err != nil {
		m.env.Logger().Warn("allstar: registration not started", "err", err)
		return
	}
	m.mu.Lock()
	m.reg = r
	m.mu.Unlock()
	m.updateReg(r)
	defer func() {
		r.Stop()
		m.mu.Lock()
		if m.reg == r {
			m.reg = nil
		}
		m.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Changed():
			m.updateReg(r)
		}
	}
}

func (m *Mode) updateReg(r Registrar) {
	st := r.Status()
	prev := m.Status().Registration
	m.SetRegistration(&mode.Registration{
		Registered: st.Registered, Ready: r.Ready(), PublicIP: st.PublicIP, DNSOK: st.DNSOK,
		LastAttempt: st.LastAttempt, LastSuccess: st.LastSuccess, Since: st.Since, LastError: st.LastError,
		HTTPStatus: st.HTTPStatus,
	})
	if prev == nil || prev.Registered != st.Registered || prev.LastError != st.LastError {
		if st.Registered {
			m.env.Logger().Info("allstar: registered", "node", m.node)
		} else if st.LastError != "" {
			m.env.Logger().Warn("allstar: registration failed", "node", m.node, "err", st.LastError)
		}
		m.Emit(mode.Event{Kind: mode.EventState, State: m.State()})
	}
}

// Start makes the mode ready. AllStar has no login of its own: the
// registration runs in the background.
func (m *Mode) Start(context.Context) error {
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	m.SetState(mode.StateReady, "node "+m.node, "")
	return nil
}

// Connect links to node t.ID, dropping the current link.
func (m *Mode) Connect(ctx context.Context, t mode.Target) error {
	if !ValidNode(t.ID) {
		return fmt.Errorf("allstar: %q is not a node number", t.ID)
	}
	if t.ID == m.node {
		return errors.New("allstar: cannot link to our own node")
	}
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return errors.New("allstar: mode is not started")
	}
	m.mu.Unlock()
	m.drop()
	lctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	tt := t
	if tt.Name == "" {
		tt.Name = "Node " + t.ID
	}
	m.SetTarget(&tt)
	m.SetLinkStats(nil)
	m.SetState(mode.StateConnecting, "linking to "+t.ID, "")
	m.wg.Add(1)
	go m.run(lctx, tt)
	return nil
}

// drop cancels the current link and waits for it.
func (m *Mode) drop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	m.TalkEnd()
	m.Flush()
}

// Disconnect drops the link.
func (m *Mode) Disconnect(context.Context) error {
	m.drop()
	m.SetTarget(nil)
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if started {
		m.SetState(mode.StateReady, "node "+m.node, "")
	}
	return nil
}

// Stop drops the link. The registration keeps running in RunBackground.
func (m *Mode) Stop() error {
	m.drop()
	m.SetTarget(nil)
	m.mu.Lock()
	m.started = false
	m.mu.Unlock()
	m.SetState(mode.StateIdle, "", "")
	return nil
}

// Send sends one frame of radio audio, keying the link on the first. It
// never blocks: keying is a round trip to the IAX2 session, so it is handed
// to the link's keyer goroutine. Frames queued before the key takes effect
// are kept by the call and sent once it is keyed.
func (m *Mode) Send(f audio.Frame) {
	m.mu.Lock()
	l := m.link
	keyNow := l != nil && !m.keyed
	if keyNow {
		m.keyed = true
		wantKey(m.keyc, true)
	}
	m.mu.Unlock()
	if l == nil {
		return
	}
	l.Send(f)
}

// EndTx unkeys the link. Like Send it never blocks.
func (m *Mode) EndTx() {
	m.mu.Lock()
	if m.link != nil && m.keyed {
		wantKey(m.keyc, false)
	}
	m.keyed = false
	m.mu.Unlock()
}

// wantKey records on as the wanted key state in c (buffer 1), replacing a
// state the keyer has not picked up yet, so the keyer always ends on the
// latest request. Called with m.mu, so there is one writer at a time.
func wantKey(c chan bool, on bool) {
	if c == nil {
		return
	}
	select {
	case <-c:
	default:
	}
	select {
	case c <- on:
	default:
	}
}

// keyer applies the wanted key state to l until stop closes.
func (m *Mode) keyer(l Link, c <-chan bool, stop <-chan struct{}) {
	applied := false
	for {
		select {
		case <-stop:
			return
		case on := <-c:
			if on == applied {
				continue
			}
			if err := l.Key(on); err != nil {
				m.env.Logger().Debug("allstar: key", "on", on, "err", err)
				continue
			}
			applied = on
		}
	}
}

// waitReady holds a link until the registrar says DNS is ready, up to
// linkWait. It returns false when ctx ends.
func (m *Mode) waitReady(ctx context.Context) bool {
	m.mu.Lock()
	r := m.reg
	m.mu.Unlock()
	if r == nil || r.Ready() || m.linkWait <= 0 {
		return ctx.Err() == nil
	}
	m.SetState(mode.StateConnecting, "waiting for registration", "")
	deadline := time.NewTimer(m.linkWait)
	defer deadline.Stop()
	tick := time.NewTicker(m.opt.ReadyPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			m.env.Logger().Warn("allstar: registration not ready, dialing anyway", "node", m.node)
			return true
		case <-tick.C:
			if r.Ready() {
				return true
			}
		}
	}
}

func (m *Mode) run(ctx context.Context, t mode.Target) {
	defer m.wg.Done()
	delay := m.opt.RetryMin
	for {
		if !m.waitReady(ctx) {
			return
		}
		m.SetState(mode.StateConnecting, "linking to "+t.ID, "")
		err := m.linkOnce(ctx, t)
		if ctx.Err() != nil {
			return
		}
		msg := "link to " + t.ID + " dropped"
		if err != nil {
			msg = fmt.Sprintf("link to %s: %v", t.ID, err)
			if errors.Is(err, iax2.ErrHungUp) || errors.Is(err, iax2.ErrRejected) {
				msg += m.registrationHint()
			}
		}
		m.env.Logger().Warn("allstar: "+msg, "node", m.node)
		if farEnded(err) {
			// The far node dropped us on purpose (a disconnect string or a
			// hangup). Like app_rpt we do not redial: the far operator must
			// be able to end the link. The node clears the saved target
			// when it sees the mode ready with none.
			m.SetTarget(nil)
			m.SetState(mode.StateReady, "node "+m.node, msg)
			return
		}
		m.SetState(mode.StateError, "", msg)
		if !m.retry || errors.Is(err, iax2.ErrRejected) || errors.Is(err, iax2.ErrAuth) || errors.Is(err, iax2.ErrNodeNotFound) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay *= 2
		if delay > m.opt.RetryMax {
			delay = m.opt.RetryMax
		}
	}
}

// newRegistration is how long after registering other nodes may still
// refuse us: they only learn our address when their node lists update.
const newRegistration = 10 * time.Minute

// registrationHint explains a refused or dropped link when our registration
// is recent, or returns "".
func (m *Mode) registrationHint() string {
	m.mu.Lock()
	r := m.reg
	m.mu.Unlock()
	if r == nil {
		return ""
	}
	since := r.Status().Since
	if since.IsZero() {
		return ""
	}
	age := m.env.Clock()().Sub(since)
	if age < 0 || age >= newRegistration {
		return ""
	}
	return fmt.Sprintf(" (registered %d min ago; other nodes may refuse calls for up to ~10 min until their node lists update)", int(age/time.Minute))
}

// linkStats converts call counters for the status.
func linkStats(st iax2.Stats) *mode.LinkStats {
	return &mode.LinkStats{
		RxVoiceFull: st.RxVoiceFull, RxVoiceMini: st.RxVoiceMini, TxVoice: st.TxVoice,
		VNAKSent: st.VNAKSent, VNAKRecv: st.VNAKRecv, Retransmits: st.Retransmits,
		ParseErrors: st.ParseErrors, Dropped: st.Dropped, Gaps: st.Gaps,
	}
}

// statsLink is the optional part of a Link that reports counters.
type statsLink interface{ Stats() iax2.Stats }

// farEnded reports whether err means the far node ended the link itself,
// as opposed to a transport failure or a timeout worth retrying.
func farEnded(err error) bool {
	return errors.Is(err, iax2.ErrDisconnected) || errors.Is(err, iax2.ErrHungUp)
}

// linkOnce dials and pumps one call until it ends or ctx ends.
func (m *Mode) linkOnce(ctx context.Context, t mode.Target) error {
	addr, err := m.opt.Resolve(ctx, t.ID)
	if err != nil {
		return err
	}
	cfg := iax2.Config{LocalNode: m.node, RemoteNode: t.ID, Addr: addr,
		Secret: m.password, Callsign: m.callsign, Logger: m.env.Logger()}
	if m.iaxPort > 0 {
		cfg.ListenAddr = netip.AddrPortFrom(netip.IPv4Unspecified(), uint16(m.iaxPort))
	}
	l, err := m.opt.Dial(ctx, cfg)
	if err != nil {
		return err
	}
	keyc, stop := make(chan bool, 1), make(chan struct{})
	var kwg sync.WaitGroup
	kwg.Add(1)
	go func() { defer kwg.Done(); m.keyer(l, keyc, stop) }()
	m.mu.Lock()
	m.link = l
	m.keyed = false
	m.keyc = keyc
	m.mu.Unlock()
	m.env.Logger().Info("allstar: linked", "node", m.node, "to", t.ID)
	m.SetState(mode.StateConnected, "linked to "+t.ID, "")
	defer func() {
		m.mu.Lock()
		m.link = nil
		m.keyed = false
		m.keyc = nil
		m.mu.Unlock()
		// The call has ended (or Hangup returned), so a Key in progress
		// returns and the keyer sees stop.
		close(stop)
		kwg.Wait()
		m.TalkEnd()
	}()
	recv, events := l.Recv(), l.Events()
	var statTick <-chan time.Time
	sl, hasStats := l.(statsLink)
	if hasStats {
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		statTick = tk.C
		defer func() {
			m.SetLinkStats(linkStats(sl.Stats()))
		}()
	}
	for {
		select {
		case <-statTick:
			m.SetLinkStats(linkStats(sl.Stats()))
		case <-ctx.Done():
			_ = l.Hangup()
			return nil
		case f, ok := <-recv:
			if !ok {
				recv = nil
				continue
			}
			m.Deliver(f)
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			m.handle(t, ev)
		case <-l.Done():
			if err := l.Err(); err != nil {
				return err
			}
			return errors.New("call ended")
		}
	}
}

func (m *Mode) handle(t mode.Target, ev iax2.Event) {
	switch ev.Kind {
	case iax2.EventRxKey:
		if ev.Keyed {
			m.TalkStart(mode.Talker{Callsign: t.ID, Name: t.Name, Target: t.ID})
		} else {
			m.TalkEnd()
		}
	case iax2.EventLinkList:
		var l []string
		for _, e := range ev.Links {
			l = append(l, e.String())
		}
		m.SetLinks(l)
		m.Emit(mode.Event{Kind: mode.EventState, State: mode.StateConnected, Text: "links changed"})
	case iax2.EventText:
		if ev.Text.Body != "" || ev.Text.Raw != "" {
			txt := ev.Text.Body
			if txt == "" {
				txt = ev.Text.Raw
			}
			m.Emit(mode.Event{Kind: mode.EventText, Text: txt})
		}
	}
}
