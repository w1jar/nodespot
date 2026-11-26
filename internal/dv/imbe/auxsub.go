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

// Shared constants and the parameter block, from imbe.h, globals.h,
// encode.h, dsp_sub.h, ch_decode.h and ch_encode.h; helper routines from
// aux_sub.cc and qnt_sub.cc.

const (
	frameLen        = 160 // FRAME: number of samples in frame
	numHarmsMax     = 56  // NUM_HARMS_MAX
	numHarmsMin     = 9   // NUM_HARMS_MIN
	numBandsMax     = 12  // NUM_BANDS_MAX
	maxBlockLen     = 10  // MAX_BLOCK_LEN
	numPredResBlks  = 6   // NUM_PRED_RES_BLKS
	peLpfOrd        = 21  // PE_LPF_ORD
	pitchEstFrame   = 301 // PITCH_EST_FRAME
	bNum            = numHarmsMax - 1
	pitchEstBufSize = 621 // PITCH_EST_BUF_SIZE
	fftLength       = 256 // FFTLENGTH

	bitStreamLen   = 3 + 3*12 + 3*11 + 3     // BIT_STREAM_LEN
	enBitStreamLen = 3 + 3*12 + 6 + 2*11 + 3 // EN_BIT_STREAM_LEN

	gainQntTblSize = 64 // GAIN_QNT_TBL_SIZE

	// globals.h
	cnst0_9254Q0_16 = 60647  // 0.9254 in unsigned Q0.16 format
	cnst0_33Q0_16   = 0x5556 // 1/3 in unsigned Q0.16 format
	cnstOneQ8_24    = 0x01000000
	cnst0_7Q1_15    = 0x599A
	cnst0_4Q1_15    = 0x3333
	cnst0_03Q1_15   = 0x03D7
	cnst0_05Q1_15   = 0x0666
	cnst1_125Q8_8   = 0x0120
	cnst0_5Q8_8     = 0x0080
	cnst0_125Q8_8   = 0x0020
	cnst0_25Q8_8    = 0x0040
	cnst0_8717Q1_15 = 0x6F94
	cnst0_0031Q1_15 = 0x0064
	cnst0_48Q4_12   = 0x07AE
	cnst1_00Q4_12   = 0x1000
	cnst0_85Q4_12   = 0x0D9B
	cnst0_4Q4_12    = 0x0666
	cnst0_05Q4_12   = 0x00CD
	cnst0_5882Q1_15 = 0x4B4B
	cnst0_2857Q1_15 = 0x2492

	// dsp_sub.h
	cnst0_5Q1_15 = 0x4000
	cnst0_5Q5_11 = 0x0400
	cnst1_0Q1_15 = 0x7FFF

	// qnt_sub.h
	cnst0_5Q0_16 = 0x8000
)

// imbeParam mirrors IMBE_PARAM.
type imbeParam struct {
	eP                int16
	pitch             int16 // Q14.2
	refPitch          int16 // Q8.8
	fundFreq          int32
	numHarms          int16
	numBands          int16
	vUvDsn            [numHarmsMax]int16
	bVec              [numHarmsMax + 3]int16
	bitAlloc          [bNum + 4]int16
	sa                [numHarmsMax]int16
	lUv               int16
	divOneByNumHarm   int16
	divOneByNumHarmSh int16
}

// getBitAllocationArr returns the offset into bitAllocationTbl for
// numHarms (get_bit_allocation_arr).
func getBitAllocationArr(numHarms int16) int {
	if numHarms == numHarmsMin {
		return 0
	}
	index := numHarms - numHarmsMin - 1
	return int(bitAllocationOffsetTbl[index>>2] + ((3 + (index >> 2)) * (index & 0x3)))
}

// getBitAllocation unpacks a bit allocation table item (get_bit_allocation).
func getBitAllocation(numHarms int16, ptr []int16) {
	bat := getBitAllocationArr(numHarms)
	p := 0
	for i := int16(0); i < numHarms-1; i += 4 {
		tmp := int16(bitAllocationTbl[bat])
		bat++
		ptr[p+3] = tmp & 0xF
		tmp >>= 4
		ptr[p+2] = tmp & 0xF
		tmp >>= 4
		ptr[p+1] = tmp & 0xF
		tmp >>= 4
		ptr[p+0] = tmp & 0xF
		p += 4
	}
}

// vZap zeroes vec (v_zap).
func vZap(vec []int16) {
	for i := range vec {
		vec[i] = 0
	}
}

// lVMagsq is the saturating sum of squares of vec (L_v_magsq).
func lVMagsq(vec []int16) int32 {
	var s int32
	for _, v := range vec {
		s = lMac(s, v, v)
	}
	return s
}

// vEquShr copies src to dst shifted right by scale (v_equ_shr).
func vEquShr(dst, src []int16, scale int16) {
	for i := range dst {
		dst[i] = shr(src[i], scale)
	}
}

// deqntByStep dequantizes qval by step size (Q0.16); result in signed
// (bitNum).16 format (deqnt_by_step).
func deqntByStep(qval int16, stepSize uint16, bitNum int16) int32 {
	if bitNum == 0 {
		return 0
	}
	res := int32(stepSize) * (int32(qval) - (int32(1) << uint(bitNum-1)))
	res = lAdd(res, (int32(stepSize)*cnst0_5Q0_16)>>16)
	return res
}

// qntByStep quantizes val (Q5.10) by step size (Q0.16) (qnt_by_step).
func qntByStep(val int16, stepSize uint16, bitNum int16) int16 {
	shift := normS(int16(stepSize))

	tmp := divS(0x4000, shl(int16(stepSize), shift)) // To get result in Qxx.16 format it is necessary left shift tmp by (shift + 3)
	qIndex := shrR(mult(val, tmp), sub(9, shift))    // q_index here is rounded to the nearest integer

	maxVal := int16(1) << uint(bitNum-1)
	minVal := negate(maxVal)

	switch {
	case qIndex < minVal:
		return 0
	case qIndex >= maxVal:
		return (int16(1) << uint(bitNum)) - 1
	default:
		return maxVal + qIndex
	}
}

// tblQuant quantizes val by table lookup (tbl_quant).
func tblQuant(val int16, qTbl []int16) int16 {
	minIndex := int16(0)
	maxIndex := int16(len(qTbl) - 1)

	if val >= qTbl[maxIndex] {
		return maxIndex
	}
	if val <= qTbl[minIndex] {
		return minIndex
	}

	for maxIndex-minIndex != 1 {
		index := minIndex + ((maxIndex - minIndex) >> 1)
		if qTbl[index] > val {
			maxIndex = index
		} else {
			minIndex = index
		}
	}

	if int32(qTbl[maxIndex])-int32(val) <= int32(val)-int32(qTbl[minIndex]) {
		return maxIndex
	}
	return minIndex
}
