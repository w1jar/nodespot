package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

func bp(v bool) *bool { return &v }

// A node.json written before presets existed still loads, and a missing
// preset means the SHARI when one is detected and none otherwise.
func TestLegacyRadioConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	old := `{"version":1,"station":{"callsign":"N0CALL","radio":{"frequencyMHz":446.1,"tone":"100.0","rxVolume":0.5,` +
		`"pttGpio":4,"pttActiveLow":true,"cosActiveLow":true,"sa818Port":"/dev/ttyUSB1"},"id":{},"advanced":{}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Station.Radio
	if r.Preset != "" || r.PTTGPIO != 4 || r.PTTActiveLow == nil || !*r.PTTActiveLow || r.COSActiveLow == nil || !*r.COSActiveLow {
		t.Fatalf("radio = %+v", r)
	}
	shari, err := r.Interface(true)
	if err != nil || shari.Preset != radio.PresetSHARI || shari.PTTPin != 4 || !shari.PTTActiveLow || !shari.COSActiveLow || !shari.SA818 {
		t.Fatalf("with a SHARI: %+v, %v", shari, err)
	}
	none, err := r.Interface(false)
	if err != nil || none.Preset != radio.PresetNone || none.Audio {
		t.Fatalf("without: %+v, %v", none, err)
	}
	// The wiring keeps the SA818 port.
	if r.Wiring().SA818Port != "/dev/ttyUSB1" {
		t.Error("SA818 port lost")
	}
}

func TestRadioConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	c := &Config{Version: ConfigVersion, Station: Station{Callsign: "N0CALL", Radio: RadioConfig{
		Preset: " URI ", PTTGPIO: 4, PTTActiveLow: bp(true), COSSource: "GPIO1", COSActiveLow: bp(false),
		CTCSSSource: "gpio2", CTCSSActiveLow: bp(true), SA818Enabled: bp(false),
	}}, Modes: map[string]mode.Settings{}}
	c.Normalize()
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{`"preset": "uri"`, `"pttGpio": 4`, `"cosSource": "gpio1"`, `"cosActiveLow": false`, `"ctcssSource": "gpio2"`, `"sa818": false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lacks %s:\n%s", want, b)
		}
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got.Station.Radio, c.Station.Radio) {
		t.Fatalf("round trip: %+v, want %+v", got.Station.Radio, c.Station.Radio)
	}
	iface, err := got.Station.Radio.Interface(false)
	want := radio.Interface{Preset: radio.PresetURI, Audio: true, PTTPin: 4, PTTActiveLow: true, COS: "gpio1",
		CTCSS: "gpio2", CTCSSActiveLow: true}
	if err != nil || iface != want {
		t.Fatalf("interface = %+v, %v; want %+v", iface, err, want)
	}
	// Unset overrides stay out of the file.
	c = &Config{Version: ConfigVersion, Station: Station{Radio: RadioConfig{Preset: "shari"}}}
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	for _, no := range []string{"pttGpio", "cosSource", "pttActiveLow", "sa818\""} {
		if strings.Contains(string(b), no) {
			t.Errorf("file has %s:\n%s", no, b)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestValidateRadioPreset(t *testing.T) {
	cases := []struct {
		name string
		r    RadioConfig
		want string
	}{
		{"none set", RadioConfig{}, ""},
		{"shari", RadioConfig{Preset: "shari"}, ""},
		{"uri with pins", RadioConfig{Preset: "uri", PTTGPIO: 1, COSSource: "gpio4", CTCSSSource: "gpio2"}, ""},
		{"none ignores pins", RadioConfig{Preset: "none", PTTGPIO: 1}, ""},
		{"unknown preset", RadioConfig{Preset: "pi"}, "unknown radio preset"},
		{"bad pin", RadioConfig{Preset: "uri", PTTGPIO: 7}, "PTT GPIO"},
		{"bad cos", RadioConfig{Preset: "uri", COSSource: "volume"}, "COS source"},
		{"ptt on the COS pin", RadioConfig{Preset: "uri", PTTGPIO: 2}, "cannot be both"},
		{"ctcss always", RadioConfig{Preset: "uri", CTCSSSource: "always"}, "CTCSS source"},
	}
	for _, tc := range cases {
		c := &Config{Version: ConfigVersion, Station: Station{Radio: tc.r}}
		c.Normalize()
		err := c.Validate()
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func openInfo(preset radio.Preset, absent bool) radio.Info {
	iface, _ := radio.Resolve(preset, radio.Overrides{}, true)
	return radio.Info{Present: !absent && iface.Audio, Interface: iface, Detected: true, Chip: "CM119A", USBID: "0d8c:013a", Product: "USB Audio Device", SHARI: preset == radio.PresetSHARI}
}

func newRadioEngine(t *testing.T, cfg *Config, info radio.Info, prog func(radio.Config, sa818.Settings) error) *Engine {
	t.Helper()
	cfg.Modes = nil
	e, err := New(cfg, Options{
		Radio: radio.NewFake(), RadioInfo: info, Now: time.Now, Program: prog,
		Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) { return modetest.New(n), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestStatusAndRadioInfo(t *testing.T) {
	cfg := quietConfig()
	cfg.Station.Radio.Preset = "uri"
	e := newRadioEngine(t, cfg, openInfo(radio.PresetURI, false), nil)
	st := e.Status().Radio
	if !st.Present || st.Preset != "uri" || st.Chip != "CM119A" || st.USBID != "0d8c:013a" || st.SA818 {
		t.Fatalf("status radio = %+v", st)
	}
	d := e.RadioInfo()
	if d.Preset != "uri" || len(d.Presets) != 4 || d.Interface.PTTPin != 3 || !d.Interface.COSActiveLow || d.RestartRequired || d.Resolved == nil {
		t.Fatalf("detail = %+v", d)
	}
	// Choosing another preset is a pending change.
	st2 := e.Config().Station
	st2.Radio.Preset = "shari"
	if err := e.SaveStation(st2); err != nil {
		t.Fatal(err)
	}
	d = e.RadioInfo()
	if !d.RestartRequired || d.Resolved.Preset != radio.PresetSHARI || d.Interface.Preset != radio.PresetURI {
		t.Fatalf("after saving: %+v", d)
	}

	// A radioless node reports none, with the chip it found.
	e = newRadioEngine(t, quietConfig(), openInfo(radio.PresetNone, true), nil)
	if st := e.Status().Radio; st.Present || st.Preset != "none" || !st.Detected || st.Chip != "CM119A" {
		t.Fatalf("none status = %+v", st)
	}
	// With no info at all the node is the legacy SHARI.
	e = newRadioEngine(t, quietConfig(), radio.Info{}, nil)
	if st := e.Status().Radio; !st.Present || st.Preset != "shari" || !st.SA818 {
		t.Fatalf("legacy status = %+v", st)
	}
}

// Only an interface with an SA818 is programmed.
func TestSA818OnlyWhenThePresetHasOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		preset radio.Preset
		want   int
	}{{"shari", radio.PresetSHARI, 2}, {"uri", radio.PresetURI, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			cfg := quietConfig()
			cfg.Station.Radio = RadioConfig{Preset: string(tc.preset), FrequencyMHz: 446.1}
			e := newRadioEngine(t, cfg, openInfo(tc.preset, false), func(radio.Config, sa818.Settings) error { calls++; return nil })
			_ = e.ProgramIfChanged()
			err := e.ProgramRadio()
			if tc.want == 0 && (err == nil || !strings.Contains(err.Error(), "no SA818")) {
				t.Errorf("ProgramRadio err = %v", err)
			}
			if tc.want == 2 && err != nil {
				t.Errorf("ProgramRadio err = %v", err)
			}
			// Saving a new channel programs it only with an SA818.
			st := e.Config().Station
			st.Radio.FrequencyMHz = 446.2
			if err := e.SaveStation(st); err != nil {
				t.Fatal(err)
			}
			if tc.want == 2 {
				tc.want = 3
			}
			if calls != tc.want {
				t.Errorf("programmed %d times, want %d", calls, tc.want)
			}
		})
	}
}

func TestRadioInfoOp(t *testing.T) {
	_, s, _ := ctlRig(t)
	r := do(t, s, OpRadioInfo, nil)
	if !r.OK {
		t.Fatal(r.Error)
	}
	var d RadioDetail
	if err := json.Unmarshal(mustJSON(t, r.Data), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Presets) != 4 || d.Interface.Preset != radio.PresetSHARI {
		t.Fatalf("detail = %+v", d)
	}
}
