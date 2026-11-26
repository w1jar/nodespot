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

// ETSI-style fixed-point basic operators, ported from basicop2.cc
// (originally from the ETSI GSM AMR reference code, as bundled with
// imbe_vocoder).
//
// Overflow/Carry: the reference keeps global Overflow and Carry flags that
// are set by the saturating operators. Nothing in imbe_vocoder ever reads
// them (and the carry-using operators L_add_c, L_sub_c, L_macNs, L_msuNs and
// L_sat are never called), so the flags have no observable effect and are
// not modelled here. Every operator below returns exactly the value the
// reference returns.
//
// All 32-bit arithmetic that the reference performs with plain C int
// operators relies on two's-complement wrap-around; Go's int32 arithmetic
// wraps identically.

const (
	max32 int32 = 0x7fffffff  // MAX_32
	min32 int32 = -0x80000000 // MIN_32
	max16 int16 = 0x7fff      // MAX_16
	min16 int16 = -0x8000     // MIN_16
)

// saturate limits a 32-bit value to the 16-bit range (saturate).
func saturate(v int32) int16 {
	if v > 0x7fff {
		return max16
	}
	if v < -0x8000 {
		return min16
	}
	return int16(v)
}

// add: short add with saturation (add).
func add(a, b int16) int16 { return saturate(int32(a) + int32(b)) }

// sub: short subtract with saturation (sub).
func sub(a, b int16) int16 { return saturate(int32(a) - int32(b)) }

// absS: short absolute value (abs_s).
func absS(a int16) int16 {
	if a == min16 {
		return max16
	}
	if a < 0 {
		return -a
	}
	return a
}

// shl: short arithmetic shift left with saturation (shl).
func shl(a, n int16) int16 {
	if n < 0 {
		if n < -16 {
			n = -16
		}
		return shr(a, -n)
	}
	result := int32(a) * (int32(1) << uint(n&31)) // C: (Word32)var1 * ((Word32)1 << var2)
	if n > 31 {
		// (Word32)1 << var2 is undefined in C for var2 >= 32; the
		// (var2 > 15 && var1 != 0) test below catches every such case
		// except var1 == 0, whose result is 0 either way.
		result = 0
	}
	if (n > 15 && a != 0) || result != int32(int16(result)) {
		if a > 0 {
			return max16
		}
		return min16
	}
	return int16(result)
}

// shr: short arithmetic shift right (shr).
func shr(a, n int16) int16 {
	if n < 0 {
		if n < -16 {
			n = -16
		}
		return shl(a, -n)
	}
	if n >= 15 {
		if a < 0 {
			return -1
		}
		return 0
	}
	if a < 0 {
		return ^((^a) >> uint(n))
	}
	return a >> uint(n)
}

// mult: Q15 multiply with truncation and saturation (mult).
func mult(a, b int16) int16 {
	p := int32(a) * int32(b)
	p = (p & -0x8000) >> 15 // (L_product & 0xffff8000) >> 15
	if p&0x00010000 != 0 {
		p |= -0x10000 // | 0xffff0000
	}
	return saturate(p)
}

// lMult: 32-bit result of a*b*2 with saturation (L_mult).
func lMult(a, b int16) int32 {
	p := int32(a) * int32(b)
	if p != 0x40000000 {
		return p * 2
	}
	return max32
}

// negate: short negate with saturation (negate).
func negate(a int16) int16 {
	if a == min16 {
		return max16
	}
	return -a
}

// extractH returns the upper 16 bits (extract_h).
func extractH(v int32) int16 { return int16(v >> 16) }

// extractL returns the lower 16 bits (extract_l).
func extractL(v int32) int16 { return int16(v) }

// round16 rounds to the upper 16 bits (round).
func round16(v int32) int16 { return extractH(lAdd(v, 0x00008000)) }

// lMac: multiply-accumulate with saturation (L_mac).
func lMac(acc int32, a, b int16) int32 { return lAdd(acc, lMult(a, b)) }

// lMsu: multiply-subtract with saturation (L_msu).
func lMsu(acc int32, a, b int16) int32 { return lSub(acc, lMult(a, b)) }

// lAdd: 32-bit add with saturation (L_add).
func lAdd(a, b int32) int32 {
	r := a + b
	if (a^b)&min32 == 0 && (r^a)&min32 != 0 {
		if a < 0 {
			return min32
		}
		return max32
	}
	return r
}

// lSub: 32-bit subtract with saturation (L_sub).
func lSub(a, b int32) int32 {
	r := a - b
	if (a^b)&min32 != 0 && (r^a)&min32 != 0 {
		if a < 0 {
			return min32
		}
		return max32
	}
	return r
}

// lNegate: 32-bit negate with saturation (L_negate).
func lNegate(a int32) int32 {
	if a == min32 {
		return max32
	}
	return -a
}

// multR: Q15 multiply with rounding and saturation (mult_r).
func multR(a, b int16) int16 {
	p := int32(a) * int32(b)
	p += 0x00004000
	p &= -0x8000
	p >>= 15
	if p&0x00010000 != 0 {
		p |= -0x10000
	}
	return saturate(p)
}

// lShl: 32-bit shift left with saturation (L_shl).
func lShl(a int32, n int16) int32 {
	if n <= 0 {
		if n < -32 {
			n = -32
		}
		return lShr(a, -n)
	}
	var out int32
	for ; n > 0; n-- {
		if a > 0x3fffffff {
			return max32
		}
		if a < -0x40000000 {
			return min32
		}
		a *= 2
		out = a
	}
	return out
}

// lShr: 32-bit arithmetic shift right (L_shr).
func lShr(a int32, n int16) int32 {
	if n < 0 {
		if n < -32 {
			n = -32
		}
		return lShl(a, -n)
	}
	if n >= 31 {
		if a < 0 {
			return -1
		}
		return 0
	}
	if a < 0 {
		return ^((^a) >> uint(n))
	}
	return a >> uint(n)
}

// shrR: short shift right with rounding (shr_r).
func shrR(a, n int16) int16 {
	if n > 15 {
		return 0
	}
	out := shr(a, n)
	if n > 0 && a&(int16(1)<<uint(n-1)) != 0 {
		out++
	}
	return out
}

// macR: multiply-accumulate with rounding (mac_r).
func macR(acc int32, a, b int16) int16 {
	return extractH(lAdd(lMac(acc, a, b), 0x00008000))
}

// msuR: multiply-subtract with rounding (msu_r).
func msuR(acc int32, a, b int16) int16 {
	return extractH(lAdd(lMsu(acc, a, b), 0x00008000))
}

// lDepositH puts a in the upper 16 bits (L_deposit_h).
func lDepositH(a int16) int32 { return int32(a) << 16 }

// lDepositL sign-extends a to 32 bits (L_deposit_l).
func lDepositL(a int16) int32 { return int32(a) }

// lShrR: 32-bit shift right with rounding (L_shr_r).
func lShrR(a int32, n int16) int32 {
	if n > 31 {
		return 0
	}
	out := lShr(a, n)
	if n > 0 && a&(int32(1)<<uint(n-1)) != 0 {
		out++
	}
	return out
}

// lAbs: 32-bit absolute value with saturation (L_abs).
func lAbs(a int32) int32 {
	if a == min32 {
		return max32
	}
	if a < 0 {
		return -a
	}
	return a
}

// normS returns the left shift needed to normalise a 16-bit value (norm_s).
func normS(a int16) int16 {
	if a == 0 {
		return 0
	}
	if a == -1 {
		return 15
	}
	if a < 0 {
		a = ^a
	}
	var n int16
	for ; a < 0x4000; n++ {
		a <<= 1
	}
	return n
}

// normL returns the left shift needed to normalise a 32-bit value (norm_l).
func normL(a int32) int16 {
	if a == 0 {
		return 0
	}
	if a == -1 {
		return 31
	}
	if a < 0 {
		a = ^a
	}
	var n int16
	for ; a < 0x40000000; n++ {
		a <<= 1
	}
	return n
}

// divS: fractional integer division a/b, 0 <= a <= b, b > 0 (div_s).
//
// The reference prints an error and calls abort() when the preconditions
// are violated. A library must not bring down its host process, so this
// port instead returns 0 for a negative operand or a zero/negative divisor
// and MAX_16 when a > b. These inputs make the reference crash, so there is
// no reference output to diverge from; they were never reached in testing.
func divS(a, b int16) int16 {
	if a < 0 || b <= 0 {
		return 0
	}
	if a >= b {
		return max16
	}
	if a == 0 {
		return 0
	}
	var out int16
	num, den := int32(a), int32(b)
	for i := 0; i < 15; i++ {
		out <<= 1
		num <<= 1
		if num >= den {
			num = lSub(num, den)
			out = add(out, 1)
		}
	}
	return out
}
