package audio

import "math"

// Source produces frames of generated or stored audio (tones, CW, WAV files).
type Source interface {
	// Next fills f with the next 20 ms. It returns false once the source is
	// finished; the part of f after the end is silence, and a final partial
	// frame is returned with true first, then false on the next call.
	Next(f *Frame) bool
}

// Drain renders a whole source into a slice of samples (for tests and short
// sounds).
func Drain(s Source) []int16 {
	var out []int16
	var f Frame
	for s.Next(&f) {
		out = append(out, f[:]...)
	}
	return out
}

// DBFS converts a level in dBFS to a peak amplitude in 16-bit counts.
func DBFS(db float64) float64 { return dbToLin(db) * 32767 }

// osc is a phase-continuous sine oscillator.
type osc struct{ phase float64 }

func (o *osc) next(freq float64) float64 {
	v := math.Sin(o.phase)
	o.phase += 2 * math.Pi * freq / SampleRate
	if o.phase > 2*math.Pi {
		o.phase -= 2 * math.Pi
	}
	return v
}

// Segment is one tone of a sequence; Freq 0 is silence.
type Segment struct {
	Freq   float64 // Hz
	Length int     // samples
}

// ms converts milliseconds to samples.
func ms(n int) int { return n * SampleRate / 1000 }

// rampSamples is the attack/decay time of a tone, to avoid clicks (4 ms).
const rampSamples = 32

// ToneSeq plays a sequence of tones at a fixed level.
type ToneSeq struct {
	segs []Segment
	amp  float64
	i    int // segment
	pos  int // sample within segment
	o    osc
}

// NewToneSeq returns a source for segs at levelDb dBFS.
func NewToneSeq(levelDb float64, segs ...Segment) *ToneSeq {
	return &ToneSeq{segs: segs, amp: DBFS(levelDb)}
}

// Next implements Source.
func (t *ToneSeq) Next(f *Frame) bool {
	*f = Frame{}
	if t.i >= len(t.segs) {
		return false
	}
	for n := 0; n < FrameSamples && t.i < len(t.segs); {
		s := t.segs[t.i]
		if t.pos >= s.Length {
			t.i++
			t.pos = 0
			continue
		}
		if s.Freq > 0 {
			env := envelope(t.pos, s.Length)
			f[n] = int16(math.Round(t.o.next(s.Freq) * t.amp * env))
		}
		t.pos++
		n++
		if t.pos >= s.Length {
			t.i++
			t.pos = 0
		}
	}
	return true
}

// envelope is the click-free attack/decay gain at sample pos of n.
func envelope(pos, n int) float64 {
	r := rampSamples
	if r > n/2 {
		r = n / 2
	}
	switch {
	case r == 0:
		return 1
	case pos < r:
		return 0.5 - 0.5*math.Cos(math.Pi*float64(pos)/float64(r))
	case pos >= n-r:
		return 0.5 - 0.5*math.Cos(math.Pi*float64(n-1-pos)/float64(r))
	}
	return 1
}
