package node

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
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
		{name: "rx AGC target low", mod: func(c *Config) { c.Station.Advanced.RxAGCTarget = -41 }, want: "radio to network target"},
		{name: "rx AGC target high", mod: func(c *Config) { c.Station.Advanced.RxAGCTarget = -5 }, want: "radio to network target"},
		{name: "rx AGC target ok", mod: func(c *Config) { c.Station.Advanced.RxAGCTarget = -40 }},
		{name: "tx AGC target low", mod: func(c *Config) { c.Station.Advanced.TxAGCTarget = -41 }, want: "network to radio target"},
		{name: "tx AGC target high", mod: func(c *Config) { c.Station.Advanced.TxAGCTarget = -5 }, want: "network to radio target"},
		{name: "tx AGC target ok", mod: func(c *Config) { c.Station.Advanced.TxAGCTarget = -6 }},
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
		!p.modeAGC || !p.radioAGC || p.id.interval != DefaultIDInterval {
		t.Fatalf("defaults = %+v", p)
	}
	if p.id.on {
		t.Fatal("ID on with no callsign")
	}
	for _, a := range []audio.AGCParams{p.rxAGC, p.txAGC} {
		if a.TargetDb != DefaultAGCTargetDb || a.MaxGainDb != DefaultAGCMaxDb {
			t.Fatalf("AGC = %+v", a)
		}
	}
	// Each direction has its own target; the maximum gain is shared.
	c.Station.Advanced = Advanced{RxAGCTarget: -30, TxAGCTarget: -12, AGCMax: 10, RadioAGC: bp(false)}
	p = paramsFor(&c, false)
	if p.rxAGC.TargetDb != -30 || p.txAGC.TargetDb != -12 || p.rxAGC.MaxGainDb != 10 || p.txAGC.MaxGainDb != 10 || p.radioAGC || !p.modeAGC {
		t.Fatalf("rx %+v tx %+v radio %v mode %v", p.rxAGC, p.txAGC, p.radioAGC, p.modeAGC)
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
		if p.rxGainDb != 0 || p.txGainDb != 0 || p.flatAudio || p.rxOnDelay != 0 || p.hangTail != 0 {
			t.Fatalf("defaults = %+v", p)
		}
		if p.plFilter != !sa818 {
			t.Fatalf("PL filter with sa818=%v: %v", sa818, p.plFilter)
		}
	}
	c.Station.Advanced = Advanced{PLFilter: bp(true), FlatAudio: true, RxGainDB: 3, TxGainDB: -4, RxOnDelayMS: 50, TxOffDelayMS: 300}
	p := paramsFor(&c, true)
	if !p.plFilter || !p.flatAudio || p.rxGainDb != 3 || p.txGainDb != -4 ||
		p.rxOnDelay != 50*time.Millisecond || p.hangTail != 300*time.Millisecond {
		t.Fatalf("params = %+v", p)
	}
}

// The SA818 filter flags: 1 on the wire bypasses a filter (the SA818
// programming manual), so "on" goes out as 0. With nothing stored the
// emphasis is bypassed and both other filters are on: AT+SETFILTER=1,0,0.
func TestSA818Filters(t *testing.T) {
	r := RadioConfig{FrequencyMHz: 446.1}
	s, err := r.SA818()
	if err != nil {
		t.Fatal(err)
	}
	cmds, _ := s.Commands()
	if got := cmds[2]; got != "AT+SETFILTER=1,0,0" {
		t.Fatalf("default filter command %q", got)
	}
	r.SA818Filter = &SA818Filter{Emphasis: bp(true), LowPass: bp(false)}
	s, _ = r.SA818()
	cmds, _ = s.Commands()
	if got := cmds[2]; got != "AT+SETFILTER=0,0,1" {
		t.Fatalf("filter command %q, want emphasis on (0), high-pass on (0), low-pass bypassed (1)", got)
	}
	// Changing only the filters reprograms the module, like any module value.
	old := RadioConfig{FrequencyMHz: 446.1}
	if !channelChanged(old, r) {
		t.Fatal("a filter change does not count as a change")
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"sa818Filter":{"emphasis":true,"lowPass":false}`) {
		t.Fatalf("json %s", b)
	}
}

func TestSA818VolumeAndTail(t *testing.T) {
	r := RadioConfig{FrequencyMHz: 446.1}
	s, err := r.SA818()
	if err != nil || s.Volume != 8 || s.Tail != nil {
		t.Fatalf("defaults: %+v, %v", s, err)
	}
	cmds, _ := s.Commands()
	if len(cmds) != 3 {
		t.Fatalf("default commands %q send a tail command", cmds)
	}
	vol := 5
	r.SA818Volume, r.SA818Tail = &vol, bp(false)
	s, err = r.SA818()
	if err != nil || s.Volume != 5 || s.Tail == nil || *s.Tail {
		t.Fatalf("set: %+v, %v", s, err)
	}
	cmds, _ = s.Commands()
	if cmds[1] != "AT+DMOSETVOLUME=5" || cmds[3] != "AT+SETTAIL=0" {
		t.Fatalf("commands %q", cmds)
	}
	for _, v := range []int{0, 9} {
		c := quietConfig()
		c.Station.Radio = RadioConfig{FrequencyMHz: 446.1, SA818Volume: &v}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "volume must be 1 to 8") {
			t.Errorf("volume %d: err = %v", v, err)
		}
	}
	// Each module value counts as a change; an unset value equals its default.
	base := RadioConfig{FrequencyMHz: 446.1}
	eight := 8
	same := RadioConfig{FrequencyMHz: 446.1, SA818Volume: &eight}
	if channelChanged(base, same) {
		t.Fatal("volume 8 differs from unset")
	}
	for name, c := range map[string]RadioConfig{
		"volume":  {FrequencyMHz: 446.1, SA818Volume: &vol},
		"tail":    {FrequencyMHz: 446.1, SA818Tail: bp(true)},
		"filters": {FrequencyMHz: 446.1, SA818Filter: &SA818Filter{LowPass: bp(false)}},
	} {
		if !channelChanged(base, c) {
			t.Errorf("%s change is not a change", name)
		}
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"sa818Volume":5`) || !strings.Contains(string(b), `"sa818Tail":false`) {
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

// loadAdvanced loads a config whose station.advanced is adv.
func loadAdvanced(t *testing.T, adv string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.json")
	body := `{"version":1,"station":{"callsign":"N0CALL","advanced":` + adv + `}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("%s: %v", adv, err)
	}
	return c
}

// The one AGC target of earlier builds becomes both targets, unless a new
// key is already there; only the new keys are written back.
func TestMigrateAGCTarget(t *testing.T) {
	a := loadAdvanced(t, `{"agcTargetDb":-30,"agcMaxDb":12}`).Station.Advanced
	if a.RxAGCTarget != -30 || a.TxAGCTarget != -30 || a.AGCMax != 12 {
		t.Fatalf("migrated %+v", a)
	}
	a = loadAdvanced(t, `{"agcTargetDb":-30,"txAgcTargetDb":-10}`).Station.Advanced
	if a.RxAGCTarget != -30 || a.TxAGCTarget != -10 {
		t.Fatalf("new key must win: %+v", a)
	}
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), `"agcTargetDb"`) || !strings.Contains(string(b), `"rxAgcTargetDb":-30`) || !strings.Contains(string(b), `"txAgcTargetDb":-10`) {
		t.Fatalf("saved %s", b)
	}
	// An out of range old target is reported, not dropped.
	path := filepath.Join(t.TempDir(), "node.json")
	_ = os.WriteFile(path, []byte(`{"version":1,"station":{"advanced":{"agcTargetDb":-50}}}`), 0o600)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("bad old target: %v", err)
	}
}

// Software pre- or de-emphasis of earlier builds becomes flat audio, which
// does both.
func TestMigrateEmphasis(t *testing.T) {
	for adv, want := range map[string]bool{
		`{"preEmphasis":true}`:                     true,
		`{"deEmphasis":true}`:                      true,
		`{"preEmphasis":true,"deEmphasis":true}`:   true,
		`{"preEmphasis":false,"deEmphasis":false}`: false,
		`{"deEmphasis":true,"flatAudio":false}`:    false,
		`{"flatAudio":true}`:                       true,
		`{}`:                                       false,
	} {
		a := loadAdvanced(t, adv).Station.Advanced
		if a.FlatAudio != want {
			t.Errorf("%s: flatAudio %v, want %v", adv, a.FlatAudio, want)
		}
		b, _ := json.Marshal(a)
		if strings.Contains(string(b), "Emphasis") {
			t.Errorf("%s: saved %s", adv, b)
		}
	}
	// A mix of every retired key still loads.
	a := loadAdvanced(t, `{"courtesyTone":true,"agcTargetDb":-20,"deEmphasis":true,"hangMs":300}`).Station.Advanced
	if !a.FlatAudio || a.RxAGCTarget != -20 || a.HangMS != 300 {
		t.Fatalf("%+v", a)
	}
}

// Both AGCs are on by default.
func TestAGCDefaultsBothOn(t *testing.T) {
	rx, tx, modeOn, radioOn := Advanced{}.AGC()
	if !modeOn || !radioOn || rx.TargetDb != -22 || tx.TargetDb != -22 {
		t.Fatalf("rx %+v tx %+v mode %v radio %v", rx, tx, modeOn, radioOn)
	}
}
