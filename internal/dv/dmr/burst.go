package dmr

// Burst layout (ETSI TS 102 361-1 4.2.1, figures 4.2/4.3), bit 0 = MSB of byte 0:
//
//	voice: AMBE 0..107 | SYNC or EMB(8) + embedded(32) + EMB(8) 108..155 | AMBE 156..263
//	data:  BPTC 0..97 | slot type 98..107 | SYNC 108..155 | slot type 156..165 | BPTC 166..263
//
// The three 72-bit AMBE frames are bits 0..71, 72..107+156..191 and 192..263.

const (
	bitCentre   = 108
	bitCentre2  = 156
	bitEmbedded = 116
)

// voicePos maps bit j of the 216 voice payload bits to its burst position.
func voicePos(j int) int {
	if j < 108 {
		return j
	}
	return j + 48
}

// PutVoice72 places three 72-bit AMBE frames in the payload of b.
func (b *Burst) PutVoice72(f [3]AMBE72) {
	for k := 0; k < 3; k++ {
		for i := 0; i < 72; i++ {
			setBit(b[:], voicePos(72*k+i), getBit(f[k][:], i))
		}
	}
}

// Voice72 returns the three 72-bit AMBE frames of a voice burst.
func (b *Burst) Voice72() (f [3]AMBE72) {
	for k := 0; k < 3; k++ {
		for i := 0; i < 72; i++ {
			setBit(f[k][:], i, getBit(b[:], voicePos(72*k+i)))
		}
	}
	return f
}

// PutVoice FEC-encodes three AMBE frames into the payload of b.
func (b *Burst) PutVoice(f [3]AMBE) {
	b.PutVoice72([3]AMBE72{EncodeAMBE(f[0]), EncodeAMBE(f[1]), EncodeAMBE(f[2])})
}

// Voice decodes the three AMBE frames of a voice burst; errs is the sum of
// the corrected bit errors (see DecodeAMBE).
func (b *Burst) Voice() (f [3]AMBE, errs int) {
	v := b.Voice72()
	for k := range v {
		var e int
		f[k], e = DecodeAMBE(v[k])
		errs += e
	}
	return f, errs
}

// PutSync writes a 48-bit SYNC pattern in the centre of b.
func (b *Burst) PutSync(s Sync) {
	for i := 0; i < 48; i++ {
		setBit(b[:], bitCentre+i, s&(1<<(47-i)) != 0)
	}
}

// Sync returns the 48-bit centre field of b.
func (b *Burst) Sync() Sync {
	var s Sync
	for i := 0; i < 48; i++ {
		s <<= 1
		if getBit(b[:], bitCentre+i) {
			s |= 1
		}
	}
	return s
}

var allSyncs = []Sync{SyncBSVoice, SyncBSData, SyncMSVoice, SyncMSData, SyncRC,
	SyncTDMA1Voice, SyncTDMA1Data, SyncTDMA2Voice, SyncTDMA2Data}

// MatchSync returns the SYNC pattern within maxErr bits of the centre field of
// b, and the bit distance, or SyncNone.
func MatchSync(b *Burst, maxErr int) (Sync, int) {
	c := b.Sync()
	for _, s := range allSyncs {
		if n := popcount(uint32((c^s)>>24)) + popcount(uint32((c^s)&0xFFFFFF)); n <= maxErr {
			return s, n
		}
	}
	return SyncNone, 0
}

// IsVoice reports whether s is a voice SYNC pattern.
func (s Sync) IsVoice() bool {
	return s == SyncBSVoice || s == SyncMSVoice || s == SyncTDMA1Voice || s == SyncTDMA2Voice
}

// IsData reports whether s is a data SYNC pattern.
func (s Sync) IsData() bool {
	return s == SyncBSData || s == SyncMSData || s == SyncTDMA1Data || s == SyncTDMA2Data
}

// PutEMB writes the QR-encoded EMB around the embedded signalling field.
func (b *Burst) PutEMB(e EMB) {
	v := uint32(e.Encode())
	putBits(b[:], bitCentre, 8, v>>8)
	putBits(b[:], bitCentre+40, 8, v&0xFF)
}

// EMB decodes the EMB of a voice burst B-F (up to 2 bit errors corrected).
func (b *Burst) EMB() (EMB, bool) {
	v := getBits(b[:], bitCentre, 8)<<8 | getBits(b[:], bitCentre+40, 8)
	return DecodeEMB(uint16(v))
}

// PutEmbedded writes 32 bits of embedded signalling (a fragment from
// EmbeddedLC, or zeros for a null fragment).
func (b *Burst) PutEmbedded(frag [4]byte) {
	for i := 0; i < 32; i++ {
		setBit(b[:], bitEmbedded+i, getBit(frag[:], i))
	}
}

// Embedded returns the 32 bits of embedded signalling of a voice burst.
func (b *Burst) Embedded() (frag [4]byte) {
	for i := 0; i < 32; i++ {
		setBit(frag[:], i, getBit(b[:], bitEmbedded+i))
	}
	return frag
}

// PutSlotType writes the Golay(20,8)-encoded slot type of a data burst.
func (b *Burst) PutSlotType(st SlotType) {
	c := golay20Encode(st.CC<<4 | uint8(st.DataType)&0xF)
	putBits(b[:], 98, 10, c>>10)
	putBits(b[:], bitCentre2, 10, c&0x3FF)
}

// SlotType decodes the slot type of a data burst (up to 3 bit errors corrected).
func (b *Burst) SlotType() (SlotType, bool) {
	c := getBits(b[:], 98, 10)<<10 | getBits(b[:], bitCentre2, 10)
	d, _, ok := golay20Decode(c)
	return SlotType{CC: d >> 4, DataType: DataType(d & 0xF)}, ok
}

// Encode returns the 16-bit QR(16,7,6) codeword of the EMB: CC(4) PI(1) LCSS(2).
func (e EMB) Encode() uint16 {
	d := (e.CC&0xF)<<3 | uint8(e.LCSS&3)
	if e.PI {
		d |= 4
	}
	return qrEncode(d)
}

// DecodeEMB decodes a QR(16,7,6) EMB codeword, correcting up to 2 bit errors.
func DecodeEMB(v uint16) (EMB, bool) {
	d, _, ok := qrDecode(v)
	return EMB{CC: d >> 3, PI: d&4 != 0, LCSS: LCSS(d & 3)}, ok
}
