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

// Encoder front end and pitch estimation, ported from dc_rmv.cc, pe_lpf.cc
// and pitch_est.cc.

const cnst0_99Q1_15 = 0x7EB8

// dcRmv is the DC-removal high-pass filter (dc_rmv).
func dcRmv(sigin, sigout []int16, mem *int32, n int) {
	lMem := *mem
	for i := 0; i < n; i++ {
		lTmp := lDepositH(sigin[i])
		lMem = lAdd(lMem, lTmp)
		sigout[i] = round16(lMem)
		lMem = lMpyLs(lMem, cnst0_99Q1_15)
		lMem = lSub(lMem, lTmp)
	}
	*mem = lMem
}

// peLpf is the pitch estimator's low-pass filter (pe_lpf).
func peLpf(sigin, sigout []int16, mem *[peLpfOrd]int16, n int) {
	for k := 0; k < n; k++ {
		copy(mem[:peLpfOrd-1], mem[1:])
		mem[peLpfOrd-1] = sigin[k]
		var lSum int32
		for i := 0; i < peLpfOrd; i++ {
			lSum = lMac(lSum, mem[i], lpfCoef[i])
		}
		sigout[k] = round16(lSum)
	}
}

// pitchEstInit is imbe_vocoder::pitch_est_init.
func (e *Encoder) pitchEstInit() {
	e.prevPitch, e.prevPrevPitch = 158, 158
	e.prevEP, e.prevPrevEP = 0, 0
}

// autocorr is imbe_vocoder::autocorr.
func autocorr(sigin []int16, shift, scaleShift int16) int32 {
	var lSum int32
	for i := int16(0); i < pitchEstFrame-shift; i++ {
		lSum = lAdd(lSum, lShr(lMult(sigin[i], sigin[i+shift]), scaleShift))
	}
	return lSum
}

// eP computes the E(p) error function (imbe_vocoder::e_p).
func eP(sigin []int16, resBuf *[203]int16) {
	var sigWndwed [pitchEstFrame]int16
	var corr [259]int32
	var scaleShift int16

	// Windowing input signal s * wi^2
	for i := 0; i < pitchEstFrame; i++ {
		sigWndwed[i] = multR(sigin[i], wi[i])
	}

	var lSum int32
	for i := 0; i < pitchEstFrame; i++ {
		lSum = lAdd(lSum, lMpyLs(lMult(sigin[i], sigin[i]), wi[i])) // sum(s^2 * wi^2)
	}

	// Check for the overflow
	if lSum == max32 {
		// Recalculate with scaling
		lSum = 0
		for i := 0; i < pitchEstFrame; i++ {
			lSum = lAdd(lSum, lMpyLs(lShr(lMult(sigin[i], sigin[i]), 5), wi[i]))
		}
		scaleShift = 5
	}

	var lE0 int32
	for i := 0; i < pitchEstFrame; i++ {
		lE0 = lAdd(lE0, lShr(lMult(sigWndwed[i], sigWndwed[i]), scaleShift)) // sum(s^2 * wi^4)
	}

	// Correlation for time shift in range 21...150 with step 0.5
	for tmp, i := int16(21), 0; tmp <= 150; tmp, i = tmp+1, i+2 {
		corr[i] = autocorr(sigWndwed[:], tmp, scaleShift)
	}
	for i := 1; i < 258; i += 2 {
		corr[i] = lShr(lAdd(corr[i-1], corr[i+1]), 1)
	}

	// variable to calculate 1 - P * sum(wi ^4) in denominator
	denPartAcc := int16(cnst0_8717Q1_15)
	indexStep := int16(42) // 21 in Q15.1, also used as p
	indexBeg := int16(0)
	lE0 = lShr(lE0, 7) // compensate wi scaling

	// p = 21...122 by step 0.5
	for i := 0; i < 203; i++ {
		var lTmp int32
		for j := indexBeg; j <= 258; j += indexStep {
			lTmp = lAdd(lTmp, corr[j])
		}
		lTmp = lShr(lTmp, 6)           // compensate wi scaling
		lTmp = lAdd(lTmp, lE0)         // For n = 0
		lTmp = lTmp * int32(indexStep) // plain C multiply (wraps)
		lNum := lSub(lSum, lTmp)
		indexBeg++
		indexStep++

		lDen := lMpyLs(lSum, denPartAcc)
		if lNum < lDen && lDen != 0 {
			if lNum <= 0 {
				resBuf[i] = 0
			} else {
				tmp := normL(lDen)
				tmp = divS(extractH(lShl(lNum, tmp)), extractH(lShl(lDen, tmp)))
				resBuf[i] = shr(tmp, 3) // convert to Q4.12
			}
		} else {
			resBuf[i] = cnst1_00Q4_12
		}
		denPartAcc = sub(denPartAcc, cnst0_0031Q1_15)
	}
}

func hiByte(v uint16) int16 { return int16(v >> 8) }
func loByte(v uint16) int16 { return int16(v & 0xFF) }

// pitchEst is imbe_vocoder::pitch_est.
func (e *Encoder) pitchEst(p *imbeParam, framesBuf []int16) {
	var eP0, eP1, eP2, e1p1E2p2EstSave, eP2Min [203]int16

	// Calculate E(p) function for current and two future frames
	eP(framesBuf[0:], &eP0)

	// Look-Back Pitch Tracking
	minIndex := hiByte(minMaxTbl[e.prevPitch])
	maxIndex := loByte(minMaxTbl[e.prevPitch])
	pb := minIndex
	ePCur := eP0[minIndex]
	for q := minIndex + 1; q <= maxIndex; q++ {
		if eP0[q] < ePCur {
			ePCur = eP0[q]
			pb = q
		}
	}
	ceb := add(ePCur, add(e.prevEP, e.prevPrevEP))

	if ceb <= cnst0_48Q4_12 {
		e.prevPrevPitch = e.prevPitch
		e.prevPitch = pb
		e.prevPrevEP = e.prevEP
		e.prevEP = eP0[pb]
		p.pitch = pb + 42 // Q15.1
		p.eP = e.prevEP
		return
	}

	// Look-Ahead Pitch Tracking
	eP(framesBuf[frameLen:], &eP1)
	eP(framesBuf[2*frameLen:], &eP2)

	p0Est := int16(0)
	cefEst := eP0[0] + eP1[0] + eP2[0] // plain int16 adds

	for p1 := int16(0); p1 < 203; p1++ {
		p2 := hiByte(minMaxTbl[p1])
		p2Max := loByte(minMaxTbl[p1])
		sTmp := eP2[p1]
		for ; p2 <= p2Max; p2++ {
			if eP2[p2] < sTmp {
				sTmp = eP2[p2]
			}
		}
		eP2Min[p1] = sTmp
	}

	for p0 := int16(0); p0 < 203; p0++ {
		est := eP1[p0] + eP2Min[p0] // plain int16 add
		p1 := hiByte(minMaxTbl[p0])
		p1Max := loByte(minMaxTbl[p0])
		for ; p1 <= p1Max; p1++ {
			if v := add(eP1[p1], eP2Min[p1]); v < est {
				est = v
			}
		}
		e1p1E2p2EstSave[p0] = est
		cef := add(eP0[p0], est)
		if cef < cefEst {
			cefEst = cef
			p0Est = p0
		}
	}

	pf := p0Est

	// Sub-multiples analysis
	if pf >= 42 {
		var i int
		switch {
		case pf < 84:
			i = 1
		case pf < 126:
			i = 2
		case pf < 168:
			i = 3
		default:
			i = 4
		}
		pFp := uint16((pf + 42) << 8) // unsigned Q7.19
		var tmp uint16
		for i > 0 {
			i--
			switch i {
			case 0:
				tmp = pFp >> 1 // P_est/2
			case 1:
				tmp = uint16((uint32(pFp) * 0x5555) >> 16) // P_est/3
			case 2:
				tmp = pFp >> 2 // P_est/4
			case 3:
				tmp = uint16((uint32(pFp) * 0x3333) >> 16) // P_est/5
			}
			pIndex := int16((int32(tmp)+0x0080)>>8) - 42
			cef := add(eP0[pIndex], e1p1E2p2EstSave[pIndex])
			if cef <= cnst0_85Q4_12 && multR(cef, cnst0_5882Q1_15) <= cefEst {
				pf = pIndex
				break
			}
			if cef <= cnst0_4Q4_12 && multR(cef, cnst0_2857Q1_15) <= cefEst {
				pf = pIndex
				break
			}
			if cef <= cnst0_05Q4_12 {
				pf = pIndex
				break
			}
		}
	}

	cef := add(eP0[pf], e1p1E2p2EstSave[pf])
	var q int16
	if ceb <= cef {
		q = pb
	} else {
		q = pf
	}
	e.prevPrevPitch = e.prevPitch
	e.prevPitch = q
	e.prevPrevEP = e.prevEP
	e.prevEP = eP0[q]
	p.pitch = q + 42 // Q15.1
	p.eP = e.prevEP
}
