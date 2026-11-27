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

// Fixed-point math helpers, ported from math_sub.cc.

const (
	x05Q15 = 16384 // X05_Q15  (0.5*(1<<15))
	oneQ15 = 32767 // ONE_Q15  ((1<<15)-1)
)

// pow2 computes 2^x; x in signed Q10.22, result in signed Q14.2 (Pow2).
func pow2(x int32) int16 {
	exponent := extractH(lShr(x, 6))
	if exponent < 0 {
		exponent = add(exponent, 1)
	}
	fraction := extractL(lShr(lSub(x, lShl(lDepositL(exponent), 6+16)), 7))

	if x < 0 {
		fraction = negate(fraction)
	}

	lx := lMult(fraction, 32) // L_x = fraction<<6
	i := extractH(lx)         // Extract b10-b16 of fraction
	lx = lShr(lx, 1)
	a := extractL(lx) // Extract b0-b9 of fraction
	a &= 0x7fff

	lx = lDepositH(pow2Table[i])             // table[i] << 16
	tmp := sub(pow2Table[i], pow2Table[i+1]) // table[i] - table[i+1]
	lx = lMsu(lx, tmp, a)                    // L_x -= tmp*a*2

	if x < 0 {
		lx = lDepositH(divS(0x4000, extractH(lx))) // calculate 1/fraction
		exponent = sub(exponent, 1)
	}

	exp := sub(12, exponent)
	lx = lShrR(lx, exp)

	return extractH(lx)
}

// lMpyLs multiplies a 32-bit number by a 16-bit number, truncating the
// 32-bit one to 31 bits first (L_mpy_ls).
func lMpyLs(l2 int32, v1 int16) int32 {
	sw := shr(extractL(l2), 1)
	sw &= 32767

	out := lMult(v1, sw)
	out = lShr(out, 15)
	out = lMac(out, v1, extractH(l2))
	return out
}

// cosFxp computes cos(x*PI), x and result in Q1.15 (cos_fxp).
func cosFxp(x int16) int16 {
	var tx int16
	sign := false
	if x < 0 {
		tx = negate(x)
	} else {
		tx = x
	}

	// if angle > pi/2, cos(angle) = -cos(pi-angle)
	if tx > x05Q15 {
		tx = sub(oneQ15, tx)
		sign = true
	}
	// convert input to be within range 0-128
	index1 := shr(tx, 7)
	index2 := add(index1, 1)

	if index1 == 128 {
		return 0
	}

	m := sub(tx, shl(index1, 7))
	// convert decimal part to Q15
	m = shl(m, 8)

	temp := sub(cosTable[index2], cosTable[index1])
	temp = mult(m, temp)
	ty := add(cosTable[index1], temp)

	if sign {
		return negate(ty)
	}
	return ty
}

// sinFxp computes sin(x*PI), x and result in Q1.15 (sin_fxp).
func sinFxp(x int16) int16 {
	var tx int16
	sign := false
	if x < 0 {
		tx = negate(x)
		sign = true
	} else {
		tx = x
	}

	if tx > x05Q15 {
		tx = sub(tx, x05Q15)
	} else {
		tx = sub(x05Q15, tx)
	}

	ty := cosFxp(tx)

	if sign {
		return negate(ty)
	}
	return ty
}

// sqrtLExp computes sqrt(lx) for positive lx (Q1.31). It returns the
// result in Q1.31 and the right shift that must still be applied to it
// (sqrt_l_exp).
func sqrtLExp(lx int32) (int32, int16) {
	if lx <= 0 {
		return 0, 0
	}

	e := normL(lx) & -2 // get next lower EVEN norm. exp (& 0xFFFE)
	lx = lShl(lx, e)    // L_x is normalized to [0.25..1)
	exp := e >> 1       // return 2*exponent (or Q1)

	lx = lShr(lx, 9)
	i := extractH(lx) // Extract b25-b31, 16 <= i <= 63 because of normalization
	lx = lShr(lx, 1)
	a := extractL(lx) // Extract b10-b24
	a &= 0x7fff

	i = sub(i, 16) // 0 <= i <= 47

	ly := lDepositH(sqrtTable[i])            // table[i] << 16
	tmp := sub(sqrtTable[i], sqrtTable[i+1]) // table[i] - table[i+1])
	ly = lMsu(ly, tmp, a)                    // L_y -= tmp*a*2

	return ly, exp
}

// log2 computes log2(x); x in Q14.2, result in Q10.22 (Log2).
func log2(x int16) int32 {
	if x <= 0 {
		return 0
	}

	exp := normS(x)
	x = shl(x, exp)

	i := shr(x, 9)       // Extract b15-b9
	a := shl(x&0x1FF, 6) // Extract b8-b0
	i = sub(i, 32)

	ly := lDepositH(logTable[i])           // table[i] << 16
	tmp := sub(logTable[i], logTable[i+1]) // table[i] - table[i+1]
	ly = lMsu(ly, tmp, a)                  // L_y -= tmp*a*2

	ly = lShr(ly, 9)

	exp = sub(12, exp)
	ly = lAdd(ly, lDepositH(shl(exp, 6)))

	return ly
}
