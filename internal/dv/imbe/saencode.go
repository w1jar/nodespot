// SPDX-License-Identifier: GPL-3.0-or-later
//
// Ported to Go from imbe_vocoder by Pavel Yazev for nodespot.
// Original notice:
//
//   Project 25 IMBE Encoder/Decoder Fixed-Point implementation
//   Developed by Pavel Yazev E-mail: pyazev@gmail.com
//   Version 1.0 (c) Copyright 2009
//
//   This is free software; you can redistribute it and/or modify it
//   under the terms of the GNU General Public License as published by
//   the Free Software Foundation; either version 3, or (at your option)
//   any later version.
//
//   The software is distributed in the hope that it will be useful, but
//   WITHOUT ANY WARRANTY; without even the implied warranty of
//   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.
//   See the GNU General Public License for more details.
//
//   You should have received a copy of the GNU General Public License
//   along with this; see the file COPYING.  If not, write to the Free
//   Software Foundation, Inc., 51 Franklin Street, Boston, MA
//   02110-1301, USA.

package imbe

// Spectral amplitude quantization and frame vector packing on the encoder
// side, ported from sa_encode.cc and ch_encode.cc.

// stepSizeHiOrd computes
// extract_h(((Word32)hi_ord_std_tbl[j-1] * hi_ord_step_size_tbl[nb-1]) << 1)
// with C's (wrapping) arithmetic.
func stepSizeHiOrd(j, numBits int16) uint16 {
	v := uint32(hiOrdStdTbl[j-1]) * uint32(hiOrdStepSizeTbl[numBits-1]) << 1
	return uint16(v >> 16)
}

// kCoefFor returns num_harms_prev/num_harms in unsigned Q8.24, as computed
// at the top of sa_encode and sa_decode.
func kCoefFor(numHarms, numHarmsPrev int16) uint32 {
	switch {
	case numHarms == numHarmsPrev:
		return cnstOneQ8_24
	case numHarms > numHarmsPrev:
		return uint32(int32(divS(numHarmsPrev<<9, numHarms<<9)) << 9)
	default:
		var k uint32
		tmp := numHarmsPrev
		for tmp > numHarms {
			tmp -= numHarms
			k += cnstOneQ8_24
		}
		return k + uint32(int32(divS(tmp<<9, numHarms<<9))<<9)
	}
}

// roCoefFor returns the prediction coefficient for numHarms.
func roCoefFor(numHarms int16) int16 {
	switch {
	case numHarms <= 15:
		return cnst0_4Q1_15
	case numHarms <= 24:
		return int16(int32(numHarms)*cnst0_03Q1_15 - cnst0_05Q1_15)
	default:
		return cnst0_7Q1_15
	}
}

// saEncodeInit is imbe_vocoder::sa_encode_init.
func (e *Encoder) saEncodeInit() {
	e.numHarmsPrev2 = 30
	for i := range e.saPrev2 {
		e.saPrev2[i] = 0
	}
}

// saEncode is imbe_vocoder::sa_encode.
func (e *Encoder) saEncode(p *imbeParam) {
	var gainVec, gainR [6]int16
	var tVec [numHarmsMax]int16
	var cVec [maxBlockLen]int16
	var vec32Tmp [numHarmsMax]int32

	numHarms := p.numHarms
	kCoef := kCoefFor(numHarms, e.numHarmsPrev2)
	roCoef := roCoefFor(numHarms)

	for i := int(e.numHarmsPrev2) + 1; i < numHarmsMax+2; i++ {
		e.saPrev2[i] = e.saPrev2[e.numHarmsPrev2]
	}

	kAcc := kCoef
	var sum int32
	for i := int16(0); i < numHarms; i++ {
		index := uint16(kAcc >> 24)                      // Get integer part
		siCoef := int16((kAcc - uint32(index)<<24) >> 9) // Get fractional part
		if siCoef == 0 {
			t := lMpyLs(e.saPrev2[index], roCoef) // sa_prev2 here is in Q10.22 format
			vec32Tmp[i] = lSub(log2(p.sa[i]), t)
			sum = lAdd(sum, e.saPrev2[index]) // sum in Q10.22 format
		} else {
			t := lMpyLs(e.saPrev2[index], sub(0x7FFF, siCoef))
			sum = lAdd(sum, t)
			vec32Tmp[i] = lSub(log2(p.sa[i]), lMpyLs(t, roCoef))
			t = lMpyLs(e.saPrev2[index+1], siCoef)
			sum = lAdd(sum, t)
			vec32Tmp[i] = lSub(vec32Tmp[i], lMpyLs(t, roCoef))
		}
		// d_gain_adjust is always 0 here: that branch is not ported.
		kAcc += kCoef
	}

	tmp := normS(numHarms)
	p.divOneByNumHarmSh = tmp
	tmp1 := divS(0x4000, numHarms<<uint(tmp)) // 1/num_harms with scaling
	p.divOneByNumHarm = tmp1

	sum = lShr(lMpyLs(lMpyLs(sum, roCoef), tmp1), 14-tmp)

	for i := int16(0); i < numHarms; i++ {
		tVec[i] = extractH(lShl(lAdd(vec32Tmp[i], sum), 5)) // Q5.11
	}

	// Encode T vector
	index := numHarms - numHarmsMin

	// Unpack bit allocation table's item
	getBitAllocation(numHarms, p.bitAlloc[:])

	lmprblItem := lmprblTbl[index]

	// Encoding the Higher Order DCT Coefficients
	tv := 0
	bv := 8
	ba := 5
	for i := 0; i < numPredResBlks; i++ {
		blLen := int16((lmprblItem >> 28) & 0xF)
		lmprblItem <<= 4
		dct(tVec[tv:], blLen, blLen, cVec[:])
		gainVec[i] = cVec[0]
		for j := int16(1); j < blLen; j++ {
			numBits := p.bitAlloc[ba]
			ba++
			if numBits != 0 {
				p.bVec[bv] = qntByStep(cVec[j], stepSizeHiOrd(j, numBits), numBits)
			} else {
				p.bVec[bv] = 0
			}
			bv++
		}
		tv += int(blLen)
	}

	// Encoding the Gain Vector
	dct(gainVec[:], numPredResBlks, numPredResBlks, gainR[:])
	gss := int(index) * 5
	p.bVec[2] = tblQuant(gainR[0], gainQntTbl[:gainQntTblSize])
	for j := 1; j < 6; j++ {
		p.bVec[2+j] = qntByStep(gainR[j], gainStepSizeTbl[gss+j-1], p.bitAlloc[j-1])
	}

	// Decode T vector
	ba = 0
	bv = 2

	// Decoding the Gain Vector. gain_vec has signed Q5.11 format
	gainVec[0] = gainQntTbl[p.bVec[bv]]
	bv++
	for i := 1; i < 6; i++ {
		gainVec[i] = extractL(lShr(deqntByStep(p.bVec[bv], gainStepSizeTbl[gss+i-1], p.bitAlloc[ba]), 5))
		bv++
		ba++
	}

	idct(gainVec[:], numPredResBlks, numPredResBlks, gainR[:])

	vZap(tVec[:])

	lmprblItem = lmprblTbl[index]

	// Decoding the Higher Order DCT Coefficients
	tv = 0
	for i := 0; i < numPredResBlks; i++ {
		blLen := int16((lmprblItem >> 28) & 0xF)
		lmprblItem <<= 4
		vZap(cVec[:])
		cVec[0] = gainR[i]
		for j := int16(1); j < blLen; j++ {
			numBits := p.bitAlloc[ba]
			ba++
			if numBits != 0 {
				cVec[j] = extractL(lShr(deqntByStep(p.bVec[bv], stepSizeHiOrd(j, numBits), numBits), 5))
			} else {
				cVec[j] = 0
			}
			bv++
		}
		idct(cVec[:], blLen, blLen, tVec[tv:])
		tv += int(blLen)
	}

	// Reconstruct Spectral Amplitudes
	kAcc = kCoef
	for i := int(e.numHarmsPrev2) + 1; i < numHarmsMax+2; i++ {
		e.saPrev2[i] = e.saPrev2[e.numHarmsPrev2]
	}

	for i := int16(0); i < numHarms; i++ {
		index := uint16(kAcc >> 24)
		siCoef := int16((kAcc - uint32(index)<<24) >> 9)
		if siCoef == 0 {
			t := lMpyLs(e.saPrev2[index], roCoef)
			vec32Tmp[i] = lAdd(lShr(lDepositH(tVec[i]), 5), t) // Convert t_vec to Q10.22 and add ...
		} else {
			t := lMpyLs(e.saPrev2[index], sub(0x7FFF, siCoef))
			vec32Tmp[i] = lAdd(lShr(lDepositH(tVec[i]), 5), lMpyLs(t, roCoef))
			t = lMpyLs(e.saPrev2[index+1], siCoef)
			vec32Tmp[i] = lAdd(vec32Tmp[i], lMpyLs(t, roCoef))
		}
		kAcc += kCoef
	}

	for i := int16(1); i <= numHarms; i++ {
		e.saPrev2[i] = lSub(vec32Tmp[i-1], sum)
	}

	e.numHarmsPrev2 = numHarms
}

// encodeFrameVector packs the quantized parameters into the u0..u7 frame
// vector (encode_frame_vector).
func encodeFrameVector(p *imbeParam, fv *[8]int16) {
	var bitStream [enBitStreamLen]int16

	numHarms := p.numHarms
	numBands := p.numBands

	*fv = [8]int16{}

	// Unpack bit allocation table's item
	getBitAllocation(numHarms, p.bitAlloc[:])

	// Priority Scanning
	index0 := 0
	ba := p.bitAlloc[:]
	var bitThr int16
	if numHarms == 0xb {
		bitThr = 9
	} else {
		bitThr = ba[0]
	}
	bitMask := shl(1, bitThr-1)

	for index0 < enBitStreamLen-int(numBands-3) {
		b := p.bVec[3:]
		for i := int16(0); i < numHarms-1; i++ {
			if bitThr != 0 && bitThr <= ba[i] {
				if b[i]&bitMask != 0 {
					bitStream[index0] = 1
				} else {
					bitStream[index0] = 0
				}
				index0++
			}
		}
		bitThr--
		bitMask = shr(bitMask, 1)
	}

	b2i := func(c bool, v int16) int16 {
		if c {
			return v
		}
		return 0
	}

	fv[0] = shl(p.bVec[0]&0xFC, 4) | (p.bVec[2] & 0x38)
	index0 = 0
	fv[0] |= b2i(bitStream[0] != 0, 4)
	fv[0] |= b2i(bitStream[1] != 0, 2)
	fv[0] |= b2i(bitStream[2] != 0, 1)
	index0 = 3

	for vecNum := 1; vecNum <= 3; vecNum++ {
		var tmp int16
		for i := 0; i < 12; i++ {
			tmp <<= 1
			tmp |= bitStream[index0]
			index0++
		}
		fv[vecNum] = tmp
	}

	index0 -= int(numBands) + 2
	bitMask = shl(1, numBands-1)
	for i := int16(0); i < numBands; i++ {
		bitStream[index0] = b2i(p.bVec[1]&bitMask != 0, 1)
		index0++
		bitMask >>= 1
	}
	bitStream[index0] = b2i(p.bVec[2]&0x04 != 0, 1)
	index0++
	bitStream[index0] = b2i(p.bVec[2]&0x02 != 0, 1)
	index0++

	index0 -= int(numBands) + 2
	for vecNum := 4; vecNum <= 6; vecNum++ {
		var tmp int16
		for i := 0; i < 11; i++ {
			tmp <<= 1
			tmp |= bitStream[index0]
			index0++
		}
		fv[vecNum] = tmp
	}

	fv[7] = shl(p.bVec[0]&0x03, 1) | shl(p.bVec[2]&0x01, 3)
	fv[7] |= b2i(bitStream[index0] != 0, 0x40)
	fv[7] |= b2i(bitStream[index0+1] != 0, 0x20)
	fv[7] |= b2i(bitStream[index0+2] != 0, 0x10)
	// Sync bit: the reference reads b_vec[num_harms + 2], which the encoder
	// never writes for the current frame (it holds whatever an earlier,
	// longer frame left there). Ported as is.
	fv[7] |= b2i(p.bVec[numHarms+2] != 0, 0x01)
}
