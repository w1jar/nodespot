package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCalibrateFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if got := runCalibrate([]string{"-colour"}, strings.NewReader(""), &out, &errb); got != 2 {
		t.Errorf("bad flag: exit %d", got)
	}
	errb.Reset()
	if got := runCalibrate([]string{"extra"}, strings.NewReader(""), &out, &errb); got != 2 || !strings.Contains(errb.String(), "unexpected argument") {
		t.Errorf("extra argument: exit %d %q", got, errb.String())
	}
	errb.Reset()
	if got := runCalibrate([]string{"-h"}, strings.NewReader(""), &out, &errb); got != 2 || !strings.Contains(errb.String(), "sudo nodespot-node calibrate") {
		t.Errorf("-h: exit %d %q", got, errb.String())
	}
}

func TestCalibrateStandaloneFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-txfreq", "433500000"}, "-txfreq needs -rxfreq"},
		{[]string{"-offset", "100"}, "-offset needs -rxfreq"},
		{[]string{"-rxfreq", "433500000", "-cc", "16"}, "-cc must be"},
		{[]string{"-rxfreq", "99999999999"}, "32 bits"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := runCalibrate(c.args, strings.NewReader(""), &out, &errb); got != 2 || !strings.Contains(errb.String(), c.want) {
			t.Errorf("%v: exit %d, stderr %q", c.args, got, errb.String())
		}
	}
	// A valid standalone line reads no config and fails only on root (or
	// the missing port), never on a config path that does not exist.
	var out, errb bytes.Buffer
	got := runCalibrate([]string{"-rxfreq", "433500000", "-config", "/nonexistent/node.json", "-dev", "/nonexistent/tty"}, strings.NewReader(""), &out, &errb)
	if got != 1 || strings.Contains(out.String(), "nonexistent/node.json") {
		t.Errorf("exit %d, output %q", got, out.String())
	}
}
