// SPDX-License-Identifier: GPL-3.0-or-later

package ysf

import "github.com/w1jar/nodespot/internal/dv/imbe"

// ---- CSD1/CSD2 (header and terminator) ----

func encodeCSD(dt *[20]byte, out []byte) {
	var b [23]byte
	for i := 0; i < 20; i++ {
		b[i] = dt[i] ^ whitening[i]
	}
	addCRC(b[:22])
	codeBlock(b[:], 180, table920, out)
}

func decodeCSD(in []byte) (dt [20]byte, ok bool) {
	out := decodeBlock(in, 180, table920, 176)
	if !checkCRC(out[:22]) {
		return dt, false
	}
	for i := 0; i < 20; i++ {
		dt[i] = out[i] ^ whitening[i]
	}
	return dt, true
}

// WriteCSD writes CSD1 (destination + source callsign) and CSD2 (downlink +
// uplink callsign) into the payload of a header or terminator frame
// (MMDVMHost CYSFPayload::writeHeader).
func WriteCSD(frame []byte, csd1, csd2 [20]byte) {
	var c1, c2 [45]byte
	encodeCSD(&csd1, c1[:])
	encodeCSD(&csd2, c2[:])
	p := frame[PayloadOff:]
	for i := 0; i < 5; i++ {
		copy(p[i*18:], c1[i*9:i*9+9])
		copy(p[i*18+9:], c2[i*9:i*9+9])
	}
}

// ReadCSD decodes CSD1 and CSD2 from a header or terminator frame
// (MMDVMHost CYSFPayload::processHeaderData).
func ReadCSD(frame []byte) (csd1 [20]byte, ok1 bool, csd2 [20]byte, ok2 bool) {
	var c1, c2 [45]byte
	p := frame[PayloadOff:]
	for i := 0; i < 5; i++ {
		copy(c1[i*9:], p[i*18:i*18+9])
		copy(c2[i*9:], p[i*18+9:i*18+18])
	}
	csd1, ok1 = decodeCSD(c1[:])
	csd2, ok2 = decodeCSD(c2[:])
	return
}

// ---- V/D mode 2 DCH ----

// WriteDCH writes the 10-byte V/D mode 2 data channel word of a
// communications frame (MMDVM_CM CYSFPayload::writeVDMode2Data). Which
// word goes in which frame depends on FN: 0 destination, 1 source,
// 2 downlink, 3 uplink, 4 Rem1+Rem2, 5 Rem3+Rem4, 6 DT1, 7 DT2.
func WriteDCH(frame []byte, dt [10]byte) {
	var b [13]byte
	for i := 0; i < 10; i++ {
		b[i] = dt[i] ^ whitening[i]
	}
	addCRC(b[:12])
	var c [25]byte
	codeBlock(b[:], 100, table520, c[:])
	p := frame[PayloadOff:]
	for i := 0; i < 5; i++ {
		copy(p[i*18:i*18+5], c[i*5:i*5+5])
	}
}

// ReadDCH decodes the V/D mode 2 data channel word (MMDVMHost
// CYSFPayload::processVDMode2Data).
func ReadDCH(frame []byte) (dt [10]byte, ok bool) {
	var c [25]byte
	p := frame[PayloadOff:]
	for i := 0; i < 5; i++ {
		copy(c[i*5:], p[i*18:i*18+5])
	}
	out := decodeBlock(c[:], 100, table520, 96)
	if !checkCRC(out[:12]) {
		return dt, false
	}
	for i := 0; i < 10; i++ {
		dt[i] = out[i] ^ whitening[i]
	}
	return dt, true
}

// ---- V/D mode 2 VCH ----

func vchOffset(j int) int { return 40 + j*144 }

// WriteVCH writes AMBE frame a (md380 layout) into VCH j (0..4) of a V/D
// mode 2 frame (MMDVM_CM ModeConv::encodeYSF packing).
func WriteVCH(frame []byte, j int, a [AMBELen]byte) {
	var buf [13]byte
	for i := 0; i < 27; i++ {
		b := getBit(a[:], i)
		setBit(buf[:], 3*i, b)
		setBit(buf[:], 3*i+1, b)
		setBit(buf[:], 3*i+2, b)
	}
	for i := 0; i < 22; i++ {
		setBit(buf[:], 81+i, getBit(a[:], 27+i))
	}
	for i := range buf {
		buf[i] ^= whitening[i]
	}
	p, off := frame[PayloadOff:], vchOffset(j)
	for i := 0; i < 104; i++ {
		setBit(p, off+table264[i], getBit(buf[:], i))
	}
}

// ReadVCH extracts the AMBE frame (md380 layout) from VCH j (0..4) of a
// V/D mode 2 frame. errs counts the corrected repetition triplets
// (MMDVMHost processVDMode2Audio, MMDVM_CM ModeConv::putYSF).
func ReadVCH(frame []byte, j int) (a [AMBELen]byte, errs int) {
	var buf [13]byte
	p, off := frame[PayloadOff:], vchOffset(j)
	for i := 0; i < 104; i++ {
		setBit(buf[:], i, getBit(p, off+table264[i]))
	}
	for i := range buf {
		buf[i] ^= whitening[i]
	}
	for i := 0; i < 27; i++ {
		v := b2u(getBit(buf[:], 3*i)) + b2u(getBit(buf[:], 3*i+1)) + b2u(getBit(buf[:], 3*i+2))
		if v == 1 || v == 2 {
			errs++
		}
		setBit(a[:], i, v >= 2)
	}
	for i := 0; i < 22; i++ {
		setBit(a[:], 27+i, getBit(buf[:], 81+i))
	}
	return a, errs
}

// ---- Voice FR mode (VW) ----

// imbeInterleave is the IMBE codeword interleave of MMDVMHost P25Audio.cpp
// / AMBEFEC.cpp (IMBE_INTERLEAVE): codeword bit i is at position
// imbeInterleave[i] of the 18-byte block.
var imbeInterleave = [144]int{
	0, 7, 12, 19, 24, 31, 36, 43, 48, 55, 60, 67, 72, 79, 84, 91, 96, 103, 108, 115, 120, 127, 132, 139,
	1, 6, 13, 18, 25, 30, 37, 42, 49, 54, 61, 66, 73, 78, 85, 90, 97, 102, 109, 114, 121, 126, 133, 138,
	2, 9, 14, 21, 26, 33, 38, 45, 50, 57, 62, 69, 74, 81, 86, 93, 98, 105, 110, 117, 122, 129, 134, 141,
	3, 8, 15, 20, 27, 32, 39, 44, 51, 56, 63, 68, 75, 80, 87, 92, 99, 104, 111, 116, 123, 128, 135, 140,
	4, 11, 16, 23, 28, 35, 40, 47, 52, 59, 64, 71, 76, 83, 88, 95, 100, 107, 112, 119, 124, 131, 136, 143,
	5, 10, 17, 22, 29, 34, 41, 46, 53, 58, 65, 70, 77, 82, 89, 94, 101, 106, 113, 118, 125, 130, 137, 142}

// VWBlocks returns the payload blocks that carry IMBE voice in a Voice FR
// communications frame: blocks 3 and 4 in the first frame after the header
// (FN=0, FT=1, MMDVMHost processVoiceFRModeAudio2), all five otherwise.
func VWBlocks(f FICH) []int {
	if f.FN == 0 && f.FT == 1 {
		return []int{3, 4}
	}
	return []int{0, 1, 2, 3, 4}
}

// ReadVW decodes the IMBE codeword in payload block j of a Voice FR frame
// into the 11-byte frame of package imbe (u0..u7, MSB first). errs is the
// number of bit errors corrected by the Golay/Hamming FEC.
func ReadVW(frame []byte, j int) (out [11]byte, errs int) {
	blk := frame[PayloadOff+j*VWBlockLen : PayloadOff+(j+1)*VWBlockLen]
	var cw [18]byte
	for i := 0; i < 144; i++ {
		setBit(cw[:], i, getBit(blk, imbeInterleave[i]))
	}
	v, _, et := imbe.DecodeFEC(&cw)
	return imbe.PackVectors(v), et
}

// WriteVW writes the 11-byte IMBE frame f into payload block j of a Voice
// FR frame (FEC and interleave; the inverse of ReadVW).
func WriteVW(frame []byte, j int, f [11]byte) {
	cw := imbe.EncodeFEC(imbe.UnpackVectors(&f))
	blk := frame[PayloadOff+j*VWBlockLen : PayloadOff+(j+1)*VWBlockLen]
	for i := 0; i < 144; i++ {
		setBit(blk, imbeInterleave[i], getBit(cw[:], i))
	}
}
