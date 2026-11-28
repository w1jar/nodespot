package mmdvm

import (
	"fmt"
)

// The MMDVM_HS firmware's SET_FREQ limits (IO.h, CIO::setFreq).
const (
	// UHFMinHz is the lowest frequency a UHF-only duplex board takes.
	UHFMinHz = 420_000_000
	// bandHint is appended to the errors below and to a NAK of SET_FREQ.
	bandHint = "the modem takes 144 to 148, 219 to 225, 420 to 475 and 842 to 950 MHz, " +
		"never 145.8 to 146.0 or 435 to 438 MHz, and a dual-chip duplex hat only UHF (420 MHz and up)"
)

// FreqHint is the plain-words list of what MMDVM_HS accepts, for a refused
// SET_FREQ.
func FreqHint() string { return bandHint }

func inBand(f uint32) bool {
	return (f >= 144_000_000 && f < 148_000_000) || (f >= 219_000_000 && f < 225_000_000) ||
		(f >= 420_000_000 && f < 475_000_000) || (f >= 842_000_000 && f < 950_000_000)
}

func banned(f uint32) bool {
	return (f >= 145_800_000 && f <= 146_000_000) || (f >= 435_000_000 && f <= 438_000_000)
}

// FreqRules says which SET_FREQ values the firmware takes.
type FreqRules struct {
	// UHFOnly is a dual ADF7021 duplex board (not dual band): RX and TX
	// below 420 MHz are refused.
	UHFOnly bool
}

// CheckFreq reports a receive and transmit frequency (Hz) the MMDVM_HS
// firmware would NAK in SET_FREQ (as [SetFreq] sends it, with TX in the
// POCSAG field): TX in no band, either one in a banned range, or, with
// UHFOnly, either one below 420 MHz. The error says why in plain words.
// Zero means unset and is checked by the caller.
func CheckFreq(rx, tx uint32, r FreqRules) error {
	for _, c := range []struct {
		name string
		f    uint32
	}{{"receive", rx}, {"transmit", tx}} {
		switch {
		case banned(c.f):
			return fmt.Errorf("the %s frequency %s is in a range the modem bans (145.8 to 146.0 and 435 to 438 MHz)", c.name, mhz(c.f))
		case r.UHFOnly && c.f < UHFMinHz:
			return fmt.Errorf("the %s frequency %s is below 420 MHz, and a dual-chip duplex hat takes only UHF", c.name, mhz(c.f))
		}
	}
	// The firmware wants RX or TX in a band, and the POCSAG field, which
	// nodespot fills with TX, in a band too: so TX must be in one.
	if !inBand(tx) {
		return fmt.Errorf("the transmit frequency %s is in none of the bands the modem takes (144 to 148, 219 to 225, 420 to 475, 842 to 950 MHz)", mhz(tx))
	}
	return nil
}

// Accepts mirrors the firmware's own SET_FREQ test (CIO::setFreq and
// checkZUMspot), including the POCSAG frequency: the fake modem uses it.
func Accepts(rx, tx, pocsag uint32, r FreqRules) bool {
	switch {
	case !inBand(rx) && !inBand(tx), !inBand(pocsag):
		return false
	case banned(rx), banned(tx), banned(pocsag):
		return false
	case r.UHFOnly && (rx < UHFMinHz || tx < UHFMinHz):
		return false
	}
	return true
}

// mhz formats Hz as MHz with up to six decimals.
func mhz(f uint32) string {
	s := fmt.Sprintf("%d.%06d", f/1_000_000, f%1_000_000)
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s + " MHz"
}
