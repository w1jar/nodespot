package helper

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sysOpsWith(r map[string]resp) (*Ops, *fakeRunner) {
	f := &fakeRunner{rules: r}
	return &Ops{Run: f, Now: func() time.Time { return time.Date(2026, 11, 20, 15, 15, 2, 0, time.UTC) },
		ResolvConf: filepath.Join("testdata", "resolv.conf")}, f
}

func TestParseHostAndTime(t *testing.T) {
	h, p, os, err := parseHostnamectlJSON(fixture(t, "hostnamectl_json.txt"))
	if err != nil || h != "node2001" || p != "" || os != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("%q %q %q %v", h, p, os, err)
	}
	// missing fields, nulls, static name only, pretty name
	h, p, os, err = parseHostnamectlJSON(`{"Hostname":null,"StaticHostname":"node2001","PrettyHostname":"Node 2001"}`)
	if err != nil || h != "node2001" || p != "Node 2001" || os != "" {
		t.Fatalf("%q %q %q %v", h, p, os, err)
	}
	if _, _, _, err = parseHostnamectlJSON("Hostname=node2001\n"); err == nil {
		t.Fatal("non-JSON accepted")
	}
	for in, want := range map[string]string{
		"PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n": "Debian GNU/Linux 12 (bookworm)",
		"# c\nNAME=x\nPRETTY_NAME='Raspbian 12'\n":         "Raspbian 12",
		"PRETTY_NAME=Debian\r\n":                           "Debian",
		"NAME=x\nJUNK\n":                                   "",
		"PRETTY_NAME=\"A \\\"q\\\" B\"\n":                  `A "q" B`,
		"#PRETTY_NAME=no\n":                                "",
	} {
		if got := parseOSReleasePretty(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if got := parseOSReleasePretty(fixture(t, "os-release.txt")); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatal(got)
	}
	var st HostStatus
	parseTimedatectl(fixture(t, "timedatectl_show.txt"), &st)
	if st.Timezone != "America/Chicago" || !st.NTP || !st.NTPSynchronized || !st.CanNTP || st.LocalRTC || !strings.HasPrefix(st.Clock, "Fri 2026-11-20") {
		t.Fatalf("%+v", st)
	}
	zones := parseTimezones(fixture(t, "timezones.txt"))
	if len(zones) != 5 || zones[1] != "America/Chicago" {
		t.Fatalf("%v", zones)
	}
}

func hostOps(rules map[string]resp, found bool) *Ops {
	o, _ := sysOpsWith(rules)
	o.OSRelease = filepath.Join("testdata", "os-release.txt")
	o.HostnameFile = filepath.Join("testdata", "no-such-hostname")
	o.GetHostname = func() (string, error) { return "node2001", nil }
	o.LookPath = func(string) (string, error) {
		if !found {
			return "", errors.New("not found")
		}
		return "/usr/bin/hostnamectl", nil
	}
	return o
}

func TestHostStatus(t *testing.T) {
	ctx := context.Background()
	// JSON path
	o := hostOps(map[string]resp{
		"hostnamectl --json=short": {out: fixture(t, "hostnamectl_json.txt")},
		"timedatectl show":         {out: fixture(t, "timedatectl_show.txt")},
	}, true)
	o.GetHostname = func() (string, error) { return "", errors.New("must not be used") }
	h := o.HostStatus(ctx)
	if !h.HostnameAvail || !h.HostnamectlAvail || h.HostnamectlMissing || !h.TimeAvail || h.Hostname != "node2001" || h.OS != "Debian GNU/Linux 12 (bookworm)" || h.Now != "2026-11-20T15:15:02Z" {
		t.Fatalf("%+v", h)
	}
	// JSON fails (old systemd): fallback, hostnamectl still usable
	o = hostOps(map[string]resp{"hostnamectl --json=short": {out: "Unknown option\n", code: 1}}, true)
	h = o.HostStatus(ctx)
	if !h.HostnameAvail || !h.HostnamectlAvail || h.HostnamectlMissing || h.Hostname != "node2001" || h.OS != "Debian GNU/Linux 12 (bookworm)" || h.TimeAvail {
		t.Fatalf("%+v", h)
	}
	// tool missing: the hostname is still shown, Set hostname is not usable
	o = hostOps(map[string]resp{}, false)
	h = o.HostStatus(ctx)
	if !h.HostnameAvail || h.HostnamectlAvail || !h.HostnamectlMissing || h.Hostname != "node2001" || h.TimeAvail {
		t.Fatalf("%+v", h)
	}
	// nothing readable at all
	o.GetHostname = func() (string, error) { return "", errors.New("x") }
	o.OSRelease = filepath.Join("testdata", "no-such-os-release")
	if h = o.HostStatus(ctx); h.HostnameAvail || h.Hostname != "" || h.OS != "" {
		t.Fatalf("%+v", h)
	}
}

func TestValidHostname(t *testing.T) {
	for _, s := range []string{"node2001", "Node-2001", "a", "0", strings.Repeat("a", 63)} {
		if _, err := ValidHostname(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "-node", "node-", "node_1", "node.local", "no de", "node;reboot", "$(x)", "-h", "--help", strings.Repeat("a", 64), "nöde", "node\n"} {
		if _, err := ValidHostname(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if got, _ := ValidHostname("Node-2001"); got != "node-2001" {
		t.Fatal(got)
	}
}

func TestSetHostname(t *testing.T) {
	o, f := sysOpsWith(map[string]resp{"hostnamectl": {}, "timedatectl show": {}})
	for _, bad := range []string{"-h", "a b", "x;y", ""} {
		if _, err := o.HostSetHostname(context.Background(), bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if f.called("hostnamectl set-hostname") != 0 {
		t.Fatalf("ran for invalid names: %v", f.calls)
	}
	if _, err := o.HostSetHostname(context.Background(), "Node2001"); err != nil {
		t.Fatal(err)
	}
	if f.calls[0] != "hostnamectl set-hostname node2001" {
		t.Fatalf("%v", f.calls)
	}
	// a failing tool is an error carrying its message
	o2, _ := sysOpsWith(map[string]resp{"hostnamectl": {out: "Access denied\n", code: 1}})
	if _, err := o2.HostSetHostname(context.Background(), "node2001"); err == nil || !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("%v", err)
	}
	// a missing tool
	o3, _ := sysOpsWith(map[string]resp{})
	if _, err := o3.HostSetHostname(context.Background(), "node2001"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("%v", err)
	}
}

func TestSetTimezoneAndNTP(t *testing.T) {
	o, f := sysOpsWith(map[string]resp{
		"timedatectl list-timezones": {out: fixture(t, "timezones.txt")},
		"timedatectl set-timezone":   {},
		"timedatectl set-ntp":        {},
		"timedatectl show":           {out: fixture(t, "timedatectl_show.txt")},
	})
	ctx := context.Background()
	for _, bad := range []string{"", "Mars/Olympus", "../etc/passwd", "America/Chicago;ls", "-h", "america/chicago", "UTC "} {
		if _, err := o.HostSetTimezone(ctx, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if f.called("timedatectl set-timezone") != 0 {
		t.Fatalf("ran for invalid zones: %v", f.calls)
	}
	if _, err := o.HostSetTimezone(ctx, "Europe/London"); err != nil {
		t.Fatal(err)
	}
	if f.called("timedatectl set-timezone Europe/London") != 1 {
		t.Fatalf("%v", f.calls)
	}
	if _, err := o.HostSetNTP(ctx, false); err != nil {
		t.Fatal(err)
	}
	if f.called("timedatectl set-ntp false") != 1 {
		t.Fatalf("%v", f.calls)
	}
	if _, err := o.HostSetNTP(ctx, true); err != nil || f.called("timedatectl set-ntp true") != 1 {
		t.Fatalf("%v %v", err, f.calls)
	}
}

func TestPoweroff(t *testing.T) {
	r := map[string]resp{"systemctl --no-block poweroff": {}}
	idleJob(r)
	r["journalctl -u nodespot-osupgrade"] = resp{}
	o, f := sysOpsWith(r)
	if res, err := o.HostPoweroff(context.Background()); err != nil || !res.OK {
		t.Fatalf("%v %v", res, err)
	}
	if f.called("systemctl --no-block poweroff") != 1 {
		t.Fatalf("%v", f.calls)
	}
}

func TestLogsRead(t *testing.T) {
	ctx := context.Background()
	o, f := sysOpsWith(map[string]resp{"journalctl": {out: "2026-11-20T09:00:00-0600 node2001 nodespot-node[1]: hello\n"}})
	for _, tc := range []struct{ unit, since string }{{"sshd", ""}, {"nodespot-node;ls", ""}, {"-n", ""}, {"", ""}, {"nodespot-node.service", ""}, {"nodespot-node", "forever"}, {"nodespot-node", "--since=x"}} {
		if _, err := o.LogsRead(ctx, tc.unit, 100, tc.since); err == nil {
			t.Errorf("%+v accepted", tc)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran for invalid requests: %v", f.calls)
	}
	cases := []struct {
		unit  string
		lines int
		since string
		want  string
	}{
		{"nodespot-node", 0, "", "journalctl -u nodespot-node -n 200 --no-pager -o short-iso"},
		{"nodespot", 5, "", "journalctl -u nodespot -n 50 --no-pager -o short-iso"},
		{"NetworkManager", 99999, "1h", "journalctl -u NetworkManager -n 1000 --no-pager -o short-iso --since 1 hour ago"},
		{"md380-emu", 300, "24h", "journalctl -u md380-emu -n 300 --no-pager -o short-iso --since 24 hours ago"},
		{"nodespot-helper", 100, "boot", "journalctl -u nodespot-helper -n 100 --no-pager -o short-iso -b"},
	}
	for _, c := range cases {
		f.calls = nil
		res, err := o.LogsRead(ctx, c.unit, c.lines, c.since)
		if err != nil || !res.Available || !strings.Contains(res.Text, "hello") {
			t.Fatalf("%+v %v", res, err)
		}
		if len(f.calls) != 1 || f.calls[0] != c.want {
			t.Fatalf("got %q want %q", f.calls, c.want)
		}
	}
	// no journalctl: unavailable, not an error
	o2, _ := sysOpsWith(map[string]resp{})
	if res, err := o2.LogsRead(ctx, "nodespot-node", 100, ""); err != nil || res.Available {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestCapTail(t *testing.T) {
	s, cut := capTail("aaaa\nbbbb\ncccc\n", 8)
	if !cut || s != "cccc\n" {
		t.Fatalf("%q %v", s, cut)
	}
	if s, cut := capTail("abc", 10); cut || s != "abc" {
		t.Fatal(s)
	}
}

func TestLogUnitsContainRequired(t *testing.T) {
	for _, u := range []string{"nodespot-node", "nodespot", "nodespot-helper", "NetworkManager", "systemd-networkd", "md380-emu", "nodespot-wifi-watchdog"} {
		if !ValidLogUnit(u) {
			t.Errorf("%s missing", u)
		}
	}
}

func TestParseServices(t *testing.T) {
	got := parseServices(fixture(t, "systemctl_show_units.txt"))
	want := []ServiceState{
		{Unit: "asterisk.service", Desc: "Asterisk PBX and telephony daemon", Active: "active", Sub: "running", Enabled: "enabled", Since: "Fri 2026-11-20 08:00:01 CST"},
		{Unit: "cockpit.socket", Desc: "Cockpit Web Service Socket", Active: "inactive", Sub: "dead", Enabled: "disabled"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestServicesStatusArgv(t *testing.T) {
	o, f := sysOpsWith(map[string]resp{"systemctl show": {out: fixture(t, "systemctl_show_units.txt")}})
	res := o.ServicesStatus(context.Background())
	if !res.Available || len(res.Services) != 2 {
		t.Fatalf("%+v", res)
	}
	c := f.calls[0]
	for _, w := range []string{"nodespot-node.service", "cockpit.socket", "md380-emu.service", " -- "} {
		if !strings.Contains(c, w) {
			t.Errorf("%s missing from %s", w, c)
		}
	}
	o2, _ := sysOpsWith(map[string]resp{})
	if o2.ServicesStatus(context.Background()).Available {
		t.Fatal("available without systemctl")
	}
}

func TestParseNetwork(t *testing.T) {
	ifs, err := parseIPAddr(fixture(t, "ip_addr.json"))
	if err != nil || len(ifs) != 3 {
		t.Fatalf("%v %v", ifs, err)
	}
	eth := ifs[1]
	if eth.Name != "eth0" || eth.State != "UP" || eth.MAC != "02:00:00:00:20:01" || len(eth.Addrs) != 2 ||
		eth.Addrs[0] != (NetAddr{Family: "inet", Addr: "192.0.2.10", Prefix: 24, Scope: "global", Dynamic: true}) {
		t.Fatalf("%+v", eth)
	}
	if ifs[2].State != "DOWN" || len(ifs[2].Addrs) != 0 {
		t.Fatalf("%+v", ifs[2])
	}
	rs, err := parseIPRoute(fixture(t, "ip_route.json"), "inet")
	if err != nil || len(rs) != 1 || rs[0] != (NetRoute{Family: "inet", Gateway: "192.0.2.1", Dev: "eth0", Metric: 100}) {
		t.Fatalf("%v %v", rs, err)
	}
	if _, err := parseIPAddr("not json"); err == nil {
		t.Fatal("bad json accepted")
	}
	if got := parseResolvConf(fixture(t, "resolv.conf")); !reflect.DeepEqual(got, []string{"192.0.2.1", "2001:db8::53"}) {
		t.Fatalf("%v", got)
	}
	if got := parseResolvectl(fixture(t, "resolvectl_status.txt")); !reflect.DeepEqual(got, []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"}) {
		t.Fatalf("%v", got)
	}
	if !isStubResolver(parseResolvConf(fixture(t, "resolv_stub.conf"))) || isStubResolver([]string{"192.0.2.1"}) {
		t.Fatal("stub detection")
	}
}

func TestNetStatus(t *testing.T) {
	o, f := sysOpsWith(map[string]resp{
		"ip -j addr":     {out: fixture(t, "ip_addr.json")},
		"ip -j route":    {out: fixture(t, "ip_route.json")},
		"ip -j -6 route": {out: "[]"},
	})
	ns := o.NetStatus(context.Background())
	if !ns.Available || len(ns.Interfaces) != 3 || len(ns.Gateways) != 1 || ns.DNSSource != "resolv.conf" || len(ns.DNS) != 2 {
		t.Fatalf("%+v", ns)
	}
	if f.called("resolvectl") != 0 {
		t.Fatal("resolvectl used without a stub resolver")
	}
	// stub resolver: ask resolvectl
	o.ResolvConf = filepath.Join("testdata", "resolv_stub.conf")
	o.Run.(*fakeRunner).rules["resolvectl status"] = resp{out: fixture(t, "resolvectl_status.txt")}
	ns = o.NetStatus(context.Background())
	if ns.DNSSource != "resolvectl" || len(ns.DNS) != 3 {
		t.Fatalf("%+v", ns)
	}
	// no ip: unavailable
	o2, _ := sysOpsWith(map[string]resp{})
	if o2.NetStatus(context.Background()).Available {
		t.Fatal("available without ip")
	}
}

func TestSysOpsDispatch(t *testing.T) {
	o, _ := sysOpsWith(map[string]resp{"hostnamectl --json=short": {out: fixture(t, "hostnamectl_json.txt")}, "timedatectl show": {out: fixture(t, "timedatectl_show.txt")}})
	ctx := context.Background()
	if _, err := o.HandleRequest(ctx, Request{Op: OpHostStatus}); err != nil {
		t.Fatal(err)
	}
	// the sys field is only for sys ops, and sys ops take no other field
	if _, err := o.HandleRequest(ctx, Request{Op: OpReboot, Sys: &SysRequest{}}); err == nil {
		t.Fatal("sys field accepted by reboot")
	}
	if _, err := o.HandleRequest(ctx, Request{Op: OpHostStatus, Backup: &BackupRequest{}}); err == nil {
		t.Fatal("backup field accepted by host-status")
	}
	if _, err := o.HandleRequest(ctx, Request{Op: OpHostSetNTP}); err == nil {
		t.Fatal("ntp without value accepted")
	}
	for _, op := range []string{OpHostStatus, OpHostSetHostname, OpHostSetTimezone, OpHostSetNTP, OpHostTimezones, OpHostPoweroff, OpLogsRead, OpServicesStatus, OpNetStatus} {
		if !knownOp(op) {
			t.Errorf("%s is not a known op", op)
		}
	}
}
