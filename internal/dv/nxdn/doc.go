// Package nxdn implements the NXDN air-interface layers needed to carry voice
// between a node and NXDN reflectors: LICH, SACCH, FACCH1, the rate 1/2
// constraint length 5 convolutional code with puncturing, CRC-6/12/15, the
// frame scrambler and the Layer 3 messages VCALL and TX_REL, plus the 33-byte
// payload that MMDVMHost, NXDNGateway and NXDN reflectors exchange (payload.go).
//
// # What travels on the network
//
// An NXDN reflector datagram "NXDND" is 43 bytes:
//
//	0..4   "NXDND"
//	5..6   source unit ID (big endian)
//	7..8   destination group/unit ID (big endian)
//	9      flags: 0x01 group call, 0x02 data, 0x04 start (VCALL), 0x08 end (TX_REL)
//	10..42 33-byte payload
//
// The payload is NOT an RF frame. It is the decoded content of one 80 ms NXDN
// 4800 bit/s frame (384 bits, 12.5 frames/s), laid out by MMDVMHost
// (NXDNControl.cpp, the netData buffer):
//
//	0       LICH byte, 8 bits with parity in bit 0 (lich.go)
//	1..4    SACCH "raw": 2 bit structure, 6 bit RAN, 18 data bits, CRC-6 (32 bits)
//	5..18   first half: FACCH1 raw (10 data bytes + CRC-12 = 12 bytes, 2 pad)
//	        or two AMBE+2 frames (98 bits, 14 bytes)
//	19..32  second half: same choice
//
// Which halves hold FACCH1 and which hold voice is given by the LICH option
// (steal) field: 3 = both voice, 1 = first FACCH1, 2 = second FACCH1,
// 0 = both FACCH1. There is no convolutional coding, interleaving or
// scrambling in the payload: MMDVMHost strips all of that before it hands the
// frame to the gateway and re-applies it for RF. This package implements those
// RF-side steps (sacch.go, facch1.go, conv.go, scramble.go) for completeness
// and so the framing is verified byte-exactly, but the network client does not
// need them.
//
// # AMBE+2 inside NXDN
//
// An NXDN 4800 bit/s (EHR) voice frame carries four AMBE+2 half-rate frames of
// 72 bits each on the air (20 ms each; 49 information bits plus Golay(24,12)
// on the first 12-bit group, Golay(23,12) on the second protected group which
// is whitened by a PRNG seeded from the first, and 25 unprotected bits).
// MMDVMHost's NXDNAudio::decode removes the FEC and PRNG and packs two frames
// back to back into 14 bytes: bits 0..48 frame A, bits 49..97 frame B, bits
// 98..111 zero, each frame ordered a(12) b(12) c(25), MSB first. That is the
// same 49-bit frame as the software AMBE vocoder (network.VoiceFrame.Data: 7 bytes,
// bytes 0..5 MSB first and bit 49 in the MSB of byte 6), so a network
// payload's four frames are copied bit for bit with no transcoding
// (PackFrames/UnpackFrames). The air-side 72-bit FEC (Golay tables and the
// 4096-entry PRNG table of NXDNAudio.cpp) is deliberately not implemented.
//
// Credit: tables and algorithms follow MMDVMHost NXDNLICH.cpp, NXDNSACCH.cpp,
// NXDNFACCH1.cpp, NXDNConvolution.cpp, NXDNCRC.cpp, NXDNLayer3.cpp and
// NXDNControl.cpp (Copyright Jonathan Naylor G4KLX, GPL-2.0-or-later).
package nxdn
