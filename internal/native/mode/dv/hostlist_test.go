package dv

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// The master fields of BrandMeister and DMR carry a host list for the UI's
// picker; TGIF and the others do not. The saved value stays plain text.
func TestHostListFields(t *testing.T) {
	want := map[string]string{mode.BM: mode.HostListBM, mode.DMR: mode.HostListDMROther}
	for _, sp := range mode.Specs() {
		for _, f := range sp.Fields {
			if f.Key != KeyAddress {
				if f.HostList != "" {
					t.Errorf("%s.%s has a host list", sp.Name, f.Key)
				}
				continue
			}
			if f.HostList != want[sp.Name] {
				t.Errorf("%s address: HostList = %q, want %q", sp.Name, f.HostList, want[sp.Name])
			}
		}
	}
	sp, _ := mode.Lookup(mode.BM)
	b, err := json.Marshal(sp.Fields)
	if err != nil || !strings.Contains(string(b), `"hostList":"bm"`) {
		t.Errorf("fields JSON = %s (%v)", b, err)
	}
	sp, _ = mode.Lookup(mode.TGIF)
	if b, _ := json.Marshal(sp.Fields); strings.Contains(string(b), "hostList") {
		t.Errorf("TGIF fields JSON has a hostList: %s", b)
	}
}

// A picked BrandMeister master is a bare host (the engine uses port 54006);
// a typed host:port is kept. A DMR master from the list carries its port.
func TestMasterAddressPorts(t *testing.T) {
	cases := []struct {
		mode, addr string
		host       string
		port       int
	}{
		{mode.BM, "bm-master.example.net", "bm-master.example.net", 0},
		{mode.BM, "bm-master.example.net:54007", "bm-master.example.net", 54007},
		{mode.DMR, "master.example.net:62031", "master.example.net", 62031},
		{mode.DMR, "master.example.net", "master.example.net", 0},
		{mode.TGIF, "", "", 0},
	}
	for _, c := range cases {
		d, _ := lookupDef(c.mode)
		cfg, _, err := d.netConfig(mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: map[string]string{KeyAddress: c.addr}})
		if err != nil {
			t.Fatalf("%s %q: %v", c.mode, c.addr, err)
		}
		if cfg.Address != c.host || cfg.Port != c.port {
			t.Errorf("%s %q: got %s:%d, want %s:%d", c.mode, c.addr, cfg.Address, cfg.Port, c.host, c.port)
		}
	}
}
