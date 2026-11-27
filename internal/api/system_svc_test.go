package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
)

// fakeSvcSys adds the services switch and the port 80 move to fakeSys.
type fakeSvcSys struct {
	*fakeSys
	port   int
	apache bool // Apache holds port 80
}

func newFakeSvcSys() *fakeSvcSys { return &fakeSvcSys{fakeSys: newFakeSys(), port: 8080, apache: true} }

func (f *fakeSvcSys) SvcStatus(context.Context) (helper.SvcStatus, error) {
	return helper.SvcStatus{Available: true, NodespotPort: f.port, NodespotOn80: f.port == 80, Scopes: []helper.SvcScopeState{}, Ports: []helper.SvcPort{}}, nil
}

func (f *fakeSvcSys) SvcSet(_ context.Context, scope, action string, fw bool) (helper.SvcSetResult, error) {
	f.rec(fmt.Sprintf("svc=%s,%s,%v", scope, action, fw))
	if scope == "apache" && action == helper.SvcActionEnable && f.port == 80 {
		return helper.SvcSetResult{}, helper.ErrPort80Held
	}
	if scope == "apache" {
		f.apache = action == helper.SvcActionEnable
	}
	return helper.SvcSetResult{Changed: []string{scope}, FirewallChanged: []string{}}, nil
}

func (f *fakeSvcSys) WebPortStatus(context.Context) (helper.WebPortStatus, error) {
	st := helper.WebPortStatus{Available: true, Port: f.port, On80: f.port == 80, DropIn: f.port == 80, BackPort: 8080}
	switch {
	case f.port == 80:
		st.Reason = "already"
	case f.apache:
		st.Reason = "busy"
	}
	st.CanMove = st.Reason == ""
	return st, nil
}

func (f *fakeSvcSys) WebPortSet(ctx context.Context, action string) (helper.WebPortStatus, error) {
	f.rec("webport=" + action)
	switch action {
	case helper.WebPortActionMove:
		f.port = 80
	case helper.WebPortActionBack:
		f.port = 8080
	}
	return f.WebPortStatus(ctx)
}

func TestSvcRoles(t *testing.T) {
	e := newEnv(t, opts{sysHost: newFakeSvcSys()})
	for _, ep := range [][2]string{
		{"GET", "/api/system/asl-services"}, {"PUT", "/api/system/asl-services/cockpit"},
		{"GET", "/api/system/web-port"}, {"PUT", "/api/system/web-port"},
	} {
		for _, role := range []string{"op", "viewer"} {
			var body any
			if ep[0] == "PUT" {
				body = map[string]any{"action": "disable"}
			}
			expectErr(t, e, role, ep[0], ep[1], body, 403, "forbidden")
		}
	}
}

func TestSvcUnavailable(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "GET", "/api/system/asl-services", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "PUT", "/api/system/web-port", map[string]any{"action": "move"}, 503, "system_unavailable")
	// a System helper without the new methods is also "not available"
	e2 := newEnv(t, opts{sysHost: newFakeSys()})
	expectErr(t, e2, "admin", "GET", "/api/system/web-port", nil, 503, "system_unavailable")
}

func TestSvcPut(t *testing.T) {
	fs := newFakeSvcSys()
	e := newEnv(t, opts{sysHost: fs})
	var st helper.SvcStatus
	if c := e.do("admin", "GET", "/api/system/asl-services", nil, &st); c != 200 || !st.Available || st.NodespotPort != 8080 {
		t.Fatalf("%d %+v", c, st)
	}
	// nothing invalid reaches the helper
	expectErr(t, e, "admin", "PUT", "/api/system/asl-services/sshd", map[string]any{"action": "disable"}, 404, "not_found")
	expectErr(t, e, "admin", "PUT", "/api/system/asl-services/cockpit", map[string]any{"action": "restart"}, 400, "bad_request")
	expectErr(t, e, "admin", "PUT", "/api/system/asl-services/cockpit", map[string]any{}, 400, "bad_request")
	if fs.got() != "" {
		t.Fatalf("helper called: %s", fs.got())
	}
	var res helper.SvcSetResult
	if c := e.do("admin", "PUT", "/api/system/asl-services/apache", map[string]any{"action": "disable", "firewall": true}, &res); c != 200 {
		t.Fatalf("%d", c)
	}
	if fs.got() != "svc=apache,disable,true" || len(res.Changed) != 1 {
		t.Fatalf("%s %+v", fs.got(), res)
	}
	aud, _ := e.st.ListAudit(10)
	if len(aud) == 0 || aud[0].Action != "system.svc" || !strings.Contains(aud[0].Detail, `"scope":"apache"`) || aud[0].Username != "admin" {
		t.Fatalf("%+v", aud)
	}
	// Apache cannot come back while nodespot holds port 80
	fs.port = 80
	expectErr(t, e, "admin", "PUT", "/api/system/asl-services/apache", map[string]any{"action": "enable"}, 409, "port80_held")
}

func TestWebPortPut(t *testing.T) {
	fs := newFakeSvcSys()
	e := newEnv(t, opts{sysHost: fs})
	expectErr(t, e, "admin", "PUT", "/api/system/web-port", map[string]any{"action": "sideways"}, 400, "bad_request")
	expectErr(t, e, "admin", "PUT", "/api/system/web-port", map[string]any{}, 400, "bad_request")
	// Apache is on: refused, nothing changes and nothing is audited
	expectErr(t, e, "admin", "PUT", "/api/system/web-port", map[string]any{"action": "move"}, 409, "port80_busy")
	expectErr(t, e, "admin", "PUT", "/api/system/web-port", map[string]any{"action": "back"}, 409, "not_port80")
	if fs.got() != "" {
		t.Fatalf("helper called: %s", fs.got())
	}
	if aud, _ := e.st.ListAudit(10); len(aud) != 0 {
		t.Fatalf("audited a refusal: %+v", aud)
	}
	fs.apache = false
	var st helper.WebPortStatus
	if c := e.do("admin", "PUT", "/api/system/web-port", map[string]any{"action": "move"}, &st); c != 200 || !st.On80 {
		t.Fatalf("%d %+v", c, st)
	}
	expectErr(t, e, "admin", "PUT", "/api/system/web-port", map[string]any{"action": "move"}, 409, "already_port80")
	if c := e.do("admin", "PUT", "/api/system/web-port", map[string]any{"action": "confirm"}, &st); c != 200 {
		t.Fatalf("%d", c)
	}
	if c := e.do("admin", "PUT", "/api/system/web-port", map[string]any{"action": "back"}, &st); c != 200 || st.On80 {
		t.Fatalf("%d %+v", c, st)
	}
	if fs.got() != "webport=move;webport=confirm;webport=back" {
		t.Fatalf("%s", fs.got())
	}
	aud, _ := e.st.ListAudit(10)
	n := 0
	for _, a := range aud {
		if a.Action == "system.webport" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d audit rows: %+v", n, aud)
	}
	if c := e.do("admin", "GET", "/api/system/web-port", nil, &st); c != 200 || st.Port != 8080 {
		t.Fatalf("%d %+v", c, st)
	}
}
