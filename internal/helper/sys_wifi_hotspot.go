package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/config"
)

// Setup hotspot and its watchdog (plan 9.3 and 9.4).
//
// The hotspot is an open access point, SSID "nodespot-<hostname>", on 10.42.0.1.
// Protection is nodespot's own login (or the first-run setup code). The watchdog
// is "nodespot-helper wifi-watchdog", run by a systemd timer every 30 seconds, so
// it works while the web server is down; WatchDecide is its logic as a pure
// function.
const (
	hotspotConnName = "nodespot-hotspot"
	hotspotZone     = "nodespot-hotspot"
	// HotspotAddr is the gateway address of the hotspot. The web server
	// compares it with the address a request arrived on to tell hotspot
	// clients (docs/API.md).
	HotspotAddr = "10.42.0.1"

	// DefaultHotspotDelay is the time without a connection before the
	// hotspot starts; the admin can change it (hotspotDelayMin..Max).
	DefaultHotspotDelay = 180
	hotspotDelayMin     = 30
	hotspotDelayMax     = 3600
	// hotspotRetry: with no client on the hotspot and saved networks, it is
	// taken down this often to give the saved networks another try.
	hotspotRetry = 10 * time.Minute
)

// WifiHotspot is the hotspot part of wifi-status.
type WifiHotspot struct {
	Enabled      bool   `json:"enabled"` // the fallback is on (persisted setting)
	DelaySeconds int    `json:"delaySeconds"`
	Active       bool   `json:"active"` // the access point is up now
	SSID         string `json:"ssid"`
	Address      string `json:"address"`
	Firewall     string `json:"firewall"` // "firewalld" or "none" (see docs/DESIGN.md)
}

// hotspotCfg is /var/lib/nodespot-wifi/hotspot.json (root only).
type hotspotCfg struct {
	Enabled      bool `json:"enabled"`
	DelaySeconds int  `json:"delaySeconds"`
}

func (o *Ops) hotspotCfgPath() string { return filepath.Join(o.wifiStateDir(), "hotspot.json") }

// loadHotspotCfg returns the stored setting; a missing or damaged file is the
// default: on, 3 minutes.
func (o *Ops) loadHotspotCfg() hotspotCfg {
	c := hotspotCfg{Enabled: true, DelaySeconds: DefaultHotspotDelay}
	b, err := os.ReadFile(o.hotspotCfgPath())
	if err != nil {
		return c
	}
	var f hotspotCfg
	if json.Unmarshal(b, &f) != nil {
		return c
	}
	c.Enabled = f.Enabled
	if f.DelaySeconds >= hotspotDelayMin && f.DelaySeconds <= hotspotDelayMax {
		c.DelaySeconds = f.DelaySeconds
	}
	return c
}

func (o *Ops) saveHotspotCfg(c hotspotCfg) error {
	if err := os.MkdirAll(o.wifiStateDir(), 0o700); err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(o.wifiStateDir())
	if err != nil {
		return err
	}
	b, _ := json.Marshal(c)
	return atomicWrite(filepath.Join(dir, "hotspot.json"), dir, append(b, '\n'), 0, 0, 0o600)
}

// hotspotSSID is "nodespot-<hostname>", cut to the 32 bytes of an SSID.
func hotspotSSID(hostname string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(hostname) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	h := b.String()
	if h == "" {
		h = "node"
	}
	s := "nodespot-" + h
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.TrimRight(s, "-")
}

func (o *Ops) hostname() string {
	if o.Wifi.Hostname != nil {
		return o.Wifi.Hostname()
	}
	h, _ := os.Hostname()
	return h
}

// hotspotInfo is the persisted setting plus the derived SSID; the caller
// fills Active and Firewall.
func (o *Ops) hotspotInfo() WifiHotspot {
	c := o.loadHotspotCfg()
	return WifiHotspot{Enabled: c.Enabled, DelaySeconds: c.DelaySeconds, SSID: hotspotSSID(o.hostname()), Address: HotspotAddr, Firewall: "none"}
}

// --------------------------------------------------------------- firewalld

// firewalldRunning reports whether firewall-cmd exists and firewalld runs.
func (o *Ops) firewalldRunning(ctx context.Context) bool {
	_, code, err := o.Run.Run(ctx, "firewall-cmd", "--state")
	return err == nil && code == 0
}

func (o *Ops) nodespotPort() int {
	if cfg, err := config.Load(o.nodespotConf()); err == nil {
		if p, err := config.ListenPort(cfg.Listen); err == nil {
			return p
		}
	}
	return 8080
}

// firewallZoneCmds is the permanent firewalld setup of the hotspot zone: a
// fresh zone whose default is DROP, that allows only nodespot's TCP port, DHCP
// (server) and DNS, and takes no part in forwarding.
func firewallZoneCmds(port int) [][]string {
	z := "--zone=" + hotspotZone
	return [][]string{
		{"firewall-cmd", "--permanent", "--new-zone=" + hotspotZone},
		{"firewall-cmd", "--permanent", z, "--set-target=DROP"},
		{"firewall-cmd", "--permanent", z, "--add-port=" + strconv.Itoa(port) + "/tcp"},
		{"firewall-cmd", "--permanent", z, "--add-service=dhcp"},
		{"firewall-cmd", "--permanent", z, "--add-service=dns"},
	}
}

// syncFirewall (re)creates the hotspot zone. It returns false when firewalld
// is not running (the hotspot then relies on having no uplink, see
// docs/DESIGN.md) and an error when firewalld runs but the zone could not be
// built: the hotspot is then not started.
func (o *Ops) syncFirewall(ctx context.Context) (bool, error) {
	if !o.firewalldRunning(ctx) {
		return false, nil
	}
	// Start from nothing so that a changed nodespot port leaves no stale rule.
	if out, code, err := o.Run.Run(ctx, "firewall-cmd", "--permanent", "--get-zones"); err == nil && code == 0 {
		for _, z := range strings.Fields(out) {
			if z == hotspotZone {
				_, _, _ = o.Run.Run(ctx, "firewall-cmd", "--permanent", "--delete-zone="+hotspotZone)
				break
			}
		}
	}
	for _, argv := range firewallZoneCmds(o.nodespotPort()) {
		if out, code, err := o.Run.Run(ctx, argv...); err != nil || code != 0 {
			return false, fmt.Errorf("firewalld: %s failed: %s", argv[2], wifiText(LastLines(out, 2)))
		}
	}
	// Newer firewalld only (to verify): no forwarding for this zone at all.
	_, _, _ = o.Run.Run(ctx, "firewall-cmd", "--permanent", "--zone="+hotspotZone, "--remove-forward")
	if out, code, err := o.Run.Run(ctx, "firewall-cmd", "--reload"); err != nil || code != 0 {
		return false, fmt.Errorf("firewalld: reload failed: %s", wifiText(LastLines(out, 2)))
	}
	return true, nil
}

func (o *Ops) dropFirewall(ctx context.Context) {
	if !o.firewalldRunning(ctx) {
		return
	}
	if out, code, err := o.Run.Run(ctx, "firewall-cmd", "--permanent", "--get-zones"); err == nil && code == 0 && strings.Contains(" "+strings.Join(strings.Fields(out), " ")+" ", " "+hotspotZone+" ") {
		_, _, _ = o.Run.Run(ctx, "firewall-cmd", "--permanent", "--delete-zone="+hotspotZone)
		_, _, _ = o.Run.Run(ctx, "firewall-cmd", "--reload")
	}
}

// ----------------------------------------------------------------- profile

// hotspotAddArgs is the nmcli command that creates the hotspot profile: an
// open access point with a fixed private address, never the default route.
// ipv4.method "shared" is what gives clients DHCP and DNS (NetworkManager runs
// its own dnsmasq); it also asks for NAT, which is why the hotspot is only
// ever up with no uplink, why the zone drops forwarding, and why the watchdog
// stops it as soon as another network connects (docs/DESIGN.md).
func hotspotAddArgs(dev, ssid string, zone bool) []string {
	a := []string{"nmcli", "connection", "add", "type", "wifi", "con-name", hotspotConnName, "ifname", dev, "ssid", ssid,
		"autoconnect", "no",
		"802-11-wireless.mode", "ap", "802-11-wireless.band", "bg",
		"ipv4.method", "shared", "ipv4.addresses", HotspotAddr + "/24", "ipv4.never-default", "yes",
		"ipv6.method", "disabled"}
	if zone {
		a = append(a, "connection.zone", hotspotZone)
	}
	return a
}

// syncHotspotProfile replaces the profile (the SSID follows the hostname) and
// builds the firewall zone first.
func (o *Ops) syncHotspotProfile(ctx context.Context, dev string) error {
	zone, err := o.syncFirewall(ctx)
	if err != nil {
		return err
	}
	_, _, _ = o.Run.Run(ctx, "nmcli", "connection", "delete", "id", hotspotConnName)
	if out, code, err := o.Run.Run(ctx, hotspotAddArgs(dev, hotspotSSID(o.hostname()), zone)...); err != nil || code != 0 {
		return errors.New("could not create the hotspot profile: " + wifiText(LastLines(out, 2)))
	}
	return nil
}

// WifiHotspotSet turns the setup hotspot fallback on or off and stores the
// start delay (0: keep). Off stops a running hotspot and removes the profile
// and the firewall zone; on prepares them, so a failure shows here and not at
// the first outage.
func (o *Ops) WifiHotspotSet(ctx context.Context, enabled bool, delay int) (WifiStatus, error) {
	cfg := o.loadHotspotCfg()
	if delay != 0 {
		if delay < hotspotDelayMin || delay > hotspotDelayMax {
			return WifiStatus{}, fmt.Errorf("invalid delay: %d to %d seconds", hotspotDelayMin, hotspotDelayMax)
		}
		cfg.DelaySeconds = delay
	}
	unlock, err := o.wifiLock(ctx, true)
	if err != nil {
		return WifiStatus{}, err
	}
	defer unlock()
	dev, err := o.wifiGate(ctx)
	if err != nil {
		return WifiStatus{}, err
	}
	cfg.Enabled = enabled
	if enabled {
		if err := o.syncHotspotProfile(ctx, dev); err != nil {
			return WifiStatus{}, err
		}
	} else {
		_, _, _ = o.Run.Run(ctx, "nmcli", "connection", "down", "id", hotspotConnName)
		_, _, _ = o.Run.Run(ctx, "nmcli", "connection", "delete", "id", hotspotConnName)
		o.dropFirewall(ctx)
	}
	if err := o.saveHotspotCfg(cfg); err != nil {
		return WifiStatus{}, errors.New("could not save the setting")
	}
	st := o.WifiStatus(ctx)
	if st.Hotspot.Firewall = "none"; o.firewalldRunning(ctx) {
		st.Hotspot.Firewall = "firewalld"
	}
	return st, nil
}

// ---------------------------------------------------------------- watchdog

// WatchInput is the state the watchdog decides on.
type WatchInput struct {
	Enabled      bool // the fallback is on
	HasWifi      bool // a WiFi device exists and the radio is not off
	Saved        int  // saved WiFi networks
	Ethernet     bool // an Ethernet device is connected
	WifiUp       bool // the WiFi device is connected to a saved network
	Connecting   bool // the WiFi device is busy connecting to a saved network
	HotspotUp    bool
	Clients      int // stations on the hotspot, -1 unknown
	Now          time.Time
	NoLinkSince  time.Time // zero: there is a link (or none was recorded)
	HotspotSince time.Time
	Delay        time.Duration
	Retry        time.Duration
}

// WatchAction is what the watchdog does.
type WatchAction int

// Watchdog actions.
const (
	WatchNone WatchAction = iota
	WatchStart
	WatchStop
)

func (a WatchAction) String() string { return [...]string{"none", "start", "stop"}[a] }

// WatchResult is the decision and the state to keep for the next run.
type WatchResult struct {
	Action       WatchAction
	NoLinkSince  time.Time
	HotspotSince time.Time
	Reason       string
}

// WatchDecide is the whole hotspot policy, as a pure function:
//   - fallback off or no WiFi: stop a running hotspot, otherwise nothing;
//   - Ethernet or a saved network connected: stop the hotspot, clear the clock;
//   - no link: start the clock; start the hotspot once Delay has passed (not
//     while a saved network is still connecting);
//   - hotspot up with saved networks and no client for Retry: stop it to try
//     the saved networks again (the radio cannot do both), with a new Delay.
func WatchDecide(in WatchInput) WatchResult {
	r := WatchResult{NoLinkSince: in.NoLinkSince, HotspotSince: in.HotspotSince}
	if !in.Enabled || !in.HasWifi {
		r.NoLinkSince, r.HotspotSince = time.Time{}, time.Time{}
		if in.HotspotUp {
			r.Action, r.Reason = WatchStop, "fallback is off or there is no WiFi device"
		}
		return r
	}
	if in.Ethernet || in.WifiUp {
		r.NoLinkSince, r.HotspotSince = time.Time{}, time.Time{}
		if in.HotspotUp {
			r.Action, r.Reason = WatchStop, "a network is connected"
		}
		return r
	}
	if in.Connecting && !in.HotspotUp {
		// A saved network is on its way; the clock keeps running but nothing starts.
		if r.NoLinkSince.IsZero() {
			r.NoLinkSince = in.Now
		}
		return r
	}
	if r.NoLinkSince.IsZero() {
		r.NoLinkSince = in.Now
	}
	if in.HotspotUp {
		if r.HotspotSince.IsZero() {
			r.HotspotSince = in.Now
		}
		if in.Saved > 0 && in.Clients == 0 && in.Now.Sub(r.HotspotSince) >= in.Retry {
			r.Action, r.Reason = WatchStop, "no client on the hotspot: trying the saved networks again"
			r.NoLinkSince, r.HotspotSince = in.Now, time.Time{}
		}
		return r
	}
	if in.Now.Sub(r.NoLinkSince) >= in.Delay {
		r.Action, r.Reason = WatchStart, "no network connection for "+in.Now.Sub(r.NoLinkSince).Round(time.Second).String()
		r.HotspotSince = in.Now
	}
	return r
}

// watchState is /run/nodespot/wifi-watchdog.json.
type watchState struct {
	NoLinkSince  int64 `json:"noLinkSince,omitempty"`  // unix seconds
	HotspotSince int64 `json:"hotspotSince,omitempty"` // unix seconds
}

func unixTime(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}

func timeUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (o *Ops) watchStatePath() string { return filepath.Join(o.wifiRunDir(), "wifi-watchdog.json") }

func (o *Ops) loadWatchState() (watchState, bool) {
	var s watchState
	b, err := os.ReadFile(o.watchStatePath())
	if err != nil || json.Unmarshal(b, &s) != nil {
		return watchState{}, false
	}
	return s, true
}

func (o *Ops) saveWatchState(s watchState) {
	if err := os.MkdirAll(o.wifiRunDir(), 0o755); err != nil {
		return
	}
	dir, err := filepath.EvalSymlinks(o.wifiRunDir())
	if err != nil {
		return
	}
	b, _ := json.Marshal(s)
	_ = atomicWrite(filepath.Join(dir, "wifi-watchdog.json"), dir, append(b, '\n'), 0, 0, 0o644)
}

// bootTime is when the machine started, from /proc/uptime: with no recorded
// state the "no link since" clock starts at boot.
func (o *Ops) bootTime(now time.Time) time.Time {
	b, err := os.ReadFile(orDefault(o.Wifi.Uptime, "/proc/uptime"))
	if err != nil {
		return now
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return now
	}
	up, err := strconv.ParseFloat(f[0], 64)
	if err != nil || up < 0 {
		return now
	}
	return now.Add(-time.Duration(up * float64(time.Second)))
}

// RunWifiWatchdog is "nodespot-helper wifi-watchdog": one pass of the hotspot
// policy. It does nothing without NetworkManager or a WiFi device, and skips
// the pass while a WiFi change by the web UI is in progress. Log lines go to
// w (the journal).
func (o *Ops) RunWifiWatchdog(ctx context.Context, w io.Writer) error {
	logf := func(f string, a ...any) { fmt.Fprintf(w, f+"\n", a...) }
	unlock, err := o.wifiLock(ctx, false)
	if err != nil {
		return nil // a WiFi change is running; the next pass decides
	}
	defer unlock()
	if ok, _ := o.nmRunning(ctx); !ok {
		return nil
	}
	cfg := o.loadHotspotCfg()
	in := WatchInput{Enabled: cfg.Enabled, Now: o.now(), Clients: -1,
		Delay: time.Duration(cfg.DelaySeconds) * time.Second, Retry: hotspotRetry}
	var wifiDev string
	out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
	if err != nil || code != 0 {
		return nil
	}
	for _, d := range parseNMDevices(out) {
		switch d.Type {
		case "ethernet":
			if d.State == "connected" {
				in.Ethernet = true
			}
		case "wifi":
			if !ifnameRE.MatchString(d.Name) {
				continue
			}
			in.HasWifi = true
			if wifiDev == "" {
				wifiDev = d.Name
			}
			up := strings.HasPrefix(d.State, "connected")
			switch {
			case d.Conn == hotspotConnName && up:
				in.HotspotUp = true
				if n, c, err := o.Run.Run(ctx, "ip", "neigh", "show", "dev", d.Name); err == nil && c == 0 {
					in.Clients = countNeighbors(n)
				}
			case up:
				in.WifiUp = true
			case strings.HasPrefix(d.State, "connecting"):
				in.Connecting = true
			}
		}
	}
	if in.HasWifi {
		if r, c, err := o.Run.Run(ctx, "nmcli", "radio", "wifi"); err == nil && c == 0 && strings.TrimSpace(r) == "disabled" {
			in.HasWifi = false
		}
	}
	if conns, err := o.wifiConns(ctx); err == nil {
		in.Saved = len(conns)
	}
	st, known := o.loadWatchState()
	in.NoLinkSince, in.HotspotSince = unixTime(st.NoLinkSince), unixTime(st.HotspotSince)
	if !known {
		in.NoLinkSince = o.bootTime(in.Now)
	}
	res := WatchDecide(in)
	switch res.Action {
	case WatchStart:
		logf("starting the setup hotspot on %s: %s", wifiDev, res.Reason)
		if err := o.syncHotspotProfile(ctx, wifiDev); err != nil {
			logf("hotspot not started: %v", err)
			res.HotspotSince = time.Time{}
			break
		}
		if out, code, err := o.Run.Run(ctx, "nmcli", "--wait", "30", "connection", "up", "id", hotspotConnName, "ifname", wifiDev); err != nil || code != 0 {
			logf("hotspot did not come up: %s", wifiText(LastLines(out, 2)))
			res.HotspotSince = time.Time{}
		}
	case WatchStop:
		logf("stopping the setup hotspot: %s", res.Reason)
		_, _, _ = o.Run.Run(ctx, "nmcli", "connection", "down", "id", hotspotConnName)
		if in.Saved > 0 && !in.Ethernet && !in.WifiUp && wifiDev != "" {
			// Hand the radio back to the saved networks (to verify: autoconnect
			// may do this by itself).
			_, _, _ = o.Run.Run(ctx, "nmcli", "device", "connect", wifiDev)
		}
		if !cfg.Enabled {
			_, _, _ = o.Run.Run(ctx, "nmcli", "connection", "delete", "id", hotspotConnName)
		}
	}
	o.saveWatchState(watchState{NoLinkSince: timeUnix(res.NoLinkSince), HotspotSince: timeUnix(res.HotspotSince)})
	return nil
}

// ------------------------------------------------------------------- lock

var wifiMu sync.Mutex

// wifiLock serialises the WiFi ops of the helper and the watchdog: an
// in-process mutex plus a flock on RunDir/wifi.lock (the watchdog is another
// process). With wait false it returns an error at once when the lock is
// taken; with wait true it waits until ctx ends.
func (o *Ops) wifiLock(ctx context.Context, wait bool) (func(), error) {
	busy := errors.New("another WiFi change is in progress")
	for {
		if wifiMu.TryLock() {
			break
		}
		if !wait {
			return nil, busy
		}
		select {
		case <-ctx.Done():
			return nil, busy
		case <-time.After(100 * time.Millisecond):
		}
	}
	var f *os.File
	if err := os.MkdirAll(o.wifiRunDir(), 0o755); err == nil {
		f, _ = os.OpenFile(filepath.Join(o.wifiRunDir(), "wifi.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	}
	for f != nil {
		err := flockFile(f, true)
		if err == nil {
			break
		}
		if !wait || ctx.Err() != nil {
			f.Close()
			wifiMu.Unlock()
			return nil, busy
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	return func() {
		if f != nil {
			_ = flockFile(f, false)
			f.Close()
		}
		wifiMu.Unlock()
	}, nil
}
