package mmdvm

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Reason is why the modem refused a command (the second NAK byte).
type Reason byte

// NAK reasons.
const (
	ReasonInvalidCommand Reason = 1 // unknown command
	ReasonWrongState     Reason = 2 // not allowed in the modem's state or build
	ReasonBadData        Reason = 4 // bad length or a value out of range
	ReasonNoSpace        Reason = 5 // the transmit FIFO is full
	ReasonNoDuplex       Reason = 6 // duplex asked of a simplex-only build
)

func (r Reason) String() string {
	switch r {
	case ReasonInvalidCommand:
		return "unknown command"
	case ReasonWrongState:
		return "wrong state"
	case ReasonBadData:
		return "bad length or data"
	case ReasonNoSpace:
		return "no buffer space"
	case ReasonNoDuplex:
		return "duplex not supported by this firmware"
	}
	return fmt.Sprintf("reason %d", byte(r))
}

// NAKError is a refused command.
type NAKError struct {
	Cmd    byte
	Reason Reason
}

func (e *NAKError) Error() string {
	return fmt.Sprintf("mmdvm: %s refused: %s", CommandName(e.Cmd), e.Reason)
}

// ErrMalformed is returned for a reply too short for its command.
var ErrMalformed = errors.New("mmdvm: malformed reply")

// ParseAck returns the acknowledged command of an ACK frame, or a
// *NAKError for a NAK frame.
func ParseAck(f Frame) (byte, error) {
	switch f.Cmd {
	case CmdACK:
		if len(f.Payload) < 1 {
			return 0, ErrMalformed
		}
		return f.Payload[0], nil
	case CmdNAK:
		if len(f.Payload) < 2 {
			return 0, ErrMalformed
		}
		return f.Payload[0], &NAKError{Cmd: f.Payload[0], Reason: Reason(f.Payload[1])}
	}
	return 0, fmt.Errorf("mmdvm: %s is not an ACK or NAK", CommandName(f.Cmd))
}

// Ack is the modem's ACK of cmd.
func Ack(cmd byte) []byte { return encode(CmdACK, cmd) }

// Nak is the modem's NAK of cmd.
func Nak(cmd byte, r Reason) []byte { return encode(CmdNAK, cmd, byte(r)) }

// CPU types in a protocol 2 version reply.
const (
	CPUAtmel = 0
	CPUNXP   = 1
	CPUST    = 2
)

// UDIDLen is the unique ID field of a protocol 2 version reply.
const UDIDLen = 16

// Version is a GET_VERSION reply.
type Version struct {
	Protocol int
	// Description is the board and firmware text, such as
	// "MMDVM_HS_Dual_Hat-v1.6.0 20210919 14.7456MHz dual ADF7021 FW by CA6JAU GitID #abcdef".
	Description string
	// Capabilities (protocol 2): the mode bits (01 D-Star, 02 DMR, 04 YSF,
	// 08 P25, 10 NXDN, 40 FM) and the second byte (01 POCSAG).
	Cap1, Cap2 byte
	CPU        byte // protocol 2
	// UDID is the processor's unique ID: 16 bytes for protocol 2, or for
	// protocol 1 the hex text some builds put after a NUL.
	UDID []byte
}

// DMR reports whether the modem can do DMR. Protocol 1 modems do not say;
// they are taken to.
func (v Version) DMR() bool { return v.Protocol < 2 || v.Cap1&0x02 != 0 }

// CPUName names the CPU of a protocol 2 modem.
func (v Version) CPUName() string {
	if v.Protocol < 2 {
		return ""
	}
	switch v.CPU {
	case CPUAtmel:
		return "Atmel"
	case CPUNXP:
		return "NXP"
	case CPUST:
		return "ST"
	}
	return fmt.Sprintf("CPU %d", v.CPU)
}

// ParseVersion parses a GET_VERSION reply payload. Protocol versions other
// than 1 and 2 are an error.
func ParseVersion(p []byte) (Version, error) {
	if len(p) < 1 {
		return Version{}, ErrMalformed
	}
	v := Version{Protocol: int(p[0])}
	switch v.Protocol {
	case 1:
		desc := p[1:]
		if i := bytes.IndexByte(desc, 0); i >= 0 {
			v.UDID = append([]byte(nil), bytes.TrimRight(desc[i+1:], "\x00")...)
			desc = desc[:i]
		}
		v.Description = cleanText(desc)
	case 2:
		if len(p) < 4+UDIDLen {
			return Version{}, ErrMalformed
		}
		v.Cap1, v.Cap2, v.CPU = p[1], p[2], p[3]
		v.UDID = append([]byte(nil), p[4:4+UDIDLen]...)
		v.Description = cleanText(p[4+UDIDLen:])
	default:
		return v, fmt.Errorf("mmdvm: protocol version %d is not supported", v.Protocol)
	}
	return v, nil
}

func cleanText(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
}

// EncodeVersion is the modem's GET_VERSION reply (used by the fake modem).
func EncodeVersion(v Version) ([]byte, error) {
	p := []byte{byte(v.Protocol)}
	switch v.Protocol {
	case 1:
		p = append(p, v.Description...)
		if len(v.UDID) > 0 {
			p = append(p, 0)
			p = append(p, v.UDID...)
		}
	case 2:
		var id [UDIDLen]byte
		copy(id[:], v.UDID)
		p = append(p, v.Cap1, v.Cap2, v.CPU)
		p = append(p, id[:]...)
		p = append(p, v.Description...)
	default:
		return nil, fmt.Errorf("mmdvm: protocol version %d is not supported", v.Protocol)
	}
	return Encode(CmdGetVersion, p)
}

// Status flag bits.
const (
	FlagTX          = 0x01
	FlagADCOverflow = 0x02 // protocol 2
	FlagRXOverflow  = 0x04
	FlagTXOverflow  = 0x08
	FlagLockout     = 0x10 // protocol 2
	FlagDACOverflow = 0x20 // protocol 2
	FlagCD          = 0x40 // protocol 2: carrier detect
)

// Status is a GET_STATUS reply. Space values are free transmit buffer in
// whole frames.
type Status struct {
	Protocol int
	// EnabledModes is the enabled-modes byte (protocol 1 only).
	EnabledModes byte
	Mode         byte
	Flags        byte

	DStarSpace, DMRSpace1, DMRSpace2 int
	YSFSpace, P25Space, NXDNSpace    int
	POCSAGSpace, M17Space, FMSpace   int
}

// TX reports the TX flag.
func (s Status) TX() bool { return s.Flags&FlagTX != 0 }

// DMRSpace is the free space for slot 1 or 2.
func (s Status) DMRSpace(slot int) int {
	if slot == 1 {
		return s.DMRSpace1
	}
	return s.DMRSpace2
}

// Status reply lengths (whole frame).
const (
	StatusLen1 = 14
	StatusLen2 = 20
)

// ParseStatus parses a GET_STATUS reply payload in the layout of the given
// protocol version. Protocol 1 replies from older firmware may stop after
// the YSF space; the missing values read as 0.
func ParseStatus(protocol int, p []byte) (Status, error) {
	s := Status{Protocol: protocol}
	at := func(i int) int {
		if i < len(p) {
			return int(p[i])
		}
		return 0
	}
	switch protocol {
	case 1:
		if len(p) < 6 { // through the DMR slot 2 space
			return s, ErrMalformed
		}
		s.EnabledModes, s.Mode, s.Flags = p[0], p[1], p[2]
		s.DStarSpace, s.DMRSpace1, s.DMRSpace2 = at(3), at(4), at(5)
		s.YSFSpace, s.P25Space, s.NXDNSpace = at(6), at(7), at(8)
		s.POCSAGSpace, s.M17Space = at(9), at(10)
	case 2:
		if len(p) < StatusLen2-3 {
			return s, ErrMalformed
		}
		s.Mode, s.Flags = p[0], p[1]
		s.DStarSpace, s.DMRSpace1, s.DMRSpace2 = at(3), at(4), at(5)
		s.YSFSpace, s.P25Space, s.NXDNSpace = at(6), at(7), at(8)
		s.FMSpace, s.POCSAGSpace = at(10), at(11)
	default:
		return s, fmt.Errorf("mmdvm: protocol version %d is not supported", protocol)
	}
	return s, nil
}

// EncodeStatus is the modem's GET_STATUS reply (used by the fake modem).
func EncodeStatus(s Status) ([]byte, error) {
	b := func(v int) byte {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return byte(v)
	}
	switch s.Protocol {
	case 1:
		return encode(CmdGetStatus, s.EnabledModes, s.Mode, s.Flags,
			b(s.DStarSpace), b(s.DMRSpace1), b(s.DMRSpace2), b(s.YSFSpace),
			b(s.P25Space), b(s.NXDNSpace), b(s.POCSAGSpace), b(s.M17Space)), nil
	case 2:
		return encode(CmdGetStatus, s.Mode, s.Flags, 0,
			b(s.DStarSpace), b(s.DMRSpace1), b(s.DMRSpace2), b(s.YSFSpace),
			b(s.P25Space), b(s.NXDNSpace), 0, b(s.FMSpace), b(s.POCSAGSpace),
			0, 0, 0, 0, 0), nil
	}
	return nil, fmt.Errorf("mmdvm: protocol version %d is not supported", s.Protocol)
}
