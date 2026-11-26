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

// DCT/IDCT and FFT, ported from dsp_sub.cc.

// wrArray/wiArray are the FFT twiddle tables. The reference recomputes them
// into per-instance members (wr_array/wi_array) in fft_init(); they depend
// on nothing but constants, so they are computed once here and only read
// afterwards.
var wrArray, wiArray = fftInit()

// fftInit builds the twiddle tables (imbe_vocoder::fft_init).
func fftInit() (wr, wi [fftLength/2 + 1]int16) {
	fftLen2 := shr(fftLength, 1)
	shift := normS(fftLen2)
	step := shl(2, shift)
	var theta int16

	for i := int16(0); i <= fftLen2; i++ {
		wr[i] = cosFxp(theta)
		wi[i] = sinFxp(theta)
		if i >= fftLen2-1 {
			theta = oneQ15
		} else {
			theta = add(theta, step)
		}
	}
	return
}

// idct performs the inverse DCT (imbe_vocoder::idct).
func idct(in []int16, mLim, iLim int16, out []int16) {
	var anglIntl, anglIntl2 uint16

	if mLim == 1 {
		anglIntl = cnst0_5Q1_15
		anglIntl2 = cnst1_0Q1_15
	} else {
		anglIntl = uint16(divS(cnst0_5Q5_11, mLim<<11)) // calculate 0.5/m_lim
		anglIntl2 = uint16(shl(int16(anglIntl), 1))
	}

	anglStep := anglIntl
	for i := int16(0); i < iLim; i++ {
		var sum int32
		anglAcc := anglStep
		for m := int16(1); m < mLim; m++ {
			sum = lAdd(sum, lShr(lMult(in[m], cosFxp(int16(anglAcc))), 7))
			anglAcc += anglStep
		}
		sum = lAdd(sum, lShr(lDepositH(in[0]), 8))
		out[i] = extractL(lShrR(sum, 8))
		anglStep += anglIntl2
	}
}

// dct performs the DCT (imbe_vocoder::dct).
func dct(in []int16, mLim, iLim int16, out []int16) {
	var anglIntl, anglIntl2 uint16

	if mLim == 1 {
		anglIntl = cnst0_5Q1_15
		anglIntl2 = cnst1_0Q1_15
	} else {
		anglIntl = uint16(divS(cnst0_5Q5_11, mLim<<11)) // calculate 0.5/m_lim
		anglIntl2 = uint16(shl(int16(anglIntl), 1))
	}

	// Calculate first coefficient
	var sum int32
	for m := int16(0); m < mLim; m++ {
		sum = lAdd(sum, lDepositL(in[m]))
	}
	out[0] = extractL(lMpyLs(sum, int16(anglIntl2)))

	// Calculate the others coefficients
	anglBegin := anglIntl
	anglStep := anglIntl2
	for i := int16(1); i < iLim; i++ {
		sum = 0
		anglAcc := anglBegin
		for m := int16(0); m < mLim; m++ {
			sum = lAdd(sum, lDepositL(mult(in[m], cosFxp(int16(anglAcc)))))
			anglAcc += anglStep
		}
		out[i] = extractL(lMpyLs(sum, int16(anglIntl2)))

		anglStep += anglIntl2
		anglBegin += anglIntl
	}
}

// fft replaces data (interleaved re/im, len 2*nn, Q14) by its DFT when
// isign is 1, or by its inverse DFT times nn when isign is -1
// (imbe_vocoder::fft). The reference indexes the array from 1; data[k-1]
// below is the reference's data[k].
func fft(d []int16, nn int16, isign int16) {
	data := func(k int16) *int16 { return &d[k-1] }

	n := shl(nn, 1)
	j := int16(1)
	for i := int16(1); i < n; i += 2 {
		if j > i {
			*data(j), *data(i) = *data(i), *data(j)
			*data(j + 1), *data(i + 1) = *data(i + 1), *data(j + 1)
		}
		m := nn
		for m >= 2 && j > m {
			j = sub(j, m)
			m = shr(m, 1)
		}
		j = add(j, m)
	}
	mmax := int16(2)

	// initialize index step
	indexStep := nn

	for n > mmax {
		istep := shl(mmax, 1) // istep = 2 * mmax

		index := int16(0)
		indexStep = shr(indexStep, 1)

		wr := int16(oneQ15)
		wi := int16(0)
		for m := int16(1); m < mmax; m += 2 {
			for i := m; i <= n; i += istep {
				j = i + mmax
				dj, dj1 := data(j), data(j+1)
				di, di1 := data(i), data(i+1)

				// tempr = wr * data[j] - wi * data[j+1]
				t1 := lShr(lMult(wr, *dj), 1)
				t2 := lShr(lMult(wi, *dj1), 1)
				tempr := lSub(t1, t2)

				// tempi = wr * data[j+1] + wi * data[j]
				t1 = lShr(lMult(wr, *dj1), 1)
				t2 = lShr(lMult(wi, *dj), 1)
				tempi := lAdd(t1, t2)

				// data[j] = data[i] - tempr
				t1 = lShr(lDepositH(*di), 1)
				*dj = round16(lSub(t1, tempr))

				// data[i] += tempr
				*di = round16(lAdd(t1, tempr))

				// data[j+1] = data[i+1] - tempi
				t1 = lShr(lDepositH(*di1), 1)
				*dj1 = round16(lSub(t1, tempi))

				// data[i+1] += tempi
				*di1 = round16(lAdd(t1, tempi))
			}
			index = add(index, indexStep)
			wr = wrArray[index]
			if isign < 0 {
				wi = negate(wiArray[index])
			} else {
				wi = wiArray[index]
			}
		}
		mmax = istep
	}
}
