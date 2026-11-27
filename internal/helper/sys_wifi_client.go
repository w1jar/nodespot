package helper

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Client methods of the WiFi ops (sys_wifi.go). The API server uses them
// through its WifiHost interface; DemoSystem implements the same set.

func (c *Client) wifi(ctx context.Context, op string, w *WifiRequest, out any) error {
	return c.sys(ctx, op, &SysRequest{Wifi: w}, out, 1<<20)
}

// WifiStatus calls wifi-status.
func (c *Client) WifiStatus(ctx context.Context) (WifiStatus, error) {
	var v WifiStatus
	return v, c.wifi(ctx, OpWifiStatus, nil, &v)
}

// WifiScan calls wifi-scan.
func (c *Client) WifiScan(ctx context.Context) (WifiScanResult, error) {
	var v WifiScanResult
	return v, c.wifi(ctx, OpWifiScan, nil, &v)
}

// WifiList calls wifi-list.
func (c *Client) WifiList(ctx context.Context) (WifiList, error) {
	var v WifiList
	return v, c.wifi(ctx, OpWifiList, nil, &v)
}

// WifiConnect calls wifi-connect. The password travels on the helper socket
// only (never in an argv, a log line or an audit row).
func (c *Client) WifiConnect(ctx context.Context, ssid, password string, hidden bool) (WifiConnectResult, error) {
	var v WifiConnectResult
	return v, c.wifi(ctx, OpWifiConnect, &WifiRequest{SSID: ssid, Password: password, Hidden: hidden}, &v)
}

// WifiForget calls wifi-forget.
func (c *Client) WifiForget(ctx context.Context, id string) (OKResult, error) {
	var v OKResult
	return v, c.wifi(ctx, OpWifiForget, &WifiRequest{ID: id}, &v)
}

// WifiPriority calls wifi-priority.
func (c *Client) WifiPriority(ctx context.Context, order []string) (WifiList, error) {
	var v WifiList
	return v, c.wifi(ctx, OpWifiPriority, &WifiRequest{Order: order}, &v)
}

// WifiHotspotSet calls wifi-hotspot-set.
func (c *Client) WifiHotspotSet(ctx context.Context, enabled bool, delaySeconds int) (WifiStatus, error) {
	var v WifiStatus
	return v, c.wifi(ctx, OpWifiHotspot, &WifiRequest{Enabled: &enabled, DelaySeconds: delaySeconds}, &v)
}

// ------------------------------------------------------------------- demo

// demoWifi is the simulated WiFi state of DemoSystem.
type demoWifi struct {
	mu      sync.Mutex
	nets    []WifiNetwork
	hotspot bool
	delay   int
	inited  bool
}

var demoWifiState demoWifi

func (w *demoWifi) init() {
	if w.inited {
		return
	}
	w.inited = true
	w.hotspot, w.delay = true, DefaultHotspotDelay
	w.nets = []WifiNetwork{
		{ID: "00000000-0000-4000-8000-000000000001", Name: "ExampleNet", SSID: "ExampleNet", Priority: 100, Autoconnect: true, Active: true, Secured: true},
		{ID: "00000000-0000-4000-8000-000000000002", Name: "ExampleGuest", SSID: "ExampleGuest", Priority: 90, Autoconnect: true, Secured: true},
	}
}

func (w *demoWifi) sorted() []WifiNetwork {
	out := append([]WifiNetwork{}, w.nets...)
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Priority > out[i].Priority {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (w *demoWifi) status() WifiStatus {
	st := WifiStatus{Available: true, HasWifi: true, Radio: "enabled", Ethernet: true,
		Devices: []WifiDevice{
			{Name: "eth0", Type: "ethernet", State: "connected", Connection: "Wired connection 1", Addrs: []string{}},
			{Name: "wlan0", Type: "wifi", State: "connected", Connection: "ExampleNet", Addrs: []string{"192.0.2.20"}},
		},
		Active:  "ExampleNet",
		Hotspot: WifiHotspot{Enabled: w.hotspot, DelaySeconds: w.delay, SSID: "nodespot-node2001", Address: HotspotAddr, Firewall: "none"}}
	return st
}

// WifiStatus returns a simulated status.
func (d *DemoSystem) WifiStatus(context.Context) (WifiStatus, error) {
	w := &demoWifiState
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	return w.status(), nil
}

// WifiScan returns a canned scan.
func (d *DemoSystem) WifiScan(context.Context) (WifiScanResult, error) {
	return WifiScanResult{Networks: []WifiAP{
		{SSID: "ExampleNet", Signal: 82, Security: "WPA2", Secured: true, Supported: true, Channel: 6, InUse: true, Saved: true},
		{SSID: "ExampleGuest", Signal: 64, Security: "WPA2", Secured: true, Supported: true, Channel: 11, Saved: true},
		{SSID: "ExampleOpen", Signal: 40, Supported: true, Channel: 1},
	}}, nil
}

// WifiList returns the simulated saved networks.
func (d *DemoSystem) WifiList(context.Context) (WifiList, error) {
	w := &demoWifiState
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	return WifiList{Networks: w.sorted()}, nil
}

// WifiConnect saves a simulated network.
func (d *DemoSystem) WifiConnect(_ context.Context, ssid, password string, hidden bool) (WifiConnectResult, error) {
	if err := ValidSSID(ssid); err != nil {
		return WifiConnectResult{}, err
	}
	if err := ValidWifiPassword(password); err != nil {
		return WifiConnectResult{}, err
	}
	w := &demoWifiState
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	top := 0
	for _, n := range w.nets {
		if n.Priority > top {
			top = n.Priority
		}
	}
	for i := range w.nets {
		if w.nets[i].SSID == ssid {
			w.nets[i].Secured, w.nets[i].Hidden = password != "", hidden
			return WifiConnectResult{ID: w.nets[i].ID, SSID: ssid, Updated: true}, nil
		}
	}
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(w.nets)+time.Now().Nanosecond()%1000000)
	w.nets = append(w.nets, WifiNetwork{ID: id, Name: ssid, SSID: ssid, Priority: top + 10, Autoconnect: true, Hidden: hidden, Secured: password != ""})
	return WifiConnectResult{ID: id, SSID: ssid}, nil
}

// WifiForget removes a simulated network.
func (d *DemoSystem) WifiForget(_ context.Context, id string) (OKResult, error) {
	w := &demoWifiState
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	for i := range w.nets {
		if w.nets[i].ID == id {
			w.nets = append(w.nets[:i], w.nets[i+1:]...)
			return OKResult{OK: true}, nil
		}
	}
	return OKResult{}, errors.New("unknown saved network")
}

// WifiPriority reorders the simulated networks.
func (d *DemoSystem) WifiPriority(_ context.Context, order []string) (WifiList, error) {
	w := &demoWifiState
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	if len(order) != len(w.nets) {
		return WifiList{}, errors.New("order must list every saved network once")
	}
	seen := map[string]bool{}
	for _, id := range order {
		found := false
		for _, n := range w.nets {
			if n.ID == id {
				found = true
			}
		}
		if !found || seen[id] {
			return WifiList{}, errors.New("order must list every saved network once")
		}
		seen[id] = true
	}
	for i, id := range order {
		for j := range w.nets {
			if w.nets[j].ID == id {
				w.nets[j].Priority = 100 - 10*i
			}
		}
	}
	return WifiList{Networks: w.sorted()}, nil
}

// WifiHotspotSet stores the simulated hotspot setting.
func (d *DemoSystem) WifiHotspotSet(_ context.Context, enabled bool, delay int) (WifiStatus, error) {
	if delay != 0 && (delay < hotspotDelayMin || delay > hotspotDelayMax) {
		return WifiStatus{}, errors.New("invalid delay")
	}
	w := &demoWifiState
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	w.hotspot = enabled
	if delay != 0 {
		w.delay = delay
	}
	return w.status(), nil
}
