package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/favstats"
)

const favFixture = `{"stats":{"node":2001,"data":{"apprptuptime":"1047840","totaltxtime":"358786","totalkeyups":"14834","keyed":false,"links":["2002","1998"]}},"node":{"Status":"Active","access_webtransceiver":"0"}}`

func favStatsEnv(t *testing.T) (*env, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, favFixture)
	}))
	t.Cleanup(ts.Close)
	e := newEnv(t, opts{favstats: favstats.New(favstats.Options{BaseURL: ts.URL, NoLimit: true})})
	return e, &hits
}

func TestFavStatsEndpointsRequireLogin(t *testing.T) {
	e, _ := favStatsEnv(t)
	if code := e.do("", "GET", "/api/favorites/stats", nil, nil); code != 401 {
		t.Fatalf("anonymous snapshot: %d", code)
	}
	if code := e.do("", "PUT", "/api/favorites/scan", map[string]any{"enabled": true}, nil); code != 401 {
		t.Fatalf("anonymous toggle: %d", code)
	}
	if code := e.do("viewer", "PUT", "/api/favorites/scan", map[string]any{"enabled": true}, nil); code != 403 {
		t.Fatalf("viewer toggle: %d", code)
	}
	var u favstats.Update
	if code := e.do("viewer", "GET", "/api/favorites/stats", nil, &u); code != 200 || u.Enabled || len(u.Stats) != 0 {
		t.Fatalf("viewer snapshot: %d %+v", code, u)
	}
}

func TestFavStatsOffMeansNoRequests(t *testing.T) {
	e, hits := favStatsEnv(t)
	var f struct{ ID int64 }
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "2001"}, &f)
	st := e.stream("viewer")
	defer st.cancel()
	time.Sleep(700 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("%d requests with the scan off", hits.Load())
	}
	var s struct {
		FavoritesScan bool `json:"favoritesScan"`
	}
	e.do("viewer", "GET", "/api/settings", nil, &s)
	if s.FavoritesScan {
		t.Fatal("scan on by default")
	}
}

func TestFavStatsScanFlow(t *testing.T) {
	e, hits := favStatsEnv(t)
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "2001"}, nil)
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "bm", "target": "91"}, nil)           // not AllStar: never queried
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": localNode}, nil) // this station: never queried

	// Nobody watching: switching on sends nothing yet.
	if code := e.do("op", "PUT", "/api/favorites/scan", map[string]any{"enabled": true}, nil); code != 200 {
		t.Fatal(code)
	}
	if code := e.do("op", "PUT", "/api/favorites/scan", map[string]any{}, nil); code != 400 {
		t.Fatalf("missing enabled: %d", code)
	}
	time.Sleep(500 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatal("polled without a viewer")
	}

	st := e.stream("viewer")
	defer st.cancel()
	ev := st.wait(func(ev sseEvent) bool {
		return ev.name == "favstats" && strings.Contains(ev.data, `"stats":{"2001"`)
	})
	var u favstats.Update
	if err := json.Unmarshal([]byte(ev.data), &u); err != nil {
		t.Fatal(err)
	}
	a := u.Stats["2001"]
	if !u.Enabled || a.LinkCnt != 2 || !a.Active || a.RxPct == nil || *a.RxPct != 34 {
		t.Fatalf("%+v", u)
	}
	if u.Checking != "" || u.Total != 1 || u.Checked != 1 || u.CheckedAt["2001"].IsZero() {
		t.Fatalf("progress: %+v", u)
	}
	// Snapshot and settings agree; the favorites list itself never carries activity.
	var snap favstats.Update
	e.do("viewer", "GET", "/api/favorites/stats", nil, &snap)
	if !snap.Enabled || len(snap.Stats) == 0 {
		t.Fatalf("%+v", snap)
	}
	_, b, _ := e.raw("GET", "/api/favorites", nil, e.ck["viewer"], nil)
	if strings.Contains(string(b), "rxPct") {
		t.Fatal("activity leaked into the favorites list")
	}
	// Public page data never mentions the scan.
	e.do("admin", "PUT", "/api/public", map[string]any{"enabled": true}, nil)
	_, pb, _ := e.raw("GET", "/api/public/status", nil, "", nil)
	if strings.Contains(string(pb), "rxPct") || strings.Contains(string(pb), "favstats") {
		t.Fatal("scan data on the public page")
	}
	// Off again: values are dropped and an event says so.
	e.do("op", "PUT", "/api/favorites/scan", map[string]any{"enabled": false}, nil)
	st.wait(func(ev sseEvent) bool { return ev.name == "favstats" && strings.Contains(ev.data, `"enabled":false`) })
	snap = favstats.Update{}
	e.do("viewer", "GET", "/api/favorites/stats", nil, &snap)
	if snap.Enabled || len(snap.Stats) != 0 {
		t.Fatalf("%+v", snap)
	}
}
