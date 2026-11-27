package audio

import (
	"math"
	"time"
)

// Per-transmission receive summary: what the tuning page shows after a local
// station has talked, so the operator judges the whole transmission instead
// of a flickering live meter.
const (
	// RxMinSpeech is the least speech a transmission needs for its summary
	// to replace the previous one; a shorter one is a kerchunk.
	RxMinSpeech = 500 * time.Millisecond
	// RxClipSample is the magnitude at which an input sample counts as
	// clipped (a hair under full scale, as converters often stop there).
	RxClipSample = 32700
	// RxAtMaxMarginDb is how close to the AGC's maximum gain counts as
	// sitting at it.
	RxAtMaxMarginDb = 0.5
)

// RxSummary is the level summary of one received transmission. The input
// figures are measured on the audio the AGC is given: after the sound chip's
// level, the PL filter, de-emphasis and the static receive gain, before the
// AGC itself.
type RxSummary struct {
	At         time.Time `json:"at"`
	DurationMs int       `json:"durationMs"`
	// PeakDb is the loudest input sample, dBFS.
	PeakDb float64 `json:"peakDb"`
	// SpeechDb is the input RMS over the frames where the speech gate was
	// open, dBFS.
	SpeechDb float64 `json:"speechDb"`
	// AGCGainDb is the mean AGC gain over those frames (0 with the AGC off).
	AGCGainDb float64 `json:"agcGainDb"`
	// OutDb is the RMS of the same frames after the AGC, dBFS.
	OutDb float64 `json:"outDb"`
	// AGCAtMax: the AGC sat at its maximum gain for more than half of the
	// speech, so the input is too quiet for it to reach its target.
	AGCAtMax bool `json:"agcAtMax"`
	// Clipped counts input samples at or above RxClipSample.
	Clipped int `json:"clipped"`
}

// RxAccum accumulates an RxSummary frame by frame. It runs its own speech
// gate (the AGC's detector) on the input, so it works with the AGC off. Use
// NewRxAccum; it is not safe for concurrent use.
type RxAccum struct {
	g                              gate
	frames, speech, atMax, clipped int
	peak                           int32
	sumIn, sumOut, sumGain         float64
}

// NewRxAccum returns an empty accumulator.
func NewRxAccum() *RxAccum { a := &RxAccum{}; a.Reset(); return a }

// Reset starts a new transmission.
func (a *RxAccum) Reset() { *a = RxAccum{}; a.g.reset() }

// Add counts one frame: in is the frame the AGC was given, out what it
// produced (the same frame when the AGC is off), gainDb the AGC's gain for
// it and maxGainDb its limit.
func (a *RxAccum) Add(in, out *Frame, gainDb, maxGainDb float64, agcOn bool) {
	var sqIn, sqOut float64
	for i, s := range in {
		v := int32(s)
		if v < 0 {
			v = -v
		}
		if v > a.peak {
			a.peak = v
		}
		if v >= RxClipSample {
			a.clipped++
		}
		sqIn += float64(s) * float64(s)
		o := float64(out[i])
		sqOut += o * o
	}
	a.frames++
	level := linToDb(math.Sqrt(sqIn/FrameSamples) / 32768)
	if a.g.update(level) != gateOpen {
		return
	}
	a.speech++
	a.sumIn += sqIn
	a.sumOut += sqOut
	if agcOn {
		a.sumGain += gainDb
		if gainDb >= maxGainDb-RxAtMaxMarginDb {
			a.atMax++
		}
	}
}

// Summary returns the summary so far, stamped at. ok is false when there was
// less than RxMinSpeech of speech.
func (a *RxAccum) Summary(at time.Time) (s RxSummary, ok bool) {
	if a.speech == 0 || time.Duration(a.speech)*FrameDuration < RxMinSpeech {
		return RxSummary{}, false
	}
	n := float64(a.speech * FrameSamples)
	s = RxSummary{
		At: at, DurationMs: a.frames * int(FrameDuration/time.Millisecond),
		PeakDb:    linToDb(float64(a.peak) / 32768),
		SpeechDb:  linToDb(math.Sqrt(a.sumIn/n) / 32768),
		AGCGainDb: a.sumGain / float64(a.speech),
		OutDb:     linToDb(math.Sqrt(a.sumOut/n) / 32768),
		AGCAtMax:  a.atMax*2 > a.speech,
		Clipped:   a.clipped,
	}
	return s, true
}

// SpeechDuration is how much speech (gate open) has been counted.
func (a *RxAccum) SpeechDuration() time.Duration { return time.Duration(a.speech) * FrameDuration }
