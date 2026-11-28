package radio

import (
	"context"
	"time"

	"github.com/w1jar/nodespot/internal/dv/network"
)

// DigitalRadio is a radio that carries codec frames and call headers per
// slot instead of PCM: an MMDVM modem. It reuses the DV network types, so an
// RF call and a network call look the same to the node.
//
// Calls on the receive side are reported to the Sink given to Run (Header,
// Voice, End; Login is never called). CallHeader.Slot says which slot the
// call is on. Transmit calls are started with StartTX, whose header's Call
// names the call for WriteVoice and whose Slot picks the slot.
type DigitalRadio interface {
	// Slots is 2 for a duplex modem (slots 1 and 2), 1 for a simplex one
	// (one slot, numbered as the modem's configuration says).
	Slots() int
	// Run opens the modem and keeps it open until ctx ends or Close is
	// called, reopening it when it stops answering. Received calls go to
	// sink, from Run's goroutines; sink must not block.
	Run(ctx context.Context, sink network.Sink) error
	// StartTX starts a transmission on h.Slot.
	StartTX(h network.CallHeader) error
	// WriteVoice adds one codec frame (7-byte AMBE for DMR) to the
	// transmission f.Call.
	WriteVoice(f network.VoiceFrame) error
	// EndTX ends the transmission on slot behind the frames already queued.
	EndTX(slot int) error
	// Abort stops all transmission now, on every slot.
	Abort()
	// Status is a snapshot for the UI.
	Status() ModemStatus
	// Close stops Run and releases the modem.
	Close() error
}

// ModemStatus is what a digital radio reports about its modem.
type ModemStatus struct {
	// Open is true while the modem is open and answering.
	Open bool `json:"open"`
	// Protocol is the modem protocol version (MMDVM: 1 or 2), 0 until known.
	Protocol int `json:"protocol,omitempty"`
	// Description is the modem's own description of its board and firmware.
	Description string `json:"description,omitempty"`
	// Board, Firmware and BuildDate (YYYY-MM-DD) are read from the
	// description, empty when it does not say.
	Board     string `json:"board,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	BuildDate string `json:"buildDate,omitempty"`
	// DuplexCapable is true when the board can run both slots (a dual-chip
	// hotspot or a full-size modem).
	DuplexCapable bool `json:"duplexCapable"`
	// Modes names the modes the firmware has enabled (protocol 1 reports
	// them in every status reply).
	Modes []string `json:"modes,omitempty"`
	// State names Mode ("idle", "dmr", ...).
	State string `json:"state,omitempty"`
	// Flags is the raw status flag byte.
	Flags int `json:"flags"`
	// Duplex is true when the modem runs both slots.
	Duplex bool `json:"duplex"`
	// Mode is the modem's current mode (MMDVM mode number).
	Mode int `json:"mode"`
	// TX is true while the modem transmits.
	TX bool `json:"tx"`
	// Flags reported by the modem.
	ADCOverflow   bool `json:"adcOverflow,omitempty"`
	RXOverflow    bool `json:"rxOverflow,omitempty"`
	TXOverflow    bool `json:"txOverflow,omitempty"`
	DACOverflow   bool `json:"dacOverflow,omitempty"`
	Lockout       bool `json:"lockout,omitempty"`
	CarrierDetect bool `json:"carrierDetect,omitempty"`
	// RXOverflows and TXOverflows count the status replies that newly
	// set the overflow flag.
	RXOverflows uint64 `json:"rxOverflows"`
	TXOverflows uint64 `json:"txOverflows"`
	// Space is the free transmit buffer per slot (index 0 is slot 1), in
	// frames, as last reported.
	Space [2]int `json:"space"`
	// LastStatus is when the modem last answered a status poll.
	LastStatus time.Time `json:"lastStatus,omitempty"`
	// Reopens counts how often the modem was closed and opened again.
	Reopens int `json:"reopens"`
	// NAKs counts refused commands; Dropped counts transmit bursts that
	// did not fit in the host queue.
	NAKs    uint64 `json:"naks"`
	Dropped uint64 `json:"dropped"`
	// Error is the last problem (open failure, no answer, a NAK).
	Error string `json:"error,omitempty"`
}
