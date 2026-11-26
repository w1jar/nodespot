package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Services switch of the System tab (plan 6): turn Cockpit, Apache and
// Allmon3 off or on again on a node that still has them from an earlier
// install. Only "systemctl disable --now" and "enable --now" (plus "stop"
// for a static unit): nothing is ever uninstalled.
const (
	OpSvcStatus = "svc-status" // read only
	OpSvcSet    = "svc-set"    // SysRequest.SvcScope, SvcAction, SvcFirewall

	SvcActionEnable  = "enable"
	SvcActionDisable = "disable"

	// DefaultSvcRecord lists the units nodespot itself disabled, one per line.
	// postrm.sh re-enables exactly these on "apt purge".
	DefaultSvcRecord = "/var/lib/nodespot/disabled-units"
)

func init() {
	registerSysOp(OpSvcStatus, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.SvcStatus(ctx), nil
	})
	registerSysOp(OpSvcSet, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		return o.SvcSet(ctx, r.SvcScope, r.SvcAction, r.SvcFirewall)
	})
}

// svcScopes is the fixed allowlist of the switch, in the order the units are
// turned off (the socket first, so nothing restarts the static service).
// The request carries a scope name and an action; no unit name comes from the
// client. packaging/scripts/postrm.sh holds the same unit names.
var svcScopes = map[string][]string{
	"cockpit": {"cockpit.socket", "cockpit.service"},
	"apache":  {"apache2"},
	"allmon3": {"allmon3"},
}

// svcScopeOrder is the order scopes are listed in.
var svcScopeOrder = []string{"cockpit", "apache", "allmon3"}

var svcLabels = map[string]string{"cockpit": "Cockpit", "apache": "Apache", "allmon3": "Allmon3"}

// svcFirewall are the firewalld services the offer to close (or reopen) a
// port covers, per scope. Allmon3's ports are not offered.
var svcFirewall = map[string][]string{
	"cockpit": {"cockpit"},
	"apache":  {"http", "https"},
}

// svcPortDefs are the ports shown after a change (plan 6.3). Allmon3's
// websockets start at 16700 and use one port per node; the range shown is
// 16700 to 16799.
var svcPortDefs = []struct {
	scope, label string
	from, to     int
}{
	{"apache", "80", 80, 80},
	{"apache", "443", 443, 443},
	{"cockpit", "9090", 9090, 9090},
	{"allmon3", "16080", 16080, 16080},
	{"allmon3", "16700+", 16700, 16799},
}

// Errors the API maps to status codes. They cross the helper socket as text
// (see the client methods in sys_svc_client.go).
var (
	ErrPort80Held      = errors.New("nodespot is using port 80")
	ErrSvcNotInstalled = errors.New("the service is not installed")
)

// SvcPaths are the files and timing of the services switch and the port 80
// move; every zero value means the default. Tests inject temporary paths.
type SvcPaths struct {
	Record     string        // default DefaultSvcRecord
	StateDir   string        // default DefaultWebPortState
	DropinDir  string        // default DefaultDropinDir
	GuardDelay time.Duration // default DefaultGuardDelay

	mu sync.Mutex // record file and move steps
}

func (o *Ops) svcRecord() string { return orDefault(o.Svc.Record, DefaultSvcRecord) }

// ValidSvcScope reports whether scope is one of the fixed scopes.
func ValidSvcScope(scope string) bool { _, ok := svcScopes[scope]; return ok }

// ValidSvcAction reports whether action is "enable" or "disable".
func ValidSvcAction(action string) bool {
	return action == SvcActionEnable || action == SvcActionDisable
}

// SvcScopes returns the scope names in display order.
func SvcScopes() []string { return append([]string{}, svcScopeOrder...) }

func unitID(u string) string {
	if !strings.Contains(u, ".") {
		return u + ".service"
	}
	return u
}

// SockListener is one TCP listening socket.
type SockListener struct {
	Addr string `json:"addr"`
	Port int    `json:"port"`
}

// parseListeners reads "ss -ltnH": one line per listening socket, columns
// State Recv-Q Send-Q Local Peer. The local column is "addr:port"
// ("*:80", "0.0.0.0:22", "[::]:9090", "127.0.0.1:631"); the port follows the
// last colon. Anything that does not look like that is skipped.
func parseListeners(text string) []SockListener {
	out := []SockListener{}
	seen := map[SockListener]bool{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		local := f[3]
		i := strings.LastIndexByte(local, ':')
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		l := SockListener{Addr: strings.Trim(local[:i], "[]"), Port: port}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// listeners runs "ss -ltnH"; ok is false when ss is missing or fails (the
// listening state is then "not available", never "closed").
func (o *Ops) listeners(ctx context.Context) ([]SockListener, bool) {
	out, code, err := o.Run.Run(ctx, "ss", "-ltnH")
	if err != nil || code != 0 {
		return nil, false
	}
	return parseListeners(out), true
}

// SvcPort is the listening state of one port (or range) of a service.
type SvcPort struct {
	Scope     string   `json:"scope"`
	Label     string   `json:"label"` // "80", "443", "9090", "16080", "16700+"
	Listening bool     `json:"listening"`
	Ports     []int    `json:"ports"` // the open ports (several for a range)
	Addrs     []string `json:"addrs"` // addresses they listen on
}

// svcPorts maps listeners to the fixed port list.
func svcPorts(ls []SockListener) []SvcPort {
	out := []SvcPort{}
	for _, d := range svcPortDefs {
		p := SvcPort{Scope: d.scope, Label: d.label, Ports: []int{}, Addrs: []string{}}
		seenAddr := map[string]bool{}
		for _, l := range ls {
			if l.Port < d.from || l.Port > d.to {
				continue
			}
			p.Listening = true
			found := false
			for _, x := range p.Ports {
				if x == l.Port {
					found = true
				}
			}
			if !found {
				p.Ports = append(p.Ports, l.Port)
			}
			if !seenAddr[l.Addr] {
				seenAddr[l.Addr] = true
				p.Addrs = append(p.Addrs, l.Addr)
			}
		}
		sort.Ints(p.Ports)
		out = append(out, p)
	}
	return out
}

// SvcFirewallState is one firewalld service of a scope and whether it is open.
type SvcFirewallState struct {
	Service string `json:"service"`
	Open    bool   `json:"open"`
}

// SvcScopeState is one switch.
type SvcScopeState struct {
	Scope     string         `json:"scope"`
	Label     string         `json:"label"`
	Installed bool           `json:"installed"`
	Active    bool           `json:"active"`   // a unit is running (or the socket listens)
	Enabled   bool           `json:"enabled"`  // a unit starts at boot
	Recorded  bool           `json:"recorded"` // nodespot turned it off (disabled-units)
	Units     []ServiceState `json:"units"`
	// Firewall is filled when firewalld runs: the services an off switch
	// could close, or an on switch reopen.
	Firewall []SvcFirewallState `json:"firewall"`
}

// SvcStatus is the response of svc-status.
type SvcStatus struct {
	Available      bool            `json:"available"` // systemctl works
	Scopes         []SvcScopeState `json:"scopes"`
	PortsAvailable bool            `json:"portsAvailable"` // ss works
	Ports          []SvcPort       `json:"ports"`
	NodespotPort   int             `json:"nodespotPort"`     // 0: unknown
	NodespotOn80   bool            `json:"nodespotOnPort80"` // nodespot holds port 80
	Firewalld      bool            `json:"firewalld"`        // firewalld is running
}

// SvcSetResult is the response of svc-set: the new status plus what changed.
type SvcSetResult struct {
	SvcStatus
	Changed         []string `json:"changed"`         // units turned off or on
	FirewallChanged []string `json:"firewallChanged"` // firewalld services closed or reopened
	FirewallError   string   `json:"firewallError,omitempty"`
}

func isEnabledState(s string) bool { return s == "enabled" || s == "enabled-runtime" }

func isActiveState(s string) bool { return s == "active" || s == "activating" || s == "reloading" }

// svcUnitStates reads the state of every unit of every scope; the units that
// are not installed are missing from the map (keyed by unit id).
func (o *Ops) svcUnitStates(ctx context.Context) (map[string]ServiceState, bool) {
	argv := []string{"systemctl", "show", "--no-pager", "--property=Id,Description,LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp", "--"}
	for _, sc := range svcScopeOrder {
		for _, u := range svcScopes[sc] {
			argv = append(argv, unitID(u))
		}
	}
	out, code, err := o.Run.Run(ctx, argv...)
	if err != nil || code != 0 {
		return nil, false
	}
	m := map[string]ServiceState{}
	for _, s := range parseServices(out) {
		m[s.Unit] = s
	}
	return m, true
}

// fwServices lists the firewalld services that are open (runtime or
// permanent); running is false without firewalld.
func (o *Ops) fwServices(ctx context.Context) (running bool, open map[string]bool) {
	out, code, err := o.Run.Run(ctx, "firewall-cmd", "--state")
	if err != nil || code != 0 || strings.TrimSpace(out) != "running" {
		return false, nil
	}
	open = map[string]bool{}
	for _, argv := range [][]string{{"firewall-cmd", "--list-services"}, {"firewall-cmd", "--permanent", "--list-services"}} {
		if out, code, err := o.Run.Run(ctx, argv...); err == nil && code == 0 {
			for _, s := range strings.Fields(out) {
				open[s] = true
			}
		}
	}
	return true, open
}

// SvcStatus reads the state of the three switches, the listening ports and
// firewalld. It never fails: a missing tool shows as not available.
func (o *Ops) SvcStatus(ctx context.Context) SvcStatus {
	res := SvcStatus{Scopes: []SvcScopeState{}, Ports: []SvcPort{}}
	states, ok := o.svcUnitStates(ctx)
	res.Available = ok
	recorded := map[string]bool{}
	for _, u := range o.readSvcRecord() {
		recorded[u] = true
	}
	running, open := o.fwServices(ctx)
	res.Firewalld = running
	for _, sc := range svcScopeOrder {
		st := SvcScopeState{Scope: sc, Label: svcLabels[sc], Units: []ServiceState{}, Firewall: []SvcFirewallState{}}
		for _, u := range svcScopes[sc] {
			if s, ok := states[unitID(u)]; ok {
				st.Installed = true
				st.Units = append(st.Units, s)
				st.Active = st.Active || isActiveState(s.Active)
				st.Enabled = st.Enabled || isEnabledState(s.Enabled)
			}
			st.Recorded = st.Recorded || recorded[u]
		}
		if running && st.Installed {
			for _, fs := range svcFirewall[sc] {
				st.Firewall = append(st.Firewall, SvcFirewallState{Service: fs, Open: open[fs]})
			}
		}
		res.Scopes = append(res.Scopes, st)
	}
	if ls, ok := o.listeners(ctx); ok {
		res.PortsAvailable = true
		res.Ports = svcPorts(ls)
	}
	if p, err := o.webPort(); err == nil {
		res.NodespotPort = p
		res.NodespotOn80 = p == 80
	}
	return res
}

// ---------------------------------------------------------------- record

// validRecordUnit: a line of the record file is used only if it is one of
// the units of svcScopes. The file sits in a directory the nodespot user owns,
// so neither the helper nor postrm.sh trusts anything else in it.
func validRecordUnit(u string) bool {
	for _, units := range svcScopes {
		for _, x := range units {
			if x == u {
				return true
			}
		}
	}
	return false
}

func (o *Ops) readSvcRecord() []string {
	b, err := os.ReadFile(o.svcRecord())
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		u := strings.TrimSpace(line)
		if validRecordUnit(u) && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// writeSvcRecord replaces the record (root-owned, 0644); an empty list
// removes the file. The caller holds Svc.mu.
func (o *Ops) writeSvcRecord(units []string) error {
	path := o.svcRecord()
	if len(units) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return writeAtomic(path, []byte(strings.Join(units, "\n")+"\n"))
}

// recordUpdate adds or removes units under the lock.
func (o *Ops) recordUpdate(add, remove []string) error {
	cur := o.readSvcRecord()
	drop := map[string]bool{}
	for _, u := range remove {
		drop[u] = true
	}
	var next []string
	for _, u := range cur {
		if !drop[u] {
			next = append(next, u)
		}
	}
	for _, u := range add {
		dup := false
		for _, x := range next {
			if x == u {
				dup = true
			}
		}
		if !dup {
			next = append(next, u)
		}
	}
	if strings.Join(next, ",") == strings.Join(cur, ",") {
		return nil
	}
	return o.writeSvcRecord(next)
}

// ---------------------------------------------------------------- set

func (o *Ops) systemctl(ctx context.Context, args ...string) error {
	out, code, err := o.Run.Run(ctx, append([]string{"systemctl"}, args...)...)
	if err != nil {
		return fmt.Errorf("systemctl: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl %s failed: %s", args[0], LastLines(out, 3))
	}
	return nil
}

// SvcSet turns one scope off or on. Turning off: a unit that is enabled gets
// "disable --now" and is added to the record; a unit that is not (a static
// service such as cockpit.service) is only stopped. Turning on: "enable
// --now" for each unit that can be enabled (static units start through their
// socket) and the record entry is removed. Enabling Apache is refused while
// nodespot holds port 80. With firewall set, the scope's firewalld services are
// closed (off) or reopened (on); a firewalld failure is reported in the
// result and does not fail the switch.
func (o *Ops) SvcSet(ctx context.Context, scope, action string, firewall bool) (SvcSetResult, error) {
	if !ValidSvcScope(scope) {
		return SvcSetResult{}, errors.New("invalid scope")
	}
	if !ValidSvcAction(action) {
		return SvcSetResult{}, errors.New("invalid action")
	}
	o.Svc.mu.Lock()
	defer o.Svc.mu.Unlock()
	states, ok := o.svcUnitStates(ctx)
	if !ok {
		return SvcSetResult{}, errors.New("systemctl is not available")
	}
	installed := false
	for _, u := range svcScopes[scope] {
		if _, ok := states[unitID(u)]; ok {
			installed = true
		}
	}
	if !installed {
		return SvcSetResult{}, ErrSvcNotInstalled
	}
	if action == SvcActionEnable && scope == "apache" {
		if p, err := o.webPort(); err == nil && p == 80 {
			return SvcSetResult{}, ErrPort80Held
		}
	}
	res := SvcSetResult{Changed: []string{}, FirewallChanged: []string{}}
	var add, remove []string
	for _, u := range svcScopes[scope] {
		s, ok := states[unitID(u)]
		if !ok {
			continue
		}
		if action == SvcActionDisable {
			switch {
			case isEnabledState(s.Enabled):
				if err := o.systemctl(ctx, "disable", "--now", u); err != nil {
					_ = o.recordUpdate(add, nil)
					return res, err
				}
				add = append(add, u)
				res.Changed = append(res.Changed, u)
			case isActiveState(s.Active):
				if err := o.systemctl(ctx, "stop", u); err != nil {
					_ = o.recordUpdate(add, nil)
					return res, err
				}
				res.Changed = append(res.Changed, u)
			}
			continue
		}
		// enable
		if s.Enabled == "static" {
			continue // starts through its socket
		}
		if s.Enabled == "masked" || s.Enabled == "masked-runtime" {
			_ = o.recordUpdate(nil, remove)
			return res, fmt.Errorf("%s is masked; unmask it by hand first", u)
		}
		if err := o.systemctl(ctx, "enable", "--now", u); err != nil {
			_ = o.recordUpdate(nil, remove)
			return res, err
		}
		remove = append(remove, u)
		res.Changed = append(res.Changed, u)
	}
	if err := o.recordUpdate(add, remove); err != nil {
		return res, fmt.Errorf("the record %s could not be written: %w", o.svcRecord(), err)
	}
	if firewall {
		res.FirewallChanged, res.FirewallError = o.svcFirewallChange(ctx, scope, action)
		if res.FirewallChanged == nil {
			res.FirewallChanged = []string{}
		}
	}
	res.SvcStatus = o.SvcStatus(ctx)
	return res, nil
}

// svcFirewallChange closes or reopens the scope's firewalld services
// (permanent, then one reload). Only services that need the change are
// touched. It returns the changed services and an error text.
func (o *Ops) svcFirewallChange(ctx context.Context, scope, action string) ([]string, string) {
	svcs := svcFirewall[scope]
	if len(svcs) == 0 {
		return nil, ""
	}
	running, _ := o.fwServices(ctx)
	if !running {
		return nil, "firewalld is not running"
	}
	// The permanent list is what a change edits.
	permanent := map[string]bool{}
	if out, code, err := o.Run.Run(ctx, "firewall-cmd", "--permanent", "--list-services"); err == nil && code == 0 {
		for _, s := range strings.Fields(out) {
			permanent[s] = true
		}
	}
	var changed []string
	for _, s := range svcs {
		verb := "--remove-service=" + s
		if action == SvcActionEnable {
			verb = "--add-service=" + s
		}
		if (action == SvcActionDisable) != permanent[s] {
			continue // already as wanted
		}
		out, code, err := o.Run.Run(ctx, "firewall-cmd", "--permanent", verb)
		if err != nil || code != 0 {
			return changed, fmt.Sprintf("firewall-cmd %s failed: %s", verb, LastLines(out, 2))
		}
		changed = append(changed, s)
	}
	if len(changed) > 0 {
		if out, code, err := o.Run.Run(ctx, "firewall-cmd", "--reload"); err != nil || code != 0 {
			return changed, "firewall-cmd --reload failed: " + LastLines(out, 2)
		}
	}
	return changed, ""
}
