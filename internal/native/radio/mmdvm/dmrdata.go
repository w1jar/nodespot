package mmdvm

import (
	"fmt"

	"github.com/w1jar/nodespot/internal/dv/dmr"
)

// DMR_DATA control byte (modem to host).
const (
	ControlVoiceSync = 0x20 // voice burst A
	ControlDataSync  = 0x40 // data burst; the low nibble is the data type
)

// ParseControl turns the control byte of a received DMR_DATA frame into
// the frame type and voice sequence or data type that [dmr.RX.Push] takes:
// 0x20 is voice burst A, 1 to 5 voice bursts B to F, 0x40 plus the data
// type a data burst (0x42 is the terminator with LC).
func ParseControl(c byte) (dmr.FrameType, uint8, error) {
	switch {
	case c&ControlDataSync != 0:
		return dmr.FrameDataSync, c & 0x0F, nil
	case c&ControlVoiceSync != 0:
		return dmr.FrameVoiceSync, 0, nil
	case c >= 1 && c <= 5:
		return dmr.FrameVoice, c, nil
	}
	return 0, 0, fmt.Errorf("mmdvm: DMR control byte 0x%02X is not voice or data", c)
}

// Control is the control byte the modem sends for a burst of frame type ft
// with voice sequence or data type v (used by the fake modem).
func Control(ft dmr.FrameType, v uint8) byte {
	switch ft {
	case dmr.FrameDataSync:
		return ControlDataSync | v&0x0F
	case dmr.FrameVoiceSync:
		return ControlVoiceSync
	}
	return v
}

// DMRFrame is a parsed DMR_DATA frame.
type DMRFrame struct {
	Slot    int
	Control byte
	Burst   dmr.Burst
	// RSSI is the raw value some firmware appends (HasRSSI).
	RSSI    uint16
	HasRSSI bool
}

// ParseDMRData parses a DMR_DATA1 or DMR_DATA2 frame.
func ParseDMRData(f Frame) (DMRFrame, error) {
	var d DMRFrame
	switch f.Cmd {
	case CmdDMRData1:
		d.Slot = 1
	case CmdDMRData2:
		d.Slot = 2
	default:
		return d, fmt.Errorf("mmdvm: %s is not DMR data", CommandName(f.Cmd))
	}
	if len(f.Payload) < 1+dmr.BurstLen {
		return d, ErrMalformed
	}
	d.Control = f.Payload[0]
	copy(d.Burst[:], f.Payload[1:])
	if len(f.Payload) >= 3+dmr.BurstLen {
		d.RSSI = uint16(f.Payload[1+dmr.BurstLen])<<8 | uint16(f.Payload[2+dmr.BurstLen])
		d.HasRSSI = true
	}
	return d, nil
}
