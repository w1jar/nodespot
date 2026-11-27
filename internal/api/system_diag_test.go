package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
)

// diagSys is a fakeSys that also builds a bundle (a one-file tarball) and
// records the request it got.
type diagSys struct {
	*fakeSys
	req  helper.DiagRequest
	err  error
	body []byte
}

func (d *diagSys) DiagBundle(_ context.Context, req helper.DiagRequest) (io.ReadCloser, error) {
	d.req = req
	if d.err != nil {
		return nil, d.err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README.txt", Mode: 0o600, Size: 2})
	_, _ = tw.Write([]byte("ok"))
	tw.Close()
	gz.Close()
	d.body = buf.Bytes()
	return io.NopCloser(bytes.NewReader(d.body)), nil
}

func TestDiagBundleEndpoint(t *testing.T) {
	d := &diagSys{fakeSys: newFakeSys()}
	e := newEnv(t, opts{sysHost: d})

	// Roles: admin only.
	for _, role := range []string{"viewer", "op"} {
		for _, p := range []string{"/api/system/diagnostics", "/api/system/diagnostics/bundle"} {
			if c := e.do(role, "GET", p, nil, nil); c != http.StatusForbidden {
				t.Errorf("%s %s: %d", role, p, c)
			}
		}
	}

	// Preview.
	var plan struct {
		Items       []struct{ Name, Desc string }
		Excluded    []string
		Redacted    []string
		Available   bool
		MaskDefault bool
		MaxBytes    int
		TimeLimit   int
	}
	if c := e.do("admin", "GET", "/api/system/diagnostics", nil, &plan); c != 200 {
		t.Fatalf("plan %d", c)
	}
	if !plan.Available || !plan.MaskDefault || len(plan.Items) < 8 || len(plan.Excluded) < 5 || plan.TimeLimit == 0 {
		t.Errorf("%+v", plan)
	}

	// Download with the default (mask on).
	status, body, hdr := e.raw("GET", "/api/system/diagnostics/bundle", nil, e.ck["admin"], nil)
	if status != 200 || !bytes.Equal(body, d.body) {
		t.Fatalf("status %d, body %d bytes", status, len(body))
	}
	if hdr.Get("Content-Type") != "application/gzip" || !strings.HasPrefix(hdr.Get("Content-Disposition"), `attachment; filename="nodespot-diagnostics-`) ||
		!strings.HasSuffix(hdr.Get("Content-Disposition"), `.tar.gz"`) || hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", hdr)
	}
	if !d.req.MaskIdentity || d.req.Version != "test" || !strings.Contains(d.req.Report, "nodespot") {
		t.Errorf("request %+v", d.req)
	}
	if !strings.Contains(d.req.Audit, "system.diagnostics.download") && d.req.Audit != "" {
		// the row is written after the request is built; the first download has no row yet
		t.Logf("audit: %q", d.req.Audit)
	}

	// Mask off, then the audit rows.
	if c := e.do("admin", "GET", "/api/system/diagnostics/bundle?mask=0", nil, nil); c != 200 {
		t.Fatalf("mask=0: %d", c)
	}
	if d.req.MaskIdentity {
		t.Error("mask=0 ignored")
	}
	if !strings.Contains(d.req.Audit, "system.diagnostics.download") || !strings.Contains(d.req.Audit, `"maskIdentity":true`) {
		t.Errorf("audit text of the second bundle: %q", d.req.Audit)
	}
	var au struct {
		Entries []struct{ Action, Detail string }
	}
	e.do("admin", "GET", "/api/audit?action=system.diagnostics", nil, &au)
	if len(au.Entries) != 2 || au.Entries[0].Detail != `{"maskIdentity":false}` || au.Entries[1].Detail != `{"maskIdentity":true}` {
		t.Errorf("audit %+v", au.Entries)
	}

	// Bad parameter.
	if c, code := e.errCode("admin", "GET", "/api/system/diagnostics/bundle?mask=maybe", nil); c != 400 || code != "bad_request" {
		t.Errorf("%d %s", c, code)
	}
	// Busy.
	d.err = helper.ErrDiagBusy
	if c, code := e.errCode("admin", "GET", "/api/system/diagnostics/bundle", nil); c != 409 || code != "diag_busy" {
		t.Errorf("busy %d %s", c, code)
	}
	// Helper not available.
	d.err = &helper.UnavailableError{Reason: "helper not running"}
	if c, code := e.errCode("admin", "GET", "/api/system/diagnostics/bundle", nil); c != 503 || code != "system_unavailable" {
		t.Errorf("unavailable %d %s", c, code)
	}
	d.err = io.ErrUnexpectedEOF
	if c, code := e.errCode("admin", "GET", "/api/system/diagnostics/bundle", nil); c != 502 || code != "helper_error" {
		t.Errorf("failure %d %s", c, code)
	}
	var au2 struct{ Entries []struct{ Action string } }
	e.do("admin", "GET", "/api/audit?action=system.diagnostics", nil, &au2)
	if len(au2.Entries) != 2 {
		t.Errorf("failed downloads were audited: %+v", au2.Entries)
	}
}

func TestDiagBundleWithoutHelper(t *testing.T) {
	e := newEnv(t, opts{})
	if c, code := e.errCode("admin", "GET", "/api/system/diagnostics/bundle", nil); c != 503 || code != "system_unavailable" {
		t.Errorf("%d %s", c, code)
	}
	var plan struct{ Available bool }
	if c := e.do("admin", "GET", "/api/system/diagnostics", nil, &plan); c != 200 || plan.Available {
		t.Errorf("%d %+v", c, plan)
	}
	// A system host without the bundle op.
	e2 := newEnv(t, opts{sysHost: newFakeSys()})
	if c, code := e2.errCode("admin", "GET", "/api/system/diagnostics/bundle", nil); c != 503 || code != "system_unavailable" {
		t.Errorf("%d %s", c, code)
	}
}
