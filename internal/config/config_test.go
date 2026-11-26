package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsWhenMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "none.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.DataDir != "/var/lib/nodespot" || c.NodeSocket != DefaultNodeSocket || c.NodeConfig != DefaultNodeConfig || c.NodelistRefresh != 24*time.Hour {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestParse(t *testing.T) {
	dir := t.TempDir()
	conf := write(t, dir, "nodespot.conf", "; comment\n[server]\nlisten = 127.0.0.1:9999 ; inline\ndatadir = /x\n"+
		"[ami]\nhost = 192.0.2.1\n# old section, ignored\n[Node]\nSocket = /tmp/n.sock\nconfig = /tmp/node.json\n"+
		"[nodelist]\nurl = http://nodes.example.net/#a\nrefresh = 2h\n")
	c, err := Load(conf)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9999" || c.DataDir != "/x" || c.NodeSocket != "/tmp/n.sock" || c.NodeConfig != "/tmp/node.json" ||
		c.NodelistURL != "http://nodes.example.net/#a" || c.NodelistRefresh != 2*time.Hour {
		t.Fatalf("%+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"header": "[server\n",
		"no eq":  "[server]\nlisten\n",
		"no key": "[server]\n = 1\n",
	} {
		if _, err := Load(write(t, dir, name+".conf", body)); err == nil || !strings.Contains(err.Error(), "line") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBadValues(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(write(t, dir, "a.conf", "[server]\nlisten\n")); err == nil {
		t.Fatal("want error")
	}
	if _, err := Load(write(t, dir, "b.conf", "[nodelist]\nrefresh = 1s\n")); err == nil {
		t.Fatal("want error")
	}
}

func TestHelperAndUpdatesSections(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "none.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if c.HelperSocket != "/run/nodespot/helper.sock" || c.UpdatesURL != "https://w1jar.github.io/nodespot" {
		t.Fatalf("defaults: %+v", c)
	}
	conf := write(t, dir, "nodespot.conf", "[helper]\nsocket = /tmp/h.sock\n[updates]\nurl = https://example.org/apt/\n")
	if c, err = Load(conf); err != nil {
		t.Fatal(err)
	}
	if c.HelperSocket != "/tmp/h.sock" || c.UpdatesURL != "https://example.org/apt" {
		t.Fatalf("%+v", c)
	}
	conf = write(t, dir, "off.conf", "[updates]\nurl =\n")
	if c, _ = Load(conf); c.UpdatesURL != "" {
		t.Fatalf("empty url should disable notes: %+v", c)
	}
}

func TestFavStatsSection(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "a.conf", ""))
	if err != nil || c.FavStatsURL != "https://stats.allstarlink.org" || c.FavStatsRate != 20 || c.FavStatsLog {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = Load(write(t, dir, "b.conf", "[favstats]\nurl = http://example.invalid/\nrate = 12\nlog = true\n"))
	if err != nil || c.FavStatsURL != "http://example.invalid" || c.FavStatsRate != 12 || !c.FavStatsLog {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Load(write(t, dir, "d.conf", "[favstats]\nlog = maybe\n")); err == nil {
		t.Error("bad log value accepted")
	}
	for _, bad := range []string{"0", "26", "x"} {
		if _, err := Load(write(t, dir, "c.conf", "[favstats]\nrate = "+bad+"\n")); err == nil {
			t.Errorf("rate %q accepted", bad)
		}
	}
}

func TestListenPort(t *testing.T) {
	for in, want := range map[string]int{":8080": 8080, "192.0.2.1:9090": 9090, "[::1]:65535": 65535, ":1": 1} {
		if got, err := ListenPort(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "8080", ":0", ":65536", ":abc", ":-1", "host"} {
		if _, err := ListenPort(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}
