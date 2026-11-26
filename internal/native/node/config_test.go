package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/native/mode"
	_ "github.com/w1jar/nodespot/internal/native/mode/all"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

func TestParseTone(t *testing.T) {
	cases := []struct {
		in   string
		want sa818.Tone
		bad  bool
	}{
		{in: "", want: sa818.Tone{}},
		{in: "none", want: sa818.Tone{}},
		{in: "100.0", want: sa818.Tone{Kind: sa818.ToneCTCSS, CTCSSHz: 100}},
		{in: "D023N", want: sa818.Tone{Kind: sa818.ToneDCS, DCSCode: 23}},
		{in: "d754i", want: sa818.Tone{Kind: sa818.ToneDCS, DCSCode: 754, DCSInverted: true}},
		{in: "101.3", bad: true},
		{in: "D089N", bad: true},
		{in: "loud", bad: true},
	}
	for _, c := range cases {
		got, err := ParseTone(c.in)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("ParseTone(%q) = %+v, %v", c.in, got, err)
		}
	}
}

func TestValidate(t *testing.T) {
	sq := 9
	vol := 1.5
	cases := []struct {
		name string
		mod  func(*Config)
		want string // "" = valid
	}{
		{name: "empty", mod: func(*Config) {}},
		{name: "good", mod: func(c *Config) {
			c.Station.Callsign = "N0CALL"
			c.Station.Radio.FrequencyMHz = 446.1
			c.Station.Radio.Tone = "100.0"
			c.Modes[mode.AllStar] = mode.Settings{Values: map[string]string{"node": "1999", "password": "x"}}
		}},
		{name: "callsign", mod: func(c *Config) { c.Station.Callsign = "HELLO" }, want: "callsign"},
		{name: "frequency", mod: func(c *Config) { c.Station.Radio.FrequencyMHz = 300 }, want: "radio"},
		{name: "tone", mod: func(c *Config) { c.Station.Radio.Tone = "99" }, want: "tone"},
		{name: "squelch", mod: func(c *Config) { c.Station.Radio.Squelch = &sq }, want: "squelch"},
		{name: "volume", mod: func(c *Config) { c.Station.Radio.TxVolume = &vol }, want: "transmit volume"},
		{name: "ID interval", mod: func(c *Config) { c.Station.ID.IntervalS = 900 }, want: "10 minutes"},
		{name: "voice path", mod: func(c *Config) { c.Station.ID.VoiceFile = "id.wav" }, want: "absolute"},
		{name: "unknown mode", mod: func(c *Config) { c.Modes["fm"] = mode.Settings{} }, want: `"fm"`},
		{name: "bad mode setting", mod: func(c *Config) {
			c.Modes[mode.AllStar] = mode.Settings{Values: map[string]string{"node": "12"}}
		}, want: "node"},
		{name: "dmr id max", mod: func(c *Config) { c.Station.DMRID = 16777215 }},
		{name: "dmr id", mod: func(c *Config) { c.Station.DMRID = 16777216 }, want: "DMR ID"},
		{name: "nxdn id max", mod: func(c *Config) { c.Station.NXDNID = 65535 }},
		{name: "nxdn id", mod: func(c *Config) { c.Station.NXDNID = 65536 }, want: "NXDN ID"},
		{name: "mode without id", mod: func(c *Config) {
			c.Station.Callsign = "N0CALL"
			c.Modes[mode.NXDN] = mode.Settings{}
		}, want: "NXDN ID is required"},
		{name: "mode id from station", mod: func(c *Config) {
			c.Station.Callsign = "N0CALL"
			c.Station.NXDNID = 12345
			c.Modes[mode.NXDN] = mode.Settings{}
		}},
		{name: "mode callsign from station", mod: func(c *Config) {
			c.Station.Callsign = "N0CALL"
			c.Modes[mode.YSF] = mode.Settings{}
		}},
		{name: "mode without callsign", mod: func(c *Config) {
			c.Modes[mode.YSF] = mode.Settings{}
		}, want: "Callsign is required"},
		{name: "old default reflector", mod: func(c *Config) {
			c.Station.Callsign = "N0CALL"
			c.Station.DMRID = 1234567
			c.Modes[mode.P25] = mode.Settings{Values: map[string]string{"address": "p25.example.net:41000"}}
		}},
		{name: "active", mod: func(c *Config) { c.Station.Active = &Selection{Mode: "fm"} }, want: "active mode"},
		{name: "several", mod: func(c *Config) {
			c.Station.Callsign = "HELLO"
			c.Station.Advanced.HangMS = -1
		}, want: "hang"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Version: 1, Modes: map[string]mode.Settings{}}
			c.mod(cfg)
			cfg.Normalize()
			err := cfg.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	// First boot: no file is normal.
	c, err := LoadConfig(path)
	if err != nil || c.Station.Callsign != "" || len(c.Modes) != 0 {
		t.Fatalf("missing file: %+v, %v", c, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("empty file: %v", err)
	}

	c.Station.Callsign = "n0call"
	c.Station.Active = &Selection{Mode: mode.AllStar, Target: &mode.Target{ID: "2000"}}
	c.Modes[mode.AllStar] = mode.Settings{Values: map[string]string{"node": "1999", "password": "secret"}}
	c.Normalize()
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Station.Callsign != "N0CALL" || got.Station.Active.Target.ID != "2000" ||
		got.Modes[mode.AllStar].Values["password"] != "secret" {
		t.Fatalf("round trip: %+v", got)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".node-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}

	for name, body := range map[string]string{
		"syntax":  "{",
		"unknown": `{"version":1,"station":{"colour":"red"}}`,
		"newer":   `{"version":99}`,
		"invalid": `{"version":1,"station":{"callsign":"HELLO"}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDefaults(t *testing.T) {
	var c Config
	p := paramsFor(&c)
	if p.hang != DefaultHang || p.txTimeout != DefaultTxTimeout || !p.courtesy ||
		!p.modeAGC || p.radioAGC || p.id.interval != DefaultIDInterval {
		t.Fatalf("defaults = %+v", p)
	}
	if p.id.on {
		t.Fatal("ID on with no callsign")
	}
	if p.agc.TargetDb != DefaultAGCTargetDb || p.agc.MaxGainDb != DefaultAGCMaxDb {
		t.Fatalf("AGC = %+v", p.agc)
	}
}

// An old config with a default reflector and a target in the active
// selection still loads.
func TestLoadOldConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	old := `{"version":1,"station":{"callsign":"N0CALL","active":{"mode":"p25","target":{"id":"10100"}}},
	 "modes":{"p25":{"values":{"address":"192.0.2.9:41000"}},"nxdn":{"values":{"nxdn_id":"12345","address":"192.0.2.10"}},"m17":{"values":{"address":"192.0.2.11:17000"}}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Station.Active; a == nil || a.Target == nil || a.Target.ID != "10100" {
		t.Fatalf("active %+v", a)
	}
}
