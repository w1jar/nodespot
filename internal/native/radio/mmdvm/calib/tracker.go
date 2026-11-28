// Package calib is the logic of "nodespot-node calibrate": it measures the
// bit errors of received DMR voice while the receive frequency is trimmed,
// so the owner can find the rxOffsetHz that suits the board's crystal. It
// receives only: the driver it uses runs in receive-only mode (RF power 0,
// transmit frames filtered out), and nothing here builds a transmit frame.
package calib

import (
	"math"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// What BER means here. A DMR voice burst carries three AMBE frames of 72
// bits. Of those, 47 bits per frame are FEC protected: C0 (a Golay 24,12
// codeword, 24 bits) and C1 (Golay 23,12, 23 bits). The decoder reports how
// many bits it corrected in C0 and C1; the other 25 bits of a frame have no
// protection, so errors in them cannot be seen. The BER shown is
//
//	corrected bit errors / (voice bursts * BitsPerVoiceBurst)
//
// so it is the bit error rate of the protected bits only, and an estimate of
// the channel quality, not of the whole burst. A frame whose C0 cannot be
// corrected counts as 10 errors (the decoder's convention), so a very bad
// signal reads lower than its true error rate; the figure saturates.
const (
	// BitsPerVoiceBurst is the FEC-protected bits in one voice burst.
	BitsPerVoiceBurst = 3 * (24 + 23)
	// BurstDuration is the air time of one burst.
	BurstDuration = 60 * time.Millisecond
	// RollingWindow is the span of the rolling BER.
	RollingWindow = time.Second
	// CallTimeout ends a call whose bursts stop without a terminator or
	// DMR_LOST, when the driver has not already ended it.
	CallTimeout = 500 * time.Millisecond
	// unusableErrs is the errors per voice burst from which a burst counts
	// as lost: the decoder counts an uncorrectable frame as 10 errors, so
	// 30 is a burst of three of them, which is noise, not speech.
	unusableErrs = 30
)

// CallResult is one finished call.
type CallResult struct {
	Start   time.Time
	Offset  int // the RX offset in force when the call began
	Changed bool
	Src     uint32
	Dst     uint32
	HaveID  bool
	Private bool
	// Bursts counts every DMR burst of the call, VoiceBursts the voice ones.
	Bursts, VoiceBursts int
	// Errs are the corrected bit errors in the voice bursts.
	Errs int
	// Unusable are voice bursts with a frame the FEC could not correct;
	// Missing the bursts the timing says never arrived.
	Unusable, Missing int
	// Lost is true when the call ended with DMR_LOST.
	Lost bool
	// Duration is the air time of the received bursts.
	Duration time.Duration
}

// BER is the bit error rate of the call as a fraction (0.032 is 3.2%), and
// false when it had no voice.
func (c CallResult) BER() (float64, bool) {
	if c.VoiceBursts == 0 {
		return 0, false
	}
	return float64(c.Errs) / float64(c.VoiceBursts*BitsPerVoiceBurst), true
}

// LostFraction is the share of the call's bursts that were unusable or
// missing, 0 to 1.
func (c CallResult) LostFraction() float64 {
	n := c.VoiceBursts + c.Missing
	if n == 0 {
		return 1
	}
	return float64(c.Unusable+c.Missing) / float64(n)
}

// Usable reports whether a sweep step may use the call: at least min of air
// time, and not mostly lost bursts.
func (c CallResult) Usable(min time.Duration) bool {
	_, ok := c.BER()
	return ok && c.Duration >= min && !c.Changed && c.LostFraction() <= 0.5
}

type liveCall struct {
	CallResult
	first, last time.Time
}

type rollPoint struct {
	t    time.Time
	errs int
}

// Tracker turns the driver's RX events into calls and error rates. Its
// Observe method is the driver's RXMonitor.
type Tracker struct {
	mu      sync.Mutex
	offset  int
	cur     *liveCall
	frames  uint64
	lost    uint64
	roll    []rollPoint
	last    *CallResult
	history []CallResult
	calls   chan CallResult
}

// NewTracker returns a tracker for the given starting offset.
func NewTracker(offset int) *Tracker {
	return &Tracker{offset: offset, calls: make(chan CallResult, 64)}
}

// Calls delivers every finished call. A reader that falls far behind loses
// the oldest.
func (t *Tracker) Calls() <-chan CallResult { return t.calls }

// SetOffset records the offset now in force. A call in progress is marked
// as changed, which keeps it out of a sweep.
func (t *Tracker) SetOffset(off int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.offset = off
	if t.cur != nil {
		t.cur.Changed = true
	}
	t.roll = nil
}

// Observe handles one driver event.
func (t *Tracker) Observe(e mmdvm.RXEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch e.Kind {
	case mmdvm.RXBurst:
		t.frames++
		c := t.cur
		if c == nil {
			c = &liveCall{first: e.Time}
			c.Start, c.Offset = e.Time, t.offset
			t.cur = c
		} else if gap := e.Time.Sub(c.last); gap > BurstDuration*3/2 && !c.last.IsZero() {
			c.Missing += int(gap/BurstDuration) - 1
		}
		c.last = e.Time
		c.Bursts++
		if e.Voice {
			c.VoiceBursts++
			c.Errs += e.FECErrors
			if e.FECErrors >= unusableErrs {
				c.Unusable++
			}
			t.roll = append(t.roll, rollPoint{e.Time, e.FECErrors})
			t.trim(e.Time)
		}
	case mmdvm.RXCallStart:
		if t.cur == nil {
			t.cur = &liveCall{first: e.Time, last: e.Time}
			t.cur.Start, t.cur.Offset = e.Time, t.offset
		}
		t.cur.Src, t.cur.Dst, t.cur.Private, t.cur.HaveID = e.Src, e.Dst, e.Private, true
	case mmdvm.RXLost:
		t.lost++
		if t.cur != nil {
			t.cur.Lost = true
			t.finish()
		}
	case mmdvm.RXCallEnd:
		t.finish()
	}
}

// Tick ends a call that has gone silent. Call it from a timer.
func (t *Tracker) Tick(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur != nil && now.Sub(t.cur.last) > CallTimeout {
		t.finish()
	}
	t.trim(now)
}

func (t *Tracker) trim(now time.Time) {
	i := 0
	for i < len(t.roll) && now.Sub(t.roll[i].t) > RollingWindow {
		i++
	}
	t.roll = t.roll[i:]
}

// finish closes the current call. The lock is held.
func (t *Tracker) finish() {
	c := t.cur
	if c == nil {
		return
	}
	t.cur = nil
	if c.Bursts == 0 {
		return
	}
	c.Duration = time.Duration(c.Bursts) * BurstDuration
	r := c.CallResult
	t.last = &r
	t.history = append(t.history, r)
	if len(t.history) > 50 {
		t.history = t.history[1:]
	}
	select {
	case t.calls <- r:
	default:
		select {
		case <-t.calls:
		default:
		}
		select {
		case t.calls <- r:
		default:
		}
	}
}

// Snapshot is what the display shows.
type Snapshot struct {
	Offset int
	Frames uint64
	Lost   uint64
	// Live is the call in progress, if any.
	Live *CallResult
	// Rolling is the BER over the last second, when RollingOK.
	Rolling   float64
	RollingOK bool
	Last      *CallResult
}

// Snapshot returns the state at now.
func (t *Tracker) Snapshot(now time.Time) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trim(now)
	s := Snapshot{Offset: t.offset, Frames: t.frames, Lost: t.lost}
	if t.cur != nil {
		c := t.cur.CallResult
		c.Duration = time.Duration(c.Bursts) * BurstDuration
		s.Live = &c
	}
	if n := len(t.roll); n > 0 {
		e := 0
		for _, p := range t.roll {
			e += p.errs
		}
		s.Rolling, s.RollingOK = float64(e)/float64(n*BitsPerVoiceBurst), true
	}
	if t.last != nil {
		l := *t.last
		s.Last = &l
	}
	return s
}

// History returns the last finished calls, oldest first.
func (t *Tracker) History() []CallResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]CallResult(nil), t.history...)
}

// Percent formats a BER fraction as "3.2%".
func Percent(f float64) string {
	if math.IsNaN(f) {
		return "-"
	}
	return fmtPct(f)
}
