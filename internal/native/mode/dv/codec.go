package dv

import (
	"github.com/w1jar/nodespot/internal/dv/imbe"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
)

// codecLen is the frame size of c in bytes, 0 for an unknown codec.
func codecLen(c network.Codec) int {
	switch c {
	case network.CodecAMBE:
		return vocoder.AMBEBytes
	case network.CodecIMBE:
		return vocoder.IMBEBytes
	case network.CodecC2:
		return vocoder.C2Bytes
	}
	return 0
}

// imbeState is the IMBE codec of one direction, replaced at every new call
// because the codec is stateful. Only that direction's worker uses it.
type imbeState struct {
	newf func() vocoder.IMBE
	call uint64
	c    vocoder.IMBE
}

func (s *imbeState) get(call uint64) vocoder.IMBE {
	if s.c == nil || s.call != call {
		s.c, s.call = s.newf(), call
	}
	return s.c
}

// encode encodes one frame. A codec that panics is dropped (the next frame
// gets a fresh one) and the frame becomes silence.
func (s *imbeState) encode(call uint64, pcm *[vocoder.PCMSamples]int16) (out [vocoder.IMBEBytes]byte) {
	defer func() {
		if recover() != nil {
			s.c = nil
			out = silenceIMBE
		}
	}()
	return s.get(call).Encode(pcm)
}

func (s *imbeState) decode(call uint64, f *[vocoder.IMBEBytes]byte) (out [vocoder.PCMSamples]int16) {
	defer func() {
		if recover() != nil {
			s.c = nil
			out = [vocoder.PCMSamples]int16{}
		}
	}()
	return s.get(call).Decode(f)
}

// silenceIMBE is a fresh encoder's encoding of digital silence.
var silenceIMBE = func() [vocoder.IMBEBytes]byte {
	var pcm [vocoder.PCMSamples]int16
	return imbe.NewCodec().Encode(&pcm)
}()

// c2State is imbeState for Codec2 3200.
type c2State struct {
	newf func() vocoder.Codec2
	call uint64
	c    vocoder.Codec2
}

func (s *c2State) get(call uint64) vocoder.Codec2 {
	if s.c == nil || s.call != call {
		s.c, s.call = s.newf(), call
	}
	return s.c
}

func (s *c2State) encode(call uint64, pcm *[vocoder.PCMSamples]int16) (out [vocoder.C2Bytes]byte) {
	defer func() {
		if recover() != nil {
			s.c = nil
			out = silenceC2
		}
	}()
	return s.get(call).Encode(pcm)
}

func (s *c2State) decode(call uint64, f *[vocoder.C2Bytes]byte) (out [vocoder.PCMSamples]int16) {
	defer func() {
		if recover() != nil {
			s.c = nil
			out = [vocoder.PCMSamples]int16{}
		}
	}()
	return s.get(call).Decode(f)
}

// silenceC2 is a fresh encoder's encoding of digital silence.
var silenceC2 = func() [vocoder.C2Bytes]byte {
	var pcm [vocoder.PCMSamples]int16
	return vocoder.NewCodec2().Encode(&pcm)
}()
