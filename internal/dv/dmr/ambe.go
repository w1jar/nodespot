package dmr

// AMBE+2 (AMBE 3600x2450) FEC for the DMR air interface.
//
// A 49-bit AMBE frame u[0..48] splits into
//
//	a = u[0..11]  -> Golay(24,12) -> C0, 24 bits
//	b = u[12..23] -> Golay(23,12) -> C1, 23 bits, XORed with a PRNG seeded by a
//	c = u[24..34] -> C2, 11 bits, no FEC
//	d = u[35..48] -> C3, 14 bits, no FEC
//
// and C0..C3 (72 bits) are interleaved into the 72-bit DMR frame. The bit
// order of the 49-bit frame is that of mbelib's ambe_d[] (mbe_eccAmbe3600x2450Data),
// which is what the software AMBE vocoder and the other AMBE+2 tools exchange. The interleave
// schedule (rW/rX/rY/rZ) is the one of DSD dmr_const.h (ISC), the PRNG is
// mbelib's mbe_demodulateAmbe3600x2450Data (ISC); the result is cross-checked
// against MMDVMHost AMBEFEC.cpp (DMR_A/B/C_TABLE, PRNG_TABLE) in the tests.

// Interleave schedule: dibit i of the 72-bit frame carries C[rW[i]] bit rX[i]
// (high bit) and C[rY[i]] bit rZ[i] (low bit).
var (
	ambeRW = [36]uint8{0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 2, 0, 2, 0, 2, 0, 2, 0, 2, 0, 2, 0, 2}
	ambeRX = [36]uint8{23, 10, 22, 9, 21, 8, 20, 7, 19, 6, 18, 5, 17, 4, 16, 3, 15, 2, 14, 1, 13, 0, 12, 10, 11, 9, 10, 8, 9, 7, 8, 6, 7, 5, 6, 4}
	ambeRY = [36]uint8{0, 2, 0, 2, 0, 2, 0, 2, 0, 3, 0, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3, 1, 3}
	ambeRZ = [36]uint8{5, 3, 4, 2, 3, 1, 2, 0, 1, 13, 0, 12, 22, 11, 21, 10, 20, 9, 19, 8, 18, 7, 17, 6, 16, 5, 15, 4, 14, 3, 13, 2, 12, 1, 11, 0}
)

// ambePRNG returns the 23-bit whitening word for C1 given the 12 data bits of
// C0. Bit 22 (the MSB) is applied to C1 bit 22.
func ambePRNG(a uint32) uint32 {
	pr := uint32(16 * a)
	var w uint32
	for i := 1; i < 24; i++ {
		pr = (173*pr + 13849) & 0xFFFF
		w = w<<1 | pr>>15
	}
	return w
}

// ambeFrames holds C0..C3 as integers (bit j = C[x][j]).
type ambeFrames [4]uint32

func interleave72(c ambeFrames) (out AMBE72) {
	for i := 0; i < 36; i++ {
		setBit(out[:], 2*i, c[ambeRW[i]]&(1<<ambeRX[i]) != 0)
		setBit(out[:], 2*i+1, c[ambeRY[i]]&(1<<ambeRZ[i]) != 0)
	}
	return out
}

func deinterleave72(in AMBE72) (c ambeFrames) {
	for i := 0; i < 36; i++ {
		if getBit(in[:], 2*i) {
			c[ambeRW[i]] |= 1 << ambeRX[i]
		}
		if getBit(in[:], 2*i+1) {
			c[ambeRY[i]] |= 1 << ambeRZ[i]
		}
	}
	return c
}

// EncodeAMBE converts an AMBE 49-bit frame to the 72-bit DMR frame.
func EncodeAMBE(f AMBE) AMBE72 {
	a := getBits(f[:], 0, 12)
	b := getBits(f[:], 12, 12)
	var c ambeFrames
	c[0] = golay24Encode(a)
	c[1] = golay23Encode(b) ^ ambePRNG(a)
	c[2] = getBits(f[:], 24, 11)
	c[3] = getBits(f[:], 35, 14)
	return interleave72(c)
}

// DecodeAMBE converts a 72-bit DMR frame to the AMBE 49-bit form and returns
// the number of bit errors corrected in C0 and C1.
//
// As in MMDVMHost, a frame whose C0 is uncorrectable, or that has too many
// errors to trust (C0 >= 4, or C0+C1 >= 6 with C0 >= 2), is replaced by
// SilenceAMBE; an uncorrectable C0 counts as 10 errors.
func DecodeAMBE(d AMBE72) (f AMBE, errs int) {
	c := deinterleave72(d)
	a, ea, ok := golay24Decode(c[0])
	if !ok {
		return SilenceAMBE, 10
	}
	b, eb := golay23Decode(c[1] ^ ambePRNG(a))
	errs = ea + eb
	if ea >= 4 || (errs >= 6 && ea >= 2) {
		return SilenceAMBE, errs
	}
	putBits(f[:], 0, 12, a)
	putBits(f[:], 12, 12, b)
	putBits(f[:], 24, 11, c[2])
	putBits(f[:], 35, 14, c[3])
	return f, errs
}
