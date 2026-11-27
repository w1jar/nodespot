package nxdn

import "errors"

// PayloadLen is the size of the NXDN payload in an NXDND reflector datagram.
const PayloadLen = 33

// Datagram flag bits (byte 9 of an NXDND datagram, NXDNGateway
// NXDNNetwork.cpp writeData).
const (
	FlagGroup = 0x01
	FlagData  = 0x02
	FlagStart = 0x04
	FlagEnd   = 0x08
)

// Payload offsets.
const (
	offLICH  = 0
	offSACCH = 1
	offHalf1 = 5
	offHalf2 = 19
	halfLen  = 14
)

// Kind classifies a received payload.
type Kind int

const (
	KindUnknown Kind = iota
	KindVCall        // Layer 3 VCALL in FACCH1 (call header)
	KindTxRel        // Layer 3 TX_REL (call end)
	KindVoice        // voice frames (late entry capable)
	KindData         // data call (ignored by the voice client)
)

// Parsed is a decoded payload.
type Parsed struct {
	Kind    Kind
	LICH    LICH
	SACCH   SACCH
	SACCHOK bool
	// L3 is the Layer 3 message of a header/trailer or of a FACCH1 stolen
	// from a voice frame (valid when HasL3).
	L3    Layer3
	HasL3 bool
	// Frames are the AMBE+2 frames (7 bytes each, vocoder layout): 4, 2 or 0.
	Frames [][7]byte
}

var errPayload = errors.New("nxdn: bad payload")

// ParsePayload decodes a 33-byte payload.
func ParsePayload(p []byte) (Parsed, error) {
	var r Parsed
	if len(p) != PayloadLen {
		return r, errPayload
	}
	r.LICH = LICH(p[offLICH])
	r.SACCH = SACCHFromRaw(p[offSACCH : offSACCH+4])
	r.SACCHOK = SACCHCRCOK(p[offSACCH : offSACCH+4])
	if r.LICH.RFCT() != RFCTRDCH && r.LICH.RFCT() != RFCTRTCHC && r.LICH.RFCT() != RFCTRTCH {
		return r, errPayload
	}
	halves := [2][]byte{p[offHalf1 : offHalf1+halfLen], p[offHalf2 : offHalf2+halfLen]}
	switch r.LICH.USC() {
	case USCUDCH:
		r.Kind = KindData
		return r, nil
	case USCSACCHNS, USCSACCHIdle:
		// header/trailer: FACCH1 in both halves (identical content)
		for _, h := range halves {
			if FACCH1CRCOK(h) {
				copy(r.L3[:], h[:10])
				r.HasL3 = true
				break
			}
		}
		if !r.HasL3 {
			return r, errPayload
		}
		switch r.L3.Type() {
		case MsgVCall:
			r.Kind = KindVCall
		case MsgTxRel:
			r.Kind = KindTxRel
		case MsgDCallH, MsgDCallD:
			r.Kind = KindData
		}
		return r, nil
	}
	// voice superframe frame: halves are voice or FACCH1 according to option
	r.Kind = KindVoice
	var isVoice [2]bool
	switch r.LICH.Option() {
	case StealNone:
		isVoice = [2]bool{true, true}
	case StealFACCH1First:
		isVoice = [2]bool{false, true}
	case StealFACCH1Second:
		isVoice = [2]bool{true, false}
	}
	for i, h := range halves {
		if isVoice[i] {
			a, b := UnpackFrames(h)
			r.Frames = append(r.Frames, a, b)
		} else if !r.HasL3 && FACCH1CRCOK(h) {
			copy(r.L3[:], h[:10])
			r.HasL3 = true
		}
	}
	if r.HasL3 && r.L3.Type() == MsgTxRel {
		r.Kind = KindTxRel
	}
	return r, nil
}

// UnpackFrames splits 14 payload bytes into two 49-bit AMBE+2 frames.
func UnpackFrames(h []byte) (a, b [7]byte) {
	for i := 0; i < 49; i++ {
		setBit(a[:], i, getBit(h, i))
		setBit(b[:], i, getBit(h, 49+i))
	}
	return a, b
}

// PackFrames packs two 49-bit AMBE+2 frames into 14 bytes (trailing 14 bits
// zero).
func PackFrames(a, b [7]byte) (h [14]byte) {
	for i := 0; i < 49; i++ {
		setBit(h[:], i, getBit(a[:], i))
		setBit(h[:], 49+i, getBit(b[:], i))
	}
	return h
}

// BuildHeader returns the payload of a call header (VCALL) or trailer
// (TX_REL): LICH 0x81, idle SACCH, the FACCH1 twice. It matches what
// MMDVMHost sends to NXDNGateway.
func BuildHeader(msg Layer3, ran int) (p [PayloadLen]byte) {
	p[offLICH] = byte(LICHHeader)
	s := NewSACCH(SRSingle, ran, SACCHIdle).Raw()
	copy(p[offSACCH:], s[:])
	f := FACCH1Raw(msg)
	copy(p[offHalf1:], f[:])
	copy(p[offHalf2:], f[:])
	return p
}

// BuildVoice returns the payload of a voice frame carrying four AMBE+2
// frames. pos is the frame's place (0..3) in the 4-frame SACCH superframe;
// the SACCH carries part pos of call (structure 3-pos).
func BuildVoice(call Layer3, ran, pos int, f [4][7]byte) (p [PayloadLen]byte) {
	p[offLICH] = byte(LICHVoice)
	s := NewSACCH(SR14-(pos&3), ran, call.SACCHPart(pos&3)).Raw()
	copy(p[offSACCH:], s[:])
	h1 := PackFrames(f[0], f[1])
	h2 := PackFrames(f[2], f[3])
	copy(p[offHalf1:], h1[:])
	copy(p[offHalf2:], h2[:])
	return p
}
