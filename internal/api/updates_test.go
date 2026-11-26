package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/updates"
)

type fakeHelper struct {
	st      helper.Status
	err     error
	applies int
	chans   []string
}

func (f *fakeHelper) SetChannel(_ context.Context, ch string) (helper.Status, error) {
	if f.err != nil {
		return helper.Status{}, f.err
	}
	f.chans = append(f.chans, ch)
	f.st.Channel = ch
	return f.st, nil
}

func (f *fakeHelper) UpdateStatus(context.Context) (helper.Status, error) { return f.st, f.err }
func (f *fakeHelper) UpdateApply(context.Context) (helper.Status, error) {
	f.applies++
	if f.err != nil {
		return helper.Status{}, f.err
	}
	st := f.st
	st.Job = helper.JobRunning
	return st, nil
}

type updInfo struct {
	Available  bool    `json:"available"`
	Reason     *string `json:"reason"`
	Channel    string  `json:"channel"`
	Installed  string  `json:"installed"`
	Candidate  string  `json:"candidate"`
	Upgradable bool    `json:"upgradable"`
	Job        string  `json:"job"`
	Notes      *string `json:"notes"`
	Checked    *string `json:"checked"`
}

func TestUpdatesAdminOnly(t *testing.T) {
	fh := &fakeHelper{st: helper.Status{Installed: "0.2.0", Candidate: "0.3.0", Upgradable: true, Job: "idle"}}
	e := newEnv(t, opts{updates: updates.New(fh, "0.2.0", func(context.Context, string, bool) (updates.Notes, error) {
		return updates.Notes{Version: "0.3.0", Notes: "- better things"}, nil
	})})
	for _, role := range []string{"op", "viewer"} {
		expectErr(t, e, role, "GET", "/api/updates", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/updates/check", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/updates/apply", nil, 403, "forbidden")
		expectErr(t, e, role, "PUT", "/api/updates/channel", map[string]string{"channel": "beta"}, 403, "forbidden")
	}
	if fh.applies != 0 {
		t.Fatal("apply reached helper for non-admin")
	}
	var in updInfo
	if c := e.do("admin", "GET", "/api/updates", nil, &in); c != 200 {
		t.Fatalf("get: %d", c)
	}
	// nothing checked yet: GET is cached state only
	if !in.Available || in.Installed != "0.2.0" || in.Candidate != "" || in.Upgradable || in.Notes != nil || in.Checked != nil {
		t.Fatalf("%+v", in)
	}
	in = updInfo{}
	if c := e.do("admin", "POST", "/api/updates/check", nil, &in); c != 200 || in.Checked == nil || in.Candidate != "0.3.0" || !in.Upgradable || in.Notes == nil || *in.Notes != "- better things" {
		t.Fatalf("check: %d %+v", c, in)
	}
	in = updInfo{}
	if c := e.do("admin", "POST", "/api/updates/apply", nil, &in); c != 202 || in.Job != "running" {
		t.Fatalf("apply: %d %+v", c, in)
	}
	// the cached flag shows up in /api/system
	var sys struct {
		UpdateAvailable bool   `json:"updateAvailable"`
		UpdateVersion   string `json:"updateVersion"`
	}
	if c := e.do("viewer", "GET", "/api/system", nil, &sys); c != 200 || !sys.UpdateAvailable || sys.UpdateVersion != "0.3.0" {
		t.Fatalf("system: %d %+v", c, sys)
	}
}

func TestUpdatesUnavailable(t *testing.T) {
	// no manager at all
	e := newEnv(t, opts{})
	var in updInfo
	if c := e.do("admin", "GET", "/api/updates", nil, &in); c != 200 || in.Available || in.Reason == nil {
		t.Fatalf("%d %+v", c, in)
	}
	expectErr(t, e, "admin", "POST", "/api/updates/check", nil, 503, "updates_unavailable")
	expectErr(t, e, "admin", "POST", "/api/updates/apply", nil, 503, "updates_unavailable")

	// helper socket missing
	m := updates.New(helper.NewClient(t.TempDir()+"/none.sock"), "dev", nil)
	e = newEnv(t, opts{updates: m})
	in = updInfo{}
	if c := e.do("admin", "GET", "/api/updates", nil, &in); c != 200 || in.Available || in.Reason == nil || *in.Reason != "not installed from a package" {
		t.Fatalf("%d %+v %v", c, in, in.Reason)
	}
	expectErr(t, e, "admin", "POST", "/api/updates/apply", nil, 503, "updates_unavailable")
}

func TestUpdatesBusyAndHelperError(t *testing.T) {
	fh := &fakeHelper{st: helper.Status{Installed: "0.2.0"}, err: helper.ErrBusy}
	e := newEnv(t, opts{updates: updates.New(fh, "0.2.0", func(context.Context, string, bool) (updates.Notes, error) {
		return updates.Notes{}, errors.New("boom")
	})})
	expectErr(t, e, "admin", "POST", "/api/updates/apply", nil, 409, "update_running")
	// a failing latest.json fetch is not a helper error
	expectErr(t, e, "admin", "POST", "/api/updates/check", nil, 502, "check_failed")
	fh.err = context.DeadlineExceeded
	expectErr(t, e, "admin", "POST", "/api/updates/apply", nil, 502, "helper_error")
}

func TestUpdatesChannel(t *testing.T) {
	fh := &fakeHelper{st: helper.Status{Installed: "1.0.0", Channel: "stable", Job: "idle"}}
	var asked []string
	e := newEnv(t, opts{updates: updates.New(fh, "1.0.0", func(_ context.Context, ch string, _ bool) (updates.Notes, error) {
		asked = append(asked, ch)
		if ch == "beta" {
			return updates.Notes{Version: "2.0.0~beta.1"}, nil
		}
		return updates.Notes{Version: "1.0.1"}, nil
	})})
	var in updInfo
	if c := e.do("admin", "GET", "/api/updates", nil, &in); c != 200 || in.Channel != "stable" {
		t.Fatalf("%d %+v", c, in)
	}
	for _, bad := range []any{map[string]string{"channel": "Beta"}, map[string]string{"channel": ""}, map[string]string{"channel": "alpha"}, map[string]string{}} {
		expectErr(t, e, "admin", "PUT", "/api/updates/channel", bad, 400, "bad_request")
	}
	if len(fh.chans) != 0 {
		t.Fatalf("helper reached for invalid channel: %v", fh.chans)
	}
	in = updInfo{}
	if c := e.do("admin", "PUT", "/api/updates/channel", map[string]string{"channel": "beta"}, &in); c != 200 || in.Channel != "beta" || in.Candidate != "2.0.0~beta.1" || !in.Upgradable {
		t.Fatalf("%d %+v", c, in)
	}
	if len(fh.chans) != 1 || fh.chans[0] != "beta" || len(asked) != 1 || asked[0] != "beta" {
		t.Fatalf("%v %v", fh.chans, asked)
	}
	fh.err = helper.ErrBusy
	expectErr(t, e, "admin", "PUT", "/api/updates/channel", map[string]string{"channel": "stable"}, 409, "update_running")
	// no manager
	e = newEnv(t, opts{})
	expectErr(t, e, "admin", "PUT", "/api/updates/channel", map[string]string{"channel": "beta"}, 503, "updates_unavailable")
}
