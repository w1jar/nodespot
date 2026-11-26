package radio

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// Fake is an in-memory [Radio] for tests and for a node with no radio. Tests
// feed received audio with Inject, set the squelch with SetCarrier and read
// what the node transmitted with Played. A radioless node uses it with
// nothing injected: it hears silence and never has a carrier.
type Fake struct {
	capCh chan Frame
	cosCh chan bool

	mu      sync.Mutex
	ptt     bool
	cos     bool
	played  []Frame
	pttLog  []bool
	vols    Volumes
	closed  bool
	pttErr  error
	rx, tx  audio.Meter
	dropped atomic.Uint64
}

// NewFake returns a Fake with a default mixer position of 0.5.
func NewFake() *Fake {
	return &Fake{
		capCh: make(chan Frame, captureQueue),
		cosCh: make(chan bool, 16),
		vols:  Volumes{Capture: 0.5, Playback: 0.5},
	}
}

// Inject delivers a received frame. It reports false if the queue was full.
func (f *Fake) Inject(fr Frame) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.rx.Observe(time.Now(), &fr)
	select {
	case f.capCh <- fr:
		return true
	default:
		f.dropped.Add(1)
		return false
	}
}

// SetCarrier sets the squelch state and reports a change on COS.
func (f *Fake) SetCarrier(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.cos == on {
		return
	}
	f.cos = on
	select {
	case f.cosCh <- on:
	default:
	}
}

// Played returns a copy of the frames transmitted so far.
func (f *Fake) Played() []Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Frame(nil), f.played...)
}

// PTTLog returns every PTT state set, in order.
func (f *Fake) PTTLog() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.pttLog...)
}

// Capture implements Radio.
func (f *Fake) Capture() <-chan Frame { return f.capCh }

// COS implements Radio.
func (f *Fake) COS() <-chan bool { return f.cosCh }

// Play implements Radio. It records every frame, keyed or not, so tests can
// check that audio is not sent before PTT; compare with PTTLog.
func (f *Fake) Play(fr Frame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.tx.Observe(time.Now(), &fr)
	f.played = append(f.played, fr)
}

// FailPTT makes every SetPTT fail with err, leaving the PTT as it was,
// until it is called with nil. It stands in for a failed HID write.
func (f *Fake) FailPTT(err error) {
	f.mu.Lock()
	f.pttErr = err
	f.mu.Unlock()
}

// SetPTT implements Radio.
func (f *Fake) SetPTT(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pttErr != nil {
		return f.pttErr
	}
	f.ptt = on
	f.pttLog = append(f.pttLog, on)
	return nil
}

// PTT implements Radio.
func (f *Fake) PTT() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.ptt }

// Carrier implements Radio.
func (f *Fake) Carrier() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.cos }

// Levels implements Radio.
func (f *Fake) Levels() Levels {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	return Levels{
		CaptureVolume: f.vols.Capture, PlaybackVolume: f.vols.Playback,
		Rx: f.rx.Snapshot(now), Tx: f.tx.Snapshot(now), DroppedRx: f.dropped.Load(),
	}
}

// SetVolumes implements Radio.
func (f *Fake) SetVolumes(capture, playback float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vols = Volumes{Capture: capture, Playback: playback}
	return nil
}

// Close implements Radio.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	f.ptt = false
	f.pttLog = append(f.pttLog, false)
	close(f.capCh)
	close(f.cosCh)
	return nil
}
