// Package modetest has a fake Mode for testing the node.
package modetest

import (
	"context"
	"sync"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// Fake is a Mode that records what the node does to it and lets a test
// play network audio. It is safe for concurrent use.
type Fake struct {
	mode.Base

	mu        sync.Mutex
	started   int
	stopped   int
	running   bool
	target    *mode.Target
	sent      []audio.Frame
	txEnds    int
	connects  []mode.Target
	discs     int
	keyed     bool
	ConnectFn func(mode.Target) error // optional: Connect's result
	dir       []mode.DirEntry         // the station directory (SetDirectory)
	dirLoaded bool
}

var _ mode.Searcher = (*Fake)(nil)

// SetDirectory gives the fake a station directory to search; nil makes it
// not loaded again.
func (f *Fake) SetDirectory(list []mode.DirEntry) {
	f.mu.Lock()
	f.dir, f.dirLoaded = list, list != nil
	f.mu.Unlock()
}

// SearchDirectory implements mode.Searcher with mode.SearchEntries.
func (f *Fake) SearchDirectory(_ context.Context, q string, limit int) (mode.DirResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirLoaded {
		return mode.DirResult{Results: []mode.DirEntry{}}, nil
	}
	return mode.DirResult{Loaded: true, Results: mode.SearchEntries(f.dir, q, limit)}, nil
}

// New returns a Fake named name.
func New(name string) *Fake {
	f := &Fake{}
	f.Init(name, name, nil)
	return f
}

// Start implements mode.Mode.
func (f *Fake) Start(context.Context) error {
	f.mu.Lock()
	f.started++
	f.running = true
	f.mu.Unlock()
	f.SetState(mode.StateReady, "", "")
	return nil
}

// Connect implements mode.Mode.
func (f *Fake) Connect(_ context.Context, t mode.Target) error {
	if f.ConnectFn != nil {
		if err := f.ConnectFn(t); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.connects = append(f.connects, t)
	c := t
	f.target = &c
	f.mu.Unlock()
	f.SetTarget(&c)
	f.SetState(mode.StateConnected, t.ID, "")
	return nil
}

// Disconnect implements mode.Mode.
func (f *Fake) Disconnect(context.Context) error {
	f.mu.Lock()
	f.discs++
	f.target = nil
	f.mu.Unlock()
	f.SetTarget(nil)
	f.SetState(mode.StateReady, "", "")
	return nil
}

// Stop implements mode.Mode.
func (f *Fake) Stop() error {
	f.mu.Lock()
	f.stopped++
	f.running = false
	f.target = nil
	f.mu.Unlock()
	f.Unkey()
	f.SetTarget(nil)
	f.SetState(mode.StateIdle, "", "")
	return nil
}

// Send implements mode.Mode.
func (f *Fake) Send(fr audio.Frame) {
	f.mu.Lock()
	f.sent = append(f.sent, fr)
	f.mu.Unlock()
}

// EndTx implements mode.Mode.
func (f *Fake) EndTx() {
	f.mu.Lock()
	f.txEnds++
	f.mu.Unlock()
}

// Talking implements mode.Mode.
func (f *Fake) Talking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keyed
}

// Key marks the network as talking and queues frames for the radio.
func (f *Fake) Key(callsign string, frames ...audio.Frame) {
	f.mu.Lock()
	f.keyed = true
	f.mu.Unlock()
	f.TalkStart(mode.Talker{Callsign: callsign})
	for _, fr := range frames {
		f.Deliver(fr)
	}
}

// KeyTalker is Key with every detail of the talker (a DMR ID, a name).
func (f *Fake) KeyTalker(t mode.Talker) {
	f.mu.Lock()
	f.keyed = true
	f.mu.Unlock()
	f.TalkStart(t)
}

// Unkey marks the network as quiet.
func (f *Fake) Unkey() {
	f.mu.Lock()
	was := f.keyed
	f.keyed = false
	f.mu.Unlock()
	if was {
		f.TalkEnd()
	}
}

// Running reports whether Start was called more recently than Stop.
func (f *Fake) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// Counts returns how often Start, Stop, Disconnect and EndTx were called.
func (f *Fake) Counts() (started, stopped, disconnects, txEnds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.stopped, f.discs, f.txEnds
}

// Sent returns a copy of every frame the node sent.
func (f *Fake) Sent() []audio.Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]audio.Frame(nil), f.sent...)
}

// Connects returns every Connect target.
func (f *Fake) Connects() []mode.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mode.Target(nil), f.connects...)
}

var _ mode.Mode = (*Fake)(nil)
