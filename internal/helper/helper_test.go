package helper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type resp struct {
	out  string
	code int
	err  error
}

// fakeRunner answers by the space-joined argv prefix (longest match wins) and
// records calls.
type fakeRunner struct {
	mu    sync.Mutex
	rules map[string]resp
	calls []string
	hook  func(cmd string)
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (string, int, error) {
	cmd := strings.Join(argv, " ")
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	hook := f.hook
	best, bestLen := resp{err: errors.New("unexpected command: " + cmd)}, -1
	for k, r := range f.rules {
		if strings.HasPrefix(cmd, k) && len(k) > bestLen {
			best, bestLen = r, len(k)
		}
	}
	f.mu.Unlock()
	if hook != nil {
		hook(cmd)
	}
	return best.out, best.code, best.err
}

func (f *fakeRunner) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const loadedUnit = "nodespot-upgrade.service loaded inactive dead nodespot upgrade\n"

func idleJob(r map[string]resp) {
	r["systemctl list-units"] = resp{out: loadedUnit}
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=inactive\nResult=success\n"}
	r["journalctl -u nodespot-upgrade"] = resp{}
}

func TestJobStateFrom(t *testing.T) {
	show := func(a, r string) map[string]string { return ParseShow("ActiveState=" + a + "\nResult=" + r + "\n") }
	cases := []struct {
		name string
		show map[string]string
		log  string
		want string
	}{
		{"running", show("active", "success"), "", JobRunning},
		{"activating", show("activating", "success"), "x", JobRunning},
		{"failed state", show("failed", "exit-code"), "", JobFailed},
		{"idle", show("inactive", "success"), "", JobIdle},
		{"succeeded", show("inactive", "success"), "Setting up nodespot\nnodespot-upgrade.service: Deactivated successfully.\n", JobSucceeded},
		{"collected failure", show("inactive", "success"), "E: boom\nnodespot-upgrade.service: Failed with result 'exit-code'.\n", JobFailed},
		{"old failure then success", show("inactive", "success"), "nodespot-upgrade.service: Failed with result 'exit-code'.\nnodespot-upgrade.service: Deactivated successfully.\n", JobSucceeded},
	}
	for _, c := range cases {
		if got := JobStateFrom(c.show, c.log); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func upgradableRules() map[string]resp {
	r := map[string]resp{
		"dpkg-query -W -f=${Version} nodespot": {out: "0.2.0"},
	}
	idleJob(r)
	return r
}

func noApt(t *testing.T, f *fakeRunner) {
	t.Helper()
	for _, p := range []string{"apt-cache", "apt-get", "apt ", "dpkg --compare"} {
		if f.called(p) != 0 {
			t.Errorf("%s must not be called: %v", p, f.calls)
		}
	}
}

func TestUpdateStatusWithoutApt(t *testing.T) {
	f := &fakeRunner{rules: upgradableRules()}
	st, err := (&Ops{Run: f}).UpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed != "0.2.0" || st.Candidate != "" || st.Upgradable || st.Job != JobIdle {
		t.Fatalf("%+v", st)
	}
	noApt(t, f)
	// not installed (dpkg-query exits 1)
	r := upgradableRules()
	r["dpkg-query -W -f=${Version} nodespot"] = resp{code: 1, out: "dpkg-query: no packages found matching nodespot"}
	st, _ = (&Ops{Run: &fakeRunner{rules: r}}).UpdateStatus(context.Background())
	if st.Installed != "" {
		t.Fatalf("%+v", st)
	}
	// update-check is the same cheap status
	f = &fakeRunner{rules: upgradableRules()}
	if st, err = (&Ops{Run: f}).UpdateCheck(context.Background()); err != nil || st.Installed != "0.2.0" {
		t.Fatalf("%+v %v", st, err)
	}
	noApt(t, f)
}

func TestJobJournalOnlyWhenNeeded(t *testing.T) {
	ctx := context.Background()
	// idle and never ran: journal read once (to classify), then cached
	r := upgradableRules()
	f := &fakeRunner{rules: r}
	o := &Ops{Run: f}
	for i := 0; i < 3; i++ {
		o.UpdateStatus(ctx)
	}
	if n := f.called("journalctl"); n != 1 {
		t.Fatalf("journalctl called %d times for a finished job", n)
	}
	// running: journal on every poll, never cached
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=active\nResult=success\nInvocationID=abc\n"}
	r["journalctl -u nodespot-upgrade"] = resp{out: "Starting nodespot-upgrade.service - x\nReading package lists...\n"}
	for i := 0; i < 2; i++ {
		st, _ := o.UpdateStatus(ctx)
		if st.Job != JobRunning || !strings.Contains(st.JobLog, "Reading package lists") {
			t.Fatalf("%+v", st)
		}
	}
	if n := f.called("journalctl"); n != 3 {
		t.Fatalf("journalctl called %d times", n)
	}
	// finished (collected): classified once, then cached
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=inactive\nResult=success\n"}
	r["journalctl -u nodespot-upgrade"] = resp{out: fixture(t, "journal-succeeded-noisy.txt")}
	for i := 0; i < 4; i++ {
		st, _ := o.UpdateStatus(ctx)
		if st.Job != JobSucceeded {
			t.Fatalf("%+v", st)
		}
	}
	if n := f.called("journalctl"); n != 4 {
		t.Fatalf("journalctl called %d times", n)
	}
	noApt(t, f)
	// a different invocation is classified again
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=inactive\nResult=success\nInvocationID=zzz\n"}
	r["journalctl -u nodespot-upgrade"] = resp{out: fixture(t, "journal-noop.txt")}
	st, _ := o.UpdateStatus(ctx)
	if st.Job != JobNoop || f.called("journalctl") != 5 {
		t.Fatalf("%+v %d", st, f.called("journalctl"))
	}
	st, _ = o.UpdateStatus(ctx)
	if st.Job != JobNoop || f.called("journalctl") != 5 {
		t.Fatalf("not cached: %+v %d", st, f.called("journalctl"))
	}
}

func TestJournalClassification(t *testing.T) {
	inactive := map[string]string{"ActiveState": "inactive", "Result": "success"}
	// the owner's real log: previous run + latest run, noisy lines
	noisy := fixture(t, "journal-succeeded-noisy.txt")
	if got := JobStateFrom(inactive, noisy); got != JobSucceeded {
		t.Fatalf("noisy: %s", got)
	}
	if got := JobStateFrom(inactive, fixture(t, "journal-noop.txt")); got != JobNoop {
		t.Fatalf("noop: %s", got)
	}
	// failed latest run after an earlier success
	if got := JobStateFrom(inactive, fixture(t, "journal-failed.txt")); got != JobFailed {
		t.Fatalf("failed: %s", got)
	}
	// a noop in the previous run must not turn a real upgrade into noop
	prev := fixture(t, "journal-noop.txt") + noisy
	if got := JobStateFrom(inactive, prev); got != JobSucceeded {
		t.Fatalf("prev noop: %s", got)
	}
	// noise only
	if got := JobStateFrom(inactive, "nodespot-upgrade.service: Failed to open /run/systemd/transient/nodespot-upgrade.service: No such file or directory\n"); got != JobIdle {
		t.Fatalf("noise only: %s", got)
	}
}

func TestCleanLog(t *testing.T) {
	got := CleanLog(fixture(t, "journal-succeeded-noisy.txt"))
	if !strings.HasPrefix(got, "Starting nodespot-upgrade.service") {
		t.Fatalf("not trimmed to the latest run:\n%s", got)
	}
	if strings.Contains(got, "Failed to open") || strings.Contains(got, "\r") || strings.Contains(got, "5%") {
		t.Fatalf("noise left:\n%q", got)
	}
	for _, want := range []string{"(Reading database ... 132342 files and directories currently installed.)", "Setting up nodespot (0.2.1)", "Deactivated successfully"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Consumed 15.090s") {
		t.Fatalf("previous run kept:\n%s", got)
	}
	// a log without a "Starting" line (longer than the tail) is kept whole
	if g := CleanLog("a\nb\n"); g != "a\nb" {
		t.Fatalf("%q", g)
	}
}

func TestUpdateApplyAndConcurrentRefusal(t *testing.T) {
	r := upgradableRules()
	started := false
	var mu sync.Mutex
	f := &fakeRunner{rules: r}
	r["systemd-run"] = resp{}
	f.hook = func(cmd string) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(cmd, "systemd-run") {
			started = true
			f.mu.Lock()
			r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
			f.mu.Unlock()
		}
	}
	o := &Ops{Run: f}
	st, err := o.UpdateApply(context.Background())
	if err != nil || st.Job != JobRunning {
		t.Fatalf("%+v %v", st, err)
	}
	if !started {
		t.Fatal("systemd-run not called")
	}
	want := "systemd-run --no-block --unit=nodespot-upgrade --collect --property=Type=oneshot --setenv=DEBIAN_FRONTEND=noninteractive --setenv=APT_LISTCHANGES_FRONTEND=none /bin/sh -c " +
		"apt-get update -o DPkg::Lock::Timeout=600 -o Dir::Etc::sourcelist=sources.list.d/nodespot.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 -q && " +
		"apt-get install -y -q -o DPkg::Lock::Timeout=600 --only-upgrade --no-install-recommends -o Dpkg::Options::=--force-confold -o Dpkg::Use-Pty=0 nodespot"
	if f.called(want) != 1 || f.called("systemd-run") != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
	// exact argv, not just a joined string
	argv := upgradeArgv()
	if len(argv) != 10 || argv[1] != "--no-block" || argv[8] != "-c" || argv[9] != upgradeScript || argv[7] != "/bin/sh" {
		t.Fatalf("argv: %q", argv)
	}
	if _, err := o.UpdateApply(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("second apply: %v", err)
	}
	if f.called("systemd-run") != 1 {
		t.Fatalf("systemd-run called %d times", f.called("systemd-run"))
	}
	// apply never touches apt directly (it all happens inside the unit)
	noApt(t, f)
}

func TestJobUnitNotLoaded(t *testing.T) {
	ctx := context.Background()
	r := upgradableRules()
	r["systemctl list-units"] = resp{} // not loaded: empty output
	f := &fakeRunner{rules: r}
	o := &Ops{Run: f}
	for i := 0; i < 3; i++ {
		st, _ := o.UpdateStatus(ctx)
		if st.Job != JobIdle {
			t.Fatalf("%+v", st)
		}
	}
	if f.called("systemctl show") != 0 {
		t.Fatalf("show called for an unloaded unit: %v", f.calls)
	}
	if f.called("systemctl list-units") != 3 || f.called("journalctl") != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
	// loaded again: show is used
	r["systemctl list-units"] = resp{out: loadedUnit}
	o.UpdateStatus(ctx)
	if f.called("systemctl show") != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
	// just started, not listed yet: running without show or journal
	r["systemctl list-units"] = resp{}
	r["systemd-run"] = resp{}
	if _, err := o.UpdateApply(ctx); err != nil {
		t.Fatal(err)
	}
	shows, js := f.called("systemctl show"), f.called("journalctl")
	if st, _ := o.UpdateStatus(ctx); st.Job != JobRunning || f.called("systemctl show") != shows || f.called("journalctl") != js {
		t.Fatalf("%+v", st)
	}
}

func TestJustStartedGrace(t *testing.T) {
	r := upgradableRules()
	r["systemd-run"] = resp{}
	f := &fakeRunner{rules: r}
	o := &Ops{Run: f}
	if _, err := o.UpdateApply(context.Background()); err != nil {
		t.Fatal(err)
	}
	// systemd does not list the unit yet: still "running", journal not read
	before := f.called("journalctl")
	st, _ := o.UpdateStatus(context.Background())
	if st.Job != JobRunning || f.called("journalctl") != before {
		t.Fatalf("%+v", st)
	}
}

func fwConf(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nodespot.conf")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestFirewall(t *testing.T) {
	r := map[string]resp{
		"firewall-cmd --state":                            {out: "running\n"},
		"firewall-cmd --query-service=nodespot":           {code: 1},
		"firewall-cmd --permanent --query-service":        {code: 1},
		"firewall-cmd --query-port":                       {code: 1},
		"firewall-cmd --permanent --query-port":           {code: 1},
		"firewall-cmd --permanent --add-service=nodespot": {out: "success"},
		"firewall-cmd --reload":                           {out: "success"},
	}
	f := &fakeRunner{rules: r}
	o := &Ops{Run: f, NodespotConf: fwConf(t, "")} // missing file: default 8080
	fw, _ := o.FirewallStatus(context.Background())
	if !fw.Present || !fw.Running || fw.ServiceEnabled || fw.Open || fw.Port != 8080 {
		t.Fatalf("%+v", fw)
	}
	f.hook = func(cmd string) {
		if cmd == "firewall-cmd --reload" {
			f.mu.Lock()
			r["firewall-cmd --query-service=nodespot"] = resp{code: 0}
			f.mu.Unlock()
		}
	}
	fw, err := o.FirewallOpen(context.Background())
	if err != nil || !fw.ServiceEnabled || !fw.Open {
		t.Fatalf("%+v %v", fw, err)
	}
	// not running
	f2 := &fakeRunner{rules: map[string]resp{"firewall-cmd --state": {out: "not running\n", code: 252}}}
	if _, err := (&Ops{Run: f2, NodespotConf: fwConf(t, "")}).FirewallOpen(context.Background()); err == nil {
		t.Fatal("expected error when firewalld is not running")
	}
	if f2.called("firewall-cmd --permanent") != 0 {
		t.Fatal("must not touch config when not running")
	}
	// not installed
	f3 := &fakeRunner{rules: map[string]resp{"firewall-cmd": {err: errors.New("not found")}}}
	fw, _ = (&Ops{Run: f3, NodespotConf: fwConf(t, "")}).FirewallStatus(context.Background())
	if fw.Present {
		t.Fatalf("%+v", fw)
	}
}

func TestFirewallCustomPort(t *testing.T) {
	r := map[string]resp{
		"firewall-cmd --state":                         {out: "running\n"},
		"firewall-cmd --query-port":                    {code: 1},
		"firewall-cmd --permanent --query-port":        {code: 1},
		"firewall-cmd --permanent --add-port=9090/tcp": {out: "success"},
		"firewall-cmd --reload":                        {out: "success"},
	}
	f := &fakeRunner{rules: r}
	o := &Ops{Run: f, NodespotConf: fwConf(t, "[server]\nlisten = 192.0.2.10:9090 ; LAN only\n")}
	fw, err := o.FirewallStatus(context.Background())
	if err != nil || fw.Port != 9090 || fw.Open || fw.ServiceEnabled {
		t.Fatalf("%+v %v", fw, err)
	}
	if f.called("firewall-cmd --query-service") != 0 || f.called("firewall-cmd --permanent --query-service") != 0 {
		t.Fatal("the nodespot service covers 8080 only; must not be queried for another port")
	}
	f.hook = func(cmd string) {
		if cmd == "firewall-cmd --permanent --add-port=9090/tcp" {
			f.mu.Lock()
			r["firewall-cmd --permanent --query-port=9090/tcp"] = resp{code: 0}
			f.mu.Unlock()
		}
	}
	fw, err = o.FirewallOpen(context.Background())
	if err != nil || !fw.PortOpen || !fw.Open || fw.Port != 9090 {
		t.Fatalf("%+v %v", fw, err)
	}
	if f.called("firewall-cmd --permanent --add-service") != 0 {
		t.Fatal("must open the port, not the 8080 service")
	}
	// Only the permanent configuration has the port (not yet reloaded).
	f4 := &fakeRunner{rules: map[string]resp{
		"firewall-cmd --state":                           {out: "running\n"},
		"firewall-cmd --query-port":                      {code: 1},
		"firewall-cmd --permanent --query-port=9090/tcp": {code: 0},
	}}
	fw, _ = (&Ops{Run: f4, NodespotConf: o.NodespotConf}).FirewallStatus(context.Background())
	if !fw.Open || !fw.PortOpen {
		t.Fatalf("%+v", fw)
	}
}

func TestFirewallBadPort(t *testing.T) {
	for _, conf := range []string{"[server]\nlisten = :0\n", "[server]\nlisten = :70000\n", "[server]\nlisten = :http\n", "[server]\nlisten = nonsense\n"} {
		f := &fakeRunner{rules: map[string]resp{"firewall-cmd --state": {out: "running\n"}}}
		o := &Ops{Run: f, NodespotConf: fwConf(t, conf)}
		if _, err := o.FirewallOpen(context.Background()); err == nil {
			t.Fatalf("%q: expected an error", conf)
		}
		if f.called("firewall-cmd --permanent") != 0 || f.called("firewall-cmd --reload") != 0 {
			t.Fatalf("%q: must not touch firewalld", conf)
		}
	}
}

func TestAuthorized(t *testing.T) {
	allowed := []uint32{0, 998}
	for uid, want := range map[uint32]bool{0: true, 998: true, 1000: false, 33: false} {
		if Authorized(uid, allowed) != want {
			t.Errorf("uid %d", uid)
		}
	}
	if !Authorized(5, DefaultAllowedUIDs(5)) || !Authorized(0, DefaultAllowedUIDs()) {
		t.Error("DefaultAllowedUIDs")
	}
}

func startServer(t *testing.T, ops *Ops, peer func(net.Conn) (uint32, error), allowed []uint32) (*Client, *Server) {
	t.Helper()
	dir, err := os.MkdirTemp("", "hlp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	ln, err := ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Ops: ops, Peer: peer, AllowedUIDs: func() []uint32 { return allowed }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); ln.Close(); <-done })
	return NewClient(sock), srv
}

func TestServerEndToEnd(t *testing.T) {
	r := upgradableRules()
	r["firewall-cmd"] = resp{err: errors.New("absent")}
	c, _ := startServer(t, &Ops{Run: &fakeRunner{rules: r}}, PeerUID, []uint32{0, uint32(os.Getuid())})
	st, err := c.UpdateStatus(context.Background())
	if err != nil || st.Installed != "0.2.0" {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err = c.UpdateCheck(context.Background()); err != nil || st.Installed != "0.2.0" {
		t.Fatalf("%+v %v", st, err)
	}
	if fw, err := c.FirewallStatus(context.Background()); err != nil || fw.Present {
		t.Fatalf("%+v %v", fw, err)
	}
	// wire format
	conn, err := net.Dial("unix", c.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, `{"op":"update-status"}`)
	b, _ := io.ReadAll(conn)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"installed", "candidate", "upgradable", "job", "jobLog"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q in %s", k, b)
		}
	}
}

func TestServerRefusals(t *testing.T) {
	f := &fakeRunner{rules: upgradableRules()}
	c, _ := startServer(t, &Ops{Run: f}, PeerUID, []uint32{0, uint32(os.Getuid())})
	raw := func(body string) string {
		conn, err := net.Dial("unix", c.Socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		io.WriteString(conn, body)
		b, _ := io.ReadAll(conn)
		return string(b)
	}
	for _, body := range []string{`{"op":"rm -rf /"}`, `{"op":""}`, `{}`, `not json`, `{"op":"update-status;id"}`} {
		out := raw(body)
		if !strings.Contains(out, `"error"`) {
			t.Errorf("%s: want error, got %s", body, out)
		}
	}
	if !strings.Contains(raw(`{"op":"nope"}`), "unknown op") {
		t.Error("unknown op message")
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran for refused requests: %v", f.calls)
	}
	if err := c.Do(context.Background(), "bogus", nil); err == nil {
		t.Error("client: expected error")
	}
}

func TestServerPeerCheck(t *testing.T) {
	f := &fakeRunner{rules: upgradableRules()}
	c, _ := startServer(t, &Ops{Run: f}, func(net.Conn) (uint32, error) { return 1234, nil }, []uint32{0, 998})
	if _, err := c.UpdateStatus(context.Background()); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran commands for an unauthorized peer: %v", f.calls)
	}
	c2, _ := startServer(t, &Ops{Run: f}, func(net.Conn) (uint32, error) { return 998, nil }, []uint32{0, 998})
	if _, err := c2.UpdateStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	c3, _ := startServer(t, &Ops{Run: f}, func(net.Conn) (uint32, error) { return 0, errors.New("boom") }, []uint32{0})
	if _, err := c3.UpdateStatus(context.Background()); err == nil {
		t.Fatal("peer error must refuse")
	}
}

func TestIdleExit(t *testing.T) {
	dir := t.TempDir()
	ln, err := ListenUnix(filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &Server{Ops: &Ops{Run: &fakeRunner{}}, Peer: PeerUID, AllowedUIDs: func() []uint32 { return nil }, Idle: 1200 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), ln) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("did not exit when idle")
	}
}

func TestClientUnavailable(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	_, err := c.UpdateStatus(context.Background())
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != "not installed from a package" {
		t.Fatalf("err = %v", err)
	}
	// socket file exists but nobody listens
	dir := t.TempDir()
	p := filepath.Join(dir, "dead.sock")
	ln, _ := net.Listen("unix", p)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	_, err = NewClient(p).UpdateStatus(context.Background())
	if !errors.As(err, &ue) || ue.Reason != "helper not running" {
		t.Fatalf("err = %v", err)
	}
}

func TestListenerFromSystemdNotActivated(t *testing.T) {
	t.Setenv("LISTEN_PID", "1")
	t.Setenv("LISTEN_FDS", "1")
	ln, err := ListenerFromSystemd()
	if ln != nil || err != nil {
		t.Fatalf("%v %v", ln, err)
	}
}

// The package's postinst restarts the helper during an upgrade while the UI
// keeps polling it. A request in flight when the helper is told to stop must
// still be answered, not dropped ("unexpected end of JSON input").
func TestShutdownFinishesRequestsInFlight(t *testing.T) {
	started := make(chan struct{}, 1)
	f := &fakeRunner{rules: upgradableRules()}
	f.hook = func(cmd string) {
		if strings.HasPrefix(cmd, "dpkg-query") {
			select {
			case started <- struct{}{}:
			default:
			}
			time.Sleep(2 * time.Second) // longer than Serve's accept poll
		}
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "h.sock")
	ln, err := ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Ops: &Ops{Run: f}, Peer: PeerUID, AllowedUIDs: func() []uint32 { return []uint32{0, uint32(os.Getuid())} }}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ctx, ln) }()

	type result struct {
		st  Status
		err error
	}
	res := make(chan result, 1)
	go func() {
		st, err := NewClient(sock).UpdateStatus(context.Background())
		res <- result{st, err}
	}()
	<-started
	cancel() // like SIGTERM from "systemctl stop" while the request runs

	// In the real helper the process exits as soon as Serve returns, which
	// would drop the connection. So Serve must not return before the reply
	// has been sent.
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	var r result
	select {
	case r = <-res:
	default:
		t.Fatal("Serve returned while a request was still in flight")
	}
	if r.err != nil || r.st.Installed != "0.2.0" {
		t.Fatalf("request in flight during shutdown: %+v %v", r.st, r.err)
	}
	ln.Close()
}

const listStable = "deb [signed-by=/usr/share/keyrings/nodespot-archive-keyring.gpg] https://example.invalid/nodespot stable main\n"

func writeList(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nodespot.list")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetChannel(t *testing.T) {
	ctx := context.Background()
	p := writeList(t, "# nodespot\n"+listStable+"deb http://other.invalid/x stable main\n")
	o := &Ops{Run: &fakeRunner{rules: upgradableRules()}, SourceList: p}
	st, err := o.UpdateStatus(ctx)
	if err != nil || st.Channel != ChannelStable {
		t.Fatalf("%+v %v", st, err)
	}
	st, err = o.SetChannel(ctx, ChannelBeta)
	if err != nil || st.Channel != ChannelBeta || st.Installed != "0.2.0" {
		t.Fatalf("%+v %v", st, err)
	}
	got, _ := os.ReadFile(p)
	// both deb lines are nodespot-shaped (suite word), only the suite changed
	want := "# nodespot\ndeb [signed-by=/usr/share/keyrings/nodespot-archive-keyring.gpg] https://example.invalid/nodespot beta main\ndeb http://other.invalid/x beta main\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
	// idempotent, and back to stable
	if st, err = o.SetChannel(ctx, ChannelBeta); err != nil || st.Channel != ChannelBeta {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err = o.SetChannel(ctx, ChannelStable); err != nil || st.Channel != ChannelStable {
		t.Fatalf("%+v %v", st, err)
	}
	if got, _ = os.ReadFile(p); !strings.Contains(string(got), "nodespot stable main\n") {
		t.Fatalf("got %q", got)
	}
	// invalid channel
	if _, err := o.SetChannel(ctx, "alpha; rm"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetChannelRefusals(t *testing.T) {
	ctx := context.Background()
	// missing file: status has no channel, set fails
	o := &Ops{Run: &fakeRunner{rules: upgradableRules()}, SourceList: filepath.Join(t.TempDir(), "none.list")}
	if st, err := o.UpdateStatus(ctx); err != nil || st.Channel != "" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := o.SetChannel(ctx, ChannelBeta); err == nil {
		t.Fatal("missing file: expected error")
	}
	// no recognizable line: file untouched
	const other = "# nothing here\ndeb-src https://example.invalid/nodespot stable main\ndeb https://example.invalid/nodespot bookworm main\n"
	p := writeList(t, other)
	o = &Ops{Run: &fakeRunner{rules: upgradableRules()}, SourceList: p}
	if st, _ := o.UpdateStatus(ctx); st.Channel != "" {
		t.Fatalf("channel %q", st.Channel)
	}
	if _, err := o.SetChannel(ctx, ChannelBeta); err == nil {
		t.Fatal("expected error")
	}
	if got, _ := os.ReadFile(p); string(got) != other {
		t.Fatalf("file changed: %q", got)
	}
	// refused while an upgrade runs
	p = writeList(t, listStable)
	r := upgradableRules()
	r["systemctl show nodespot-upgrade"] = resp{out: "ActiveState=active\nResult=success\n"}
	o = &Ops{Run: &fakeRunner{rules: r}, SourceList: p}
	if _, err := o.SetChannel(ctx, ChannelBeta); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != listStable {
		t.Fatalf("file changed: %q", got)
	}
}

func TestChannelOpsOverSocket(t *testing.T) {
	p := writeList(t, listStable)
	f := &fakeRunner{rules: upgradableRules()}
	c, _ := startServer(t, &Ops{Run: f, SourceList: p}, PeerUID, []uint32{0, uint32(os.Getuid())})
	st, err := c.SetChannel(context.Background(), ChannelBeta)
	if err != nil || st.Channel != ChannelBeta {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err = c.UpdateStatus(context.Background()); err != nil || st.Channel != ChannelBeta {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := c.SetChannel(context.Background(), "x"); err == nil {
		t.Fatal("client must reject unknown channel")
	}
	if got := c.Do(context.Background(), "update-channel-gamma", nil); got == nil {
		t.Fatal("unknown channel op must fail")
	}
}
