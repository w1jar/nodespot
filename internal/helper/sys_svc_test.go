package helper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const showPrefix = "systemctl show --no-pager"

// svcOps returns Ops with temporary paths and a fake runner. conf is the
// content of nodespot.conf ("" for none).
func svcOps(t *testing.T, conf string, r map[string]resp) (*Ops, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	f := &fakeRunner{rules: r}
	o := &Ops{Run: f, Now: func() time.Time { return time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC) },
		NodespotConf: filepath.Join(dir, "nodespot.conf"),
		Svc: SvcPaths{Record: filepath.Join(dir, "lib", "disabled-units"), StateDir: filepath.Join(dir, "webport"),
			DropinDir: filepath.Join(dir, "nodespot.service.d"), GuardDelay: 60 * time.Second}}
	if conf != "" {
		if err := os.WriteFile(o.NodespotConf, []byte(conf), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return o, f
}

func svcRules(t *testing.T) map[string]resp {
	return map[string]resp{
		showPrefix:                   {out: fixture(t, "svc_show_units.txt")},
		"ss -ltnH":                   {out: fixture(t, "ss_ltnh.txt")},
		"systemctl disable --now":    {},
		"systemctl enable --now":     {},
		"systemctl stop":             {},
		"systemctl daemon-reload":    {},
		"systemctl --no-block":       {},
		"systemd-run":                {},
		"firewall-cmd --state":       {out: "not running\n", code: 252},
		"systemctl is-active apache": {out: "active\n"},
	}
}

func TestParseListeners(t *testing.T) {
	ls := parseListeners(fixture(t, "ss_ltnh.txt") + "garbage\nLISTEN 0 1 nocolon x\nLISTEN 0 1 *:99999 x\n")
	if len(ls) != 10 || ls[0] != (SockListener{Addr: "0.0.0.0", Port: 22}) {
		t.Fatalf("%v", ls)
	}
	ports := svcPorts(ls)
	want := map[string][]int{"80": {80}, "443": {443}, "9090": {9090}, "16080": {16080}, "16700+": {16700, 16701}}
	for _, p := range ports {
		if !p.Listening || !reflect.DeepEqual(p.Ports, want[p.Label]) {
			t.Errorf("%+v", p)
		}
	}
	if got := svcPorts(nil); len(got) != 5 || got[0].Listening {
		t.Fatalf("%+v", got)
	}
}

func TestSvcStatus(t *testing.T) {
	o, _ := svcOps(t, "[server]\nlisten = :8080\n", svcRules(t))
	if err := os.MkdirAll(filepath.Dir(o.Svc.Record), 0o755); err != nil {
		t.Fatal(err)
	}
	// only allowlisted lines count
	_ = os.WriteFile(o.Svc.Record, []byte("allmon3\nsshd.service\n../x\n"), 0o644)
	st := o.SvcStatus(context.Background())
	if !st.Available || !st.PortsAvailable || st.NodespotPort != 8080 || st.NodespotOn80 || len(st.Scopes) != 3 {
		t.Fatalf("%+v", st)
	}
	if st.Scopes[0].Scope != "cockpit" || !st.Scopes[0].Active || !st.Scopes[0].Enabled || len(st.Scopes[0].Units) != 2 {
		t.Fatalf("%+v", st.Scopes[0])
	}
	if !st.Scopes[2].Recorded || st.Scopes[1].Recorded {
		t.Fatalf("%+v", st.Scopes)
	}
	// no tools: not an error, shown as unavailable
	o2, _ := svcOps(t, "", map[string]resp{})
	st = o2.SvcStatus(context.Background())
	if st.Available || st.PortsAvailable || len(st.Scopes) != 3 {
		t.Fatalf("%+v", st)
	}
}

func TestSvcSetDisableEnable(t *testing.T) {
	o, f := svcOps(t, "[server]\nlisten = :8080\n", svcRules(t))
	ctx := context.Background()
	res, err := o.SvcSet(ctx, "cockpit", SvcActionDisable, false)
	if err != nil {
		t.Fatal(err)
	}
	// the socket is enabled and gets disable --now; the static service is not active
	if f.called("systemctl disable --now cockpit.socket") != 1 || f.called("systemctl disable --now cockpit.service") != 0 ||
		!reflect.DeepEqual(res.Changed, []string{"cockpit.socket"}) {
		t.Fatalf("%v %v", f.calls, res.Changed)
	}
	if got := o.readSvcRecord(); !reflect.DeepEqual(got, []string{"cockpit.socket"}) {
		t.Fatalf("record %v", got)
	}
	if _, err := o.SvcSet(ctx, "apache", SvcActionDisable, false); err != nil {
		t.Fatal(err)
	}
	if got := o.readSvcRecord(); !reflect.DeepEqual(got, []string{"cockpit.socket", "apache2"}) {
		t.Fatalf("record %v", got)
	}
	if _, err := o.SvcSet(ctx, "cockpit", SvcActionEnable, false); err != nil {
		t.Fatal(err)
	}
	if f.called("systemctl enable --now cockpit.socket") != 1 || f.called("systemctl enable --now cockpit.service") != 0 {
		t.Fatalf("%v", f.calls)
	}
	if got := o.readSvcRecord(); !reflect.DeepEqual(got, []string{"apache2"}) {
		t.Fatalf("record %v", got)
	}
	// nothing from the request reaches the command line unchecked
	for _, bad := range [][2]string{{"sshd", "enable"}, {"cockpit", "restart"}, {"", ""}, {"cockpit;reboot", "disable"}} {
		if _, err := o.SvcSet(ctx, bad[0], bad[1], false); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestSvcSetRefusals(t *testing.T) {
	// nodespot on port 80: Apache cannot be turned on
	o, _ := svcOps(t, "[server]\nlisten = :80\n", svcRules(t))
	if _, err := o.SvcSet(context.Background(), "apache", SvcActionEnable, false); !errors.Is(err, ErrPort80Held) {
		t.Fatalf("%v", err)
	}
	// unit not installed
	r := svcRules(t)
	r[showPrefix] = resp{out: "Id=allmon3.service\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\n"}
	o2, _ := svcOps(t, "", r)
	if _, err := o2.SvcSet(context.Background(), "apache", SvcActionDisable, false); !errors.Is(err, ErrSvcNotInstalled) {
		t.Fatalf("%v", err)
	}
	// a failing systemctl is an error and leaves the record alone
	r = svcRules(t)
	r["systemctl disable --now apache2"] = resp{out: "boom\n", code: 1}
	o3, _ := svcOps(t, "", r)
	if _, err := o3.SvcSet(context.Background(), "apache", SvcActionDisable, false); err == nil || len(o3.readSvcRecord()) != 0 {
		t.Fatalf("%v %v", err, o3.readSvcRecord())
	}
}

func TestSvcSetFirewall(t *testing.T) {
	r := svcRules(t)
	r["firewall-cmd --state"] = resp{out: "running\n"}
	r["firewall-cmd --list-services"] = resp{out: "ssh http https\n"}
	r["firewall-cmd --permanent --list-services"] = resp{out: "ssh http\n"}
	r["firewall-cmd --permanent --remove-service"] = resp{}
	r["firewall-cmd --reload"] = resp{}
	o, f := svcOps(t, "", r)
	res, err := o.SvcSet(context.Background(), "apache", SvcActionDisable, true)
	if err != nil {
		t.Fatal(err)
	}
	// https is not in the permanent list: only http changes
	if !reflect.DeepEqual(res.FirewallChanged, []string{"http"}) || f.called("firewall-cmd --permanent --remove-service=http") != 1 ||
		f.called("firewall-cmd --permanent --remove-service=https") != 0 || f.called("firewall-cmd --reload") != 1 {
		t.Fatalf("%v %v", res.FirewallChanged, f.calls)
	}
	// without the flag firewalld is not touched
	o2, f2 := svcOps(t, "", r)
	if _, err := o2.SvcSet(context.Background(), "apache", SvcActionDisable, false); err != nil || f2.called("firewall-cmd --permanent --remove") != 0 {
		t.Fatalf("%v %v", err, f2.calls)
	}
	// a firewalld failure does not fail the switch
	r["firewall-cmd --permanent --remove-service"] = resp{out: "denied\n", code: 1}
	o3, _ := svcOps(t, "", r)
	res, err = o3.SvcSet(context.Background(), "apache", SvcActionDisable, true)
	if err != nil || res.FirewallError == "" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestSvcRecordValidation(t *testing.T) {
	for _, u := range []string{"cockpit.socket", "cockpit.service", "apache2", "allmon3"} {
		if !validRecordUnit(u) {
			t.Errorf("%q rejected", u)
		}
	}
	for _, u := range []string{"", "asterisk.service", "apache2.service", "apache2 ", "../apache2.service", "ssh.service"} {
		if validRecordUnit(u) {
			t.Errorf("%q accepted", u)
		}
	}
	if !strings.Contains(DefaultSvcRecord, "disabled-units") {
		t.Fatal(DefaultSvcRecord)
	}
}

func TestWebPortMoveBackConfirm(t *testing.T) {
	r := svcRules(t)
	r["ss -ltnH"] = resp{out: "LISTEN 0 50 *:8080 *:*\nLISTEN 0 1 0.0.0.0:22 0.0.0.0:*\n"}
	o, f := svcOps(t, "# keep me\n[server]\nlisten = 192.0.2.10:8080\n[other]\nx = 1\n", r)
	ctx := context.Background()
	st := o.WebPortStatus(ctx)
	if !st.Available || !st.CanMove || st.Port != 8080 || st.On80 || st.BackPort != 8080 {
		t.Fatalf("%+v", st)
	}
	st, err := o.WebPortSet(ctx, WebPortActionMove)
	if err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(o.NodespotConf)
	if !strings.Contains(string(conf), "listen = 192.0.2.10:80\n") || !strings.Contains(string(conf), "# keep me") || !strings.Contains(string(conf), "x = 1") {
		t.Fatalf("%s", conf)
	}
	di, err := os.ReadFile(filepath.Join(o.Svc.DropinDir, "port80.conf"))
	if err != nil || !strings.Contains(string(di), "AmbientCapabilities=CAP_NET_BIND_SERVICE") {
		t.Fatalf("%v %s", err, di)
	}
	if !st.On80 || !st.DropIn || st.Pending == nil || st.Previous != "192.0.2.10:8080" || st.BackPort != 8080 {
		t.Fatalf("%+v", st)
	}
	// reload first, then a non-blocking restart; the revert timer is armed
	if f.called("systemctl daemon-reload") < 1 || f.called("systemctl --no-block restart nodespot.service") != 1 ||
		f.called("systemd-run --unit=nodespot-webport-guard --on-active=60s") != 1 {
		t.Fatalf("%v", f.calls)
	}
	// a second move is refused
	if _, err := o.WebPortSet(ctx, WebPortActionMove); !errors.Is(err, ErrAlreadyPort80) {
		t.Fatalf("%v", err)
	}
	// confirm clears the marker and stops the timer
	st, err = o.WebPortSet(ctx, WebPortActionConfirm)
	if err != nil || st.Pending != nil || f.called("systemctl stop nodespot-webport-guard.timer") < 1 {
		t.Fatalf("%v %+v %v", err, st, f.calls)
	}
	// the guard now does nothing
	if err := o.WebPortGuard(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(o.NodespotConf); !strings.Contains(string(c), ":80\n") {
		t.Fatalf("%s", c)
	}
	// back restores the saved listen value and removes the drop-in
	st, err = o.WebPortSet(ctx, WebPortActionBack)
	if err != nil {
		t.Fatal(err)
	}
	conf, _ = os.ReadFile(o.NodespotConf)
	if !strings.Contains(string(conf), "listen = 192.0.2.10:8080\n") || st.DropIn {
		t.Fatalf("%s %+v", conf, st)
	}
	if _, err := os.Stat(filepath.Join(o.Svc.DropinDir, "port80.conf")); err == nil {
		t.Fatal("drop-in left")
	}
	if _, err := o.WebPortSet(ctx, WebPortActionBack); !errors.Is(err, ErrNotOnPort80) {
		t.Fatalf("%v", err)
	}
	if _, err := o.WebPortSet(ctx, "sideways"); err == nil {
		t.Fatal("bad action accepted")
	}
}

func TestWebPortMoveRefusals(t *testing.T) {
	// something holds port 80
	o, f := svcOps(t, "[server]\nlisten = :8080\n", svcRules(t)) // fixture has *:80
	st := o.WebPortStatus(context.Background())
	if st.CanMove || st.Reason != "busy" || !st.ApacheActive {
		t.Fatalf("%+v", st)
	}
	if _, err := o.WebPortSet(context.Background(), WebPortActionMove); !errors.Is(err, ErrPort80Busy) {
		t.Fatalf("%v", err)
	}
	if c, _ := os.ReadFile(o.NodespotConf); !strings.Contains(string(c), ":8080") || f.called("systemctl --no-block") != 0 {
		t.Fatalf("%s %v", c, f.calls)
	}
	if _, err := os.Stat(o.Svc.DropinDir); err == nil {
		t.Fatal("drop-in dir created")
	}
	// ss missing: the port cannot be checked, so no move
	r := svcRules(t)
	delete(r, "ss -ltnH")
	o2, _ := svcOps(t, "[server]\nlisten = :8080\n", r)
	if st := o2.WebPortStatus(context.Background()); st.CanMove || st.Reason != "unchecked" {
		t.Fatalf("%+v", st)
	}
	if _, err := o2.WebPortSet(context.Background(), WebPortActionMove); !errors.Is(err, ErrPort80Unchecked) {
		t.Fatalf("%v", err)
	}
}

func TestWebPortMoveRollback(t *testing.T) {
	r := svcRules(t)
	r["ss -ltnH"] = resp{out: "LISTEN 0 50 *:8080 *:*\n"}
	r["systemd-run"] = resp{out: "no systemd\n", code: 1}
	o, _ := svcOps(t, "[server]\nlisten = :8080\n", r)
	if _, err := o.WebPortSet(context.Background(), WebPortActionMove); err == nil {
		t.Fatal("no error")
	}
	// the timer could not be armed: nothing may stay changed
	if c, _ := os.ReadFile(o.NodespotConf); !strings.Contains(string(c), "listen = :8080\n") {
		t.Fatalf("%s", c)
	}
	if _, err := os.Stat(filepath.Join(o.Svc.DropinDir, "port80.conf")); err == nil {
		t.Fatal("drop-in left")
	}
	if o.pending() != nil {
		t.Fatal("marker left")
	}
}

func TestWebPortGuardReverts(t *testing.T) {
	r := svcRules(t)
	r["ss -ltnH"] = resp{out: "LISTEN 0 50 *:8080 *:*\n"}
	o, f := svcOps(t, "[server]\nlisten = :8080\n", r)
	ctx := context.Background()
	if _, err := o.WebPortSet(ctx, WebPortActionMove); err != nil {
		t.Fatal(err)
	}
	if err := o.WebPortGuard(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(o.NodespotConf); !strings.Contains(string(c), "listen = :8080\n") {
		t.Fatalf("%s", c)
	}
	if _, err := os.Stat(filepath.Join(o.Svc.DropinDir, "port80.conf")); err == nil {
		t.Fatal("drop-in left")
	}
	if f.called("systemctl --no-block restart nodespot.service") != 2 {
		t.Fatalf("%v", f.calls)
	}
	st := o.WebPortStatus(ctx)
	if st.Pending != nil || st.Reverted == "" || st.Port != 8080 {
		t.Fatalf("%+v", st)
	}
}

func TestListenWithPort(t *testing.T) {
	for in, want := range map[string]string{":8080": ":80", "192.0.2.10:8080": "192.0.2.10:80", "[2001:db8::1]:8080": "[2001:db8::1]:80", "junk": ":80"} {
		if got := listenWithPort(in, 80); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestMapSvcErr(t *testing.T) {
	for _, e := range svcErrors {
		if got := mapSvcErr(errors.New(e.Error() + " extra")); got != e {
			t.Errorf("%v -> %v", e, got)
		}
	}
	if mapSvcErr(nil) != nil || mapSvcErr(errors.New("other")).Error() != "other" {
		t.Fatal("mapping")
	}
}

func TestSvcDemo(t *testing.T) {
	d := NewDemoSystem()
	ctx := context.Background()
	if _, err := d.WebPortSet(ctx, WebPortActionMove); !errors.Is(err, ErrPort80Busy) {
		t.Fatalf("%v", err)
	}
	if _, err := d.SvcSet(ctx, "apache", SvcActionDisable, true); err != nil {
		t.Fatal(err)
	}
	st, err := d.WebPortSet(ctx, WebPortActionMove)
	if err != nil || !st.On80 || st.Pending == nil {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := d.SvcSet(ctx, "apache", SvcActionEnable, false); !errors.Is(err, ErrPort80Held) {
		t.Fatalf("%v", err)
	}
	if st, err = d.WebPortSet(ctx, WebPortActionBack); err != nil || st.On80 {
		t.Fatalf("%v %+v", err, st)
	}
	// a second demo has its own state
	if s, _ := NewDemoSystem().SvcStatus(ctx); !s.Scopes[1].Active {
		t.Fatalf("%+v", s.Scopes)
	}
}
