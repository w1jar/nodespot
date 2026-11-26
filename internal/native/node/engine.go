package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

var sprintf = fmt.Sprintf

// Options configures an Engine.
type Options struct {
	// Radio is the radio. Required; a node with no radio passes a
	// radio.Fake and RadioAbsent.
	Radio       radio.Radio
	RadioAbsent bool
	// RadioConfig is what the radio was opened with, for SA818 programming.
	// It should be Station.Radio.Wiring() of the config the radio was
	// opened from.
	RadioConfig radio.Config
	// RadioInfo is what radio.Open reported: the interface in use and the
	// chip found. Zero means the legacy case: a SHARI, or none when
	// RadioAbsent.
	RadioInfo radio.Info
	// RadioChanged, when set, is called in its own goroutine after a saved
	// station changes the radio wiring. The radio is opened only once, so
	// the daemon restarts itself to reopen it.
	RadioChanged func()
	// Program programs the SA818 (default radio.ProgramSA818).
	Program func(radio.Config, sa818.Settings) error
	// ConfigPath is the config file; "" keeps changes in memory only.
	ConfigPath string
	// TunePath is the file of remembered tunes (default: tune.json next to
	// ConfigPath; "" with no ConfigPath keeps them in memory only).
	TunePath string
	// Build builds a mode (default mode.Build).
	Build func(name string, env mode.Env, s mode.Settings) (mode.Mode, error)
	// Log is where the node logs (default: discard).
	Log *slog.Logger
	// Now is the clock (default time.Now).
	Now func() time.Time
	// StopTimeout bounds how long a mode switch waits for the old mode.
	StopTimeout time.Duration
}

// instance is one built mode.
type instance struct {
	name     string
	m        mode.Mode
	settings mode.Settings
	env      mode.Env
	running  bool // Start succeeded and Stop has not been called
	cancel   context.CancelFunc
	done     chan struct{} // closed when the event pump and background work end
}

// Engine is the node: the radio, the modes and the one active mode.
type Engine struct {
	o   Options
	log *slog.Logger

	opMu sync.Mutex // serializes selects, saves and reloads

	mu       sync.Mutex
	cfg      *Config
	params   stepParams
	modes    map[string]*instance
	buildErr map[string]string
	tunes    map[string]Tune // what each mode reconnects to (see Tune)
	active   *instance
	since    time.Time
	info     radio.Info
	sx       simplex
	started  time.Time

	heard heardLog
	hub   hub
	wg    sync.WaitGroup
	ctx   context.Context
	stop  context.CancelFunc
}

// New returns an engine with cfg. Call Start to bring the modes up.
func New(cfg *Config, o Options) (*Engine, error) {
	if o.Radio == nil {
		return nil, errors.New("node: no radio")
	}
	if cfg == nil {
		cfg = &Config{Version: ConfigVersion}
	}
	if o.Build == nil {
		o.Build = mode.Build
	}
	if o.Program == nil {
		o.Program = radio.ProgramSA818
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 10 * time.Second
	}
	cfg = cfg.Clone()
	info := o.RadioInfo
	if info.Interface.Preset == "" {
		info.Present, info.SHARI = !o.RadioAbsent, !o.RadioAbsent
		info.Interface, _ = radio.Resolve(radio.PresetAuto, radio.Overrides{}, info.SHARI)
	} else {
		o.RadioAbsent = !info.Present
	}
	e := &Engine{
		info: info, o: o, log: o.Log, cfg: cfg, params: paramsFor(cfg),
		modes: map[string]*instance{}, buildErr: map[string]string{}, tunes: map[string]Tune{},
	}
	if p := o.tunePath(); p != "" {
		t, err := loadTunes(p)
		if err != nil {
			o.Log.Warn("remembered tunes ignored", "err", err)
		}
		e.tunes = t
	}
	e.sx.modeAGC, e.sx.radioAGC = audio.NewAGC(), audio.NewAGC()
	e.ctx, e.stop = context.WithCancel(context.Background())
	return e, nil
}

func (e *Engine) now() time.Time { return e.o.Now() }

// Config returns a copy of the current config.
func (e *Engine) Config() *Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Clone()
}

// Start builds the configured modes, starts their background work and
// restores the active mode and target from the config. A mode that fails
// to come back is logged; the node runs idle.
func (e *Engine) Start(ctx context.Context) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	e.started = e.now()
	cfg := e.cfg
	e.mu.Unlock()
	if err := e.applyVolumes(cfg.Station.Radio); err != nil {
		e.logf(e.now(), "%v", err)
	}
	e.migrate()
	e.mu.Lock()
	cfg = e.cfg
	e.mu.Unlock()
	e.applyModes(cfg)
	if a := cfg.Station.Active; a != nil {
		if err := e.selectLocked(ctx, a.Mode, nil, false); err != nil {
			e.logf(e.now(), "restoring %s: %v", a.Mode, err)
			return err
		}
	}
	return nil
}

// Close stops every mode and unkeys the transmitter. It does not close
// the radio, which belongs to the caller.
func (e *Engine) Close() error {
	e.dropPTT()
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	all := make([]*instance, 0, len(e.modes))
	for _, in := range e.modes {
		all = append(all, in)
	}
	e.modes = map[string]*instance{}
	e.active = nil
	e.mu.Unlock()
	for _, in := range all {
		e.closeInstance(in)
	}
	e.stop()
	e.wg.Wait()
	e.dropPTT()
	return nil
}

// env is what the modes get from the station config.
func (e *Engine) env(c *Config) mode.Env {
	a := c.Station.Advanced
	md := a.MD380
	return mode.Env{
		Callsign:     c.Station.Callsign,
		DMRID:        c.Station.DMRID,
		NXDNID:       c.Station.NXDNID,
		Log:          e.log,
		Now:          e.o.Now,
		MD380:        md,
		MD380Timeout: time.Duration(a.MD380TimeoutMS) * time.Millisecond,
	}
}

func sameEnv(a, b mode.Env) bool {
	return a.Callsign == b.Callsign && a.DMRID == b.DMRID && a.NXDNID == b.NXDNID && a.MD380 == b.MD380 && a.MD380Timeout == b.MD380Timeout
}

// applyModes makes the built modes match c: a mode whose settings or
// environment changed is rebuilt (and, if it was active, restarted on the
// same target); a mode no longer configured is closed. Called with opMu.
func (e *Engine) applyModes(c *Config) {
	env := e.env(c)
	e.mu.Lock()
	var stale []*instance
	restart := ""
	var restartTarget *mode.Target
	for name, in := range e.modes {
		s, ok := c.Modes[name]
		if ok && reflect.DeepEqual(norm(s), norm(in.settings)) && sameEnv(env, in.env) {
			continue
		}
		if !ok && reflect.DeepEqual(norm(in.settings), mode.Settings{}) && sameEnv(env, in.env) {
			continue // built on demand with no settings (the echo test)
		}
		if e.active == in {
			restart = name
			if t := in.m.Status().Target; t != nil {
				c := *t
				restartTarget = &c
			}
			e.active = nil
			e.resetRadioLocked()
		}
		delete(e.modes, name)
		stale = append(stale, in)
	}
	names := make([]string, 0, len(c.Modes))
	for n := range c.Modes {
		names = append(names, n)
	}
	sort.Strings(names)
	e.mu.Unlock()

	for _, in := range stale {
		e.closeInstance(in)
		e.heard.end(in.name, e.now())
	}
	for _, n := range names {
		e.mu.Lock()
		_, have := e.modes[n]
		e.mu.Unlock()
		if !have {
			if _, err := e.build(n, c, env); err != nil {
				e.logf(e.now(), "%s: %v", n, err)
			}
		}
	}
	if restart != "" {
		if err := e.selectLocked(e.ctx, restart, restartTarget, false); err != nil {
			e.logf(e.now(), "restarting %s: %v", restart, err)
		}
	}
}

func norm(s mode.Settings) mode.Settings {
	if len(s.Values) == 0 {
		s.Values = nil
	}
	if len(s.Favorites) == 0 {
		s.Favorites = nil
	}
	if len(s.Reflectors) == 0 {
		s.Reflectors = nil
	}
	return s
}

// build builds mode n from c and starts its event pump and background
// work. Called with opMu.
func (e *Engine) build(n string, c *Config, env mode.Env) (*instance, error) {
	s := c.Modes[n].Clone()
	m, err := e.o.Build(n, env, s)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.buildErr[n] = err.Error()
		return nil, err
	}
	delete(e.buildErr, n)
	ctx, cancel := context.WithCancel(e.ctx)
	in := &instance{name: n, m: m, settings: s, env: env, cancel: cancel, done: make(chan struct{})}
	e.modes[n] = in
	var bg sync.WaitGroup
	if b, ok := m.(mode.Background); ok {
		bg.Add(1)
		go func() { defer bg.Done(); b.RunBackground(ctx) }()
	}
	bg.Add(1)
	go func() { defer bg.Done(); e.pump(ctx, in) }()
	e.wg.Add(1)
	go func() { defer e.wg.Done(); bg.Wait(); close(in.done) }()
	return in, nil
}

// closeInstance stops a mode that is no longer wanted and waits (bounded)
// for its goroutines.
func (e *Engine) closeInstance(in *instance) {
	if in.running {
		e.stopMode(in)
	}
	in.cancel()
	select {
	case <-in.done:
	case <-time.After(e.o.StopTimeout):
		e.log.Warn("mode did not stop in time", "mode", in.name)
	}
}

// stopMode stops in's mode, bounded by StopTimeout.
func (e *Engine) stopMode(in *instance) {
	in.running = false
	done := make(chan error, 1)
	go func() { done <- in.m.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			e.log.Warn("stopping mode", "mode", in.name, "err", err)
		}
	case <-time.After(e.o.StopTimeout):
		e.log.Warn("mode did not stop in time", "mode", in.name)
	}
}

// pump forwards in's events to the last-heard list (when it is the active
// mode) and to subscribers.
func (e *Engine) pump(ctx context.Context, in *instance) {
	evs := in.m.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-evs:
			e.mu.Lock()
			active := e.active == in
			e.mu.Unlock()
			if active {
				switch ev.Kind {
				case mode.EventTalkStart:
					if ev.Talker != nil {
						e.heard.start(in.name, *ev.Talker)
					}
				case mode.EventTalkEnd:
					e.heard.end(in.name, ev.At)
				}
			}
			e.hub.publish(Event{Kind: EventMode, At: ev.At, Mode: &ev})
		}
	}
}

// instanceFor returns the built mode n, building it from the config (with
// empty settings when the config has none, which suits a mode such as the
// echo test). Called with opMu.
func (e *Engine) instanceFor(n string) (*instance, error) {
	e.mu.Lock()
	in := e.modes[n]
	c := e.cfg
	e.mu.Unlock()
	if in != nil {
		return in, nil
	}
	in, err := e.build(n, c, e.env(c))
	if err != nil {
		return nil, fmt.Errorf("%s is not set up: %w", n, err)
	}
	return in, nil
}

// Select makes mode n the active mode, connected to t when t is not nil.
// The old mode is dropped first: networks are never bridged. An empty n
// makes the node idle. The choice is saved so it comes back at boot. With
// no t, a mode that becomes active reconnects to the target it remembers
// (the last one connected), unless the operator disconnected it on
// purpose; an explicit t is remembered once the mode accepts it.
func (e *Engine) Select(ctx context.Context, n string, t *mode.Target) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	return e.selectLocked(ctx, n, t, true)
}

func (e *Engine) selectLocked(ctx context.Context, n string, t *mode.Target, persist bool) error {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "none" {
		n = ""
	}
	var in *instance
	if n != "" {
		var err error
		if in, err = e.instanceFor(n); err != nil {
			return err
		}
	}
	e.mu.Lock()
	old := e.active
	if old != in {
		e.active = nil
		e.resetRadioLocked()
	}
	e.mu.Unlock()

	if old != nil && old != in {
		e.stopMode(old)
		e.heard.end(old.name, e.now())
	}
	if in == nil {
		e.mu.Lock()
		e.since = e.now()
		e.mu.Unlock()
		e.hub.publish(Event{Kind: EventSelect, At: e.now()})
		if persist {
			return e.persistActive(nil)
		}
		return nil
	}
	if !in.running {
		if err := in.m.Start(ctx); err != nil {
			return fmt.Errorf("starting %s: %w", n, err)
		}
		in.running = true
	}
	e.mu.Lock()
	if e.active != in {
		e.active = in
		e.since = e.now()
	}
	e.mu.Unlock()
	if old != in {
		e.hub.publish(Event{Kind: EventSelect, At: e.now(), Text: n})
	}
	explicit := t != nil
	if !explicit && old != in {
		t = e.lastTarget(n) // activated: pick up where it was
	}
	var connErr error
	if t != nil {
		connErr = in.m.Connect(ctx, *t)
		if connErr != nil {
			connErr = fmt.Errorf("connecting %s: %w", n, connErr)
			if !explicit {
				// The remembered target no longer works: the mode is
				// active anyway, and the target stays remembered.
				e.logf(e.now(), "%v", connErr)
				connErr = nil
			}
		}
	}
	var saveErr error
	if persist {
		if explicit && connErr == nil {
			saveErr = e.setTune(n, Tune{Target: cloneTarget(t)})
		}
		saveErr = errors.Join(saveErr, e.persistActive(&Selection{Mode: n}))
	}
	return errors.Join(connErr, saveErr)
}

// resetRadioLocked forgets the link between the radio and the old mode.
// Called with mu when the active mode changes.
func (e *Engine) resetRadioLocked() {
	e.sx.rfSentTo = nil
	e.sx.tone = nil
	e.sx.netStart, e.sx.txLocked = time.Time{}, false
	e.sx.netPlaying, e.sx.prebufAt = false, time.Time{}
	e.sx.modeAGC.Reset()
}

// Disconnect drops the active mode's link. The mode stays active.
func (e *Engine) Disconnect(ctx context.Context) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	in := e.active
	e.mu.Unlock()
	if in == nil {
		return errors.New("no mode is active")
	}
	if err := in.m.Disconnect(ctx); err != nil {
		return fmt.Errorf("disconnecting %s: %w", in.name, err)
	}
	// An explicit disconnect: do not come back to the target.
	return errors.Join(e.setTune(in.name, Tune{Unlinked: true}), e.persistActive(&Selection{Mode: in.name}))
}

// Connect connects the active mode to t.
func (e *Engine) Connect(ctx context.Context, t mode.Target) error {
	e.mu.Lock()
	in := e.active
	e.mu.Unlock()
	if in == nil {
		return errors.New("no mode is active: select one first")
	}
	return e.Select(ctx, in.name, &t)
}

// persistActive saves the active selection. Called with opMu.
func (e *Engine) persistActive(sel *Selection) error {
	e.mu.Lock()
	if reflect.DeepEqual(e.cfg.Station.Active, sel) {
		e.mu.Unlock()
		return nil
	}
	c := e.cfg.Clone()
	c.Station.Active = sel
	e.mu.Unlock()
	return e.commit(c, false)
}

// commit saves c and makes it current; with apply it also rebuilds the
// modes and station settings that changed. Called with opMu.
func (e *Engine) commit(c *Config, apply bool) error {
	if e.o.ConfigPath != "" {
		if err := SaveConfig(e.o.ConfigPath, c); err != nil {
			return err
		}
	}
	e.mu.Lock()
	e.cfg = c.Clone()
	e.params = paramsFor(c)
	e.mu.Unlock()
	if apply {
		e.applyModes(c)
	}
	e.hub.publish(Event{Kind: EventConfig, At: e.now()})
	return nil
}

// Heard returns up to n calls, newest first.
func (e *Engine) Heard(n int) []mode.Heard { return e.heard.list(n) }

// NowCalling returns who is talking on the active mode, or nil.
func (e *Engine) NowCalling() *Calling {
	e.mu.Lock()
	in := e.active
	e.mu.Unlock()
	if in == nil {
		return nil
	}
	st := in.m.Status()
	if st.Talker == nil {
		return nil
	}
	return &Calling{Mode: in.name, Talker: *st.Talker, Target: st.Target}
}

// Calling is who is talking now.
type Calling struct {
	Mode   string       `json:"mode"`
	Talker mode.Talker  `json:"talker"`
	Target *mode.Target `json:"target,omitempty"`
}

// Subscribe returns a channel of node events and a function to stop.
func (e *Engine) Subscribe() (<-chan Event, func()) {
	c := e.hub.subscribe()
	return c, func() { e.hub.unsubscribe(c) }
}
