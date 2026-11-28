package radio

import (
	"reflect"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/native/radio/cm108"
)

func ptr[T any](v T) *T { return &v }

func TestPresetDefaults(t *testing.T) {
	tests := []struct {
		preset Preset
		want   Interface
	}{
		{PresetSHARI, Interface{Preset: PresetSHARI, Audio: true, PTTPin: 3, COS: "gpio2", CTCSS: "none", SA818: true}},
		{PresetURI, Interface{Preset: PresetURI, Audio: true, PTTPin: 3, COS: "gpio2", COSActiveLow: true, CTCSS: "none"}},
		{PresetNone, Interface{Preset: PresetNone, COS: "none", CTCSS: "none"}},
	}
	for _, tc := range tests {
		got, err := Resolve(tc.preset, Overrides{}, false)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.preset, got, err, tc.want)
		}
	}
}

func TestResolveAuto(t *testing.T) {
	got, err := Resolve(PresetAuto, Overrides{}, true)
	if err != nil || got.Preset != PresetSHARI || !got.SA818 {
		t.Errorf("auto with a SHARI: %+v, %v", got, err)
	}
	got, err = Resolve(PresetAuto, Overrides{}, false)
	if err != nil || got.Preset != PresetNone || got.Audio || got.SA818 {
		t.Errorf("auto without one: %+v, %v", got, err)
	}
}

func TestResolveOverrides(t *testing.T) {
	got, err := Resolve(PresetURI, Overrides{
		PTTPin: 4, PTTActiveLow: ptr(true), COS: "gpio1", COSActiveLow: ptr(false),
		CTCSS: "gpio2", CTCSSActiveLow: ptr(true), SA818: ptr(true),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := Interface{Preset: PresetURI, Audio: true, PTTPin: 4, PTTActiveLow: true, COS: "gpio1",
		CTCSS: "gpio2", CTCSSActiveLow: true, SA818: true}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// An explicit false overrides a preset's true, and unset keeps it.
	got, _ = Resolve(PresetURI, Overrides{COSActiveLow: ptr(false)}, false)
	if got.COSActiveLow {
		t.Error("explicit false did not override the URI preset")
	}
	got, _ = Resolve(PresetSHARI, Overrides{SA818: ptr(false)}, false)
	if got.SA818 {
		t.Error("SA818 override ignored")
	}
	// The mmdvm and none presets ignore overrides.
	if got, err := Resolve(PresetMMDVM, Overrides{PTTPin: 9, COS: "bogus"}, false); err != nil ||
		got.Preset != PresetMMDVM || got.Audio || got.PTTPin != 0 {
		t.Errorf("mmdvm with overrides: %+v, %v", got, err)
	}
	got, err = Resolve(PresetNone, Overrides{PTTPin: 9, COS: "bogus"}, false)
	if err != nil || got.Audio || got.PTTPin != 0 {
		t.Errorf("none: %+v, %v", got, err)
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		p    Preset
		ov   Overrides
		want string
	}{
		{"unknown preset", "pi", Overrides{}, "unknown radio preset"},
		{"pin too high", PresetURI, Overrides{PTTPin: 5}, "PTT pin"},
		{"bad cos", PresetURI, Overrides{COS: "gpio9"}, "COS source"},
		{"bad ctcss", PresetURI, Overrides{CTCSS: "always"}, "CTCSS source"},
		{"ptt on cos pin", PresetURI, Overrides{PTTPin: 2}, "cannot be both"},
		{"cos on ptt pin", PresetSHARI, Overrides{COS: "gpio3"}, "cannot be both"},
		{"ctcss on ptt pin", PresetSHARI, Overrides{CTCSS: "gpio3"}, "cannot be both"},
		{"cos and ctcss share", PresetSHARI, Overrides{CTCSS: "gpio2"}, "cannot both use"},
	}
	for _, tc := range tests {
		_, err := Resolve(tc.p, tc.ov, false)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestOverridesEqual(t *testing.T) {
	a := Overrides{PTTPin: 3, COSActiveLow: ptr(true)}
	if !a.Equal(Overrides{PTTPin: 3, COSActiveLow: ptr(true)}) {
		t.Error("equal sets differ")
	}
	for _, b := range []Overrides{{}, {PTTPin: 3}, {PTTPin: 3, COSActiveLow: ptr(false)}, {PTTPin: 4, COSActiveLow: ptr(true)}, {PTTPin: 3, COSActiveLow: ptr(true), COS: "gpio1"}} {
		if a.Equal(b) {
			t.Errorf("%+v equals %+v", a, b)
		}
	}
}

func TestPresetList(t *testing.T) {
	var names []Preset
	for _, p := range Presets() {
		names = append(names, p.Name)
		if p.Title == "" || p.Description == "" {
			t.Errorf("%s has no title or description", p.Name)
		}
		if _, err := Resolve(p.Name, Overrides{}, false); err != nil {
			t.Errorf("%s does not resolve: %v", p.Name, err)
		}
	}
	if !reflect.DeepEqual(names, []Preset{PresetSHARI, PresetURI, PresetMMDVM, PresetNone}) {
		t.Errorf("names = %v", names)
	}
	if _, ok := LookupPreset("x"); ok {
		t.Error("found a preset that does not exist")
	}
}

func TestInterfaceSquelch(t *testing.T) {
	in, _ := Resolve(PresetSHARI, Overrides{}, false)
	// SHARI: carrier present when GPIO2 is high.
	if !in.Squelch().Carrier(0x02) || in.Squelch().Carrier(0x00) {
		t.Error("SHARI squelch")
	}
	uri, _ := Resolve(PresetURI, Overrides{}, false)
	// URI: present when GPIO2 is low.
	if uri.Squelch().Carrier(0x02) || !uri.Squelch().Carrier(0x00) {
		t.Error("URI squelch")
	}
	if uri.Squelch().COS != cm108.SourceGPIO2 {
		t.Error("COS source")
	}
}
