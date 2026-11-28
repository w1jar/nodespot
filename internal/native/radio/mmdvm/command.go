package mmdvm

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
)

// Command bytes (the subset nodespot uses, plus the ones it logs).
const (
	CmdGetVersion = 0x00
	CmdGetStatus  = 0x01
	CmdSetConfig  = 0x02
	CmdSetMode    = 0x03
	CmdSetFreq    = 0x04
	CmdCalData    = 0x08
	CmdRSSIData   = 0x09
	CmdSendCWID   = 0x0A

	CmdDMRData1   = 0x18
	CmdDMRLost1   = 0x19
	CmdDMRData2   = 0x1A
	CmdDMRLost2   = 0x1B
	CmdDMRShortLC = 0x1C
	CmdDMRStart   = 0x1D
	CmdDMRAbort   = 0x1E

	CmdACK = 0x70
	CmdNAK = 0x7F

	CmdSerialData  = 0x80
	CmdTransparent = 0x90
	CmdQSOInfo     = 0x91

	CmdDebug1    = 0xF1
	CmdDebug2    = 0xF2
	CmdDebug3    = 0xF3
	CmdDebug4    = 0xF4
	CmdDebug5    = 0xF5
	CmdDebugDump = 0xFA
)

var cmdNames = map[byte]string{
	CmdGetVersion: "GET_VERSION", CmdGetStatus: "GET_STATUS", CmdSetConfig: "SET_CONFIG",
	CmdSetMode: "SET_MODE", CmdSetFreq: "SET_FREQ", CmdCalData: "CAL_DATA", CmdRSSIData: "RSSI_DATA",
	CmdSendCWID: "SEND_CWID", CmdDMRData1: "DMR_DATA1", CmdDMRLost1: "DMR_LOST1",
	CmdDMRData2: "DMR_DATA2", CmdDMRLost2: "DMR_LOST2", CmdDMRShortLC: "DMR_SHORTLC",
	CmdDMRStart: "DMR_START", CmdDMRAbort: "DMR_ABORT", CmdACK: "ACK", CmdNAK: "NAK",
	CmdSerialData: "SERIAL_DATA", CmdTransparent: "TRANSPARENT", CmdQSOInfo: "QSO_INFO",
	CmdDebug1: "DEBUG1", CmdDebug2: "DEBUG2", CmdDebug3: "DEBUG3", CmdDebug4: "DEBUG4",
	CmdDebug5: "DEBUG5", CmdDebugDump: "DEBUG_DUMP",
}

// CommandName is the protocol name of a command byte.
func CommandName(c byte) string {
	if n, ok := cmdNames[c]; ok {
		return n
	}
	return fmt.Sprintf("0x%02X", c)
}

// Modem modes (SET_MODE, SET_CONFIG's initial mode, GET_STATUS).
const (
	ModeIdle   = 0
	ModeDStar  = 1
	ModeDMR    = 2
	ModeYSF    = 3
	ModeP25    = 4
	ModeNXDN   = 5
	ModePOCSAG = 6
	ModeFM     = 10
)

// GetVersion is the GET_VERSION command.
func GetVersion() []byte { return encode(CmdGetVersion) }

// GetStatus is the GET_STATUS command.
func GetStatus() []byte { return encode(CmdGetStatus) }

// SetMode is the SET_MODE command. The modem ACKs it, or NAKs with
// [ReasonBadData] when the mode is not enabled.
func SetMode(mode byte) []byte { return encode(CmdSetMode, mode) }

// Freq is the SET_FREQ payload.
type Freq struct {
	RX, TX uint32 // Hz
	// Power is the RF level in percent, 0 to 100.
	Power float64
	// POCSAG is the POCSAG frequency in Hz; 0 is fine when POCSAG is off.
	POCSAG uint32
}

// SetFreq is the SET_FREQ command (17 bytes). Only hotspot boards use it;
// the full MMDVM firmware ACKs it and ignores the payload.
func SetFreq(f Freq) []byte {
	p := make([]byte, 14)
	binary.LittleEndian.PutUint32(p[1:], f.RX)
	binary.LittleEndian.PutUint32(p[5:], f.TX)
	p[9] = level(f.Power)
	binary.LittleEndian.PutUint32(p[10:], f.POCSAG)
	return encode(CmdSetFreq, p...)
}

// Modes is a set of enabled modes for SET_CONFIG.
type Modes struct {
	DStar, DMR, YSF, P25, NXDN, POCSAG, FM bool
}

func (m Modes) enabled(mode byte) bool {
	switch mode {
	case ModeIdle:
		return true
	case ModeDStar:
		return m.DStar
	case ModeDMR:
		return m.DMR
	case ModeYSF:
		return m.YSF
	case ModeP25:
		return m.P25
	case ModeNXDN:
		return m.NXDN
	case ModePOCSAG:
		return m.POCSAG
	case ModeFM:
		return m.FM
	}
	return false
}

// Config is the SET_CONFIG payload. Levels are percentages, 0 to 100.
type Config struct {
	RXInvert, TXInvert, PTTInvert bool
	YSFLowDeviation               bool
	Debug                         bool
	COSLockout                    bool
	// Duplex runs both DMR slots; false sets the simplex flag.
	Duplex bool

	Modes Modes
	// TXDelay is rounded down to 10 ms; at most 500 ms.
	TXDelay time.Duration
	// InitialMode is the mode the modem starts in (normally ModeIdle).
	InitialMode byte

	RXLevel, CWIDLevel                       float64
	DStarLevel, DMRLevel, YSFLevel, P25Level float64
	NXDNLevel, POCSAGLevel, FMLevel          float64

	// TXDCOffset and RXDCOffset are -128 to 127.
	TXDCOffset, RXDCOffset int

	ColorCode uint8 // DMR color code, 0 to 15
	DMRDelay  uint8
	// DMRTrunking sets the trunking flag (protocol 2 only).
	DMRTrunking bool

	YSFTXHang, P25TXHang, NXDNTXHang uint8
}

// MaxTXDelay is the largest TX delay the firmware accepts.
const MaxTXDelay = 500 * time.Millisecond

// Check reports a Config the firmware would refuse.
func (c Config) Check() error {
	switch {
	case c.TXDelay < 0 || c.TXDelay > MaxTXDelay:
		return fmt.Errorf("mmdvm: TX delay %v is not 0 to %v", c.TXDelay, MaxTXDelay)
	case c.ColorCode > 15:
		return fmt.Errorf("mmdvm: color code %d is not 0 to 15", c.ColorCode)
	case !c.Modes.enabled(c.InitialMode):
		return fmt.Errorf("mmdvm: initial mode %d is not enabled", c.InitialMode)
	case c.TXDCOffset < -128 || c.TXDCOffset > 127 || c.RXDCOffset < -128 || c.RXDCOffset > 127:
		return fmt.Errorf("mmdvm: DC offsets must be -128 to 127")
	}
	return nil
}

func (c Config) flags() byte {
	var f byte
	set := func(on bool, bit byte) {
		if on {
			f |= bit
		}
	}
	set(c.RXInvert, 0x01)
	set(c.TXInvert, 0x02)
	set(c.PTTInvert, 0x04)
	set(c.YSFLowDeviation, 0x08)
	set(c.Debug, 0x10)
	set(c.COSLockout, 0x20)
	set(!c.Duplex, 0x80)
	return f
}

// modeBits is the enabled-modes byte; protocol 1 has POCSAG at 0x20,
// protocol 2 has FM there and POCSAG in a second byte.
func (c Config) modeBits(protocol int) (b1, b2 byte) {
	m := c.Modes
	set := func(on bool, b *byte, bit byte) {
		if on {
			*b |= bit
		}
	}
	set(m.DStar, &b1, 0x01)
	set(m.DMR, &b1, 0x02)
	set(m.YSF, &b1, 0x04)
	set(m.P25, &b1, 0x08)
	set(m.NXDN, &b1, 0x10)
	if protocol >= 2 {
		set(m.FM, &b1, 0x20)
		set(m.POCSAG, &b2, 0x01)
	} else {
		set(m.POCSAG, &b1, 0x20)
	}
	return
}

// SetConfig is the SET_CONFIG command in the layout of the given protocol
// version: 26 bytes for protocol 1 (MMDVM_HS), 40 for protocol 2 (MMDVM).
func SetConfig(protocol int, c Config) ([]byte, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	delay := byte(c.TXDelay / (10 * time.Millisecond))
	m1, m2 := c.modeBits(protocol)
	switch protocol {
	case 1:
		p := make([]byte, 23)
		p[0] = c.flags()
		p[1] = m1
		p[2] = delay
		p[3] = c.InitialMode
		p[4] = level(c.RXLevel)
		p[5] = level(c.CWIDLevel)
		p[6] = c.ColorCode
		p[7] = c.DMRDelay
		p[8] = 128 // was the oscillator offset
		p[9] = level(c.DStarLevel)
		p[10] = level(c.DMRLevel)
		p[11] = level(c.YSFLevel)
		p[12] = level(c.P25Level)
		p[13] = byte(c.TXDCOffset + 128)
		p[14] = byte(c.RXDCOffset + 128)
		p[15] = level(c.NXDNLevel)
		p[16] = c.YSFTXHang
		p[17] = level(c.POCSAGLevel)
		p[18] = level(c.FMLevel)
		p[19] = c.P25TXHang
		p[20] = c.NXDNTXHang
		return encode(CmdSetConfig, p...), nil
	case 2:
		p := make([]byte, 37)
		p[0] = c.flags()
		p[1] = m1
		p[2] = m2
		p[3] = delay
		p[4] = c.InitialMode
		p[5] = byte(c.TXDCOffset + 128)
		p[6] = byte(c.RXDCOffset + 128)
		p[7] = level(c.RXLevel)
		p[8] = level(c.CWIDLevel)
		p[9] = level(c.DStarLevel)
		p[10] = level(c.DMRLevel)
		p[11] = level(c.YSFLevel)
		p[12] = level(c.P25Level)
		p[13] = level(c.NXDNLevel)
		p[15] = level(c.POCSAGLevel)
		p[16] = level(c.FMLevel)
		p[20] = c.YSFTXHang
		p[21] = c.P25TXHang
		p[22] = c.NXDNTXHang
		if c.DMRTrunking {
			p[25] = 0x80
		}
		p[26] = c.ColorCode
		p[27] = c.DMRDelay
		p[28] = 128
		return encode(CmdSetConfig, p...), nil
	}
	return nil, fmt.Errorf("mmdvm: protocol version %d is not supported", protocol)
}

// level turns a percentage into the firmware's 0..255 scale.
func level(pct float64) byte {
	switch {
	case !(pct > 0): // also NaN
		return 0
	case pct >= 100:
		return 255
	}
	return byte(math.Round(pct * 255 / 100))
}

// DMRData is a DMR_DATA1 or DMR_DATA2 frame for slot 1 or 2 (37 bytes).
// The host sends control 0; the modem's own frames carry the control byte
// described at [ParseControl].
func DMRData(slot int, control byte, b *dmr.Burst) []byte {
	cmd := byte(CmdDMRData2)
	if slot == 1 {
		cmd = CmdDMRData1
	}
	p := make([]byte, 0, 1+dmr.BurstLen)
	p = append(p, control)
	p = append(p, b[:]...)
	return encode(cmd, p...)
}

// DMRLost is the modem's DMR_LOST1 or DMR_LOST2 frame.
func DMRLost(slot int) []byte {
	if slot == 1 {
		return encode(CmdDMRLost1)
	}
	return encode(CmdDMRLost2)
}

// ShortLCLen is the length of the DMR_SHORTLC payload.
const ShortLCLen = 9

// DMRShortLC is the DMR_SHORTLC command: the 9-byte encoded short LC the
// modem puts in the CACH (duplex only). No ACK on success.
func DMRShortLC(lc [ShortLCLen]byte) []byte { return encode(CmdDMRShortLC, lc[:]...) }

// DMRStart is the DMR_START command, which starts (on) or stops the duplex
// TDMA downlink. No ACK on success; NAK when the modem is not in DMR mode.
func DMRStart(on bool) []byte {
	if on {
		return encode(CmdDMRStart, 1)
	}
	return encode(CmdDMRStart, 0)
}

// DMRAbort is the DMR_ABORT command: drop slot's queued transmission.
func DMRAbort(slot int) []byte { return encode(CmdDMRAbort, byte(slot)) }
