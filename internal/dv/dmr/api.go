package dmr

// This file declares the exported API of the package. Implementations live in
// the other files.

const (
	BurstLen   = 33 // one DMR burst, 264 bits
	AMBELen    = 7  // AMBE+2 frame, 49 bits
	AMBE72Len  = 9  // DMR air-interface AMBE+2 frame, 72 bits with FEC
	LCLen      = 9  // full link control, 72 bits
	DMRDLen    = 55 // Homebrew DMRD packet with BER and RSSI
	DMRDMinLen = 53 // DMRD without BER/RSSI
)

// Burst is one 33-byte DMR burst as carried by the networks: 108 bits of
// payload, a 48-bit SYNC or EMB+embedded field, 108 bits of payload.
type Burst [BurstLen]byte

// AMBE is one 20 ms AMBE+2 frame: 49 bits, MSB first in bytes 0-5, bit 49
// in the MSB of byte 6 (the low 7 bits of byte 6 are zero).
type AMBE [AMBELen]byte

// AMBE72 is one AMBE+2 frame as sent on the DMR air interface (Golay FEC,
// PRNG whitening and interleave applied).
type AMBE72 [AMBE72Len]byte

// FrameType is the Homebrew / Rewind frame type of a burst (DMRD flags bits 5-4).
type FrameType uint8

const (
	FrameVoice     FrameType = 0 // voice burst B-F (VoiceSeq 1-5)
	FrameVoiceSync FrameType = 1 // voice burst A (VoiceSeq 0)
	FrameDataSync  FrameType = 2 // data burst; DMRD low nibble is the DataType
)

// DataType is the slot-type data type of a data burst (ETSI TS 102 361-1 9.3.6).
type DataType uint8

const (
	DataPIHeader         DataType = 0x0
	DataVoiceLCHeader    DataType = 0x1
	DataTerminatorWithLC DataType = 0x2
	DataCSBK             DataType = 0x3
	DataMBCHeader        DataType = 0x4
	DataMBCContinuation  DataType = 0x5
	DataDataHeader       DataType = 0x6
	DataRate12           DataType = 0x7
	DataRate34           DataType = 0x8
	DataIdle             DataType = 0x9
	DataRate1            DataType = 0xA
)

// Sync is a 48-bit SYNC pattern (ETSI TS 102 361-1 9.1.1, table 9.2).
type Sync uint64

const (
	SyncBSVoice     Sync = 0x755FD7DF75F7
	SyncBSData      Sync = 0xDFF57D75DF5D
	SyncMSVoice     Sync = 0x7F7D5DD57DFD
	SyncMSData      Sync = 0xD5D7F77FD757
	SyncRC          Sync = 0x77D55F7DFD77
	SyncTDMA1Voice  Sync = 0x5D577F7757FF // direct mode time slot 1
	SyncTDMA1Data   Sync = 0xF7FDD5DDFD55
	SyncTDMA2Voice  Sync = 0x7DFFD5F55D5F // direct mode time slot 2
	SyncTDMA2Data   Sync = 0xD7557F5FF7F5
	SyncNone        Sync = 0 // returned by MatchSync when nothing matches
	SyncMaxBitError      = 4 // default tolerance used by Parse/RX
)

// LCSS is the link control start/stop field of the EMB.
type LCSS uint8

const (
	LCSSSingle   LCSS = 0 // single fragment (also used in burst A/F with null data)
	LCSSFirst    LCSS = 1
	LCSSLast     LCSS = 2
	LCSSContinue LCSS = 3
)

// EMB is the embedded signalling field of voice bursts B-F.
type EMB struct {
	CC   uint8 // colour code 0-15
	PI   bool  // privacy indicator
	LCSS LCSS
}

// FLCO is the full link control opcode.
type FLCO uint8

const (
	FLCOGroup   FLCO = 0x00 // group voice channel user
	FLCOPrivate FLCO = 0x03 // unit to unit voice channel user
)

// LC is a full link control word (ETSI TS 102 361-2 7.1.1.1).
type LC struct {
	PF             bool // protect flag
	FLCO           FLCO
	FID            uint8 // feature set ID, 0 = standard
	ServiceOptions uint8
	Dst            uint32 // 24 bits: talkgroup or private ID
	Src            uint32 // 24 bits
}

// SlotType is the 20-bit slot type field of a data burst.
type SlotType struct {
	CC       uint8
	DataType DataType
}

// DMRD is a Homebrew (MMDVM) DMRD packet.
//
//	0  "DMRD"
//	4  seq u8
//	5  src u24
//	8  dst u24
//	11 repeater ID u32
//	15 flags: bit7 slot 2, bit6 private, bits5-4 FrameType, bits3-0 VoiceSeq/DataType
//	16 stream ID u32
//	20 33-byte burst
//	53 BER u8, 54 RSSI u8 (optional)
type DMRD struct {
	Seq        uint8
	Src, Dst   uint32
	RepeaterID uint32
	Slot       int // 1 or 2
	Private    bool
	FrameType  FrameType
	DTypeVSeq  uint8 // voice sequence 0-5 (A-F), or the DataType for data sync
	StreamID   uint32
	Burst      Burst
	BER, RSSI  uint8
	HasBERRSSI bool // set by ParseDMRD for 55-byte packets
}

// TXConfig configures a transmit call for the superframe builder.
type TXConfig struct {
	Src     uint32
	Dst     uint32 // talkgroup, or the private ID when Private
	Private bool
	CC      uint8 // colour code, 0-15
	// Headers is how many voice LC headers Start emits (0 means 3).
	Headers int
	// FID and ServiceOptions go into the LC as is (0 is fine for both networks).
	FID            uint8
	ServiceOptions uint8
	// PadSuperframe makes End fill the superframe up to burst F with silence
	// before the terminator. Without it only the current burst is padded.
	PadSuperframe bool
	// MSSync uses the MS-sourced sync patterns instead of the BS-sourced ones.
	MSSync bool
}

// TXFrame is one burst produced by the superframe builder, with the
// signalling the networks put next to it.
type TXFrame struct {
	FrameType FrameType
	DTypeVSeq uint8 // DataType for FrameDataSync, voice sequence 0-5 otherwise
	Burst     Burst
}

// RXResult is what RX.Push learnt from one burst.
type RXResult struct {
	// LC is set (LCValid) when a header, a terminator or a complete embedded
	// LC (late entry) has been decoded in this burst.
	LC           LC
	LCValid      bool
	LCFromHeader bool // from a voice LC header or terminator, not embedded
	NewCall      bool // first LC of the stream (header or late entry)
	// Voice holds the three AMBE frames of a voice burst (Voice true).
	Voice     [3]AMBE
	HasVoice  bool
	FECErrors int  // corrected bit errors in this burst's AMBE frames
	End       bool // terminator with LC seen
}

var (
	// SilenceAMBE is the AMBE 49-bit form of the AMBE+2 silence frame
	// (DecodeAMBE(SilenceAMBE72), 0 bit errors). It pads a trailing partial
	// burst. Note: {b9 e8 81 52 61 73 00} is NOT the 49-bit silence; it is the
	// first 7 bytes of the 72-bit air-interface frame.
	SilenceAMBE = AMBE{0xf8, 0x01, 0xa9, 0x9f, 0x8c, 0xe0, 0x80}
	// SilenceAMBE72 is the DMR air-interface silence frame (B9E881526173002A6B).
	SilenceAMBE72 = AMBE72{0xb9, 0xe8, 0x81, 0x52, 0x61, 0x73, 0x00, 0x2a, 0x6b}
)
