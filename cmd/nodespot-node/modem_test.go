package main

import (
	"log/slog"
	"testing"

	"github.com/w1jar/nodespot/internal/native/node"
)

// The mmdvm preset only detects the modem: transmit stays off in the
// driver even when Allow transmit is on.
func TestModemSettingsDetectOnly(t *testing.T) {
	m := (&node.MMDVMConfig{AllowTX: true, RXFreqHz: 123456000, TXFreqHz: 123456000,
		RXOffsetHz: -100, TXOffsetHz: 200}).Resolved()
	s := modemSettings(m, slog.New(slog.DiscardHandler))
	if !s.DetectOnly || s.AllowTX {
		t.Errorf("detect only %v, allow TX %v", s.DetectOnly, s.AllowTX)
	}
	if s.Port != "/dev/serial0" || s.Baud != 115200 || !s.Modem.Duplex || s.Modem.ColorCode != 1 {
		t.Errorf("settings %+v", s)
	}
	if s.Freq.RX != 123455900 || s.Freq.TX != 123456200 {
		t.Errorf("freq %+v", s.Freq)
	}
	if s := modemSettings((*node.MMDVMConfig)(nil).Resolved(), nil); s.Freq.RX != 0 || s.Freq.TX != 0 {
		t.Errorf("unset frequencies with offsets: %+v", s.Freq)
	}
}
