// Package vocoder defines the voice codecs the engine uses and the software AMBE vocoder
// client that provides AMBE+2.
package vocoder

import "context"

const (
	PCMSamples = 160 // 20 ms at 8 kHz
	AMBEBytes  = 7   // AMBE+2 49 bits
	IMBEBytes  = 11  // IMBE 88 bits
	C2Bytes    = 8   // Codec2 3200, 64 bits
)

// AMBE is the AMBE+2 vocoder (DMR, YSF, NXDN).
type AMBE interface {
	Encode(ctx context.Context, pcm *[PCMSamples]int16) ([AMBEBytes]byte, error)
	Decode(ctx context.Context, f [AMBEBytes]byte) ([PCMSamples]int16, error)
}

// IMBE is implemented by internal/dv/imbe (pure Go, stateful, not safe for
// concurrent use: one instance per direction and call).
// Bit packing: u0..u7 vectors (12,12,12,12,11,11,11,7 bits) MSB-first.
type IMBE interface {
	Encode(pcm *[PCMSamples]int16) [IMBEBytes]byte
	Decode(f *[IMBEBytes]byte) [PCMSamples]int16
}

// Codec2 is a Codec2 3200 coder (M17): 8 kHz, 160 samples in, 8 bytes out,
// the same frame size the bridge uses, so no resampling. Stateful and not
// safe for concurrent use: one instance per direction and call.
type Codec2 interface {
	Encode(pcm *[PCMSamples]int16) [C2Bytes]byte
	Decode(f *[C2Bytes]byte) [PCMSamples]int16
}

// SilenceAMBE is the AMBE+2 frame that decodes to silence; it replaces a
// frame whose encode timed out.
var SilenceAMBE = [AMBEBytes]byte{0xf8, 0x01, 0xa9, 0x9f, 0x8c, 0xe0, 0x80}

// Stats are the vocoder client counters, summed over both directions.
type Stats struct {
	Frames   uint64 // requests answered by the software AMBE vocoder
	Timeouts uint64 // requests that timed out or failed (replaced by silence)
	Down     bool   // too many consecutive timeouts
}
