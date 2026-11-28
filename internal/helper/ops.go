package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/config"
)

const (
	pkgName     = "nodespot"
	upgradeUnit = "nodespot-upgrade"
	fwService   = "nodespot"
	logLines    = 50

	// DefaultSourceList is the apt source written by install.sh.
	DefaultSourceList = "/etc/apt/sources.list.d/nodespot.list"
)

// Ops implements the operations on top of a Runner.
type Ops struct {
	Run Runner
	// SourceList is the nodespot apt source file whose suite is the update
	// channel (default DefaultSourceList).
	SourceList string

	// Backup and restore locations (defaults in backup.go); injectable for
	// tests.
	NodeConfig   string // default node.DefaultConfigPath
	SA818State   string // default sa818.DefaultStatePath
	NodeUser     string // owner of the restored node files (default DefaultNodeUser)
	NodespotConf string // default DefaultNodespotConf (restored; read for the web port)
	ResolvConf   string // default DefaultResolvConf (net-status)
	OSRelease    string // default DefaultOSRelease
	// Svc holds the paths and timing of the services switch and the port 80
	// move (sys_svc.go, sys_webport.go); the zero value means the defaults.
	Svc SvcPaths
	// RF holds the boot files and devices of rf-prepare and
	// rf-device-check (sys_rf.go); the zero value means the defaults.
	RF RFPaths
	// Host info fallbacks of host-status (injectable for tests): the
	// static hostname file, the kernel hostname and the PATH lookup.
	HostnameFile string
	GetHostname  func() (string, error)
	LookPath     func(string) (string, error)
	// TempDir is where an uploaded archive is staged during a restore
	// (default /var/tmp).
	TempDir string
	// SelfPath is the helper binary (default: os.Executable).
	SelfPath string

	// applyMu serialises apply requests so that two concurrent requests
	// cannot both observe "not running".
	applyMu sync.Mutex

	jobMu     sync.Mutex
	cache     map[string]jobCache // by unit
	startedAt map[string]time.Time

	// RebootFile and RebootPkgsFile are the Debian reboot-required markers
	// (defaults below); injectable for tests.
	// Wifi holds the WiFi manager paths (sys_wifi.go); all optional.
	Wifi WifiPaths

	RebootFile     string
	RebootPkgsFile string
	// OSCheckTimeout bounds apt-get update of os-check (default 5 minutes).
	OSCheckTimeout time.Duration
	osCheckMu      sync.Mutex

	// VocoderKeyring, VocoderSourceList and VocoderDropin are the files of
	// the vocoder install (vocoder.go); injectable for tests.
	VocoderKeyring    string
	VocoderSourceList string
	VocoderDropin     string

	// HTTPGet fetches the keyring (tests inject; default: fixed-URL client,
	// redirects refused, 1 MiB cap). Now is the clock check's time source.
	HTTPGet func(ctx context.Context, url string) ([]byte, error)
	Now     func() time.Time
}

// jobCache remembers the classification of a finished job.
type jobCache struct {
	valid           bool
	key, state, log string
}

func (o *Ops) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// NewOps returns Ops using the real command runner.
func NewOps() *Ops { return &Ops{Run: ExecRunner{}} }

// Handle dispatches one request and returns the response value.
func (o *Ops) Handle(ctx context.Context, op string) (any, error) {
	return o.HandleRequest(ctx, Request{Op: op})
}

// HandleRequest dispatches a full request. Each typed field is accepted by
// its own operations only.
func (o *Ops) HandleRequest(ctx context.Context, req Request) (any, error) {
	op := req.Op
	if req.Terminal != nil {
		// the terminal ops are served by Server.handleTerminal, never from here.
		return nil, errors.New("invalid request: unexpected terminal field")
	}
	if isSysOp(op) {
		if req.Backup != nil || req.Restore != nil || req.Diag != nil {
			return nil, errors.New("invalid request: unexpected field")
		}
		return o.handleSys(ctx, req)
	}
	if req.Sys != nil {
		return nil, errors.New("invalid request: unexpected sys field")
	}
	if req.Diag != nil {
		return nil, errors.New("invalid request: unexpected diag field")
	}
	if req.Backup != nil && op != OpBackupCreate || req.Restore != nil && op != OpBackupRestore {
		return nil, errors.New("invalid request: unexpected field")
	}
	switch op {
	case OpBackupInfo:
		return o.BackupInfo(ctx)
	case OpUpdateStatus:
		return o.UpdateStatus(ctx)
	case OpUpdateCheck:
		return o.UpdateCheck(ctx)
	case OpUpdateApply:
		return o.UpdateApply(ctx)
	case OpChannelStable:
		return o.SetChannel(ctx, ChannelStable)
	case OpChannelBeta:
		return o.SetChannel(ctx, ChannelBeta)
	case OpOSCheck:
		return o.OSCheck(ctx)
	case OpOSStatus:
		return o.OSStatus(ctx)
	case OpOSApply:
		return o.OSApply(ctx)
	case OpReboot:
		return o.Reboot(ctx)
	case OpNodespotRestart:
		return o.NodespotRestart(ctx)
	case OpFirewallStatus:
		return o.FirewallStatus(ctx)
	case OpFirewallOpen:
		return o.FirewallOpen(ctx)
	}
	return nil, fmt.Errorf("unknown op")
}

// UpdateStatus reports the installed version (dpkg-query only) and the state
// of the upgrade job. It never runs apt: loading apt's package cache takes
// many seconds on a Raspberry Pi 3. Candidate and Upgradable stay empty; the
// server decides from latest.json.
func (o *Ops) UpdateStatus(ctx context.Context) (Status, error) {
	st, err := o.installed(ctx)
	if err != nil {
		return st, err
	}
	st.Job, st.JobLog = o.job(ctx)
	return st, nil
}

func (o *Ops) installed(ctx context.Context) (Status, error) {
	var st Status
	out, code, err := o.Run.Run(ctx, "dpkg-query", "-W", "-f=${Version}", pkgName)
	if err != nil {
		return st, fmt.Errorf("dpkg-query: %w", err)
	}
	if code == 0 {
		st.Installed = strings.TrimSpace(out)
	}
	st.Channel, _ = readChannel(o.sourceList())
	return st, nil
}

func (o *Ops) sourceList() string {
	if o.SourceList != "" {
		return o.SourceList
	}
	return DefaultSourceList
}

// sourceLine matches an nodespot apt source line: deb [options] URL SUITE ...
// where SUITE is one of the two channels. Group 1 is everything before the
// suite, 2 the suite, 3 the rest of the line.
var sourceLine = regexp.MustCompile(`^(\s*deb\s+(?:\[[^\]\n]*\]\s+)?\S+\s+)(stable|beta)(\s.*|)$`)

// readChannel returns the suite of the first nodespot line of the source file.
func readChannel(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := sourceLine.FindStringSubmatch(line); m != nil {
			return m[2], nil
		}
	}
	return "", errors.New("no nodespot source line found")
}

// SetChannel rewrites only the suite word of the nodespot line(s) of the source
// file (atomically, mode 0644) and returns the new status. It refuses while
// an upgrade runs. channel must be ChannelStable or ChannelBeta.
func (o *Ops) SetChannel(ctx context.Context, channel string) (Status, error) {
	if channel != ChannelStable && channel != ChannelBeta {
		return Status{}, errors.New("invalid channel")
	}
	o.applyMu.Lock()
	defer o.applyMu.Unlock()
	return o.setChannelLocked(ctx, channel)
}

// setChannelLocked is SetChannel with applyMu already held.
func (o *Ops) setChannelLocked(ctx context.Context, channel string) (Status, error) {
	if o.aptJobRunning(ctx) {
		return Status{}, ErrBusy
	}
	path := o.sourceList()
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{}, fmt.Errorf("cannot read the apt source: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if m := sourceLine.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + channel + m[3]
			found = true
		}
	}
	if !found {
		return Status{}, errors.New("no nodespot line found in the apt source")
	}
	if out := strings.Join(lines, "\n"); out != string(data) {
		if err := writeAtomic(path, []byte(out)); err != nil {
			return Status{}, fmt.Errorf("cannot write the apt source: %w", err)
		}
	}
	return o.installed(ctx)
}

// writeAtomic replaces path with data (temp file in the same directory,
// mode 0644, then rename). The temp name does not end in .list, so apt
// ignores it.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// justStarted is how long after update-apply a unit that systemd does not
// list yet is still reported as running.
const justStarted = 3 * time.Second

// job reads the state and log of the detached upgrade unit. systemctl show
// is cheap and runs every time; the journal is read only while the job runs,
// or once per finished job (the classification is cached by the unit's
// InvocationID / start time; a collected unit has neither, so the cache is
// dropped when a job is seen running or started).
func (o *Ops) job(ctx context.Context) (state, log string) { return o.jobUnit(ctx, upgradeUnit) }

// jobUnit is job for one of the two upgrade units (upgradeUnit,
// osUpgradeUnit).
func (o *Ops) jobUnit(ctx context.Context, unit string) (state, log string) {
	o.jobMu.Lock()
	defer o.jobMu.Unlock()
	// systemctl show on a unit that is not loaded makes systemd try to load it
	// from disk, which logs "Failed to open /run/systemd/transient/...: No such
	// file or directory" to the journal on every poll. list-units only
	// enumerates loaded units, so ask it first and skip show when absent (an
	// empty property set is what show reports for a missing unit anyway).
	list, lcode, err := o.Run.Run(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "--full", unit+".service")
	if err != nil {
		return JobIdle, ""
	}
	props := map[string]string{}
	if lcode != 0 || strings.Contains(list, unit+".service") {
		show, _, err := o.Run.Run(ctx, "systemctl", "show", unit, "-p", "ActiveState,Result,InvocationID,ExecMainStartTimestamp")
		if err != nil {
			return JobIdle, ""
		}
		props = ParseShow(show)
	}
	key := props["InvocationID"]
	if key == "" {
		key = props["ExecMainStartTimestamp"]
	}
	running := JobStateFrom(props, "") == JobRunning
	if running {
		o.setCache(unit, jobCache{})
	} else if c := o.cache[unit]; c.valid && c.key == key {
		return c.state, c.log
	} else if key == "" && time.Since(o.startedAt[unit]) < justStarted {
		return JobRunning, ""
	}
	out, code, err := o.Run.Run(ctx, "journalctl", "-u", unit, "-n", fmt.Sprint(logLines), "-o", "cat", "-a", "--no-pager")
	if err != nil || code != 0 {
		out = ""
	}
	log = LastLines(cleanLogUnit(out, unit), logLines)
	state = jobStateFrom(props, out, unit)
	if !running && err == nil && code == 0 {
		o.setCache(unit, jobCache{valid: true, key: key, state: state, log: log})
	}
	return state, log
}

// setCache stores the cache entry of unit (jobMu held).
func (o *Ops) setCache(unit string, c jobCache) {
	if o.cache == nil {
		o.cache = map[string]jobCache{}
	}
	o.cache[unit] = c
}

func (o *Ops) resetJob() { o.resetJobUnit(upgradeUnit) }

func (o *Ops) resetJobUnit(unit string) {
	o.jobMu.Lock()
	o.setCache(unit, jobCache{})
	if o.startedAt == nil {
		o.startedAt = map[string]time.Time{}
	}
	o.startedAt[unit] = time.Now()
	o.jobMu.Unlock()
}

// UpdateCheck is kept for compatibility with older servers. It used to run
// `apt-get update`; it now returns the same cheap status as UpdateStatus
// (the server checks latest.json itself).
func (o *Ops) UpdateCheck(ctx context.Context) (Status, error) { return o.UpdateStatus(ctx) }

// ErrBusy is returned when an upgrade is already running.
var ErrBusy = errors.New("an update is already running")

// upgradeScript is the fixed script of the upgrade unit; nothing a client
// sends reaches it. One unit does the (scoped) index refresh and the install,
// so the first apt run is not paid twice. Quiet, no recommends, no changelog
// reader (APT_LISTCHANGES_FRONTEND=none is set in the unit environment).
const upgradeScript = "apt-get update -o DPkg::Lock::Timeout=600 -o Dir::Etc::sourcelist=sources.list.d/nodespot.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 -q && " +
	"apt-get install -y -q -o DPkg::Lock::Timeout=600 --only-upgrade --no-install-recommends -o Dpkg::Options::=--force-confold -o Dpkg::Use-Pty=0 nodespot"

// upgradeArgv is the systemd-run command line of update-apply.
func upgradeArgv() []string {
	// --no-block: return as soon as the unit is queued. Without it systemd-run
	// waits for a oneshot unit to finish, so the helper would hold the apply
	// request open for the whole upgrade, and the postinst (which stops the
	// helper) would wait for the helper while the helper waits for the upgrade.
	return []string{"systemd-run", "--no-block", "--unit=" + upgradeUnit, "--collect", "--property=Type=oneshot",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=APT_LISTCHANGES_FRONTEND=none",
		"/bin/sh", "-c", upgradeScript}
}

// UpdateApply starts the upgrade in a transient systemd unit, detached from
// the helper and the server so it survives their restart.
func (o *Ops) UpdateApply(ctx context.Context) (Status, error) {
	o.applyMu.Lock()
	defer o.applyMu.Unlock()
	if o.aptJobRunning(ctx) {
		return Status{}, ErrBusy
	}
	out, code, err := o.Run.Run(ctx, upgradeArgv()...)
	o.resetJob()
	if err != nil {
		return Status{}, fmt.Errorf("systemd-run: %w", err)
	}
	if code != 0 {
		return Status{}, fmt.Errorf("systemd-run failed: %s", LastLines(out, 5))
	}
	st, err := o.installed(ctx)
	if err != nil {
		return st, err
	}
	// The unit was just started; report it as running even if systemd has not
	// registered it yet (no journal read: the log is still empty).
	st.Job = JobRunning
	return st, nil
}

// defaultPort is the port the packaged firewalld service "nodespot" covers.
const defaultPort = 8080

// webPort reads the port from the [server] listen setting of nodespot.conf. The
// port is never taken from the request: the operations stay argument-free.
func (o *Ops) webPort() (int, error) {
	c, err := config.Load(o.nodespotConf())
	if err != nil {
		return 0, err
	}
	return config.ListenPort(c.Listen)
}

// fwQuery succeeds when a firewall-cmd --query-* call exits 0, in the runtime
// configuration and then (a rule added with --permanent) the permanent one.
func (o *Ops) fwQuery(ctx context.Context, query string) bool {
	for _, args := range [][]string{{"firewall-cmd", query}, {"firewall-cmd", "--permanent", query}} {
		if _, code, err := o.Run.Run(ctx, args...); err == nil && code == 0 {
			return true
		}
	}
	return false
}

// FirewallStatus reports firewalld state for the configured web port: open
// when the nodespot service (port 8080 only) or the port itself is enabled.
func (o *Ops) FirewallStatus(ctx context.Context) (Firewall, error) {
	var fw Firewall
	out, code, err := o.Run.Run(ctx, "firewall-cmd", "--state")
	if err != nil {
		return fw, nil // firewall-cmd not installed
	}
	fw.Present = true
	fw.Running = code == 0 && strings.TrimSpace(out) == "running"
	port, perr := o.webPort()
	if perr != nil {
		return fw, perr
	}
	fw.Port = port
	if fw.Running {
		if port == defaultPort {
			fw.ServiceEnabled = o.fwQuery(ctx, "--query-service="+fwService)
		}
		if !fw.ServiceEnabled {
			fw.PortOpen = o.fwQuery(ctx, fmt.Sprintf("--query-port=%d/tcp", port))
		}
		fw.Open = fw.ServiceEnabled || fw.PortOpen
	}
	return fw, nil
}

// FirewallOpen opens the configured web port permanently and reloads: the
// nodespot service for 8080, else --add-port=<port>/tcp.
func (o *Ops) FirewallOpen(ctx context.Context) (Firewall, error) {
	fw, err := o.FirewallStatus(ctx)
	if !fw.Present {
		return fw, errors.New("firewalld is not installed")
	}
	if !fw.Running {
		return fw, errors.New("firewalld is not running")
	}
	if err != nil {
		return fw, err
	}
	add := "--add-service=" + fwService
	if fw.Port != defaultPort {
		add = fmt.Sprintf("--add-port=%d/tcp", fw.Port)
	}
	// Reload first so firewalld knows the service definition shipped by the
	// package, then add the rule permanently and reload again.
	for _, args := range [][]string{{"firewall-cmd", "--reload"}, {"firewall-cmd", "--permanent", add}, {"firewall-cmd", "--reload"}} {
		out, code, err := o.Run.Run(ctx, args...)
		if err != nil || code != 0 {
			return fw, fmt.Errorf("firewall-cmd %s failed: %s", args[1], LastLines(out, 3))
		}
	}
	return o.FirewallStatus(ctx)
}

// callTimeout bounds one request (apt-get update can be slow on a Pi).
const callTimeout = 150 * time.Second
