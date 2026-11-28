package node

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// MMDVM defaults.
const (
	DefaultMMDVMDevice    = "/dev/serial0"
	DefaultMMDVMBaud      = 115200
	DefaultMMDVMColorCode = 1
	// MaxMMDVMOffsetHz bounds the RX and TX trim offsets.
	MaxMMDVMOffsetHz = 100000
	// MinMMDVMFreqHz and MaxMMDVMFreqHz are a sanity range, not a band
	// plan: a frequency outside it is a typing mistake.
	MinMMDVMFreqHz = 100000000
	MaxMMDVMFreqHz = 1000000000
	// MaxGPIO is the largest Linux GPIO line number accepted for the
	// modem reset pin.
	MaxGPIO = 1023
)

// MMDVMBauds are the UART speeds MMDVM boards use.
var MMDVMBauds = []int{38400, 57600, 115200, 230400, 460800}

// MMDVMConfig is the MMDVM modem, used when the radio preset is "mmdvm".
// Unset values take the defaults above. Frequencies are in Hz; 0 is unset.
type MMDVMConfig struct {
	// Device is the serial port (default /dev/serial0), Baud its speed
	// (default 115200).
	Device string `json:"device,omitempty"`
	Baud   int    `json:"baud,omitempty"`
	// Duplex runs both DMR slots (default on, for a dual hat).
	Duplex *bool `json:"duplex,omitempty"`
	// RXFreqHz and TXFreqHz are the receive and transmit frequencies;
	// RXOffsetHz and TXOffsetHz trim them for the board.
	RXFreqHz   uint32 `json:"rxFreqHz,omitempty"`
	TXFreqHz   uint32 `json:"txFreqHz,omitempty"`
	RXOffsetHz int    `json:"rxOffsetHz,omitempty"`
	TXOffsetHz int    `json:"txOffsetHz,omitempty"`
	// RFPower is the transmit level in percent, 0 to 100 (unset: 0, the
	// lowest the board offers).
	RFPower *int `json:"rfPower,omitempty"`
	// ColorCode is the DMR color code, 0 to 15 (default 1).
	ColorCode *int `json:"colorCode,omitempty"`
	// AllowTX lets the node transmit. Off by default, and forced off when
	// a backup is restored.
	AllowTX bool `json:"allowTx,omitempty"`
	// ResetGPIO is the GPIO line that resets the modem, if it is wired.
	ResetGPIO *int `json:"resetGpio,omitempty"`
}

// MMDVMSettings is an MMDVMConfig with the defaults filled in.
type MMDVMSettings struct {
	Device     string `json:"device"`
	Baud       int    `json:"baud"`
	Duplex     bool   `json:"duplex"`
	RXFreqHz   uint32 `json:"rxFreqHz"`
	TXFreqHz   uint32 `json:"txFreqHz"`
	RXOffsetHz int    `json:"rxOffsetHz"`
	TXOffsetHz int    `json:"txOffsetHz"`
	RFPower    int    `json:"rfPower"`
	ColorCode  int    `json:"colorCode"`
	AllowTX    bool   `json:"allowTx"`
	ResetGPIO  *int   `json:"resetGpio,omitempty"`
}

// Resolved fills in the defaults. A nil config is all defaults.
func (m *MMDVMConfig) Resolved() MMDVMSettings {
	s := MMDVMSettings{Device: DefaultMMDVMDevice, Baud: DefaultMMDVMBaud, Duplex: true, ColorCode: DefaultMMDVMColorCode}
	if m == nil {
		return s
	}
	if m.Device != "" {
		s.Device = m.Device
	}
	if m.Baud != 0 {
		s.Baud = m.Baud
	}
	s.Duplex = on(m.Duplex, true)
	s.RXFreqHz, s.TXFreqHz = m.RXFreqHz, m.TXFreqHz
	s.RXOffsetHz, s.TXOffsetHz = m.RXOffsetHz, m.TXOffsetHz
	if m.RFPower != nil {
		s.RFPower = *m.RFPower
	}
	if m.ColorCode != nil {
		s.ColorCode = *m.ColorCode
	}
	s.AllowTX = m.AllowTX
	if m.ResetGPIO != nil {
		v := *m.ResetGPIO
		s.ResetGPIO = &v
	}
	return s
}

// ModemFreq is what the node sends the modem in SET_FREQ: the receive
// frequency and the transmit frequency (the receive one when no transmit
// frequency is set), each with its trim offset. receive is false when no
// receive frequency is set: the node then only detects the modem.
func (s MMDVMSettings) ModemFreq() (rx, tx uint32, receive bool) {
	if s.RXFreqHz == 0 {
		return 0, 0, false
	}
	t := s.TXFreqHz
	if t == 0 {
		t = s.RXFreqHz
	}
	return uint32(int64(s.RXFreqHz) + int64(s.RXOffsetHz)), uint32(int64(t) + int64(s.TXOffsetHz)), true
}

// Rules are the SET_FREQ rules of this board setting: a duplex setting
// (the default, for a dual-chip hat) takes UHF only. A dual-chip hat set to
// simplex is still refused below 420 MHz by the modem itself, which the
// modem status then shows.
func (s MMDVMSettings) Rules() mmdvm.FreqRules { return mmdvm.FreqRules{UHFOnly: s.Duplex} }

// CheckFreq reports why the modem would refuse these frequencies, or nil.
// The same rules are in web/src/lib/mmdvm.ts.
func (s MMDVMSettings) CheckFreq() error {
	rx, tx, ok := s.ModemFreq()
	if !ok {
		return nil
	}
	return mmdvm.CheckFreq(rx, tx, s.Rules())
}

// Equal reports whether two resolved settings are the same.
func (s MMDVMSettings) Equal(o MMDVMSettings) bool {
	g := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	a, b := s, o
	a.ResetGPIO, b.ResetGPIO = nil, nil
	return a == b && g(s.ResetGPIO) == g(o.ResetGPIO)
}

// validate reports the problems with m.
func (m *MMDVMConfig) validate() []error {
	if m == nil {
		return nil
	}
	var errs []error
	if m.Device != "" && (!filepath.IsAbs(m.Device) || filepath.Clean(m.Device) != m.Device ||
		filepath.Dir(m.Device) != "/dev") {
		errs = append(errs, fmt.Errorf("MMDVM: serial port %q must be a device in /dev, such as %s", m.Device, DefaultMMDVMDevice))
	}
	if m.Baud != 0 {
		ok := false
		for _, b := range MMDVMBauds {
			ok = ok || b == m.Baud
		}
		if !ok {
			errs = append(errs, fmt.Errorf("MMDVM: baud %d is not one of %v", m.Baud, MMDVMBauds))
		}
	}
	for name, f := range map[string]uint32{"receive": m.RXFreqHz, "transmit": m.TXFreqHz} {
		if f != 0 && (f < MinMMDVMFreqHz || f > MaxMMDVMFreqHz) {
			errs = append(errs, fmt.Errorf("MMDVM: %s frequency %d Hz must be 100 to 1000 MHz", name, f))
		}
	}
	for name, v := range map[string]int{"receive": m.RXOffsetHz, "transmit": m.TXOffsetHz} {
		if v < -MaxMMDVMOffsetHz || v > MaxMMDVMOffsetHz {
			errs = append(errs, fmt.Errorf("MMDVM: %s offset must be -%d to %d Hz", name, MaxMMDVMOffsetHz, MaxMMDVMOffsetHz))
		}
	}
	if res := m.Resolved(); len(errs) == 0 {
		if err := res.CheckFreq(); err != nil {
			errs = append(errs, fmt.Errorf("MMDVM: %w", err))
		}
	}
	if m.RFPower != nil && (*m.RFPower < 0 || *m.RFPower > 100) {
		errs = append(errs, errors.New("MMDVM: RF power must be 0 to 100%"))
	}
	if m.ColorCode != nil && (*m.ColorCode < 0 || *m.ColorCode > 15) {
		errs = append(errs, errors.New("MMDVM: color code must be 0 to 15"))
	}
	if m.ResetGPIO != nil && (*m.ResetGPIO < 0 || *m.ResetGPIO > MaxGPIO) {
		errs = append(errs, fmt.Errorf("MMDVM: reset GPIO must be 0 to %d", MaxGPIO))
	}
	return errs
}

// Modem is the MMDVM modem the node daemon runs for the mmdvm preset.
type Modem interface {
	Status() radio.ModemStatus
	// Reopen closes the modem and opens it again.
	Reopen()
}

// ErrNoModem is returned by ResetModem when the node runs no modem.
var ErrNoModem = errors.New("node: no MMDVM modem is in use (the radio preset is not mmdvm)")

// ModemInfo is the modem for the API. It never carries the modem's unique
// ID.
type ModemInfo struct {
	// Configured is true when the radio preset is mmdvm; Running when the
	// daemon runs the modem (false until it restarts on a new preset).
	Configured bool `json:"configured"`
	Running    bool `json:"running"`
	// Connected is true while the modem is open and answering.
	Connected bool   `json:"connected"`
	Device    string `json:"device"`
	Baud      int    `json:"baud"`
	// DetectOnly is true when no receive frequency is set: the node only
	// identifies and watches the modem. Receive is true when it is set up
	// to receive DMR (simplex, receive only; it never transmits in this
	// release).
	DetectOnly bool `json:"detectOnly"`
	Receive    bool `json:"receive"`
	// RXState is "" (not running), "detect", "configured" (set up, the
	// modem not open yet), "listening" or "receiving".
	RXState string `json:"rxState,omitempty"`
	// RXCall is the call being received from RF.
	RXCall *ModemCall `json:"rxCall,omitempty"`
	// Notes are plain-words remarks for the operator.
	Notes []string `json:"notes,omitempty"`

	Protocol      int      `json:"protocol,omitempty"`
	Description   string   `json:"description,omitempty"`
	Board         string   `json:"board,omitempty"`
	Firmware      string   `json:"firmware,omitempty"`
	BuildDate     string   `json:"buildDate,omitempty"`
	DuplexCapable bool     `json:"duplexCapable"`
	Duplex        bool     `json:"duplex"`
	Modes         []string `json:"modes,omitempty"`
	State         string   `json:"state,omitempty"`

	Flags       ModemFlags `json:"flags"`
	RXOverflows uint64     `json:"rxOverflows"`
	TXOverflows uint64     `json:"txOverflows"`
	Space       [2]int     `json:"space"`

	LastStatus time.Time `json:"lastStatus,omitempty"`
	Reopens    int       `json:"reopens"`
	NAKs       uint64    `json:"naks"`
	LastError  string    `json:"lastError,omitempty"`
}

// ModemCall is a call being received on the modem.
type ModemCall struct {
	Slot    int       `json:"slot"`
	Src     uint32    `json:"src"`
	Dst     uint32    `json:"dst"`
	Private bool      `json:"private,omitempty"`
	Since   time.Time `json:"since"`
}

// ModemFlags are the status flags the modem last reported.
type ModemFlags struct {
	Raw           int  `json:"raw"`
	TX            bool `json:"tx"`
	RXOverflow    bool `json:"rxOverflow"`
	TXOverflow    bool `json:"txOverflow"`
	ADCOverflow   bool `json:"adcOverflow"`
	DACOverflow   bool `json:"dacOverflow"`
	Lockout       bool `json:"lockout"`
	CarrierDetect bool `json:"carrierDetect"`
}

// ModemInfo reports the MMDVM modem.
func (e *Engine) ModemInfo() ModemInfo {
	e.mu.Lock()
	r := e.cfg.Station.Radio
	e.mu.Unlock()
	cur := r.MMDVM.Resolved()
	if e.o.Modem != nil {
		cur = e.o.ModemConfig
	}
	rxHz, _, receive := cur.ModemFreq()
	mi := ModemInfo{Configured: radio.Preset(r.Preset) == radio.PresetMMDVM, Running: e.o.Modem != nil,
		Device: cur.Device, Baud: cur.Baud, DetectOnly: !receive, Receive: receive}
	if e.o.Modem == nil {
		return mi
	}
	s := e.o.Modem.Status()
	switch {
	case !receive:
		mi.RXState = "detect"
		mi.Notes = append(mi.Notes, "Set the receive frequency to start receiving. Until then the node only detects the modem.")
	case !s.Open:
		mi.RXState = "configured"
	case s.RXCall != nil:
		mi.RXState = "receiving"
		c := s.RXCall
		mi.RXCall = &ModemCall{Slot: c.Slot, Src: c.Src, Dst: c.Dst, Private: c.Private, Since: c.Since}
	default:
		mi.RXState = "listening"
	}
	if receive {
		if cur.Duplex {
			mi.Notes = append(mi.Notes, "Duplex receive needs transmit, which comes in a later release. For now the modem listens in simplex on slot 2.")
		}
		if !s.Open && s.DuplexCapable && rxHz < mmdvm.UHFMinHz {
			mi.Notes = append(mi.Notes, "A dual-chip hat takes only UHF (420 MHz and up).")
		}
		if n := e.rfNoteText(); n != "" {
			mi.Notes = append(mi.Notes, n)
		}
	}
	mi.Connected = s.Open
	mi.Protocol, mi.Description = s.Protocol, s.Description
	mi.Board, mi.Firmware, mi.BuildDate = s.Board, s.Firmware, s.BuildDate
	mi.DuplexCapable, mi.Duplex = s.DuplexCapable, s.Duplex
	mi.Modes = append([]string(nil), s.Modes...)
	mi.State = s.State
	mi.Flags = ModemFlags{Raw: s.Flags, TX: s.TX, RXOverflow: s.RXOverflow, TXOverflow: s.TXOverflow,
		ADCOverflow: s.ADCOverflow, DACOverflow: s.DACOverflow, Lockout: s.Lockout, CarrierDetect: s.CarrierDetect}
	mi.RXOverflows, mi.TXOverflows, mi.Space = s.RXOverflows, s.TXOverflows, s.Space
	mi.LastStatus, mi.Reopens, mi.NAKs, mi.LastError = s.LastStatus, s.Reopens, s.NAKs, s.Error
	return mi
}

// ResetModem closes the modem and opens it again.
func (e *Engine) ResetModem() error {
	if e.o.Modem == nil {
		return ErrNoModem
	}
	e.o.Modem.Reopen()
	return nil
}
