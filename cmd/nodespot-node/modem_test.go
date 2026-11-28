package main

import (
	"log/slog"
	"testing"

	"github.com/w1jar/nodespot/internal/native/node"
)

// With a receive frequency the driver receives in simplex with power 0, the
// offsets are applied, and transmit stays off whatever Allow transmit says.
func TestModemSettingsReceive(t *testing.T) {
	pw := 80
	m := (&node.MMDVMConfig{AllowTX: true, RXFreqHz: 441000000, TXFreqHz: 446000000,
		RXOffsetHz: -100, TXOffsetHz: 200, RFPower: &pw}).Resolved() // duplex by default
	s := modemSettings(m, slog.New(slog.DiscardHandler))
	if !s.RXOnly || s.DetectOnly || s.AllowTX {
		t.Errorf("rx only %v, detect only %v, allow TX %v", s.RXOnly, s.DetectOnly, s.AllowTX)
	}
	if s.Port != "/dev/serial0" || s.Baud != 115200 || s.Modem.Duplex || s.Modem.ColorCode != 1 {
		t.Errorf("settings %+v", s)
	}
	if s.Freq.RX != 440999900 || s.Freq.TX != 446000200 || s.Freq.Power != 0 {
		t.Errorf("freq %+v (offsets on rx and tx, power 0)", s.Freq)
	}
}

// The transmit frequency defaults to the receive one, each with its own offset.
func TestModemSettingsTXDefaultsToRX(t *testing.T) {
	m := (&node.MMDVMConfig{RXFreqHz: 441000000, RXOffsetHz: 100, TXOffsetHz: -300}).Resolved()
	s := modemSettings(m, nil)
	if s.Freq.RX != 441000100 || s.Freq.TX != 440999700 {
		t.Errorf("freq %+v", s.Freq)
	}
}

// With no receive frequency the node only detects the modem.
func TestModemSettingsDetectOnly(t *testing.T) {
	m := (&node.MMDVMConfig{AllowTX: true, TXFreqHz: 446000000, RXOffsetHz: 5}).Resolved()
	s := modemSettings(m, nil)
	if !s.DetectOnly || s.RXOnly || s.AllowTX || s.Freq.RX != 0 || s.Freq.TX != 0 {
		t.Errorf("settings %+v", s)
	}
}
