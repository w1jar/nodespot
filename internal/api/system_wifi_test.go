package api_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
)

const (
	wifiIDA = "00000000-0000-4000-8000-0000000000a1"
	wifiIDB = "00000000-0000-4000-8000-0000000000a2"
)

// fakeWifiSys is the System helper with WiFi support.
type fakeWifiSys struct {
	*fakeSys
	status  helper.WifiStatus
	nets    []helper.WifiNetwork
	connErr error
	passwd  []string // every password the helper received
}

func newFakeWifiSys() *fakeWifiSys {
	return &fakeWifiSys{
		fakeSys: newFakeSys(),
		status: helper.WifiStatus{Available: true, HasWifi: true, Radio: "enabled",
			Devices: []helper.WifiDevice{{Name: "wlan0", Type: "wifi", State: "connected", Connection: "ExampleNet", Addrs: []string{"127.0.0.1"}}},
			Active:  "ExampleNet", Hotspot: helper.WifiHotspot{Enabled: true, DelaySeconds: 180, SSID: "nodespot-node2001", Address: "10.42.0.1"}},
		nets: []helper.WifiNetwork{
			{ID: wifiIDA, Name: "ExampleNet", SSID: "ExampleNet", Priority: 100, Autoconnect: true, Active: true, Secured: true},
			{ID: wifiIDB, Name: "ExampleGuest", SSID: "ExampleGuest", Priority: 90, Autoconnect: true, Secured: true},
		}}
}

func (f *fakeWifiSys) WifiStatus(context.Context) (helper.WifiStatus, error) { return f.status, nil }
func (f *fakeWifiSys) WifiScan(context.Context) (helper.WifiScanResult, error) {
	f.rec("scan")
	return helper.WifiScanResult{Networks: []helper.WifiAP{{SSID: "ExampleNet", Signal: 80, Secured: true, Supported: true}}}, nil
}
func (f *fakeWifiSys) WifiList(context.Context) (helper.WifiList, error) {
	return helper.WifiList{Networks: f.nets}, nil
}
func (f *fakeWifiSys) WifiConnect(_ context.Context, ssid, pw string, hidden bool) (helper.WifiConnectResult, error) {
	f.passwd = append(f.passwd, pw)
	f.rec(fmt.Sprintf("connect=%s,%v", ssid, hidden))
	if f.connErr != nil {
		return helper.WifiConnectResult{}, f.connErr
	}
	return helper.WifiConnectResult{ID: "00000000-0000-4000-8000-0000000000a9", SSID: ssid}, nil
}
func (f *fakeWifiSys) WifiForget(_ context.Context, id string) (helper.OKResult, error) {
	f.rec("forget=" + id)
	return helper.OKResult{OK: true}, nil
}
func (f *fakeWifiSys) WifiPriority(_ context.Context, order []string) (helper.WifiList, error) {
	f.rec("order=" + strings.Join(order, ","))
	return helper.WifiList{Networks: f.nets}, nil
}
func (f *fakeWifiSys) WifiHotspotSet(_ context.Context, on bool, delay int) (helper.WifiStatus, error) {
	f.rec(fmt.Sprintf("hotspot=%v,%d", on, delay))
	f.status.Hotspot.Enabled = on
	return f.status, nil
}

func TestWifiRoles(t *testing.T) {
	e := newEnv(t, opts{sysHost: newFakeWifiSys()})
	for _, ep := range [][2]string{
		{"GET", "/api/system/wifi"}, {"POST", "/api/system/wifi/scan"}, {"POST", "/api/system/wifi/networks"},
		{"DELETE", "/api/system/wifi/networks/" + wifiIDA}, {"PUT", "/api/system/wifi/order"}, {"PUT", "/api/system/wifi/hotspot"},
	} {
		for _, role := range []string{"op", "viewer"} {
			var body any
			if ep[0] == "POST" || ep[0] == "PUT" {
				body = map[string]any{}
			}
			expectErr(t, e, role, ep[0], ep[1], body, 403, "forbidden")
		}
	}
}

func TestWifiUnavailable(t *testing.T) {
	// no helper
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "GET", "/api/system/wifi", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "POST", "/api/system/wifi/networks", map[string]any{"ssid": "ExampleNet"}, 503, "system_unavailable")
	// a helper without WiFi support
	e = newEnv(t, opts{sysHost: newFakeSys()})
	expectErr(t, e, "admin", "GET", "/api/system/wifi", nil, 503, "system_unavailable")
	// no WiFi device: reads show "not available", changes are refused
	f := newFakeWifiSys()
	f.status = helper.WifiStatus{Available: true, HasWifi: false, Reason: "no WiFi device found", Devices: []helper.WifiDevice{}}
	e = newEnv(t, opts{sysHost: f})
	var v struct {
		Available bool   `json:"available"`
		HasWifi   bool   `json:"hasWifi"`
		Reason    string `json:"reason"`
	}
	if c := e.do("admin", "GET", "/api/system/wifi", nil, &v); c != 200 || v.HasWifi || v.Reason == "" {
		t.Fatalf("%d %+v", c, v)
	}
	expectErr(t, e, "admin", "POST", "/api/system/wifi/scan", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "POST", "/api/system/wifi/networks", map[string]any{"ssid": "ExampleNet", "password": "example-pass"}, 503, "system_unavailable")
	expectErr(t, e, "admin", "PUT", "/api/system/wifi/hotspot", map[string]any{"enabled": false}, 503, "system_unavailable")
	if f.got() != "" {
		t.Errorf("helper changed something: %s", f.got())
	}
}

func TestWifiGetAndViaWifi(t *testing.T) {
	f := newFakeWifiSys()
	e := newEnv(t, opts{sysHost: f})
	var v struct {
		Available  bool                 `json:"available"`
		Networks   []helper.WifiNetwork `json:"networks"`
		ViaWifi    bool                 `json:"viaWifi"`
		ViaHotspot bool                 `json:"viaHotspot"`
		Hotspot    helper.WifiHotspot   `json:"hotspot"`
	}
	if c := e.do("admin", "GET", "/api/system/wifi", nil, &v); c != 200 || !v.Available || len(v.Networks) != 2 || v.Hotspot.SSID != "nodespot-node2001" {
		t.Fatalf("%d %+v", c, v)
	}
	// the test server is reached on 127.0.0.1, which the fake lists as the WiFi address
	if !v.ViaWifi || v.ViaHotspot {
		t.Errorf("viaWifi %v viaHotspot %v", v.ViaWifi, v.ViaHotspot)
	}
	f.status.Devices[0].Addrs = []string{"192.0.2.20"}
	e.do("admin", "GET", "/api/system/wifi", nil, &v)
	if v.ViaWifi {
		t.Error("viaWifi set for another address")
	}
}

func TestWifiChanges(t *testing.T) {
	f := newFakeWifiSys()
	e := newEnv(t, opts{sysHost: f})
	var scan struct {
		Networks []helper.WifiAP `json:"networks"`
	}
	if c := e.do("admin", "POST", "/api/system/wifi/scan", nil, &scan); c != 200 || len(scan.Networks) != 1 {
		t.Fatalf("%d %+v", c, scan)
	}
	if c := e.do("admin", "POST", "/api/system/wifi/networks", map[string]any{"ssid": "ExampleNet2", "password": "example-pass", "hidden": true}, nil); c != 200 {
		t.Fatalf("add: %d", c)
	}
	if c := e.do("admin", "POST", "/api/system/wifi/networks", map[string]any{"ssid": "ExampleOpen"}, nil); c != 200 {
		t.Fatalf("add open: %d", c)
	}
	if c := e.do("admin", "PUT", "/api/system/wifi/order", map[string]any{"order": []string{wifiIDB, wifiIDA}}, nil); c != 200 {
		t.Fatalf("order: %d", c)
	}
	if c := e.do("admin", "DELETE", "/api/system/wifi/networks/"+wifiIDB, nil, nil); c != 200 {
		t.Fatalf("forget: %d", c)
	}
	if c := e.do("admin", "PUT", "/api/system/wifi/hotspot", map[string]any{"enabled": false, "delaySeconds": 300}, nil); c != 200 {
		t.Fatalf("hotspot: %d", c)
	}
	want := "scan;connect=ExampleNet2,true;connect=ExampleOpen,false;order=" + wifiIDB + "," + wifiIDA + ";forget=" + wifiIDB + ";hotspot=false,300"
	if f.got() != want {
		t.Errorf("calls\n got %s\nwant %s", f.got(), want)
	}
	if len(f.passwd) != 2 || f.passwd[0] != "example-pass" || f.passwd[1] != "" {
		t.Errorf("passwords %q", f.passwd)
	}
	aud, _ := e.st.ListAudit(50)
	var all []string
	for _, a := range aud {
		all = append(all, a.Action+" "+a.Detail)
	}
	text := strings.Join(all, "\n")
	for _, w := range []string{"system.wifi.add", "system.wifi.order", "system.wifi.forget", "system.wifi.hotspot", `"ssid":"ExampleNet2"`, `"secured":true`, `"ssid":"ExampleGuest"`} {
		if !strings.Contains(text, w) {
			t.Errorf("audit lacks %q:\n%s", w, text)
		}
	}
	if strings.Contains(text, "example-pass") || strings.Contains(strings.ToLower(text), "password") {
		t.Errorf("audit contains the password:\n%s", text)
	}
}

func TestWifiValidation(t *testing.T) {
	f := newFakeWifiSys()
	e := newEnv(t, opts{sysHost: f})
	bad := []map[string]any{
		{"ssid": ""},
		{"ssid": strings.Repeat("s", 33)},
		{"ssid": "ExampleNet", "password": "short"},
		{"ssid": "ExampleNet", "password": strings.Repeat("g", 64)},
		{"ssid": "bad\u0000name"},
	}
	for _, b := range bad {
		code, msg := e.errCode("admin", "POST", "/api/system/wifi/networks", b)
		if code != 400 || msg != "bad_request" {
			t.Errorf("%v: %d %s", b, code, msg)
		}
	}
	// the answer to a bad password never repeats it
	_, body, _ := e.raw("POST", "/api/system/wifi/networks", map[string]any{"ssid": "ExampleNet", "password": "tiny-pw"}, e.ck["admin"], nil)
	if strings.Contains(string(body), "tiny-pw") {
		t.Errorf("response repeats the password: %s", body)
	}
	expectErr(t, e, "admin", "DELETE", "/api/system/wifi/networks/not-a-uuid", nil, 400, "bad_request")
	for _, b := range []any{
		map[string]any{"order": []string{}},
		map[string]any{"order": []string{wifiIDA, wifiIDA}},
		map[string]any{"order": []string{"x"}},
	} {
		expectErr(t, e, "admin", "PUT", "/api/system/wifi/order", b, 400, "bad_request")
	}
	expectErr(t, e, "admin", "PUT", "/api/system/wifi/hotspot", map[string]any{}, 400, "bad_request")
	expectErr(t, e, "admin", "PUT", "/api/system/wifi/hotspot", map[string]any{"enabled": true, "delaySeconds": 5}, 400, "bad_request")
	if f.got() != "" {
		t.Errorf("helper called for invalid input: %s", f.got())
	}
}

func TestWifiHelperFailureHidesNothingSecret(t *testing.T) {
	f := newFakeWifiSys()
	f.connErr = errors.New("could not connect: the password may be wrong")
	e := newEnv(t, opts{sysHost: f})
	code, c := e.errCode("admin", "POST", "/api/system/wifi/networks", map[string]any{"ssid": "ExampleNet", "password": "example-pass"})
	if code != 502 || c != "helper_error" {
		t.Errorf("%d %s", code, c)
	}
	aud, _ := e.st.ListAudit(50)
	for _, a := range aud {
		if strings.HasPrefix(a.Action, "system.wifi") {
			t.Errorf("failed change audited: %+v", a)
		}
	}
}

func TestSetupReportsNoHotspotOnLoopback(t *testing.T) {
	e := newEnv(t, opts{noUsers: true})
	var v struct {
		Needed  bool `json:"needed"`
		Hotspot bool `json:"hotspot"`
	}
	if c := e.do("", "GET", "/api/setup", nil, &v); c != 200 || !v.Needed || v.Hotspot {
		t.Fatalf("%d %+v", c, v)
	}
}
