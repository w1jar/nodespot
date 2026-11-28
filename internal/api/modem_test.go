package api_test

import (
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/node/nodetest"
)

func mmdvmNodeConfig() *node.Config {
	c := testNodeConfig()
	c.Station.Radio = node.RadioConfig{Preset: "mmdvm"}
	return c
}

func TestModemStatusAndReset(t *testing.T) {
	m := &nodetest.DemoModem{}
	e := newEnv(t, opts{modem: m, nodeCfg: mmdvmNodeConfig()})
	var mi node.ModemInfo
	if c := e.do("admin", "GET", "/api/radio/modem", nil, &mi); c != 200 {
		t.Fatalf("status %d", c)
	}
	if !mi.Configured || !mi.Running || !mi.Connected || mi.Board != "MMDVM_HS" || mi.Firmware != "1.6.1" ||
		mi.Protocol != 1 || mi.BuildDate != "2023-11-15" || !mi.DuplexCapable || mi.State != "idle" || !mi.DetectOnly {
		t.Errorf("modem %+v", mi)
	}
	code, raw, _ := e.raw("GET", "/api/radio/modem", nil, e.ck["admin"], nil)
	if code != 200 || strings.Contains(strings.ToLower(string(raw)), "udid") {
		t.Errorf("GET /api/radio/modem %d names a unique ID: %s", code, raw)
	}
	if c := e.do("admin", "POST", "/api/radio/modem/reset", nil, &mi); c != 200 || m.Reopens() != 1 {
		t.Fatalf("reset %d, %d reopens", c, m.Reopens())
	}
	aud, _ := e.st.ListAudit(5)
	if len(aud) == 0 || aud[0].Action != "radio.modem.reset" {
		t.Fatalf("audit %+v", aud)
	}
	for _, role := range []string{"op", "viewer"} {
		expectErr(t, e, role, "GET", "/api/radio/modem", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/radio/modem/reset", nil, 403, "forbidden")
	}
}

// Without the mmdvm preset there is no modem to reset.
func TestModemNone(t *testing.T) {
	e := newEnv(t, opts{})
	var mi node.ModemInfo
	if c := e.do("admin", "GET", "/api/radio/modem", nil, &mi); c != 200 || mi.Configured || mi.Running {
		t.Fatalf("%d %+v", c, mi)
	}
	expectErr(t, e, "admin", "POST", "/api/radio/modem/reset", nil, 409, "node_refused")
	e.stopNode()
	expectErr(t, e, "admin", "GET", "/api/radio/modem", nil, 503, "node_unavailable")
}

func TestRFPrepareRoutes(t *testing.T) {
	fs := newFakeSys()
	e := newEnv(t, opts{sysHost: fs})
	var res helper.RFPrepareResult
	if c := e.do("admin", "POST", "/api/radio/prepare", nil, &res); c != 200 || !res.RebootNeeded || len(res.Changes) != 1 {
		t.Fatalf("%d %+v", c, res)
	}
	if fs.got() != "rf-prepare" {
		t.Fatalf("calls %q", fs.got())
	}
	aud, _ := e.st.ListAudit(5)
	if len(aud) == 0 || aud[0].Action != "radio.prepare" {
		t.Fatalf("audit %+v", aud)
	}
	var chk helper.RFDeviceCheck
	if c := e.do("admin", "GET", "/api/radio/device-check", nil, &chk); c != 200 || chk.LinkTarget != "ttyAMA0" {
		t.Fatalf("%d %+v", c, chk)
	}
	for _, role := range []string{"op", "viewer"} {
		expectErr(t, e, role, "POST", "/api/radio/prepare", nil, 403, "forbidden")
		expectErr(t, e, role, "GET", "/api/radio/device-check", nil, 403, "forbidden")
	}
	if fs.got() != "rf-prepare" {
		t.Fatalf("a refused call reached the helper: %q", fs.got())
	}

	e2 := newEnv(t, opts{})
	expectErr(t, e2, "admin", "POST", "/api/radio/prepare", nil, 503, "system_unavailable")
	expectErr(t, e2, "admin", "GET", "/api/radio/device-check", nil, 503, "system_unavailable")
}
