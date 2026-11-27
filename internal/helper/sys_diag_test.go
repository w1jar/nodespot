package helper

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Planted values (placeholders). None may appear in any file of a bundle.
var diagPlanted = []string{
	"example-pass-iax", "example-pass-echo", "example-pass-bm",
	"example-pass-nodespot", "example-pass-wifi", "example-pass-req",
	"example-pass-journal", "example-pass-url", "example-pass-bearer",
}

const (
	diagNode = `{
  "version": 1,
  "station": {"callsign": "N0CALL", "radio": {"frequencyMHz": 446.1}},
  "modes": {
    "allstar": {"values": {"node": "1999", "password": "example-pass-iax"}},
    "echolink": {"values": {"callsign": "N0CALL-L", "password": "example-pass-echo", "location": "Example Town"}},
    "bm": {"values": {"dmr_id": "1234567", "password": "example-pass-bm", "address": "master.example.net"}}
  }
}
`
	diagSA818    = `{"version":1,"settings":{"bandwidth":0,"rx_mhz":446.1,"tx_mhz":446.1}}` + "\n"
	diagNodespot = "[server]\nlisten=:8080\npublic_url=https://node.example.net/\n[smtp]\npassword=example-pass-nodespot\n"
	diagWifi     = "[wifi-security]\npsk=example-pass-wifi\n"
)

type diagFixture struct {
	o    *Ops
	f    *fakeRunner
	root string
}

func newDiagFixture(t *testing.T) *diagFixture {
	t.Helper()
	root := t.TempDir()
	w := func(rel, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	nodeConf := w("etc-nodespot/node.json", diagNode)
	sa := w("etc-nodespot/sa818.json", diagSA818)
	nodespot := w("etc-nodespot/nodespot.conf", diagNodespot)
	// Things that must never be collected:
	w("etc-nodespot/nodespot.db", "SQLITE example-pass-nodespot")
	w("etc-nm/ExampleNet.nmconnection", diagWifi)
	w("os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n")

	rules := map[string]resp{
		"uname -srm":  {out: "Linux 6.1.0 aarch64\n"},
		"dpkg-query":  {out: "ii  md380-emu 1.0\nrc  nodespot-old 1.0\nii  nodespot 1.3.4\n"},
		"uptime":      {out: " 12:00:00 up 1 day, load average: 0.01, 0.02, 0.03\n"},
		"free -m":     {out: "              total used\nMem: 1000 100\n"},
		"df -hP":      {out: "Filesystem Size Used\n/dev/root 30G 5G\n"},
		"ss -tulnH":   {out: "tcp LISTEN 0 4096 192.0.2.10:8080 0.0.0.0:*\n"},
		"systemctl":   {out: fixture(t, "systemctl_show_units.txt")},
		"ip -j addr":  {out: fixture(t, "ip_addr.json")},
		"ip -j route": {out: fixture(t, "ip_route.json")},
		"ip -j -6":    {out: "[]"},
		"nmcli":       {out: "wlan0:wifi:connected\n"},
		"journalctl -u nodespot-node": {out: "2026-11-20T12:00:00+0000 node2001 nodespot-node[1]: N0CALL from 198.51.100.7 rejected password=example-pass-journal\n" +
			"allstar: registration failed with example-pass-iax\nfetch https://user:example-pass-url@host.example.net/x\nAuthorization: Bearer example-pass-bearer\n"},
		"journalctl": {out: "2026-11-20T12:00:00+0000 node2001 x[1]: ok\n"},
	}
	f := &fakeRunner{rules: rules}
	o := &Ops{Run: f, Now: func() time.Time { return time.Date(2026, 11, 20, 15, 15, 2, 0, time.UTC) },
		NodeConfig: nodeConf, SA818State: sa, NodespotConf: nodespot,
		OSRelease:  filepath.Join(root, "os-release"),
		ResolvConf: filepath.Join("testdata", "resolv.conf")}
	old := diagTempBases
	diagTempBases = []string{filepath.Join(root, "tmp")}
	if err := os.MkdirAll(diagTempBases[0], 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { diagTempBases = old })
	return &diagFixture{o: o, f: f, root: root}
}

// readBundle returns the files of a .tar.gz stream in archive order.
func readBundle(t *testing.T, r io.Reader) (names []string, files map[string]string) {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files = map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		names = append(names, h.Name)
		files[h.Name] = string(b)
	}
	return names, files
}

func collect(t *testing.T, fx *diagFixture, req DiagRequest) (names []string, files map[string]string) {
	t.Helper()
	b, err := fx.o.DiagCollect(context.Background(), &req)
	if err != nil {
		t.Fatal(err)
	}
	names, files = readBundle(t, b)
	dir := b.dir
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("build directory not removed: %v", err)
	}
	return names, files
}

func TestDiagBundleContentAndSecrets(t *testing.T) {
	fx := newDiagFixture(t)
	req := DiagRequest{Version: "1.3.4", Secrets: []string{"example-pass-req"}, Callsigns: []string{"N0CALL"},
		Report: "report: AMI secret=example-pass-req node N0CALL\n", Audit: "2026-11-20 admin diagnostics.bundle {}\n"}
	for _, mask := range []bool{false, true} {
		req.MaskIdentity = mask
		names, files := collect(t, fx, req)
		if names[0] != "README.txt" {
			t.Errorf("mask=%v: README is not first: %v", mask, names)
		}
		for _, want := range []string{"report.txt", "versions.txt", "system.txt", "services.txt", "network.txt", "audit.txt",
			"journal/nodespot-node.txt", "journal/nodespot.txt", "config/node.json", "config/nodespot.conf", "config/sa818.json"} {
			if _, ok := files[want]; !ok {
				t.Errorf("mask=%v: missing %s (have %v)", mask, want, names)
			}
		}
		// Never collected.
		for name := range files {
			for _, bad := range []string{"nodespot.db", "nmconnection"} {
				if strings.Contains(name, bad) {
					t.Errorf("mask=%v: %s collected", mask, name)
				}
			}
		}
		all := strings.Join(names, "\n")
		for name, text := range files {
			all += "\n" + text
			for _, p := range diagPlanted {
				if strings.Contains(text, p) {
					t.Errorf("mask=%v: %s contains planted secret %s", mask, name, p)
				}
			}
		}
		for _, p := range diagPlanted {
			if strings.Contains(all, p) {
				t.Errorf("mask=%v: planted secret %s in the tarball", mask, p)
			}
		}
		// Configuration that is not secret stays readable.
		if !strings.Contains(files["config/node.json"], "master.example.net") || !strings.Contains(files["config/sa818.json"], "446.1") ||
			!strings.Contains(files["journal/nodespot-node.txt"], "registration failed") || !strings.Contains(files["config/nodespot.conf"], "listen=:8080") {
			t.Errorf("over-redacted: %q %q", files["config/node.json"], files["journal/nodespot-node.txt"])
		}
		if !strings.Contains(files["versions.txt"], "md380-emu 1.0") || strings.Contains(files["versions.txt"], "nodespot-old") ||
			!strings.Contains(files["versions.txt"], "nodespot: 1.3.4") {
			t.Errorf("versions: %q", files["versions.txt"])
		}
		if !strings.Contains(files["system.txt"], "/dev/root") || !strings.Contains(files["system.txt"], "Mem:") {
			t.Errorf("system: %q", files["system.txt"])
		}
		if !strings.Contains(files["network.txt"], "eth0") || strings.Contains(files["network.txt"], "ExampleNet") {
			t.Errorf("network: %q", files["network.txt"])
		}
		readme := files["README.txt"]
		for _, want := range []string{"journal/nodespot-node.txt", "config/node.json", "Never collected", "Redacted in every file", "Read every file"} {
			if !strings.Contains(readme, want) {
				t.Errorf("README lacks %q:\n%s", want, readme)
			}
		}
		if strings.Contains(readme, "example-pass") {
			t.Errorf("README leaks a secret:\n%s", readme)
		}

		own := []string{"N0CALL"}
		for _, v := range own {
			for name, text := range files {
				if mask && strings.Contains(text, v) {
					t.Errorf("mask on: %q still in %s: %q", v, name, text)
				}
			}
		}
		if !mask && !strings.Contains(files["config/node.json"], "N0CALL") {
			t.Errorf("mask off: identity was changed\n%s", files["config/node.json"])
		}
	}
}

func TestDiagBundleOwnInterfaceAddressMasked(t *testing.T) {
	// ip_addr.json has eth0 192.0.2.10 (a public-range stand-in).
	fx := newDiagFixture(t)
	_, files := collect(t, fx, DiagRequest{MaskIdentity: true})
	if strings.Contains(files["services.txt"], "192.0.2.10") || !strings.Contains(files["services.txt"], "<own-ip>:8080") {
		t.Errorf("services: %q", files["services.txt"])
	}
	if strings.Contains(files["network.txt"], "192.0.2.10") {
		t.Errorf("network: %q", files["network.txt"])
	}
	_, files = collect(t, fx, DiagRequest{})
	if !strings.Contains(files["services.txt"], "192.0.2.10:8080") {
		t.Errorf("unmasked services: %q", files["services.txt"])
	}
}

func TestDiagBundleToolsMissing(t *testing.T) {
	fx := newDiagFixture(t)
	fx.f.rules = map[string]resp{} // every command "not found"
	names, files := collect(t, fx, DiagRequest{})
	if !strings.Contains(files["versions.txt"], "not available") || !strings.Contains(files["network.txt"], "not available") ||
		!strings.Contains(files["journal/nodespot-node.txt"], "not available") {
		t.Errorf("tools missing not reported: %v", files["versions.txt"])
	}
	if len(names) < 8 {
		t.Errorf("%v", names)
	}
	// Config files are still collected without any tool.
	if _, ok := files["config/node.json"]; !ok {
		t.Error("configs missing")
	}
}

func TestDiagBundleSizeCapAndTruncation(t *testing.T) {
	fx := newDiagFixture(t)
	big := strings.Repeat("2026-11-20T12:00:00+0000 node2001 nodespot-node[1]: a long log line to fill the cap\n", 10000) // ~720 KiB
	fx.f.rules["journalctl"] = resp{out: big}
	delete(fx.f.rules, "journalctl -u nodespot-node")
	names, files := collect(t, fx, DiagRequest{})
	j := files["journal/nodespot-node.txt"]
	if len(j) > diagMaxFile || !strings.HasSuffix(j, "fill the cap\n") || !strings.HasPrefix(j, "2026-") {
		t.Errorf("journal not capped at a line boundary: %d bytes", len(j))
	}
	if !strings.Contains(files["README.txt"], "truncated to the last 256 KiB") {
		t.Errorf("README does not say truncated")
	}
	total := 0
	for _, n := range names {
		total += len(files[n])
	}
	if total > DiagMaxTotal+(64<<10) {
		t.Errorf("total %d over the cap", total)
	}

	// Total cap: many big journals; the rest is skipped and listed.
	b := &diagBuilder{dir: t.TempDir(), now: time.Now()}
	chunk := strings.Repeat(strings.Repeat("x", 199)+"\n", 1200) // 240 KB
	b.total = DiagMaxTotal - 1000                                // nearly full
	b.add("journal/small.txt", "d", "tiny\n", 0, diagMaxFile)
	b.add("journal/big.txt", "d", chunk, 0, diagMaxFile)
	b.add("journal/after.txt", "d", chunk, 0, diagMaxFile)
	if b.total > DiagMaxTotal {
		t.Errorf("builder total %d", b.total)
	}
	if len(b.entries) != 1 || len(b.skipped) != 2 || !strings.Contains(b.skipped[0], "size limit") {
		t.Errorf("skips: %v", b.skipped)
	}
	if !strings.Contains(b.readme(), "size limit reached") {
		t.Error("README does not list the skip")
	}
}

func TestDiagBundleTimeLimit(t *testing.T) {
	fx := newDiagFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fx.f.hook = func(cmd string) {
		if strings.HasPrefix(cmd, "journalctl -u nodespot-node") {
			cancel() // the budget runs out during the first journal
		}
	}
	b, err := fx.o.DiagCollect(ctx, &DiagRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, files := readBundle(t, b)
	b.Close()
	if !strings.Contains(files["README.txt"], "time limit reached") {
		t.Errorf("README:\n%s", files["README.txt"])
	}
	if _, ok := files["journal/NetworkManager.txt"]; ok {
		t.Error("journals after the time limit were collected")
	}
	if _, ok := files["config/node.json"]; !ok {
		t.Error("configs should still be there (no command needed)")
	}
}

func TestDiagBundleBusyAndCleanup(t *testing.T) {
	fx := newDiagFixture(t)
	if !diagBusy.CompareAndSwap(false, true) {
		t.Fatal("busy flag set by another test")
	}
	_, err := fx.o.DiagCollect(context.Background(), &DiagRequest{})
	diagBusy.Store(false)
	if !errors.Is(err, ErrDiagBusy) {
		t.Fatalf("got %v", err)
	}
	// After a failed pack nothing is left behind: point the base at a file.
	base := diagTempBases[0]
	entries, _ := os.ReadDir(base)
	if len(entries) != 0 {
		t.Errorf("leftover in %s: %v", base, entries)
	}
	diagTempBases = []string{filepath.Join(fx.root, "no", "such", "dir")}
	if _, err := fx.o.DiagCollect(context.Background(), &DiagRequest{}); err == nil {
		t.Error("expected an error without a build directory")
	}
	if diagBusy.Load() {
		t.Error("busy flag left set")
	}
}

func TestDiagRequestValidate(t *testing.T) {
	bad := []DiagRequest{
		{Version: "1.3.4; rm"}, {Version: strings.Repeat("1", 41)},
		{Report: strings.Repeat("x", DiagMaxReport+1)}, {Audit: strings.Repeat("x", DiagMaxAudit+1)},
		{Callsigns: []string{"N0 CALL"}}, {Callsigns: []string{"$(x)"}}, {Secrets: []string{strings.Repeat("a", 257)}},
		{Secrets: make([]string, 21)},
	}
	for i, r := range bad {
		if r.validate() == nil {
			t.Errorf("%d accepted", i)
		}
	}
	if err := (&DiagRequest{Version: "1.3.4-1~beta", Callsigns: []string{"N0CALL", "N0CALL-R"}}).validate(); err != nil {
		t.Error(err)
	}
}

func TestDiagBuilderRejectsBadNames(t *testing.T) {
	b := &diagBuilder{dir: t.TempDir()}
	for _, n := range []string{"../x", "/etc/x", "a/../../x", "", "..", "a//b"} {
		b.add(n, "d", "text", 0, 100)
	}
	if len(b.entries) != 0 || len(b.skipped) != 6 {
		t.Errorf("%v %v", b.entries, b.skipped)
	}
}

func TestDiagPlanInfo(t *testing.T) {
	p := DiagPlanInfo()
	if len(p.Items) < 8 || len(p.Excluded) < 5 || len(p.Redacted) < 5 || p.MaxBytes != DiagMaxTotal || p.TimeLimit != 90 {
		t.Errorf("%+v", p)
	}
	var names []string
	for _, i := range p.Items {
		names = append(names, i.Name)
	}
	sort.Strings(names)
	if !strings.Contains(strings.Join(names, ","), "README.txt") {
		t.Error(names)
	}
}

// End to end over the helper socket: the client tells the gzip stream from an
// error line.
func TestDiagBundleOverSocket(t *testing.T) {
	fx := newDiagFixture(t)
	c, _ := startServer(t, fx.o, PeerUID, []uint32{0, uint32(os.Getuid())})
	rc, err := c.DiagBundle(context.Background(), DiagRequest{Version: "1.3.4", MaskIdentity: true, Callsigns: []string{"N0CALL"}})
	if err != nil {
		t.Fatal(err)
	}
	names, files := readBundle(t, rc)
	rc.Close()
	if names[0] != "README.txt" || strings.Contains(files["config/node.json"], "example-pass") {
		t.Errorf("%v", names)
	}
	// An error comes back as an error, not as a broken archive.
	if _, err := c.DiagBundle(context.Background(), DiagRequest{Version: "bad version"}); err == nil || !strings.Contains(err.Error(), "invalid version") {
		t.Errorf("got %v", err)
	}
	diagBusy.Store(true)
	_, err = c.DiagBundle(context.Background(), DiagRequest{})
	diagBusy.Store(false)
	if !errors.Is(err, ErrDiagBusy) {
		t.Errorf("busy: %v", err)
	}
	// Other ops refuse the diag field; the diag op refuses nothing else.
	if _, err := fx.o.HandleRequest(context.Background(), Request{Op: OpUpdateStatus, Diag: &DiagRequest{}}); err == nil {
		t.Error("diag field accepted by another op")
	}
	if _, err := fx.o.HandleRequest(context.Background(), Request{Op: OpNetStatus, Diag: &DiagRequest{}}); err == nil {
		t.Error("diag field accepted by a sys op")
	}
}

func TestDemoDiagBundle(t *testing.T) {
	d := NewDemoSystem()
	for _, mask := range []bool{true, false} {
		rc, err := d.DiagBundle(context.Background(), DiagRequest{MaskIdentity: mask, Version: "1.3.4"})
		if err != nil {
			t.Fatal(err)
		}
		_, files := readBundle(t, rc)
		rc.Close()
		all := ""
		for _, f := range files {
			all += f
		}
		if strings.Contains(all, "example-pass") {
			t.Errorf("demo bundle leaks the placeholder password")
		}
		if mask == strings.Contains(all, "N0CALL") {
			t.Errorf("mask=%v but N0CALL present=%v", mask, strings.Contains(all, "N0CALL"))
		}
	}
}
