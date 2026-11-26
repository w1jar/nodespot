package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/backupsvc"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

type bkEnv struct {
	*env
	ops  *helper.Ops
	svc  *backupsvc.Service
	data string
}

func demoSvc(t *testing.T, version string, demo bool) (*helper.Ops, func(st *store.Store) *backupsvc.Service, string) {
	t.Helper()
	ops, err := helper.NewDemoOps(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	return ops, func(st *store.Store) *backupsvc.Service {
		return backupsvc.New(backupsvc.Options{Helper: helper.LocalBackup{Ops: ops}, Store: st, DataDir: data,
			Version: version, Logger: log.New(io.Discard, "", 0), Demo: demo,
			Hostname: func() string { return "node-a" }, Timezone: func() string { return "UTC" }})
	}, data
}

func newBk(t *testing.T, o opts, version string, demo bool) *bkEnv {
	ops, fn, data := demoSvc(t, version, demo)
	o.backupFn = fn
	e := newEnv(t, o)
	return &bkEnv{env: e, ops: ops, data: data}
}

func (e *env) create(role string, body any) (int, []byte, http.Header) {
	e.t.Helper()
	return e.raw("POST", "/api/backup", body, e.ck[role], nil)
}

func multipartBody(t *testing.T, fields map[string]string, file []byte, fileFirst bool) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	addFile := func() {
		if file == nil {
			return
		}
		fw, _ := w.CreateFormFile("file", "backup.tar.gz")
		fw.Write(file)
	}
	if fileFirst {
		addFile()
	}
	for k, v := range fields {
		w.WriteField(k, v)
	}
	if !fileFirst {
		addFile()
	}
	w.Close()
	return &b, w.FormDataContentType()
}

func (e *env) upload(role string, fields map[string]string, file []byte) (int, map[string]any) {
	e.t.Helper()
	body, ct := multipartBody(e.t, fields, file, false)
	return e.uploadRaw(role, body, ct)
}

func (e *env) uploadRaw(role string, body io.Reader, ct string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/restore/upload", body)
	req.Header.Set("X-nodespot", "1")
	req.Header.Set("Content-Type", ct)
	if c := e.ck[role]; c != "" {
		req.Header.Set("Cookie", auth.CookieName+"="+c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func code(m map[string]any) string { s, _ := m["code"].(string); return s }

func (e *bkEnv) backupBytes(role string) []byte {
	e.t.Helper()
	st, b, h := e.create(role, map[string]bool{"secrets": true})
	if st != 200 {
		e.t.Fatalf("create: %d %s", st, b)
	}
	if h.Get("Content-Type") != "application/gzip" || !strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="nodespot-backup-node-a-`) {
		e.t.Fatalf("headers %v", h)
	}
	return b
}

func TestBackupAuthMatrix(t *testing.T) {
	e := newBk(t, opts{}, "1.0.0", true)
	for _, role := range []string{"viewer", "op"} {
		for _, c := range []struct{ m, p string }{
			{"GET", "/api/backup/info"}, {"POST", "/api/backup"}, {"POST", "/api/restore/upload"},
			{"POST", "/api/restore/abc/apply"}, {"DELETE", "/api/restore/abc"},
		} {
			expectErr(t, e.env, role, c.m, c.p, map[string]string{}, 403, "forbidden")
		}
	}
	for _, c := range []struct{ m, p string }{
		{"GET", "/api/backup/info"}, {"POST", "/api/backup"}, {"POST", "/api/restore/upload"},
		{"POST", "/api/restore/abc/apply"}, {"DELETE", "/api/restore/abc"},
	} {
		expectErr(t, e.env, "", c.m, c.p, map[string]string{}, 401, "unauthorized")
	}
	// CSRF
	st, _, _ := e.raw("POST", "/api/backup", map[string]bool{}, e.ck["admin"], map[string]string{"X-nodespot": ""})
	if st != 403 {
		t.Fatalf("csrf: %d", st)
	}
	// the result needs a login once it is not fresh; none yet
	expectErr(t, e.env, "admin", "GET", "/api/restore/result", nil, 404, "not_found")
	// info
	var info struct {
		Node    bool `json:"node"`
		Secrets bool `json:"secrets"`
		SA818   bool `json:"sa818"`
	}
	if e.do("admin", "GET", "/api/backup/info", nil, &info) != 200 || !info.Node || !info.Secrets || !info.SA818 {
		t.Fatalf("info %+v", info)
	}
	// unavailable service
	e2 := newEnv(t, opts{})
	expectErr(t, e2, "admin", "GET", "/api/backup/info", nil, 503, "backup_unavailable")
}

func TestBackupRoundTripDemo(t *testing.T) {
	e := newBk(t, opts{}, "1.0.0", true)
	arc := e.backupBytes("admin")

	// wrong and missing password
	st, out := e.upload("admin", map[string]string{"password": "nope"}, arc)
	if st != 403 || code(out) != "bad_password" {
		t.Fatalf("bad password: %d %v", st, out)
	}
	st, out = e.upload("admin", nil, arc)
	if st != 403 || code(out) != "bad_password" {
		t.Fatalf("no password: %d %v", st, out)
	}
	// not a backup
	st, out = e.upload("admin", map[string]string{"password": "password-admin"}, []byte("this is not a backup"))
	if st != 400 || code(out) != "bad_backup" {
		t.Fatalf("garbage: %d %v", st, out)
	}
	// missing file
	st, out = e.upload("admin", map[string]string{"password": "password-admin"}, nil)
	if st != 400 {
		t.Fatalf("no file: %d %v", st, out)
	}

	st, out = e.upload("admin", map[string]string{"password": "password-admin"}, arc)
	if st != 200 {
		t.Fatalf("upload: %d %v", st, out)
	}
	id, _ := out["id"].(string)
	prev := out["preview"].(map[string]any)
	for _, k := range []string{"warnings", "users", "checklist", "modes", "missingSecrets"} {
		if _, ok := prev[k].([]any); !ok {
			t.Errorf("%s must be an array, got %v", k, prev[k])
		}
	}
	if prev["radio"] == nil || prev["radio"].(map[string]any)["bandwidth"] != "narrow" || prev["radio"].(map[string]any)["rxMHz"] != 146.52 {
		t.Errorf("radio: %v", prev["radio"])
	}
	if len(prev["users"].([]any)) != 3 {
		t.Errorf("users: %v", prev["users"])
	}
	parts := prev["parts"].(map[string]any)
	if parts["sa818"] != true || parts["node"] != true || parts["secrets"] != true || parts["conf"] != false {
		t.Errorf("parts: %v", parts)
	}
	if b := prev["backup"].(map[string]any); b["hostname"] != "node-a" || b["nodespotVersion"] != "1.0.0" || b["callsign"] != "N0CALL" {
		t.Errorf("backup: %v", b)
	}

	if m := prev["modes"].([]any); len(m) != 1 || m[0] != "allstar" {
		t.Errorf("modes: %v", m)
	}
	// a backup with passwords has no password warnings
	for _, w := range prev["warnings"].([]any) {
		if c := code(w.(map[string]any)); c == "no_secrets" || c == "missing_secrets" {
			t.Errorf("unexpected warning %v", w)
		}
	}

	// cancel and re-upload
	if e.do("admin", "DELETE", "/api/restore/"+id, nil, nil) != 204 {
		t.Fatal("cancel")
	}
	expectErr(t, e.env, "admin", "POST", "/api/restore/"+id+"/apply", map[string]any{}, 404, "not_found")
	_, out = e.upload("admin", map[string]string{"password": "password-admin"}, arc)
	id = out["id"].(string)

	var res struct {
		OK         bool
		Restarting bool
		Steps      []struct {
			Name string
			OK   bool
		}
	}
	if e.do("admin", "POST", "/api/restore/"+id+"/apply", map[string]any{}, &res) != 200 || !res.OK || res.Restarting || len(res.Steps) == 0 {
		t.Fatalf("apply: %+v", res)
	}
	// last result: readable without a login while fresh
	var r struct {
		At     time.Time
		Steps  []any
		Health struct {
			OK      bool
			Pending bool
			Checks  []any
		}
		Checklist []any
	}
	if e.do("", "GET", "/api/restore/result", nil, &r) != 200 || len(r.Steps) == 0 || !r.Health.OK || len(r.Checklist) == 0 {
		t.Fatalf("result: %+v", r)
	}

	// audit entries, no secrets
	aud, _ := e.st.ListAudit(50)
	seen := map[string]bool{}
	for _, a := range aud {
		seen[a.Action] = true
		if strings.Contains(a.Detail, "password") || strings.Contains(a.Detail, "nope") {
			t.Errorf("secret in audit: %v", a)
		}
	}
	if !seen["backup.create"] {
		t.Errorf("audit: %v", aud)
	}
}

func TestRestoreResultThirtyMinuteRule(t *testing.T) {
	e := newBk(t, opts{}, "1.0.0", true)
	old := map[string]any{"at": time.Now().Add(-31 * time.Minute).UTC(), "backupCreated": time.Now().UTC(), "steps": []any{},
		"health": map[string]any{"ok": true, "pending": false, "checks": []any{}}, "checklist": []any{}}
	write := func(at time.Time) {
		old["at"] = at.UTC()
		b, _ := json.Marshal(old)
		if err := os.WriteFile(filepath.Join(e.data, "restore-result.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Now().Add(-5 * time.Minute))
	if st := e.do("", "GET", "/api/restore/result", nil, nil); st != 200 {
		t.Fatalf("fresh anonymous: %d", st)
	}
	write(time.Now().Add(-31 * time.Minute))
	expectErr(t, e.env, "", "GET", "/api/restore/result", nil, 401, "unauthorized")
	expectErr(t, e.env, "op", "GET", "/api/restore/result", nil, 403, "forbidden")
	if st := e.do("admin", "GET", "/api/restore/result", nil, nil); st != 200 {
		t.Fatalf("old admin: %d", st)
	}
}

func TestRestoreSetupCodePath(t *testing.T) {
	// a source node makes the backup
	src := newBk(t, opts{}, "1.0.0", true)
	arc := src.backupBytes("admin")

	e := newBk(t, opts{noUsers: true}, "1.0.0", true)
	codeFile, err := e.as.EnsureSetupCode()
	if err != nil || codeFile == "" {
		t.Fatalf("setup code: %q %v", codeFile, err)
	}
	st, out := e.upload("", map[string]string{"code": "WRONGCODE1"}, arc)
	if st != 403 || code(out) != "bad_setup_code" {
		t.Fatalf("bad code: %d %v", st, out)
	}
	st, out = e.upload("", nil, arc)
	if st != 403 || code(out) != "bad_setup_code" {
		t.Fatalf("no code: %d %v", st, out)
	}
	// create needs a session even on a fresh node
	expectErr(t, e.env, "", "POST", "/api/backup", map[string]bool{}, 401, "unauthorized")
	st, out = e.upload("", map[string]string{"code": codeFile}, arc)
	if st != 200 {
		t.Fatalf("upload with code: %d %v", st, out)
	}
	id := out["id"].(string)
	// the code is also needed to apply
	expectErr(t, e.env, "", "POST", "/api/restore/"+id+"/apply", map[string]any{}, 403, "bad_setup_code")
	var res struct{ OK bool }
	if e.do("", "POST", "/api/restore/"+id+"/apply", map[string]any{"code": codeFile}, &res) != 200 || !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	// once a user exists the code path is closed
	if _, err := e.st.CreateUser("boss", testHash("admin"), "admin"); err != nil {
		t.Fatal(err)
	}
	st, out = e.upload("", map[string]string{"code": codeFile}, arc)
	if st != 401 {
		t.Fatalf("after setup: %d %v", st, out)
	}
}

func TestRestoreTooLargeAndTooNew(t *testing.T) {
	newer := newBk(t, opts{}, "9.0.0", true)
	arc := newer.backupBytes("admin")

	e := newBk(t, opts{}, "1.0.0", true)
	st, out := e.upload("admin", map[string]string{"password": "password-admin"}, arc)
	if st != 409 || code(out) != "backup_too_new" {
		t.Fatalf("too new: %d %v", st, out)
	}
	// an older or equal backup is fine
	older := newBk(t, opts{}, "0.9.0", true)
	if st, out := e.upload("admin", map[string]string{"password": "password-admin"}, older.backupBytes("admin")); st != 200 {
		t.Fatalf("older: %d %v", st, out)
	}

	if testing.Short() {
		t.Skip("large upload")
	}
	big := make([]byte, 256<<20+1)
	st, out = e.upload("admin", map[string]string{"password": "password-admin"}, big)
	if st != 413 || code(out) != "too_large" {
		t.Fatalf("too large: %d %v", st, out)
	}
}

// archRunner makes this node look like another architecture.
type archRunner struct{ next helper.Runner }

func (v *archRunner) Run(ctx context.Context, argv ...string) (string, int, error) {
	out, c, err := v.next.Run(ctx, argv...)
	if len(argv) > 0 && argv[0] == "dpkg" {
		return "arm64\n", c, err
	}
	return out, c, err
}

func TestRestoreVersionWarnings(t *testing.T) {
	src := newBk(t, opts{}, "1.0.0", true)
	arc := src.backupBytes("admin")
	e := newBk(t, opts{}, "1.0.0", true)
	e.ops.Run = &archRunner{next: e.ops.Run}
	st, out := e.upload("admin", map[string]string{"password": "password-admin"}, arc)
	if st != 200 {
		t.Fatalf("upload: %d %v", st, out)
	}
	have := map[string]bool{}
	for _, w := range out["preview"].(map[string]any)["warnings"].([]any) {
		have[w.(map[string]any)["code"].(string)] = true
	}
	if !have["arch"] {
		t.Errorf("warnings: %v preview %v", have, out["preview"].(map[string]any)["backup"])
	}
}
