package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		out  string
	}{
		{name: "version", args: []string{"-version"}, code: 0, out: "nodespot-node"},
		{name: "bad flag", args: []string{"-colour"}, code: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if got := run(c.args, &out); got != c.code {
				t.Fatalf("exit %d, want %d (%s)", got, c.code, out.String())
			}
			if !strings.Contains(out.String(), c.out) {
				t.Fatalf("output %q", out.String())
			}
		})
	}
}

func TestBadConfigExitsWithConfigError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.json")
	if err := os.WriteFile(p, []byte(`{"version":1,"station":{"callsign":"HELLO"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if got := run([]string{"-config", p, "-socket", filepath.Join(t.TempDir(), "s")}, &out); got != exitConfig {
		t.Fatalf("exit %d, want %d (%s)", got, exitConfig, out.String())
	}
	if !strings.Contains(out.String(), "callsign") {
		t.Fatalf("error does not say what is wrong: %s", out.String())
	}
}
