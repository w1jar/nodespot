package node

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/w1jar/nodespot/internal/native/mode"
	_ "github.com/w1jar/nodespot/internal/native/mode/all"
)

func modeInfo(t *testing.T, g *rig, name string) ModeInfo {
	t.Helper()
	for _, mi := range g.e.Modes() {
		if mi.Name == name {
			return mi
		}
	}
	t.Fatalf("no %s in the mode list", name)
	return ModeInfo{}
}

// A mode is ready when its required values come from its own settings or
// from the station; the echo test needs nothing.
func TestModesReadiness(t *testing.T) {
	bare := func() *Config { c := quietConfig(); c.Modes = map[string]mode.Settings{}; return c }

	g := newRig(t, bare())
	if mi := modeInfo(t, g, mode.EchoTest); !mi.Configured || len(mi.Missing) != 0 || mi.Saved {
		t.Fatalf("echotest %+v", mi)
	}
	// Station callsign only: EchoLink lacks its password, BM its DMR ID
	// and password, YSF is ready.
	for name, want := range map[string][]string{
		mode.EchoLink: {"password"},
		mode.BM:       {"dmr_id", "password", "address"},
		mode.TGIF:     {"dmr_id", "password"},
		mode.NXDN:     {"nxdn_id"},
		mode.AllStar:  {"node"},
		mode.YSF:      nil,
		mode.M17:      nil,
		mode.P25:      nil,
	} {
		mi := modeInfo(t, g, name)
		if !reflect.DeepEqual(mi.Missing, want) || mi.Configured != (len(want) == 0) {
			t.Errorf("%s: configured=%v missing=%v, want missing %v", name, mi.Configured, mi.Missing, want)
		}
	}

	// With the station IDs, the ID-only modes are ready.
	c := bare()
	c.Station.DMRID, c.Station.NXDNID = 1234567, 12345
	g = newRig(t, c)
	for _, name := range []string{mode.NXDN, mode.P25, mode.YSF, mode.M17, mode.EchoTest} {
		if mi := modeInfo(t, g, name); !mi.Configured {
			t.Errorf("%s not ready with the station IDs: %v", name, mi.Missing)
		}
	}
	// TGIF with the station DMR ID still lacks its hotspot password, until one is stored.
	if mi := modeInfo(t, g, mode.TGIF); mi.Configured || !reflect.DeepEqual(mi.Missing, []string{"password"}) {
		t.Errorf("tgif without a password %+v", mi)
	}
	if mi := modeInfo(t, g, mode.BM); mi.Configured || !reflect.DeepEqual(mi.Missing, []string{"password", "address"}) {
		t.Errorf("bm %+v", mi)
	}

	// With no station callsign, the callsign is missing where it is needed.
	c = bare()
	c.Station.Callsign = ""
	g = newRig(t, c)
	for _, name := range []string{mode.YSF, mode.M17, mode.P25, mode.NXDN, mode.EchoLink} {
		mi := modeInfo(t, g, name)
		if mi.Configured || len(mi.Missing) == 0 || mi.Missing[0] != "callsign" {
			t.Errorf("%s with no callsign anywhere: %+v", name, mi)
		}
	}
	if mi := modeInfo(t, g, mode.EchoTest); !mi.Configured {
		t.Error("echotest needs the station callsign")
	}

	// A mode's own value counts, and saved says settings exist.
	c = bare()
	c.Station.Callsign = ""
	c.Modes[mode.YSF] = mode.Settings{Values: map[string]string{"callsign": "N1CALL"}}
	g = newRig(t, c)
	if mi := modeInfo(t, g, mode.YSF); !mi.Configured || !mi.Saved {
		t.Errorf("ysf with its own callsign: %+v", mi)
	}
}

// The default reflector setting of an old config seeds the tune when the
// reflector list names its address, and is dropped from the config.
func TestMigrateDefaultReflector(t *testing.T) {
	reflectors := []mode.Target{{ID: "10200", Host: "192.0.2.9", Port: 41000}, {ID: "10300", Host: "192.0.2.12"}}
	cfg := func() *Config {
		c := quietConfig()
		c.Station.DMRID = 1234567
		c.Modes[mode.P25] = mode.Settings{Values: map[string]string{"address": "192.0.2.9:41000"}, Reflectors: reflectors}
		c.Modes[mode.NXDN] = mode.Settings{Values: map[string]string{"nxdn_id": "12345", "address": "198.51.100.1"}}
		c.Modes[mode.M17] = mode.Settings{Values: map[string]string{"address": "192.0.2.12"}, Reflectors: reflectors}
		c.Station.Active = &Selection{Mode: mode.P25}
		return c
	}
	g := newRig(t, cfg())
	if c := g.fake(mode.P25).Connects(); len(c) != 1 || c[0].ID != "10200" || c[0].Host != "192.0.2.9" {
		t.Fatalf("p25 connects %+v", c)
	}
	for _, n := range []string{mode.P25, mode.NXDN, mode.M17} {
		if _, ok := g.e.Config().Modes[n].Values["address"]; ok {
			t.Errorf("%s still has the old default reflector", n)
		}
	}
	// An address no reflector entry names has no talkgroup to remember;
	// an M17 target needs a module.
	for _, n := range []string{mode.NXDN, mode.M17} {
		if tn, ok := g.e.tuneOf(n); ok {
			t.Errorf("%s tune %+v from an address alone", n, tn)
		}
	}
	if v := g.e.Config().Modes[mode.NXDN].Values["nxdn_id"]; v != "12345" {
		t.Errorf("nxdn_id lost: %q", v)
	}

	// A remembered tune (here, an explicit unlink) wins over the old value.
	tp := filepath.Join(t.TempDir(), "tune.json")
	if err := os.WriteFile(tp, []byte(`{"version":1,"modes":{"p25":{"unlinked":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g = newRigAt(t, cfg(), tp)
	if c := g.fake(mode.P25).Connects(); len(c) != 0 {
		t.Fatalf("the old default overrode the remembered unlink: %+v", c)
	}
	if _, ok := g.e.Config().Modes[mode.P25].Values["address"]; ok {
		t.Error("the old key was kept")
	}
}
