// SPDX-License-Identifier: GPL-3.0-or-later

// Package codec2 is a pure-Go port of the Codec2 3200 bit/s vocoder, derived
// from Codec2-mod (https://github.com/M17-Project/Codec2-mod, commit
// 7c334283794fa6304dc014f12df70afc8393143a), the 3200-only extraction of
// Codec2 used by M17. The bit packing is that of libcodec2's
// CODEC2_MODE_3200.
//
// # Licence
//
// Codec2 is the work of David Rowe and the other Codec2 contributors and is
// distributed under the GNU Lesser General Public License, version 2.1.
// Codec2-mod carries the same licence (its LICENSE file is the LGPL-2.1
// text). Section 3 of the LGPL-2.1 lets a recipient apply the terms of the
// ordinary GNU General Public License instead, to a given copy of the
// library, by altering the notices that refer to the LGPL so that they refer
// to the GPL, and expressly allows naming a version of the GPL newer than 2.
// This package is such a copy: it is distributed here under the GNU General
// Public License version 3 or later (see LICENSE), the licence of nodespot as a
// whole.
//
// Every ported file carries the SPDX identifier GPL-3.0-or-later, the
// original copyright holders, and a line naming the Codec2-mod file it is
// derived from and the commit, with the words "relicensed under LGPL-2.1
// section 3 to GPL-3.0-or-later". The original C function names are given
// in the doc comments.
//
// fft.go ports KISS FFT (Copyright (c) 2003-2010 Mark Borgerding, BSD-3-Clause,
// https://github.com/mborgerding/kissfft) as bundled in Codec2-mod; its notice
// is reproduced in packaging/THIRD-PARTY-NOTICES. BSD-3-Clause is compatible
// with GPL-3.0-or-later.
//
// Codec2-mod's source files carry no per-file copyright header; its
// codec2_internal.h states "This work uses code written by David Rowe et
// al.". Credit for the algorithms goes to them and to the Codec2-mod
// authors.
//
// # Usage
//
// Encoder and Decoder are independent and stateful (one per stream); they
// are not safe for concurrent use, and the package has no mutable globals.
// Encode takes 160 samples (20 ms) of 8 kHz, 16-bit linear PCM and produces
// 8 bytes; Decode does the reverse. Neither allocates.
//
// # Frame format
//
// One 64-bit frame, packed MSB first, with every field wider than one bit
// Gray coded as in libcodec2:
//
//	voiced flag of the first 10 ms half   1 bit
//	voiced flag of the second 10 ms half  1 bit
//	pitch Wo index                        7 bits
//	energy index                          5 bits
//	LSP differences 1..10                 5 bits each
//
// The first half-frame's pitch, energy and LSPs are interpolated in the
// decoder.
//
// # Conformance
//
//   - Encode is bit-exact with the C reference on every frame of the test
//     signal (512 frames), on amd64, arm64 and armhf.
//   - The encoder goldens come from Codec2-mod's C sources. The decoder
//     follows upstream libcodec2 1.2 (cosf in lsp_to_lpc, powf LPC
//     post-filter with bass boost, libcodec2's PI and float Wo limits),
//     not Codec2-mod's approximations; its goldens are regression pins of
//     this port. Agreement with libcodec2 is measured by the manual
//     workflow .github/workflows/codec2-check.yml.
//
// # Floating point
//
// The reference is float code. The port reproduces its arithmetic exactly:
// float expressions are float32, expressions that C promotes to double (any
// use of M_PI, an unsuffixed literal) are float64 with the same conversions,
// and every product is wrapped in an explicit float32() or float64()
// conversion so that the Go compiler may not fuse it with an addition into a
// fused multiply-add, which it is otherwise allowed to do on arm64. Constants
// that C evaluates in double are package-level variables (Go folds constant
// expressions exactly). The C reference was compiled with
// "-ffp-contract=off -O2" (gcc 13.3, x86-64, glibc 2.39), so that it does not
// fuse either.
//
// The reference calls libm. The port does not depend on the host's libm for
// anything that can be tabulated: the KISS FFT twiddle factors, the analysis
// window and the pitch estimator's window are tables (libmtables.go) taken
// from the reference's glibc build. This matters: glibc's cosf and sinf are
// not correctly rounded, and 22 of the twiddle factors and 1 pitch window
// entry differ in the last bit from float32(math.Cos(x)); using the
// correctly rounded values made 54 of 81920 decoded speech samples (1403 of
// 48000 for random frames, up to 5 LSB) differ from the reference.
//
// Four libm calls remain at run time, on data dependent arguments: sinf and
// cosf of the harmonic phases, powf(10, x) for the background noise
// threshold and log10f for the energy terms. They are evaluated in double
// precision and rounded once to float32, which is correctly rounded.
// glibc's versions differ from that on rare arguments, which is the entire
// difference of the decoder from the reference (a build of the reference
// with the same double-precision functions is reproduced bit-exactly by this
// package). The encoder's energy index uses log10f; the encoder was
// bit-exact on all test frames, but in principle a frame on which glibc's
// log10f rounds differently could, with the quantiser, give a different
// energy index. Go's math package is pure Go on these targets, but on arm64
// it too may fuse operations internally; arm64 and armhf (under qemu-user)
// gave the same results as amd64.
//
// Other deviations: the reference's unused radix-2/3/5 FFT butterflies and
// the SIMD and fixed-point variants are not ported (a 512-point real FFT is
// four radix-4 stages only), and the reference's habit of reusing one
// scratch buffer for several arrays is replaced by separate arrays, which
// does not change results (checked by comparing intermediate values with the
// reference).
package codec2
