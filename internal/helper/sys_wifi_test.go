package helper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	idA = "00000000-0000-4000-8000-0000000000a1" // ExampleNet
	idB = "00000000-0000-4000-8000-0000000000a2" // Example:Guest
	idO = "00000000-0000-4000-8000-0000000000a5" // OtherAP (access point mode)
)

const nmProps = "nmcli -t -f 802-11-wireless.ssid,802-11-wireless.hidden,802-11-wireless.mode,802-11-wireless-security.key-mgmt connection show uuid "

func wifiRules() map[string]resp {
	return map[string]resp{
		"nmcli -t -f RUNNING general":                            {out: "running\n"},
		"nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status": {out: "eth0:ethernet:connected:Wired connection 1\nwlan0:wifi:connected:ExampleNet\n"},
		"nmcli radio wifi":                                       {out: "enabled\n"},
		"ip -j addr show dev wlan0":                              {out: `[{"ifname":"wlan0","operstate":"UP","addr_info":[{"family":"inet","local":"192.0.2.20","prefixlen":24,"scope":"global"}]}]`},
		"nmcli -t -f NAME,UUID,TYPE,AUTOCONNECT,AUTOCONNECT-PRIORITY,ACTIVE connection show": {out: fixtureS("nmcli-connections.txt")},
		nmProps + idA: {out: "802-11-wireless.ssid:ExampleNet\n802-11-wireless.hidden:no\n802-11-wireless.mode:infrastructure\n802-11-wireless-security.key-mgmt:wpa-psk\n"},
		nmProps + idB: {out: "802-11-wireless.ssid:Example\\:Guest\n802-11-wireless.hidden:yes\n802-11-wireless.mode:infrastructure\n802-11-wireless-security.key-mgmt:--\n"},
		nmProps + idO: {out: "802-11-wireless.ssid:OtherAP\n802-11-wireless.hidden:no\n802-11-wireless.mode:ap\n802-11-wireless-security.key-mgmt:--\n"},
		"nmcli -t -f IN-USE,SSID,SIGNAL,CHAN,SECURITY device wifi list": {out: fixtureS("nmcli-wifi-list.txt")},
	}
}

func fixtureS(name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return string(b)
}

func newWifiOps(t *testing.T, rules map[string]resp) (*Ops, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{rules: rules}
	o := &Ops{Run: f}
	d := t.TempDir()
	o.Wifi = WifiPaths{NMDir: filepath.Join(d, "nm"), StateDir: filepath.Join(d, "state"), RunDir: filepath.Join(d, "run"),
		Uptime: filepath.Join(d, "uptime"), Hostname: func() string { return "node2001" }}
	if err := os.MkdirAll(o.Wifi.NMDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return o, f
}

func TestSplitTerse(t *testing.T) {
	cases := map[string][]string{
		`a:b:c`:           {"a", "b", "c"},
		`a\:b:c`:          {"a:b", "c"},
		`a\\:b`:           {`a\`, "b"},
		`::x`:             {"", "", "x"},
		``:                {""},
		`trailing\`:       {`trailing\`},
		`Example\:Net:82`: {"Example:Net", "82"},
	}
	for in, want := range cases {
		got := splitTerse(in)
		if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("splitTerse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseWifiScan(t *testing.T) {
	aps := parseWifiScan(fixtureS("nmcli-wifi-list.txt"))
	by := map[string]WifiAP{}
	for _, a := range aps {
		by[a.SSID] = a
	}
	if len(aps) != 6 {
		t.Fatalf("got %d networks, want 6 (hidden and garbage skipped, duplicates merged): %+v", len(aps), aps)
	}
	if a := by["ExampleNet"]; !a.InUse || a.Signal != 82 || a.Channel != 6 || !a.Secured || !a.Supported {
		t.Errorf("ExampleNet: %+v", a)
	}
	if _, ok := by["Example:Colon"]; !ok {
		t.Error("escaped colon in SSID lost")
	}
	if _, ok := by[`Back\slash`]; !ok {
		t.Error("escaped backslash in SSID lost")
	}
	if a := by["ExampleOpen"]; a.Secured || a.Security != "" {
		t.Errorf("open network: %+v", a)
	}
	if a := by["Corp"]; a.Supported {
		t.Errorf("802.1X must be unsupported: %+v", a)
	}
	for i := 1; i < len(aps); i++ {
		if aps[i].Signal > aps[i-1].Signal {
			t.Error("not sorted by signal")
		}
	}
}

func TestParseDevicesAndConns(t *testing.T) {
	devs := parseNMDevices(fixtureS("nmcli-device-status.txt"))
	if len(devs) != 4 || devs[1].Name != "wlan0" || devs[1].Conn != "ExampleNet" || devs[2].Conn != "" {
		t.Errorf("devices: %+v", devs)
	}
	rows := parseConnRows(fixtureS("nmcli-connections.txt"))
	if len(rows) != 5 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[1].Name != "Example:Guest" || rows[1].Auto != true || rows[1].Active || rows[1].Priority != 90 {
		t.Errorf("row 1: %+v", rows[1])
	}
	if rows[2].Priority != -999 {
		t.Errorf("negative priority: %+v", rows[2])
	}
}

func TestParseTerseProps(t *testing.T) {
	m := parseTerseProps("a.b:x\\:y\nc.d:--\nbad\n:novalue\n")
	if m["a.b"] != "x:y" || m["c.d"] != "--" || len(m) != 2 {
		t.Errorf("%v", m)
	}
}

func TestValidators(t *testing.T) {
	long33 := strings.Repeat("a", 33)
	for _, s := range []string{"", long33, "bad\x00ssid", "bad\nssid", "\xff\xfe"} {
		if ValidSSID(s) == nil {
			t.Errorf("SSID %q accepted", s)
		}
	}
	for _, s := range []string{"a", "ExampleNet", strings.Repeat("a", 32), "Café", "with space"} {
		if err := ValidSSID(s); err != nil {
			t.Errorf("SSID %q: %v", s, err)
		}
	}
	bad := []string{"short", strings.Repeat("a", 64-1) + "é", strings.Repeat("g", 64), strings.Repeat("a", 64+1), "tab\tinside-pass", "caf\u00e9-password"}
	for _, s := range bad {
		err := ValidWifiPassword(s)
		if err == nil {
			t.Errorf("password %q accepted", s)
		} else if strings.Contains(err.Error(), s) {
			t.Errorf("error repeats the password: %v", err)
		}
	}
	for _, s := range []string{"", "example-pass", strings.Repeat("a", 63), strings.Repeat("0123456789abcdef", 4)} {
		if err := ValidWifiPassword(s); err != nil {
			t.Errorf("password %q: %v", s, err)
		}
	}
	if !ValidConnID(idA) || ValidConnID("x") || ValidConnID(idA+" ") || ValidConnID("-f") {
		t.Error("ValidConnID")
	}
}

func TestBuildKeyfile(t *testing.T) {
	k := buildWifiKeyfile("Example Net", idA, "Ex\\ample", "example-pass", "wpa-psk", true, 100)
	for _, want := range []string{"id=Example Net\n", "uuid=" + idA + "\n", "autoconnect-priority=100\n", "hidden=true\n",
		"key-mgmt=wpa-psk\n", "psk=example-pass\n", "ssid=69;120;92;97;109;112;108;101;\n"} {
		if !strings.Contains(k, want) {
			t.Errorf("keyfile lacks %q:\n%s", want, k)
		}
	}
	open := buildWifiKeyfile("ExampleOpen", idA, "ExampleOpen", "", "wpa-psk", false, 90)
	if strings.Contains(open, "wifi-security") || strings.Contains(open, "hidden") {
		t.Errorf("open network keyfile:\n%s", open)
	}
	if keyfileEscape(" a ") != `\sa\s` || keyfileEscape(`a\b`) != `a\\b` {
		t.Error("keyfileEscape")
	}
	if keyMgmtFor("WPA3") != "sae" || keyMgmtFor("WPA2 WPA3") != "wpa-psk" || keyMgmtFor("WPA2") != "wpa-psk" {
		t.Error("keyMgmtFor")
	}
}

func TestCountNeighbors(t *testing.T) {
	text := "10.42.0.12 lladdr 02:00:00:00:00:01 REACHABLE\n10.42.0.13 lladdr 02:00:00:00:00:02 STALE\n10.42.0.14  FAILED\n10.42.0.15 lladdr 02:00:00:00:00:03 FAILED\n"
	if n := countNeighbors(text); n != 2 {
		t.Errorf("got %d", n)
	}
}

func TestHotspotSSID(t *testing.T) {
	if s := hotspotSSID("node2001"); s != "nodespot-node2001" {
		t.Error(s)
	}
	if s := hotspotSSID("Node_2001.example"); s != "nodespot-node2001example" {
		t.Error(s)
	}
	if s := hotspotSSID(""); s != "nodespot-node" {
		t.Error(s)
	}
	if s := hotspotSSID(strings.Repeat("a", 63)); len(s) > 32 {
		t.Error(len(s))
	}
}

func TestWifiStatusAndList(t *testing.T) {
	o, _ := newWifiOps(t, wifiRules())
	st := o.WifiStatus(context.Background())
	if !st.Available || !st.HasWifi || st.Active != "ExampleNet" || !st.Ethernet || st.Radio != "enabled" {
		t.Errorf("%+v", st)
	}
	if len(st.Devices) != 2 || st.Devices[1].Addrs[0] != "192.0.2.20" {
		t.Errorf("devices %+v", st.Devices)
	}
	if !st.Hotspot.Enabled || st.Hotspot.SSID != "nodespot-node2001" || st.Hotspot.DelaySeconds != 180 || st.Hotspot.Address != "10.42.0.1" {
		t.Errorf("hotspot defaults %+v", st.Hotspot)
	}
	l, err := o.WifiList(context.Background())
	if err != nil || len(l.Networks) != 2 {
		t.Fatalf("%v %+v", err, l)
	}
	if l.Networks[0].SSID != "ExampleNet" || !l.Networks[0].Active || !l.Networks[0].Secured {
		t.Errorf("%+v", l.Networks[0])
	}
	if n := l.Networks[1]; n.SSID != "Example:Guest" || !n.Hidden || n.Secured || n.Priority != 90 {
		t.Errorf("%+v", n)
	}
}

func TestWifiStatusMissingTools(t *testing.T) {
	o, _ := newWifiOps(t, map[string]resp{"nmcli": {err: errors.New("not found")}})
	st := o.WifiStatus(context.Background())
	if st.Available || st.Reason == "" || st.Devices == nil {
		t.Errorf("%+v", st)
	}
	if _, err := o.WifiScan(context.Background()); err == nil {
		t.Error("scan without nmcli")
	}
	if _, err := o.WifiList(context.Background()); err == nil {
		t.Error("list without nmcli")
	}
	r := wifiRules()
	r["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status"] = resp{out: "eth0:ethernet:connected:x\n"}
	o, _ = newWifiOps(t, r)
	if st := o.WifiStatus(context.Background()); st.HasWifi || st.Reason != "no WiFi device found" {
		t.Errorf("%+v", st)
	}
	if _, err := o.WifiConnect(context.Background(), "ExampleNet", "example-pass", false); err == nil {
		t.Error("connect without a WiFi device")
	}
}

func TestWifiScanSavedFlag(t *testing.T) {
	o, f := newWifiOps(t, wifiRules())
	res, err := o.WifiScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	saved := map[string]bool{}
	for _, a := range res.Networks {
		saved[a.SSID] = a.Saved
	}
	if !saved["ExampleNet"] || saved["ExampleOpen"] {
		t.Errorf("%v", saved)
	}
	if f.called("nmcli -t -f IN-USE,SSID,SIGNAL,CHAN,SECURITY device wifi list --rescan yes") != 1 {
		t.Error("no rescan requested")
	}
}

func TestWifiScanRescanRefused(t *testing.T) {
	r := wifiRules()
	r["nmcli -t -f IN-USE,SSID,SIGNAL,CHAN,SECURITY device wifi list --rescan yes"] = resp{code: 1, out: "Error: Scanning not allowed immediately following previous scan."}
	o, _ := newWifiOps(t, r)
	res, err := o.WifiScan(context.Background())
	if err != nil || res.Note == "" || len(res.Networks) == 0 {
		t.Errorf("%v %+v", err, res)
	}
}

// argvOK fails when a secret appears in any recorded command.
func noSecretInArgv(t *testing.T, f *fakeRunner, secret string) {
	t.Helper()
	for _, c := range f.calls {
		if strings.Contains(c, secret) {
			t.Errorf("secret in argv: %q", c)
		}
	}
}

func TestWifiConnectNew(t *testing.T) {
	r := wifiRules()
	r["nmcli connection load"] = resp{}
	r["nmcli --wait 30 connection up uuid "] = resp{}
	o, f := newWifiOps(t, r)
	res, err := o.WifiConnect(context.Background(), "ExampleGuest2", "example-pass", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated || !ValidConnID(res.ID) {
		t.Errorf("%+v", res)
	}
	noSecretInArgv(t, f, "example-pass")
	files, _ := filepath.Glob(filepath.Join(o.Wifi.NMDir, "nodespot-*.nmconnection"))
	if len(files) != 1 {
		t.Fatalf("keyfile not written: %v", files)
	}
	st, _ := os.Stat(files[0])
	if st.Mode().Perm() != 0o600 {
		t.Errorf("keyfile mode %v", st.Mode().Perm())
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "psk=example-pass") || !strings.Contains(string(b), "autoconnect-priority=80\n") {
		t.Errorf("keyfile:\n%s", b)
	}
	if f.called("nmcli connection load "+files[0]) != 1 {
		t.Error("keyfile not loaded")
	}
}

func TestWifiConnectReplacesSameSSID(t *testing.T) {
	r := wifiRules()
	r["nmcli connection load"] = resp{}
	r["nmcli --wait 30 connection up uuid "] = resp{}
	r["nmcli connection delete uuid "] = resp{}
	o, f := newWifiOps(t, r)
	res, err := o.WifiConnect(context.Background(), "ExampleNet", "another-pass", false)
	if err != nil || !res.Updated {
		t.Fatalf("%v %+v", err, res)
	}
	if f.called("nmcli connection delete uuid "+idA) != 1 {
		t.Error("old profile not deleted")
	}
	files, _ := filepath.Glob(filepath.Join(o.Wifi.NMDir, "nodespot-*.nmconnection"))
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "autoconnect-priority=100\n") {
		t.Errorf("priority not kept:\n%s", b)
	}
}

func TestWifiConnectFailureRollsBackAndRedacts(t *testing.T) {
	r := wifiRules()
	r["nmcli connection load"] = resp{}
	r["nmcli --wait 30 connection up uuid "] = resp{code: 4, out: "Error: Connection activation failed: Secrets were required, but not provided. example-pass"}
	r["nmcli connection delete uuid "] = resp{}
	o, f := newWifiOps(t, r)
	_, err := o.WifiConnect(context.Background(), "ExampleGuest2", "example-pass", false)
	if err == nil || strings.Contains(err.Error(), "example-pass") || !strings.Contains(err.Error(), "password may be wrong") {
		t.Fatalf("%v", err)
	}
	files, _ := filepath.Glob(filepath.Join(o.Wifi.NMDir, "nodespot-*.nmconnection"))
	if len(files) != 0 {
		t.Error("keyfile left behind")
	}
	if f.called("nmcli connection delete uuid ") != 1 {
		t.Error("profile not deleted after failure")
	}
	noSecretInArgv(t, f, "example-pass")
}

func TestWifiConnectLoadFailureRedacts(t *testing.T) {
	r := wifiRules()
	r["nmcli connection load"] = resp{code: 1, out: "Error: bad file psk=example-pass"}
	o, _ := newWifiOps(t, r)
	_, err := o.WifiConnect(context.Background(), "ExampleGuest2", "example-pass", false)
	if err == nil || strings.Contains(err.Error(), "example-pass") {
		t.Fatalf("%v", err)
	}
}

func TestWifiConnectValidation(t *testing.T) {
	o, f := newWifiOps(t, wifiRules())
	for _, c := range [][2]string{{"", "example-pass"}, {strings.Repeat("s", 33), ""}, {"ExampleNet", "short"}} {
		if _, err := o.WifiConnect(context.Background(), c[0], c[1], false); err == nil {
			t.Errorf("accepted %q", c)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran for invalid input: %v", f.calls)
	}
}

func TestWifiConnectOpenAndSAE(t *testing.T) {
	r := wifiRules()
	r["nmcli connection load"] = resp{}
	r["nmcli --wait 30 connection up uuid "] = resp{}
	o, _ := newWifiOps(t, r)
	if _, err := o.WifiConnect(context.Background(), "ExampleOpen", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := o.WifiConnect(context.Background(), "Wpa3Only", "example-pass", false); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(o.Wifi.NMDir, "nodespot-*.nmconnection"))
	var open, sae bool
	for _, f := range files {
		b, _ := os.ReadFile(f)
		s := string(b)
		if strings.Contains(s, "id=ExampleOpen") && !strings.Contains(s, "wifi-security") {
			open = true
		}
		if strings.Contains(s, "id=Wpa3Only") && strings.Contains(s, "key-mgmt=sae") {
			sae = true
		}
	}
	if !open || !sae {
		t.Errorf("open %v sae %v", open, sae)
	}
	if _, err := o.WifiConnect(context.Background(), "Wpa3Only", strings.Repeat("a", 64), false); err == nil {
		t.Error("64-digit key accepted for WPA3")
	}
}

func TestWifiForget(t *testing.T) {
	r := wifiRules()
	r["nmcli connection delete uuid "] = resp{}
	o, f := newWifiOps(t, r)
	ctx := context.Background()
	if _, err := o.WifiForget(ctx, "not-a-uuid"); err == nil {
		t.Error("bad id")
	}
	if _, err := o.WifiForget(ctx, "00000000-0000-4000-8000-0000000000ff"); err == nil {
		t.Error("unknown id")
	}
	// the hotspot and AP profiles cannot be removed through forget
	for _, id := range []string{"00000000-0000-4000-8000-0000000000a4", idO, "00000000-0000-4000-8000-0000000000a3"} {
		if _, err := o.WifiForget(ctx, id); err == nil {
			t.Errorf("forgot %s", id)
		}
	}
	if f.called("nmcli connection delete") != 0 {
		t.Error("delete ran for a refused id")
	}
	if res, err := o.WifiForget(ctx, idB); err != nil || !res.OK {
		t.Fatalf("%v", err)
	}
	if f.called("nmcli connection delete uuid "+idB) != 1 {
		t.Error("not deleted")
	}
}

func TestWifiPriority(t *testing.T) {
	r := wifiRules()
	r["nmcli connection modify uuid "] = resp{}
	o, f := newWifiOps(t, r)
	ctx := context.Background()
	if _, err := o.WifiPriority(ctx, []string{idB, idA}); err != nil {
		t.Fatal(err)
	}
	if f.called("nmcli connection modify uuid "+idB+" connection.autoconnect-priority 100") != 1 ||
		f.called("nmcli connection modify uuid "+idA+" connection.autoconnect-priority 90") != 1 {
		t.Errorf("calls: %v", f.calls)
	}
	n := len(f.calls)
	for _, bad := range [][]string{nil, {idA}, {idA, idA}, {idA, idO}, {idA, "x"}, {idA, idB, idO}} {
		if _, err := o.WifiPriority(ctx, bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	for _, c := range f.calls[n:] {
		if strings.HasPrefix(c, "nmcli connection modify") {
			t.Errorf("modify ran for an invalid order: %s", c)
		}
	}
}

func TestHotspotSetOffAndOn(t *testing.T) {
	r := wifiRules()
	r["nmcli connection down"] = resp{}
	r["nmcli connection delete"] = resp{}
	r["nmcli connection add"] = resp{}
	r["firewall-cmd --state"] = resp{err: errors.New("not found")}
	o, f := newWifiOps(t, r)
	ctx := context.Background()
	st, err := o.WifiHotspotSet(ctx, false, 0)
	if err != nil || st.Hotspot.Enabled {
		t.Fatalf("%v %+v", err, st)
	}
	if got := o.loadHotspotCfg(); got.Enabled || got.DelaySeconds != 180 {
		t.Errorf("not persisted: %+v", got)
	}
	if info, err := os.Stat(o.hotspotCfgPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("hotspot.json: %v", err)
	}
	st, err = o.WifiHotspotSet(ctx, true, 300)
	if err != nil || !st.Hotspot.Enabled || st.Hotspot.DelaySeconds != 300 {
		t.Fatalf("%v %+v", err, st)
	}
	if f.called("nmcli connection add type wifi con-name nodespot-hotspot ifname wlan0 ssid nodespot-node2001") != 1 {
		t.Errorf("profile not created: %v", f.calls)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "nmcli connection add") {
			for _, want := range []string{"802-11-wireless.mode ap", "ipv4.method shared", "ipv4.never-default yes", "autoconnect no"} {
				if !strings.Contains(c, want) {
					t.Errorf("hotspot profile lacks %q: %s", want, c)
				}
			}
			if strings.Contains(c, "wifi-sec") || strings.Contains(c, "psk") {
				t.Errorf("hotspot must be open: %s", c)
			}
		}
	}
	if _, err := o.WifiHotspotSet(ctx, true, 5); err == nil {
		t.Error("delay 5 accepted")
	}
	if _, err := o.WifiHotspotSet(ctx, true, 99999); err == nil {
		t.Error("delay 99999 accepted")
	}
}

func TestHotspotFirewalldZone(t *testing.T) {
	r := wifiRules()
	r["nmcli connection"] = resp{}
	r["firewall-cmd --state"] = resp{out: "running\n"}
	r["firewall-cmd --permanent --get-zones"] = resp{out: "public nodespot-hotspot\n"}
	r["firewall-cmd"] = resp{}
	o, f := newWifiOps(t, r)
	if _, err := o.WifiHotspotSet(context.Background(), true, 0); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"firewall-cmd --permanent --delete-zone=nodespot-hotspot",
		"firewall-cmd --permanent --new-zone=nodespot-hotspot",
		"firewall-cmd --permanent --zone=nodespot-hotspot --set-target=DROP",
		"firewall-cmd --permanent --zone=nodespot-hotspot --add-port=8080/tcp",
		"firewall-cmd --permanent --zone=nodespot-hotspot --add-service=dhcp",
		"firewall-cmd --permanent --zone=nodespot-hotspot --add-service=dns",
		"firewall-cmd --reload",
	} {
		if f.called(want) == 0 {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	zoned := false
	for _, c := range f.calls {
		if strings.HasPrefix(c, "nmcli connection add") && strings.Contains(c, "connection.zone nodespot-hotspot") {
			zoned = true
		}
	}
	if !zoned {
		t.Error("hotspot profile not put in the zone")
	}
}

func TestHotspotFirewalldFailureBlocksHotspot(t *testing.T) {
	r := wifiRules()
	r["firewall-cmd --state"] = resp{out: "running\n"}
	r["firewall-cmd --permanent --get-zones"] = resp{out: "public\n"}
	r["firewall-cmd"] = resp{code: 1, out: "boom"}
	o, f := newWifiOps(t, r)
	if _, err := o.WifiHotspotSet(context.Background(), true, 0); err == nil {
		t.Fatal("expected an error")
	}
	if f.called("nmcli connection add") != 0 {
		t.Error("hotspot profile created without its firewall zone")
	}
}

func TestHotspotCfgDefaults(t *testing.T) {
	o, _ := newWifiOps(t, wifiRules())
	if c := o.loadHotspotCfg(); !c.Enabled || c.DelaySeconds != 180 {
		t.Errorf("%+v", c)
	}
	_ = os.MkdirAll(o.Wifi.StateDir, 0o700)
	_ = os.WriteFile(o.hotspotCfgPath(), []byte("garbage"), 0o600)
	if c := o.loadHotspotCfg(); !c.Enabled || c.DelaySeconds != 180 {
		t.Errorf("damaged file: %+v", c)
	}
	b, _ := json.Marshal(hotspotCfg{Enabled: false, DelaySeconds: 7})
	_ = os.WriteFile(o.hotspotCfgPath(), b, 0o600)
	if c := o.loadHotspotCfg(); c.Enabled || c.DelaySeconds != 180 {
		t.Errorf("out of range delay: %+v", c)
	}
}

func TestWatchDecide(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	base := WatchInput{Enabled: true, HasWifi: true, Saved: 1, Clients: -1, Now: t0, Delay: 3 * time.Minute, Retry: 10 * time.Minute}
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	type tc struct {
		name   string
		mod    func(in *WatchInput)
		action WatchAction
		noLink bool // NoLinkSince is zero afterwards
	}
	cases := []tc{
		{"no link yet starts the clock", func(in *WatchInput) {}, WatchNone, false},
		{"2 minutes without link", func(in *WatchInput) { in.NoLinkSince = at(-2) }, WatchNone, false},
		{"3 minutes without link starts", func(in *WatchInput) { in.NoLinkSince = at(-3) }, WatchStart, false},
		{"still connecting waits", func(in *WatchInput) { in.NoLinkSince = at(-9); in.Connecting = true }, WatchNone, false},
		{"ethernet blocks start", func(in *WatchInput) { in.NoLinkSince = at(-9); in.Ethernet = true }, WatchNone, true},
		{"wifi connected blocks start", func(in *WatchInput) { in.NoLinkSince = at(-9); in.WifiUp = true }, WatchNone, true},
		{"disabled never starts", func(in *WatchInput) { in.NoLinkSince = at(-9); in.Enabled = false }, WatchNone, true},
		{"no wifi device never starts", func(in *WatchInput) { in.NoLinkSince = at(-9); in.HasWifi = false }, WatchNone, true},
		{"saved network connected stops hotspot", func(in *WatchInput) { in.HotspotUp = true; in.WifiUp = true }, WatchStop, true},
		{"ethernet stops hotspot", func(in *WatchInput) { in.HotspotUp = true; in.Ethernet = true }, WatchStop, true},
		{"admin turns it off", func(in *WatchInput) { in.HotspotUp = true; in.Enabled = false }, WatchStop, true},
		{"hotspot with a client stays", func(in *WatchInput) {
			in.HotspotUp = true
			in.NoLinkSince = at(-30)
			in.HotspotSince = at(-20)
			in.Clients = 1
		}, WatchNone, false},
		{"hotspot idle before retry stays", func(in *WatchInput) {
			in.HotspotUp = true
			in.NoLinkSince = at(-30)
			in.HotspotSince = at(-9)
			in.Clients = 0
		}, WatchNone, false},
		{"hotspot idle after retry stops to retry", func(in *WatchInput) {
			in.HotspotUp = true
			in.NoLinkSince = at(-30)
			in.HotspotSince = at(-10)
			in.Clients = 0
		}, WatchStop, false},
		{"unknown client count never retries", func(in *WatchInput) {
			in.HotspotUp = true
			in.NoLinkSince = at(-30)
			in.HotspotSince = at(-60)
			in.Clients = -1
		}, WatchNone, false},
		{"no saved networks never retries", func(in *WatchInput) {
			in.HotspotUp = true
			in.Saved = 0
			in.NoLinkSince = at(-30)
			in.HotspotSince = at(-60)
			in.Clients = 0
		}, WatchNone, false},
	}
	for _, c := range cases {
		in := base
		c.mod(&in)
		res := WatchDecide(in)
		if res.Action != c.action {
			t.Errorf("%s: action %v, want %v (%s)", c.name, res.Action, c.action, res.Reason)
		}
		if c.noLink && !res.NoLinkSince.IsZero() {
			t.Errorf("%s: no-link clock not cleared", c.name)
		}
		if !c.noLink && res.NoLinkSince.IsZero() {
			t.Errorf("%s: no-link clock lost", c.name)
		}
	}
	// the clock starts now when it was empty
	if res := WatchDecide(base); !res.NoLinkSince.Equal(t0) {
		t.Errorf("clock %v", res.NoLinkSince)
	}
	// a retry restarts the delay
	in := base
	in.HotspotUp, in.NoLinkSince, in.HotspotSince, in.Clients = true, at(-30), at(-11), 0
	if res := WatchDecide(in); res.Action != WatchStop || !res.NoLinkSince.Equal(t0) || !res.HotspotSince.IsZero() {
		t.Errorf("%+v", res)
	}
}

func watchRules(devices string) map[string]resp {
	r := wifiRules()
	r["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status"] = resp{out: devices}
	r["nmcli connection"] = resp{}
	r["nmcli --wait 30 connection up id nodespot-hotspot"] = resp{}
	r["nmcli device connect"] = resp{}
	r["firewall-cmd"] = resp{err: errors.New("not found")}
	r["ip neigh show dev wlan0"] = resp{out: ""}
	return r
}

func TestWatchdogStartsAfterDelay(t *testing.T) {
	o, f := newWifiOps(t, watchRules("wlan0:wifi:disconnected:\n"))
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return now }
	// uptime 100s: the clock starts at boot, so 80s remain of the 180s
	_ = os.WriteFile(o.Wifi.Uptime, []byte("100.5 200.1\n"), 0o644)
	var log strings.Builder
	if err := o.RunWifiWatchdog(context.Background(), &log); err != nil {
		t.Fatal(err)
	}
	if f.called("nmcli connection add") != 0 {
		t.Fatal("hotspot started too early")
	}
	now = now.Add(81 * time.Second)
	if err := o.RunWifiWatchdog(context.Background(), &log); err != nil {
		t.Fatal(err)
	}
	if f.called("nmcli connection add type wifi con-name nodespot-hotspot") != 1 || f.called("nmcli --wait 30 connection up id nodespot-hotspot ifname wlan0") != 1 {
		t.Errorf("hotspot not started: %v\nlog: %s", f.calls, log.String())
	}
}

func TestWatchdogStopsWhenNetworkConnects(t *testing.T) {
	o, f := newWifiOps(t, watchRules("wlan0:wifi:connected:ExampleNet\n"))
	o.Now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	// nothing to stop: the hotspot is not the active connection
	if err := o.RunWifiWatchdog(context.Background(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if f.called("nmcli connection down") != 0 {
		t.Error("stopped something that was not running")
	}
	// the hotspot is up and Ethernet appears
	o, f = newWifiOps(t, watchRules("eth0:ethernet:connected:x\nwlan0:wifi:connected:nodespot-hotspot\n"))
	o.Now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	if err := o.RunWifiWatchdog(context.Background(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if f.called("nmcli connection down id nodespot-hotspot") != 1 {
		t.Errorf("hotspot not stopped: %v", f.calls)
	}
}

func TestWatchdogNoNetworkManagerIsQuiet(t *testing.T) {
	o, f := newWifiOps(t, map[string]resp{"nmcli": {err: errors.New("not found")}})
	if err := o.RunWifiWatchdog(context.Background(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Errorf("%v", f.calls)
	}
}

func TestWatchdogDisabledStopsNothingNew(t *testing.T) {
	o, f := newWifiOps(t, watchRules("wlan0:wifi:disconnected:\n"))
	o.Now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	_ = os.MkdirAll(o.Wifi.StateDir, 0o700)
	b, _ := json.Marshal(hotspotCfg{Enabled: false, DelaySeconds: 180})
	_ = os.WriteFile(o.hotspotCfgPath(), b, 0o600)
	_ = os.WriteFile(o.Wifi.Uptime, []byte("99999 1\n"), 0o644)
	if err := o.RunWifiWatchdog(context.Background(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if f.called("nmcli connection add") != 0 {
		t.Error("hotspot started while turned off")
	}
}

func TestWifiLockExclusion(t *testing.T) {
	o, _ := newWifiOps(t, wifiRules())
	unlock, err := o.wifiLock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.wifiLock(context.Background(), false); err == nil {
		t.Error("second lock granted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := o.wifiLock(ctx, true); err == nil {
		t.Error("waiting lock granted while held")
	}
	unlock()
	u2, err := o.wifiLock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	u2()
}

func TestWifiOpsViaHandler(t *testing.T) {
	o, _ := newWifiOps(t, wifiRules())
	v, err := o.HandleRequest(context.Background(), Request{Op: OpWifiStatus, Sys: &SysRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := v.(WifiStatus); !ok || !st.Available {
		t.Errorf("%T %+v", v, v)
	}
	if _, err := o.HandleRequest(context.Background(), Request{Op: OpWifiHotspot, Sys: &SysRequest{}}); err == nil {
		t.Error("hotspot-set without enabled accepted")
	}
	if !ValidLogUnit("nodespot-wifi-watchdog") {
		t.Error("watchdog log unit missing")
	}
}
