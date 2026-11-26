package radio

import (
	"errors"
	"fmt"

	"github.com/w1jar/nodespot/internal/native/radio/cm108"
)

// Preset names a ready-made set of interface settings.
type Preset string

const (
	// PresetAuto (the empty string) is what a config with no preset means:
	// the SHARI if one is detected, otherwise no radio.
	PresetAuto Preset = ""
	// PresetSHARI is the SHARI: a CM108B sound chip wired to an SA818
	// module (carrierfrom=usbinvert, ctcssfrom=no, PTT not inverted).
	PresetSHARI Preset = "shari"
	// PresetURI is a generic CM108 or CM119 board wired to an external
	// radio, such as a URI-style or RA-style interface. The radio's
	// frequency and tones are set on the radio itself.
	PresetURI Preset = "uri"
	// PresetNone is a radioless node: no audio, no PTT.
	PresetNone Preset = "none"
)

// Source and pin names, shared with the HID layer.
type Source = cm108.Source

// Interface is the resolved set of settings the driver runs with. The invert
// settings are "active low": the carrier or tone input counts as present
// when its pin is low, or the PTT pin is driven low to transmit. They are
// simpleusb's invertptt and carrierfrom=usb (active low), against
// carrierfrom=usbinvert (present when high), which is the SHARI's.
type Interface struct {
	// Preset is the preset it came from (never PresetAuto).
	Preset Preset `json:"preset"`
	// Audio is true when there is a sound chip to open at all.
	Audio bool `json:"audio"`
	// PTTPin is the GPIO (1 to 4) that keys the radio; 0 for none.
	PTTPin int `json:"pttPin"`
	// PTTActiveLow drives the pin low to transmit.
	PTTActiveLow bool `json:"pttActiveLow"`
	// COS is the carrier source: none, always, or gpio1 to gpio4.
	COS Source `json:"cos"`
	// COSActiveLow reads the carrier as present when the pin is low
	// (carrierfrom=usb); false is present when high (usbinvert).
	COSActiveLow bool `json:"cosActiveLow"`
	// CTCSS is the tone-detect source: none or gpio1 to gpio4. With a pin,
	// the carrier needs both COS and CTCSS present.
	CTCSS          Source `json:"ctcss"`
	CTCSSActiveLow bool   `json:"ctcssActiveLow"`
	// SA818 is true when the interface has an SA818 module the node
	// programs. Otherwise the frequency and tones are set on the radio.
	SA818 bool `json:"sa818"`
}

// PresetInfo describes one preset for the pickers.
type PresetInfo struct {
	Name        Preset `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Defaults are the settings the preset fills in.
	Defaults Interface `json:"defaults"`
}

var presets = []PresetInfo{
	{
		Name:  PresetSHARI,
		Title: "SHARI",
		Description: "The SHARI board: a CM108B sound chip and an SA818 radio module. " +
			"PTT on GPIO3, carrier from GPIO2 (volume down) present when high, no CTCSS input. " +
			"The node programs the SA818.",
		Defaults: Interface{Preset: PresetSHARI, Audio: true, PTTPin: 3, COS: cm108.SourceGPIO2, CTCSS: cm108.SourceNone, SA818: true},
	},
	{
		Name:  PresetURI,
		Title: "USB interface (URI-style)",
		Description: "A CM108 or CM119 USB interface wired to an external radio, such as a URI-style or RA-style board. " +
			"PTT on GPIO3, carrier from GPIO2 (volume down) present when low, no CTCSS input. " +
			"Set the frequency and tones on the radio.",
		Defaults: Interface{Preset: PresetURI, Audio: true, PTTPin: 3, COS: cm108.SourceGPIO2, COSActiveLow: true, CTCSS: cm108.SourceNone},
	},
	{
		Name:        PresetNone,
		Title:       "None (radioless)",
		Description: "No radio. The node links modes with no audio to or from a radio.",
		Defaults:    Interface{Preset: PresetNone, COS: cm108.SourceNone, CTCSS: cm108.SourceNone},
	},
}

// Presets returns the presets in the order the pickers show them.
func Presets() []PresetInfo { return append([]PresetInfo(nil), presets...) }

// LookupPreset returns the preset called name.
func LookupPreset(name Preset) (PresetInfo, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p, true
		}
	}
	return PresetInfo{}, false
}

// Overrides are the settings an operator changed from the preset. A zero
// value (0, "", nil) keeps the preset's setting.
type Overrides struct {
	PTTPin         int
	PTTActiveLow   *bool
	COS            Source
	COSActiveLow   *bool
	CTCSS          Source
	CTCSSActiveLow *bool
	// SA818 forces SA818 programming on or off.
	SA818 *bool
}

// Equal reports whether two override sets are the same.
func (o Overrides) Equal(p Overrides) bool {
	eq := func(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	return o.PTTPin == p.PTTPin && o.COS == p.COS && o.CTCSS == p.CTCSS &&
		eq(o.PTTActiveLow, p.PTTActiveLow) && eq(o.COSActiveLow, p.COSActiveLow) &&
		eq(o.CTCSSActiveLow, p.CTCSSActiveLow) && eq(o.SA818, p.SA818)
}

// Resolve applies overrides to a preset. PresetAuto resolves to the SHARI
// when shari is true (a SHARI was detected) and to none otherwise. An unknown
// preset or an impossible pin combination is an error. The none preset
// ignores overrides: there is no interface to apply them to.
func Resolve(p Preset, ov Overrides, shari bool) (Interface, error) {
	if p == PresetAuto {
		p = PresetNone
		if shari {
			p = PresetSHARI
		}
	}
	info, ok := LookupPreset(p)
	if !ok {
		return Interface{}, fmt.Errorf("unknown radio preset %q (use shari, uri or none)", p)
	}
	in := info.Defaults
	if p == PresetNone {
		return in, nil
	}
	if ov.PTTPin != 0 {
		in.PTTPin = ov.PTTPin
	}
	if ov.PTTActiveLow != nil {
		in.PTTActiveLow = *ov.PTTActiveLow
	}
	if ov.COS != "" {
		in.COS = ov.COS
	}
	if ov.COSActiveLow != nil {
		in.COSActiveLow = *ov.COSActiveLow
	}
	if ov.CTCSS != "" {
		in.CTCSS = ov.CTCSS
	}
	if ov.CTCSSActiveLow != nil {
		in.CTCSSActiveLow = *ov.CTCSSActiveLow
	}
	if ov.SA818 != nil {
		in.SA818 = *ov.SA818
	}
	return in, in.Validate()
}

// Validate checks the pins.
func (in Interface) Validate() error {
	if !in.Audio {
		return nil
	}
	var errs []error
	if in.PTTPin < 1 || in.PTTPin > 4 {
		errs = append(errs, errors.New("PTT pin must be GPIO1 to GPIO4"))
	}
	if !in.COS.Valid() {
		errs = append(errs, fmt.Errorf("COS source %q is not one of none, always, gpio1 to gpio4", in.COS))
	}
	if !in.CTCSS.Valid() || in.CTCSS == cm108.SourceAlways {
		errs = append(errs, fmt.Errorf("CTCSS source %q is not one of none, gpio1 to gpio4", in.CTCSS))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for _, s := range []struct {
		name string
		src  Source
	}{{"COS", in.COS}, {"CTCSS", in.CTCSS}} {
		if s.src.Pin() == in.PTTPin {
			errs = append(errs, fmt.Errorf("GPIO%d cannot be both the PTT output and the %s input", in.PTTPin, s.name))
		}
	}
	if in.COS.Pin() != 0 && in.COS == in.CTCSS {
		errs = append(errs, fmt.Errorf("COS and CTCSS cannot both use GPIO%d", in.COS.Pin()))
	}
	return errors.Join(errs...)
}

// Squelch returns the HID carrier decoding for the interface.
func (in Interface) Squelch() cm108.Squelch {
	return cm108.Squelch{COS: in.COS, COSActiveLow: in.COSActiveLow, CTCSS: in.CTCSS, CTCSSActiveLow: in.CTCSSActiveLow}
}
