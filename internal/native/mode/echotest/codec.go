package echotest

import (
	"context"
	"fmt"
	"strings"

	"github.com/w1jar/nodespot/internal/dv/imbe"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/gsm"
	"github.com/w1jar/nodespot/internal/native/iax2"
)

// Codec names: the values of the "codec" setting.
const (
	CodecPlain  = "plain"  // no codec: 8 kHz linear, as AllStar sounds with ulaw
	CodecAMBE   = "ambe"   // DMR, YSF, NXDN (the software AMBE vocoder)
	CodecIMBE   = "imbe"   // P25
	CodecCodec2 = "codec2" // M17, 3200 mode
	CodecGSM    = "gsm"    // EchoLink
	CodecULaw   = "ulaw"   // AllStar
)

// Codecs lists the codec setting's choices, plain first.
var Codecs = []string{CodecPlain, CodecAMBE, CodecIMBE, CodecCodec2, CodecGSM, CodecULaw}

// coder is one recording's trip through a codec: every frame is encoded
// and decoded again. Codecs are stateful, so each recording gets a new
// coder. A coder is used by one goroutine.
type coder interface {
	// Code returns f after the codec. The frame count never changes: one
	// frame in, one frame out. ok is false when the codec could not do
	// its job for this frame; f comes back unchanged then.
	Code(ctx context.Context, f audio.Frame) (out audio.Frame, ok bool)
}

type plainCoder struct{}

func (plainCoder) Code(_ context.Context, f audio.Frame) (audio.Frame, bool) { return f, true }

type ulawCoder struct{}

func (ulawCoder) Code(_ context.Context, f audio.Frame) (audio.Frame, bool) {
	for i, s := range f {
		f[i] = iax2.ULawDecode(iax2.ULawEncode(s))
	}
	return f, true
}

// guard turns a panic of a codec into "could not code this frame".
func guard(f audio.Frame, fn func() audio.Frame) (out audio.Frame, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = f, false
		}
	}()
	return fn(), true
}

type imbeCoder struct{ c vocoder.IMBE }

func (c imbeCoder) Code(_ context.Context, f audio.Frame) (audio.Frame, bool) {
	return guard(f, func() audio.Frame {
		pcm := [vocoder.PCMSamples]int16(f)
		b := c.c.Encode(&pcm)
		return audio.Frame(c.c.Decode(&b))
	})
}

type codec2Coder struct{ c vocoder.Codec2 }

func (c codec2Coder) Code(_ context.Context, f audio.Frame) (audio.Frame, bool) {
	return guard(f, func() audio.Frame {
		pcm := [vocoder.PCMSamples]int16(f)
		b := c.c.Encode(&pcm)
		return audio.Frame(c.c.Decode(&b))
	})
}

type gsmCoder struct {
	e *gsm.Encoder
	d *gsm.Decoder
}

func (c gsmCoder) Code(_ context.Context, f audio.Frame) (audio.Frame, bool) {
	return guard(f, func() audio.Frame {
		pcm := [gsm.FrameSamples]int16(f)
		b := c.e.Encode(&pcm)
		var out [gsm.FrameSamples]int16
		if err := c.d.Decode(b[:], &out); err != nil {
			panic(err) // guard: keep the frame as it was
		}
		return audio.Frame(out)
	})
}

// ambeCoder codes through the software AMBE vocoder. When it does not answer
// (a timeout, or it is down) the frame is left as it was and the failure
// is counted, so the mode can say the vocoder is not working.
type ambeCoder struct {
	v     vocoder.AMBE
	stats func() vocoder.Stats // nil when the vocoder keeps none
}

func (c ambeCoder) timeouts() uint64 {
	if c.stats == nil {
		return 0
	}
	return c.stats().Timeouts
}

func (c ambeCoder) Code(ctx context.Context, f audio.Frame) (audio.Frame, bool) {
	t0 := c.timeouts()
	pcm := [vocoder.PCMSamples]int16(f)
	b, err := c.v.Encode(ctx, &pcm)
	if err != nil || c.timeouts() != t0 {
		return f, false
	}
	t0 = c.timeouts()
	out, err := c.v.Decode(ctx, b)
	if err != nil || c.timeouts() != t0 {
		return f, false
	}
	return audio.Frame(out), true
}

// parseCodec normalizes a codec setting.
func parseCodec(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return CodecPlain, nil
	}
	for _, c := range Codecs {
		if c == s {
			return c, nil
		}
	}
	return "", fmt.Errorf("codec %q is not one of %s", s, strings.Join(Codecs, ", "))
}

// newCoder builds a fresh coder for codec. ambe is the vocoder for
// CodecAMBE.
func newCoder(codec string, ambe func() ambeCoder) coder {
	switch codec {
	case CodecAMBE:
		return ambe()
	case CodecIMBE:
		return imbeCoder{imbe.NewCodec()}
	case CodecCodec2:
		return codec2Coder{vocoder.NewCodec2()}
	case CodecGSM:
		return gsmCoder{gsm.NewEncoder(), gsm.NewDecoder()}
	case CodecULaw:
		return ulawCoder{}
	}
	return plainCoder{}
}
