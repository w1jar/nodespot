package api_test

import (
	"errors"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
)

func TestNodeServiceGet(t *testing.T) {
	e := newEnv(t, opts{sysHost: newFakeSys()})
	var got struct {
		Available bool
		Service   *helper.ServiceState
	}
	if c := e.do("admin", "GET", "/api/system/node-service", nil, &got); c != 200 {
		t.Fatalf("status %d", c)
	}
	if !got.Available || got.Service == nil || got.Service.Unit != "nodespot-node.service" || got.Service.Sub != "running" {
		t.Fatalf("%+v %+v", got, got.Service)
	}
}

func TestNodeServiceRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
		wantOK   bool
	}{
		{name: "ok", wantCode: 200, wantOK: true},
		{name: "fails", err: errors.New("systemctl restart nodespot-node failed: exit 1"), wantCode: 502, wantErr: "helper_error"},
		{name: "unavailable", err: &helper.UnavailableError{Reason: "no systemd"}, wantCode: 503, wantErr: "system_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeSys()
			fs.restartErr = tc.err
			e := newEnv(t, opts{sysHost: fs})
			if tc.wantErr != "" {
				expectErr(t, e, "admin", "POST", "/api/system/node-service/restart", nil, tc.wantCode, tc.wantErr)
			} else if c := e.do("admin", "POST", "/api/system/node-service/restart", nil, nil); c != tc.wantCode {
				t.Fatalf("status %d", c)
			}
			if fs.got() != "node-restart" {
				t.Fatalf("calls %q", fs.got())
			}
			aud, _ := e.st.ListAudit(5)
			if len(aud) == 0 || aud[0].Action != "node.restart" {
				t.Fatalf("audit %+v", aud)
			}
		})
	}
}

func TestNodeServiceUnavailable(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "GET", "/api/system/node-service", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "POST", "/api/system/node-service/restart", nil, 503, "system_unavailable")
}
