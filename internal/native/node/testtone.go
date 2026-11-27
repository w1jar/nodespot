package node

import (
	"errors"
	"math"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// The transmit test tone: a 1 kHz tone keyed on the radio for a few seconds
// so the operator can set the transmit level (Settings > Station > Audio).
const (
	TestToneHz      = 1000
	DefaultTestTone = 5 * time.Second
	MaxTestTone     = 10 * time.Second
	// testToneGrace is how long past its length the tone may wait for the
	// transmitter to key before it is dropped.
	testToneGrace = 2 * time.Second
)

// Errors of TestTone.
var (
	ErrToneNoRadio      = errors.New("no radio is connected")
	ErrToneTransmitting = errors.New("the node is transmitting: try again when it is quiet")
	ErrToneReceiving    = errors.New("the radio is receiving a signal: try again when the channel is clear")
	ErrToneTalking      = errors.New("the active mode is talking: try again when it is quiet")
	ErrToneLength       = errors.New("the test tone must be 1 to 10 seconds")
)

// testToneLevel is the tone's peak level in dBFS: a sine whose RMS is the
// level network speech goes to the radio at, the network-to-radio AGC
// target when that AGC is on, else the default target plus the transmit
// gain. It never goes above -1 dBFS.
func testToneLevel(p stepParams) float64 {
	rms := float64(DefaultAGCTargetDb) + p.txGainDb
	if p.modeAGC {
		rms = p.txAGC.TargetDb
	}
	return math.Min(rms+3.01, -1) // a sine's peak is 3 dB over its RMS
}

// TestTone keys the radio with a 1 kHz tone for d (0 is DefaultTestTone, at
// most MaxTestTone). It is refused while the node transmits, while the
// radio receives a signal and while the active mode is talking. A local
// station or the network starting during the tone stops it.
func (e *Engine) TestTone(d time.Duration) error {
	if d == 0 {
		d = DefaultTestTone
	}
	if d < time.Second || d > MaxTestTone {
		return ErrToneLength
	}
	if e.o.RadioAbsent {
		return ErrToneNoRadio
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := &e.sx
	switch {
	case s.ptt || s.pttStuck:
		return ErrToneTransmitting
	case s.cos || e.o.Radio.Carrier():
		return ErrToneReceiving
	}
	if in := e.active; in != nil && (in.m.Talking() || len(in.m.Recv()) > 0) {
		return ErrToneTalking
	}
	n := int(d.Seconds() * audio.SampleRate)
	s.tone = audio.NewToneSeq(testToneLevel(e.params), audio.Segment{Freq: TestToneHz, Length: n})
	now := e.now()
	s.toneLimit = now.Add(d + e.params.txOnDelay + testToneGrace)
	e.publishOn(EventTone, true, now)
	return nil
}

// StopTestTone ends the test tone early. It is not an error when none plays.
func (e *Engine) StopTestTone() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sx.tone != nil {
		e.endTone(e.now(), "")
	}
}

// endTone drops the test tone, logging why when why is not empty. Called
// with mu.
func (e *Engine) endTone(now time.Time, why string) {
	e.sx.tone = nil
	if why != "" {
		e.logf(now, "%s", why)
	}
	e.publishOn(EventTone, false, now)
}
