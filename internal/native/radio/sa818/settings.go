package sa818

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Bandwidth is the channel width.
type Bandwidth int

// Channel widths. The wire value is 0 for narrow and 1 for wide.
const (
	Narrow Bandwidth = iota // 12.5 kHz
	Wide                    // 25 kHz
)

// String returns "12.5k" or "25k".
func (b Bandwidth) String() string {
	if b == Wide {
		return "25k"
	}
	return "12.5k"
}

// ToneKind selects the sub-audible squelch tone type.
type ToneKind string

// Tone kinds.
const (
	ToneNone  ToneKind = ""
	ToneCTCSS ToneKind = "ctcss"
	ToneDCS   ToneKind = "dcs"
)

// Tone is a CTCSS or DCS setting for one direction.
type Tone struct {
	Kind ToneKind `json:"kind,omitempty"`
	// CTCSSHz is one of the 38 standard tones, such as 100.0.
	CTCSSHz float64 `json:"ctcss_hz,omitempty"`
	// DCSCode is the three-digit code as written, such as 23 for 023.
	DCSCode int `json:"dcs_code,omitempty"`
	// DCSInverted selects the inverted polarity (I rather than N).
	DCSInverted bool `json:"dcs_inverted,omitempty"`
}

// ctcssTones are the 38 tones of the SA818, in code order (code = index+1).
var ctcssTones = []float64{
	67.0, 71.9, 74.4, 77.0, 79.7, 82.5, 85.4, 88.5, 91.5, 94.8,
	97.4, 100.0, 103.5, 107.2, 110.9, 114.8, 118.8, 123.0, 127.3, 131.8,
	136.5, 141.3, 146.2, 151.4, 156.7, 162.2, 167.9, 173.8, 179.9, 186.2,
	192.8, 203.5, 210.7, 218.1, 225.7, 233.6, 241.8, 250.3,
}

// CTCSSTones returns a copy of the supported CTCSS frequencies.
func CTCSSTones() []float64 { return append([]float64(nil), ctcssTones...) }

// wire returns the 4-character field the module expects.
func (t Tone) wire() (string, error) {
	switch t.Kind {
	case ToneNone:
		return "0000", nil
	case ToneCTCSS:
		for i, f := range ctcssTones {
			if math.Abs(f-t.CTCSSHz) < 0.05 {
				return fmt.Sprintf("%04d", i+1), nil
			}
		}
		return "", fmt.Errorf("sa818: %.1f Hz is not a supported CTCSS tone", t.CTCSSHz)
	case ToneDCS:
		if t.DCSCode < 1 || t.DCSCode > 777 || !octalDigits(t.DCSCode) {
			return "", fmt.Errorf("sa818: %d is not a DCS code (three octal digits)", t.DCSCode)
		}
		pol := 'N'
		if t.DCSInverted {
			pol = 'I'
		}
		return fmt.Sprintf("%03d%c", t.DCSCode, pol), nil
	}
	return "", fmt.Errorf("sa818: unknown tone kind %q", t.Kind)
}

func octalDigits(n int) bool {
	for ; n > 0; n /= 10 {
		if n%10 > 7 {
			return false
		}
	}
	return true
}

// Filters selects the module's audio filters; true means the filter is on.
// On the wire (AT+SETFILTER=emphasis,highpass,lowpass, per the SA818
// programming manual) 1 bypasses a filter and 0 leaves it in the audio path,
// so an "on" filter goes out as 0.
type Filters struct {
	Emphasis bool `json:"emphasis"`
	HighPass bool `json:"highpass"`
	LowPass  bool `json:"lowpass"`
}

// Settings is everything the module is programmed with.
type Settings struct {
	Bandwidth Bandwidth `json:"bandwidth"`
	// RxMHz and TxMHz are frequencies in MHz, such as 146.5200.
	RxMHz  float64 `json:"rx_mhz"`
	TxMHz  float64 `json:"tx_mhz"`
	RxTone Tone    `json:"rx_tone"`
	TxTone Tone    `json:"tx_tone"`
	// Squelch is 0 (open) to 8.
	Squelch int `json:"squelch"`
	// Volume is 1 to 8.
	Volume  int     `json:"volume"`
	Filters Filters `json:"filters"`
	// Tail is the tail tone (AT+SETTAIL): true opens it (1), false closes it
	// (0). Nil sends nothing and leaves the module's own setting alone.
	Tail *bool `json:"tail,omitempty"`
}

// DefaultSettings returns a simplex starting point: wide, squelch 4, volume 8,
// no tones, DefaultFilters. The frequencies must still be set.
func DefaultSettings() Settings {
	return Settings{Bandwidth: Wide, Squelch: 4, Volume: 8, Filters: DefaultFilters()}
}

// DefaultFilters are the filters of a module with no stored choice: the
// pre/de-emphasis bypassed (flat audio, as an AllStar node expects), the
// high-pass and low-pass on. On the wire: AT+SETFILTER=1,0,0.
func DefaultFilters() Filters { return Filters{Emphasis: false, HighPass: true, LowPass: true} }

func freqOK(mhz float64) bool {
	return (mhz >= 134 && mhz <= 174) || (mhz >= 400 && mhz <= 480)
}

// Validate reports the first problem with the settings.
func (s Settings) Validate() error {
	var errs []error
	if !freqOK(s.RxMHz) {
		errs = append(errs, fmt.Errorf("sa818: receive frequency %.4f MHz is outside 134-174 and 400-480", s.RxMHz))
	}
	if !freqOK(s.TxMHz) {
		errs = append(errs, fmt.Errorf("sa818: transmit frequency %.4f MHz is outside 134-174 and 400-480", s.TxMHz))
	}
	if s.Squelch < 0 || s.Squelch > 8 {
		errs = append(errs, fmt.Errorf("sa818: squelch %d is outside 0-8", s.Squelch))
	}
	if s.Volume < 1 || s.Volume > 8 {
		errs = append(errs, fmt.Errorf("sa818: volume %d is outside 1-8", s.Volume))
	}
	if s.Bandwidth != Narrow && s.Bandwidth != Wide {
		errs = append(errs, fmt.Errorf("sa818: bandwidth %d is not narrow or wide", s.Bandwidth))
	}
	if _, err := s.RxTone.wire(); err != nil {
		errs = append(errs, err)
	}
	if _, err := s.TxTone.wire(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// bypass is the wire value of a filter flag: 1 bypasses the filter, 0 keeps
// it on.
func bypass(on bool) int {
	if on {
		return 0
	}
	return 1
}

// Commands formats the AT commands that program the settings, in the order
// they are sent: group (frequencies, tones, squelch), volume, filters, and the
// tail tone when Tail is set.
func (s Settings) Commands() ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	tx, _ := s.TxTone.wire()
	rx, _ := s.RxTone.wire()
	bw := 0
	if s.Bandwidth == Wide {
		bw = 1
	}
	cmds := []string{
		fmt.Sprintf("AT+DMOSETGROUP=%d,%.4f,%.4f,%s,%d,%s", bw, s.TxMHz, s.RxMHz, tx, s.Squelch, rx),
		fmt.Sprintf("AT+DMOSETVOLUME=%d", s.Volume),
		fmt.Sprintf("AT+SETFILTER=%d,%d,%d", bypass(s.Filters.Emphasis), bypass(s.Filters.HighPass), bypass(s.Filters.LowPass)),
	}
	if s.Tail != nil {
		t := 0
		if *s.Tail {
			t = 1
		}
		cmds = append(cmds, fmt.Sprintf("AT+SETTAIL=%d", t))
	}
	return cmds, nil
}

// Summary is a one-line description for logs, with no secrets in it.
func (s Settings) Summary() string {
	return strings.TrimSpace(fmt.Sprintf("rx %.4f tx %.4f %s sq %d vol %d", s.RxMHz, s.TxMHz, s.Bandwidth, s.Squelch, s.Volume))
}
