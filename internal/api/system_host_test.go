package api_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// fakeSys is the System tab helper; it records the calls that change things.
type fakeSys struct {
	mu         sync.Mutex
	host       helper.HostStatus
	calls      []string
	restartErr error // NodeRestart answers this
}

func newFakeSys() *fakeSys {
	return &fakeSys{host: helper.HostStatus{Hostname: "node2001", Timezone: "America/Chicago", NTP: true, HostnameAvail: true, TimeAvail: true}}
}

func (f *fakeSys) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeSys) got() string  { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(f.calls, ";") }

func (f *fakeSys) HostStatus(context.Context) (helper.HostStatus, error) { return f.host, nil }
func (f *fakeSys) SetHostname(_ context.Context, n string) (helper.HostStatus, error) {
	f.rec("hostname=" + n)
	f.host.Hostname = n
	return f.host, nil
}
func (f *fakeSys) SetTimezone(_ context.Context, z string) (helper.HostStatus, error) {
	f.rec("tz=" + z)
	f.host.Timezone = z
	return f.host, nil
}
func (f *fakeSys) SetNTP(_ context.Context, on bool) (helper.HostStatus, error) {
	f.rec(fmt.Sprint("ntp=", on))
	f.host.NTP = on
	return f.host, nil
}
func (f *fakeSys) Timezones(context.Context) ([]string, error) {
	return []string{"America/Chicago", "Europe/London", "UTC"}, nil
}
func (f *fakeSys) Poweroff(context.Context) error { f.rec("poweroff"); return nil }
func (f *fakeSys) Logs(_ context.Context, unit string, lines int, since string) (helper.LogsResult, error) {
	f.rec(fmt.Sprintf("logs=%s,%d,%s", unit, lines, since))
	return helper.LogsResult{Unit: unit, Lines: lines, Since: since, Text: "line\n", Available: true}, nil
}
func (f *fakeSys) Services(context.Context) (helper.ServicesResult, error) {
	return helper.ServicesResult{Available: true, Services: []helper.ServiceState{{Unit: "nodespot.service", Active: "active"}, {Unit: "nodespot-node.service", Active: "active", Sub: "running"}}}, nil
}
func (f *fakeSys) NodeRestart(context.Context) error { f.rec("node-restart"); return f.restartErr }
func (f *fakeSys) Network(context.Context) (helper.NetStatus, error) {
	return helper.NetStatus{Available: true, Interfaces: []helper.NetIface{{Name: "eth0", State: "UP"}}}, nil
}

func TestSystemRoles(t *testing.T) {
	e := newEnv(t, opts{sysHost: newFakeSys()})
	for _, ep := range [][2]string{
		{"GET", "/api/system/host"}, {"PUT", "/api/system/host"}, {"GET", "/api/system/timezones"}, {"POST", "/api/system/poweroff"},
		{"GET", "/api/system/logs?unit=nodespot-node"}, {"GET", "/api/system/node-service"}, {"POST", "/api/system/node-service/restart"}, {"GET", "/api/system/services"}, {"GET", "/api/system/network"}, {"GET", "/api/audit"},
	} {
		for _, role := range []string{"op", "viewer"} {
			var body any
			if ep[0] == "PUT" {
				body = map[string]any{"ntp": false}
			}
			expectErr(t, e, role, ep[0], ep[1], body, 403, "forbidden")
		}
	}
}

func TestSystemUnavailable(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "GET", "/api/system/host", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "GET", "/api/system/network", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "POST", "/api/system/poweroff", nil, 503, "system_unavailable")
}

func TestSystemHostPut(t *testing.T) {
	fs := newFakeSys()
	e := newEnv(t, opts{sysHost: fs})
	var st helper.HostStatus
	if c := e.do("admin", "GET", "/api/system/host", nil, &st); c != 200 || st.Hostname != "node2001" {
		t.Fatalf("%d %+v", c, st)
	}
	// invalid values never reach the helper
	for _, b := range []map[string]any{{}, {"hostname": "-h"}, {"hostname": "a b"}, {"hostname": "x;reboot"}, {"timezone": "../etc"}, {"timezone": "Mars/Base"}} {
		expectErr(t, e, "admin", "PUT", "/api/system/host", b, 400, "bad_request")
	}
	if fs.got() != "" {
		t.Fatalf("helper called: %s", fs.got())
	}
	if c := e.do("admin", "PUT", "/api/system/host", map[string]any{"hostname": "Node2002", "timezone": "Europe/London", "ntp": false}, &st); c != 200 {
		t.Fatalf("%d", c)
	}
	if fs.got() != "hostname=node2002;tz=Europe/London;ntp=false" || st.Hostname != "node2002" || st.NTP {
		t.Fatalf("%s %+v", fs.got(), st)
	}
	// unchanged values are not applied again
	e.do("admin", "PUT", "/api/system/host", map[string]any{"hostname": "node2002", "ntp": false}, nil)
	if strings.Count(fs.got(), ";") != 2 {
		t.Fatalf("%s", fs.got())
	}
	aud, _ := e.st.ListAudit(50)
	var actions []string
	for _, a := range aud {
		actions = append(actions, a.Action)
	}
	for _, w := range []string{"system.hostname", "system.timezone", "system.ntp"} {
		if !strings.Contains(strings.Join(actions, " "), w) {
			t.Errorf("no audit %s in %v", w, actions)
		}
	}
	if a := aud[2]; a.Action != "system.hostname" || !strings.Contains(a.Detail, `"to":"node2002"`) || a.Username != "admin" {
		t.Fatalf("%+v", a)
	}
	var tz struct{ Timezones []string }
	if c := e.do("admin", "GET", "/api/system/timezones", nil, &tz); c != 200 || len(tz.Timezones) != 3 {
		t.Fatalf("%d %v", c, tz)
	}
}

func TestSystemPoweroff(t *testing.T) {
	fs := newFakeSys()
	e := newEnv(t, opts{sysHost: fs})
	if c := e.do("admin", "POST", "/api/system/poweroff", nil, nil); c != 202 {
		t.Fatalf("%d", c)
	}
	if fs.got() != "poweroff" {
		t.Fatal(fs.got())
	}
	if aud, _ := e.st.ListAudit(5); len(aud) == 0 || aud[0].Action != "system.poweroff" {
		t.Fatalf("%+v", aud)
	}
}

func TestSystemLogsServicesNetwork(t *testing.T) {
	fs := newFakeSys()
	e := newEnv(t, opts{sysHost: fs})
	for _, q := range []string{"unit=sshd", "unit=asterisk%3Bls", "unit=asterisk&since=forever", "unit=asterisk&lines=abc", "unit=--help"} {
		expectErr(t, e, "admin", "GET", "/api/system/logs?"+q, nil, 400, "bad_request")
	}
	if fs.got() != "" {
		t.Fatalf("helper called: %s", fs.got())
	}
	var lr helper.LogsResult
	if c := e.do("admin", "GET", "/api/system/logs?unit=nodespot&lines=99999&since=1h", nil, &lr); c != 200 || lr.Text != "line\n" {
		t.Fatalf("%d %+v", c, lr)
	}
	e.do("admin", "GET", "/api/system/logs?unit=nodespot&lines=1", nil, nil)
	if fs.got() != "logs=nodespot,1000,1h;logs=nodespot,50," {
		t.Fatalf("%s", fs.got())
	}
	var units struct{ Units []string }
	if c := e.do("admin", "GET", "/api/system/logs", nil, &units); c != 200 || len(units.Units) < 5 {
		t.Fatalf("%d %v", c, units)
	}
	var sv helper.ServicesResult
	if c := e.do("admin", "GET", "/api/system/services", nil, &sv); c != 200 || len(sv.Services) != 2 {
		t.Fatalf("%d %+v", c, sv)
	}
	var ns helper.NetStatus
	if c := e.do("admin", "GET", "/api/system/network", nil, &ns); c != 200 || len(ns.Interfaces) != 1 {
		t.Fatalf("%d %+v", c, ns)
	}
}

func TestAuditEndpoint(t *testing.T) {
	e := newEnv(t, opts{})
	for i := 1; i <= 7; i++ {
		who := "admin"
		if i%2 == 0 {
			who = "op"
		}
		_ = e.st.AddAudit(storeAudit(who, fmt.Sprintf("test.action%d", i), fmt.Sprintf(`{"n":%d}`, i)))
	}
	type page struct {
		Entries []struct {
			ID     int64
			Time   string
			User   string
			Action string
			Detail string
		}
		Next *int64
	}
	var p1, p2, p3 page
	if c := e.do("admin", "GET", "/api/audit?limit=3&action=test.", nil, &p1); c != 200 || len(p1.Entries) != 3 || p1.Next == nil || p1.Entries[0].Action != "test.action7" || p1.Entries[0].Time == "" || p1.Entries[0].Detail != `{"n":7}` {
		t.Fatalf("%d %+v", c, p1)
	}
	e.do("admin", "GET", fmt.Sprintf("/api/audit?limit=3&action=test.&before=%d", *p1.Next), nil, &p2)
	e.do("admin", "GET", fmt.Sprintf("/api/audit?limit=3&action=test.&before=%d", *p2.Next), nil, &p3)
	if len(p2.Entries) != 3 || p2.Next == nil || len(p3.Entries) != 1 || p3.Next != nil || p3.Entries[0].Action != "test.action1" || p2.Entries[0].ID >= p1.Entries[2].ID {
		t.Fatalf("%+v %+v", p2, p3)
	}
	var byUser page
	e.do("admin", "GET", "/api/audit?user=op&action=test.", nil, &byUser)
	if len(byUser.Entries) != 3 || byUser.Next != nil {
		t.Fatalf("%+v", byUser)
	}
	for _, q := range []string{"limit=0", "limit=x", "before=0", "before=abc"} {
		expectErr(t, e, "admin", "GET", "/api/audit?"+q, nil, 400, "bad_request")
	}
}

func storeAudit(user, action, detail string) store.Audit {
	return store.Audit{Username: user, Action: action, Detail: detail}
}
