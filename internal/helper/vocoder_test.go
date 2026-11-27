package helper

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syntheticKey builds a minimal OpenPGP v4 public key packet (tag 6) and
// returns it in binary and armored form.
func syntheticKey(seed byte) (bin []byte, armored string) {
	body := []byte{4, 0, 0, 0, seed, 1, 0, 8, 0xC1, 0, 8, 3}
	bin = append([]byte{0xC6, byte(len(body))}, body...)
	b64 := base64.StdEncoding.EncodeToString(bin)
	crc := crc24(bin)
	c := base64.StdEncoding.EncodeToString([]byte{byte(crc >> 16), byte(crc >> 8), byte(crc)})
	return bin, "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n" + b64 + "\n=" + c + "\n-----END PGP PUBLIC KEY BLOCK-----\n"
}

func pinKey(t *testing.T, bin []byte) {
	t.Helper()
	fps, err := KeyringFingerprints(bin)
	if err != nil || len(fps) != 1 {
		t.Fatalf("%v %v", fps, err)
	}
	old := vocoderPinnedFP
	vocoderPinnedFP = fps[0]
	t.Cleanup(func() { vocoderPinnedFP = old })
}

func TestVerifyVocoderKeyring(t *testing.T) {
	bin, arm := syntheticKey(1)
	pinKey(t, bin)
	for name, in := range map[string][]byte{"binary": bin, "armored": []byte(arm)} {
		got, err := VerifyVocoderKeyring(in)
		if err != nil || !bytes.Equal(got, bin) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other, otherArm := syntheticKey(2)
	for name, in := range map[string][]byte{
		"other key": other, "other armored": []byte(otherArm), "garbage": []byte("not a key"), "empty": nil,
		"two keys": append(append([]byte{}, bin...), other...),
		"bad crc":  []byte(strings.Replace(arm, "=", "=AAAA", 1)),
	} {
		if _, err := VerifyVocoderKeyring(in); !errors.Is(err, ErrVocoderKeyMismatch) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestVocoderPinnedFingerprintFormat(t *testing.T) {
	if len(VocoderKeyFingerprint) != 40 || strings.ToUpper(VocoderKeyFingerprint) != VocoderKeyFingerprint {
		t.Fatalf("%q", VocoderKeyFingerprint)
	}
}

func TestVocoderPlatform(t *testing.T) {
	for _, c := range []string{"bookworm", "trixie"} {
		for _, arch := range []string{"arm64", "amd64", "armhf"} {
			if ok, reason := VocoderPlatform(c, arch); !ok || reason != "" {
				t.Errorf("%s %s: %v %q", c, arch, ok, reason)
			}
		}
		if ok, _ := VocoderPlatform(c, "riscv64"); ok {
			t.Errorf("%s riscv64 accepted", c)
		}
	}
	if ok, _ := VocoderPlatform("bullseye", "arm64"); ok {
		t.Error("bullseye accepted")
	}
}

func TestVocoderPackages(t *testing.T) {
	if got := vocoderPackages("arm64"); len(got) != 2 || got[0] != "md380-emu" || got[1] != "qemu-user-static" {
		t.Fatalf("%v", got)
	}
	if got := vocoderPackages("armhf"); len(got) != 1 || got[0] != "md380-emu" {
		t.Fatalf("%v", got)
	}
}

func TestVocoderScript(t *testing.T) {
	s := vocoderScript("arm64", DefaultVocoderDropin)
	order := []string{
		"set -e", "rm -f /var/lib/apt/lists/", "apt-get update", "apt-get install -y", "md380-emu qemu-user-static",
		"IPAddressDeny=any", "systemctl daemon-reload", "systemctl enable --now md380-emu.service", "is-active",
	}
	at := -1
	for _, w := range order {
		i := strings.Index(s[at+1:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", w, s)
		}
		at += i + 1
	}
	if strings.Contains(vocoderScript("armhf", DefaultVocoderDropin), "qemu") {
		t.Error("armhf runs the vocoder natively")
	}
	if strings.Contains(s, "apt-get purge") || strings.Contains(s, "apt-get remove") || strings.Contains(s, "nodespot-dv") {
		t.Error("the install job only installs the vocoder")
	}
}

func TestVocoderSourceLine(t *testing.T) {
	got := vocoderSourceLine("/k.gpg")
	if got != "deb [signed-by=/k.gpg] http://dvswitch.org/DVSwitch_Repository bookworm hamradio\n" {
		t.Fatalf("%q", got)
	}
}

// vocoderOps returns Ops on a fake host: Debian trixie, arm64, a clock in
// the future of 2026 and a key the test pinned.
func vocoderOps(t *testing.T, r map[string]resp) (*Ops, *fakeRunner, []byte) {
	t.Helper()
	bin, _ := syntheticKey(7)
	pinKey(t, bin)
	if _, ok := r["dpkg --print-architecture"]; !ok {
		r["dpkg --print-architecture"] = resp{out: "arm64\n"}
	}
	r["systemctl list-units"] = resp{}
	r["journalctl"] = resp{}
	f := &fakeRunner{rules: r}
	dir := t.TempDir()
	rel := filepath.Join(dir, "os-release")
	os.WriteFile(rel, []byte("VERSION_CODENAME=trixie\n"), 0o644)
	o := &Ops{
		Run: f, OSRelease: rel,
		VocoderKeyring: filepath.Join(dir, "keyrings", "v.gpg"), VocoderSourceList: filepath.Join(dir, "apt", "v.list"),
		VocoderDropin: filepath.Join(dir, "d", "nodespot.conf"),
		HTTPGet:       func(context.Context, string) ([]byte, error) { return bin, nil },
		Now:           func() time.Time { return time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC) },
	}
	return o, f, bin
}

const (
	cmdDpkgVocoder = "dpkg-query -W -f=${db:Status-Status} md380-emu"
	cmdActive      = "systemctl is-active --quiet md380-emu.service"
)

func TestVocoderStatus(t *testing.T) {
	r := map[string]resp{cmdDpkgVocoder: {out: "installed"}, cmdActive: {}}
	o, _, _ := vocoderOps(t, r)
	st, err := o.VocoderStatus(context.Background())
	if err != nil || !st.Installed || !st.Active || !st.Supported || st.Arch != "arm64" || st.Job != "" {
		t.Fatalf("%+v %v", st, err)
	}
	// installed but stopped
	r[cmdActive] = resp{code: 3}
	if st, _ = o.VocoderStatus(context.Background()); !st.Installed || st.Active {
		t.Fatalf("%+v", st)
	}
	// not installed (dpkg-query exits 1 and prints nothing useful)
	r[cmdDpkgVocoder] = resp{code: 1}
	if st, _ = o.VocoderStatus(context.Background()); st.Installed {
		t.Fatalf("%+v", st)
	}
	r[cmdDpkgVocoder] = resp{out: "config-files"}
	if st, _ = o.VocoderStatus(context.Background()); st.Installed {
		t.Fatalf("removed package reads as installed: %+v", st)
	}
}

func TestVocoderInstall(t *testing.T) {
	r := map[string]resp{cmdDpkgVocoder: {code: 1}, cmdActive: {code: 3}, "systemd-run": {}}
	o, f, bin := vocoderOps(t, r)
	st, err := o.VocoderInstall(context.Background())
	if err != nil || st.Job != "running" || st.Installed {
		t.Fatalf("%+v %v", st, err)
	}
	if f.called("systemd-run --no-block --unit=nodespot-vocoder-install --collect --property=Type=oneshot") != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
	if got, _ := os.ReadFile(o.VocoderKeyring); !bytes.Equal(got, bin) {
		t.Errorf("keyring: %q", got)
	}
	src, _ := os.ReadFile(o.VocoderSourceList)
	if string(src) != vocoderSourceLine(o.VocoderKeyring) {
		t.Errorf("source: %q", src)
	}
	// started a moment ago: a second call reports the job and starts nothing
	if st, err = o.VocoderInstall(context.Background()); err != nil || st.Job != "running" {
		t.Fatalf("%+v %v", st, err)
	}
	if f.called("systemd-run") != 1 {
		t.Fatalf("second unit started: %v", f.calls)
	}
}

func TestVocoderInstallIdempotentWhenActive(t *testing.T) {
	r := map[string]resp{cmdDpkgVocoder: {out: "installed"}, cmdActive: {}}
	o, f, _ := vocoderOps(t, r)
	st, err := o.VocoderInstall(context.Background())
	if err != nil || !st.Installed || !st.Active {
		t.Fatalf("%+v %v", st, err)
	}
	if f.called("systemd-run") != 0 {
		t.Fatal("ran a job although the vocoder is active")
	}
	if _, err := os.Stat(o.VocoderSourceList); err == nil {
		t.Error("wrote the apt source although nothing was to do")
	}
}

func TestVocoderInstallRepairsInstalledButStopped(t *testing.T) {
	r := map[string]resp{cmdDpkgVocoder: {out: "installed"}, cmdActive: {code: 3}, "systemd-run": {}}
	o, f, _ := vocoderOps(t, r)
	if st, err := o.VocoderInstall(context.Background()); err != nil || st.Job != "running" || !st.Installed {
		t.Fatalf("%+v %v", st, err)
	}
	if f.called("systemd-run") != 1 {
		t.Fatalf("%v", f.calls)
	}
}

func TestVocoderInstallRefusals(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, mut func(o *Ops, r map[string]resp)) (error, *Ops, *fakeRunner) {
		r := map[string]resp{cmdDpkgVocoder: {code: 1}, cmdActive: {code: 3}, "systemd-run": {}}
		o, f, _ := vocoderOps(t, r)
		mut(o, r)
		_, err := o.VocoderInstall(ctx)
		if f.called("systemd-run") != 0 {
			t.Error("a job started")
		}
		if _, serr := os.Stat(o.VocoderSourceList); serr == nil {
			t.Error("the apt source was written before the refusal")
		}
		return err, o, f
	}
	t.Run("unsupported release", func(t *testing.T) {
		err, _, _ := run(t, func(o *Ops, _ map[string]resp) {
			os.WriteFile(o.OSRelease, []byte("VERSION_CODENAME=bullseye\n"), 0o644)
		})
		if !errors.Is(err, ErrVocoderUnsupported) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unsupported arch", func(t *testing.T) {
		err, _, _ := run(t, func(_ *Ops, r map[string]resp) { r["dpkg --print-architecture"] = resp{out: "i386\n"} })
		if !errors.Is(err, ErrVocoderUnsupported) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("clock", func(t *testing.T) {
		err, _, _ := run(t, func(o *Ops, _ map[string]resp) {
			o.Now = func() time.Time { return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC) }
		})
		if !errors.Is(err, ErrVocoderClockUnset) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		err, _, _ := run(t, func(o *Ops, _ map[string]resp) {
			other, _ := syntheticKey(9)
			o.HTTPGet = func(context.Context, string) ([]byte, error) { return other, nil }
		})
		if !errors.Is(err, ErrVocoderKeyMismatch) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("download fails", func(t *testing.T) {
		err, _, _ := run(t, func(o *Ops, _ map[string]resp) {
			o.HTTPGet = func(context.Context, string) ([]byte, error) { return nil, errors.New("offline") }
		})
		if err == nil || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("apt job running", func(t *testing.T) {
		err, _, _ := run(t, func(_ *Ops, r map[string]resp) {
			r["systemctl list-units"] = resp{out: loadedUnit}
			r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
		})
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("os check running", func(t *testing.T) {
		err, _, _ := run(t, func(o *Ops, _ map[string]resp) { o.osCheckMu.Lock() })
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("%v", err)
		}
	})
}

// The vocoder install unit counts as an apt job: it blocks os-apply and an
// update, and is blocked by them.
func TestVocoderJobBlocksOSApply(t *testing.T) {
	r := map[string]resp{"systemd-run": {}}
	o, _ := osOps(t, r)
	r["systemctl list-units"] = resp{out: loadedUnit + "nodespot-vocoder-install.service loaded active running x\n"}
	r["systemctl show nodespot-vocoder-install"] = resp{out: "ActiveState=active\nResult=success\n"}
	if _, err := o.OSApply(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
}

func TestVocoderOpsOverSocket(t *testing.T) {
	r := map[string]resp{cmdDpkgVocoder: {code: 1}, cmdActive: {code: 3}, "systemd-run": {}}
	o, _, _ := vocoderOps(t, r)
	c, _ := startServer(t, o, PeerUID, []uint32{0, uint32(os.Getuid())})
	ctx := context.Background()
	st, err := c.VocoderStatus(ctx)
	if err != nil || st.Installed || !st.Supported {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err = c.VocoderInstall(ctx); err != nil || st.Job != "running" {
		t.Fatalf("%+v %v", st, err)
	}
	// a refusal reaches the client with its code
	o.Now = func() time.Time { return time.Unix(0, 0) }
	o.resetJobUnit(vocoderJobUnit)
	o.startedAt[vocoderJobUnit] = time.Unix(0, 0)
	if _, err = c.VocoderInstall(ctx); err == nil || !strings.HasPrefix(err.Error(), "clock_unset") {
		t.Fatalf("%v", err)
	}
}

func TestVocoderStatusJobStarted(t *testing.T) {
	r := map[string]resp{cmdDpkgVocoder: {code: 1}, cmdActive: {code: 3}}
	o, _, _ := vocoderOps(t, r)
	r["systemctl list-units"] = resp{out: loadedUnit + "nodespot-vocoder-install.service loaded active running x\n"}
	r["systemctl show nodespot-vocoder-install"] = resp{out: "ActiveState=active\nResult=success\nInvocationID=abc\n"}
	r["systemctl show --property=ActiveEnterTimestamp"] = resp{out: "ActiveEnterTimestamp=@1790000000\n"}
	st, err := o.VocoderStatus(context.Background())
	if err != nil || st.Job != "running" || st.JobStarted != "2026-09-21T14:13:20Z" {
		t.Fatalf("%+v %v", st, err)
	}
	// no timestamp (unit never active): the field stays empty
	r["systemctl show --property=ActiveEnterTimestamp"] = resp{out: "ActiveEnterTimestamp=\n"}
	if st, _ = o.VocoderStatus(context.Background()); st.JobStarted != "" {
		t.Fatalf("%+v", st)
	}
	// no job: no start time, and no systemctl show call needed
	r["systemctl list-units"] = resp{}
	if st, _ = o.VocoderStatus(context.Background()); st.Job != "" || st.JobStarted != "" {
		t.Fatalf("%+v", st)
	}
}

func TestWaitVocoderInstall(t *testing.T) {
	ctx := context.Background()
	started := func(context.Context) (VocoderStatus, error) { return VocoderStatus{Job: "running"}, nil }
	seq := func(sts ...VocoderStatus) func(context.Context) (VocoderStatus, error) {
		i := 0
		return func(context.Context) (VocoderStatus, error) {
			st := sts[min(i, len(sts)-1)]
			i++
			return st, nil
		}
	}
	run := func(install, status func(context.Context) (VocoderStatus, error)) (string, error) {
		var buf bytes.Buffer
		err := waitVocoderInstall(ctx, &buf, install, status, time.Millisecond, time.Minute)
		return buf.String(), err
	}

	out, err := run(started, seq(VocoderStatus{Job: "running", JobLog: "a\n"},
		VocoderStatus{Job: "running", JobLog: "a\nb\n"},
		VocoderStatus{Job: "done", JobLog: "a\nb\nvocoder installed\n", Installed: true, Active: true}))
	if err != nil || out != "installing the software AMBE vocoder; this can take a few minutes\na\nb\nvocoder installed\n" {
		t.Errorf("success: %q %v", out, err)
	}

	already := func(context.Context) (VocoderStatus, error) { return VocoderStatus{Installed: true, Active: true}, nil }
	if out, err = run(already, nil); err != nil || !strings.Contains(out, "already installed") {
		t.Errorf("already: %q %v", out, err)
	}

	_, err = run(started, seq(VocoderStatus{Job: "failed", JobLog: "E: unable to locate package\n"}))
	if err == nil || !strings.Contains(err.Error(), "unable to locate package") {
		t.Errorf("failed job: %v", err)
	}
	if _, err = run(started, seq(VocoderStatus{Job: "done", JobLog: "ok\n"})); err == nil {
		t.Error("done but not active accepted")
	}
	if _, err = run(started, seq(VocoderStatus{})); err == nil || !strings.Contains(err.Error(), "vanished") {
		t.Errorf("lost job: %v", err)
	}

	for name, in := range map[string]error{
		"unsupported": fmt.Errorf("%w: %s", ErrVocoderUnsupported, `Debian "bullseye" is not supported (bookworm and trixie are)`),
		"clock":       ErrVocoderClockUnset,
	} {
		bad := func(context.Context) (VocoderStatus, error) { return VocoderStatus{}, in }
		_, err = run(bad, nil)
		if err == nil || strings.Contains(err.Error(), "not_supported") || strings.Contains(err.Error(), "clock_unset") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err = run(func(context.Context) (VocoderStatus, error) { return VocoderStatus{}, ErrBusy }, nil); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("busy: %v", err)
	}

	var buf bytes.Buffer
	err = waitVocoderInstall(ctx, &buf, started, seq(VocoderStatus{Job: "running"}), time.Millisecond, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("timeout: %v", err)
	}
}
