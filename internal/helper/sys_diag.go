package helper

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/w1jar/nodespot/internal/diag"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// Diagnostics bundle (docs/PLAN-1.3.0.md section 12): a .tar.gz of versions,
// journal excerpts, service and network state, disk and memory, and the
// config files, with secrets removed. The helper reads what needs root,
// builds the archive in a private temporary directory and streams it to the
// API process, which streams it to the browser.
//
// Like the backup, the op is a stream op: after the request line the answer
// is either one JSON error line ("{...}") or the raw gzip stream (magic
// bytes 0x1f 0x8b), told apart by the first byte.
const OpDiagCollect = "diag-collect"

// Limits.
const (
	// DiagTimeBudget bounds the whole collection; what is not done by then is
	// skipped and listed in the README.
	DiagTimeBudget = 90 * time.Second
	// DiagMaxTotal bounds the uncompressed size of all files. The compressed
	// archive is smaller.
	DiagMaxTotal = 12 << 20
	// DiagMaxReport and DiagMaxAudit bound the texts the API sends.
	DiagMaxReport = 768 << 10
	DiagMaxAudit  = 256 << 10

	diagMaxFile      = 256 << 10 // one journal or command output (the tail is kept)
	diagMaxConf      = 512 << 10 // one config file
	diagMaxConfTotal = 4 << 20
	diagMaxConfFiles = 120
	diagJournalLines = 500
	diagWriteTimeout = 5 * time.Minute // the browser may be slow
)

// DiagRequest is the argument of diag-collect.
type DiagRequest struct {
	// MaskIdentity replaces the node's own callsigns, public IP addresses and
	// host names.
	MaskIdentity bool `json:"maskIdentity"`
	// Version is the nodespot version string.
	Version string `json:"version,omitempty"`
	// Report and Audit are the texts of report.txt and audit.txt, built by
	// the API process from data the helper cannot see. They are redacted again here.
	Report string `json:"report,omitempty"`
	Audit  string `json:"audit,omitempty"`
	// Secrets are literals removed wherever they occur (the AMI secret);
	// Callsigns are the node's own callsigns.
	Secrets   []string `json:"secrets,omitempty"`
	Callsigns []string `json:"callsigns,omitempty"`
}

var (
	reDiagVersion  = regexp.MustCompile(`^[0-9A-Za-z.+~:\-]{1,40}$`)
	reDiagCallsign = regexp.MustCompile(`^[A-Za-z0-9/\-]{2,16}$`)
)

func (r *DiagRequest) validate() error {
	if r.Version != "" && !reDiagVersion.MatchString(r.Version) {
		return errors.New("invalid version")
	}
	if len(r.Report) > DiagMaxReport || len(r.Audit) > DiagMaxAudit {
		return errors.New("text too large")
	}
	if len(r.Secrets) > 20 || len(r.Callsigns) > 20 {
		return errors.New("too many values")
	}
	for _, s := range r.Secrets {
		if len(s) > 256 {
			return errors.New("secret too long")
		}
	}
	for _, c := range r.Callsigns {
		if !reDiagCallsign.MatchString(c) {
			return errors.New("invalid callsign")
		}
	}
	return nil
}

// ErrDiagBusy is returned while another bundle is being built.
var ErrDiagBusy = errors.New("a diagnostics bundle is already being built")

// diagBusy admits one collection at a time.
var diagBusy atomic.Bool

// diagTempBases are the directories tried, in order, for the private build
// directory. /run is a tmpfs on the node, so nothing sensitive reaches the SD
// card; /var/tmp is the fallback. Tests replace it.
var diagTempBases = []string{"/run", "/var/tmp"}

// DiagItem describes one part of the bundle for the UI preview.
type DiagItem struct {
	Name string `json:"name"`
	What string `json:"what"`
}

// DiagPlan is what GET /api/system/diagnostics shows before the download.
type DiagPlan struct {
	Items    []DiagItem `json:"items"`
	Excluded []string   `json:"excluded"`
	Redacted []string   `json:"redacted"`
	// MaxBytes is the cap on the uncompressed size, TimeLimit the cap on
	// the build time in seconds.
	MaxBytes  int `json:"maxBytes"`
	TimeLimit int `json:"timeLimit"`
}

// DiagPlanInfo returns the static description of the bundle.
func DiagPlanInfo() DiagPlan {
	return DiagPlan{
		Items: []DiagItem{
			{"README.txt", "What was collected, what was redacted, and what could not be collected"},
			{"report.txt", "nodespot status report: versions, the node's modes and links, updates, recent nodespot log"},
			{"versions.txt", "nodespot, operating system, kernel, and the vocoder and nodespot package versions"},
			{"system.txt", "Uptime and load, memory, disk use"},
			{"services.txt", "State of the node, nodespot, network and time services, and listening ports"},
			{"network.txt", "Interfaces, addresses, default gateway, DNS servers (no WiFi passwords or saved networks)"},
			{"journal/<unit>.txt", fmt.Sprintf("Last %d journal lines of %s", diagJournalLines, strings.Join(logUnits(), ", "))},
			{"config/", "nodespot.conf, the node config without its mode passwords, and the SA818 settings, with secret values replaced by <redacted>"},
			{"audit.txt", "The last 200 audit log entries"},
		},
		Excluded: []string{
			"/etc/shadow and every other password file",
			"private keys and certificates",
			"the nodespot database, session tokens and the setup code",
			"WiFi passwords and NetworkManager connection files",
			"terminal data",
		},
		Redacted: []string{
			"the node's mode passwords (AllStar, EchoLink, DMR and the others), wherever they show up",
			"nodespot's own secrets and session data",
			"WiFi pre-shared keys",
			"tokens, cookies, bearer headers and passwords in URLs",
			"every password found in a config file, wherever else it shows up (for example in a log line)",
		},
		MaxBytes:  DiagMaxTotal,
		TimeLimit: int(DiagTimeBudget / time.Second),
	}
}

// DiagBundle is a finished bundle: Read it to the end, then Close it, which
// removes the build directory.
type DiagBundle struct {
	File *os.File
	Size int64
	dir  string
}

// Read implements io.Reader.
func (b *DiagBundle) Read(p []byte) (int, error) { return b.File.Read(p) }

// Close closes the archive and removes the build directory.
func (b *DiagBundle) Close() error {
	err := b.File.Close()
	if rerr := os.RemoveAll(b.dir); err == nil {
		err = rerr
	}
	return err
}

// diagEntry is one file of the bundle as the README lists it.
type diagEntry struct {
	name, desc string
	size       int
	counts     diag.Counts
	note       string
}

// diagBuilder stages files in a private directory, redacting each one, and
// packs them. It does not know where the files come from.
type diagBuilder struct {
	dir     string
	opts    diag.BundleOptions
	now     time.Time
	version string
	entries []diagEntry
	skipped []string
	total   int
}

func newDiagBuilder(bases []string, opts diag.BundleOptions, now time.Time, version string) (*diagBuilder, error) {
	var lastErr error
	for _, base := range bases {
		dir, err := os.MkdirTemp(base, "nodespot-diag-")
		if err == nil {
			_ = os.Chmod(dir, 0o700)
			return &diagBuilder{dir: dir, opts: opts, now: now, version: version}, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("cannot create a temporary directory: %w", lastErr)
}

func (b *diagBuilder) cleanup() { _ = os.RemoveAll(b.dir) }

var errBadDiagName = errors.New("bad name")

func safeDiagName(name string) bool {
	return name != "" && path.Clean(name) == name && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && name != ".."
}

// add redacts text and stages it as name. The tail is kept when text is
// longer than max. A file that would pass the total size cap is skipped.
func (b *diagBuilder) add(name, desc, text string, kind diag.Kind, max int) {
	if !safeDiagName(name) {
		b.skip(name, "internal error: bad file name")
		return
	}
	note := ""
	if t, cut := capTail(text, max); cut {
		text, note = t, "truncated to the last "+fmt.Sprint(max>>10)+" KiB"
	}
	if b.total >= DiagMaxTotal { // no room left: do not spend time redacting
		b.skip(name, "size limit reached")
		return
	}
	text, counts := diag.RedactBundleText(text, kind, b.opts)
	if b.total+len(text) > DiagMaxTotal {
		b.skip(name, "size limit reached")
		return
	}
	full := filepath.Join(b.dir, "stage", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		b.skip(name, "could not stage")
		return
	}
	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		b.skip(name, "could not stage")
		return
	}
	b.total += len(text)
	b.entries = append(b.entries, diagEntry{name: name, desc: desc, size: len(text), counts: counts, note: note})
}

func (b *diagBuilder) skip(name, why string) { b.skipped = append(b.skipped, name+": "+why) }

// readme renders README.txt from what has been staged.
func (b *diagBuilder) readme() string {
	var w strings.Builder
	w.WriteString("nodespot diagnostics bundle\n========================\n\n")
	fmt.Fprintf(&w, "Created:  %s\n", b.now.UTC().Format(time.RFC3339))
	if b.version != "" {
		fmt.Fprintf(&w, "nodespot:    %s\n", b.version)
	}
	if b.opts.MaskIdentity {
		w.WriteString("Identity: MASKED. This node's own callsigns, public IP addresses and host names are replaced\n" +
			"          by <callsign>, <own-ip> and <own-host>; location and e-mail keys of config files by <masked>.\n")
	} else {
		w.WriteString("Identity: NOT masked. Callsigns and addresses appear as they are on the node.\n")
	}
	w.WriteString("\nRead every file before you post this bundle publicly. Redaction is automatic and cannot know\n" +
		"every way a secret can appear; if you find one, change that secret and tell the people you sent it to.\n\n")
	w.WriteString("Files\n-----\n")
	sorted := append([]diagEntry(nil), b.entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	fmt.Fprintf(&w, "%-44s %9s %9s %9s\n", "file", "bytes", "secrets", "identity")
	fmt.Fprintf(&w, "%-44s %9s %9s %9s   %s\n", "README.txt", "", "", "", "this file")
	var sec, id int
	for _, e := range sorted {
		fmt.Fprintf(&w, "%-44s %9d %9d %9d", e.name, e.size, e.counts.Secrets, e.counts.Identity)
		if e.note != "" {
			w.WriteString("   (" + e.note + ")")
		}
		w.WriteByte('\n')
		sec += e.counts.Secrets
		id += e.counts.Identity
	}
	fmt.Fprintf(&w, "\n\"secrets\" and \"identity\" count the values that were replaced in each file.\nTotal: %d secret values redacted, %d identity values masked.\n", sec, id)
	w.WriteString("\nWhat each file holds\n--------------------\n")
	for _, e := range sorted {
		fmt.Fprintf(&w, "%s: %s\n", e.name, e.desc)
	}
	w.WriteString("\nRedacted in every file\n----------------------\n")
	for _, r := range DiagPlanInfo().Redacted {
		w.WriteString("- " + r + "\n")
	}
	w.WriteString("\nNever collected\n---------------\n")
	for _, r := range DiagPlanInfo().Excluded {
		w.WriteString("- " + r + "\n")
	}
	if len(b.skipped) > 0 {
		w.WriteString("\nNot collected or incomplete\n---------------------------\n")
		sk := append([]string(nil), b.skipped...)
		sort.Strings(sk)
		for _, s := range sk {
			w.WriteString("- " + s + "\n")
		}
	}
	return w.String()
}

// pack writes README.txt and the staged files to bundle.tar.gz in the build
// directory and returns the open archive.
func (b *diagBuilder) pack() (*DiagBundle, error) {
	archive := filepath.Join(b.dir, "bundle.tar.gz")
	f, err := os.OpenFile(archive, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*DiagBundle, error) {
		f.Close()
		return nil, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	put := func(name string, data []byte) error {
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: b.now, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := put("README.txt", []byte(b.readme())); err != nil {
		return fail(err)
	}
	sorted := append([]diagEntry(nil), b.entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, e := range sorted {
		data, err := os.ReadFile(filepath.Join(b.dir, "stage", filepath.FromSlash(e.name)))
		if err != nil {
			return fail(err)
		}
		if err := put(e.name, data); err != nil {
			return fail(err)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return &DiagBundle{File: f, Size: st.Size(), dir: b.dir}, nil
}

// ---- collection

// diagConf is one config file read for the bundle.
type diagConf struct {
	name, desc, text string
	secrets          []string
	callsigns        []string
}

var errDiagExcluded = errors.New("excluded")

// readDiagFile reads a regular file of at most max bytes without following
// a symbolic link; binary files are refused.
func readDiagFile(p string, max int) (string, error) {
	if diag.ExcludedPath(p) {
		return "", errDiagExcluded
	}
	fd, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer fd.Close()
	st, err := fd.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(fd, int64(max)+1))
	if err != nil {
		return "", err
	}
	if len(data) > max {
		return "", errors.New("too large")
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return "", errors.New("binary file")
	}
	return string(data), nil
}

// listDiagDir returns the regular files directly in dir (symbolic links and
// directories are skipped) that pass ExcludedPath and match keep.
func listDiagDir(dir string, keep func(name string) bool) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") || !keep(e.Name()) {
			continue
		}
		if p := filepath.Join(dir, e.Name()); !diag.ExcludedPath(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// diagConfigs reads the config files that go into the bundle.
func (o *Ops) diagConfigs(b *diagBuilder) []diagConf {
	var out []diagConf
	budget := diagMaxConfTotal
	read := func(p, name, desc string) {
		if len(out) >= diagMaxConfFiles {
			b.skip(name, "too many config files")
			return
		}
		text, err := readDiagFile(p, diagMaxConf)
		switch {
		case err == nil:
			if len(text) > budget {
				b.skip(name, "config size limit reached")
				return
			}
			budget -= len(text)
			out = append(out, diagConf{name: name, desc: desc, text: text})
		case errors.Is(err, os.ErrNotExist):
		case errors.Is(err, errDiagExcluded):
		default:
			b.skip(name, err.Error())
		}
	}
	read(o.nodespotConf(), "config/nodespot.conf", "nodespot configuration")
	read(o.sa818State(), "config/sa818.json", "SA818 radio module settings")
	if c, err := o.diagNodeConfig(); err == nil {
		out = append(out, c)
	} else if !errors.Is(err, os.ErrNotExist) {
		b.skip("config/node.json", err.Error())
	}
	return out
}

// diagNodeConfig returns the node config with every mode secret removed.
// It carries the secrets themselves (they are redacted wherever else they
// show up, for example in a log line) and the callsigns (masked on request).
func (o *Ops) diagNodeConfig() (diagConf, error) {
	b, _, err := readRegular(o.nodeConfig(), maxNodeFile)
	if err != nil {
		return diagConf{}, err
	}
	c, err := parseNodeConfig(b)
	if err != nil {
		return diagConf{}, err
	}
	calls := []string{}
	if c.Station.Callsign != "" {
		calls = append(calls, c.Station.Callsign)
	}
	var secrets []string
	for name, s := range c.Modes {
		if sp, ok := mode.Lookup(name); ok {
			secrets = append(secrets, sp.Secrets(s)...)
		} else {
			for _, v := range s.Values {
				secrets = append(secrets, v)
			}
		}
	}
	for _, s := range c.Modes {
		if v := s.Values["callsign"]; v != "" {
			calls = append(calls, v)
		}
	}
	stripSecrets(c)
	out, err := encodeNodeConfig(c)
	if err != nil {
		return diagConf{}, err
	}
	return diagConf{name: "config/node.json", desc: "Node configuration (mode passwords removed)", text: string(out),
		secrets: secrets, callsigns: calls}, nil
}

// ownPublicIPs lists the public addresses of this node's interfaces.
func ownPublicIPs(n NetStatus) []string {
	var out []string
	for _, i := range n.Interfaces {
		for _, a := range i.Addrs {
			if ip := net.ParseIP(a.Addr); ip != nil && diag.IsPublicIP(ip) {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

func diagOK(ctx context.Context) bool { return ctx.Err() == nil }

// DiagCollect builds the bundle. The caller reads the result and closes it.
func (o *Ops) DiagCollect(ctx context.Context, req *DiagRequest) (*DiagBundle, error) {
	if req == nil {
		req = &DiagRequest{}
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if !diagBusy.CompareAndSwap(false, true) {
		return nil, ErrDiagBusy
	}
	defer diagBusy.Store(false)
	ctx, cancel := context.WithTimeout(ctx, DiagTimeBudget)
	defer cancel()

	// Phase 1: read the configs and the addresses; what they reveal (their
	// passwords, callsigns, public addresses) drives the redaction of
	// everything else.
	b0 := &diagBuilder{} // collects skips of the config read before the real builder exists
	confs := o.diagConfigs(b0)
	ns := o.NetStatus(ctx)
	var allConf strings.Builder
	var confSecrets, confCalls []string
	for _, c := range confs {
		confSecrets = append(confSecrets, c.secrets...)
		confCalls = append(confCalls, c.callsigns...)
		allConf.WriteString(c.text)
		allConf.WriteByte('\n')
	}
	id := diag.ExtractIdentity(allConf.String())
	id.Callsigns = append(append(id.Callsigns, confCalls...), req.Callsigns...)
	id.IPs = append(id.IPs, ownPublicIPs(ns)...)
	opts := diag.BundleOptions{
		MaskIdentity: req.MaskIdentity,
		Secrets:      append(append(diag.ExtractSecrets(allConf.String()), confSecrets...), req.Secrets...),
		Identity:     id,
	}
	b, err := newDiagBuilder(diagTempBases, opts, o.now(), req.Version)
	if err != nil {
		return nil, err
	}
	b.skipped = append(b.skipped, b0.skipped...)
	ok := false
	defer func() {
		if !ok {
			b.cleanup()
		}
	}()

	// Phase 2: the files.
	if req.Report != "" {
		b.add("report.txt", "nodespot status report", req.Report, diag.KindText, DiagMaxReport)
	} else {
		b.skip("report.txt", "not provided")
	}
	b.add("versions.txt", "Versions", o.diagVersions(ctx, req.Version), diag.KindText, diagMaxFile)
	b.add("system.txt", "Uptime, memory and disk", o.diagSystem(ctx), diag.KindText, diagMaxFile)
	b.add("services.txt", "Service states and listening ports", o.diagServices(ctx), diag.KindText, diagMaxFile)
	b.add("network.txt", "Network status", o.diagNetwork(ctx, ns), diag.KindText, diagMaxFile)
	for _, u := range logUnits() {
		if !diagOK(ctx) {
			b.skip("journal/"+u+".txt", "time limit reached")
			continue
		}
		b.add("journal/"+u+".txt", "Journal of "+u, o.diagJournal(ctx, u), diag.KindText, diagMaxFile)
	}
	for _, c := range confs {
		b.add(c.name, c.desc, c.text, diag.KindConf, diagMaxConf)
	}
	if req.Audit != "" {
		b.add("audit.txt", "Recent audit log entries", req.Audit, diag.KindText, DiagMaxAudit)
	} else {
		b.skip("audit.txt", "not provided")
	}
	if !diagOK(ctx) {
		b.skip("(whole bundle)", "time limit reached; later parts may be missing")
	}
	bundle, err := b.pack()
	if err != nil {
		return nil, err
	}
	ok = true
	return bundle, nil
}

// cmdText returns the output of a command, or a note when it cannot be run.
func (o *Ops) cmdText(ctx context.Context, argv ...string) string {
	out, code, err := o.Run.Run(ctx, argv...)
	if err != nil {
		return "(not available: " + argv[0] + " could not be run)\n"
	}
	if code != 0 {
		return fmt.Sprintf("(%s exited with status %d)\n%s", argv[0], code, LastLines(out, 20)+"\n")
	}
	return out
}

func (o *Ops) diagVersions(ctx context.Context, nodespot string) string {
	var w strings.Builder
	if nodespot != "" {
		fmt.Fprintf(&w, "nodespot: %s\n", nodespot)
	}
	if b, err := os.ReadFile(o.osRelease()); err == nil {
		fmt.Fprintf(&w, "OS: %s\n", orDefault(diag.ParseOSRelease(string(b)), "(unknown)"))
	}
	if out, code, err := o.Run.Run(ctx, "uname", "-srm"); err == nil && code == 0 {
		fmt.Fprintf(&w, "Kernel: %s\n", strings.TrimSpace(out))
	}
	w.WriteString("\nInstalled packages\n")
	out, _, err := o.Run.Run(ctx, "dpkg-query", "-W", "-f=${db:Status-Abbrev} ${Package} ${Version}\\n",
		"md380*", "nodespot*")
	if err != nil {
		w.WriteString("(not available: dpkg-query could not be run)\n")
		return w.String()
	}
	var pk []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && strings.HasPrefix(f[0], "ii") {
			pk = append(pk, f[1]+" "+f[2])
		}
	}
	sort.Strings(pk)
	if len(pk) == 0 {
		w.WriteString("(none found)\n")
	}
	for _, p := range pk {
		w.WriteString(p + "\n")
	}
	return w.String()
}

func (o *Ops) diagSystem(ctx context.Context) string {
	var w strings.Builder
	for _, c := range [][]string{{"uptime"}, {"free", "-m"}, {"df", "-hP"}} {
		fmt.Fprintf(&w, "$ %s\n%s\n", strings.Join(c, " "), strings.TrimRight(o.cmdText(ctx, c...), "\n"))
	}
	return w.String()
}

func (o *Ops) diagServices(ctx context.Context) string {
	var w strings.Builder
	sv := o.ServicesStatus(ctx)
	if !sv.Available {
		w.WriteString("Services: (not available: systemctl could not be run)\n")
	}
	for _, s := range sv.Services {
		fmt.Fprintf(&w, "%-28s %-10s %-9s %-9s %s\n", s.Unit, s.Active, s.Sub, s.Enabled, s.Since)
	}
	fmt.Fprintf(&w, "\n$ ss -tulnH\n%s", o.cmdText(ctx, "ss", "-tulnH"))
	return w.String()
}

func (o *Ops) diagNetwork(ctx context.Context, n NetStatus) string {
	var w strings.Builder
	if !n.Available {
		return "(not available: the ip tool could not be run)\n"
	}
	for _, i := range n.Interfaces {
		fmt.Fprintf(&w, "%s: %s, %s, mac %s, mtu %d\n", i.Name, i.State, i.Kind, i.MAC, i.MTU)
		for _, a := range i.Addrs {
			fmt.Fprintf(&w, "    %s %s/%d scope %s\n", a.Family, a.Addr, a.Prefix, a.Scope)
		}
	}
	for _, g := range n.Gateways {
		fmt.Fprintf(&w, "default gateway: %s via %s metric %d\n", g.Gateway, g.Dev, g.Metric)
	}
	fmt.Fprintf(&w, "DNS (%s): %s\n", n.DNSSource, strings.Join(n.DNS, " "))
	// Device list only: no connection names, SSIDs or passwords.
	if out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE", "device"); err == nil && code == 0 {
		w.WriteString("\nNetworkManager devices (device:type:state)\n" + out)
	}
	return w.String()
}

func (o *Ops) diagJournal(ctx context.Context, unit string) string {
	argv := []string{"journalctl", "-u", unit, "-n", fmt.Sprint(diagJournalLines), "--no-pager", "-o", "short-iso"}
	out, code, err := o.Run.Run(ctx, argv...)
	switch {
	case err != nil:
		return "(not available: journalctl could not be run)\n"
	case code != 0:
		return "(journalctl failed: " + LastLines(out, 3) + ")\n"
	case strings.TrimSpace(out) == "":
		return "(no entries)\n"
	}
	return out
}

// ---- stream (helper side)

// handleDiagStream serves diag-collect: an error line, or the gzip stream.
func (s *Server) handleDiagStream(ctx context.Context, conn net.Conn, req Request, uid uint32) {
	reply := func(err error) { _ = json.NewEncoder(conn).Encode(errorResponse{Error: err.Error()}) }
	b, err := s.Ops.DiagCollect(ctx, req.Diag)
	if err != nil {
		s.logf("uid %d: %s: error: %v", uid, req.Op, err)
		reply(err)
		return
	}
	defer b.Close()
	_ = conn.SetDeadline(time.Now().Add(diagWriteTimeout))
	bw := bufio.NewWriterSize(conn, 64<<10)
	if _, err := io.Copy(bw, b); err != nil {
		s.logf("uid %d: %s: write: %v", uid, req.Op, err)
		return
	}
	if err := bw.Flush(); err != nil {
		s.logf("uid %d: %s: write: %v", uid, req.Op, err)
		return
	}
	s.logf("uid %d: %s ok (%d bytes, mask=%v)", uid, req.Op, b.Size, req.Diag != nil && req.Diag.MaskIdentity)
}
