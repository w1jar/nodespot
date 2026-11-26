package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/legacy"
)

// The AMBE vocoder (md380-emu) is installed on demand, from the Modes page,
// the first time a mode that needs it is used (docs/NATIVE_NODE.md). The two
// ops here run fixed command lines: the package list, the paths and the units
// are constants, and nothing a client sends reaches a command line or a path.
//
//	vocoder-status   read-only: installed, active, platform, install job
//	vocoder-install  key, apt source, then the install job in a transient unit
//
// vocoder-install is idempotent: when the vocoder is installed and active it
// does nothing, while its job runs it reports the job, and otherwise it
// (re)starts the job, which repairs a half-done install.

// Operation names.
const (
	OpVocoderStatus  = "vocoder-status"
	OpVocoderInstall = "vocoder-install"
)

const (
	// VocoderKeyURL is the fixed keyring URL and VocoderKeyFingerprint the
	// only primary key it may hold (rsa4096).
	VocoderKeyURL         = legacy.KeyURL
	VocoderKeyFingerprint = "6DDFC33CD5FC678659642DF572147EC1E788D4C3"

	DefaultVocoderKeyring    = legacy.KeyringPath
	DefaultVocoderSourceList = legacy.SourcesPath

	// DefaultVocoderDropin is the unit drop-in that keeps the vocoder, which
	// binds every address, reachable from this machine only.
	DefaultVocoderDropin = "/etc/systemd/system/md380-emu.service.d/nodespot.conf"
	vocoderDropinText    = "[Service]\nIPAddressDeny=any\nIPAddressAllow=localhost\n"

	vocoderSuite      = "bookworm" // the repository uses it on trixie too
	vocoderKeyMaxSize = 1 << 20
	vocoderKeyTimeout = 30 * time.Second
	vocoderMinYear    = 2026 // apt rejects a Release file from the future

	vocoderUnit    = "md380-emu"
	vocoderPkg     = "md380-emu"
	vocoderQemuPkg = "qemu-user-static"
	// vocoderJobUnit is the transient unit of the install job.
	vocoderJobUnit = "nodespot-vocoder-install"
)

// Errors of the vocoder ops. The client gets the texts back (client.go) and
// the API maps them by their leading code.
var (
	ErrVocoderUnsupported = errors.New("not_supported: this host is not supported")
	ErrVocoderClockUnset  = errors.New("clock_unset: the system clock is not set; fix the date and time first")
	ErrVocoderKeyMismatch = errors.New("key_mismatch: the repository key does not have the expected fingerprint")
)

// VocoderStatus is the reply of vocoder-status and vocoder-install.
type VocoderStatus struct {
	// Supported: this OS release and architecture can install the vocoder.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Installed bool   `json:"installed"` // the md380-emu package is installed
	Active    bool   `json:"active"`    // md380-emu.service is active
	// Job is "", running, failed or done (the install job).
	Job    string `json:"job,omitempty"`
	JobLog string `json:"jobLog,omitempty"`
}

// VocoderPlatform decides whether the vocoder can be installed: host codename
// bookworm or trixie, architecture amd64, arm64 or armhf.
func VocoderPlatform(codename, arch string) (bool, string) {
	if codename != "bookworm" && codename != "trixie" {
		return false, fmt.Sprintf("Debian %q is not supported (bookworm and trixie are)", codename)
	}
	switch arch {
	case "amd64", "arm64", "armhf":
		return true, ""
	}
	return false, fmt.Sprintf("architecture %q is not supported (amd64, arm64 and armhf are)", arch)
}

// vocoderPackages is the install set: the vocoder emulator (an ARM binary)
// and, except on armhf where it runs natively, qemu user-mode emulation.
func vocoderPackages(arch string) []string {
	if arch == "armhf" {
		return []string{vocoderPkg}
	}
	return []string{vocoderPkg, vocoderQemuPkg}
}

func (o *Ops) vocoderKeyring() string {
	return orDefault(o.VocoderKeyring, DefaultVocoderKeyring)
}
func (o *Ops) vocoderSourceList() string {
	return orDefault(o.VocoderSourceList, DefaultVocoderSourceList)
}
func (o *Ops) vocoderDropin() string { return orDefault(o.VocoderDropin, DefaultVocoderDropin) }

// vocoderSourceLine is the apt source: the suite is always bookworm.
func vocoderSourceLine(keyring string) string {
	return "deb [signed-by=" + keyring + "] " + legacy.AptRepoURL + " " + vocoderSuite + " hamradio\n"
}

func init() {
	registerSysOp(OpVocoderStatus, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.VocoderStatus(ctx)
	})
	registerSysOp(OpVocoderInstall, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.VocoderInstall(ctx)
	})
}

// VocoderStatus reports whether md380-emu is installed and active, whether
// this host can install it and the state of the install job. It runs no apt.
func (o *Ops) VocoderStatus(ctx context.Context) (VocoderStatus, error) {
	var st VocoderStatus
	out, _, err := o.Run.Run(ctx, "dpkg-query", "-W", "-f=${db:Status-Status}", vocoderPkg)
	if err != nil {
		return st, fmt.Errorf("dpkg-query: %w", err)
	}
	st.Installed = strings.TrimSpace(out) == "installed"
	if _, code, err := o.Run.Run(ctx, "systemctl", "is-active", "--quiet", vocoderUnit+".service"); err == nil && code == 0 {
		st.Active = true
	}
	st.Arch = o.arch(ctx)
	st.Supported, st.Reason = VocoderPlatform(o.osInfo().Codename, st.Arch)
	state, log := o.jobUnit(ctx, vocoderJobUnit)
	switch state {
	case JobRunning:
		st.Job = "running"
	case JobFailed:
		st.Job = "failed"
	case JobSucceeded, JobNoop:
		st.Job = "done"
	}
	if st.Job != "" {
		st.JobLog = LastLines(log, logLines)
	}
	return st, nil
}

// vocoderScript is the fixed script of the install unit. It sequences apt and
// systemctl only; every step is safe to repeat.
func vocoderScript(arch, dropin string) string {
	apt := "-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o Dpkg::Use-Pty=0"
	return strings.Join([]string{
		"set -e",
		"rm -f " + legacy.AptListsGlob,
		"apt-get update -q -o DPkg::Lock::Timeout=600 --allow-releaseinfo-change",
		"apt-get install -y -q -o DPkg::Lock::Timeout=600 --no-install-recommends " + apt + " " + strings.Join(vocoderPackages(arch), " "),
		"mkdir -p " + shQuote(filepath.Dir(dropin)),
		"printf '%s' " + shQuote(vocoderDropinText) + " > " + shQuote(dropin),
		"systemctl daemon-reload",
		"systemctl enable --now " + vocoderUnit + ".service",
		"systemctl is-active --quiet " + vocoderUnit + ".service || { echo 'the vocoder did not start'; exit 1; }",
		"echo 'vocoder installed'",
	}, "\n")
}

func vocoderArgv(script string) []string {
	return []string{"systemd-run", "--no-block", "--unit=" + vocoderJobUnit, "--collect", "--property=Type=oneshot",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=APT_LISTCHANGES_FRONTEND=none", "--setenv=LC_ALL=C",
		"/bin/sh", "-c", script}
}

// vocoderHTTPGet is the default fetcher of the keyring: fixed URL given by the
// caller, redirects refused, size capped.
func vocoderHTTPGet(ctx context.Context, url string) ([]byte, error) {
	c := &http.Client{Timeout: vocoderKeyTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keyring download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, vocoderKeyMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > vocoderKeyMaxSize {
		return nil, errors.New("keyring download: too large")
	}
	return data, nil
}

// VocoderInstall prepares (platform, clock, key, apt source) and starts the
// install job in a transient unit, which survives a restart of the helper.
// Every refusal happens before anything is written. It does nothing when the
// vocoder is already installed and active, and reports the job when one is
// already running.
func (o *Ops) VocoderInstall(ctx context.Context) (VocoderStatus, error) {
	o.applyMu.Lock()
	defer o.applyMu.Unlock()
	st, err := o.VocoderStatus(ctx)
	if err != nil {
		return st, err
	}
	if st.Job == "running" || st.Installed && st.Active {
		return st, nil
	}
	if o.aptJobRunning(ctx) {
		return VocoderStatus{}, ErrBusy
	}
	if !o.osCheckMu.TryLock() { // an OS check holds the apt locks
		return VocoderStatus{}, ErrBusy
	}
	o.osCheckMu.Unlock()
	if !st.Supported {
		return VocoderStatus{}, fmt.Errorf("%w: %s", ErrVocoderUnsupported, st.Reason)
	}
	if o.now().Year() < vocoderMinYear {
		return VocoderStatus{}, ErrVocoderClockUnset
	}
	get := o.HTTPGet
	if get == nil {
		get = vocoderHTTPGet
	}
	raw, err := get(ctx, VocoderKeyURL)
	if err != nil {
		return VocoderStatus{}, fmt.Errorf("cannot download the package repository keyring: %w", err)
	}
	bin, err := VerifyVocoderKeyring(raw)
	if err != nil {
		return VocoderStatus{}, err
	}
	for _, p := range []string{o.vocoderKeyring(), o.vocoderSourceList()} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return VocoderStatus{}, fmt.Errorf("cannot create %s: %w", filepath.Dir(p), err)
		}
	}
	if err := writeAtomic(o.vocoderKeyring(), bin); err != nil {
		return VocoderStatus{}, fmt.Errorf("cannot write the keyring: %w", err)
	}
	if err := writeAtomic(o.vocoderSourceList(), []byte(vocoderSourceLine(o.vocoderKeyring()))); err != nil {
		return VocoderStatus{}, fmt.Errorf("cannot write the apt source: %w", err)
	}
	out, code, err := o.Run.Run(ctx, vocoderArgv(vocoderScript(st.Arch, o.vocoderDropin()))...)
	o.resetJobUnit(vocoderJobUnit)
	if err != nil {
		return VocoderStatus{}, fmt.Errorf("systemd-run: %w", err)
	}
	if code != 0 {
		return VocoderStatus{}, fmt.Errorf("systemd-run failed: %s", LastLines(out, 5))
	}
	st.Job, st.JobLog = "running", ""
	return st, nil
}

// VocoderStatus calls vocoder-status.
func (c *Client) VocoderStatus(ctx context.Context) (VocoderStatus, error) {
	var v VocoderStatus
	return v, c.sys(ctx, OpVocoderStatus, nil, &v, 1<<20)
}

// VocoderInstall calls vocoder-install.
func (c *Client) VocoderInstall(ctx context.Context) (VocoderStatus, error) {
	var v VocoderStatus
	return v, c.sys(ctx, OpVocoderInstall, nil, &v, 1<<20)
}

// VocoderStatus simulates the vocoder as installed (nothing happens).
func (d *DemoSystem) VocoderStatus(context.Context) (VocoderStatus, error) {
	return VocoderStatus{Supported: true, Installed: true, Active: true}, nil
}

// VocoderInstall simulates the install (nothing happens).
func (d *DemoSystem) VocoderInstall(ctx context.Context) (VocoderStatus, error) {
	return d.VocoderStatus(ctx)
}
