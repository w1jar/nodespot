package helper

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// Demo behaviour of the services switch and the port 80 move. The state sits
// in a package-level map keyed by the DemoSystem, so sys_demo.go stays as it
// is. Nothing on the machine is touched.
var demoSvc sync.Map // *DemoSystem -> *demoSvcState

type demoSvcState struct {
	mu      sync.Mutex
	off     map[string]bool // scope -> turned off
	port    int
	pending bool
}

func (d *DemoSystem) svcState() *demoSvcState {
	v, _ := demoSvc.LoadOrStore(d, &demoSvcState{off: map[string]bool{}, port: 8080})
	return v.(*demoSvcState)
}

func (s *demoSvcState) status() SvcStatus {
	res := SvcStatus{Available: true, PortsAvailable: true, Firewalld: true, NodespotPort: s.port, NodespotOn80: s.port == 80,
		Scopes: []SvcScopeState{}, Ports: []SvcPort{}}
	for _, sc := range svcScopeOrder {
		on := !s.off[sc]
		st := SvcScopeState{Scope: sc, Label: svcLabels[sc], Installed: true, Active: on, Enabled: on, Recorded: !on,
			Units: []ServiceState{}, Firewall: []SvcFirewallState{}}
		for _, u := range svcScopes[sc] {
			en := "enabled"
			if u == "cockpit.service" {
				en = "static"
			}
			act := "inactive"
			if on {
				act = "active"
			}
			st.Units = append(st.Units, ServiceState{Unit: u, Active: act, Enabled: en})
		}
		for _, f := range svcFirewall[sc] {
			st.Firewall = append(st.Firewall, SvcFirewallState{Service: f, Open: on})
		}
		res.Scopes = append(res.Scopes, st)
	}
	var ls []SockListener
	add := func(p int) { ls = append(ls, SockListener{Addr: "*", Port: p}) }
	add(s.port)
	if !s.off["apache"] {
		add(80)
		add(443)
	}
	if !s.off["cockpit"] {
		add(9090)
	}
	if !s.off["allmon3"] {
		add(16080)
		add(16700)
	}
	res.Ports = svcPorts(ls)
	return res
}

// SvcStatus implements the SvcHost interface of the API.
func (d *DemoSystem) SvcStatus(context.Context) (SvcStatus, error) {
	s := d.svcState()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status(), nil
}

// SvcSet flips a simulated switch.
func (d *DemoSystem) SvcSet(_ context.Context, scope, action string, firewall bool) (SvcSetResult, error) {
	if !ValidSvcScope(scope) {
		return SvcSetResult{}, errors.New("invalid scope")
	}
	if !ValidSvcAction(action) {
		return SvcSetResult{}, errors.New("invalid action")
	}
	s := d.svcState()
	s.mu.Lock()
	defer s.mu.Unlock()
	if action == SvcActionEnable && scope == "apache" && s.port == 80 {
		return SvcSetResult{}, ErrPort80Held
	}
	res := SvcSetResult{Changed: append([]string{}, svcScopes[scope]...), FirewallChanged: []string{}}
	s.off[scope] = action == SvcActionDisable
	if firewall {
		res.FirewallChanged = append(res.FirewallChanged, svcFirewall[scope]...)
	}
	res.SvcStatus = s.status()
	return res, nil
}

func (s *demoSvcState) webPort() WebPortStatus {
	st := WebPortStatus{Available: true, Listen: ":" + strconv.Itoa(s.port), Port: s.port, On80: s.port == 80, BackPort: 8080,
		ApacheActive: !s.off["apache"], DropIn: s.port == 80, Previous: ":8080"}
	switch {
	case s.port == 80:
		st.Reason = "already"
	case !s.off["apache"]:
		st.Reason = "busy"
	}
	st.CanMove = st.Reason == ""
	if s.pending {
		st.Pending = &WebPortPending{Seconds: int(DefaultGuardDelay.Seconds())}
	}
	return st
}

// WebPortStatus implements the SvcHost interface of the API.
func (d *DemoSystem) WebPortStatus(context.Context) (WebPortStatus, error) {
	s := d.svcState()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.webPort(), nil
}

// WebPortSet simulates move, back and confirm.
func (d *DemoSystem) WebPortSet(_ context.Context, action string) (WebPortStatus, error) {
	s := d.svcState()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case WebPortActionMove:
		if s.port == 80 {
			return WebPortStatus{}, ErrAlreadyPort80
		}
		if !s.off["apache"] {
			return WebPortStatus{}, ErrPort80Busy
		}
		s.port, s.pending = 80, true
	case WebPortActionBack:
		if s.port != 80 {
			return WebPortStatus{}, ErrNotOnPort80
		}
		s.port, s.pending = 8080, false
	case WebPortActionConfirm:
		s.pending = false
	default:
		return WebPortStatus{}, errors.New("invalid action")
	}
	return s.webPort(), nil
}
