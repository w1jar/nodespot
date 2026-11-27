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

// Pitch refinement and voiced/unvoiced determination, ported from
// pitch_ref.cc and v_uv_det.cc. Spectra are interleaved re/im int16
// slices: bin k is (fb[2k], fb[2k+1]).

const (
	pitchRefFrame = 221 // PITCH_REF_FRAME (unused by the reference too)
	minIndex      = 50  // MIN_INDEX

	cnst0_5625Q1_15 = 0x4800
	cnst0_45Q1_15   = 0x3999
	cnst0_1741Q1_15 = 0x164A
	cnst0_1393Q1_15 = 0x11D5
	cnst0_01Q1_15   = 0x0148
	cnst0_0025Q1_15 = 0x0051
	cnst0_25Q1_15   = 0x2000
	cnstPi4Q1_15    = 0x6488
	cnst0_55Q4_12   = 0x08CD
)

// q16 computes the C expression (UWord32)c * (Word16)x, reinterpreted as
// a Word32 as it is when passed to a basic op.
func q16(c uint32, x int16) int32 { return int32(c * uint32(int32(x))) }

// bandIndex computes (h >> 8) + ((h & 0xFF) ? 1 : 0).
func bandIndex(h int16) int16 {
	r := h >> 8
	if h&0xFF != 0 {
		r++
	}
	return r
}

// pitchRef refines the pitch estimate (pitch_ref).
func pitchRef(p *imbeParam, fb []int16) {
	var spRec [fftLength]int16 // Cmplx16 sp_rec[FFTLENGTH/2], interleaved
	var indexTbl [20]int16
	var pitchCand int16
	var fundFreqCand int32

	pitchEst := shl(p.pitch, 7)             // Convert to Q8.8
	pitchEst = sub(pitchEst, cnst1_125Q8_8) // Sub 1.125 = 9/8

	lDiffMin := int32(max32)
	for i := 0; i < 19; i++ {
		shift := normS(pitchEst)
		tmp := shl(pitchEst, shift)
		tmp = divS(0x4000, tmp)
		fundFreq := lShl(int32(tmp), shift+11)

		fundFreqAcc := fundFreq
		fundFreq2 := lShr(fundFreq, 1)
		accA := lSub(fundFreq, fundFreq2)
		accB := lAdd(fundFreq, fundFreq2)

		// Calculate upper limit for spectrum reconstruction
		upLim := extractH(lShr(q16(cnst0_9254Q0_16, pitchEst), 1)) // 0.9254/fund_freq
		upLim = sub(upLim, cnst0_5Q8_8)                            // sub 0.5
		upLim = int16(int32(upLim) & 0xFF00)                       // extract fixed part
		upLim = mult(upLim, extractH(fundFreq))
		upLim = shr(upLim, 1)

		indexB := int16(0)
		for indexB <= upLim {
			indexA := bandIndex(extractH(accA))
			indexB = bandIndex(extractH(accB))

			if indexB >= minIndex {
				lTmp := lShl(lDepositH(indexA), 8)
				lTmp = lSub(lTmp, fundFreqAcc)
				lTmp = lAdd(lTmp, 0x00020000) // for rounding purpose
				lTmp = lShr(lTmp, 2)

				indexASave := indexA
				itInd := 0
				var ampReAcc, ampImAcc int32
				for ; indexA < indexB; indexA++ {
					indexWr := extractH(lTmp)
					if indexWr < 0 && lTmp&0xFFFF != 0 { // truncating for negative number
						indexWr = add(indexWr, 1)
					}
					indexWr = add(indexWr, 160)
					indexTbl[itInd] = indexWr
					itInd++
					if indexWr >= 0 && indexWr <= 320 {
						ampReAcc = lMac(ampReAcc, fb[2*int(indexA)], wrSp[indexWr])
						ampImAcc = lMac(ampImAcc, fb[2*int(indexA)+1], wrSp[indexWr])
					}
					lTmp = lAdd(lTmp, 0x400000)
				}

				itInd = 0
				for indexA = indexASave; indexA < indexB; indexA++ {
					indexWr := indexTbl[itInd]
					itInd++
					k := 2 * int(indexA)
					if indexWr < 0 || indexWr > 320 {
						spRec[k], spRec[k+1] = 0, 0
					} else {
						spRec[k+1] = mult(mult(extractH(ampImAcc), wrSp[indexWr]), 0x6666)
						spRec[k] = mult(mult(extractH(ampReAcc), wrSp[indexWr]), 0x6666)
					}
				}
			}

			accA = lAdd(accA, fundFreq)
			accB = lAdd(accB, fundFreq)
			fundFreqAcc = lAdd(fundFreqAcc, fundFreq)
		}

		var lSum int32
		for j := int16(minIndex); j <= upLim; j++ {
			k := 2 * int(j)
			reTmp := sub(fb[k], spRec[k])
			imTmp := sub(fb[k+1], spRec[k+1])
			lSum = lMac(lSum, reTmp, reTmp)
			lSum = lMac(lSum, imTmp, imTmp)
		}

		if lSum < lDiffMin {
			lDiffMin = lSum
			pitchCand = pitchEst
			fundFreqCand = fundFreq
		}

		pitchEst = add(pitchEst, cnst0_125Q8_8) // Add 0.125 = 1/8
	}

	p.refPitch = pitchCand
	p.fundFreq = fundFreqCand
}

// pitchRefInit is imbe_vocoder::pitch_ref_init.
func (e *Encoder) pitchRefInit() {
	vZap(e.vUvDsn[:])
	e.thMax = 0
}

// voicedSaCalc is imbe_vocoder::voiced_sa_calc.
func voicedSaCalc(num int32, den int16) int16 {
	lTmp := lMpyLs(num, den)
	lTmp, tmp := sqrtLExp(lTmp)
	lTmp = lShr(lTmp, tmp-3)
	return extractH(lTmp)
}

// unvoicedSaCalc is imbe_vocoder::unvoiced_sa_calc.
func unvoicedSaCalc(num int32, den int16) int16 {
	shift := normS(den)
	tmp := divS(0x4000, shl(den, shift))
	lTmp := lShl(lMpyLs(num, tmp), shift+2)
	lTmp, tmp = sqrtLExp(lTmp)
	lTmp = lShr(lTmp, tmp-2+6)
	lTmp = lMpyLs(lTmp, 0x4A76)
	return extractH(lTmp)
}

// vUvDet is imbe_vocoder::v_uv_det.
func (e *Encoder) vUvDet(p *imbeParam, fb []int16) {
	var indexTbl [30]int16
	var mNum [numHarmsMax]int32
	var mDen [numHarmsMax]int16
	var dsnThr int16

	fundFreq := p.fundFreq

	tmp := shr(add(shr(p.refPitch, 1), cnst0_25Q8_8), 8) // fix(pitch_cand / 2 + 0.5)
	numHarms := extractH(q16(cnst0_9254Q0_16, tmp))      // fix(0.9254 * fix(pitch_cand / 2 + 0.5))

	if numHarms < numHarmsMin {
		numHarms = numHarmsMin
	} else if numHarms > numHarmsMax {
		numHarms = numHarmsMax
	}

	var numBands int16
	if numHarms <= 36 {
		numBands = extractH(int32(uint32(int32(numHarms)+2) * cnst0_33Q0_16)) // fix((L+2)/3)
	} else {
		numBands = numBandsMax
	}

	p.numHarms = numHarms
	p.numBands = numBands

	// M(th) function calculation
	var thLf, thHf int32
	for j := 0; j < 64; j++ {
		thLf = lMac(thLf, fb[2*j], fb[2*j])
		thLf = lMac(thLf, fb[2*j+1], fb[2*j+1])
	}
	for j := 64; j < 128; j++ {
		thHf = lMac(thHf, fb[2*j], fb[2*j])
		thHf = lMac(thHf, fb[2*j+1], fb[2*j+1])
	}

	th0 := lAdd(thLf, thHf)
	if th0 > e.thMax {
		e.thMax = lShr(lAdd(e.thMax, th0), 1)
	} else {
		e.thMax = lAdd(lMpyLs(e.thMax, cnst0_99Q1_15), lMpyLs(th0, cnst0_01Q1_15))
	}

	mFcnNum := lAdd(th0, lMpyLs(e.thMax, cnst0_0025Q1_15))
	mFcnDen := lAdd(th0, lMpyLs(e.thMax, cnst0_01Q1_15))

	var mFcn int16
	if mFcnDen == 0 {
		mFcn = cnst0_25Q1_15
	} else {
		tmp = normL(mFcnDen)
		mFcnDen = lShl(mFcnDen, tmp)
		mFcnNum = lShl(mFcnNum, tmp)
		mFcn = divS(extractH(mFcnNum), extractH(mFcnDen))

		if lTmp := lAdd(lShl(thHf, 2), thHf); thLf < lTmp { // compare th_lf < 5*th_hf
			tmp = normL(lTmp)
			mFcnDen = lShl(lTmp, tmp)
			thLf = lShl(thLf, tmp)
			tmp = divS(extractH(thLf), extractH(mFcnDen))
			lTmp, tmp = sqrtLExp(lDepositH(tmp))
			if tmp != 0 {
				lTmp = lShr(lTmp, tmp)
			}
			mFcn = mult(mFcn, extractH(lTmp))
		}
	}

	fundFrStep := lShl(lMpyLs(fundFreq, cnstPi4Q1_15), 2) // mult by PI
	var uvHarmsCnt, b1Vec, bandCnt, numHarmsCnt int16
	var dNum, dDen, fundFrAcc int32
	fundFreqAcc := fundFreq
	fundFreq2 := lShr(fundFreq, 1)
	accA := lSub(fundFreq, fundFreq2)
	accB := lAdd(fundFreq, fundFreq2)
	var indexB, indexASave int16

	for j := int16(0); j < numHarms; j++ {
		indexA := bandIndex(extractH(accA))
		indexB = bandIndex(extractH(accB))

		lTmp := lShl(lDepositH(indexA), 8)
		lTmp = lSub(lTmp, fundFreqAcc)
		lTmp = lAdd(lTmp, 0x00020000) // for rounding purpose
		lTmp = lShr(lTmp, 2)

		indexASave = indexA
		itInd := 0

		// v/uv determination threshold function
		if numHarmsCnt == 0 { // calculate one time per band
			if p.eP > cnst0_55Q4_12 && bandCnt >= 1 {
				dsnThr = 0
			} else if e.vUvDsn[bandCnt] == 1 {
				dsnThr = mult(mFcn, sub(cnst0_5625Q1_15, mult(cnst0_1741Q1_15, extractH(fundFrAcc))))
			} else {
				dsnThr = mult(mFcn, sub(cnst0_45Q1_15, mult(cnst0_1393Q1_15, extractH(fundFrAcc))))
			}
			fundFrAcc = lAdd(fundFrAcc, fundFrStep)
		}

		var mDenSum, ampReAcc, ampImAcc int32
		for ; indexA < indexB; indexA++ {
			indexWr := extractH(lTmp)
			if indexWr < 0 && lTmp&0xFFFF != 0 { // truncating for negative number
				indexWr = add(indexWr, 1)
			}
			indexWr = add(indexWr, 160)
			indexTbl[itInd] = indexWr
			itInd++
			if indexWr >= 0 && indexWr <= 320 {
				ampReAcc = lMac(ampReAcc, fb[2*int(indexA)], wrSp[indexWr])
				ampImAcc = lMac(ampImAcc, fb[2*int(indexA)+1], wrSp[indexWr])
				mDenSum = lAdd(mDenSum, int32(mult(wrSp[indexWr], wrSp[indexWr])))
			}
			lTmp = lAdd(lTmp, 0x400000)
		}

		scCoef := divS(0x4000, extractL(lShr(mDenSum, 1)))
		imTmp2 := mult(extractH(ampImAcc), scCoef)
		reTmp2 := mult(extractH(ampReAcc), scCoef)
		var mNumSum int32
		itInd = 0
		for indexA = indexASave; indexA < indexB; indexA++ {
			indexWr := indexTbl[itInd]
			itInd++
			var spRecRe, spRecIm int16
			if indexWr >= 0 && indexWr <= 320 {
				spRecIm = mult(imTmp2, wrSp[indexWr])
				spRecRe = mult(reTmp2, wrSp[indexWr])
			}
			k := 2 * int(indexA)
			reTmp := sub(fb[k], spRecRe)
			imTmp := sub(fb[k+1], spRecIm)
			dNum = lMac(dNum, reTmp, reTmp)
			dNum = lMac(dNum, imTmp, imTmp)
			mNumSum = lMac(mNumSum, fb[k], fb[k])
			mNumSum = lMac(mNumSum, fb[k+1], fb[k+1])
		}

		mDen[j] = scCoef
		mNum[j] = mNumSum
		dDen = lAdd(dDen, mNumSum)

		numHarmsCnt++
		if numHarmsCnt == 3 && bandCnt < numBands-1 {
			b1Vec <<= 1
			dk := bandDk(dNum, dDen)
			if dk < dsnThr {
				// voiced band
				e.vUvDsn[bandCnt] = 1
				b1Vec |= 1
				for i := j - 2; i <= j; i++ {
					p.sa[i] = voicedSaCalc(mNum[i], mDen[i])
					p.vUvDsn[i] = 1
				}
			} else {
				// unvoiced band
				e.vUvDsn[bandCnt] = 0
				for i := j - 2; i <= j; i++ {
					p.sa[i] = unvoicedSaCalc(mNum[i], indexB-indexASave)
					p.vUvDsn[i] = 0
					uvHarmsCnt++
				}
			}
			dNum, dDen = 0, 0
			numHarmsCnt = 0
			bandCnt++
		}

		accA = lAdd(accA, fundFreq)
		accB = lAdd(accB, fundFreq)
		fundFreqAcc = lAdd(fundFreqAcc, fundFreq)
	}

	if numHarmsCnt != 0 {
		b1Vec <<= 1
		dk := bandDk(dNum, dDen)
		if dk < dsnThr {
			// voiced band
			e.vUvDsn[bandCnt] = 1
			b1Vec |= 1
			for i := numHarms - numHarmsCnt; i < numHarms; i++ {
				p.sa[i] = voicedSaCalc(mNum[i], mDen[i])
				p.vUvDsn[i] = 1
			}
		} else {
			// unvoiced band
			e.vUvDsn[bandCnt] = 0
			for i := numHarms - numHarmsCnt; i < numHarms; i++ {
				p.sa[i] = unvoicedSaCalc(mNum[i], indexB-indexASave)
				p.vUvDsn[i] = 0
				uvHarmsCnt++
			}
		}
	}

	p.lUv = uvHarmsCnt
	p.bVec[1] = b1Vec                           // Save encoded voiced/unvoiced decision
	p.bVec[0] = shr(sub(p.refPitch, 0x1380), 7) // Pitch encode fix(2*pitch - 39)
}

// bandDk computes the band distortion ratio Dk used by v_uv_det.
func bandDk(dNum, dDen int32) int16 {
	if dDen > dNum && dDen != 0 {
		tmp := normL(dDen)
		return divS(extractH(lShl(dNum, tmp)), extractH(lShl(dDen, tmp)))
	}
	return max16
}
