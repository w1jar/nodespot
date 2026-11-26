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

// Encoder and Decoder, ported from imbe_vocoder.cc/.h, encode.cc and
// decode.cc. The C++ class imbe_vocoder holds both encoder and decoder
// state; here they are split into two independent types.

// Encoder is a stateful IMBE encoder (the encoder half of imbe_vocoder).
// It is not safe for concurrent use.
type Encoder struct {
	param imbeParam // my_imbe_param, as used by encode()

	prevPitch, prevPrevPitch, prevEP, prevPrevEP int16

	numHarmsPrev2 int16
	saPrev2       [numHarmsMax + 2]int32
	thMax         int32
	vUvDsn        [numBandsMax]int16
	pitchEstBuf   [pitchEstBufSize]int16
	pitchRefBuf   [pitchEstBufSize]int16
	dcRmvMem      int32
	fftBuf        [2 * fftLength]int16 // Cmplx16 fft_buf[FFTLENGTH], interleaved
	peLpfMem      [peLpfOrd]int16
}

// NewEncoder returns an Encoder in the reference's initial state
// (imbe_vocoder::imbe_vocoder, decode_init + encode_init).
func NewEncoder() *Encoder {
	e := &Encoder{}
	paramInit(&e.param)
	e.encodeInit()
	return e
}

// paramInit sets up IMBE_PARAM as decode_init does ("make previous frame
// for the first frame").
func paramInit(p *imbeParam) {
	*p = imbeParam{}
	p.fundFreq = 0x0cf6474a
	p.numHarms = 9
	p.numBands = 3
}

// encodeInit is imbe_vocoder::encode_init.
func (e *Encoder) encodeInit() {
	vZap(e.pitchEstBuf[:])
	vZap(e.pitchRefBuf[:])
	vZap(e.peLpfMem[:])
	e.pitchEstInit()
	e.dcRmvMem = 0
	e.saEncodeInit()
	e.pitchRefInit()
}

// Encode compresses one 20 ms frame of 160 16-bit linear PCM samples at
// 8 kHz into an 88-bit IMBE frame (imbe_vocoder::encode followed by
// packing, see PackVectors).
func (e *Encoder) Encode(pcm *[160]int16) [11]byte {
	var fv [8]int16
	e.EncodeVectors(pcm, &fv)
	return PackVectors(VectorsFromInt16(&fv))
}

// EncodeVectors is Encode returning the reference's frame_vector (u0..u7)
// instead of packed bytes.
func (e *Encoder) EncodeVectors(pcm *[160]int16, fv *[8]int16) {
	p := &e.param

	copy(e.pitchEstBuf[:pitchEstBufSize-frameLen], e.pitchEstBuf[frameLen:])
	copy(e.pitchRefBuf[:pitchEstBufSize-frameLen], e.pitchRefBuf[frameLen:])

	dcRmv(pcm[:], e.pitchRefBuf[pitchEstBufSize-frameLen:], &e.dcRmvMem, frameLen)
	peLpf(e.pitchRefBuf[pitchEstBufSize-frameLen:], e.pitchEstBuf[pitchEstBufSize-frameLen:], &e.peLpfMem, frameLen)

	e.pitchEst(p, e.pitchEstBuf[:])

	// Speech windowing and FFT calculation
	fb := &e.fftBuf
	sig := 40
	w := 0
	for i := 146; i < 256; i++ {
		fb[2*i] = mult(e.pitchRefBuf[sig], wr[w])
		fb[2*i+1] = 0
		sig++
		w++
	}
	fb[0] = e.pitchRefBuf[sig]
	fb[1] = 0
	sig++
	w--
	for i := 1; i < 111; i++ {
		fb[2*i] = mult(e.pitchRefBuf[sig], wr[w])
		fb[2*i+1] = 0
		sig++
		w--
	}
	for i := 111; i < 146; i++ {
		fb[2*i], fb[2*i+1] = 0, 0
	}

	fft(fb[:], fftLength, 1)

	pitchRef(p, fb[:])
	e.vUvDet(p, fb[:])
	e.saEncode(p)
	encodeFrameVector(p, fv)
}

// Decoder is a stateful IMBE decoder (the decoder half of imbe_vocoder).
// It is not safe for concurrent use.
type Decoder struct {
	param imbeParam // my_imbe_param, as used by decode()

	seed uint32 // rand_gen() state (a file-static global in the reference)

	numHarmsPrev1 int16
	saPrev1       [numHarmsMax + 2]int32
	uvMem         [105]int16
	phMem         [numHarmsMax]uint32
	numHarmsPrev3 int16
	fundFreqPrev  int32
	vuDsnPrev     [numHarmsMax]int16
	saPrev3       [numHarmsMax]int16

	uw [2 * fftLength]int16 // uv_synt scratch (a local in the reference)
}

// NewDecoder returns a Decoder in the reference's initial state
// (imbe_vocoder::imbe_vocoder, decode_init).
func NewDecoder() *Decoder {
	d := &Decoder{seed: 1}
	// decode_init: v_synt_init draws 56 values from rand_gen().
	d.vSyntInit()
	d.uvSyntInit()
	d.saDecodeInit()
	paramInit(&d.param)
	return d
}

// Decode synthesises 160 samples of 8 kHz PCM from one 88-bit IMBE frame
// (unpacking, see UnpackVectors, followed by imbe_vocoder::decode).
func (d *Decoder) Decode(frame *[11]byte) [160]int16 {
	u := UnpackVectors(frame)
	fv := u.Int16()
	return d.DecodeVectors(&fv)
}

// DecodeVectors is Decode taking the reference's frame_vector (u0..u7).
// Bits above each vector's width are ignored except as the reference
// itself would see them; callers should pass in-range values.
func (d *Decoder) DecodeVectors(fv *[8]int16) [160]int16 {
	var snd, sndTmp [frameLen]int16
	p := &d.param

	decodeFrameVector(p, fv)
	vUvDecode(p)
	d.saDecode(p)
	saEnh(p)
	d.vSynt(p, &snd)
	d.uvSynt(p, &sndTmp)

	for j := range snd {
		snd[j] = add(snd[j], sndTmp[j])
	}
	return snd
}
