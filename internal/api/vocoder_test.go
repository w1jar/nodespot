package api_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
)

// vocoderSys is a fakeSys that also installs the vocoder: it records the
// calls and answers from st.
type vocoderSys struct {
	*fakeSys
	vmu        sync.Mutex
	st         helper.VocoderStatus
	installErr error
	installs   int
}

func (v *vocoderSys) VocoderStatus(context.Context) (helper.VocoderStatus, error) {
	v.vmu.Lock()
	defer v.vmu.Unlock()
	return v.st, nil
}

func (v *vocoderSys) VocoderInstall(context.Context) (helper.VocoderStatus, error) {
	v.vmu.Lock()
	defer v.vmu.Unlock()
	if v.installErr != nil {
		return helper.VocoderStatus{}, v.installErr
	}
	v.installs++
	if !(v.st.Installed && v.st.Active) {
		v.st.Job = "running"
	}
	return v.st, nil
}

type vocoderResp struct {
	Installed bool     `json:"installed"`
	Active    bool     `json:"active"`
	Needed    []string `json:"needed"`
	Supported bool     `json:"supported"`
	Reason    string   `json:"reason"`
	Job       string   `json:"job"`
}

func newVocoderEnv(t *testing.T, st helper.VocoderStatus) (*env, *vocoderSys) {
	t.Helper()
	vs := &vocoderSys{fakeSys: newFakeSys(), st: st}
	return newEnv(t, opts{sysHost: vs}), vs
}

func TestVocoderGet(t *testing.T) {
	e, _ := newVocoderEnv(t, helper.VocoderStatus{Supported: true})
	var got vocoderResp
	if c := e.do("admin", "GET", "/api/vocoder", nil, &got); c != 200 {
		t.Fatalf("status %d", c)
	}
	if got.Installed || got.Active || !got.Supported {
		t.Fatalf("%+v", got)
	}
	// the AMBE modes of the node, and only those
	sort.Strings(got.Needed)
	want := "bm,dmr,nxdn,tgif,ysf" // the echo test only with its AMBE codec
	if strings.Join(got.Needed, ",") != want {
		t.Fatalf("needed %v, want %s", got.Needed, want)
	}
}

func TestVocoderGetInstalled(t *testing.T) {
	e, _ := newVocoderEnv(t, helper.VocoderStatus{Supported: true, Installed: true, Active: true})
	var got vocoderResp
	if c := e.do("admin", "GET", "/api/vocoder", nil, &got); c != 200 || !got.Installed || !got.Active {
		t.Fatalf("%d %+v", c, got)
	}
}

func TestVocoderGetNoNode(t *testing.T) {
	vs := &vocoderSys{fakeSys: newFakeSys(), st: helper.VocoderStatus{Supported: true}}
	e := newEnv(t, opts{sysHost: vs, noNode: true})
	var got vocoderResp
	if c := e.do("admin", "GET", "/api/vocoder", nil, &got); c != 200 || got.Needed == nil || len(got.Needed) != 0 {
		t.Fatalf("%d %+v", c, got)
	}
}

func TestVocoderInstall(t *testing.T) {
	e, vs := newVocoderEnv(t, helper.VocoderStatus{Supported: true})
	var got vocoderResp
	if c := e.do("admin", "POST", "/api/vocoder/install", nil, &got); c != 202 || got.Job != "running" {
		t.Fatalf("%d %+v", c, got)
	}
	if vs.installs != 1 {
		t.Fatalf("installs %d", vs.installs)
	}
	aud, _ := e.st.ListAudit(5)
	if len(aud) == 0 || aud[0].Action != "vocoder.install" {
		t.Fatalf("audit %+v", aud)
	}
}

func TestVocoderInstallAlreadyInstalled(t *testing.T) {
	e, _ := newVocoderEnv(t, helper.VocoderStatus{Supported: true, Installed: true, Active: true})
	var got vocoderResp
	if c := e.do("admin", "POST", "/api/vocoder/install", nil, &got); c != 200 || got.Job != "" || !got.Active {
		t.Fatalf("%d %+v", c, got)
	}
}

func TestVocoderInstallErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{"unsupported", helper.ErrVocoderUnsupported, 409, "not_supported"},
		{"clock", helper.ErrVocoderClockUnset, 409, "clock_unset"},
		{"key", helper.ErrVocoderKeyMismatch, 502, "key_mismatch"},
		{"busy", helper.ErrBusy, 409, "update_running"},
		{"apt busy", helper.ErrAptBusy, 409, "apt_busy"},
		{"no helper", &helper.UnavailableError{Reason: "helper not running", Err: errors.New("dial")}, 503, "system_unavailable"},
		{"other", errors.New("systemd-run failed"), 502, "helper_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, vs := newVocoderEnv(t, helper.VocoderStatus{Supported: true})
			vs.installErr = tc.err
			expectErr(t, e, "admin", "POST", "/api/vocoder/install", nil, tc.code, tc.want)
		})
	}
}

func TestVocoderAdminOnly(t *testing.T) {
	e, vs := newVocoderEnv(t, helper.VocoderStatus{Supported: true})
	for _, role := range []string{"op", "viewer"} {
		expectErr(t, e, role, "GET", "/api/vocoder", nil, 403, "forbidden")
		expectErr(t, e, role, "POST", "/api/vocoder/install", nil, 403, "forbidden")
	}
	if vs.installs != 0 {
		t.Fatal("a non-admin started the install")
	}
}

func TestVocoderUnavailable(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "GET", "/api/vocoder", nil, 503, "system_unavailable")
	expectErr(t, e, "admin", "POST", "/api/vocoder/install", nil, 503, "system_unavailable")
	// a System helper without the vocoder ops is unavailable too
	e = newEnv(t, opts{sysHost: newFakeSys()})
	expectErr(t, e, "admin", "GET", "/api/vocoder", nil, 503, "system_unavailable")
}
