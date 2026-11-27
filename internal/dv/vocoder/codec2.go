package vocoder

import "github.com/w1jar/nodespot/internal/dv/codec2"

// C2 is the native Codec2 3200 vocoder: an encoder and a decoder of
// internal/dv/codec2 behind the Codec2 interface. Stateful and not safe for
// concurrent use. The codec works on 8 kHz, 160-sample frames, the same as
// the bridge and USRP, so there is no resampling.
type C2 struct {
	enc *codec2.Encoder
	dec *codec2.Decoder
}

// NewCodec2 returns a Codec2 3200 codec in its initial state.
func NewCodec2() *C2 { return &C2{enc: codec2.NewEncoder(), dec: codec2.NewDecoder()} }

// Encode encodes 20 ms of PCM into one 8-byte frame.
func (c *C2) Encode(pcm *[PCMSamples]int16) (out [C2Bytes]byte) {
	c.enc.Encode(&out, pcm)
	return out
}

// Decode decodes one 8-byte frame into 20 ms of PCM.
func (c *C2) Decode(f *[C2Bytes]byte) (pcm [PCMSamples]int16) {
	c.dec.Decode(&pcm, f)
	return pcm
}
