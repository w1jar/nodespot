// Package echotest is the echo test (parrot): a mode with no network that
// records the radio's audio as it reaches a mode (after the radio receive
// AGC), waits about a second after the transmission ends and plays the
// recording back to the radio, through a codec of the operator's choice so
// that the playback sounds like what a network would give. It also reports
// the recording's levels (peak, voiced RMS, clipping) for tuning the radio's
// receive level.
package echotest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// Setting keys.
const (
	KeyCodec = "codec"
	KeyDelay = "playback_delay_ms"
)

const (
	// MaxFrames caps one recording: 60 s of 20 ms frames. Audio after the
	// cap is not recorded.
	MaxFrames = 3000
	// DefaultDelay is the pause between the end of the transmission and
	// the playback.
	DefaultDelay = time.Second
	// Callsign is the pseudo callsign of the playback in the heard list.
	Callsign = "ECHO TEST"
	// queued is how many frames the playback keeps waiting for the node;
	// the node takes one every 20 ms, so the playback follows its pace.
	queued = 3

	// DefaultMD380 is the md380-emu address used when the node gives none.
	DefaultMD380 = "127.0.0.1:2470"
)

// Options are injectable for tests.
type Options struct {
	// AMBE is the vocoder for the ambe codec; default md380-emu at
	// Env.MD380.
	AMBE vocoder.AMBE
	// Tick is the playback interval (20 ms). A negative Tick runs no
	// goroutine: the test calls Step.
	Tick time.Duration
}

type phase int

const (
	idle      phase = iota
	recording       // radio audio is arriving
	waiting         // the transmission ended; coding, then the delay
	playing         // the playback is going to the radio
)

// Mode is the echo test.
type Mode struct {
	mode.Base

	env   mode.Env
	codec string
	delay time.Duration
	opt   Options

	mu      sync.Mutex
	running bool
	ph      phase
	gen     uint64 // bumped by every new recording and by Stop
	rec     []audio.Frame
	an      audio.Analyzer
	endAt   time.Time
	ready   bool // out holds the coded recording
	out     []audio.Frame
	pos     int
	cancel  context.CancelFunc // the coding worker
	own     *vocoder.MD380     // made by the mode, closed when it stops
	wg      sync.WaitGroup
	tickWG  sync.WaitGroup
	tickEnd context.CancelFunc
}

var _ mode.Mode = (*Mode)(nil)

// New builds the echo test from its settings.
func New(env mode.Env, s mode.Settings, opt Options) (*Mode, error) {
	codec, err := parseCodec(s.Get(KeyCodec, CodecPlain))
	if err != nil {
		return nil, err
	}
	ms, err := s.Int(KeyDelay, int(DefaultDelay/time.Millisecond))
	if err != nil {
		return nil, err
	}
	if ms < 0 {
		return nil, errors.New("playback delay must not be negative")
	}
	if opt.Tick == 0 {
		opt.Tick = audio.FrameDuration
	}
	m := &Mode{env: env, codec: codec, delay: time.Duration(ms) * time.Millisecond, opt: opt}
	m.Init(mode.EchoTest, "Echo test", env.Clock())
	return m, nil
}

// Codec is the codec in use.
func (m *Mode) Codec() string { return m.codec }

// Start makes the echo test ready: it is always "connected".
func (m *Mode) Start(context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = true
	m.ph = idle
	if m.opt.Tick > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		m.tickEnd = cancel
		m.tickWG.Add(1)
		go m.tick(ctx, m.opt.Tick)
	}
	m.mu.Unlock()
	m.SetTarget(&mode.Target{ID: "echo", Name: "Echo test"})
	m.SetState(mode.StateConnected, "talk to hear yourself"+m.codecNote(), "")
	return nil
}

func (m *Mode) codecNote() string {
	if m.codec == CodecPlain {
		return ""
	}
	return " (" + m.codec + ")"
}

func (m *Mode) tick(ctx context.Context, d time.Duration) {
	defer m.tickWG.Done()
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Step(m.Now())
		}
	}
}

// Connect does nothing: the echo test has no target.
func (m *Mode) Connect(context.Context, mode.Target) error { return nil }

// Disconnect does nothing.
func (m *Mode) Disconnect(context.Context) error { return nil }

// Stop ends the recording or playback and releases the vocoder.
func (m *Mode) Stop() error {
	m.mu.Lock()
	m.running = false
	end := m.tickEnd
	m.tickEnd = nil
	m.interruptLocked()
	m.ph = idle
	m.rec, m.out = nil, nil
	m.mu.Unlock()
	if end != nil {
		end()
	}
	m.tickWG.Wait()
	m.wg.Wait()
	m.mu.Lock()
	own := m.own
	m.own = nil
	m.mu.Unlock()
	if own != nil {
		_ = own.Close()
	}
	m.Flush()
	m.SetTarget(nil)
	m.SetState(mode.StateIdle, "", "")
	return nil
}

// interruptLocked drops a coding in progress and a playback; the caller
// holds mu. It bumps gen so that a worker's late result is ignored.
func (m *Mode) interruptLocked() {
	m.gen++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	wasPlaying := m.ph == playing
	m.ready, m.out, m.pos = false, nil, 0
	if wasPlaying {
		m.TalkEnd()
	}
	m.Flush()
}

// Send takes one frame of the radio's audio. The first frame of a new
// transmission drops a playback in progress and starts a new recording.
func (m *Mode) Send(f audio.Frame) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	if m.ph != recording {
		m.interruptLocked()
		m.ph = recording
		m.rec = make([]audio.Frame, 0, 256)
		m.an = audio.Analyzer{}
	}
	if len(m.rec) < MaxFrames {
		m.rec = append(m.rec, f)
		m.an.Add(&f)
	}
}

// EndTx ends the recording: it is measured and coded in the background and
// plays back DefaultDelay later.
func (m *Mode) EndTx() {
	m.mu.Lock()
	if !m.running || m.ph != recording {
		m.mu.Unlock()
		return
	}
	now := m.Now()
	rep := m.an.Report(now)
	frames := m.rec
	m.rec = nil
	m.ph = waiting
	m.endAt = now
	m.ready = false
	gen := m.gen
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	m.SetLevels(&rep)
	m.SetState(mode.StateConnected, fmt.Sprintf("recorded %.1f s%s", float64(rep.DurationMs)/1000, m.codecNote()), "")
	go m.code(ctx, gen, frames)
}

// ambeVocoder returns the AMBE vocoder, building the md380-emu client the
// first time. Called by the coding worker.
func (m *Mode) ambeVocoder() ambeCoder {
	if m.opt.AMBE != nil {
		return ambeCoder{v: m.opt.AMBE}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.own == nil {
		addr := m.env.MD380
		if addr == "" {
			addr = DefaultMD380
		}
		m.own = vocoder.NewMD380(addr, m.env.MD380Timeout)
	}
	return ambeCoder{v: m.own, stats: m.own.Stats}
}

// code runs the recording through the codec and hands the result to Step.
func (m *Mode) code(ctx context.Context, gen uint64, in []audio.Frame) {
	defer m.wg.Done()
	c := newCoder(m.codec, m.ambeVocoder)
	out := make([]audio.Frame, len(in))
	failed := 0
	for i, f := range in {
		if ctx.Err() != nil {
			return
		}
		var ok bool
		if out[i], ok = c.Code(ctx, f); !ok {
			failed++
		}
	}
	m.mu.Lock()
	if m.gen != gen || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	m.out, m.ready = out, true
	m.mu.Unlock()
	if failed > 0 {
		// The recording still plays back, without the codec on the
		// frames the vocoder missed.
		msg := fmt.Sprintf("the AMBE vocoder (md380-emu) did not answer for %d of %d frames: it is not running or not reachable. Install or start it on the Modes page", failed, len(in))
		m.env.Logger().Warn("echotest: " + msg)
		m.SetState(mode.StateConnected, "AMBE vocoder not working: playing back as recorded", msg)
	}
}

// Step moves the echo test on at now: once the coded recording is ready
// and the delay has passed the playback starts, and while it plays frames
// go to the node as it takes them. The mode's own ticker calls it every
// 20 ms.
func (m *Mode) Step(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	if m.ph == waiting && m.ready && !now.Before(m.endAt.Add(m.delay)) {
		m.ph, m.pos = playing, 0
		m.TalkStart(mode.Talker{Callsign: Callsign, Since: now})
	}
	if m.ph != playing {
		return
	}
	for m.pos < len(m.out) && len(m.Recv()) < queued {
		m.Deliver(m.out[m.pos])
		m.pos++
	}
	if m.pos >= len(m.out) {
		m.ph, m.out, m.ready = idle, nil, false
		m.TalkEnd()
	}
}

// Phase names the state for tests and the status detail.
func (m *Mode) phaseName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.ph {
	case recording:
		return "recording"
	case waiting:
		return "waiting"
	case playing:
		return "playing"
	}
	return "idle"
}
