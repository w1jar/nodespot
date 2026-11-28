package node

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
)

type testModem struct {
	mu      sync.Mutex
	st      radio.ModemStatus
	reopens int
}

func (m *testModem) Status() radio.ModemStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *testModem) Reopen() {
	m.mu.Lock()
	m.reopens++
	m.mu.Unlock()
}

func ip(v int) *int { return &v }

func TestMMDVMConfigDefaults(t *testing.T) {
	var nilCfg *MMDVMConfig
	want := MMDVMSettings{Device: "/dev/serial0", Baud: 115200, Duplex: true, ColorCode: 1}
	if got := nilCfg.Resolved(); !got.Equal(want) {
		t.Errorf("nil: %+v", got)
	}
	if got := (&MMDVMConfig{}).Resolved(); !got.Equal(want) || got.AllowTX {
		t.Errorf("empty: %+v", got)
	}
	off := false
	got := (&MMDVMConfig{Device: "/dev/ttyAMA0", Baud: 460800, Duplex: &off, ColorCode: ip(0), RFPower: ip(50),
		ResetGPIO: ip(12), AllowTX: true}).Resolved()
	if got.Device != "/dev/ttyAMA0" || got.Baud != 460800 || got.Duplex || got.ColorCode != 0 ||
		got.RFPower != 50 || got.ResetGPIO == nil || *got.ResetGPIO != 12 || !got.AllowTX {
		t.Errorf("set: %+v", got)
	}
	a, b := got, got
	b.ResetGPIO = ip(12)
	if !a.Equal(b) {
		t.Error("equal reset pins compare different")
	}
	b.ResetGPIO = ip(13)
	if a.Equal(b) {
		t.Error("different reset pins compare equal")
	}
	b.ResetGPIO = nil
	if a.Equal(b) {
		t.Error("a reset pin and none compare equal")
	}
}

func TestMMDVMConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		m    MMDVMConfig
		want string
	}{
		{"defaults", MMDVMConfig{}, ""},
		{"all set", MMDVMConfig{Device: "/dev/ttyAMA0", Baud: 115200, RXFreqHz: 441000000, TXFreqHz: 446000000,
			RXOffsetHz: -500, TXOffsetHz: 500, RFPower: ip(100), ColorCode: ip(15), ResetGPIO: ip(0)}, ""},
		{"relative device", MMDVMConfig{Device: "serial0"}, "serial port"},
		{"device outside /dev", MMDVMConfig{Device: "/etc/passwd"}, "serial port"},
		{"device with dots", MMDVMConfig{Device: "/dev/../etc/passwd"}, "serial port"},
		{"baud", MMDVMConfig{Baud: 9600}, "baud"},
		{"rx freq", MMDVMConfig{RXFreqHz: 1000}, "receive frequency"},
		{"tx freq", MMDVMConfig{TXFreqHz: 2000000000}, "transmit frequency"},
		{"offset", MMDVMConfig{TXOffsetHz: 200000}, "transmit offset"},
		{"vhf on a dual hat", MMDVMConfig{RXFreqHz: 144800000}, "below 420 MHz"},
		{"vhf simplex", MMDVMConfig{RXFreqHz: 144800000, Duplex: bp(false)}, ""},
		{"banned 70 cm", MMDVMConfig{RXFreqHz: 436000000}, "bans"},
		{"banned 2 m", MMDVMConfig{RXFreqHz: 145900000, Duplex: bp(false)}, "bans"},
		{"gap", MMDVMConfig{RXFreqHz: 300000000, Duplex: bp(false)}, "none of the bands"},
		{"offset pushes into the ban", MMDVMConfig{RXFreqHz: 434950000, RXOffsetHz: 100000}, "bans"},
		{"tx default is rx", MMDVMConfig{RXFreqHz: 441000000, TXOffsetHz: -100000}, ""},
		{"power", MMDVMConfig{RFPower: ip(101)}, "RF power"},
		{"color code", MMDVMConfig{ColorCode: ip(16)}, "color code"},
		{"reset gpio", MMDVMConfig{ResetGPIO: ip(-1)}, "reset GPIO"},
	}
	for _, tc := range cases {
		m := tc.m
		c := &Config{Version: ConfigVersion, Station: Station{Radio: RadioConfig{Preset: " MMDVM ", MMDVM: &m}}}
		c.Normalize()
		err := c.Validate()
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if c.Station.Radio.Preset != "mmdvm" {
			t.Errorf("%s: preset %q", tc.name, c.Station.Radio.Preset)
		}
	}
}

// The mmdvm settings load from node.json (the strict decoder knows them).
func TestMMDVMConfigLoads(t *testing.T) {
	var c Config
	raw := `{"version":1,"station":{"radio":{"preset":"mmdvm","mmdvm":{"device":"/dev/serial0","baud":115200,
		"duplex":true,"rxFreqHz":441000000,"txFreqHz":446000000,"colorCode":1,"allowTx":false,"resetGpio":20}}}}`
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if m := c.Station.Radio.MMDVM; m == nil || *m.ResetGPIO != 20 || m.RXFreqHz != 441000000 {
		t.Errorf("mmdvm %+v", m)
	}
}

func mmdvmEngine(t *testing.T, m Modem, changed func()) *Engine {
	t.Helper()
	cfg := quietConfig()
	cfg.Modes = nil
	cfg.Station.Radio = RadioConfig{Preset: "mmdvm"}
	iface, _ := radio.Resolve(radio.PresetMMDVM, radio.Overrides{}, false)
	e, err := New(cfg, Options{Radio: radio.NewFake(), RadioInfo: radio.Info{Interface: iface},
		Modem: m, ModemConfig: cfg.Station.Radio.MMDVM.Resolved(), RadioChanged: changed, Now: time.Now,
		Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) { return modetest.New(n), nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestModemInfo(t *testing.T) {
	m := &testModem{st: radio.ModemStatus{Open: true, Protocol: 1,
		Description: "MMDVM_HS-v1.6.1 20231115 14.7456MHz dual ADF7021 FW by AUTHORS. GitID #7e16099",
		Board:       "MMDVM_HS", Firmware: "1.6.1", BuildDate: "2023-11-15", DuplexCapable: true, Duplex: true,
		Modes: []string{"dmr"}, State: "idle", Flags: 0x04, RXOverflow: true, RXOverflows: 3, Space: [2]int{10, 25},
		Reopens: 2, NAKs: 1, Error: "mmdvm: no answer"}}
	e := mmdvmEngine(t, m, nil)
	if e.Status().RestartRequired || e.RadioInfo().RestartRequired {
		t.Error("restart required with the settings the modem runs")
	}
	if e.Status().Radio.Present {
		t.Error("the sound-chip radio reads as present")
	}
	mi := e.ModemInfo()
	if !mi.Configured || !mi.Running || !mi.Connected || !mi.DetectOnly || mi.Device != "/dev/serial0" ||
		mi.Baud != 115200 || mi.Protocol != 1 || mi.Board != "MMDVM_HS" || mi.Firmware != "1.6.1" ||
		mi.BuildDate != "2023-11-15" || !mi.DuplexCapable || mi.State != "idle" || mi.Flags.Raw != 4 ||
		!mi.Flags.RXOverflow || mi.RXOverflows != 3 || mi.Reopens != 2 || mi.LastError != "mmdvm: no answer" {
		t.Errorf("modem info %+v", mi)
	}
	if err := e.ResetModem(); err != nil || m.reopens != 1 {
		t.Errorf("reset: %v, %d reopens", err, m.reopens)
	}
	s := NewServer(e, ServerOptions{})
	r := do(t, s, OpModemStatus, nil)
	if !r.OK {
		t.Fatal(r.Error)
	}
	b := mustJSON(t, r.Data)
	for _, k := range []string{`"board":"MMDVM_HS"`, `"firmware":"1.6.1"`, `"buildDate":"2023-11-15"`, `"duplexCapable":true`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("modem_status lacks %s: %s", k, b)
		}
	}
	if strings.Contains(strings.ToLower(string(b)), "udid") {
		t.Errorf("modem_status names a unique ID: %s", b)
	}
	if r := do(t, s, OpModemReset, nil); !r.OK || m.reopens != 2 {
		t.Errorf("modem_reset: %+v, %d reopens", r, m.reopens)
	}
}

func TestModemInfoNoModem(t *testing.T) {
	g := newRig(t, quietConfig())
	mi := g.e.ModemInfo()
	if mi.Configured || mi.Running || mi.Connected || mi.Device != "/dev/serial0" {
		t.Errorf("modem info %+v", mi)
	}
	if err := g.e.ResetModem(); err != ErrNoModem {
		t.Errorf("reset: %v", err)
	}
	if r := do(t, NewServer(g.e, ServerOptions{}), OpModemReset, nil); r.OK {
		t.Error("modem_reset with no modem succeeded")
	}
}

func TestMMDVMChangeAsksForRestart(t *testing.T) {
	tests := []struct {
		name string
		edit func(*RadioConfig)
		want bool
	}{
		{"nothing", func(*RadioConfig) {}, false},
		{"defaults spelled out", func(r *RadioConfig) {
			r.MMDVM = &MMDVMConfig{Device: "/dev/serial0", Baud: 115200, ColorCode: ip(1)}
		}, false},
		{"device", func(r *RadioConfig) { r.MMDVM = &MMDVMConfig{Device: "/dev/ttyAMA0"} }, true},
		{"color code", func(r *RadioConfig) { r.MMDVM = &MMDVMConfig{ColorCode: ip(2)} }, true},
		{"allow transmit", func(r *RadioConfig) { r.MMDVM = &MMDVMConfig{AllowTX: true} }, true},
		{"preset", func(r *RadioConfig) { r.Preset = "none" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := make(chan struct{}, 1)
			e := mmdvmEngine(t, &testModem{}, func() { called <- struct{}{} })
			st := e.Config().Station
			tt.edit(&st.Radio)
			if err := e.SaveStation(st); err != nil {
				t.Fatal(err)
			}
			wait := 100 * time.Millisecond
			if tt.want {
				wait = 10 * time.Second
			}
			got := false
			select {
			case <-called:
				got = true
			case <-time.After(wait):
			}
			if got != tt.want || e.Status().RestartRequired != tt.want {
				t.Fatalf("restart asked %v, status %v, want %v", got, e.Status().RestartRequired, tt.want)
			}
		})
	}
}
