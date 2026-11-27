package bm

import (
	"encoding/binary"
	"errors"

	"github.com/w1jar/nodespot/internal/dv/dmr"
)

// Rewind wire format, as used by Open DMR Terminal (service 0x21). Sources:
// BrandMeister wiki "Simple External Application" and the protocol header
// Rewind.h of BrandMeister/DigestPlay (MIT), cross-checked with
// BrandMeister/go-brandmeister (MIT, used as reference only); the Open DMR
// Terminal differences were confirmed from the published ODMRTP protocol
// description and implemented independently. Rechecked in 1.0.0 against
// https://github.com/BrandMeister/DigestPlay (Rewind.h, RewindClient.[ch]),
// https://github.com/BrandMeister/go-brandmeister (rewind/const.go) and two
// Open DMR Terminal clients on GitHub: a DroidStar fork ("Droidstar-DMR",
// dmr.cpp) and pySPOT ("pyspot_rx"). All integers are little-endian.
//
//	0   8  signature "REWIND01"
//	8   2  type
//	10  2  flags
//	12  4  sequence number
//	16  2  payload length
//	18  .. payload
const (
	sign      = "REWIND01"
	hdrLen    = 18
	callLen   = 10
	superLen  = 12 + 2*callLen // RewindSuperHeader (RX only): type, srcID, dstID, srcCall, dstCall
	lcLen     = 12             // DMR voice header Full LC incl. CRC
	frameLen  = 7              // linear AMBE frame (49 bits, MSB first)
	frameLen3 = 3 * frameLen   // linear audio packet (RX only, see typAudio handling)
	frame72   = 9              // FEC coded AMBE+2 frame (72 bits, "mode 33")
	audioLen  = 3 * frame72    // DMR_AUDIO_FRAME: 3 x 72-bit frames = 60 ms
)

// Packet types.
const (
	typKeepAlive     uint16 = 0x0000
	typClose         uint16 = 0x0001
	typChallenge     uint16 = 0x0002
	typAuth          uint16 = 0x0003
	typReport        uint16 = 0x0100
	typBusyNotice    uint16 = 0x0200
	typConfiguration uint16 = 0x0900 // never sent (options 0 = none, as DigestPlay does)
	typSubscription  uint16 = 0x0901
	typCancelling    uint16 = 0x0902
	typSessionPoll   uint16 = 0x0903
	typDMRDataBase   uint16 = 0x0910
	typVoiceHeader   uint16 = typDMRDataBase + 1
	typTerminator    uint16 = typDMRDataBase + 2
	typAudio         uint16 = 0x0920
	typEmbedded      uint16 = 0x0927
	typSuperHeader   uint16 = 0x0928
	typFailureCode   uint16 = 0x0929
	flagRealTime1    uint16 = 1
	serviceOpenTerm  byte   = 0x21 // OPEN_TERMINAL (0x20 is the Simple External Application)
	// Session types (SESSION_TYPE_* in DigestPlay RewindClient.h,
	// rewind.PrivateVoice/GroupVoice in go-brandmeister): the type of a
	// SUBSCRIPTION and of a super header.
	sessionPrivate    uint32 = 5 // private voice; subscribed with our own DMR ID
	sessionGroup      uint32 = 7 // group voice; subscribed with the talkgroup
	maxPacket                = 512
	defaultPort              = 54006
	maxDescriptionLen        = 96
)

type packet struct {
	typ     uint16
	flags   uint16
	seq     uint32
	payload []byte
}

var errShort = errors.New("bm: short or foreign packet")

func marshal(typ, flags uint16, seq uint32, payload []byte) []byte {
	b := make([]byte, hdrLen+len(payload))
	copy(b, sign)
	binary.LittleEndian.PutUint16(b[8:], typ)
	binary.LittleEndian.PutUint16(b[10:], flags)
	binary.LittleEndian.PutUint32(b[12:], seq)
	binary.LittleEndian.PutUint16(b[16:], uint16(len(payload)))
	copy(b[hdrLen:], payload)
	return b
}

func unmarshal(b []byte) (packet, error) {
	if len(b) < hdrLen || string(b[:8]) != sign {
		return packet{}, errShort
	}
	n := int(binary.LittleEndian.Uint16(b[16:]))
	if hdrLen+n > len(b) {
		return packet{}, errShort
	}
	return packet{
		typ:     binary.LittleEndian.Uint16(b[8:]),
		flags:   binary.LittleEndian.Uint16(b[10:]),
		seq:     binary.LittleEndian.Uint32(b[12:]),
		payload: append([]byte(nil), b[hdrLen:hdrLen+n]...),
	}, nil
}

// versionData is the keep-alive payload: the operator's DMR ID, service,
// description text.
func versionData(id uint32, desc string) []byte {
	if len(desc) > maxDescriptionLen {
		desc = desc[:maxDescriptionLen]
	}
	b := make([]byte, 5+len(desc))
	binary.LittleEndian.PutUint32(b, id)
	b[4] = serviceOpenTerm
	copy(b[5:], desc)
	return b
}

func u32s(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}

type superHeader struct {
	typ      uint32
	src, dst uint32
	srcCall  string
	dstCall  string
}

func parseSuper(b []byte) (superHeader, bool) {
	if len(b) < superLen {
		return superHeader{}, false
	}
	return superHeader{
		typ:     binary.LittleEndian.Uint32(b[0:]),
		src:     binary.LittleEndian.Uint32(b[4:]),
		dst:     binary.LittleEndian.Uint32(b[8:]),
		srcCall: cstr(b[12 : 12+callLen]),
		dstCall: cstr(b[12+callLen : superLen]),
	}, true
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	return string(b)
}

// voiceHeaderLC builds the 12 byte Full LC of a voice header (ETSI TS 102
// 361-1): FLCO (0x00 group, 0x03 unit to unit), FID, service options,
// dst(3), src(3) and the RS(12,9) parity masked with the voice LC header
// CRC mask 0x96 (TS 102 361-1 B.3.12). Earlier versions left the parity
// zero; DroidStar's Open DMR Terminal client (dmr.cpp, send_frame) computes
// it, so it is computed here too, the same way as for the Homebrew bursts
// (dmr.RS129). Whether the master checks it is not confirmed.
func voiceHeaderLC(private bool, dst, src uint32) []byte {
	var raw [9]byte
	if private {
		raw[0] = 0x03
	}
	raw[3], raw[4], raw[5] = byte(dst>>16), byte(dst>>8), byte(dst)
	raw[6], raw[7], raw[8] = byte(src>>16), byte(src>>8), byte(src)
	p := dmr.RS129(raw)
	b := make([]byte, lcLen)
	copy(b, raw[:])
	for i := range p {
		b[9+i] = p[i] ^ dmr.MaskVoiceLCHeader
	}
	return b
}

func parseLC(b []byte) (private bool, dst, src uint32, ok bool) {
	if len(b) < 9 {
		return
	}
	private = b[0]&0x3f == 0x03
	dst = uint32(b[3])<<16 | uint32(b[4])<<8 | uint32(b[5])
	src = uint32(b[6])<<16 | uint32(b[7])<<8 | uint32(b[8])
	return private, dst, src, true
}
