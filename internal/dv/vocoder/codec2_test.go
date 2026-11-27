package vocoder_test

import (
	"math"
	"testing"

	"github.com/w1jar/nodespot/internal/dv/vocoder"
)

var _ vocoder.Codec2 = (*vocoder.C2)(nil)

func c2tone(k int) (p [vocoder.PCMSamples]int16) {
	for i := range p {
		p[i] = int16(8000 * math.Sin(2*math.Pi*500*float64(k*vocoder.PCMSamples+i)/8000))
	}
	return p
}

func TestCodec2Adapter(t *testing.T) {
	enc, dec := vocoder.NewCodec2(), vocoder.NewCodec2()
	var loud int
	for k := 0; k < 25; k++ {
		pcm := c2tone(k)
		f := enc.Encode(&pcm)
		out := dec.Decode(&f)
		var e float64
		for _, s := range out {
			e += float64(s) * float64(s)
		}
		if e/vocoder.PCMSamples > 1e5 {
			loud++
		}
	}
	if loud < 12 {
		t.Fatalf("only %d of 25 frames carry audio", loud)
	}
}

// Same input, same state: same bits (the codec is deterministic).
func TestCodec2Deterministic(t *testing.T) {
	a, b := vocoder.NewCodec2(), vocoder.NewCodec2()
	for k := 0; k < 10; k++ {
		pcm := c2tone(k)
		if fa, fb := a.Encode(&pcm), b.Encode(&pcm); fa != fb {
			t.Fatalf("frame %d differs: %x vs %x", k, fa, fb)
		}
	}
}
