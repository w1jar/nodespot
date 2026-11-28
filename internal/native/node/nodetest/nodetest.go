// Package nodetest runs a node engine with fake modes and a fake radio,
// served on a real control socket, for the app's tests and its demo mode.
// Nothing reaches a network or a device.
package nodetest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	_ "github.com/w1jar/nodespot/internal/native/mode/all" // the mode specs (fields, titles)
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// Rig is a running fake node.
type Rig struct {
	Engine *node.Engine
	Radio  *radio.Fake
	// Socket is the control socket's path, ConfigPath the node config the
	// engine saves to.
	Socket     string
	ConfigPath string

	mu     sync.Mutex
	fakes  map[string]*modetest.Fake
	onNew  func(*modetest.Fake)
	cancel context.CancelFunc
	done   chan struct{}
	tmp    string
}

// Options configure Start.
type Options struct {
	// Dir holds the socket and the config ("" makes a short temporary
	// directory, removed by Close: a Unix socket path must be short).
	Dir string
	// OnBuild is called for every fake mode the engine builds.
	OnBuild func(*modetest.Fake)
	Log     *slog.Logger
	// Modem, when set, is the MMDVM modem the engine reports (see
	// [DemoModem]).
	Modem node.Modem
}

// Start builds the engine from cfg (nil: an empty config), starts it and
// serves its control socket.
func Start(cfg *node.Config, o Options) (*Rig, error) {
	if cfg == nil {
		cfg = &node.Config{Version: node.ConfigVersion, Modes: map[string]mode.Settings{}}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	r := &Rig{Radio: radio.NewFake(), fakes: map[string]*modetest.Fake{}, onNew: o.OnBuild, done: make(chan struct{})}
	dir := o.Dir
	if dir == "" {
		d, err := os.MkdirTemp("", "nodespot-nd")
		if err != nil {
			return nil, fmt.Errorf("nodetest: %w", err)
		}
		dir, r.tmp = d, d
	}
	r.Socket = filepath.Join(dir, "control.sock")
	r.ConfigPath = filepath.Join(dir, "node.json")
	e, err := node.New(cfg, node.Options{
		Radio: r.Radio, ConfigPath: r.ConfigPath, Log: o.Log, StopTimeout: time.Second,
		Modem: o.Modem, ModemConfig: cfg.Station.Radio.MMDVM.Resolved(),
		// Like the real builder: an unknown mode or settings that fail
		// the spec's check are refused.
		Build: func(n string, env mode.Env, s mode.Settings) (mode.Mode, error) {
			sp, ok := mode.Lookup(n)
			if !ok {
				return nil, fmt.Errorf("unknown mode %q", n)
			}
			if err := sp.CheckEnv(env, s); err != nil {
				return nil, err
			}
			f := modetest.New(n)
			r.mu.Lock()
			r.fakes[n] = f
			cb := r.onNew
			r.mu.Unlock()
			if cb != nil {
				cb(f)
			}
			return f, nil
		},
	})
	if err != nil {
		r.cleanup()
		return nil, fmt.Errorf("nodetest: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.Start(ctx); err != nil {
		cancel()
		r.cleanup()
		return nil, fmt.Errorf("nodetest: %w", err)
	}
	ln, err := node.Listen(r.Socket)
	if err != nil {
		cancel()
		e.Close()
		r.cleanup()
		return nil, fmt.Errorf("nodetest: %w", err)
	}
	r.Engine, r.cancel = e, cancel
	srv := node.NewServer(e, node.ServerOptions{Log: o.Log})
	go func() {
		defer close(r.done)
		_ = srv.Serve(ctx, ln)
	}()
	return r, nil
}

// Client returns a client for the rig's socket.
func (r *Rig) Client() *node.Client { return node.NewClient(r.Socket) }

// Fake returns the fake mode named n, or nil while the engine has not
// built it.
func (r *Rig) Fake(n string) *modetest.Fake {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakes[n]
}

// Close stops serving, closes the engine and removes a temporary
// directory.
func (r *Rig) Close() {
	r.cancel()
	<-r.done
	r.Engine.Close()
	r.cleanup()
}

func (r *Rig) cleanup() {
	if r.tmp != "" {
		os.RemoveAll(r.tmp)
	}
}
