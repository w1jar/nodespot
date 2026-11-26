package radio

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/radio/alsa"
)

// Parts are the hardware pieces a [Radio] is built from. Open assembles the
// real ones; tests supply fakes.
type Parts struct {
	Capture  FrameSource
	Playback FrameSink
	PTT      Keyer
	Mixer    Mixer
}

// FrameSource delivers captured frames; ReadFrame blocks and returns an error
// once closed.
type FrameSource interface {
	ReadFrame() (Frame, error)
	Close() error
}

// FrameSink plays frames; Stop discards queued audio.
type FrameSink interface {
	WriteFrame(Frame) error
	Stop() error
	Close() error
}

// Keyer is the PTT output and COS input.
type Keyer interface {
	SetPTT(bool) error
	PTT() bool
	COS() bool
	Changes() <-chan bool
	Close() error
}

// Mixer is the sound chip's volume control. It may be nil.
type Mixer interface {
	SetCaptureVolume(float64) error
	SetPlaybackVolume(float64) error
	CaptureVolume() (float64, error)
	PlaybackVolume() (float64, error)
	Close() error
}

// BoostMixer is implemented by a [Mixer] whose chip may have a microphone
// boost control (the alsa mixer does).
type BoostMixer interface {
	BoostAvailable() bool
	Boost() (bool, error)
	SetBoost(bool) error
}

// xruns is implemented by the real stream types.
type xrunCounter interface{ Xruns() uint64 }

const (
	captureQueue = 50 // one second
	playQueue    = 25 // half a second
	idleStop     = 100 * time.Millisecond
)

// core is the Radio built on Parts.
type core struct {
	p Parts

	capCh chan Frame
	cosCh chan bool
	play  chan Frame

	rx, tx    audio.Meter
	droppedRx atomic.Uint64
	droppedTx atomic.Uint64

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
	err  error
}

// New builds a Radio on parts and starts its goroutines.
func New(p Parts) Radio {
	c := &core{
		p:     p,
		capCh: make(chan Frame, captureQueue),
		cosCh: make(chan bool, 16),
		play:  make(chan Frame, playQueue),
		stop:  make(chan struct{}),
	}
	c.wg.Add(3)
	go c.captureLoop()
	go c.playLoop()
	go c.cosLoop()
	return c
}

func (c *core) Capture() <-chan Frame { return c.capCh }
func (c *core) COS() <-chan bool      { return c.cosCh }
func (c *core) Carrier() bool         { return c.p.PTT.COS() }
func (c *core) PTT() bool             { return c.p.PTT.PTT() }

func (c *core) captureLoop() {
	defer c.wg.Done()
	for {
		f, err := c.p.Capture.ReadFrame()
		if err != nil {
			if errors.Is(err, alsa.ErrClosed) || c.closing() {
				return
			}
			// Transient device error: back off and try again.
			select {
			case <-c.stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		c.rx.Observe(time.Now(), &f)
		select {
		case c.capCh <- f:
		default:
			c.droppedRx.Add(1)
		}
	}
}

func (c *core) playLoop() {
	defer c.wg.Done()
	playing := false
	for {
		var idle <-chan time.Time
		if playing {
			idle = time.After(idleStop)
		}
		select {
		case <-c.stop:
			return
		case f := <-c.play:
			c.tx.Observe(time.Now(), &f)
			if err := c.p.Playback.WriteFrame(f); err != nil && c.closing() {
				return
			}
			playing = true
		case <-idle:
			// Nothing to play for a while: let the device drain so the next
			// burst starts with a clean prefill.
			_ = c.p.Playback.Stop()
			playing = false
		}
	}
}

func (c *core) cosLoop() {
	defer c.wg.Done()
	src := c.p.PTT.Changes()
	for {
		select {
		case <-c.stop:
			return
		case v := <-src:
			for {
				select {
				case c.cosCh <- v:
				default:
					select { // full: drop the oldest
					case <-c.cosCh:
						continue
					default:
					}
				}
				break
			}
		}
	}
}

func (c *core) closing() bool {
	select {
	case <-c.stop:
		return true
	default:
		return false
	}
}

func (c *core) Play(f Frame) {
	select {
	case c.play <- f:
	default:
		c.droppedTx.Add(1)
	}
}

func (c *core) SetPTT(on bool) error {
	if err := c.p.PTT.SetPTT(on); err != nil {
		return err
	}
	if !on {
		// Discard audio that has not gone out yet.
		for {
			select {
			case <-c.play:
				continue
			default:
			}
			break
		}
		return c.p.Playback.Stop()
	}
	return nil
}

func (c *core) SetVolumes(capture, playback float64) error {
	if c.p.Mixer == nil {
		return errors.New("radio: no mixer")
	}
	return errors.Join(c.p.Mixer.SetCaptureVolume(capture), c.p.Mixer.SetPlaybackVolume(playback))
}

// SetBoost implements Booster.
func (c *core) SetBoost(on bool) error {
	if b, ok := c.p.Mixer.(BoostMixer); ok && b.BoostAvailable() {
		return b.SetBoost(on)
	}
	return ErrNoBoost
}

func (c *core) Levels() Levels {
	now := time.Now()
	l := Levels{
		CaptureVolume: -1, PlaybackVolume: -1,
		Rx: c.rx.Snapshot(now), Tx: c.tx.Snapshot(now),
		DroppedRx: c.droppedRx.Load(), DroppedTx: c.droppedTx.Load(),
	}
	if c.p.Mixer != nil {
		if v, err := c.p.Mixer.CaptureVolume(); err == nil {
			l.CaptureVolume = v
		}
		if v, err := c.p.Mixer.PlaybackVolume(); err == nil {
			l.PlaybackVolume = v
		}
	}
	if b, ok := c.p.Mixer.(BoostMixer); ok && b.BoostAvailable() {
		l.BoostAvailable = true
		l.Boost, _ = b.Boost()
	}
	for _, s := range []any{c.p.Capture, c.p.Playback} {
		if x, ok := s.(xrunCounter); ok {
			l.Xruns += x.Xruns()
		}
	}
	return l
}

func (c *core) Close() error {
	c.once.Do(func() {
		close(c.stop)
		// Unkey first, whatever else fails.
		errs := []error{c.p.PTT.SetPTT(false)}
		errs = append(errs, c.p.Capture.Close(), c.p.Playback.Close(), c.p.PTT.Close())
		if c.p.Mixer != nil {
			errs = append(errs, c.p.Mixer.Close())
		}
		c.wg.Wait()
		close(c.capCh)
		close(c.cosCh)
		c.err = errors.Join(errs...)
	})
	return c.err
}
