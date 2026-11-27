package helper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DemoSystem simulates the System tab ops for --demo: it keeps the hostname,
// time zone and NTP setting in memory and returns canned logs, services and
// network data. Nothing on the machine is touched; poweroff does nothing.
type DemoSystem struct {
	mu   sync.Mutex
	host HostStatus
}

// NewDemoSystem returns a simulated System helper.
func NewDemoSystem() *DemoSystem {
	return &DemoSystem{host: HostStatus{Hostname: "node2001", PrettyHostname: "", OS: "Debian GNU/Linux 12 (bookworm)", HostnameAvail: true, HostnamectlAvail: true,
		Timezone: "America/Chicago", NTP: true, NTPSynchronized: true, CanNTP: true, TimeAvail: true}}
}

var demoZones = []string{"America/Chicago", "America/Denver", "America/Los_Angeles", "America/New_York", "Europe/Berlin", "Europe/London", "UTC"}

func (d *DemoSystem) snapshot() HostStatus {
	h := d.host
	h.Now = time.Now().UTC().Format(time.RFC3339)
	h.Clock = time.Now().Format("Mon 2006-01-02 15:04:05 MST")
	return h
}

// HostStatus implements the SystemHost interface of the API.
func (d *DemoSystem) HostStatus(context.Context) (HostStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot(), nil
}

// SetHostname sets the simulated hostname.
func (d *DemoSystem) SetHostname(_ context.Context, name string) (HostStatus, error) {
	name, err := ValidHostname(name)
	if err != nil {
		return HostStatus{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.host.Hostname = name
	return d.snapshot(), nil
}

// SetTimezone sets the simulated time zone.
func (d *DemoSystem) SetTimezone(_ context.Context, tz string) (HostStatus, error) {
	for _, z := range demoZones {
		if z == tz {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.host.Timezone = tz
			return d.snapshot(), nil
		}
	}
	return HostStatus{}, errors.New("invalid timezone")
}

// SetNTP sets the simulated NTP switch.
func (d *DemoSystem) SetNTP(_ context.Context, on bool) (HostStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.host.NTP, d.host.NTPSynchronized = on, on
	return d.snapshot(), nil
}

// Timezones returns a short list of zones.
func (d *DemoSystem) Timezones(context.Context) ([]string, error) {
	return append([]string{}, demoZones...), nil
}

// Poweroff does nothing in the demo.
func (d *DemoSystem) Poweroff(context.Context) error { return nil }

// Logs returns a few canned lines.
func (d *DemoSystem) Logs(_ context.Context, unit string, lines int, since string) (LogsResult, error) {
	if !ValidLogUnit(unit) {
		return LogsResult{}, errors.New("invalid unit")
	}
	if !ValidLogSince(since) {
		return LogsResult{}, errors.New("invalid since")
	}
	res := LogsResult{Unit: unit, Lines: ClampLogLines(lines), Since: since, Available: true}
	var b strings.Builder
	now := time.Now()
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "%s node2001 %s[412]: demo log line %d\n", now.Add(time.Duration(i-12)*time.Minute).Format("2006-01-02T15:04:05-0700"), unit, i+1)
	}
	res.Text = b.String()
	return res, nil
}

// Services returns a canned service list.
func (d *DemoSystem) Services(context.Context) (ServicesResult, error) {
	since := time.Now().Add(-26 * time.Hour).Format("Mon 2006-01-02 15:04:05 MST")
	mk := func(u, desc, en string) ServiceState {
		return ServiceState{Unit: u, Desc: desc, Active: "active", Sub: "running", Enabled: en, Since: since}
	}
	return ServicesResult{Available: true, Services: []ServiceState{
		mk("nodespot-node.service", "nodespot native node (one radio, one network mode)", "enabled"),
		mk("nodespot.service", "nodespot web control panel", "enabled"),
		{Unit: "nodespot-helper.service", Desc: "nodespot privileged helper", Active: "inactive", Sub: "dead", Enabled: "static"},
		mk("NetworkManager.service", "Network Manager", "enabled"),
		mk("cockpit.socket", "Cockpit Web Service Socket", "enabled"),
		mk("chrony.service", "chrony, an NTP client/server", "enabled"),
	}}, nil
}

// Network returns a canned status.
func (d *DemoSystem) Network(context.Context) (NetStatus, error) {
	return NetStatus{Available: true,
		Interfaces: []NetIface{
			{Name: "lo", State: "UNKNOWN", Kind: "loopback", MAC: "00:00:00:00:00:00", MTU: 65536, Addrs: []NetAddr{{Family: "inet", Addr: "127.0.0.1", Prefix: 8, Scope: "host"}}},
			{Name: "eth0", State: "UP", Kind: "ether", MAC: "02:00:00:00:20:01", MTU: 1500, Addrs: []NetAddr{{Family: "inet", Addr: "192.0.2.10", Prefix: 24, Scope: "global", Dynamic: true}, {Family: "inet6", Addr: "2001:db8::10", Prefix: 64, Scope: "global"}}},
		},
		Gateways:  []NetRoute{{Family: "inet", Gateway: "192.0.2.1", Dev: "eth0", Metric: 100}},
		DNS:       []string{"192.0.2.1"},
		DNSSource: "resolv.conf"}, nil
}
