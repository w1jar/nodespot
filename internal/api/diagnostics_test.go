package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/config"
	"github.com/w1jar/nodespot/internal/diag"
)

func TestAbout(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "", "GET", "/api/about", nil, 401, "unauthorized")
	for _, role := range []string{"admin", "op", "viewer"} {
		var a struct {
			Version     string `json:"version"`
			NodeRunning bool   `json:"nodeRunning"`
			Platform    struct {
				OS       string `json:"os"`
				Arch     string `json:"arch"`
				Hardware string `json:"hardware"`
			} `json:"platform"`
		}
		if c := e.do(role, "GET", "/api/about", nil, &a); c != 200 {
			t.Fatalf("%s: %d", role, c)
		}
		if a.Version != "test" || !a.NodeRunning || a.Platform.OS == "" || a.Platform.Arch == "" || a.Platform.Hardware == "" {
			t.Fatalf("%s: %+v", role, a)
		}
	}
}

func TestDiagnostics(t *testing.T) {
	ring := diag.NewRing(50)
	// The node's passwords never reach the app; a secret the app knows
	// (here registered by hand) is redacted.
	secret := "test-app-secret"
	e := newEnv(t, opts{log: ring, cfg: func() config.Config {
		c := config.Defaults()
		c.NodelistRefresh = time.Hour
		return c
	}})
	e.srv.AddSecret(secret)
	ring.Write([]byte("2026/11/15 login with secret=" + secret + " from 203.0.113.50\nplain mention " + secret + " here, peer N2CALL mail x@y.org\n"))
	e.srv.AddSecret("late-resolved-secret")
	ring.Write([]byte("oops late-resolved-secret\n"))

	for _, role := range []string{"op", "viewer"} {
		expectErr(t, e, role, "GET", "/api/diagnostics", nil, 403, "forbidden")
	}
	expectErr(t, e, "", "GET", "/api/diagnostics", nil, 401, "unauthorized")

	code, body, hdr := e.raw("GET", "/api/diagnostics", nil, e.ck["admin"], nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="nodespot-diagnostics-`) || !strings.HasSuffix(cd, `.txt"`) {
		t.Errorf("disposition %q", cd)
	}
	out := string(body)
	for _, bad := range []string{secret, "late-resolved-secret", "203.0.113.50", "N2CALL", "x@y.org"} {
		if secret == "" || strings.Contains(out, bad) {
			t.Errorf("diagnostics contains %q", bad)
		}
	}
	for _, good := range []string{"nodespot:    test", "N0CALL", "allstar", "Entries: 4", "== Server log", "<redacted>", "<ipv4>", "[node]"} {
		if !strings.Contains(out, good) {
			t.Errorf("diagnostics lacks %q\n%s", good, out)
		}
	}
}
