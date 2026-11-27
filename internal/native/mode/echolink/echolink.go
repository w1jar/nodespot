package echolink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	el "github.com/w1jar/nodespot/internal/native/echolink"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// Directory is the part of *echolink.Directory the mode uses.
type Directory interface {
	Connect(ctx context.Context, target string) (Conn, error)
	Close() error
	Status() el.Status
	// Stations is the directory's station list, downloaded when the
	// cached copy is older than el.Config.ListMaxAge.
	Stations(ctx context.Context) ([]el.Station, error)
}

// Conn is the part of *echolink.Conn the mode uses.
type Conn interface {
	Recv() <-chan el.Frame
	Events() <-chan el.Event
	Done() <-chan struct{}
	Err() error
	Send(f el.Frame) error
	Flush() error
	Close() error
	Station() el.Station
	Status() el.ConnStatus
}

// Options are injectable for tests.
type Options struct {
	// Login logs on to the directory; default echolink.Login.
	Login func(ctx context.Context, cfg el.Config) (Directory, error)
	// Retry is the delay between failed logins (30 s).
	Retry time.Duration
}

type realDir struct{ *el.Directory }

func (d realDir) Connect(ctx context.Context, target string) (Conn, error) {
	c, err := d.Directory.Connect(ctx, target)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Mode is the EchoLink mode.
type Mode struct {
	mode.Base

	env mode.Env
	cfg el.Config
	opt Options

	mu       sync.Mutex
	runCtx   context.Context
	stop     context.CancelFunc // ends the login loop
	loginWG  sync.WaitGroup
	dir      Directory
	pending  *mode.Target
	linkStop context.CancelFunc
	linkWG   sync.WaitGroup
	conn     Conn
	sending  bool
	over0    uint64 // the receive queue's overflow when conn connected
}

var (
	_ mode.Mode     = (*Mode)(nil)
	_ mode.Searcher = (*Mode)(nil)
)

// New builds the mode from its settings.
func New(env mode.Env, s mode.Settings, opt Options) (*Mode, error) {
	call := strings.ToUpper(s.Get(KeyCallsign, ""))
	if call == "" {
		if env.Callsign == "" {
			return nil, errors.New("set the station callsign or an EchoLink callsign")
		}
		call = strings.ToUpper(env.Callsign) + "-L"
	}
	cfg := el.Config{
		Callsign: call,
		Password: s.Get(KeyPassword, ""),
		Name:     s.Get(KeyName, ""),
		Location: s.Get(KeyLocation, ""),
		Info:     s.Get(KeyInfo, ""),
		Logger:   env.Logger(),
	}
	for _, sv := range strings.Split(s.Get(KeyServers, ""), ",") {
		if sv = strings.TrimSpace(sv); sv != "" {
			cfg.Servers = append(cfg.Servers, sv)
		}
	}
	if opt.Login == nil {
		opt.Login = func(ctx context.Context, cfg el.Config) (Directory, error) {
			d, err := el.Login(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return realDir{d}, nil
		}
	}
	if opt.Retry <= 0 {
		opt.Retry = el.DefaultRetry
	}
	m := &Mode{env: env, cfg: cfg, opt: opt}
	m.InitBuffer(mode.EchoLink, "EchoLink", env.Clock(), recvBuffer)
	return m, nil
}

// Start logs on to the directory in the background, retrying until it
// works or the password is refused.
func (m *Mode) Start(context.Context) error {
	m.mu.Lock()
	if m.stop != nil {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.runCtx, m.stop = ctx, cancel
	m.mu.Unlock()
	m.SetState(mode.StateStarting, "logging in to the directory", "")
	m.loginWG.Add(1)
	go m.login(ctx)
	return nil
}

func (m *Mode) login(ctx context.Context) {
	defer m.loginWG.Done()
	for {
		d, err := m.opt.Login(ctx, m.cfg)
		if err == nil {
			m.mu.Lock()
			if ctx.Err() != nil {
				m.mu.Unlock()
				_ = d.Close()
				return
			}
			m.dir = d
			p := m.pending
			m.pending = nil
			m.mu.Unlock()
			st := d.Status()
			m.env.Logger().Info("echolink: logged in", "callsign", m.cfg.Callsign, "server", st.Server)
			m.SetState(mode.StateReady, "logged in as "+m.cfg.Callsign, "")
			if p != nil {
				m.startLink(*p)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		msg := fmt.Sprintf("directory login: %v", err)
		m.env.Logger().Warn("echolink: "+msg, "callsign", m.cfg.Callsign)
		m.SetState(mode.StateError, "", msg)
		if errors.Is(err, el.ErrBadLogon) {
			return // a wrong password will not fix itself
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.opt.Retry):
		}
		m.SetState(mode.StateStarting, "logging in to the directory", "")
	}
}

// Connect calls t.ID, dropping the current connection. Before the
// directory login completes the target is held and called once it does.
func (m *Mode) Connect(_ context.Context, t mode.Target) error {
	t.ID = strings.ToUpper(strings.TrimSpace(t.ID))
	if t.ID == "" {
		return errors.New("echolink: no station given")
	}
	m.mu.Lock()
	if m.stop == nil {
		m.mu.Unlock()
		return errors.New("echolink: mode is not started")
	}
	m.mu.Unlock()
	m.dropLink()
	m.mu.Lock()
	tt := t
	if m.dir == nil {
		m.pending = &tt
		m.mu.Unlock()
		m.SetTarget(&tt)
		m.SetState(mode.StateConnecting, "waiting for the directory login", "")
		return nil
	}
	m.mu.Unlock()
	m.startLink(tt)
	return nil
}

func (m *Mode) startLink(t mode.Target) {
	m.mu.Lock()
	ctx, cancel := context.WithCancel(m.runCtx)
	m.linkStop = cancel
	d := m.dir
	m.mu.Unlock()
	m.SetTarget(&t)
	m.SetLinkStats(nil)
	m.SetState(mode.StateConnecting, "calling "+t.ID, "")
	m.linkWG.Add(1)
	go m.link(ctx, d, t)
}

func (m *Mode) link(ctx context.Context, d Directory, t mode.Target) {
	defer m.linkWG.Done()
	c, err := d.Connect(ctx, t.ID)
	if err != nil {
		if ctx.Err() == nil {
			msg := fmt.Sprintf("call %s: %v", t.ID, err)
			m.env.Logger().Warn("echolink: " + msg)
			m.SetState(mode.StateError, "", msg)
		}
		return
	}
	over0 := m.Overflow()
	m.mu.Lock()
	m.conn = c
	m.sending = false
	m.over0 = over0
	m.mu.Unlock()
	st := c.Station()
	if st.Location != "" && t.Name == "" {
		t.Name = st.Location
		m.SetTarget(&t)
	}
	m.env.Logger().Info("echolink: connected", "station", st.Callsign)
	m.SetState(mode.StateConnected, "connected to "+st.Callsign, "")
	// The counters are the connection's: the receive queue's overflow
	// counts from over0, and from the start of each transmission for its
	// log line.
	stats := func() *mode.LinkStats { return linkStats(c.Status(), m.Overflow()-over0) }
	spurtOver := over0
	m.SetLinkStats(stats())
	defer func() {
		m.mu.Lock()
		m.conn = nil
		m.mu.Unlock()
		m.SetLinkStats(stats())
		m.TalkEnd()
	}()
	statTick := time.NewTicker(statsEvery)
	defer statTick.Stop()
	peerName := ""
	recv, events := c.Recv(), c.Events()
	for {
		select {
		case <-statTick.C:
			m.SetLinkStats(stats())
		case <-ctx.Done():
			_ = c.Close()
			return
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
			switch ev.Kind {
			case el.EventTalkStart:
				name := ""
				if strings.EqualFold(ev.Callsign, st.Callsign) || ev.Callsign == "" {
					name = peerName
				}
				call := ev.Callsign
				if call == "" {
					call = st.Callsign
				}
				m.TalkStart(mode.Talker{Callsign: call, Name: name, Target: t.ID})
				spurtOver = m.Overflow()
			case el.EventTalkStop:
				m.TalkEnd()
				m.SetLinkStats(stats())
				m.env.Logger().Info(fmt.Sprintf("echolink: received %d packets (late %d, dropped %d)",
					ev.Packets, ev.Late, ev.Dropped+m.Overflow()-spurtOver), "station", st.Callsign)
			case el.EventPeer:
				peerName = ev.Name
				m.UpdateTalker("", ev.Name, "")
			case el.EventInfo:
				m.Emit(mode.Event{Kind: mode.EventText, Text: ev.Text})
			case el.EventChat:
				m.Emit(mode.Event{Kind: mode.EventText, Text: ev.Callsign + ": " + ev.Text})
			case el.EventRefused:
				m.env.Logger().Info("echolink: refused an incoming call", "from", ev.Callsign)
			}
		case <-c.Done():
			err := c.Err()
			msg := "station " + st.Callsign + " disconnected"
			if err != nil && !errors.Is(err, el.ErrRemoteBye) {
				msg = fmt.Sprintf("connection to %s: %v", st.Callsign, err)
			}
			m.env.Logger().Info("echolink: " + msg)
			m.SetTarget(nil)
			m.SetState(mode.StateReady, "logged in as "+m.cfg.Callsign, "")
			m.Emit(mode.Event{Kind: mode.EventError, Err: msg})
			return
		}
	}
}

// statsEvery is how often the link counters are copied to the status.
const statsEvery = time.Second

// recvBuffer is the mode's receive queue, in 20 ms frames: thirty seconds,
// so a station (the echo test's parrot) that sends faster than real time
// is queued, not cut short.
const recvBuffer = 1500

// linkStats converts connection counters for the status. overflow is the
// frames the mode's receive queue dropped on this connection, counted with
// the connection's own drops.
func linkStats(st el.ConnStatus, overflow uint64) *mode.LinkStats {
	return &mode.LinkStats{
		Proto:       "echolink",
		RxVoiceFull: st.RxPackets, TxVoice: st.TxPackets,
		ParseErrors: st.RxBad, Dropped: st.RxDropped + overflow, Late: st.RxLate, TxErrors: st.TxErrors,
	}
}

func (m *Mode) dropLink() {
	m.mu.Lock()
	cancel := m.linkStop
	m.linkStop = nil
	m.pending = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.linkWG.Wait()
	m.TalkEnd()
	m.Flush()
}

// Disconnect drops the connection; the directory login stays.
func (m *Mode) Disconnect(context.Context) error {
	m.dropLink()
	m.SetTarget(nil)
	m.mu.Lock()
	in, started := m.dir != nil, m.stop != nil
	m.mu.Unlock()
	if in {
		m.SetState(mode.StateReady, "logged in as "+m.cfg.Callsign, "")
	} else if started {
		m.SetState(mode.StateStarting, "logging in to the directory", "")
	}
	return nil
}

// SearchDirectory searches the directory's station list for the UI's
// target picker. Before the login it reports Loaded false. The list is the
// one Connect resolves targets with: it is downloaded on the first search
// and then reused for el.Config.ListMaxAge.
func (m *Mode) SearchDirectory(ctx context.Context, q string, limit int) (mode.DirResult, error) {
	m.mu.Lock()
	d := m.dir
	m.mu.Unlock()
	if d == nil {
		return mode.DirResult{Results: []mode.DirEntry{}}, nil
	}
	list, err := d.Stations(ctx)
	if err != nil {
		return mode.DirResult{Results: []mode.DirEntry{}, Error: err.Error()}, nil
	}
	return mode.DirResult{
		Loaded:  true,
		Updated: d.Status().ListUpdated,
		Results: mode.SearchEntries(DirEntries(list), q, limit),
	}, nil
}

// DirEntries converts a station list for the UI.
func DirEntries(list []el.Station) []mode.DirEntry {
	out := make([]mode.DirEntry, len(list))
	for i, s := range list {
		st := strings.ToLower(s.Status)
		switch st {
		case "on":
			st = "online"
		case "busy":
			st = "busy"
		}
		out[i] = mode.DirEntry{Node: s.ID, Callsign: s.Callsign, Location: s.Location, Status: st, Kind: s.Kind().String()}
	}
	return out
}

// Stop drops the connection and logs off the directory.
func (m *Mode) Stop() error {
	m.dropLink()
	m.mu.Lock()
	stop := m.stop
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	m.loginWG.Wait()
	m.mu.Lock()
	d := m.dir
	m.dir = nil
	m.mu.Unlock()
	var err error
	if d != nil {
		err = d.Close()
		m.env.Logger().Info("echolink: logged off")
	}
	m.SetTarget(nil)
	m.SetState(mode.StateIdle, "", "")
	return err
}

// Send sends one frame of radio audio.
func (m *Mode) Send(f audio.Frame) {
	m.mu.Lock()
	c := m.conn
	if c != nil {
		m.sending = true
	}
	m.mu.Unlock()
	if c != nil {
		_ = c.Send(f)
	}
}

// EndTx sends the last partial packet.
func (m *Mode) EndTx() {
	m.mu.Lock()
	c := m.conn
	was := m.sending
	m.sending = false
	over0 := m.over0
	m.mu.Unlock()
	if c != nil && was {
		_ = c.Flush()
		m.SetLinkStats(linkStats(c.Status(), m.Overflow()-over0))
	}
}
