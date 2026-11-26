package helper

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/legacy"
)

// postrm.sh is exercised with a fake systemctl and a temporary root: it must
// re-enable exactly the recorded units on purge and never fail.

type postrmEnv struct {
	root, bin, log, sysdir string
}

func newPostrmEnv(t *testing.T, systemctlExit string) *postrmEnv {
	t.Helper()
	d := t.TempDir()
	e := &postrmEnv{root: filepath.Join(d, "root"), bin: filepath.Join(d, "bin"), log: filepath.Join(d, "calls.log"), sysdir: filepath.Join(d, "run-systemd")}
	for _, p := range []string{e.root, e.bin, e.sysdir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\necho \"systemctl $*\" >>" + e.log + "\nexit " + systemctlExit + "\n"
	write(t, filepath.Join(e.bin, "systemctl"), script, 0o755)
	// Nothing may touch the real account database.
	write(t, filepath.Join(e.bin, "getent"), "#!/bin/sh\nexit 2\n", 0o755)
	return e
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (e *postrmEnv) path(p string) string { return filepath.Join(e.root, p) }

func (e *postrmEnv) run(t *testing.T, arg string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", filepath.Join("..", "..", "packaging", "scripts", "postrm.sh"), arg)
	cmd.Env = []string{"PATH=" + e.bin + ":/usr/bin:/bin", "NODESPOT_POSTRM_ROOT=" + e.root, "NODESPOT_POSTRM_SYSTEMD_DIR=" + e.sysdir}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *postrmEnv) calls() []string {
	b, _ := os.ReadFile(e.log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func (e *postrmEnv) populate(t *testing.T, record string) {
	write(t, e.path("var/lib/nodespot/disabled-units"), record, 0o644)
	write(t, e.path("var/lib/nodespot/nodespot.db"), "x", 0o600)
	write(t, e.path("var/lib/nodespot-webport/previous-listen"), ":8080\n", 0o600)
	write(t, e.path("etc/systemd/system/nodespot.service.d/port80.conf"), "[Service]\n", 0o644)
	write(t, e.path("var/lib/nodespot-config-history/x"), "x", 0o600)
	for _, d := range []string{"nodespot-dvstate", "nodespot-dvstate-history", filepath.Base(legacy.OldStateDir), filepath.Base(legacy.OldHistoryDir)} {
		write(t, e.path("var/lib/"+d+"/x"), "x", 0o600)
	}
}

func TestPostrmPurgeReenablesRecordedUnits(t *testing.T) {
	e := newPostrmEnv(t, "0")
	// valid lines, a duplicate, CRLF, and lines that are not on the allowlist
	e.populate(t, "apache2\ncockpit.socket\r\napache2\nssh.service\n../../etc/passwd\nasterisk\n\nallmon3")
	out, err := e.run(t, "purge")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{"systemctl daemon-reload", "systemctl enable --now apache2", "systemctl enable --now cockpit.socket", "systemctl enable --now allmon3"}
	got := e.calls()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls:\n%v\nwant:\n%v", got, want)
	}
	// the drop-in is gone and systemd reloaded BEFORE Apache comes back
	if exists(e.path("etc/systemd/system/nodespot.service.d")) {
		t.Fatal("drop-in left")
	}
	for _, p := range []string{"var/lib/nodespot", "var/lib/nodespot-webport", "var/lib/nodespot-config-history", "var/lib/nodespot-dvstate", "var/lib/nodespot-dvstate-history", legacy.OldStateDir[1:], legacy.OldHistoryDir[1:]} {
		if exists(e.path(p)) {
			t.Errorf("%s left", p)
		}
	}
}

func TestPostrmPurgeKeepsOtherDropins(t *testing.T) {
	e := newPostrmEnv(t, "0")
	e.populate(t, "")
	write(t, e.path("etc/systemd/system/nodespot.service.d/local.conf"), "[Service]\n", 0o644)
	if out, err := e.run(t, "purge"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if exists(e.path("etc/systemd/system/nodespot.service.d/port80.conf")) || !exists(e.path("etc/systemd/system/nodespot.service.d/local.conf")) {
		t.Fatal("only port80.conf may go")
	}
}

func TestPostrmNeverFails(t *testing.T) {
	// systemctl fails for every call: purge still succeeds and still cleans up
	e := newPostrmEnv(t, "1")
	e.populate(t, "apache2\ncockpit.socket\n")
	if out, err := e.run(t, "purge"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if exists(e.path("var/lib/nodespot")) || exists(e.path("etc/systemd/system/nodespot.service.d")) {
		t.Fatal("cleanup skipped after a failing systemctl")
	}
	if n := len(e.calls()); n != 3 { // reload + two enables, each tried
		t.Fatalf("%v", e.calls())
	}
}

func TestPostrmWithoutRecordOrSystemd(t *testing.T) {
	e := newPostrmEnv(t, "0")
	e.populate(t, "apache2\n")
	_ = os.Remove(e.path("var/lib/nodespot/disabled-units"))
	if _, err := e.run(t, "purge"); err != nil {
		t.Fatal(err)
	}
	if c := e.calls(); len(c) != 1 || c[0] != "systemctl daemon-reload" {
		t.Fatalf("%v", c)
	}
	// not running under systemd (a container, chroot): no systemctl at all
	e2 := newPostrmEnv(t, "0")
	e2.populate(t, "apache2\n")
	if err := os.Remove(e2.sysdir); err != nil {
		t.Fatal(err)
	}
	if out, err := e2.run(t, "purge"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(e2.calls()) != 0 || exists(e2.path("var/lib/nodespot")) {
		t.Fatalf("%v", e2.calls())
	}
}

func TestPostrmIgnoresSymlinkedRecord(t *testing.T) {
	e := newPostrmEnv(t, "0")
	e.populate(t, "")
	_ = os.Remove(e.path("var/lib/nodespot/disabled-units"))
	target := filepath.Join(t.TempDir(), "elsewhere")
	write(t, target, "apache2\n", 0o644)
	if err := os.Symlink(target, e.path("var/lib/nodespot/disabled-units")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, "purge"); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.calls() {
		if strings.Contains(c, "enable") {
			t.Fatalf("followed a symlinked record: %v", e.calls())
		}
	}
}

func TestPostrmRemoveLeavesEverything(t *testing.T) {
	e := newPostrmEnv(t, "0")
	e.populate(t, "apache2\n")
	if out, err := e.run(t, "remove"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if c := e.calls(); len(c) != 1 || c[0] != "systemctl daemon-reload" {
		t.Fatalf("%v", c)
	}
	for _, p := range []string{"var/lib/nodespot/disabled-units", "etc/systemd/system/nodespot.service.d/port80.conf", "var/lib/nodespot-webport/previous-listen"} {
		if !exists(e.path(p)) {
			t.Errorf("%s removed by apt remove", p)
		}
	}
	for _, arg := range []string{"upgrade", "abort-upgrade"} {
		if _, err := e.run(t, arg); err != nil {
			t.Errorf("%s: %v", arg, err)
		}
	}
	if _, err := e.run(t, "bogus"); err == nil {
		t.Error("unknown argument accepted")
	}
}
