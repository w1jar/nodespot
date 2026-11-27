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

// Decoder side, ported from ch_decode.cc, sa_decode.cc, sa_enh.cc,
// v_synt.cc, uv_synt.cc and rand_gen.cc.

const (
	cnst0_9898Q1_15 = 0x7EB3
	cnst0_5Q2_14    = 0x2000
	cnst1_2Q2_14    = 0x4CCC
	cnst0_1Q1_15    = 0x0CCD
)

// randGen is rand_gen(), a Park-Miller style generator. The reference keeps
// its seed in a file-static variable shared by every imbe_vocoder instance
// in the process; here each Decoder has its own.
func (d *Decoder) randGen() int16 {
	lo := 16807 * (d.seed & 0xFFFF)
	hi := 16807 * (d.seed >> 16)
	lo += (hi & 0x7FFF) << 16
	lo += hi >> 15
	if lo > 0x7FFFFFFF {
		lo -= 0x7FFFFFFF
	}
	d.seed = lo
	return int16(lo)
}

// decodeFrameVector unpacks u0..u7 into the parameter block
// (decode_frame_vector). An invalid pitch index leaves the previous
// frame's parameters in place (frame repeat).
func decodeFrameVector(p *imbeParam, fv *[8]int16) {
	var bitStream [bitStreamLen]int16

	p.bVec[0] = (shr(fv[0], 4) & 0xFC) | (shr(fv[7], 1) & 0x3)

	if p.bVec[0] < 0 || p.bVec[0] > 207 {
		return // frame repeating
	}

	tmp := ((p.bVec[0] & 0xFF) << 1) + 0x4F // unsigned Q15.1, add 39.5

	// Calculate fundamental frequency with higher precision
	shift := normS(tmp)
	tmp1 := tmp << uint(shift)
	tmp2 := divS(0x4000, tmp1)
	p.fundFreq = lShr(lDepositH(tmp2), 11-shift)
	lTmp := lSub(0x40000000, lMult(tmp1, tmp2))
	tmp2 = divS(extractL(lShr(lTmp, 2)), tmp1)
	lTmp = lShr(lDepositL(tmp2), 11-shift-2)
	p.fundFreq = lAdd(p.fundFreq, lTmp)

	tmp = (tmp + 0x2) >> 3 // (b0 + 39.5 + 1)/4
	p.numHarms = int16((uint32(cnst0_9254Q0_16) * uint32(int32(tmp))) >> 16)

	if p.numHarms <= 36 {
		p.numBands = extractH(int32(uint32(int32(p.numHarms)+2) * cnst0_33Q0_16)) // fix((L+2)/3)
	} else {
		p.numBands = numBandsMax
	}

	bit := func(c bool) int16 {
		if c {
			return 1
		}
		return 0
	}

	// Convert input vector (from b_3 to b_L+1) to bit stream
	bitStream[0] = bit(fv[0]&0x4 != 0)
	bitStream[1] = bit(fv[0]&0x2 != 0)
	bitStream[2] = bit(fv[0]&0x1 != 0)

	bitStream[bitStreamLen-3] = bit(fv[7]&0x40 != 0)
	bitStream[bitStreamLen-2] = bit(fv[7]&0x20 != 0)
	bitStream[bitStreamLen-1] = bit(fv[7]&0x10 != 0)

	index0 := 3 + 3*12 - 1
	for vecNum := 3; vecNum >= 1; vecNum-- {
		t := fv[vecNum]
		for i := 0; i < 12; i++ {
			bitStream[index0] = bit(t&0x1 != 0)
			t >>= 1
			index0--
		}
	}

	index0 = 3 + 3*12 + 3*11 - 1
	for vecNum := 6; vecNum >= 4; vecNum-- {
		t := fv[vecNum]
		for i := 0; i < 11; i++ {
			bitStream[index0] = bit(t&0x1 != 0)
			t >>= 1
			index0--
		}
	}

	// Rebuild b1
	index0 = 3 + 3*12
	tmp = 0
	for i := int16(0); i < p.numBands; i++ {
		tmp = (tmp << 1) | bitStream[index0]
		index0++
	}
	p.bVec[1] = tmp

	// Rebuild b2
	tmp = 0
	tmp |= bitStream[index0] << 1
	index0++
	tmp |= bitStream[index0]
	index0++
	p.bVec[2] = (fv[0] & 0x38) | (tmp << 1) | (shr(fv[7], 3) & 0x01)

	// Shift the rest of sequence
	sh := int(p.numBands) + 2
	for ; index0 < bitStreamLen; index0++ {
		bitStream[index0-sh] = bitStream[index0]
	}

	// Priority ReScanning
	b := p.bVec[3:]
	ba := p.bitAlloc[:]
	for i := 0; i < bNum; i++ {
		ba[i], b[i] = 0, 0
	}

	// Unpack bit allocation table's item
	getBitAllocation(p.numHarms, p.bitAlloc[:])

	index0 = 0
	var bitThr int16
	if p.numHarms == 0xb {
		bitThr = 9
	} else {
		bitThr = ba[0]
	}

	for index0 < bitStreamLen-int(p.numBands)-2 {
		for i := int16(0); i < p.numHarms-1; i++ {
			if bitThr != 0 && bitThr <= ba[i] {
				b[i] = (b[i] << 1) | bitStream[index0]
				index0++
			}
		}
		bitThr--
	}

	// Synchronization Bit Decoding
	p.bVec[p.numHarms+2] = fv[7] & 1
}

// vUvDecode expands the band voicing decisions to harmonics (v_uv_decode).
func vUvDecode(p *imbeParam) {
	numHarms := p.numHarms
	numBands := p.numBands
	vuVec := p.bVec[1]
	mask := int16(1) << uint(numBands-1)

	vZap(p.vUvDsn[:])
	var i, uvCnt int16
	for k := int16(0); k < numHarms; k++ {
		if vuVec&mask != 0 {
			p.vUvDsn[k] = 1
		} else {
			p.vUvDsn[k] = 0
			uvCnt++
		}
		i++
		if i == 3 {
			if numBands > 1 {
				numBands--
				mask >>= 1
			}
			i = 0
		}
	}
	p.lUv = uvCnt
}

// saDecodeInit is imbe_vocoder::sa_decode_init.
func (d *Decoder) saDecodeInit() {
	d.numHarmsPrev1 = 30
	for i := range d.saPrev1 {
		d.saPrev1[i] = 0
	}
}

// saDecode reconstructs the spectral amplitudes (imbe_vocoder::sa_decode).
func (d *Decoder) saDecode(p *imbeParam) {
	var gainVec, gainR [6]int16
	var tVec [numHarmsMax]int16
	var cVec [maxBlockLen]int16
	var saTmp [numHarmsMax]int32

	numHarms := p.numHarms
	index := numHarms - numHarmsMin
	ba := 0
	bv := 2

	// Decoding the Gain Vector. gain_vec has signed Q5.11 format
	gss := int(index) * 5
	gainVec[0] = gainQntTbl[p.bVec[bv]]
	bv++
	for i := 1; i < 6; i++ {
		gainVec[i] = extractL(lShr(deqntByStep(p.bVec[bv], gainStepSizeTbl[gss+i-1], p.bitAlloc[ba]), 5))
		bv++
		ba++
	}

	idct(gainVec[:], numPredResBlks, numPredResBlks, gainR[:])

	lmprblItem := lmprblTbl[index]

	// Decoding the Higher Order DCT Coefficients
	tv := 0
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

	kCoef := kCoefFor(numHarms, d.numHarmsPrev1)
	roCoef := roCoefFor(numHarms)

	kAcc := kCoef
	var sum int32

	for i := int(d.numHarmsPrev1) + 1; i < numHarmsMax+2; i++ {
		d.saPrev1[i] = d.saPrev1[d.numHarmsPrev1]
	}

	for i := int16(0); i < numHarms; i++ {
		idx := uint16(kAcc >> 24)                      // Get integer part
		siCoef := int16((kAcc - uint32(idx)<<24) >> 9) // Get fractional part
		if siCoef == 0 {
			t := lMpyLs(d.saPrev1[idx], roCoef)             // sa_prev1 here is in Q10.22 format
			saTmp[i] = lAdd(lShr(lDepositH(tVec[i]), 5), t) // Convert t_vec to Q10.22 and add ...
			sum = lAdd(sum, d.saPrev1[idx])                 // sum in Q10.22 format
		} else {
			t := lMpyLs(d.saPrev1[idx], sub(0x7FFF, siCoef))
			sum = lAdd(sum, t)
			saTmp[i] = lAdd(lShr(lDepositH(tVec[i]), 5), lMpyLs(t, roCoef))
			t = lMpyLs(d.saPrev1[idx+1], siCoef)
			sum = lAdd(sum, t)
			saTmp[i] = lAdd(saTmp[i], lMpyLs(t, roCoef))
		}
		kAcc += kCoef
	}

	tmp := normS(numHarms)
	p.divOneByNumHarmSh = tmp
	tmp1 := divS(0x4000, numHarms<<uint(tmp)) // 1/num_harms with scaling
	p.divOneByNumHarm = tmp1

	sum = lShr(lMpyLs(lMpyLs(sum, roCoef), tmp1), 14-tmp)

	for i := int16(1); i <= numHarms; i++ {
		d.saPrev1[i] = lSub(saTmp[i-1], sum)
		p.sa[i-1] = pow2(d.saPrev1[i])
	}

	d.numHarmsPrev1 = numHarms
}

// saEnh is the spectral amplitude enhancement (sa_enh).
func saEnh(p *imbeParam) {
	var saTmp, cosW [numHarmsMax]int16

	numHarm := p.numHarms
	sa := p.sa[:numHarm]
	st := saTmp[:numHarm]

	copy(st, sa) // v_equ

	rm0 := lVMagsq(sa)
	if rm0 == 0 {
		return
	}

	nm := normL(rm0)
	if rm0 == max32 {
		nm = 1
		vEquShr(st, sa, nm)
		rm0 = lVMagsq(st)
	} else if nm > 2 {
		nm = -(nm >> 1)
		vEquShr(st, sa, nm)
		rm0 = lVMagsq(st)
	}

	w0 := p.fundFreq
	var cosAcc, rm1 int32
	for i := range st {
		cosAcc = lAdd(cosAcc, w0)
		cosW[i] = cosFxp(extractH(cosAcc))
		rm1 = lAdd(rm1, lMpyLs(lMult(st[i], st[i]), cosW[i]))
	}

	rm0s := extractH(rm0)
	rm1s := extractH(rm1)

	lRm0_2 := lMult(rm0s, rm0s)
	lRm1_2 := lMult(rm1s, rm1s)
	lDen := lSub(lRm0_2, lRm1_2)
	lDen = lMult(extractH(lDen), rm0s)
	nm1 := normL(lDen)
	lDen = lShl(lDen, nm1)
	nm2 := normL(w0)
	lDen = lMpyLs(lDen, extractH(lShl(w0, nm2))) // w0 * Rm0 * (Rm0^2 - Rm1^2)
	nm1 += nm2                                   // total denominator shift

	if lDen < 1 {
		return // reference: "fix bug infinite loop due to invalid input"
	}

	lSumRm02Rm12 := lAdd(lShr(lRm0_2, 2), lShr(lRm1_2, 2))
	rm0Rm1 := shr(multR(rm0s, rm1s), 1)

	for i := int16(0); i < numHarm; i++ {
		if ((i+1)<<3) > numHarm && st[i] != 0 {
			lNum := lSub(lSumRm02Rm12, lMult(rm0Rm1, cosW[i]))
			totNm := normL(lNum)
			lNum = lShl(lNum, totNm)
			for lNum >= lDen {
				lNum = lShr(lNum, 1)
				totNm--
			}

			tmp := divS(extractH(lNum), extractH(lDen))
			totNm -= nm1

			lTmp := lMult(st[i], st[i])
			nm2 = normL(lTmp)
			lTmp = lShl(lTmp, nm2)
			lTmp = lMult(extractH(lTmp), tmp)
			totNm += nm2
			totNm -= 2

			if totNm <= 0 {
				lTmp = lShr(lTmp, add(8, totNm))
				lTmp, totNm = sqrtLExp(lTmp)
				lTmp = lShr(lTmp, totNm)
				lTmp, totNm = sqrtLExp(lTmp)
				lTmp = lShr(lTmp, totNm)
				lTmp = lMult(extractH(lTmp), cnst0_9898Q1_15)
				tmp = extractH(lShl(lTmp, 1))
			} else if totNm <= 8 {
				lTmp = lShr(lTmp, totNm)
				lTmp, totNm = sqrtLExp(lTmp)
				lTmp = lShr(lTmp, totNm)
				lTmp, totNm = sqrtLExp(lTmp)
				lTmp = lShr(lTmp, totNm+1)
				tmp = mult(extractH(lTmp), cnst0_9898Q1_15)
			} else {
				// Note: nm1 (the denominator shift) is overwritten here and
				// stays modified for the following harmonics, as in the
				// reference.
				nm1 = int16(int32(totNm) & 0xFFFE)
				lTmp = lShr(lTmp, totNm-nm1)
				lTmp, totNm = sqrtLExp(lTmp)
				lTmp = lShr(lTmp, totNm)
				totNm = nm1 >> 1
				nm1 = int16(int32(totNm) & 0xFFFE)
				lTmp = lShr(lTmp, totNm-nm1)
				lTmp, totNm = sqrtLExp(lTmp)
				lTmp = lShr(lTmp, totNm)
				lTmp = lMult(extractH(lTmp), cnst0_9898Q1_15)
				totNm = nm1 >> 1
				tmp = extractH(lShr(lTmp, totNm+1))
			}

			switch {
			case tmp > cnst1_2Q2_14:
				sa[i] = extractH(lShl(lMult(sa[i], cnst1_2Q2_14), 1))
			case tmp < cnst0_5Q2_14:
				sa[i] = shr(sa[i], 1)
			default:
				sa[i] = extractH(lShl(lMult(sa[i], tmp), 1))
			}
		}
	}

	// Compute the correct scale factor
	vEquShr(st, sa, nm)
	lSumMod := lVMagsq(st)
	if lSumMod > rm0 {
		tmp := divS(extractH(rm0), extractH(lSumMod))
		lTmp, totNm := sqrtLExp(lDepositH(tmp))
		tmp = shr(extractH(lTmp), totNm)
		for i := range sa {
			sa[i] = multR(sa[i], tmp)
		}
	}
}

// vSyntInit is imbe_vocoder::v_synt_init.
func (d *Decoder) vSyntInit() {
	for i := 0; i < numHarmsMax; i++ {
		d.phMem[i] = uint32(lDepositH(d.randGen()))
		d.vuDsnPrev[i] = 0
	}
	d.numHarmsPrev3 = 0
	d.fundFreqPrev = 0
}

// vSynt synthesises the voiced component (imbe_vocoder::v_synt). Phase
// accumulators use wrapping 32-bit arithmetic, as in the reference.
func (d *Decoder) vSynt(p *imbeParam, snd *[frameLen]int16) {
	var lSnd [frameLen]int32
	var phMemPrev, dph [numHarmsMax]uint32

	fundFreq := p.fundFreq
	numHarms := p.numHarms
	vuDsn := &p.vUvDsn
	sa := &p.sa
	numHarmsInv := p.divOneByNumHarm
	numHarmsSh := p.divOneByNumHarmSh
	numUv := p.lUv
	ffPrev := d.fundFreqPrev

	// Update phases (calculated phase value correspond to bound of frame)
	lTmp := (((ffPrev + fundFreq) >> 7) * frameLen / 2) << 7 // integer multiplication by mod 1
	var lPhAcc int32
	for i := 0; i < numHarmsMax; i++ {
		phMemPrev[i] = d.phMem[i]
		lPhAcc += lTmp
		d.phMem[i] += uint32(lPhAcc)
		dph[i] = 0
	}

	numHarmsMax := numHarms
	if d.numHarmsPrev3 > numHarms {
		numHarmsMax = d.numHarmsPrev3
	}
	numHarmsMax4 := numHarmsMax >> 2

	freqFlag := lAbs(lSub(fundFreq, ffPrev)) >= lMpyLs(fundFreq, cnst0_1Q1_15)

	var lPhStep, lPhStepPrev int32
	for i := int16(0); i < numHarmsMax; i++ {
		lPhStep += fundFreq
		lPhStepPrev += ffPrev

		if i > numHarmsMax4 {
			if numUv == numHarms {
				dph[i] = uint32(lDepositH(d.randGen()))
			} else {
				t := lMult(d.randGen(), numHarmsInv)
				dph[i] = uint32(lShr(t, 15-numHarmsSh) * int32(numUv))
			}
			d.phMem[i] += dph[i]
		}

		cur, prev := vuDsn[i], d.vuDsnPrev[i]
		if cur == 0 && prev == 0 {
			continue
		}

		if cur == 1 && prev == 0 { // unvoiced => voiced
			s := 0
			acc := int32(d.phMem[i]) - (((lPhStep >> 7) * 104) << 7)
			for j := 56; j <= 104; j++ {
				t := lMult(ws[s], sa[i])
				s++
				t = lMpyLs(t, cosFxp(extractH(acc)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)
				acc += lPhStep
			}
			for j := 105; j <= 159; j++ {
				t := lMult(sa[i], cosFxp(extractH(acc)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)
				acc += lPhStep
			}
			continue
		}

		if cur == 0 && prev == 1 { // voiced => unvoiced
			s := 48
			acc := int32(phMemPrev[i])
			for j := 0; j <= 55; j++ {
				t := lMult(d.saPrev3[i], cosFxp(extractH(acc)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)
				acc += lPhStepPrev
			}
			for j := 56; j <= 104; j++ {
				t := lMult(ws[s], d.saPrev3[i])
				s--
				t = lMpyLs(t, cosFxp(extractH(acc)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)
				acc += lPhStepPrev
			}
			continue
		}

		if i >= 7 || freqFlag {
			sAux := 48
			accAux := int32(phMemPrev[i])
			s := 0
			acc := int32(d.phMem[i]) - (((lPhStep >> 7) * 104) << 7)
			for j := 0; j <= 55; j++ {
				t := lMult(d.saPrev3[i], cosFxp(extractH(accAux)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)
				accAux += lPhStepPrev
			}
			for j := 56; j <= 104; j++ {
				t := lMult(ws[sAux], d.saPrev3[i])
				sAux--
				t = lMpyLs(t, cosFxp(extractH(accAux)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)

				t = lMult(ws[s], sa[i])
				s++
				t = lMpyLs(t, cosFxp(extractH(acc)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)

				accAux += lPhStepPrev
				acc += lPhStep
			}
			for j := 105; j <= 159; j++ {
				t := lMult(sa[i], cosFxp(extractH(acc)))
				t = lShr(t, 1)
				lSnd[j] = lAdd(lSnd[j], t)
				acc += lPhStep
			}
			continue
		}

		lAmpStep := lMpyLs(lShr(lDepositH(sub(sa[i], d.saPrev3[i])), 4+1), cnst0_1Q1_15) // (sa[i] - sa_prev3[i]) / 160
		lAmpAcc := lShr(lDepositH(d.saPrev3[i]), 1)
		lPhStepAux := lMpyLs(lShr(fundFreq-ffPrev, 4+1), cnst0_1Q1_15) // (fund_freq - fund_freq_prev)/(2*160)
		lPhStepAux = ((lPhStepAux >> 7) * int32(i+1)) << 7
		acc := int32(phMemPrev[i])
		lTmp1 := lMpyLs(lShr(int32(dph[i]), 4), cnst0_1Q1_15) // dph[i] / 160
		for j := int32(0); j < 160; j++ {
			accAux := ((lPhStepAux >> 9) * j) << 9
			accAux = ((accAux >> 9) * j) << 9
			t := lMpyLs(lAmpAcc, cosFxp(extractH(acc+accAux)))
			lSnd[j] = lAdd(lSnd[j], t)
			lAmpAcc = lAdd(lAmpAcc, lAmpStep)
			acc += lPhStepPrev
			acc += lTmp1
		}
	}

	for i := range snd {
		snd[i] = extractH(lSnd[i])
	}

	vZap(d.vuDsnPrev[:])
	copy(d.vuDsnPrev[:numHarms], p.vUvDsn[:numHarms])
	copy(d.saPrev3[:numHarms], p.sa[:numHarms])
	d.numHarmsPrev3 = numHarms
	d.fundFreqPrev = fundFreq
}

// uvSyntInit is imbe_vocoder::uv_synt_init (fft_init is a package table).
func (d *Decoder) uvSyntInit() {
	vZap(d.uvMem[:])
}

// uvSynt synthesises the unvoiced component (imbe_vocoder::uv_synt).
func (d *Decoder) uvSynt(p *imbeParam, snd *[frameLen]int16) {
	uw := &d.uw // Cmplx16 Uw[FFTLENGTH], interleaved
	*uw = [2 * fftLength]int16{}

	fundFreq := p.fundFreq
	fundFreq2 := lShr(fundFreq, 1)
	accA := lSub(fundFreq, fundFreq2)
	accB := lAdd(fundFreq, fundFreq2)

	for i := int16(0); i < p.numHarms; i++ {
		indexA := bandIndex(extractH(accA))
		indexB := bandIndex(extractH(accB))
		indexAux := 256 - indexA
		sa := shl(p.sa[i], 3)

		if p.vUvDsn[i] != 0 {
			for ; indexA < indexB; indexA, indexAux = indexA+1, indexAux-1 {
				uw[2*int(indexA)], uw[2*int(indexA)+1] = 0, 0
				uw[2*int(indexAux)], uw[2*int(indexAux)+1] = 0, 0
			}
		} else {
			for ; indexA < indexB; indexA, indexAux = indexA+1, indexAux-1 {
				re := mult(sa, d.randGen())
				im := mult(sa, d.randGen())
				uw[2*int(indexA)], uw[2*int(indexA)+1] = re, im
				uw[2*int(indexAux)], uw[2*int(indexAux)+1] = re, negate(im)
			}
		}

		accA = lAdd(accA, fundFreq)
		accB = lAdd(accB, fundFreq)
	}

	fft(uw[:], fftLength, -1)

	copy(snd[:105], d.uvMem[:])

	indexAux := 73
	for i := 105; i < frameLen; i++ {
		snd[i] = shl(uw[2*indexAux], 3)
		indexAux++
	}

	// Weighted Overlap Add Algorithm
	indexAux = 24
	ia, ib := 0, 48
	for i := 56; i < 105; i++ {
		snd[i] = extractH(lAdd(lMult(snd[i], ws[ib]), lMult(shl(uw[2*indexAux], 3), ws[ia])))
		indexAux++
		ia++
		ib--
	}

	indexAux = 128
	for i := 0; i < 105; i++ {
		d.uvMem[i] = shl(uw[2*indexAux], 3)
		indexAux++
	}
}
