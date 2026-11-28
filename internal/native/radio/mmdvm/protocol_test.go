package mmdvm

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
)

func hexEq(t *testing.T, name string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s:\n got % X\nwant % X", name, got, want)
	}
}

func TestSimpleCommands(t *testing.T) {
	hexEq(t, "GET_VERSION", GetVersion(), []byte{0xE0, 0x03, 0x00})
	hexEq(t, "GET_STATUS", GetStatus(), []byte{0xE0, 0x03, 0x01})
	hexEq(t, "SET_MODE idle", SetMode(ModeIdle), []byte{0xE0, 0x04, 0x03, 0x00})
	hexEq(t, "SET_MODE DMR", SetMode(ModeDMR), []byte{0xE0, 0x04, 0x03, 0x02})
	hexEq(t, "DMR_START 1", DMRStart(true), []byte{0xE0, 0x04, 0x1D, 0x01})
	hexEq(t, "DMR_START 0", DMRStart(false), []byte{0xE0, 0x04, 0x1D, 0x00})
	hexEq(t, "DMR_ABORT 1", DMRAbort(1), []byte{0xE0, 0x04, 0x1E, 0x01})
	hexEq(t, "DMR_ABORT 2", DMRAbort(2), []byte{0xE0, 0x04, 0x1E, 0x02})
	hexEq(t, "DMR_LOST1", DMRLost(1), []byte{0xE0, 0x03, 0x19})
	hexEq(t, "DMR_LOST2", DMRLost(2), []byte{0xE0, 0x03, 0x1B})
	hexEq(t, "ACK", Ack(CmdSetConfig), []byte{0xE0, 0x04, 0x70, 0x02})
	hexEq(t, "NAK", Nak(CmdSetConfig, ReasonBadData), []byte{0xE0, 0x05, 0x7F, 0x02, 0x04})
	hexEq(t, "DMR_SHORTLC", DMRShortLC([9]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}),
		[]byte{0xE0, 0x0C, 0x1C, 1, 2, 3, 4, 5, 6, 7, 8, 9})
}

func TestSetFreq(t *testing.T) {
	got := SetFreq(Freq{RX: 438800000, TX: 433000000, Power: 100, POCSAG: 439987500})
	hexEq(t, "SET_FREQ", got, []byte{0xE0, 0x11, 0x04, 0x00,
		0x80, 0x8E, 0x27, 0x1A, // RX LE
		0x40, 0x0E, 0xCF, 0x19, // TX LE
		0xFF,                   // 100 %
		0x2C, 0xAD, 0x39, 0x1A, // POCSAG LE
	})
	got = SetFreq(Freq{RX: 438800000, TX: 438800000, Power: 50})
	hexEq(t, "SET_FREQ half", got, []byte{0xE0, 0x11, 0x04, 0x00,
		0x80, 0x8E, 0x27, 0x1A, 0x80, 0x8E, 0x27, 0x1A, 0x80, 0, 0, 0, 0})
}

func testConfig() Config {
	return Config{Duplex: true, Modes: Modes{DMR: true}, TXDelay: 100 * time.Millisecond,
		ColorCode: 1, RXLevel: 50, DMRLevel: 50}
}

func TestSetConfigV1(t *testing.T) {
	got, err := SetConfig(1, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	hexEq(t, "SET_CONFIG v1 duplex", got, []byte{0xE0, 0x1A, 0x02,
		0x00,       // flags: duplex
		0x02,       // modes: DMR
		0x0A,       // TX delay 100 ms
		0x00,       // initial mode idle
		0x80,       // RX level 50 %
		0x00,       // CW ID level
		0x01,       // color code
		0x00,       // DMR delay
		0x80,       // old oscillator offset
		0x00, 0x80, // D-Star, DMR level
		0x00, 0x00, // YSF, P25 level
		0x80, 0x80, // TX, RX DC offset
		0x00,       // NXDN level
		0x00,       // YSF TX hang
		0x00, 0x00, // POCSAG, FM level
		0x00, 0x00, // P25, NXDN TX hang
		0x00, 0x00, // reserved
	})
	if len(got) != 26 {
		t.Errorf("v1 length %d, want 26", len(got))
	}

	c := testConfig()
	c.Duplex = false
	c.RXInvert, c.PTTInvert, c.Debug = true, true, true
	c.Modes.POCSAG = true
	c.TXDCOffset, c.RXDCOffset = -3, 5
	got, _ = SetConfig(1, c)
	hexEq(t, "v1 flags", got[3:5], []byte{0x95, 0x22})
	hexEq(t, "v1 DC offsets", got[16:18], []byte{0x7D, 0x85})
}

func TestSetConfigV2(t *testing.T) {
	c := testConfig()
	c.DMRDelay = 7
	got, err := SetConfig(2, c)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xE0, 0x28, 0x02,
		0x00,       // flags
		0x02, 0x00, // modes 1 and 2
		0x0A,       // TX delay
		0x00,       // initial mode
		0x80, 0x80, // TX, RX DC offset
		0x80, 0x00, // RX, CW ID level
		0x00, 0x80, 0x00, 0x00, 0x00, // D-Star, DMR, YSF, P25, NXDN level
		0x00,       // reserved
		0x00, 0x00, // POCSAG, FM level
		0x00, 0x00, 0x00, // reserved
		0x00, 0x00, 0x00, // YSF, P25, NXDN TX hang
		0x00, 0x00, // reserved
		0x00,                   // DMR flags (trunking)
		0x01,                   // color code
		0x07,                   // DMR delay
		0x80,                   // old oscillator offset
		0, 0, 0, 0, 0, 0, 0, 0, // FM parameters and spare
	}
	hexEq(t, "SET_CONFIG v2", got, want)
	if len(got) != 40 {
		t.Errorf("v2 length %d, want 40", len(got))
	}
	c.Modes = Modes{DMR: true, FM: true, POCSAG: true}
	c.DMRTrunking = true
	c.Duplex = false
	got, _ = SetConfig(2, c)
	hexEq(t, "v2 flags and modes", got[3:6], []byte{0x80, 0x22, 0x01})
	if got[28] != 0x80 {
		t.Errorf("v2 trunking byte 0x%02X", got[28])
	}
}

func TestSetConfigCheck(t *testing.T) {
	bad := []func(*Config){
		func(c *Config) { c.TXDelay = 510 * time.Millisecond },
		func(c *Config) { c.ColorCode = 16 },
		func(c *Config) { c.InitialMode = ModeYSF },
		func(c *Config) { c.TXDCOffset = 128 },
	}
	for i, f := range bad {
		c := testConfig()
		f(&c)
		if _, err := SetConfig(2, c); err == nil {
			t.Errorf("case %d: no error", i)
		}
	}
	if _, err := SetConfig(3, testConfig()); err == nil {
		t.Error("protocol 3 accepted")
	}
}

func testBurst() dmr.Burst {
	var b dmr.Burst
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestDMRData(t *testing.T) {
	b := testBurst()
	want := append([]byte{0xE0, 0x25, 0x18, 0x00}, b[:]...)
	hexEq(t, "DMR_DATA1", DMRData(1, 0, &b), want)
	want = append([]byte{0xE0, 0x25, 0x1A, 0x20}, b[:]...)
	hexEq(t, "DMR_DATA2", DMRData(2, 0x20, &b), want)

	f := Frame{Cmd: CmdDMRData2, Payload: append([]byte{0x42}, b[:]...)}
	d, err := ParseDMRData(f)
	if err != nil || d.Slot != 2 || d.Control != 0x42 || d.Burst != b || d.HasRSSI {
		t.Errorf("ParseDMRData = %+v, %v", d, err)
	}
	f = Frame{Cmd: CmdDMRData1, Payload: append(append([]byte{0x03}, b[:]...), 0x01, 0x2C)}
	d, err = ParseDMRData(f)
	if err != nil || d.Slot != 1 || !d.HasRSSI || d.RSSI != 0x012C {
		t.Errorf("ParseDMRData RSSI = %+v, %v", d, err)
	}
	if _, err := ParseDMRData(Frame{Cmd: CmdDMRData1, Payload: b[:]}); err == nil {
		t.Error("short DMR data accepted")
	}
	if _, err := ParseDMRData(Frame{Cmd: CmdGetStatus}); err == nil {
		t.Error("status taken as DMR data")
	}
}

func TestControl(t *testing.T) {
	cases := []struct {
		c  byte
		ft dmr.FrameType
		v  uint8
	}{
		{0x20, dmr.FrameVoiceSync, 0},
		{0x01, dmr.FrameVoice, 1},
		{0x05, dmr.FrameVoice, 5},
		{0x41, dmr.FrameDataSync, uint8(dmr.DataVoiceLCHeader)},
		{0x42, dmr.FrameDataSync, uint8(dmr.DataTerminatorWithLC)},
		{0x43, dmr.FrameDataSync, uint8(dmr.DataCSBK)},
	}
	for _, c := range cases {
		ft, v, err := ParseControl(c.c)
		if err != nil || ft != c.ft || v != c.v {
			t.Errorf("ParseControl(0x%02X) = %d %d %v", c.c, ft, v, err)
		}
		if got := Control(c.ft, c.v); got != c.c {
			t.Errorf("Control(%d, %d) = 0x%02X, want 0x%02X", c.ft, c.v, got, c.c)
		}
	}
	for _, c := range []byte{0x00, 0x06, 0x10} {
		if _, _, err := ParseControl(c); err == nil {
			t.Errorf("ParseControl(0x%02X) accepted", c)
		}
	}
}

func TestParseAck(t *testing.T) {
	cmd, err := ParseAck(Frame{Cmd: CmdACK, Payload: []byte{CmdSetMode}})
	if err != nil || cmd != CmdSetMode {
		t.Errorf("ACK = %d, %v", cmd, err)
	}
	for _, r := range []Reason{ReasonInvalidCommand, ReasonWrongState, ReasonBadData, ReasonNoSpace, ReasonNoDuplex} {
		cmd, err = ParseAck(Frame{Cmd: CmdNAK, Payload: []byte{CmdSetConfig, byte(r)}})
		var nak *NAKError
		if cmd != CmdSetConfig || !errors.As(err, &nak) || nak.Reason != r || nak.Cmd != CmdSetConfig {
			t.Errorf("NAK %d = %d, %v", r, cmd, err)
		}
		if nak != nil && nak.Error() == "" {
			t.Error("empty NAK text")
		}
	}
	if _, err := ParseAck(Frame{Cmd: CmdNAK, Payload: []byte{2}}); !errors.Is(err, ErrMalformed) {
		t.Errorf("short NAK: %v", err)
	}
	if _, err := ParseAck(Frame{Cmd: CmdACK}); !errors.Is(err, ErrMalformed) {
		t.Errorf("short ACK: %v", err)
	}
	if ReasonNoDuplex.String() != "duplex not supported by this firmware" || Reason(9).String() != "reason 9" {
		t.Error("reason text")
	}
}

func TestVersionV1(t *testing.T) {
	p := append([]byte{0x01}, "MMDVM_HS_Hat-v1.6.1 20230101 14.7456MHz ADF7021 FW\x00"...)
	p = append(p, "4A0039001551"...)
	v, err := ParseVersion(p)
	if err != nil {
		t.Fatal(err)
	}
	if v.Protocol != 1 || v.Description != "MMDVM_HS_Hat-v1.6.1 20230101 14.7456MHz ADF7021 FW" ||
		string(v.UDID) != "4A0039001551" || !v.DMR() || v.CPUName() != "" {
		t.Errorf("v1 = %+v", v)
	}
	v, err = ParseVersion(append([]byte{0x01}, "MMDVM 20200101"...))
	if err != nil || v.Description != "MMDVM 20200101" || v.UDID != nil {
		t.Errorf("v1 no UDID = %+v %v", v, err)
	}
	b, err := EncodeVersion(Version{Protocol: 1, Description: "ab", UDID: []byte("12")})
	if err != nil {
		t.Fatal(err)
	}
	hexEq(t, "EncodeVersion v1", b, []byte{0xE0, 0x09, 0x00, 0x01, 'a', 'b', 0x00, '1', '2'})
}

func TestVersionV2(t *testing.T) {
	p := []byte{0x02, 0x02, 0x01, 0x02}
	p = append(p, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F)
	p = append(p, "MMDVM_HS_Dual_Hat-v1.6.1"...)
	v, err := ParseVersion(p)
	if err != nil {
		t.Fatal(err)
	}
	if v.Protocol != 2 || v.Cap1 != 0x02 || v.Cap2 != 0x01 || v.CPU != CPUST || v.CPUName() != "ST" ||
		v.UDID[0] != 0x10 || v.UDID[15] != 0x1F || v.Description != "MMDVM_HS_Dual_Hat-v1.6.1" || !v.DMR() {
		t.Errorf("v2 = %+v", v)
	}
	v.Cap1 = 0x01
	if v.DMR() {
		t.Error("v2 without the DMR bit reports DMR")
	}
	if _, err := ParseVersion(p[:10]); !errors.Is(err, ErrMalformed) {
		t.Errorf("short v2: %v", err)
	}
	if _, err := ParseVersion([]byte{0x03, 'x'}); err == nil {
		t.Error("protocol 3 accepted")
	}
	if _, err := ParseVersion(nil); err == nil {
		t.Error("empty version accepted")
	}
	b, err := EncodeVersion(Version{Protocol: 2, Description: "x", Cap1: 2, CPU: 2})
	if err != nil {
		t.Fatal(err)
	}
	hexEq(t, "EncodeVersion v2", b, append(append([]byte{0xE0, 0x18, 0x00, 0x02, 0x02, 0x00, 0x02},
		make([]byte, 16)...), 'x'))
}

func TestStatusV1(t *testing.T) {
	frame := []byte{0xE0, 0x0E, 0x01, 0x02, 0x02, 0x05, 0x00, 0x0A, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	var dec Decoder
	var got []Frame
	dec.Feed(frame, func(f Frame) { got = append(got, f) })
	if len(got) != 1 {
		t.Fatalf("frames %d", len(got))
	}
	s, err := ParseStatus(1, got[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if s.EnabledModes != 0x02 || s.Mode != ModeDMR || s.Flags != 0x05 || !s.TX() ||
		s.DMRSpace1 != 10 || s.DMRSpace2 != 8 || s.DMRSpace(1) != 10 || s.DMRSpace(2) != 8 {
		t.Errorf("v1 = %+v", s)
	}
	b, err := EncodeStatus(s)
	if err != nil {
		t.Fatal(err)
	}
	hexEq(t, "EncodeStatus v1", b, frame)
	// Older firmware stops after the YSF space.
	s, err = ParseStatus(1, []byte{0x02, 0x00, 0x00, 0x00, 0x03, 0x04, 0x05})
	if err != nil || s.DMRSpace2 != 4 || s.YSFSpace != 5 || s.P25Space != 0 {
		t.Errorf("short v1 = %+v %v", s, err)
	}
	if _, err := ParseStatus(1, []byte{0, 0, 0, 0, 0}); !errors.Is(err, ErrMalformed) {
		t.Errorf("too short v1: %v", err)
	}
}

func TestStatusV2(t *testing.T) {
	frame := []byte{0xE0, 0x14, 0x01,
		0x02,             // mode DMR
		0x41,             // TX + carrier detect
		0x00,             // reserved
		0x00, 0x07, 0x09, // D-Star, DMR1, DMR2
		0x00, 0x00, 0x00, // YSF, P25, NXDN
		0x00,       // reserved
		0x03, 0x04, // FM, POCSAG
		0, 0, 0, 0, 0}
	var dec Decoder
	var got []Frame
	dec.Feed(frame, func(f Frame) { got = append(got, f) })
	if len(got) != 1 {
		t.Fatalf("frames %d", len(got))
	}
	s, err := ParseStatus(2, got[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != ModeDMR || s.Flags&FlagCD == 0 || !s.TX() || s.DMRSpace1 != 7 || s.DMRSpace2 != 9 ||
		s.FMSpace != 3 || s.POCSAGSpace != 4 {
		t.Errorf("v2 = %+v", s)
	}
	b, err := EncodeStatus(s)
	if err != nil {
		t.Fatal(err)
	}
	hexEq(t, "EncodeStatus v2", b, frame)
	if _, err := ParseStatus(2, frame[3:15]); !errors.Is(err, ErrMalformed) {
		t.Errorf("short v2: %v", err)
	}
	if _, err := ParseStatus(4, frame[3:]); err == nil {
		t.Error("protocol 4 accepted")
	}
}

func TestEncodeLong(t *testing.T) {
	p := make([]byte, 300)
	for i := range p {
		p[i] = byte(i)
	}
	b, err := Encode(CmdSerialData, p)
	if err != nil {
		t.Fatal(err)
	}
	// 4-byte header + 300 = 304 = 255 + 49.
	hexEq(t, "long header", b[:4], []byte{0xE0, 0x00, 49, 0x80})
	if len(b) != 304 {
		t.Errorf("long length %d", len(b))
	}
	var dec Decoder
	var got []Frame
	dec.Feed(b, func(f Frame) { got = append(got, f) })
	if len(got) != 1 || got[0].Cmd != CmdSerialData || !bytes.Equal(got[0].Payload, p) {
		t.Errorf("long decode %d frames", len(got))
	}
	// 252 payload bytes still fit a short frame; 253 do not.
	b, _ = Encode(CmdSerialData, p[:252])
	hexEq(t, "largest short", b[:3], []byte{0xE0, 0xFF, 0x80})
	b, _ = Encode(CmdSerialData, p[:253])
	hexEq(t, "smallest long", b[:4], []byte{0xE0, 0x00, 0x02, 0x80}) // 4 + 253 = 255 + 2
	if _, err := Encode(CmdSerialData, make([]byte, 507)); !errors.Is(err, ErrFrameTooLong) {
		t.Errorf("too long: %v", err)
	}
	if _, err := Encode(CmdSerialData, make([]byte, 506)); err != nil {
		t.Errorf("longest: %v", err)
	}
}

func TestDecoderResync(t *testing.T) {
	stream := []byte{0x55, 0xAA}              // noise
	stream = append(stream, 0xE0, 0x02)       // impossible length
	stream = append(stream, 0xE0, 0x00, 0x00) // impossible long length
	stream = append(stream, 0xE0, 0x04, 0x70, 0x02)
	var dec Decoder
	var got []Frame
	dec.Feed(stream, func(f Frame) { got = append(got, f) })
	if len(got) != 1 || got[0].Cmd != CmdACK || got[0].Payload[0] != CmdSetConfig {
		t.Fatalf("got %v", got)
	}
	if dec.Skipped != 7 {
		t.Errorf("skipped %d, want 7", dec.Skipped)
	}
	got = nil
	dec = Decoder{}
	stream = []byte{0x01, 0xE0, 0x01, 0xE0, 0x04, 0x70, 0x03, 0xE0, 0x05, 0x7F, 0x02, 0x05}
	for _, c := range stream {
		dec.Feed([]byte{c}, func(f Frame) { got = append(got, f) })
	}
	if len(got) != 2 || got[0].Cmd != CmdACK || got[0].Payload[0] != CmdSetMode ||
		got[1].Cmd != CmdNAK || !bytes.Equal(got[1].Payload, []byte{0x02, 0x05}) {
		t.Fatalf("got %v", got)
	}
	if dec.Skipped != 3 { // 01, and E0 01 dropped as a bad start
		t.Errorf("skipped %d, want 3", dec.Skipped)
	}
}

func TestCommandName(t *testing.T) {
	if CommandName(CmdDMRShortLC) == "" || CommandName(0x55) != "0x55" {
		t.Errorf("names %q %q", CommandName(CmdDMRShortLC), CommandName(0x55))
	}
	if s := (Frame{Cmd: CmdACK, Payload: []byte{2}}).String(); s == "" {
		t.Error("empty frame text")
	}
}
