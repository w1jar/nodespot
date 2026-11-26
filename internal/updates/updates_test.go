package updates

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
)

type fh struct {
	st      helper.Status
	err     error
	applies int
	setErr  error
}

func (f *fh) SetChannel(_ context.Context, ch string) (helper.Status, error) {
	if f.setErr != nil {
		return helper.Status{}, f.setErr
	}
	f.st.Channel = ch
	return f.st, nil
}

func (f *fh) UpdateStatus(context.Context) (helper.Status, error) { return f.st, f.err }
func (f *fh) UpdateApply(context.Context) (helper.Status, error) {
	f.applies++
	return f.st, f.err
}

func latestServer(t *testing.T, body string, hits *atomic.Int32, queries *[]string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if queries != nil {
			*queries = append(*queries, r.URL.RawQuery)
		}
		if r.URL.Path != "/latest.json" && r.URL.Path != "/latest-beta.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestIsPackageVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"0.2.1": true, "0.2.1-1": true, "1:0.3": true, "dev": false, "": false,
		"v0.1.0-4-g1a2b3c4": false, "0.1.0-4-g1a2b3c4": false, "0.0.0~git1a2b3c4": false, "v0.2.0": false, "0.2.0-dirty": false,
	} {
		if IsPackageVersion(v) != want {
			t.Errorf("IsPackageVersion(%q) = %v", v, !want)
		}
	}
}

func TestHTTPLatestCases(t *testing.T) {
	ctx := context.Background()
	ok := latestServer(t, `{"version":"0.3.0","notes":"<b>x</b>","date":"2026-11-15"}`, nil, nil)
	n, err := HTTPLatest(ok.URL+"/", nil)(ctx, "stable", false)
	if err != nil || n.Version != "0.3.0" || n.Notes != "<b>x</b>" {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := HTTPLatest(ok.URL+"/sub", nil)(ctx, "stable", false); err == nil {
		t.Fatal("404 must error")
	}
	if HTTPLatest("", nil) != nil {
		t.Fatal("empty url must disable checks")
	}
	// garbage
	g := latestServer(t, `<html>not json`, nil, nil)
	if _, err := HTTPLatest(g.URL, nil)(ctx, "stable", false); err == nil {
		t.Fatal("garbage must error")
	}
	// oversize (> 64 KB) is refused, not truncated
	big := latestServer(t, `{"version":"1","notes":"`+strings.Repeat("a", 70<<10)+`"}`, nil, nil)
	if _, err := HTTPLatest(big.URL, nil)(ctx, "stable", false); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize: %v", err)
	}
	// long notes are truncated
	long := latestServer(t, `{"version":"1","notes":"`+strings.Repeat("a", 20000)+`"}`, nil, nil)
	n, _ = HTTPLatest(long.URL, nil)(ctx, "stable", false)
	if len([]rune(n.Notes)) > maxNotes+1 {
		t.Fatalf("notes not truncated: %d", len(n.Notes))
	}
	// timeout: server never answers; the client's own timeout fires
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	start := time.Now()
	_, err = HTTPLatest(slow.URL, &http.Client{Timeout: 100 * time.Millisecond})(ctx, "stable", false)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(start))
	}
}

func TestCacheBusterOnlyForManualCheck(t *testing.T) {
	var hits atomic.Int32
	var q []string
	ts := latestServer(t, `{"version":"0.3.0"}`, &hits, &q)
	f := HTTPLatest(ts.URL, nil)
	f(context.Background(), "stable", false)
	f(context.Background(), "stable", true)
	if len(q) != 2 || q[0] != "" || !strings.HasPrefix(q[1], "t=") || len(q[1]) < 10 {
		t.Fatalf("queries: %q", q)
	}
	// Manager.Check is the manual check: buster; Run is the background check: none
	q = nil
	m := New(&fh{st: helper.Status{Installed: "0.2.0", Job: "idle"}}, "0.2.0", f)
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.FirstCheck = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if len(q) < 2 || !strings.HasPrefix(q[0], "t=") || q[1] != "" {
		t.Fatalf("queries: %q", q)
	}
}

func checkWith(t *testing.T, installed, body string) (Info, error) {
	t.Helper()
	ts := latestServer(t, body, nil, nil)
	m := New(&fh{st: helper.Status{Installed: installed, Job: "idle"}}, installed, HTTPLatest(ts.URL, nil))
	return m.Check(context.Background())
}

func TestCheckComparison(t *testing.T) {
	in, err := checkWith(t, "0.2.1", `{"version":"0.2.10","notes":"- hi"}`)
	if err != nil || !in.Upgradable || in.Candidate != "0.2.10" || in.Notes == nil || *in.Notes != "- hi" || in.Checked == nil {
		t.Fatalf("newer: %+v %v", in, err)
	}
	in, err = checkWith(t, "0.2.1", `{"version":"0.2.1","notes":"x"}`)
	if err != nil || in.Upgradable || in.Notes != nil || in.Checked == nil {
		t.Fatalf("same: %+v %v", in, err)
	}
	in, err = checkWith(t, "0.2.1", `{"version":"0.2.0"}`)
	if err != nil || in.Upgradable {
		t.Fatalf("older: %+v %v", in, err)
	}
	for _, body := range []string{`garbage`, `{"version":"not a version"}`, `{"notes":"x"}`, `{"version":""}`} {
		if _, err := checkWith(t, "0.2.1", body); err == nil {
			t.Errorf("%q must fail", body)
		} else {
			var ce *CheckError
			if !errors.As(err, &ce) {
				t.Errorf("%q: %T", body, err)
			}
		}
	}
	if _, err := checkWith(t, "0.2.1", `{"version":"0.2.1","pad":"`+strings.Repeat("a", 70<<10)+`"}`); err == nil {
		t.Error("oversize must fail")
	}
}

func TestFailedCheckKeepsState(t *testing.T) {
	var fail atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", 500)
			return
		}
		w.Write([]byte(`{"version":"0.3.0"}`))
	}))
	defer ts.Close()
	m := New(&fh{st: helper.Status{Job: "idle"}}, "0.2.0", HTTPLatest(ts.URL, nil))
	if ok, _ := m.Pending(); ok {
		t.Fatal("pending before any check")
	}
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if ok, v := m.Pending(); !ok || v != "0.3.0" {
		t.Fatalf("%v %v", ok, v)
	}
	if in := m.Info(context.Background()); in.Checked == nil || !in.Upgradable {
		t.Fatalf("%+v", in)
	}
}

func TestInfoNeverFetches(t *testing.T) {
	var hits atomic.Int32
	ts := latestServer(t, `{"version":"0.3.0"}`, &hits, nil)
	m := New(&fh{st: helper.Status{Installed: "0.2.0", Job: "idle"}}, "0.2.0", HTTPLatest(ts.URL, nil))
	for i := 0; i < 3; i++ {
		in := m.Info(context.Background())
		if !in.Available || in.Installed != "0.2.0" || in.Upgradable || in.Checked != nil {
			t.Fatalf("%+v", in)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("Info must not touch the network")
	}
}

func TestHelperUnreachableStillChecks(t *testing.T) {
	ts := latestServer(t, `{"version":"0.3.0"}`, nil, nil)
	h := &fh{err: &helper.UnavailableError{Reason: "helper not running", Err: errors.New("x")}}
	m := New(h, "0.2.0", HTTPLatest(ts.URL, nil))
	in, err := m.Check(context.Background())
	if err != nil || !in.Available || !in.Upgradable || in.ApplyReason == nil || *in.ApplyReason != "helper not running" || in.Job != "idle" {
		t.Fatalf("%+v %v", in, err)
	}
	if ok, v := m.Pending(); !ok || v != "0.3.0" {
		t.Fatal("pending")
	}
}

func TestNotAPackageBuild(t *testing.T) {
	ts := latestServer(t, `{"version":"0.3.0"}`, nil, nil)
	// dev binary + helper without the package: not installed from a package
	m := New(&fh{st: helper.Status{Job: "idle"}}, "dev", HTTPLatest(ts.URL, nil))
	if in := m.Info(context.Background()); in.Available || in.Reason == nil || *in.Reason != "not installed from a package" {
		t.Fatalf("%+v", in)
	}
	// dev binary but dpkg knows the package: installed comes from the helper
	m = New(&fh{st: helper.Status{Installed: "0.2.0", Job: "idle"}}, "v0.1.0-4-g1a2b3c4", HTTPLatest(ts.URL, nil))
	in, err := m.Check(context.Background())
	if err != nil || !in.Available || in.Installed != "0.2.0" || !in.Upgradable {
		t.Fatalf("%+v %v", in, err)
	}
	// no helper, no package build
	if _, err := New(nil, "dev", nil).Check(context.Background()); err != ErrUnavailable {
		t.Fatal(err)
	}
	if _, err := New(nil, "dev", nil).Apply(context.Background()); err != ErrUnavailable {
		t.Fatal(err)
	}
	// package build without update URL
	if _, err := New(&fh{}, "0.2.0", nil).Check(context.Background()); !errors.Is(err, ErrNoSource) {
		t.Fatal(err)
	}
}

func TestDpkgNewerThanBinary(t *testing.T) {
	ts := latestServer(t, `{"version":"0.3.0"}`, nil, nil)
	// upgrade finished, restart pending: not upgradable any more
	m := New(&fh{st: helper.Status{Installed: "0.3.0", Job: "succeeded"}}, "0.2.0", HTTPLatest(ts.URL, nil))
	in, _ := m.Check(context.Background())
	if in.Installed != "0.3.0" || in.Upgradable || in.Job != "succeeded" {
		t.Fatalf("%+v", in)
	}
}

func TestApplyBusyAndNoop(t *testing.T) {
	h := &fh{st: helper.Status{Installed: "0.2.0", Job: "running"}}
	m := New(h, "0.2.0", nil)
	in, err := m.Apply(context.Background())
	if err != nil || in.Job != "running" || h.applies != 1 {
		t.Fatalf("%+v %v", in, err)
	}
	h.err = helper.ErrBusy
	if _, err := m.Apply(context.Background()); err != helper.ErrBusy {
		t.Fatal(err)
	}
	h.err = nil
	h.st.Job = helper.JobNoop
	if in := m.Info(context.Background()); in.Job != "noop" {
		t.Fatalf("%+v", in)
	}
}

func TestDemoHelper(t *testing.T) {
	d := NewDemoHelper("dev")
	d.RunTime = 50 * time.Millisecond
	ctx := context.Background()
	m := New(d, "dev", d.DemoFetch())
	in, err := m.Check(ctx)
	if err != nil || !in.Upgradable || in.Job != helper.JobIdle {
		t.Fatalf("%+v %v", in, err)
	}
	if in, _ = m.Apply(ctx); in.Job != helper.JobRunning {
		t.Fatalf("%+v", in)
	}
	if _, err := d.UpdateApply(ctx); err != helper.ErrBusy {
		t.Fatalf("concurrent apply: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if in = m.Info(ctx); in.Job != helper.JobSucceeded || in.Upgradable {
		t.Fatalf("%+v", in)
	}
}

func TestIsPackageVersionBeta(t *testing.T) {
	for v, want := range map[string]bool{
		"2.0.0~beta.1": true, "2.0.0~beta.12": true, "1.0.1": true, "2.0.0~rc1-1": true,
		"2.0.0-beta.1":            true, // valid Debian syntax (revision "beta.1")
		"2.0.0~beta.1~git1a2b3c4": false,
	} {
		if IsPackageVersion(v) != want {
			t.Errorf("IsPackageVersion(%q) = %v", v, !want)
		}
	}
}

func TestChannelFilesAndSwitch(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/latest.json":
			w.Write([]byte(`{"version":"1.0.1","notes":"stable notes"}`))
		case "/latest-beta.json":
			w.Write([]byte(`{"version":"2.0.0~beta.2","notes":"beta notes"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	h := &fh{st: helper.Status{Installed: "1.0.0", Job: "idle"}} // channel unknown: stable
	m := New(h, "1.0.0", HTTPLatest(ts.URL, nil))
	in, err := m.Check(ctx)
	if err != nil || in.Channel != ChannelStable || in.Candidate != "1.0.1" || !in.Upgradable {
		t.Fatalf("%+v %v", in, err)
	}
	if ok, v := m.Pending(); !ok || v != "1.0.1" {
		t.Fatalf("%v %v", ok, v)
	}
	// switching: cached stable result is dropped, a fresh beta check runs
	paths = nil
	in, err = m.SetChannel(ctx, ChannelBeta)
	if err != nil || in.Channel != ChannelBeta || in.Candidate != "2.0.0~beta.2" || !in.Upgradable || in.Notes == nil || *in.Notes != "beta notes" {
		t.Fatalf("%+v %v", in, err)
	}
	if len(paths) != 1 || paths[0] != "/latest-beta.json" {
		t.Fatalf("paths %v", paths)
	}
	// a channel changed behind our back (apt line edited) is noticed by Info
	h.st.Channel = ChannelStable
	if in := m.Info(ctx); in.Channel != ChannelStable || in.Candidate != "" || in.Upgradable {
		t.Fatalf("stale candidate after external switch: %+v", in)
	}
	if ok, _ := m.Pending(); ok {
		t.Fatal("pending must not use another channel's result")
	}
	// invalid and refused
	if _, err := m.SetChannel(ctx, "alpha"); err == nil {
		t.Fatal("invalid channel")
	}
	h.setErr = helper.ErrBusy
	if _, err := m.SetChannel(ctx, ChannelBeta); !errors.Is(err, helper.ErrBusy) {
		t.Fatal(err)
	}
	if _, err := New(nil, "1.0.0", nil).SetChannel(ctx, ChannelBeta); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestDemoChannels(t *testing.T) {
	d := NewDemoHelper("dev")
	d.RunTime = 20 * time.Millisecond
	ctx := context.Background()
	m := New(d, "dev", d.DemoFetch())
	in, _ := m.Check(ctx)
	if in.Channel != ChannelStable || in.Candidate != "99.0.0" {
		t.Fatalf("%+v", in)
	}
	in, err := m.SetChannel(ctx, ChannelBeta)
	if err != nil || in.Channel != ChannelBeta || in.Candidate != "99.1.0~beta.1" || !in.Upgradable {
		t.Fatalf("%+v %v", in, err)
	}
	m.Apply(ctx)
	if _, err := m.SetChannel(ctx, ChannelStable); !errors.Is(err, helper.ErrBusy) {
		t.Fatalf("switch while running: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if in = m.Info(ctx); in.Installed != "99.1.0~beta.1" || in.Upgradable {
		t.Fatalf("%+v", in)
	}
	// back to stable: the installed beta is newer, nothing to offer
	in, err = m.SetChannel(ctx, ChannelStable)
	if err != nil || in.Channel != ChannelStable || in.Candidate != "99.0.0" || in.Upgradable {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestArmhfInstallCannotApply(t *testing.T) {
	ts := latestServer(t, `{"version":"0.2.10"}`, nil, nil)
	for goarch, wantReason := range map[string]bool{"arm": true, "arm64": false, "amd64": false} {
		m := New(&fh{st: helper.Status{Installed: "0.2.1", Job: "idle"}}, "0.2.1", HTTPLatest(ts.URL, nil))
		m.GOARCH = goarch
		in, err := m.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := in.ApplyReason != nil; got != wantReason {
			t.Errorf("%s: applyReason = %v", goarch, in.ApplyReason)
		}
		if wantReason && (!strings.Contains(*in.ApplyReason, "64-bit") || !in.Available) {
			t.Errorf("%s: %+v", goarch, in)
		}
	}
}
