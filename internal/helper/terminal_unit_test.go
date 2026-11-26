package helper

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTermUnitArgv(t *testing.T) {
	got, err := termUnitArgv("0123456789abcdef", "/usr/libexec/nodespot/nodespot-helper", 30, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemd-run", "--quiet",
		"--unit=nodespot-term-0123456789abcdef",
		"--description=nodespot web terminal",
		"--service-type=exec", "--collect",
		"--property=TimeoutStopSec=5s",
		"--", "/usr/libexec/nodespot/nodespot-helper", "term-host",
		"--id=0123456789abcdef",
		"--sock=/run/nodespot/term/0123456789abcdef.sock",
		"--rows=30", "--cols=100", "--owner=7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
	// No sandbox property and no tty or utmp property: term-host opens the
	// PTY itself, and login writes utmp.
	for _, a := range got {
		for _, bad := range []string{"PrivateDevices", "PrivateTmp", "ProtectHome", "ProtectSystem", "NoNewPrivileges", "User=", "TTYPath", "Utmp", "StandardInput"} {
			if strings.Contains(a, bad) {
				t.Errorf("unexpected property in argv: %s", a)
			}
		}
	}
}

func TestTermUnitValidation(t *testing.T) {
	good := "00ff00ff00ff00ff"
	self := "/usr/libexec/nodespot/nodespot-helper"
	for _, id := range []string{"", "xyz", "0123456789ABCDEF", "0123456789abcde", "0123456789abcdef0", "0123456789abcde;", "../../etc/passwd", "0123456789abcde\n"} {
		if _, err := termUnitArgv(id, self, 24, 80, 1); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if err := stopTermUnit(&fakeRunner{}, id); err == nil {
			t.Errorf("stop accepted id %q", id)
		}
	}
	for _, sp := range []string{"", "helper", "/x y", "/x=y"} {
		if _, err := termUnitArgv(good, sp, 24, 80, 1); err == nil {
			t.Errorf("helper path %q accepted", sp)
		}
	}
	for _, sz := range [][2]uint16{{0, 80}, {24, 0}, {501, 80}, {24, 501}} {
		if _, err := termUnitArgv(good, self, sz[0], sz[1], 1); err == nil {
			t.Errorf("size %v accepted", sz)
		}
	}
	if _, err := termUnitArgv(good, self, 24, 80, -1); err == nil {
		t.Error("negative owner accepted")
	}
	id, err := newTermID()
	if err != nil || !termIDRe.MatchString(id) {
		t.Fatalf("generated id %q: %v", id, err)
	}
	id2, _ := newTermID()
	if id == id2 {
		t.Fatal("ids repeat")
	}
}

func TestStopTermUnit(t *testing.T) {
	id := "0123456789abcdef"
	r := &fakeRunner{rules: map[string]resp{"systemctl stop nodespot-term-0123456789abcdef.service": {}}}
	if err := stopTermUnit(r, id); err != nil {
		t.Fatal(err)
	}
	if r.called("systemctl stop nodespot-term-0123456789abcdef.service") != 1 {
		t.Fatalf("calls: %v", r.calls)
	}
	// already gone
	r = &fakeRunner{rules: map[string]resp{"systemctl stop": {out: "Failed to stop x.service: Unit x.service not loaded.\n", code: 5}}}
	if err := stopTermUnit(r, id); err != nil {
		t.Fatalf("a unit that is gone is not an error: %v", err)
	}
	// a real failure and a command that cannot run
	r = &fakeRunner{rules: map[string]resp{"systemctl stop": {out: "Access denied\n", code: 1}}}
	if err := stopTermUnit(r, id); err == nil {
		t.Fatal("failure not reported")
	}
	r = &fakeRunner{rules: map[string]resp{"systemctl stop": {err: errors.New("not found")}}}
	if err := stopTermUnit(r, id); err == nil {
		t.Fatal("run error not reported")
	}
}

func TestTermUnitEnded(t *testing.T) {
	id := "0123456789abcdef"
	for out, want := range map[string]bool{"active\n": false, "activating\n": false, "deactivating\n": false, "inactive\n": true, "failed\n": true, "": false} {
		r := &fakeRunner{rules: map[string]resp{"systemctl show -p ActiveState --value nodespot-term-" + id: {out: out}}}
		if got := termUnitEnded(r, id); got != want {
			t.Errorf("%q: got %v, want %v", out, got, want)
		}
	}
	// when systemctl cannot say, the unit is not declared over
	r := &fakeRunner{rules: map[string]resp{"systemctl show": {err: errors.New("boom")}}}
	if termUnitEnded(r, id) {
		t.Error("an error counted as ended")
	}
}

// fakeCgroup points cgroupRoots and procRoot at a temp tree.
func fakeCgroup(t *testing.T, unit string, procs map[int]string) {
	t.Helper()
	dir := t.TempDir()
	oldC, oldP := cgroupRoots, procRoot
	cgroupRoots = []string{filepath.Join(dir, "cg")}
	procRoot = filepath.Join(dir, "proc")
	t.Cleanup(func() { cgroupRoots, procRoot = oldC, oldP })
	var list strings.Builder
	for pid, comm := range procs {
		d := filepath.Join(procRoot, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		list.WriteString(strconv.Itoa(pid) + "\n")
	}
	if err := os.MkdirAll(filepath.Join(cgroupRoots[0], unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroupRoots[0], unit, "cgroup.procs"), []byte(list.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTermUnitPIDsAndBusy(t *testing.T) {
	unit := "nodespot-term-0123456789abcdef.service"
	fakeCgroup(t, unit, map[int]string{4: "login", 5: "bash"})
	if got := termUnitPIDs(unit); len(got) != 2 {
		t.Fatalf("pids %v", got)
	}
	if termUnitBusy(unit) {
		t.Fatal("login and bash are not a package manager")
	}
	if termUnitPIDs("../x") != nil || termUnitPIDs("nodespot-term-zz.service") != nil {
		t.Fatal("invalid unit name accepted")
	}
	if termUnitPIDs("nodespot-term-ffffffffffffffff.service") != nil {
		t.Fatal("missing cgroup returned pids")
	}
}

func TestTermUnitBusyApt(t *testing.T) {
	unit := "nodespot-term-0123456789abcdef.service"
	fakeCgroup(t, unit, map[int]string{4: "login", 5: "bash", 6: "dpkg"})
	if !termUnitBusy(unit) {
		t.Fatal("dpkg in the unit is not busy")
	}
}
