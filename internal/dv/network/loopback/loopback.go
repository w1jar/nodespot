// Package loopback is the echo test network (mode LOOP): it records the AMBE
// frames of one transmission, waits, then plays them back as a received call
// from the pseudo callsign "ECHO TEST", with no talkgroup. It proves both
// audio directions on a live node and never touches a network.
package loopback

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
)

const (
	// Callsign is the source of the playback call (ctl.EchoTestCall). The
	// call has no talkgroup and no slot, so it cannot be taken for a real
	// network's call (BrandMeister's Parrot is TG 9990 there).
	Callsign = ctl.EchoTestCall

	// MaxFrames caps one recording (60 s of 20 ms frames).
	MaxFrames = 3000
	// DefaultDelay is the pause between the end of the transmission and
	// the playback.
	DefaultDelay = time.Second
	// FrameInterval paces the playback like a real network would.
	FrameInterval = 20 * time.Millisecond

	// OptPlaybackDelayMS overrides DefaultDelay (tests).
	OptPlaybackDelayMS = "playback_delay_ms"
)

func init() {
	network.Register(ctl.ModeLoop, func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
		return New(cfg), nil
	})
}

// Loopback is the echo test network.
type Loopback struct {
	enabled bool
	delay   time.Duration

	mu      sync.Mutex
	sink    network.Sink
	running bool
	rec     [][]byte
	recCall uint64
	cancel  context.CancelFunc // current playback
	playing chan struct{}      // closed when the playback goroutine exits
	runCtx  context.Context
	nextID  uint64
	wg      sync.WaitGroup
}

// New builds the echo test from its config.
func New(cfg ctl.NetConfig) *Loopback {
	l := &Loopback{enabled: cfg.Enabled, delay: DefaultDelay, nextID: 1 << 32}
	if v, err := strconv.Atoi(cfg.Options[OptPlaybackDelayMS]); err == nil && v >= 0 {
		l.delay = time.Duration(v) * time.Millisecond
	}
	return l
}

func (l *Loopback) Mode() string                           { return ctl.ModeLoop }
func (l *Loopback) Codec() network.Codec                   { return network.CodecAMBE }
func (l *Loopback) Tune(context.Context, ctl.Target) error { return nil }

// Unlink drops the recording and stops a playback in progress.
func (l *Loopback) Unlink(context.Context) error {
	l.stopPlayback()
	l.mu.Lock()
	l.rec = nil
	l.mu.Unlock()
	return nil
}

// Status reports the echo test; it needs no login.
func (l *Loopback) Status() ctl.NetStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := ctl.NetStatus{Mode: ctl.ModeLoop, Available: true, Enabled: l.enabled, Login: ctl.LoginOff}
	if l.running {
		st.Login = ctl.LoginOK
	}
	return st
}

// Run blocks until ctx ends.
func (l *Loopback) Run(ctx context.Context, s network.Sink) error {
	l.mu.Lock()
	l.sink, l.runCtx, l.running = s, ctx, true
	l.mu.Unlock()
	s.Login(ctl.LoginOK, "")
	<-ctx.Done()
	l.stopPlayback()
	l.wg.Wait()
	l.mu.Lock()
	l.running = false
	l.mu.Unlock()
	return nil
}

// StartTX starts a new recording; a playback in progress is cut.
func (l *Loopback) StartTX(h network.CallHeader) error {
	l.stopPlayback()
	l.mu.Lock()
	l.rec = l.rec[:0]
	l.recCall = h.Call
	l.mu.Unlock()
	return nil
}

// WriteVoice records a frame (up to MaxFrames).
func (l *Loopback) WriteVoice(f network.VoiceFrame) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.rec) < MaxFrames && f.Call == l.recCall {
		l.rec = append(l.rec, append([]byte(nil), f.Data...))
	}
	return nil
}

// EndTX schedules the playback of the recording.
func (l *Loopback) EndTX(call uint64) error {
	l.mu.Lock()
	if call != l.recCall || len(l.rec) == 0 || !l.running {
		l.mu.Unlock()
		return nil
	}
	frames := l.rec
	l.rec = nil
	l.nextID++
	id := l.nextID
	pctx, cancel := context.WithCancel(l.runCtx)
	done := make(chan struct{})
	l.cancel, l.playing = cancel, done
	sink := l.sink
	l.wg.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.wg.Done()
		defer close(done)
		l.play(pctx, sink, id, frames)
	}()
	return nil
}

func (l *Loopback) play(ctx context.Context, sink network.Sink, id uint64, frames [][]byte) {
	if !sleep(ctx, l.delay) {
		return
	}
	sink.Header(network.CallHeader{Call: id, Src: Callsign})
	t := time.NewTicker(FrameInterval)
	defer t.Stop()
	for i, f := range frames {
		if i > 0 {
			select {
			case <-ctx.Done():
				sink.End(id)
				return
			case <-t.C:
			}
		}
		sink.Voice(network.VoiceFrame{Call: id, Seq: uint32(i), Data: f})
	}
	// Hold the end one interval so the last frame keeps its slot.
	sleep(ctx, FrameInterval)
	sink.End(id)
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (l *Loopback) stopPlayback() {
	l.mu.Lock()
	cancel, done := l.cancel, l.playing
	l.cancel, l.playing = nil, nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
