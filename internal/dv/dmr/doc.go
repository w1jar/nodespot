// Package dmr implements the DMR air-interface framing (ETSI TS 102 361-1/-2)
// shared by the BrandMeister (Rewind) and Homebrew (TGIF, DMR - Other)
// networks: 33-byte bursts, AMBE+2 FEC between AMBE 49-bit frames and the
// 72-bit DMR form, SYNC patterns, EMB (QR 16,7,6), embedded LC, slot type
// (Golay 20,8), full LC with BPTC(196,96) and RS(12,9), a superframe builder
// for transmit (TX), a parser for receive (RX), and the Homebrew DMRD packet.
//
// The package has no options, no state shared between calls, and does no I/O.
//
// # Voice frames
//
// AMBE frames (type AMBE) carry 49 bits MSB first: bytes 0-5 hold bits
// 0..47 and bit 48 is the MSB of byte 6. The bit order is mbelib's ambe_d[]:
// 12 bits of C0 data, 12 bits of C1 data, 11 bits of C2, 14 bits of C3. The
// 72-bit form (AMBE72) applies Golay(24,12) to C0, Golay(23,12) and the
// C0-seeded PRNG to C1, and the DMR interleave. SilenceAMBE (f801a99f8ce080)
// is the 49-bit decode of the air-interface silence frame B9E881526173002A6B.
//
// # Transmit
//
//	tx := dmr.NewTX(dmr.TXConfig{Src: 1234567, Dst: 9990, Private: true, CC: 1})
//	frames := tx.Start()           // 3 voice LC headers
//	frames = tx.Write(ambe)        // a voice burst every 3rd frame (A..F)
//	frames = tx.End()              // padded last burst + terminator with LC
//
// Each TXFrame carries the FrameType and voice sequence / data type that
// the DMRD flags byte (or the Rewind equivalent) needs.
//
// # Receive
//
// RX.Push takes each burst with the frame type the network signalled and
// returns the LC (from a header, terminator, or the embedded LC for late
// entry), the three AMBE frames of a voice burst with the corrected bit
// count, and End on a terminator. Lost-end timeouts are up to the network.
//
// References: ETSI TS 102 361-1 V2.5.1 (sections 4.2, 9.1, annex B) and
// TS 102 361-2 (7.1.1); MMDVMHost (G4KLX, GPL-2.0-or-later) for the bit
// layouts, cross-checked by the vectors in testdata (see testdata/README.md);
// mbelib and DSD (ISC) for the AMBE+2 bit order and interleave.
package dmr
