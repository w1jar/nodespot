package node

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{name: "ID tail", mod: func(c *Config) { c.Station.ID.TailWindowS = 601 }, want: "tail window"},
		{name: "ID quiet", mod: func(c *Config) { c.Station.ID.EndQuietS = -1 }, want: "quiet time"},
		{name: "ID wpm", mod: func(c *Config) { c.Station.ID.WPM = 61 }, want: "WPM"},
		{name: "ID pitch low", mod: func(c *Config) { c.Station.ID.PitchHz = 199 }, want: "pitch"},
		{name: "ID pitch high", mod: func(c *Config) { c.Station.ID.PitchHz = 2001 }, want: "pitch"},
		{name: "ID pitch ok", mod: func(c *Config) { c.Station.ID.PitchHz = 2000 }},
		{name: "ID level low", mod: func(c *Config) { c.Station.ID.LevelDb = -61 }, want: "level"},
		{name: "ID level high", mod: func(c *Config) { c.Station.ID.LevelDb = 1 }, want: "level"},
		{name: "ID level ok", mod: func(c *Config) { c.Station.ID.LevelDb = -60 }},
		{name: "hang", mod: func(c *Config) { c.Station.Advanced.HangMS = 10001 }, want: "hang"},
		{name: "tx timeout", mod: func(c *Config) { c.Station.Advanced.TxTimeoutS = 1801 }, want: "timeout"},
		{name: "tx timeout max", mod: func(c *Config) { c.Station.Advanced.TxTimeoutS = 1800 }},
		{name: "AGC target low", mod: func(c *Config) { c.Station.Advanced.AGCTarget = -41 }, want: "AGC: target"},
		{name: "AGC target high", mod: func(c *Config) { c.Station.Advanced.AGCTarget = -5 }, want: "AGC: target"},
		{name: "AGC target ok", mod: func(c *Config) { c.Station.Advanced.AGCTarget = -40 }},
		{name: "AGC max negative", mod: func(c *Config) { c.Station.Advanced.AGCMax = -1 }, want: "maximum gain"},
		{name: "AGC max high", mod: func(c *Config) { c.Station.Advanced.AGCMax = 31 }, want: "maximum gain"},
		{name: "AGC max ok", mod: func(c *Config) { c.Station.Advanced.AGCMax = 30 }},
		{name: "rx gain low", mod: func(c *Config) { c.Station.Advanced.RxGainDB = -20.5 }, want: "receive gain"},
		{name: "rx gain high", mod: func(c *Config) { c.Station.Advanced.RxGainDB = 21 }, want: "receive gain"},
		{name: "rx gain min", mod: func(c *Config) { c.Station.Advanced.RxGainDB = -20 }},
		{name: "rx gain max", mod: func(c *Config) { c.Station.Advanced.RxGainDB = 20 }},
		{name: "tx gain low", mod: func(c *Config) { c.Station.Advanced.TxGainDB = -21 }, want: "transmit gain"},
		{name: "tx gain high", mod: func(c *Config) { c.Station.Advanced.TxGainDB = 20.1 }, want: "transmit gain"},
		{name: "tx gain ok", mod: func(c *Config) { c.Station.Advanced.TxGainDB = 7.5 }},
		{name: "gain nan", mod: func(c *Config) { c.Station.Advanced.TxGainDB = math.NaN() }, want: "transmit gain"},
		{name: "rx on delay negative", mod: func(c *Config) { c.Station.Advanced.RxOnDelayMS = -1 }, want: "receive-on delay"},
		{name: "rx on delay high", mod: func(c *Config) { c.Station.Advanced.RxOnDelayMS = 1001 }, want: "receive-on delay"},
		{name: "rx on delay max", mod: func(c *Config) { c.Station.Advanced.RxOnDelayMS = 1000 }},
		{name: "tx off delay negative", mod: func(c *Config) { c.Station.Advanced.TxOffDelayMS = -1 }, want: "transmit-off delay"},
		{name: "tx off delay high", mod: func(c *Config) { c.Station.Advanced.TxOffDelayMS = 2001 }, want: "transmit-off delay"},
		{name: "tx off delay max", mod: func(c *Config) { c.Station.Advanced.TxOffDelayMS = 2000 }},
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
	p := paramsFor(&c, false)
	if p.hang != DefaultHang || p.txTimeout != DefaultTxTimeout ||
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

func TestAudioDefaults(t *testing.T) {
	var c Config
	for _, sa818 := range []bool{false, true} {
		p := paramsFor(&c, sa818)
		if p.rxGainDb != 0 || p.txGainDb != 0 || p.preEmph || p.deEmph || p.rxOnDelay != 0 || p.hangTail != 0 {
			t.Fatalf("defaults = %+v", p)
		}
		if p.plFilter != !sa818 {
			t.Fatalf("PL filter with sa818=%v: %v", sa818, p.plFilter)
		}
	}
	c.Station.Advanced = Advanced{PLFilter: bp(true), PreEmphasis: true, DeEmphasis: true, RxGainDB: 3, TxGainDB: -4, RxOnDelayMS: 50, TxOffDelayMS: 300}
	p := paramsFor(&c, true)
	if !p.plFilter || !p.preEmph || !p.deEmph || p.rxGainDb != 3 || p.txGainDb != -4 ||
		p.rxOnDelay != 50*time.Millisecond || p.hangTail != 300*time.Millisecond {
		t.Fatalf("params = %+v", p)
	}
}

// The SA818 filter flags default to the settings the node has always
// programmed (all on, AT+SETFILTER=1,1,1), so an unchanged config sends the
// same commands as before.
func TestSA818Filters(t *testing.T) {
	r := RadioConfig{FrequencyMHz: 446.1}
	s, err := r.SA818()
	if err != nil {
		t.Fatal(err)
	}
	cmds, _ := s.Commands()
	if got := cmds[2]; got != "AT+SETFILTER=1,1,1" {
		t.Fatalf("default filter command %q", got)
	}
	r.SA818Filter = &SA818Filter{Emphasis: bp(false), LowPass: bp(false)}
	s, _ = r.SA818()
	cmds, _ = s.Commands()
	if got := cmds[2]; got != "AT+SETFILTER=0,1,0" {
		t.Fatalf("filter command %q, want emphasis 0, high-pass 1, low-pass 0", got)
	}
	// Changing only the filters is not a channel change: nothing is programmed.
	old := RadioConfig{FrequencyMHz: 446.1}
	if channelChanged(old, r) {
		t.Fatal("a filter change counts as a channel change")
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"sa818Filter":{"emphasis":false,"lowPass":false}`) {
		t.Fatalf("json %s", b)
	}
}

func TestOldCourtesyToneKeyIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	body := `{"version":1,"station":{"callsign":"N0CALL","advanced":{"hangMs":400,"courtesyTone":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("a config with courtesyTone must load: %v", err)
	}
	if c.Station.Advanced.HangMS != 400 {
		t.Fatalf("hangMs = %d", c.Station.Advanced.HangMS)
	}
}
