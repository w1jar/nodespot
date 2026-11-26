package iax2

import "github.com/w1jar/nodespot/internal/native/audio"

// Frame is 20 ms of signed 16-bit linear PCM, mono, 8000 Hz, shared with
// the rest of the native node.
type Frame = audio.Frame

const (
	// SampleRate is the audio sample rate in Hz.
	SampleRate = audio.SampleRate
	// FrameSamples is the number of samples in one Frame.
	FrameSamples = audio.FrameSamples
	// FrameDuration is the length of one Frame.
	FrameDuration = audio.FrameDuration
	frameMS       = 20
)
