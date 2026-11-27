package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
	"github.com/w1jar/nodespot/internal/updates"
)

type fakeOS struct {
	mu       sync.Mutex
	st       helper.OSStatus
	checkErr error
	applyErr error
	checks   int
	applies  int
	reboots  int
	restarts int
}

func (f *fakeOS) OSCheck(context.Context) (helper.OSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.st, f.checkErr
}
func (f *fakeOS) OSStatus(context.Context) (helper.OSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return helper.OSStatus{Job: f.st.Job, JobLog: f.st.JobLog, RebootRequired: f.st.RebootRequired}, nil
}
func (f *fakeOS) OSApply(context.Context) (helper.OSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return helper.OSStatus{}, f.applyErr
	}
	f.applies++
	f.st.Job = helper.JobRunning
	return f.st, nil
}
func (f *fakeOS) Reboot(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.Job == helper.JobRunning {
		return helper.ErrBusy
	}
	f.reboots++
	return nil
}
func (f *fakeOS) RestartNodespot(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	return nil
}

func (f *fakeOS) set(fn func(*helper.OSStatus)) {
	f.mu.Lock()
	fn(&f.st)
	f.mu.Unlock()
}

func osEnv(t *testing.T, fo *fakeOS) *env {
	e, _ := osEnvOM(t, fo)
	return e
}

func osEnvOM(t *testing.T, fo *fakeOS) (*env, *updates.OSManager) {
	t.Helper()
	var om *updates.OSManager
	e := newEnv(t, opts{power: fo, updatesFn: func(st *store.Store) *updates.Manager {
		m := updates.New(&fakeHelper{st: helper.Status{Installed: "0.2.0", Job: "idle"}}, "0.2.0", func(context.Context, string, bool) (updates.Notes, error) {
			return updates.Notes{Version: "0.3.0"}, nil
		})
		m.OS = updates.NewOS(fo, st)
		m.OS.Tick = 20 * time.Millisecond
		om = m.OS
		return m
	}})
	return e, om
}

type osInfoResp struct {
	OS struct {
		Available bool    `json:"available"`
		CheckedAt *string `json:"checkedAt"`
		Checking  bool    `json:"checking"`
		Error     *string `json:"error"`
		Packages  []struct {
			Name     string `json:"name"`
			From     string `json:"from"`
			To       string `json:"to"`
			Security bool   `json:"security"`
		} `json:"packages"`
		KeptBack       []string `json:"keptBack"`
		RebootRequired bool     `json:"rebootRequired"`
		Job            string   `json:"job"`
	} `json:"os"`
}

func sampleOS() helper.OSStatus {
	return helper.OSStatus{
		Packages: []helper.OSPackage{{Name: "libssl3", From: "1", To: "2", Security: true}, {Name: "asterisk", From: "1", To: "2"}},
		KeptBack: []string{"linux-image-arm64"}, Job: helper.JobIdle,
	}
}

func waitOS(t *testing.T, e *env, pred func(osInfoResp) bool) osInfoResp {
	t.Helper()
	for i := 0; i < 200; i++ {
		var in osInfoResp
		if c := e.do("admin", "GET", "/api/updates", nil, &in); c != 200 {
			t.Fatalf("get: %d", c)
		}
		if pred(in) {
			return in
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
	return osInfoResp{}
}

func TestOSUpdatesAdminOnly(t *testing.T) {
	fo := &fakeOS{st: sampleOS()}
	e := osEnv(t, fo)
	for _, role := range []string{"op", "viewer"} {
		expectErr(t, e, role, "GET", "/api/updates", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/updates/os/check", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/updates/os/apply", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/system/reboot", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/system/restart-nodespot", nil, 403, "forbidden")
	}
	if fo.checks+fo.applies+fo.reboots+fo.restarts != 0 {
		t.Fatalf("helper reached by a non-admin: %+v", fo)
	}
}

func TestOSCheckApplyRebootAudit(t *testing.T) {
	fo := &fakeOS{st: sampleOS()}
	e := osEnv(t, fo)
	var in osInfoResp
	if c := e.do("admin", "GET", "/api/updates", nil, &in); c != 200 || !in.OS.Available || len(in.OS.Packages) != 0 || in.OS.CheckedAt != nil {
		t.Fatalf("initial: %d %+v", c, in)
	}
	if c := e.do("admin", "POST", "/api/updates/os/check", nil, &in); c != 202 {
		t.Fatalf("check: %d", c)
	}
	in = waitOS(t, e, func(r osInfoResp) bool { return r.OS.CheckedAt != nil && !r.OS.Checking })
	if len(in.OS.Packages) != 2 || !in.OS.Packages[0].Security && !in.OS.Packages[1].Security || len(in.OS.KeptBack) != 1 {
		t.Fatalf("%+v", in)
	}
	// apply: 202, running, audited with package names
	if c := e.do("admin", "POST", "/api/updates/os/apply", nil, &in); c != 202 || in.OS.Job != "running" {
		t.Fatalf("apply: %d %+v", c, in)
	}
	// a second apply or check while it runs: 409
	fo.set(func(s *helper.OSStatus) { s.Job = helper.JobRunning })
	fo.applyErr = helper.ErrBusy
	expectErr(t, e, "admin", "POST", "/api/updates/os/apply", nil, 409, "update_running")
	expectErr(t, e, "admin", "POST", "/api/updates/os/check", nil, 409, "update_running")
	expectErr(t, e, "admin", "POST", "/api/system/reboot", nil, 409, "update_running")
	fo.applyErr = nil
	// it finishes: the server re-checks by itself
	checks := fo.checks
	fo.set(func(s *helper.OSStatus) { s.Job = helper.JobSucceeded; s.Packages = nil; s.RebootRequired = true })
	waitOS(t, e, func(r osInfoResp) bool { return len(r.OS.Packages) == 0 && r.OS.RebootRequired && !r.OS.Checking })
	if fo.checks <= checks {
		t.Fatal("no automatic re-check after the upgrade")
	}
	// reboot and nodespot restart
	if c := e.do("admin", "POST", "/api/system/reboot", nil, nil); c != 202 || fo.reboots != 1 {
		t.Fatalf("reboot: %d", c)
	}
	if c := e.do("admin", "POST", "/api/system/restart-nodespot", nil, nil); c != 202 || fo.restarts != 1 {
		t.Fatalf("restart: %d", c)
	}
	var actions []string
	aud, _ := e.st.ListAudit(50)
	for _, a := range aud {
		actions = append(actions, a.Action)
		if a.Action == "updates.os.apply" && (!strings.Contains(a.Detail, "libssl3") || a.Username != "admin") {
			t.Errorf("apply audit: %+v", a)
		}
	}
	got := strings.Join(actions, ",")
	for _, w := range []string{"updates.os.apply", "system.reboot", "nodespot.restart"} {
		if !strings.Contains(got, w) {
			t.Errorf("audit lacks %s: %s", w, got)
		}
	}
}

func TestOSCheckErrors(t *testing.T) {
	fo := &fakeOS{st: sampleOS(), checkErr: helper.ErrAptBusy}
	e := osEnv(t, fo)
	if c := e.do("admin", "POST", "/api/updates/os/check", nil, nil); c != 202 {
		t.Fatalf("%d", c)
	}
	in := waitOS(t, e, func(r osInfoResp) bool { return r.OS.Error != nil })
	if !strings.Contains(*in.OS.Error, "apt is busy") || in.OS.CheckedAt != nil {
		t.Fatalf("%+v", in)
	}
}

func TestPowerUnavailable(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "POST", "/api/system/reboot", nil, 503, "updates_unavailable")
	expectErr(t, e, "admin", "POST", "/api/system/restart-nodespot", nil, 503, "updates_unavailable")
	expectErr(t, e, "admin", "POST", "/api/updates/os/check", nil, 503, "updates_unavailable")
}

func TestSSEUpdatesBadgeAdminOnly(t *testing.T) {
	fo := &fakeOS{st: sampleOS()}
	e, om := osEnvOM(t, fo)
	// the check result is in place before the streams open
	if err := om.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	adm, op, vw := e.stream("admin"), e.stream("op"), e.stream("viewer")
	ev := adm.wait(func(ev sseEvent) bool { return ev.name == "updates" })
	var b struct {
		OS       int  `json:"os"`
		Security int  `json:"security"`
		Reboot   bool `json:"reboot"`
	}
	if err := json.Unmarshal([]byte(ev.data), &b); err != nil || b.OS != 2 || b.Security != 1 || b.Reboot {
		t.Fatalf("%s %v", ev.data, err)
	}
	if strings.Contains(ev.data, "libssl3") {
		t.Fatalf("package names in the badge: %s", ev.data)
	}
	// a change is pushed to admins; others get nothing, ever
	fo.set(func(s *helper.OSStatus) { s.Packages = s.Packages[:1] })
	if err := om.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	adm.wait(func(ev sseEvent) bool { return ev.name == "updates" && strings.Contains(ev.data, `"os":1`) })
	for _, s := range []*sseStream{op, vw} {
		s.first() // hello
		s.first() // nodes
		deadline := time.After(300 * time.Millisecond)
	loop:
		for {
			select {
			case ev := <-s.events:
				if ev.name == "updates" {
					t.Fatalf("badge sent to a non-admin: %s", ev.data)
				}
			case <-deadline:
				break loop
			}
		}
	}
}
