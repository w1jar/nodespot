package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/legacy"
)

const simUpgrade = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Reading package lists...
Building dependency tree...
Reading state information...
Calculating upgrade...
The following packages have been kept back:
  linux-image-arm64 linux-headers-arm64
  raspi-firmware
The following packages will be upgraded:
  asterisk libssl3 tzdata
3 upgraded, 0 newly installed, 0 to remove and 3 not upgraded.
Inst libssl3 [3.0.11-1~deb12u1] (3.0.13-1~deb12u2 Debian-Security:12/stable-security [arm64])
Inst tzdata [2024a-0+deb12u1] (2024b-0+deb12u1 Debian:12.7/stable-updates, Debian:12.8/stable [all])
Inst asterisk [1:20.5.0-1] (1:20.9.0-1 AllStarLink:bookworm [arm64])
Inst newpkg (1.0 Debian:12/stable [arm64])
Conf libssl3 (3.0.13-1~deb12u2 Debian-Security:12/stable-security [arm64])
Conf tzdata (2024b-0+deb12u1 Debian:12.7/stable-updates, Debian:12.8/stable [all])
`

func TestParseUpgradeSim(t *testing.T) {
	pkgs, kept := ParseUpgradeSim(simUpgrade)
	if len(pkgs) != 4 {
		t.Fatalf("%+v", pkgs)
	}
	by := map[string]OSPackage{}
	for _, p := range pkgs {
		by[p.Name] = p
	}
	ssl := by["libssl3"]
	if ssl.From != "3.0.11-1~deb12u1" || ssl.To != "3.0.13-1~deb12u2" || !ssl.Security || ssl.Origin != "Debian-Security:12/stable-security" {
		t.Fatalf("%+v", ssl)
	}
	if tz := by["tzdata"]; tz.Security || tz.To != "2024b-0+deb12u1" || !strings.Contains(tz.Origin, "stable-updates") {
		t.Fatalf("%+v", tz)
	}
	if a := by["asterisk"]; a.From != "1:20.5.0-1" || a.To != "1:20.9.0-1" || a.Security {
		t.Fatalf("%+v", a)
	}
	if n := by["newpkg"]; n.From != "" || n.To != "1.0" {
		t.Fatalf("%+v", n)
	}
	if strings.Join(kept, ",") != "linux-headers-arm64,linux-image-arm64,raspi-firmware" {
		t.Fatalf("kept back: %v", kept)
	}
	if pkgs[0].Name != "asterisk" {
		t.Fatal("not sorted")
	}
	// nothing to do: empty, non-nil
	pkgs, kept = ParseUpgradeSim("Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n")
	if pkgs == nil || kept == nil || len(pkgs) != 0 || len(kept) != 0 {
		t.Fatalf("%#v %#v", pkgs, kept)
	}
	// garbage lines are ignored
	if pkgs, _ = ParseUpgradeSim("Inst broken\nInst\n"); len(pkgs) != 0 {
		t.Fatalf("%+v", pkgs)
	}
}

// Debian 13 / Raspberry Pi OS: some Inst lines end with an extra "[]" group.
const simUpgradeTrailingBrackets = `Reading package lists... Done
Building dependency tree... Done
Reading state information... Done
Calculating upgrade... Done
The following packages will be upgraded:
  libgbm1 libgl1-mesa-dri libglx-mesa0 mesa-libgallium mesa-vulkan-drivers
5 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.
Inst libgl1-mesa-dri [26.2.6-1~bpo13+0~rpt2] (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64]) []
Inst libglx-mesa0 [26.2.6-1~bpo13+0~rpt2] (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64]) []
Inst libgbm1 [26.2.6-1~bpo13+0~rpt2] (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64]) []
Inst mesa-libgallium [26.2.6-1~bpo13+0~rpt2] (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
Inst mesa-vulkan-drivers [26.2.6-1~bpo13+0~rpt2] (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
Conf libgl1-mesa-dri (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
Conf libglx-mesa0 (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
Conf libgbm1 (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
Conf mesa-libgallium (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
Conf mesa-vulkan-drivers (26.2.7-1~bpo13+0~rpt1 Raspberry Pi Foundation:stable [arm64])
`

func TestParseUpgradeSimTrailingBrackets(t *testing.T) {
	pkgs, kept := ParseUpgradeSim(simUpgradeTrailingBrackets)
	names := []string{"libgbm1", "libgl1-mesa-dri", "libglx-mesa0", "mesa-libgallium", "mesa-vulkan-drivers"}
	if len(pkgs) != len(names) {
		t.Fatalf("got %d packages, want %d: %+v", len(pkgs), len(names), pkgs)
	}
	for i, p := range pkgs {
		if p.Name != names[i] {
			t.Fatalf("package %d: name %q, want %q", i, p.Name, names[i])
		}
		if p.From != "26.2.6-1~bpo13+0~rpt2" || p.To != "26.2.7-1~bpo13+0~rpt1" || p.Origin != "Raspberry Pi Foundation:stable" || p.Security {
			t.Fatalf("%s: %+v", p.Name, p)
		}
	}
	if len(kept) != 0 {
		t.Fatalf("kept back: %v", kept)
	}
}

func TestReadReboot(t *testing.T) {
	dir := t.TempDir()
	flag, pkgs := filepath.Join(dir, "reboot-required"), filepath.Join(dir, "reboot-required.pkgs")
	if req, p := ReadReboot(flag, pkgs); req || p == nil || len(p) != 0 {
		t.Fatalf("%v %v", req, p)
	}
	os.WriteFile(flag, []byte("*** System restart required ***\n"), 0o644)
	if req, p := ReadReboot(flag, pkgs); !req || len(p) != 0 {
		t.Fatalf("%v %v", req, p)
	}
	os.WriteFile(pkgs, []byte("linux-image-6.1.0-13-arm64\nlibc6\nlibc6\n\n"), 0o644)
	if req, p := ReadReboot(flag, pkgs); !req || strings.Join(p, ",") != "libc6,linux-image-6.1.0-13-arm64" {
		t.Fatalf("%v %v", req, p)
	}
}

const (
	cmdUpdate = "env LC_ALL=C DEBIAN_FRONTEND=noninteractive apt-get -q update"
	cmdSim    = "env LC_ALL=C DEBIAN_FRONTEND=noninteractive apt-get -q -s -o Debug::NoLocking=1 upgrade"
)

func osOps(t *testing.T, r map[string]resp) (*Ops, *fakeRunner) {
	t.Helper()
	idleJob(r)
	r["systemctl list-units"] = resp{out: loadedUnit + "nodespot-osupgrade.service loaded inactive dead x\n"}
	r["systemctl show nodespot-osupgrade"] = resp{out: "ActiveState=inactive\nResult=success\n"}
	r["journalctl -u nodespot-osupgrade"] = resp{}
	f := &fakeRunner{rules: r}
	dir := t.TempDir()
	return &Ops{Run: f, RebootFile: filepath.Join(dir, "rr"), RebootPkgsFile: filepath.Join(dir, "rr.pkgs")}, f
}

func TestOSCheck(t *testing.T) {
	o, f := osOps(t, map[string]resp{cmdUpdate: {out: "Hit:1 http://deb.debian.org bookworm InRelease\n"}, cmdSim: {out: simUpgrade}})
	os.WriteFile(o.RebootFile, nil, 0o644)
	os.WriteFile(o.RebootPkgsFile, []byte("libc6\n"), 0o644)
	st, err := o.OSCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Packages) != 4 || len(st.KeptBack) != 3 || !st.RebootRequired || len(st.RebootPkgs) != 1 || st.Job != JobIdle {
		t.Fatalf("%+v", st)
	}
	// update first, then the simulation, never a real upgrade
	if f.called(cmdUpdate) != 1 || f.called(cmdSim) != 1 || f.called("systemd-run") != 0 {
		t.Fatalf("calls: %v", f.calls)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "apt-get") && !strings.Contains(c, " update") && !strings.Contains(c, " -s ") {
			t.Fatalf("non-simulated apt: %s", c)
		}
	}
}

func TestOSCheckNothingToDo(t *testing.T) {
	o, _ := osOps(t, map[string]resp{cmdUpdate: {}, cmdSim: {out: "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"}})
	st, err := o.OSCheck(context.Background())
	if err != nil || len(st.Packages) != 0 || st.RebootRequired || st.Packages == nil || st.RebootPkgs == nil {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestOSCheckLockHeld(t *testing.T) {
	lock := "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 812 (apt-get)\nE: Unable to lock directory /var/lib/apt/lists/\n"
	o, _ := osOps(t, map[string]resp{cmdUpdate: {out: lock, code: 100}})
	if _, err := o.OSCheck(context.Background()); !errors.Is(err, ErrAptBusy) {
		t.Fatalf("update: %v", err)
	}
	dpkg := "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 99 (unattended-upgr)\nE: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?\n"
	o, _ = osOps(t, map[string]resp{cmdUpdate: {}, cmdSim: {out: dpkg, code: 100}})
	if _, err := o.OSCheck(context.Background()); !errors.Is(err, ErrAptBusy) {
		t.Fatalf("sim: %v", err)
	}
	// any other failure is a plain error carrying the output
	o, _ = osOps(t, map[string]resp{cmdUpdate: {out: "E: The repository is not signed.\n", code: 100}})
	_, err := o.OSCheck(context.Background())
	if err == nil || errors.Is(err, ErrAptBusy) || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("%v", err)
	}
}

func TestOSCheckRefusedWhileJobRuns(t *testing.T) {
	r := map[string]resp{cmdUpdate: {}, cmdSim: {}}
	o, f := osOps(t, r)
	r["systemctl show nodespot-osupgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
	if _, err := o.OSCheck(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	// the nodespot upgrade counts too
	r["systemctl show nodespot-osupgrade"] = resp{out: "ActiveState=inactive\nResult=success\n"}
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
	if _, err := o.OSCheck(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	if f.called("env") != 0 {
		t.Fatalf("apt ran: %v", f.calls)
	}
}

func TestOSApply(t *testing.T) {
	r := map[string]resp{"systemd-run": {}}
	o, f := osOps(t, r)
	o.SelfPath = "/usr/libexec/nodespot/nodespot-helper"
	st, err := o.OSApply(context.Background())
	if err != nil || st.Job != JobRunning {
		t.Fatalf("%+v %v", st, err)
	}
	want := "systemd-run --no-block --unit=nodespot-osupgrade --collect --property=Type=oneshot --setenv=DEBIAN_FRONTEND=noninteractive --setenv=APT_LISTCHANGES_FRONTEND=none /bin/sh -c " +
		o.osUpgradeScript()
	if f.called(want) != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
	script := o.osUpgradeScript()
	if strings.Contains(script, "full-upgrade") || strings.Contains(script, "dist-upgrade") {
		t.Fatal("must be a plain upgrade")
	}
	for _, w := range []string{"LC_ALL=C apt-get update -q -o DPkg::Lock::Timeout=600 > '" + osUpdateOutFile + "'",
		"'/usr/libexec/nodespot/nodespot-helper' dv-step " + StepAptUpdateCheck + " '" + osUpdateOutFile + "' || exit \"$rc\"", "\napt-get upgrade -y -q"} {
		if !strings.Contains(script, w) {
			t.Fatalf("script lacks %q:\n%s", w, script)
		}
	}
	// not while the nodespot upgrade runs, and not while a check runs
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
	if _, err := o.OSApply(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	if f.called("systemd-run") != 1 {
		t.Fatal("second unit started")
	}
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=inactive\nResult=success\n"}
	o.osCheckMu.Lock()
	if _, err := o.OSApply(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("during check: %v", err)
	}
	o.osCheckMu.Unlock()
}

func TestNodespotApplyRefusedWhileOSJobRuns(t *testing.T) {
	r := map[string]resp{"dpkg-query -W -f=${Version} nodespot": {out: "0.2.0"}, "systemd-run": {}}
	o, f := osOps(t, r)
	r["systemctl show nodespot-osupgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
	if _, err := o.UpdateApply(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	if f.called("systemd-run") != 0 {
		t.Fatal("started")
	}
}

func TestReboot(t *testing.T) {
	r := map[string]resp{"systemctl --no-block reboot": {}}
	o, f := osOps(t, r)
	if res, err := o.Reboot(context.Background()); err != nil || !res.OK || f.called("systemctl --no-block reboot") != 1 {
		t.Fatalf("%+v %v %v", res, err, f.calls)
	}
	r["systemctl show nodespot-osupgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
	if _, err := o.Reboot(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	if f.called("systemctl --no-block reboot") != 1 {
		t.Fatal("rebooted during an upgrade")
	}
}

func TestOSOpsOverSocket(t *testing.T) {
	lock := "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 812 (apt-get)\n"
	o, _ := osOps(t, map[string]resp{cmdUpdate: {out: lock, code: 100}, "systemctl --no-block reboot": {}, "systemd-run": {}})
	c, _ := startServer(t, o, PeerUID, []uint32{0, uint32(os.Getuid())})
	ctx := context.Background()
	if _, err := c.OSCheck(ctx); !errors.Is(err, ErrAptBusy) {
		t.Fatalf("%v", err)
	}
	if st, err := c.OSStatus(ctx); err != nil || st.Job != JobIdle || st.Packages == nil {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.Reboot(ctx); err != nil {
		t.Fatal(err)
	}
	if st, err := c.OSApply(ctx); err != nil || st.Job != JobRunning {
		t.Fatalf("%+v %v", st, err)
	}
	// just started: a reboot is refused
	if err := c.Reboot(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
}

func TestNodespotRestart(t *testing.T) {
	o, f := osOps(t, map[string]resp{"systemctl --no-block restart nodespot.service": {}})
	if res, err := o.NodespotRestart(context.Background()); err != nil || !res.OK || f.called("systemctl --no-block restart nodespot.service") != 1 {
		t.Fatalf("%+v %v %v", res, err, f.calls)
	}
	o, _ = osOps(t, map[string]resp{"systemctl --no-block restart": {code: 1, out: "Failed"}})
	if _, err := o.NodespotRestart(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

const testRepo = "http://apt.example.net/Repo"

func TestAptUpdateOnlyRepoFailed(t *testing.T) {
	debian := "Hit:1 http://deb.debian.org/debian bookworm InRelease\n"
	cases := []struct {
		name, out string
		want      bool
	}{
		{"release file missing", debian +
			"Ign:2 http://apt.example.net/Repo bookworm InRelease\n" +
			"Err:3 http://apt.example.net/Repo bookworm Release\n  404  Not Found [IP: 192.0.2.10 80]\n" +
			"Reading package lists...\n" +
			"E: The repository 'http://apt.example.net/Repo bookworm Release' does not have a Release file.\n" +
			"N: Updating from such a repository can't be done securely, and is therefore disabled by default.\n", true},
		{"no pubkey", debian +
			"Err:2 http://apt.example.net/Repo bookworm InRelease\n  The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 0123456789ABCDEF\n" +
			"W: GPG error: http://apt.example.net/Repo bookworm InRelease: The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 0123456789ABCDEF\n" +
			"E: The repository 'http://apt.example.net/Repo bookworm InRelease' is not signed.\n", true},
		{"expired key", debian +
			"Err:2 http://apt.example.net/Repo bookworm InRelease\n  The following signatures were invalid: EXPKEYSIG 0123456789ABCDEF Example\n" +
			"E: Failed to fetch http://apt.example.net/Repo/dists/bookworm/InRelease  The following signatures were invalid: EXPKEYSIG 0123456789ABCDEF Example\n" +
			"E: Some index files failed to download. They have been ignored, or old ones used instead.\n", true},
		{"dns failure with Err line", debian +
			"Err:2 http://apt.example.net/Repo bookworm InRelease\n  Temporary failure resolving 'apt.example.net'\n", true},
		{"dns failure, warnings only", debian +
			"Ign:2 http://apt.example.net/Repo bookworm InRelease\n" +
			"W: Failed to fetch http://apt.example.net/Repo/dists/bookworm/InRelease  Temporary failure resolving 'apt.example.net'\n" +
			"W: Some index files failed to download. They have been ignored, or old ones used instead.\n", false},
		{"mixed", debian +
			"Err:2 http://apt.example.net/Repo bookworm InRelease\n  404  Not Found\n" +
			"Err:3 http://deb.debian.org/debian bookworm-updates InRelease\n  Connection failed\n" +
			"E: The repository 'http://apt.example.net/Repo bookworm InRelease' is not signed.\n" +
			"E: Failed to fetch http://deb.debian.org/debian/dists/bookworm-updates/InRelease  Connection failed\n", false},
		{"debian only", "Err:1 http://deb.debian.org/debian bookworm InRelease\n  403  Forbidden\nE: The repository 'http://deb.debian.org/debian bookworm InRelease' is no longer signed.\n", false},
		{"lock", "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 812 (apt-get)\nE: Unable to lock directory /var/lib/apt/lists/\n", false},
		{"no error lines", debian + "Reading package lists...\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := aptUpdateOnlyRepoFailed(c.out, testRepo); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if aptUpdateOnlyRepoFailed("E: x apt.example.net\n", "") {
		t.Error("empty repo URL must never match")
	}
}

func TestAptUpdateRepoWarned(t *testing.T) {
	w := "W: Failed to fetch http://apt.example.net/Repo/dists/bookworm/InRelease  Temporary failure resolving 'apt.example.net'\n"
	if !aptUpdateRepoWarned(w, testRepo) || aptUpdateRepoWarned("Hit:1 http://deb.debian.org/debian bookworm InRelease\n", testRepo) ||
		aptUpdateRepoWarned("W: Failed to fetch http://deb.debian.org/x\n", testRepo) {
		t.Fatal("wrong classification")
	}
}

func TestOSCheckRepoDown(t *testing.T) {
	bad := "Err:2 " + legacy.AptRepoURL + " bookworm InRelease\n  404  Not Found\nE: The repository '" + legacy.AptRepoURL + " bookworm InRelease' is not signed.\n"
	o, f := osOps(t, map[string]resp{cmdUpdate: {out: bad, code: 100}, cmdSim: {out: simUpgrade}})
	st, err := o.OSCheck(context.Background())
	if err != nil || len(st.Packages) != 4 || len(st.Warnings) != 1 || st.Warnings[0] != OSWarnRepo || f.called(cmdSim) != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	// another repository failing too is still an error
	mixed := bad + "E: Failed to fetch http://deb.debian.org/debian/dists/bookworm/InRelease  403  Forbidden\n"
	o, f = osOps(t, map[string]resp{cmdUpdate: {out: mixed, code: 100}, cmdSim: {out: simUpgrade}})
	if _, err := o.OSCheck(context.Background()); err == nil || f.called(cmdSim) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestAptUpdateCheckStep(t *testing.T) {
	o := &Ops{}
	file := filepath.Join(t.TempDir(), "out")
	os.WriteFile(file, []byte("E: The repository '"+legacy.AptRepoURL+" bookworm Release' does not have a Release file.\n"), 0o644)
	var w strings.Builder
	if err := o.AptUpdateCheck(file, &w); err != nil || !strings.Contains(w.String(), "warning:") {
		t.Fatalf("%v %q", err, w.String())
	}
	os.WriteFile(file, []byte("Err:1 http://deb.debian.org/debian bookworm InRelease\nE: Failed to fetch http://deb.debian.org/debian\n"), 0o644)
	if err := o.AptUpdateCheck(file, &w); err == nil {
		t.Fatal("other failure must fail")
	}
	if err := o.AptUpdateCheck(file+".missing", &w); err == nil {
		t.Fatal("missing file must fail")
	}
}

// The script, run by a real shell with a fake apt-get, continues after a
// repo-only failure and stops otherwise.
func TestOSUpgradeScriptShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, c := range []struct {
		name     string
		checkRC  int
		wantUpgr bool
	}{{"repo only", 0, true}, {"other failure", 1, false}} {
		dir := t.TempDir()
		log := filepath.Join(dir, "log")
		os.WriteFile(filepath.Join(dir, "apt-get"), []byte("#!/bin/sh\necho \"$1\" >> "+log+"\n[ \"$1\" = update ] && { echo 'E: boom'; exit 100; }\nexit 0\n"), 0o755)
		os.WriteFile(filepath.Join(dir, "self"), []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", c.checkRC)), 0o755)
		o := &Ops{SelfPath: filepath.Join(dir, "self")}
		script := strings.ReplaceAll(o.osUpgradeScript(), osUpdateOutFile, filepath.Join(dir, "out"))
		cmd := exec.Command(sh, "-c", script)
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
		err := cmd.Run()
		b, _ := os.ReadFile(log)
		if got := strings.Contains(string(b), "upgrade"); got != c.wantUpgr || (err == nil) != c.wantUpgr {
			t.Errorf("%s: upgrade ran=%v err=%v log=%q", c.name, got, err, b)
		}
	}
}
