// SPDX-License-Identifier: GPL-3.0-or-later

// Package ysf is the System Fusion (C4FM) air-interface framing that a YSF
// network frame carries: the 120-byte YSF frame (5-byte sync, 25-byte FICH
// region, 90-byte payload), the FICH channel coding, the V/D mode 2 ("DN")
// voice channel (VCH) and data channel (DCH), the header/terminator callsign
// data (CSD1/CSD2) and access to the Voice FR ("VW") IMBE codewords.
//
// The algorithms follow g4klx MMDVMHost (YSFFICH.cpp, YSFConvolution.cpp,
// YSFPayload.cpp, Golay24128.cpp, CRC.cpp; GPL-2.0-or-later) and the V/D
// mode 2 AMBE bit packing of MMDVM_CM USRP2YSF/ModeConv.cpp (encodeYSF and
// putYSF, GPL-2.0-or-later). The code here is written from those sources
// and checked byte-for-byte against vectors produced by them (see
// testdata/README.md).
//
// # Frame layout
//
//	bytes  0..4    sync D4 71 C9 63 4D
//	bytes  5..29   FICH: 4 bytes + CRC-CCITT, 4x Golay(24,12), rate 1/2
//	               K=5 convolutional code (100 dibits), dibit interleave
//	bytes 30..119  payload, five 18-byte blocks
//
// In V/D mode 2 each 18-byte block is a 5-byte DCH slice followed by a
// 13-byte VCH; the five DCH slices form one 10-byte DCH word (whitened,
// CRC, convolutional coding, 5x20 interleave) and each VCH carries one
// 49-bit AMBE+2 frame (bits 0..26 repeated three times, bits 27..48 once,
// one pad bit, whitened, 26x4 bit interleave). In header and terminator
// frames each block is 9 bytes of CSD1 followed by 9 bytes of CSD2 (20
// bytes each, whitened, CRC, convolutional coding, 9x20 interleave). In
// Voice FR mode each block is one interleaved 144-bit IMBE codeword, except
// in the first communications frame (FN=0, FT=1) where blocks 0..2 carry
// CSD3 and only blocks 3 and 4 carry voice.
//
// The 49-bit AMBE frame is passed as 7 bytes in the vocoder layout: bits 0..47
// MSB first in bytes 0..5, bit 48 in the MSB of byte 6.
package ysf

// Sizes and offsets.
const (
	FrameLen    = 120
	SyncLen     = 5
	FICHLen     = 25
	PayloadOff  = SyncLen + FICHLen // 30
	CallsignLen = 10
	AMBELen     = 7
	VWBlockLen  = 18
)

// Sync is the YSF frame sync.
var Sync = [SyncLen]byte{0xD4, 0x71, 0xC9, 0x63, 0x4D}

// FICH frame information (FI).
const (
	FIHeader         = 0
	FICommunications = 1
	FITerminator     = 2
	FITest           = 3
)

// FICH data types (DT).
const (
	DTVDMode1     = 0
	DTDataFRMode  = 1
	DTVDMode2     = 2 // "DN"
	DTVoiceFRMode = 3 // "VW"
)

// AMBESilence is a 49-bit AMBE+2 silence frame in vocoder layout (the
// AMBE_SILENCE of MMDVM_CM YSF2NXDN; its VCH encoding is MMDVM_CM's
// YSF_SILENCE).
var AMBESilence = [AMBELen]byte{0xF8, 0x01, 0xA9, 0x9F, 0x8C, 0xE0, 0x80}

// whitening is MMDVMHost's WHITENING_DATA.
var whitening = [20]byte{0x93, 0xD7, 0x51, 0x21, 0x9C, 0x2F, 0x6C, 0xD0, 0xEF, 0x0F,
	0xF8, 0x3D, 0xF1, 0x73, 0x20, 0x94, 0xED, 0x1E, 0x7C, 0xD8}

func getBit(p []byte, i int) bool { return p[i>>3]&(0x80>>uint(i&7)) != 0 }

func setBit(p []byte, i int, b bool) {
	if b {
		p[i>>3] |= 0x80 >> uint(i&7)
	} else {
		p[i>>3] &^= 0x80 >> uint(i&7)
	}
}

func b2u(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// dibitTable builds MMDVMHost's INTERLEAVE_TABLE_<cols>_20: entry r*cols+c
// is r*2 + c*40.
func dibitTable(cols int) []int {
	t := make([]int, cols*20)
	for r := 0; r < 20; r++ {
		for c := 0; c < cols; c++ {
			t[r*cols+c] = r*2 + c*40
		}
	}
	return t
}

var (
	table520 = dibitTable(5) // FICH and V/D mode 2 DCH
	table920 = dibitTable(9) // CSD
	table264 [104]int        // VCH bit interleave: entry r*26+c is c*4+r
)

func init() {
	for r := 0; r < 4; r++ {
		for c := 0; c < 26; c++ {
			table264[r*26+c] = c*4 + r
		}
	}
}

// codeBlock convolutionally encodes the first nBits of in and dibit
// interleaves the result into out with table.
func codeBlock(in []byte, nBits int, table []int, out []byte) {
	conv := make([]byte, (2*nBits+7)/8)
	convEncode(in, conv, nBits)
	for i := 0; i < nBits; i++ {
		n := table[i]
		setBit(out, n, getBit(conv, 2*i))
		setBit(out, n+1, getBit(conv, 2*i+1))
	}
}

// decodeBlock de-interleaves steps dibits from in with table and Viterbi
// decodes nOut bits.
func decodeBlock(in []byte, steps int, table []int, nOut int) []byte {
	var v viterbi
	v.start(steps)
	for i := 0; i < steps; i++ {
		n := table[i]
		v.decode(b2u(getBit(in, n)), b2u(getBit(in, n+1)))
	}
	out := make([]byte, (nOut+7)/8+1)
	v.chainback(out, nOut)
	return out
}
