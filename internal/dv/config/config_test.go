package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/dv/ctl"
)

func TestDefaultsWhenMissing(t *testing.T) {
	c, found, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || found {
		t.Fatal(err, found)
	}
	if c.USRP.Listen != "127.0.0.1:34001" || c.USRP.Peer != "127.0.0.1:32001" ||
		c.Vocoder.MD380 != "127.0.0.1:2470" || c.Vocoder.TimeoutMS != 60 ||
		len(c.Networks) != 1 || c.Networks[0].Mode != ctl.ModeLoop {
		t.Fatalf("%+v", c)
	}
}

func TestLoadGood(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	doc := `{"version":1,"usrp":{"listen":"127.0.0.1:1234","peer":"127.0.0.1:1235","gainRxDb":3},
	 "identity":{"callsign":"N0CALL","dmrId":1234567},
	 "networks":[{"mode":"LOOP","enabled":true},{"mode":"BM","address":"192.0.2.1","port":62031,"password":"x"}],
	 "ctl":{"allowUids":[1000]}}`
	os.WriteFile(p, []byte(doc), 0o600)
	c, found, err := Load(p)
	if err != nil || !found {
		t.Fatal(err)
	}
	if c.Identity.DMRID != 1234567 || c.Vocoder.TimeoutMS != 60 || len(c.Networks) != 2 || c.USRP.GainRx != 3 {
		t.Fatalf("%+v", c)
	}
}

func TestStrict(t *testing.T) {
	bad := map[string]string{
		"unknown field": `{"version":1,"bogus":1}`,
		"version":       `{"version":2}`,
		"no version":    `{}`,
		"trailing":      `{"version":1} {"version":1}`,
		"bad addr":      `{"version":1,"usrp":{"listen":"nope"}}`,
		"port":          `{"version":1,"usrp":{"listen":"127.0.0.1:99999"}}`,
		"timeout":       `{"version":1,"vocoder":{"timeoutMs":5000}}`,
		"gain":          `{"version":1,"usrp":{"gainTxDb":50}}`,
		"mode":          `{"version":1,"networks":[{"mode":"XYZ"}]}`,
		"dup mode":      `{"version":1,"networks":[{"mode":"LOOP"},{"mode":"LOOP"}]}`,
		"not json":      `version`,
		"uid":           `{"version":1,"ctl":{"allowUids":[-1]}}`,
		"net port":      `{"version":1,"networks":[{"mode":"BM","port":70000}]}`,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(`{"version":1,"x":1}`)); err == nil || !strings.Contains(err.Error(), "config") {
		t.Errorf("err %v", err)
	}
}

// The echo test (LOOP) is part of every config and always on, whatever the
// file says.
func TestEchoTestAlwaysOn(t *testing.T) {
	for name, doc := range map[string]string{
		"absent":   `{"version":1,"networks":[{"mode":"BM","enabled":true}]}`,
		"disabled": `{"version":1,"networks":[{"mode":"BM"},{"mode":"LOOP","enabled":false}]}`,
		"no list":  `{"version":1}`,
	} {
		c, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		n := 0
		for _, nc := range c.Networks {
			if nc.Mode == ctl.ModeLoop {
				n++
				if !nc.Enabled {
					t.Errorf("%s: LOOP disabled", name)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %d LOOP entries in %+v", name, n, c.Networks)
		}
	}
}

func TestM17Config(t *testing.T) {
	ok := `{"version":1,"identity":{"callsign":"N0CALL"},"networks":[{"mode":"M17","enabled":true,"address":"192.0.2.10","port":17000,"options":{"module":"C","can":"2","reflector":"M17-XXX","suffix":"B"}}]}`
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"module":   `"options":{"module":"c"}`,
		"can":      `"options":{"can":"16"}`,
		"reflect":  `"options":{"reflector":"XXX"}`,
		"callsign": `"options":{"suffix":"TOOLONGSUF"}`,
	} {
		b := `{"version":1,"identity":{"callsign":"N0CALL"},"networks":[{"mode":"M17","enabled":true,` + doc + `}]}`
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a disabled M17 needs no callsign
	if _, err := Parse([]byte(`{"version":1,"networks":[{"mode":"M17"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{"version":1,"networks":[{"mode":"M17","enabled":true}]}`)); err == nil {
		t.Fatal("enabled M17 without callsign accepted")
	}
}

func TestAudioDefaultsAndRanges(t *testing.T) {
	c, err := Parse([]byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.USRP.AGCTx || !c.USRP.AGCRx || c.USRP.AGCTarget != -22 || c.USRP.AGCMax != 18 || c.USRP.GainRx != 0 || c.USRP.GainTx != 0 {
		t.Fatalf("defaults %+v", c.USRP)
	}
	if d := Default().USRP; !d.AGCRx || d.AGCTx || d.AGCTarget != -22 || d.AGCMax != 18 {
		t.Fatalf("Default %+v", d)
	}
	c, err = Parse([]byte(`{"version":1,"usrp":{"agcTx":true,"agcRx":true,"agcTargetDb":-40,"agcMaxGainDb":0,"gainRxDb":-20,"gainTxDb":20}}`))
	if err != nil || !c.USRP.AGCTx || !c.USRP.AGCRx || c.USRP.AGCTarget != -40 || c.USRP.AGCMax != 0 {
		t.Fatalf("explicit values: %+v, %v", c.USRP, err)
	}
	// an explicit agcRx false is honoured and survives a round trip
	c, err = Parse([]byte(`{"version":1,"usrp":{"agcRx":false}}`))
	if err != nil || c.USRP.AGCRx {
		t.Fatalf("agcRx false: %+v, %v", c.USRP, err)
	}
	b, err := json.Marshal(c)
	if err != nil || !strings.Contains(string(b), `"agcRx":false`) {
		t.Fatalf("agcRx false not written: %s, %v", b, err)
	}
	if c, err = Parse(b); err != nil || c.USRP.AGCRx {
		t.Fatalf("round trip: %+v, %v", c.USRP, err)
	}
	// a usrp block without agcRx stays on
	if c, err = Parse([]byte(`{"version":1,"usrp":{"agcTx":true}}`)); err != nil || !c.USRP.AGCRx || !c.USRP.AGCTx {
		t.Fatalf("usrp without agcRx: %+v, %v", c.USRP, err)
	}
	c, err = Parse([]byte(`{"version":1,"usrp":{"agcTargetDb":-6,"agcMaxGainDb":30}}`))
	if err != nil || c.USRP.AGCTarget != -6 || c.USRP.AGCMax != 30 {
		t.Fatalf("upper edge: %+v, %v", c.USRP, err)
	}
	for name, doc := range map[string]string{
		"target low":  `{"version":1,"usrp":{"agcTargetDb":-41}}`,
		"target high": `{"version":1,"usrp":{"agcTargetDb":-5}}`,
		"max low":     `{"version":1,"usrp":{"agcMaxGainDb":-1}}`,
		"max high":    `{"version":1,"usrp":{"agcMaxGainDb":31}}`,
		"agc type":    `{"version":1,"usrp":{"agcTx":"yes"}}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// BrandMeister is on demand unless the option stay_logged_in is "true"; the
// other networks are not; an explicit onDemand wins.
func TestOnDemandDefault(t *testing.T) {
	c, err := Parse([]byte(`{"version":1,"networks":[
		{"mode":"BM","enabled":true,"address":"bm.example.net"},
		{"mode":"TGIF","enabled":true},
		{"mode":"DMR","enabled":true,"onDemand":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"BM": true, "TGIF": false, "DMR": true, "LOOP": false}
	for _, n := range c.Networks {
		if n.OnDemand == nil || *n.OnDemand != want[n.Mode] {
			t.Errorf("%s: %v", n.Mode, n.OnDemand)
		}
	}
	c, err = Parse([]byte(`{"version":1,"networks":[{"mode":"BM","enabled":true,"options":{"stay_logged_in":"true"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range c.Networks {
		if n.Mode == "BM" && *n.OnDemand {
			t.Error("stay_logged_in did not keep BM always on")
		}
	}
}
