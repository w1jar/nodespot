package api_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/hostlists"
)

// baseSetting is a Settings that points the downloader at a test server.
type baseSetting string

func (b baseSetting) GetSetting(k string) (string, error) {
	if k == hostlists.SettingBaseURL {
		return string(b), nil
	}
	return "", nil
}

const (
	dmrFile = "# test\nBM_3104_United_States 192.0.2.10 passw0rd 62031\nBM_2001_Test_Land 192.0.2.11 passw0rd 62031\nTGIF_Network 192.0.2.12 passw0rd 62031\nOther_Master 192.0.2.13 secret 55555\n"
	ysfFile = "00001;Test Room;Test;192.0.2.20;42000;000\n"
)

func hostListsEnv(t *testing.T, fail bool) *env {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case fail:
			http.Error(w, "gone", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "DMR_Hosts.txt"):
			w.Write([]byte(dmrFile))
		case strings.HasSuffix(r.URL.Path, "YSF_Hosts.txt"):
			w.Write([]byte(ysfFile))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	hl := hostlists.New(hostlists.Options{Dir: filepath.Join(t.TempDir(), "hostlists"), Settings: baseSetting(ts.URL)})
	return newEnv(t, opts{hostlists: hl})
}

type statusDoc struct {
	Busy       bool                            `json:"busy"`
	NextManual string                          `json:"nextManual"`
	Lists      map[string]hostlists.ListStatus `json:"lists"`
	Nodelist   *struct {
		Count int     `json:"count"`
		Error *string `json:"error"`
	} `json:"nodelist"`
}

func TestHostListsStatusAndRefresh(t *testing.T) {
	e := hostListsEnv(t, false)

	var st statusDoc
	if c := e.do("op", "GET", "/api/hostlists/status", nil, &st); c != 200 {
		t.Fatalf("status: %d", c)
	}
	if len(st.Lists) != 5 || st.Lists["dmr"].Entries != 0 || st.Nodelist == nil || st.Nodelist.Count != 4 {
		t.Fatalf("status before: %+v nodelist %+v", st, st.Nodelist)
	}

	// The DMR list is not there yet: the pickers get 503.
	if c, code := e.errCode("op", "GET", "/api/hostlists?mode=bm", nil); c != 503 || code != "hostlists_unavailable" {
		t.Fatalf("bm before refresh: %d %q", c, code)
	}

	// Refresh: lists that download are counted, the others report their error,
	// and the node list (no URL in the test) reports its own.
	if c := e.do("admin", "POST", "/api/hostlists/refresh", nil, &st); c != 200 {
		t.Fatalf("refresh: %d", c)
	}
	if st.Lists["dmr"].Entries != 4 || st.Lists["ysf"].Entries != 1 || st.Lists["dmr"].Updated == "" || st.Lists["p25"].Error == "" {
		t.Fatalf("status after: %+v", st.Lists)
	}
	if st.NextManual == "" || st.Nodelist == nil || st.Nodelist.Error == nil || !strings.Contains(*st.Nodelist.Error, "URL") {
		t.Fatalf("next manual %q nodelist %+v", st.NextManual, st.Nodelist)
	}

	// The second manual refresh is refused with the reason.
	var er struct{ Error, Code string }
	if c := e.do("admin", "POST", "/api/hostlists/refresh", nil, &er); c != 409 || !strings.Contains(er.Error, "refreshed a moment ago") {
		t.Fatalf("second refresh: %d %+v", c, er)
	}

	// The BrandMeister and other-master pickers.
	var bm, other hostlists.List
	if c := e.do("op", "GET", "/api/hostlists?mode=bm", nil, &bm); c != 200 || len(bm.Items) != 2 || bm.Items[0].Name != "2001 – Test Land" || bm.Items[1].Value != "192.0.2.10" || bm.Items[1].Port != 0 {
		t.Fatalf("bm: %d %+v", c, bm)
	}
	if c := e.do("op", "GET", "/api/hostlists?mode=dmr-other", nil, &other); c != 200 || len(other.Items) != 1 || other.Items[0].Value != "192.0.2.13" || other.Items[0].Port != 55555 || other.Items[0].Password != "secret" {
		t.Fatalf("other: %d %+v", c, other)
	}
}

func TestHostListsRefreshFailureIsNotARefusal(t *testing.T) {
	e := hostListsEnv(t, true)
	var st statusDoc
	if c := e.do("admin", "POST", "/api/hostlists/refresh", nil, &st); c != 200 {
		t.Fatalf("refresh: %d", c)
	}
	if st.Lists["dmr"].Error == "" || !strings.Contains(st.Lists["dmr"].Error, "HTTP 500") {
		t.Fatalf("status: %+v", st.Lists)
	}
}

func TestHostListsRoles(t *testing.T) {
	e := hostListsEnv(t, false)
	expectErr(t, e, "viewer", "GET", "/api/hostlists/status", nil, 403, "forbidden")
	expectErr(t, e, "op", "POST", "/api/hostlists/refresh", nil, 403, "forbidden")
	expectErr(t, e, "viewer", "POST", "/api/hostlists/refresh", nil, 403, "forbidden")
	expectErr(t, e, "", "GET", "/api/hostlists/status", nil, 401, "unauthorized")
}

func TestHostListsStatusWithoutManager(t *testing.T) {
	e := newEnv(t, opts{})
	var st statusDoc
	if c := e.do("op", "GET", "/api/hostlists/status", nil, &st); c != 200 || st.Lists == nil || len(st.Lists) != 0 {
		t.Fatalf("status: %d %+v", c, st)
	}
	expectErr(t, e, "admin", "POST", "/api/hostlists/refresh", nil, 503, "hostlists_unavailable")
}
