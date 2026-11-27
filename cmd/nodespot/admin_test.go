package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/store"
)

type adminRig struct {
	t    *testing.T
	dir  string
	conf string
}

func newRig(t *testing.T) *adminRig {
	dir := t.TempDir()
	conf := filepath.Join(t.TempDir(), "nodespot.conf")
	if err := os.WriteFile(conf, []byte("[server]\ndatadir = "+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &adminRig{t: t, dir: dir, conf: conf}
}

// run executes "nodespot admin args... --config conf" with stdin as a non-tty.
func (r *adminRig) run(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runAdmin(append(args, "--config", r.conf), adminEnv{
		in: strings.NewReader(stdin), out: &out, errw: &errb, isTerm: func() bool { return false },
	})
	return code, out.String(), errb.String()
}

func (r *adminRig) store() *store.Store {
	st, err := store.Open(filepath.Join(r.dir, "nodespot.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { st.Close() })
	return st
}

func TestAdminCreateFirstUserRemovesSetupCode(t *testing.T) {
	r := newRig(t)
	// Let the service logic create the setup code first.
	st := r.store()
	as, _ := auth.NewService(st, r.dir)
	if code, err := as.EnsureSetupCode(); err != nil || code == "" {
		t.Fatal(code, err)
	}
	if need, _ := as.SetupNeeded(); !need {
		t.Fatal("setup should be needed")
	}
	if code, _, e := r.run("hunter2hunter2\nhunter2hunter2\n", "create"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "setup-code")); !os.IsNotExist(err) {
		t.Fatalf("setup-code still present: %v", err)
	}
	// The running service (same DB) now reports setup done.
	if need, _ := as.SetupNeeded(); need {
		t.Fatal("setup still needed after create")
	}
	u, err := st.UserByName("admin")
	if err != nil || u.Role != store.RoleAdmin {
		t.Fatal(u, err)
	}
	if ok, _ := auth.VerifyPassword("hunter2hunter2", u.PWHash); !ok {
		t.Fatal("password not stored")
	}
	if _, _, err := as.CompleteSetup("1.2.3.4", "X", "bob", "password123"); err != auth.ErrSetupDone {
		t.Fatalf("CompleteSetup after admin create: %v", err)
	}
}

func TestAdminCreateOptionsAndDuplicate(t *testing.T) {
	r := newRig(t)
	if code, out, e := r.run("longenough1\nlongenough1\n", "create", "--username", "Bob", "--role", "viewer"); code != 0 || !strings.Contains(out, "viewer") {
		t.Fatal(code, out, e)
	}
	code, _, e := r.run("longenough1\nlongenough1\n", "create", "--username", "bob")
	if code != 1 || !strings.Contains(e, "already exists") {
		t.Fatalf("duplicate: %d %q", code, e)
	}
	if code, _, _ := r.run("", "create", "--role", "root"); code != 2 {
		t.Fatalf("bad role exit %d", code)
	}
	if code, _, e := r.run("longenough1\nlongenough1\n", "create", "--username", "bad name"); code != 1 || !strings.Contains(e, "username") {
		t.Fatal(code, e)
	}
}

func TestAdminPasswordRules(t *testing.T) {
	r := newRig(t)
	for name, in := range map[string]string{
		"short":    "short\nshort\n",
		"mismatch": "longenough1\nlongenough2\n",
		"eof":      "longenough1\n",
		"empty":    "",
	} {
		if code, _, e := r.run(in, "create", "--username", "u"); code != 1 || e == "" {
			t.Fatalf("%s: exit %d %q", name, code, e)
		}
	}
	if n, _ := r.store().CountUsers(); n != 0 {
		t.Fatalf("%d users created despite failures", n)
	}
	// CRLF input is tolerated.
	if code, _, e := r.run("longenough1\r\nlongenough1\r\n", "create", "--username", "u"); code != 0 {
		t.Fatal(code, e)
	}
}

func TestAdminResetPassword(t *testing.T) {
	r := newRig(t)
	r.run("firstpassword\nfirstpassword\n", "create", "--username", "alice")
	st := r.store()
	u, _ := st.UserByName("alice")
	as, _ := auth.NewService(st, r.dir)
	var toks []string
	for i := 0; i < 2; i++ {
		tok, err := as.StartSession(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := as.Validate(tok); err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tok)
	}
	code, out, e := r.run("secondpassword\nsecondpassword\n", "reset-password", "ALICE")
	if code != 0 || !strings.Contains(out, "alice") {
		t.Fatal(code, out, e)
	}
	u2, _ := st.UserByName("alice")
	if u2.PWHash == u.PWHash {
		t.Fatal("hash unchanged")
	}
	if ok, _ := auth.VerifyPassword("secondpassword", u2.PWHash); !ok {
		t.Fatal("new password does not verify")
	}
	for _, tok := range toks {
		if _, err := as.Validate(tok); err == nil {
			t.Fatal("session survived password reset")
		}
	}
	if code, _, e := r.run("x\nx\n", "reset-password", "nobody"); code != 1 || !strings.Contains(e, "no such user") {
		t.Fatal(code, e)
	}
	if code, _, _ := r.run("", "reset-password"); code != 2 {
		t.Fatal("missing USER should be usage error")
	}
	// Password rules apply and a failed reset leaves the hash alone.
	if code, _, _ := r.run("short\nshort\n", "reset-password", "alice"); code != 1 {
		t.Fatal(code)
	}
	if u3, _ := st.UserByName("alice"); u3.PWHash != u2.PWHash {
		t.Fatal("hash changed on failed reset")
	}
}

func TestAdminList(t *testing.T) {
	r := newRig(t)
	if code, out, _ := r.run("", "list"); code != 0 || !strings.Contains(out, "No users") {
		t.Fatal(code, out)
	}
	if code, out, _ := r.run("", "list", "--quiet"); code != exitNoUsers || out != "" {
		t.Fatal(code, out)
	}
	r.run("longenough1\nlongenough1\n", "create", "--username", "alice")
	r.run("longenough1\nlongenough1\n", "create", "--username", "bob", "--role", "operator")
	code, out, _ := r.run("", "list")
	if code != 0 || !strings.Contains(out, "alice") || !strings.Contains(out, "operator") || !strings.Contains(out, "USERNAME") {
		t.Fatal(code, out)
	}
	if code, out, _ := r.run("", "list", "-q"); code != 0 || out != "" {
		t.Fatal(code, out)
	}
}

func TestAdminUsageAndErrors(t *testing.T) {
	r := newRig(t)
	if code, _, e := r.run("", "bogus"); code != 2 || !strings.Contains(e, "Usage") {
		t.Fatal(code, e)
	}
	if code, out, _ := r.run("", "--help"); code != 0 || !strings.Contains(out, "reset-password") {
		t.Fatal(code, out)
	}
	if code, out, _ := r.run("", "create", "-h"); code != 0 || !strings.Contains(out, "Usage") {
		t.Fatal(code, out)
	}
	if code, _, _ := r.run("", "list", "extra"); code != 2 {
		t.Fatal(code)
	}
	var out, errb bytes.Buffer
	if code := runAdmin(nil, adminEnv{in: strings.NewReader(""), out: &out, errw: &errb}); code != 2 {
		t.Fatal(code)
	}
	// Missing data directory is a clear error.
	missing := filepath.Join(t.TempDir(), "conf")
	os.WriteFile(missing, []byte("[server]\ndatadir = /nonexistent/nodespot\n"), 0o600)
	code := runAdmin([]string{"list", "--config", missing}, adminEnv{in: strings.NewReader(""), out: &out, errw: &errb})
	if code != 1 || !strings.Contains(errb.String(), "data directory") {
		t.Fatal(code, errb.String())
	}
}

func TestAdminReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	r := newRig(t)
	os.Chmod(r.dir, 0o500)
	defer os.Chmod(r.dir, 0o700)
	code, _, e := r.run("", "list")
	if code != 1 || !strings.Contains(e, "run as root") {
		t.Fatal(code, e)
	}
}

func TestSetupLink(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1")},
		&net.IPNet{IP: net.ParseIP("fe80::1")},
		&net.IPNet{IP: net.ParseIP("169.254.3.4")},
		&net.IPNet{IP: net.ParseIP("192.168.1.50")},
		&net.IPNet{IP: net.ParseIP("10.0.0.2")},
	}
	for listen, want := range map[string]string{
		":8080":          "http://192.168.1.50:8080/#/setup?code=ABC",
		"0.0.0.0:9000":   "http://192.168.1.50:9000/#/setup?code=ABC",
		"127.0.0.1:8081": "http://127.0.0.1:8081/#/setup?code=ABC",
		"10.1.1.1:8080":  "http://10.1.1.1:8080/#/setup?code=ABC",
		"garbage":        "http://192.168.1.50:8080/#/setup?code=ABC",
	} {
		if got := setupLink(listen, "ABC", addrs); got != want {
			t.Errorf("%s: %s want %s", listen, got, want)
		}
	}
	if got := setupLink(":8080", "ABC", nil); got != "http://localhost:8080/#/setup?code=ABC" {
		t.Fatal(got)
	}
}

func TestAdminSetupCode(t *testing.T) {
	r := newRig(t)
	st := r.store()
	as, _ := auth.NewService(st, r.dir)
	want, err := as.EnsureSetupCode()
	if err != nil || want == "" {
		t.Fatal(want, err)
	}
	code, out, e := r.run("", "setup-code")
	if code != 0 || strings.TrimSpace(out) != want || e != "" {
		t.Fatalf("exit %d out %q err %q want %q", code, out, e, want)
	}

	// Missing file: created like the server does.
	if err := os.Remove(filepath.Join(r.dir, "setup-code")); err != nil {
		t.Fatal(err)
	}
	code, out, _ = r.run("", "setup-code")
	got := strings.TrimSpace(out)
	if code != 0 || got == "" {
		t.Fatalf("exit %d out %q", code, out)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, "setup-code"))
	if err != nil || strings.TrimSpace(string(b)) != got {
		t.Fatalf("file %q err %v want %q", b, err, got)
	}

	// Accounts exist: error, exit 1, nothing on stdout.
	if c, _, e := r.run("hunter2hunter2\nhunter2hunter2\n", "create"); c != 0 {
		t.Fatal(c, e)
	}
	code, out, e = r.run("", "setup-code")
	if code != 1 || out != "" || !strings.Contains(e, "already complete") {
		t.Fatalf("exit %d out %q err %q", code, out, e)
	}
}
