package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/api"
	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/backupsvc"
	"github.com/w1jar/nodespot/internal/config"
	"github.com/w1jar/nodespot/internal/diag"
	"github.com/w1jar/nodespot/internal/favstats"
	"github.com/w1jar/nodespot/internal/hostlists"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/node/nodetest"
	"github.com/w1jar/nodespot/internal/nodelist"
	"github.com/w1jar/nodespot/internal/nowcalling"
	"github.com/w1jar/nodespot/internal/schedules"
	"github.com/w1jar/nodespot/internal/store"
	"github.com/w1jar/nodespot/internal/system"
	"github.com/w1jar/nodespot/internal/updates"
)

// localNode is the test station's AllStar node number.
const localNode = "1997"

type env struct {
	t    *testing.T
	node *nodetest.Rig // nil with opts.noNode
	dir  string        // the node's socket and config
	st   *store.Store
	as   *auth.Service
	srv  *api.Server
	ts   *httptest.Server
	nl   *nodelist.DB
	ck   map[string]string // role -> cookie header
	clk  *fakeClock        // with opts.schedules
	eng  *schedules.Engine // with opts.schedules
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type opts struct {
	noUsers   bool
	dev       bool
	updates   *updates.Manager
	updatesFn func(st *store.Store) *updates.Manager
	power     api.Power
	sysHost   api.SystemHost
	hostlists *hostlists.Manager
	log       *diag.Ring
	cfg       func() config.Config
	backupFn  func(st *store.Store) *backupsvc.Service
	restart   func()
	favstats  *favstats.Service
	// schedules builds a schedule engine on a fake clock (env.clk), in New
	// York time; the clock starts on Tuesday 2026-11-17 18:59:50.
	schedules bool
	// noNode leaves Deps.Node nil; nodeCfg replaces the default node config.
	noNode  bool
	nodeCfg *node.Config
}

// testNodeConfig is a station with AllStar and BrandMeister set up, the ID
// and the tones off so the fake radio stays quiet.
func testNodeConfig() *node.Config {
	off := false
	return &node.Config{
		Version: node.ConfigVersion,
		Station: node.Station{
			Callsign: "N0CALL",
			ID:       node.IDConfig{Enabled: &off},
			Advanced: node.Advanced{ModeAGC: &off, RadioAGC: &off},
		},
		Modes: map[string]mode.Settings{
			mode.AllStar: {Values: map[string]string{"node": localNode, "password": "test-node-secret"}},
			mode.BM:      {Values: map[string]string{"dmr_id": "1234567", "password": "test-bm-secret", "address": "master.example.net"}},
		},
	}
}

// startNode starts (or restarts) the fake node on the env's socket.
func (e *env) startNode(cfg *node.Config) {
	e.t.Helper()
	if cfg == nil {
		cfg = testNodeConfig()
	}
	rig, err := nodetest.Start(cfg, nodetest.Options{Dir: e.dir})
	if err != nil {
		e.t.Fatal(err)
	}
	e.node = rig
}

// stopNode stops the fake node (the API keeps running).
func (e *env) stopNode() {
	if e.node != nil {
		e.node.Close()
		e.node = nil
	}
}

// fake returns the fake mode n, waiting for the engine to build it.
func (e *env) fake(n string) *modetest.Fake {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f := e.node.Fake(n); f != nil {
			return f
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("mode %s was never built", n)
	return nil
}

// nodeJSON reads GET /api/node as the admin.
func (e *env) nodeJSON() api.NodeJSON {
	e.t.Helper()
	var n api.NodeJSON
	if c := e.do("admin", "GET", "/api/node", nil, &n); c != 200 {
		e.t.Fatalf("GET /api/node: %d", c)
	}
	return n
}

// waitNode waits until GET /api/node satisfies pred.
func (e *env) waitNode(what string, pred func(api.NodeJSON) bool) api.NodeJSON {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := e.nodeJSON()
		if pred(n) {
			return n
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for the node: %s (%+v)", what, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newEnv(t *testing.T, o opts) *env {
	t.Helper()
	quiet := log.New(io.Discard, "", 0)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	as, err := auth.NewService(st, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	as.Limiter.Max = 1000 // tests log in often; rate limit has its own test

	// A Unix socket path must be short, so not t.TempDir.
	dir, err := os.MkdirTemp("", "nodespot-at")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := &env{t: t, dir: dir, st: st, as: as, ck: map[string]string{}}
	if !o.noNode {
		e.startNode(o.nodeCfg)
		t.Cleanup(e.stopNode)
	}

	nl := nodelist.New(nodelist.Options{Logger: quiet})
	nl.Set([]nodelist.Info{
		{Node: "1998", Callsign: "N1CALL", Description: "Metro Hub", Location: "Springfield, ST"},
		{Node: "2000", Callsign: "N2CALL", Description: "Reflector", Location: "Nowhere, XX"},
		{Node: localNode, Callsign: "N0CALL", Description: "Home node", Location: "Home, ST"},
		{Node: "1999", Callsign: "N3CALL", Description: "Test Hub", Location: "Metropolis, ST"},
	}, "test", time.Now())
	e.nl = nl

	var origins []string
	if o.dev {
		origins = []string{"http://localhost:5173"}
	}
	var nd api.Node
	if !o.noNode {
		nd = node.NewClient(filepath.Join(dir, "control.sock"))
	}
	deps := api.Deps{
		Auth: as, Store: st, Node: nd, Nodelist: nl, System: system.New("test", api.SystemNode(nd)), Updates: o.updates, Version: "test",
		AllowedOrigins: origins, Logger: quiet,
		Log: o.log, PlatformPaths: &diag.PlatformPaths{DeviceModel: "/nonexistent"},
		Power: o.power, SysHost: o.sysHost, HostLists: o.hostlists, FavStats: o.favstats,
	}
	if o.updatesFn != nil {
		deps.Updates = o.updatesFn(st)
	}
	if o.backupFn != nil {
		deps.Backup = o.backupFn(st)
		deps.Restart = o.restart
	}
	if o.schedules {
		ny, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatal(err)
		}
		e.clk = &fakeClock{t: time.Date(2026, 11, 17, 18, 59, 50, 0, ny)}
		e.eng = &schedules.Engine{Store: st, Now: e.clk.now, Loc: ny, Zone: "America/New_York", Tick: time.Hour}
		deps.Schedules = e.eng
	}
	if o.cfg != nil {
		deps.Config = o.cfg()
	}
	srv := api.New(deps)
	srv.PingInterval = 100 * time.Millisecond
	srv.SystemInterval = 200 * time.Millisecond
	srv.NodeRetry = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		cancel()
		srv.Shutdown()
		ts.Close()
	})
	e.srv, e.ts = srv, ts

	if !o.noUsers {
		for _, u := range []struct{ name, role string }{{"admin", "admin"}, {"op", "operator"}, {"viewer", "viewer"}} {
			if _, err := st.CreateUser(u.name, testHash(u.name), u.role); err != nil {
				t.Fatal(err)
			}
		}
		e.session("admin")
		e.session("op")
		e.session("viewer")
		if !o.noNode {
			e.waitNode("running", func(n api.NodeJSON) bool { return n.Running })
		}
	}
	return e
}

func (e *env) session(name string) {
	e.t.Helper()
	u, err := e.st.UserByName(name)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := e.as.StartSession(u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	e.ck[name] = tok
}

var (
	hashMu    sync.Mutex
	hashCache = map[string]string{}
)

// testHash returns a cached argon2id hash of "password-<name>".
func testHash(name string) string {
	hashMu.Lock()
	defer hashMu.Unlock()
	if h, ok := hashCache[name]; ok {
		return h
	}
	h, err := auth.HashPassword("password-" + name)
	if err != nil {
		panic(err)
	}
	hashCache[name] = h
	return h
}

func (e *env) raw(method, path string, body any, cookie string, extra map[string]string) (int, []byte, http.Header) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if method != "GET" {
		req.Header.Set("X-nodespot", "1")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", auth.CookieName+"="+cookie)
	}
	for k, v := range extra {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// do performs a request as role ("" = anonymous) and decodes JSON into out.
func (e *env) do(role, method, path string, body any, out any) int {
	e.t.Helper()
	code, b, _ := e.raw(method, path, body, e.ck[role], nil)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: bad JSON %q: %v", method, path, b, err)
		}
	}
	return code
}

func (e *env) errCode(role, method, path string, body any) (int, string) {
	e.t.Helper()
	var er struct{ Error, Code string }
	code := e.do(role, method, path, body, &er)
	return code, er.Code
}

func expectErr(t *testing.T, e *env, role, method, path string, body any, wantStatus int, wantCode string) {
	t.Helper()
	code, c := e.errCode(role, method, path, body)
	if code != wantStatus || c != wantCode {
		t.Fatalf("%s %s as %q: got %d/%q want %d/%q", method, path, role, code, c, wantStatus, wantCode)
	}
}

// ------------------------------------------------------------------ tests

func TestSetupAndSession(t *testing.T) {
	e := newEnv(t, opts{noUsers: true})
	var need struct{ Needed bool }
	if e.do("", "GET", "/api/setup", nil, &need) != 200 || !need.Needed {
		t.Fatal("setup should be needed")
	}
	expectErr(t, e, "", "GET", "/api/node", nil, 401, "unauthorized")
	code, err := e.as.EnsureSetupCode()
	if err != nil || code == "" {
		t.Fatal(err)
	}
	expectErr(t, e, "", "POST", "/api/setup", map[string]string{"code": "WRONGWRONG", "username": "boss", "password": "longpassword"}, 403, "bad_setup_code")
	expectErr(t, e, "", "POST", "/api/setup", map[string]string{"code": code, "username": "boss", "password": "short"}, 400, "bad_request")
	st, _, hdr := e.raw("POST", "/api/setup", map[string]string{"code": code, "username": "boss", "password": "longpassword"}, "", nil)
	if st != 204 {
		t.Fatalf("setup: %d", st)
	}
	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie: %+v", cookie)
	}
	e.ck["boss"] = cookie.Value
	var u api.UserJSON
	if e.do("boss", "GET", "/api/session", nil, &u) != 200 || u.Username != "boss" || u.Role != "admin" {
		t.Fatalf("session: %+v", u)
	}
	if e.do("", "GET", "/api/setup", nil, &need) != 200 || need.Needed {
		t.Fatal("setup still needed")
	}
	expectErr(t, e, "", "POST", "/api/setup", map[string]string{"code": code, "username": "x2", "password": "longpassword"}, 409, "setup_done")
	// logout invalidates the session server-side
	if c := e.do("boss", "POST", "/api/logout", nil, nil); c != 204 {
		t.Fatal(c)
	}
	expectErr(t, e, "boss", "GET", "/api/session", nil, 401, "unauthorized")
	expectErr(t, e, "", "POST", "/api/login", map[string]string{"username": "boss", "password": "nope"}, 401, "bad_credentials")
	if c := e.do("", "POST", "/api/login", map[string]string{"username": "BOSS", "password": "longpassword"}, &u); c != 200 || u.Username != "boss" {
		t.Fatal(c, u)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, opts{})
	e.as.Limiter.Max = 3
	for i := 0; i < 3; i++ {
		expectErr(t, e, "", "POST", "/api/login", map[string]string{"username": "op", "password": "bad"}, 401, "bad_credentials")
	}
	code, _, hdr := e.raw("POST", "/api/login", map[string]string{"username": "op", "password": "password-op"}, "", nil)
	if code != 429 || hdr.Get("Retry-After") == "" {
		t.Fatalf("got %d retry-after=%q", code, hdr.Get("Retry-After"))
	}
	expectErr(t, e, "", "POST", "/api/login", map[string]string{"username": "viewer", "password": "bad"}, 401, "bad_credentials") // other user unaffected
}

func TestCSRFAndOrigin(t *testing.T) {
	e := newEnv(t, opts{})
	body := map[string]string{"username": "op", "password": "password-op"}
	code, b, _ := e.raw("POST", "/api/login", body, "", map[string]string{"X-nodespot": ""})
	if code != 403 || !strings.Contains(string(b), `"csrf"`) {
		t.Fatalf("missing header: %d %s", code, b)
	}
	code, _, _ = e.raw("POST", "/api/login", body, "", map[string]string{"Origin": "http://evil.example"})
	if code != 403 {
		t.Fatalf("cross-origin: %d", code)
	}
	code, _, _ = e.raw("POST", "/api/login", body, "", map[string]string{"Origin": e.ts.URL})
	if code != 200 {
		t.Fatalf("same-origin: %d", code)
	}
	code, _, _ = e.raw("POST", "/api/login", body, "", map[string]string{"Origin": "http://localhost:5173"})
	if code != 403 {
		t.Fatalf("vite origin without --dev: %d", code)
	}
	// authenticated state-changing request without the header is rejected too
	code, _, _ = e.raw("POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, e.ck["op"], map[string]string{"X-nodespot": ""})
	if code != 403 {
		t.Fatalf("authed no header: %d", code)
	}
	if n := len(e.fake(mode.AllStar).Connects()); n != 0 {
		t.Fatalf("select ran despite CSRF rejection (%d)", n)
	}

	d := newEnv(t, opts{dev: true})
	code, _, _ = d.raw("POST", "/api/login", map[string]string{"username": "op", "password": "password-op"}, "", map[string]string{"Origin": "http://localhost:5173"})
	if code != 200 {
		t.Fatalf("vite origin with --dev: %d", code)
	}
}

func TestRoles(t *testing.T) {
	e := newEnv(t, opts{})
	sel := map[string]string{"mode": "allstar", "target": "2000"}
	forbidden := []struct{ role, method, path string }{
		{"viewer", "POST", "/api/select"},
		{"viewer", "POST", "/api/connect"},
		{"viewer", "POST", "/api/disconnect"},
		{"viewer", "POST", "/api/favorites"},
		{"viewer", "GET", "/api/hostlists?mode=ysf"},
		{"viewer", "GET", "/api/users"},
		{"op", "GET", "/api/users"},
		{"op", "POST", "/api/nodelist/refresh"},
		{"op", "PUT", "/api/modes/allstar"},
		{"op", "DELETE", "/api/modes/allstar"},
		{"op", "GET", "/api/station"},
		{"op", "PUT", "/api/station"},
		{"op", "GET", "/api/radio"},
		{"op", "PUT", "/api/radio"},
		{"op", "GET", "/api/radio/info"},
		{"op", "POST", "/api/radio/program"},
		{"op", "POST", "/api/radio/tone"},
		{"op", "DELETE", "/api/radio/tone"},
		{"op", "POST", "/api/node/reload"},
	}
	for _, f := range forbidden {
		expectErr(t, e, f.role, f.method, f.path, sel, 403, "forbidden")
	}
	if len(e.fake(mode.AllStar).Connects()) != 0 {
		t.Fatal("a forbidden request reached the node")
	}
	if c := e.do("op", "POST", "/api/select", sel, nil); c != 200 {
		t.Fatalf("operator select: %d", c)
	}
	if c := e.do("admin", "POST", "/api/nodelist/refresh", nil, nil); c != 202 {
		t.Fatalf("admin refresh: %d", c)
	}
	for _, r := range []string{"viewer", "op", "admin"} {
		for _, p := range []string{"/api/node", "/api/modes", "/api/heard", "/api/favorites", "/api/system", "/api/nodelist", "/api/lookup/2000", "/api/lookup?q=n0", "/api/nowcalling"} {
			if c := e.do(r, "GET", p, nil, nil); c != 200 {
				t.Errorf("%s GET %s: %d", r, p, c)
			}
		}
	}
	expectErr(t, e, "admin", "GET", "/api/nope", nil, 404, "not_found")
}

func TestNodeStatus(t *testing.T) {
	e := newEnv(t, opts{})
	n := e.nodeJSON()
	if !n.Running || n.Error != "" || n.SetupNeeded || !n.Configured || n.Callsign != "N0CALL" || n.Active != "" || n.Started.IsZero() {
		t.Fatalf("%+v", n)
	}
	_, raw, _ := e.raw("GET", "/api/node", nil, e.ck["viewer"], nil)
	for _, k := range []string{`"running"`, `"setupNeeded"`, `"callsign"`, `"radio"`, `"configured"`, `"id"`} {
		if !bytes.Contains(raw, []byte(k)) {
			t.Errorf("missing %s in %s", k, raw)
		}
	}
	var setup struct{ Needed, Station bool }
	if e.do("", "GET", "/api/setup", nil, &setup); setup.Needed || setup.Station {
		t.Fatalf("setup: %+v", setup)
	}
	if c := e.do("admin", "POST", "/api/node/reload", nil, nil); c != 200 {
		t.Fatalf("reload: %d", c)
	}
}

func TestStationSetupFlag(t *testing.T) {
	cfg := testNodeConfig()
	cfg.Station.Callsign = ""
	e := newEnv(t, opts{nodeCfg: cfg})
	if n := e.nodeJSON(); !n.SetupNeeded || n.Configured {
		t.Fatalf("a station without a callsign needs setup: %+v", n)
	}
	var setup struct{ Station bool }
	if e.do("", "GET", "/api/setup", nil, &setup); !setup.Station {
		t.Fatal("GET /api/setup: station setup not flagged")
	}
	var stn node.Station
	if c := e.do("admin", "GET", "/api/station", nil, &stn); c != 200 || stn.Callsign != "" {
		t.Fatal(c, stn)
	}
	stn.Callsign = " n1call "
	var n api.NodeJSON
	if c := e.do("admin", "PUT", "/api/station", stn, &n); c != 200 || n.Callsign != "N1CALL" || n.SetupNeeded {
		t.Fatal(c, n)
	}
	e.waitNode("configured", func(n api.NodeJSON) bool { return !n.SetupNeeded })
	if e.do("admin", "GET", "/api/station", nil, &stn); stn.Callsign != "N1CALL" {
		t.Fatalf("%+v", stn)
	}
}

func TestStationDigitalIDs(t *testing.T) {
	cfg := testNodeConfig()
	delete(cfg.Modes[mode.BM].Values, "dmr_id")
	cfg.Station.DMRID = 1234567
	e := newEnv(t, opts{nodeCfg: cfg})
	var stn node.Station
	if c := e.do("admin", "GET", "/api/station", nil, &stn); c != 200 || stn.DMRID != 1234567 || stn.NXDNID != 0 {
		t.Fatal(c, stn)
	}
	stn.NXDNID = 4321
	if c := e.do("admin", "PUT", "/api/station", stn, nil); c != 200 {
		t.Fatalf("PUT station: %d", c)
	}
	var got node.Station
	if e.do("admin", "GET", "/api/station", nil, &got); got.DMRID != 1234567 || got.NXDNID != 4321 {
		t.Fatalf("%+v", got)
	}
	stn.NXDNID = 70000
	expectErr(t, e, "admin", "PUT", "/api/station", stn, 409, "node_refused")
	// BrandMeister has no DMR ID of its own: it builds with the station's.
	e.fake(mode.BM)
}

func modeByName(l []node.ModeInfo, n string) *node.ModeInfo {
	for i := range l {
		if l[i].Name == n {
			return &l[i]
		}
	}
	return nil
}

func TestModesSettingsAndSecrets(t *testing.T) {
	e := newEnv(t, opts{})
	var l []node.ModeInfo
	if c := e.do("admin", "GET", "/api/modes", nil, &l); c != 200 {
		t.Fatal(c)
	}
	for _, n := range []string{mode.AllStar, mode.EchoLink, mode.BM, mode.TGIF, mode.YSF, mode.P25, mode.NXDN, mode.M17} {
		if modeByName(l, n) == nil {
			t.Errorf("mode %s missing", n)
		}
	}
	as := modeByName(l, mode.AllStar)
	if !as.Configured || len(as.Fields) == 0 || as.Settings.Values["node"] != localNode || !as.SecretsSet["password"] {
		t.Fatalf("allstar: %+v", as)
	}
	if as.Settings.Values["password"] != "" {
		t.Fatal("the password was sent back")
	}
	_, raw, _ := e.raw("GET", "/api/modes", nil, e.ck["admin"], nil)
	if bytes.Contains(raw, []byte("test-node-secret")) {
		t.Fatal("GET /api/modes leaks a password")
	}
	// viewers see the modes but not their settings
	var vl []node.ModeInfo
	e.do("viewer", "GET", "/api/modes", nil, &vl)
	if v := modeByName(vl, mode.AllStar); v == nil || !v.Configured || len(v.Settings.Values) != 0 || v.SecretsSet != nil || v.Fields != nil {
		t.Fatalf("viewer: %+v", v)
	}

	// passwords are write-only: an empty one keeps the saved one
	put := mode.Settings{Values: map[string]string{"callsign": "N0CALL-L", "password": "echo-pass"}}
	if c := e.do("admin", "PUT", "/api/modes/echolink", put, &l); c != 200 {
		t.Fatal(c)
	}
	el := modeByName(l, mode.EchoLink)
	if el == nil || !el.Configured || !el.SecretsSet["password"] || el.Settings.Values["password"] != "" || el.Settings.Values["callsign"] != "N0CALL-L" {
		t.Fatalf("echolink: %+v", el)
	}
	put = mode.Settings{Values: map[string]string{"callsign": "N1CALL-L", "password": ""}}
	e.do("admin", "PUT", "/api/modes/echolink", put, &l)
	if el = modeByName(l, mode.EchoLink); !el.SecretsSet["password"] || el.Settings.Values["callsign"] != "N1CALL-L" {
		t.Fatalf("empty password should keep the saved one: %+v", el)
	}
	cfg, err := os.ReadFile(e.node.ConfigPath)
	if err != nil || !bytes.Contains(cfg, []byte("echo-pass")) {
		t.Fatalf("the node config should keep the password (%v)", err)
	}
	// clear removes only the named password
	clr := mode.Settings{Clear: []string{"password"}}
	var l2 []node.ModeInfo
	if c := e.do("admin", "PUT", "/api/modes/echolink", clr, &l2); c != 200 {
		t.Fatal(c)
	}
	if el = modeByName(l2, mode.EchoLink); el.Configured || el.SecretsSet["password"] || el.Settings.Values["callsign"] != "N1CALL-L" {
		t.Fatalf("clear: %+v", el)
	}
	expectErr(t, e, "admin", "PUT", "/api/modes/echolink", mode.Settings{Clear: []string{"callsign"}}, 409, "node_refused")
	expectErr(t, e, "admin", "PUT", "/api/modes/Bad%20Mode", put, 400, "bad_mode")
	expectErr(t, e, "admin", "PUT", "/api/modes/nosuchmode", put, 409, "node_refused")

	if c := e.do("admin", "DELETE", "/api/modes/echolink", nil, nil); c != 204 {
		t.Fatal(c)
	}
	e.do("admin", "GET", "/api/modes", nil, &l)
	if el = modeByName(l, mode.EchoLink); el.Configured {
		t.Fatalf("deleted: %+v", el)
	}
}

func TestSelectConnectDisconnect(t *testing.T) {
	e := newEnv(t, opts{})
	var n api.NodeJSON
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "AllStar", "target": "2000"}, &n); c != 200 {
		t.Fatal(c)
	}
	if n.Active != mode.AllStar || n.Mode == nil {
		t.Fatalf("%+v", n)
	}
	as := e.fake(mode.AllStar)
	if c := as.Connects(); len(c) != 1 || c[0].ID != "2000" {
		t.Fatalf("connects: %+v", c)
	}
	if c := e.do("op", "POST", "/api/connect", map[string]string{"target": "1999"}, &n); c != 200 || n.Active != mode.AllStar {
		t.Fatal(c, n)
	}
	if c := as.Connects(); len(c) != 2 || c[1].ID != "1999" {
		t.Fatalf("connects: %+v", c)
	}
	if c := e.do("op", "POST", "/api/disconnect", nil, &n); c != 200 || n.Active != mode.AllStar {
		t.Fatal(c, n)
	}
	if _, _, d, _ := as.Counts(); d != 1 {
		t.Fatalf("disconnects: %d", d)
	}

	// one radio, one mode: selecting BM stops AllStar
	if c := e.do("op", "POST", "/api/select", map[string]any{"mode": "bm", "target": "91", "slot": 1}, &n); c != 200 || n.Active != mode.BM {
		t.Fatal(c, n)
	}
	if as.Running() {
		t.Fatal("AllStar still runs next to BM")
	}
	if c := e.fake(mode.BM).Connects(); len(c) != 1 || c[0].ID != "91" || c[0].Slot != 1 {
		t.Fatalf("bm connects: %+v", c)
	}
	// idle
	n = api.NodeJSON{}
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": ""}, &n); c != 200 || n.Active != "" {
		t.Fatal(c, n)
	}

	bad := []struct {
		path string
		body any
		code int
		err  string
	}{
		{"/api/select", map[string]string{"mode": "all star"}, 400, "bad_mode"},
		{"/api/select", map[string]string{"mode": "", "target": "2000"}, 400, "bad_request"},
		{"/api/select", map[string]string{"mode": "allstar", "target": "2000\nx"}, 400, "bad_target"},
		{"/api/select", map[string]any{"mode": "allstar", "target": "2000", "port": 70000}, 400, "bad_target"},
		{"/api/select", map[string]any{"mode": "bm", "target": "91", "slot": 3}, 400, "bad_target"},
		{"/api/select", map[string]string{"mode": "allstar", "target": strings.Repeat("1", 65)}, 400, "bad_target"},
		{"/api/select", map[string]string{"mode": "nosuchmode"}, 409, "node_refused"},
		{"/api/connect", map[string]string{"target": "2000"}, 409, "node_refused"}, // idle
		{"/api/connect", map[string]string{"target": ""}, 400, "bad_target"},
		{"/api/disconnect", nil, 409, "node_refused"},
	}
	for _, b := range bad {
		expectErr(t, e, "op", "POST", b.path, b.body, b.code, b.err)
	}
	if c := as.Connects(); len(c) != 2 {
		t.Fatalf("a bad request reached the node: %+v", c)
	}
	// the node keeps the selection across restarts
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "1998"}, nil)
	cfg, _ := os.ReadFile(e.node.ConfigPath)
	if !bytes.Contains(cfg, []byte(`"allstar"`)) {
		t.Fatalf("selection not saved: %s", cfg)
	}
	tune, _ := os.ReadFile(filepath.Join(filepath.Dir(e.node.ConfigPath), "tune.json"))
	if !bytes.Contains(tune, []byte(`"1998"`)) {
		t.Fatalf("last target not remembered: %s", tune)
	}
}

func TestNodeDown(t *testing.T) {
	e := newEnv(t, opts{})
	e.stopNode()
	n := e.waitNode("down", func(n api.NodeJSON) bool { return !n.Running })
	if n.Error == "" || n.Callsign != "N0CALL" {
		t.Fatalf("down keeps the last status and says why: %+v", n)
	}
	expectErr(t, e, "op", "POST", "/api/select", map[string]string{"mode": "allstar"}, 503, "node_unavailable")
	expectErr(t, e, "op", "POST", "/api/disconnect", nil, 503, "node_unavailable")
	expectErr(t, e, "viewer", "GET", "/api/modes", nil, 503, "node_unavailable")
	expectErr(t, e, "viewer", "GET", "/api/heard", nil, 503, "node_unavailable")
	var sys system.System
	e.do("viewer", "GET", "/api/system", nil, &sys)
	if sys.Node.Running || sys.Node.Error == nil {
		t.Fatalf("%+v", sys.Node)
	}
	// it comes back by itself
	e.startNode(nil)
	e.waitNode("back", func(n api.NodeJSON) bool { return n.Running && n.Error == "" })
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar"}, nil); c != 200 {
		t.Fatal(c)
	}
}

func TestNoNode(t *testing.T) {
	e := newEnv(t, opts{noNode: true})
	if n := e.nodeJSON(); n.Running {
		t.Fatalf("%+v", n)
	}
	expectErr(t, e, "op", "POST", "/api/select", map[string]string{"mode": "allstar"}, 503, "node_unavailable")
	expectErr(t, e, "admin", "GET", "/api/radio", nil, 503, "node_unavailable")
}

func TestHeard(t *testing.T) {
	e := newEnv(t, opts{})
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil)
	f := e.fake(mode.AllStar)
	f.Key("N1CALL")
	time.Sleep(nowcalling.MinDuration + 100*time.Millisecond) // shorter is a glitch
	f.Unkey()
	var l []mode.Heard
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e.do("viewer", "GET", "/api/heard?limit=5", nil, &l); len(l) == 1 && !l[0].End.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(l) != 1 || l[0].Callsign != "N1CALL" || l[0].Mode != mode.AllStar {
		t.Fatalf("%+v", l)
	}
	expectErr(t, e, "viewer", "GET", "/api/heard?limit=0", nil, 400, "bad_request")
	expectErr(t, e, "viewer", "GET", "/api/heard?limit=501", nil, 400, "bad_request")
	var nc struct {
		Recent []struct{ Kind, Call string }
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e.do("viewer", "GET", "/api/nowcalling", nil, &nc); len(nc.Recent) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(nc.Recent) != 1 || nc.Recent[0].Kind != mode.AllStar || nc.Recent[0].Call != "N1CALL" {
		t.Fatalf("now calling: %+v", nc)
	}
}

func TestRadioLevels(t *testing.T) {
	e := newEnv(t, opts{})
	var l map[string]any
	if c := e.do("admin", "GET", "/api/radio", nil, &l); c != 200 {
		t.Fatal(c)
	}
	if c := e.do("admin", "PUT", "/api/radio", map[string]float64{"rxVolume": 0.5}, &l); c != 200 {
		t.Fatal(c, l)
	}
	if v, ok := l["CaptureVolume"].(float64); !ok || v < 0.49 || v > 0.51 {
		t.Fatalf("%+v", l)
	}
	expectErr(t, e, "admin", "PUT", "/api/radio", map[string]float64{"txVolume": 1.5}, 400, "bad_request")
	expectErr(t, e, "admin", "PUT", "/api/radio", map[string]float64{}, 400, "bad_request")
}

func TestRadioInfoAndPreset(t *testing.T) {
	e := newEnv(t, opts{})
	var d struct {
		Present   bool
		Preset    string
		Interface struct{ Preset string }
		Presets   []struct{ Name, Title string }
	}
	if c := e.do("admin", "GET", "/api/radio/info", nil, &d); c != 200 {
		t.Fatal(c)
	}
	if len(d.Presets) != 3 || d.Presets[0].Name != "shari" || d.Interface.Preset == "" {
		t.Fatalf("%+v", d)
	}
	// A bad preset is refused with the node's words; a good one is saved.
	var st map[string]any
	e.do("admin", "GET", "/api/station", nil, &st)
	radio := st["radio"].(map[string]any)
	radio["preset"] = "bogus"
	expectErr(t, e, "admin", "PUT", "/api/station", st, 409, "node_refused")
	radio["preset"] = "uri"
	radio["pttGpio"] = 4
	if c := e.do("admin", "PUT", "/api/station", st, nil); c != 200 {
		t.Fatal(c)
	}
	var got map[string]any
	e.do("admin", "GET", "/api/station", nil, &got)
	if r := got["radio"].(map[string]any); r["preset"] != "uri" || r["pttGpio"] != float64(4) {
		t.Fatalf("%+v", r)
	}
	if c := e.do("admin", "GET", "/api/radio/info", nil, &d); c != 200 || d.Preset != "uri" {
		t.Fatalf("%d %+v", c, d)
	}
}

func TestFavorites(t *testing.T) {
	e := newEnv(t, opts{})
	var f1, f2, f3, f4 api.FavoriteJSON
	if c := e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "1998"}, &f1); c != 201 {
		t.Fatal(c)
	}
	if f1.Label != "N1CALL Metro Hub" || f1.Info == nil || f1.Info.Callsign != "N1CALL" || f1.Group != "" || f1.Mode != mode.AllStar {
		t.Fatalf("default label: %+v", f1)
	}
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "2000", "label": " My hub ", "group": "Hubs"}, &f2)
	if f2.Label != "My hub" || f2.Group != "Hubs" || f2.Position <= f1.Position {
		t.Fatalf("%+v", f2)
	}
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "424242"}, &f3)
	if f3.Label != "424242" || f3.Info != nil {
		t.Fatalf("unknown node label: %+v", f3)
	}
	if c := e.do("op", "POST", "/api/favorites", map[string]string{"mode": "bm", "target": "91", "label": "Worldwide"}, &f4); c != 201 || f4.Mode != mode.BM || f4.Info != nil {
		t.Fatal(c, f4)
	}
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "abc"}, 400, "bad_target")
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "nosuchmode", "target": "1"}, 400, "bad_mode")
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"target": "1"}, 400, "bad_mode")
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "2000"}, 409, "duplicate")
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "1999", "label": strings.Repeat("x", 500)}, 400, "bad_request")

	var list []api.FavoriteJSON
	e.do("viewer", "GET", "/api/favorites?mode=allstar", nil, &list)
	if len(list) != 3 || list[0].ID != f1.ID {
		t.Fatalf("%+v", list)
	}
	// ordered by position (the UI groups them)
	if list[1].Group != "Hubs" || list[0].Position != 0 || list[2].Position != 2 {
		t.Fatalf("order: %+v", list)
	}
	e.do("viewer", "GET", "/api/favorites", nil, &list)
	if len(list) != 4 {
		t.Fatalf("all modes: %+v", list)
	}
	e.do("viewer", "GET", "/api/favorites?mode=bm", nil, &list)
	if len(list) != 1 || list[0].Target != "91" {
		t.Fatalf("bm: %+v", list)
	}

	var up api.FavoriteJSON
	if c := e.do("op", "PUT", fmt.Sprintf("/api/favorites/%d", f2.ID), map[string]string{"label": "Renamed", "group": ""}, &up); c != 200 || up.Label != "Renamed" || up.Group != "" || up.Target != "2000" {
		t.Fatal(c, up)
	}
	if c := e.do("op", "PUT", fmt.Sprintf("/api/favorites/%d", f2.ID), map[string]string{"target": "1999"}, &up); c != 200 || up.Target != "1999" || up.Info == nil || up.Info.Callsign != "N3CALL" {
		t.Fatal(c, up)
	}
	expectErr(t, e, "op", "PUT", "/api/favorites/9999", map[string]string{"label": "x"}, 404, "not_found")
	expectErr(t, e, "op", "PUT", fmt.Sprintf("/api/favorites/%d", f2.ID), map[string]string{"target": "bad"}, 400, "bad_target")

	// reorder within a mode: all in one group now, positions follow the order
	if c := e.do("op", "POST", "/api/favorites/reorder", map[string]any{"mode": "allstar", "ids": []int64{f3.ID, f2.ID, f1.ID}}, nil); c != 204 {
		t.Fatal(c)
	}
	e.do("op", "GET", "/api/favorites?mode=allstar", nil, &list)
	if len(list) != 3 || list[0].ID != f3.ID || list[1].ID != f2.ID || list[2].ID != f1.ID {
		t.Fatalf("after reorder: %+v", list)
	}
	for i, f := range list {
		if f.Position != i {
			t.Fatalf("positions: %+v", list)
		}
	}
	expectErr(t, e, "op", "POST", "/api/favorites/reorder", map[string]any{"mode": "allstar", "ids": []int64{f3.ID, f4.ID}}, 400, "bad_request")

	if c := e.do("op", "DELETE", fmt.Sprintf("/api/favorites/%d", f1.ID), nil, nil); c != 204 {
		t.Fatal(c)
	}
	expectErr(t, e, "op", "DELETE", fmt.Sprintf("/api/favorites/%d", f1.ID), nil, 404, "not_found")
	e.do("op", "GET", "/api/favorites?mode=allstar", nil, &list)
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
}

func TestFavoriteEchoLinkTargets(t *testing.T) {
	e := newEnv(t, opts{})
	var f api.FavoriteJSON
	// a node number or a callsign (saved in upper case); anything else is refused
	if c := e.do("op", "POST", "/api/favorites", map[string]string{"mode": "echolink", "target": "n0call-l"}, &f); c != 201 || f.Target != "N0CALL-L" || f.Label != "N0CALL-L" {
		t.Fatal(c, f)
	}
	if c := e.do("op", "POST", "/api/favorites", map[string]string{"mode": "echolink", "target": "123456"}, &f); c != 201 || f.Target != "123456" {
		t.Fatal(c, f)
	}
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "echolink", "target": "N0CALL-X"}, 400, "bad_target")
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "echolink", "target": "no spaces"}, 400, "bad_target")
	// AllStar still takes only node numbers
	expectErr(t, e, "op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "N0CALL"}, 400, "bad_target")
	// writes need an operator
	expectErr(t, e, "viewer", "POST", "/api/favorites/reorder", map[string]any{"mode": "echolink", "ids": []int64{}}, 403, "forbidden")
}

func TestFavoritePins(t *testing.T) {
	e := newEnv(t, opts{})
	var f api.FavoriteJSON
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "allstar", "target": "2000"}, &f)
	if f.Pinned {
		t.Fatalf("new favorites are not pinned: %+v", f)
	}
	path := fmt.Sprintf("/api/favorites/%d", f.ID)
	expectErr(t, e, "viewer", "PUT", path, map[string]bool{"pinned": true}, 403, "forbidden")
	var up api.FavoriteJSON
	if c := e.do("op", "PUT", path, map[string]bool{"pinned": true}, &up); c != 200 || !up.Pinned || up.Label != f.Label {
		t.Fatal(c, up)
	}
	// an edit that does not mention pinned keeps the pin
	if c := e.do("op", "PUT", path, map[string]string{"label": "Renamed"}, &up); c != 200 || !up.Pinned || up.Label != "Renamed" {
		t.Fatal(c, up)
	}
	var list []api.FavoriteJSON
	e.do("viewer", "GET", "/api/favorites", nil, &list)
	if len(list) != 1 || !list[0].Pinned {
		t.Fatalf("viewer sees the pin: %+v", list)
	}
	if c := e.do("op", "PUT", path, map[string]bool{"pinned": false}, &up); c != 200 || up.Pinned {
		t.Fatal(c, up)
	}
	e.do("op", "DELETE", path, nil, nil)
	e.do("op", "GET", "/api/favorites", nil, &list)
	if len(list) != 0 {
		t.Fatalf("deleted with its pin: %+v", list)
	}
}

func TestLookup(t *testing.T) {
	e := newEnv(t, opts{})
	var in nodelist.Info
	if c := e.do("viewer", "GET", "/api/lookup/1998", nil, &in); c != 200 || in.Callsign != "N1CALL" || in.Location != "Springfield, ST" {
		t.Fatal(c, in)
	}
	expectErr(t, e, "viewer", "GET", "/api/lookup/777", nil, 404, "not_found")
	var res []nodelist.Info
	if c := e.do("viewer", "GET", "/api/lookup?q=n&limit=2", nil, &res); c != 200 || len(res) != 2 {
		t.Fatal(c, res)
	}
	e.do("viewer", "GET", "/api/lookup?q=metropolis", nil, &res)
	if len(res) != 1 || res[0].Node != "1999" {
		t.Fatalf("%+v", res)
	}
	e.do("viewer", "GET", "/api/lookup?q=20", nil, &res)
	if len(res) != 1 || res[0].Node != "2000" {
		t.Fatalf("prefix: %+v", res)
	}
	e.do("viewer", "GET", "/api/lookup", nil, &res)
	if res == nil || len(res) != 0 {
		t.Fatalf("empty q: %+v", res)
	}
	expectErr(t, e, "viewer", "GET", "/api/lookup?q=x&limit=-1", nil, 400, "bad_request")
	var st nodelist.Status
	if c := e.do("viewer", "GET", "/api/nodelist", nil, &st); c != 200 || st.Count != 4 || st.Source != "test" || st.Updated == nil || st.Error != nil {
		t.Fatal(c, st)
	}
}

func TestSystem(t *testing.T) {
	e := newEnv(t, opts{})
	var sys system.System
	if c := e.do("viewer", "GET", "/api/system", nil, &sys); c != 200 {
		t.Fatal(c)
	}
	if !sys.Node.Running || sys.Node.Uptime == nil || sys.Node.Error != nil || sys.Version != "test" {
		t.Fatalf("%+v", sys)
	}
	_, raw, _ := e.raw("GET", "/api/system", nil, e.ck["viewer"], nil)
	for _, k := range []string{`"hostname"`, `"cpuTemp"`, `"load"`, `"memTotal"`, `"memAvailable"`, `"uptime"`, `"node"`, `"version"`, `"running"`, `"error"`} {
		if !bytes.Contains(raw, []byte(k)) {
			t.Errorf("missing %s in %s", k, raw)
		}
	}
}

func TestUsersAdmin(t *testing.T) {
	e := newEnv(t, opts{})
	var u api.UserJSON
	if c := e.do("admin", "POST", "/api/users", map[string]string{"username": "newbie", "password": "longpassword", "role": "viewer"}, &u); c != 201 || u.Role != "viewer" || u.ID == 0 {
		t.Fatal(c, u)
	}
	expectErr(t, e, "admin", "POST", "/api/users", map[string]string{"username": "NEWBIE", "password": "longpassword", "role": "viewer"}, 409, "exists")
	expectErr(t, e, "admin", "POST", "/api/users", map[string]string{"username": "x", "password": "short", "role": "viewer"}, 400, "bad_request")
	expectErr(t, e, "admin", "POST", "/api/users", map[string]string{"username": "x", "password": "longpassword", "role": "root"}, 400, "bad_request")
	var list []api.UserJSON
	e.do("admin", "GET", "/api/users", nil, &list)
	if len(list) != 4 {
		t.Fatalf("%+v", list)
	}
	_, raw, _ := e.raw("GET", "/api/users", nil, e.ck["admin"], nil)
	if bytes.Contains(raw, []byte("argon2")) || bytes.Contains(raw, []byte("pw_hash")) {
		t.Fatal("password hash leaked")
	}
	path := fmt.Sprintf("/api/users/%d", u.ID)
	// promote + reset password; new password works, old sessions die
	if c := e.do("admin", "PUT", path, map[string]string{"role": "operator", "password": "another-long-pw"}, &u); c != 200 || u.Role != "operator" {
		t.Fatal(c, u)
	}
	if c := e.do("", "POST", "/api/login", map[string]string{"username": "newbie", "password": "another-long-pw"}, nil); c != 200 {
		t.Fatalf("login with new password: %d", c)
	}
	expectErr(t, e, "admin", "PUT", "/api/users/9999", map[string]string{"role": "viewer"}, 404, "not_found")
	expectErr(t, e, "admin", "PUT", path, map[string]string{"role": "king"}, 400, "bad_request")

	var me api.UserJSON
	e.do("admin", "GET", "/api/session", nil, &me)
	selfPath := fmt.Sprintf("/api/users/%d", me.ID)
	expectErr(t, e, "admin", "DELETE", selfPath, nil, 409, "self_delete")
	expectErr(t, e, "admin", "PUT", selfPath, map[string]string{"role": "viewer"}, 409, "last_admin")
	if c := e.do("admin", "DELETE", path, nil, nil); c != 204 {
		t.Fatal(c)
	}
	expectErr(t, e, "admin", "DELETE", path, nil, 404, "not_found")
	// second admin allows demoting the first; last admin cannot be deleted by the other
	var a2 api.UserJSON
	e.do("admin", "POST", "/api/users", map[string]string{"username": "boss2", "password": "longpassword", "role": "admin"}, &a2)
	if c := e.do("admin", "PUT", selfPath, map[string]string{"role": "operator"}, nil); c != 200 {
		t.Fatalf("demote with 2 admins: %d", c)
	}
}

func TestPasswordChange(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "op", "POST", "/api/password", map[string]string{"current": "wrong", "new": "brand-new-password"}, 403, "bad_credentials")
	expectErr(t, e, "op", "POST", "/api/password", map[string]string{"current": "password-op", "new": "short"}, 400, "bad_request")
	// a second device
	code, _, hdr := e.raw("POST", "/api/login", map[string]string{"username": "op", "password": "password-op"}, "", nil)
	if code != 200 {
		t.Fatal(code)
	}
	var other string
	for _, c := range hdr.Values("Set-Cookie") {
		other = strings.SplitN(strings.SplitN(c, ";", 2)[0], "=", 2)[1]
	}
	if c := e.do("op", "POST", "/api/password", map[string]string{"current": "password-op", "new": "brand-new-password"}, nil); c != 204 {
		t.Fatal(c)
	}
	if c := e.do("op", "GET", "/api/session", nil, nil); c != 200 {
		t.Fatalf("current session should survive: %d", c)
	}
	if c, _, _ := e.raw("GET", "/api/session", nil, other, nil); c != 401 {
		t.Fatalf("other session should be revoked: %d", c)
	}
	if c := e.do("", "POST", "/api/login", map[string]string{"username": "op", "password": "brand-new-password"}, nil); c != 200 {
		t.Fatalf("login with new password: %d", c)
	}
}

func TestSecurityHeadersAndNoStore(t *testing.T) {
	e := newEnv(t, opts{})
	_, _, h := e.raw("GET", "/api/setup", nil, "", nil)
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") == "" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", h)
	}
}

// ------------------------------------------------------------------- SSE

type sseEvent struct {
	name, data string
}

type sseStream struct {
	events  chan sseEvent
	pings   chan struct{}
	cancel  context.CancelFunc
	closed  chan struct{}
	resp    *http.Response
	t       *testing.T
	pending []sseEvent
}

func (e *env) stream(role string) *sseStream {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/events", nil)
	req.Header.Set("Cookie", auth.CookieName+"="+e.ck[role])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		e.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		cancel()
		e.t.Fatalf("events: %d %s", resp.StatusCode, b)
	}
	s := &sseStream{events: make(chan sseEvent, 1000), pings: make(chan struct{}, 1000), cancel: cancel, closed: make(chan struct{}), resp: resp, t: e.t}
	go func() {
		defer close(s.closed)
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		var cur sseEvent
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, ": ping"):
				select {
				case s.pings <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "" && cur.name != "":
				s.events <- cur
				cur = sseEvent{}
			}
		}
	}()
	e.t.Cleanup(cancel)
	return s
}

// next waits for an event satisfying pred (earlier non-matching events are skipped).
func (s *sseStream) wait(pred func(sseEvent) bool) sseEvent {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.events:
			if pred(ev) {
				return ev
			}
		case <-timeout:
			s.t.Fatal("timed out waiting for SSE event")
		}
	}
}

func (s *sseStream) first() sseEvent {
	s.t.Helper()
	select {
	case ev := <-s.events:
		return ev
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for first SSE event")
	}
	return sseEvent{}
}

func TestInstanceHeaderMatchesHello(t *testing.T) {
	e := newEnv(t, opts{})
	_, _, h := e.raw("GET", "/api/session", nil, e.ck["admin"], nil)
	inst := h.Get("X-nodespot-Instance")
	if inst == "" {
		t.Fatal("missing X-nodespot-Instance header")
	}
	var hello struct {
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal([]byte(e.stream("admin").first().data), &hello); err != nil || hello.Instance != inst {
		t.Fatalf("hello instance %q header %q (%v)", hello.Instance, inst, err)
	}
}

func TestSSEHelloNodeAndCalls(t *testing.T) {
	e := newEnv(t, opts{})
	s := e.stream("admin")
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	ev := s.first()
	if ev.name != "hello" {
		t.Fatalf("first event %q", ev.name)
	}
	var hello struct {
		Instance    string       `json:"instance"`
		User        api.UserJSON `json:"user"`
		Version     string       `json:"version"`
		SetupNeeded *bool        `json:"setupNeeded"`
	}
	if err := json.Unmarshal([]byte(ev.data), &hello); err != nil || hello.User.Username != "admin" || hello.Version != "test" || hello.Instance == "" || hello.SetupNeeded == nil || *hello.SetupNeeded {
		t.Fatalf("hello: %s %v", ev.data, err)
	}
	ev = s.first()
	if ev.name != "node" {
		t.Fatalf("second event %q", ev.name)
	}
	var n api.NodeJSON
	if err := json.Unmarshal([]byte(ev.data), &n); err != nil || !n.Running || n.Callsign != "N0CALL" {
		t.Fatalf("node: %s %v", ev.data, err)
	}

	// a select shows up as a node frame
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil)
	s.wait(func(ev sseEvent) bool {
		var n api.NodeJSON
		return ev.name == "node" && json.Unmarshal([]byte(ev.data), &n) == nil && n.Active == mode.AllStar
	})
	// a talker shows up in now calling
	e.fake(mode.AllStar).Key("1999")
	ev = s.wait(func(ev sseEvent) bool { return ev.name == "nowcalling" && strings.Contains(ev.data, `"current":{`) })
	var nc struct {
		Current struct{ Kind, Node, Call string }
	}
	if err := json.Unmarshal([]byte(ev.data), &nc); err != nil || nc.Current.Kind != mode.AllStar || nc.Current.Node != "1999" || nc.Current.Call != "N3CALL" {
		t.Fatalf("nowcalling: %s %v", ev.data, err)
	}
	e.fake(mode.AllStar).Unkey()
	s.wait(func(ev sseEvent) bool { return ev.name == "nowcalling" && strings.Contains(ev.data, `"current":null`) })

	// pings (test interval 100 ms)
	select {
	case <-s.pings:
	case <-time.After(10 * time.Second):
		t.Fatal("no ping comment")
	}
	// system pushes (test interval 200 ms)
	ev = s.wait(func(ev sseEvent) bool { return ev.name == "system" })
	var sys system.System
	if err := json.Unmarshal([]byte(ev.data), &sys); err != nil || !sys.Node.Running {
		t.Fatalf("system event: %s %v", ev.data, err)
	}
	s.cancel()
	<-s.closed
	deadline := time.Now().Add(10 * time.Second)
	for e.srv.Clients() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.srv.Clients() != 0 {
		t.Fatalf("clients after disconnect: %d", e.srv.Clients())
	}
}

func TestSSEFavoritesEvent(t *testing.T) {
	e := newEnv(t, opts{})
	v := e.stream("viewer")
	if v.first().name != "hello" {
		t.Fatal("hello")
	}
	if ev := v.first(); ev.name != "node" {
		t.Fatalf("second event %q", ev.name)
	}
	// favorites event reaches every client
	e.do("op", "POST", "/api/favorites", map[string]string{"mode": "bm", "target": "91", "label": "Via SSE"}, nil)
	ev := v.wait(func(ev sseEvent) bool { return ev.name == "favorites" })
	var favs []api.FavoriteJSON
	if err := json.Unmarshal([]byte(ev.data), &favs); err != nil || len(favs) != 1 || favs[0].Label != "Via SSE" || favs[0].Mode != mode.BM {
		t.Fatalf("%s %v", ev.data, err)
	}
}
func TestSSERequiresSessionAndEndsOnLogout(t *testing.T) {
	e := newEnv(t, opts{})
	if c, _, _ := e.raw("GET", "/api/events", nil, "", nil); c != 401 {
		t.Fatalf("anonymous events: %d", c)
	}
	s := e.stream("op")
	s.first()
	e.do("op", "POST", "/api/logout", nil, nil)
	select {
	case <-s.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("stream not closed after logout")
	}
}

func TestSSENodeRestart(t *testing.T) {
	e := newEnv(t, opts{})
	s := e.stream("admin")
	s.first()
	s.first()
	e.stopNode()
	s.wait(func(ev sseEvent) bool {
		var n api.NodeJSON
		return ev.name == "node" && json.Unmarshal([]byte(ev.data), &n) == nil && !n.Running
	})
	e.startNode(nil)
	s.wait(func(ev sseEvent) bool {
		var n api.NodeJSON
		return ev.name == "node" && json.Unmarshal([]byte(ev.data), &n) == nil && n.Running
	})
}

func TestRadioInputLevel(t *testing.T) {
	e := newEnv(t, opts{})
	var l struct {
		PeakDb, RMSDb *float64
		COS           *bool
	}
	if c := e.do("admin", "GET", "/api/radio/input", nil, &l); c != 200 {
		t.Fatal(c)
	}
	if l.PeakDb == nil || l.RMSDb == nil || l.COS == nil || *l.PeakDb != -90 {
		t.Fatalf("%+v", l)
	}
	// The level is in the node status too.
	var n struct {
		Radio struct{ Level map[string]any }
	}
	if c := e.do("viewer", "GET", "/api/node", nil, &n); c != 200 || n.Radio.Level["peakDb"] == nil {
		t.Fatalf("%d %+v", c, n)
	}
	expectErr(t, e, "viewer", "GET", "/api/radio/input", nil, 403, "forbidden")
}

// TestSSELocalStation: the Dashboard's "Local station" follows the node's
// rxActive as it changes, not only when something else refreshes the
// status.
func TestSSELocalStation(t *testing.T) {
	e := newEnv(t, opts{})
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil); c != 200 {
		t.Fatalf("select: %d", c)
	}
	e.waitNode("connected", func(n api.NodeJSON) bool {
		return n.Active == mode.AllStar && n.Mode != nil && n.Mode.State == mode.StateConnected
	})
	time.Sleep(500 * time.Millisecond) // the select's own refreshes are over
	s := e.stream("admin")
	s.first()
	// The rig's engine has no clock of its own: step it in real time.
	done := make(chan struct{})
	defer close(done)
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-tk.C:
				e.node.Engine.Step(now)
			}
		}
	}()
	e.node.Radio.SetCarrier(true)
	s.wait(func(ev sseEvent) bool { return ev.name == "node" && strings.Contains(ev.data, `"rxActive":true`) })
	e.node.Radio.SetCarrier(false)
	s.wait(func(ev sseEvent) bool {
		return ev.name == "node" && strings.Contains(ev.data, `"cos":false`) && !strings.Contains(ev.data, `"rxActive":true`)
	})
}

func TestRadioTestTone(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "POST", "/api/radio/tone", map[string]any{"seconds": 11}, 400, "bad_request")
	if c := e.do("admin", "POST", "/api/radio/tone", map[string]any{"seconds": 3}, nil); c != 200 {
		t.Fatalf("test tone: %d", c)
	}
	e.waitNode("tone on", func(n api.NodeJSON) bool { return n.Radio.TestTone })
	if c := e.do("admin", "DELETE", "/api/radio/tone", nil, nil); c != 200 {
		t.Fatalf("stop: %d", c)
	}
	e.waitNode("tone off", func(n api.NodeJSON) bool { return !n.Radio.TestTone })
	// Refused while the radio receives: 409 with the node's reason.
	e.node.Radio.SetCarrier(true)
	var out struct{ Code, Error string }
	if c := e.do("admin", "POST", "/api/radio/tone", nil, &out); c != 409 || out.Code != "node_refused" || !strings.Contains(out.Error, "receiving") {
		t.Fatalf("while receiving: %d %+v", c, out)
	}
}

func TestRadioAutoset(t *testing.T) {
	e := newEnv(t, opts{})
	var st struct{ State, Error string }
	if c := e.do("admin", "GET", "/api/radio/autoset", nil, &st); c != 200 || st.State != "idle" {
		t.Fatalf("idle: %d %+v", c, st)
	}
	// A sound chip with no capture steps: 409 with the node's reason.
	var out struct{ Code, Error string }
	if c := e.do("admin", "POST", "/api/radio/autoset", nil, &out); c != 409 || out.Code != "node_refused" || !strings.Contains(out.Error, "receive level") {
		t.Fatalf("no steps: %d %+v", c, out)
	}
	e.node.Radio.SetCaptureSteps([]float64{0, 1, 2, 3, 4})
	if c := e.do("admin", "POST", "/api/radio/autoset", nil, &st); c != 200 || st.State != "waiting" {
		t.Fatalf("start: %d %+v", c, st)
	}
	if c := e.do("admin", "POST", "/api/radio/autoset", nil, &out); c != 409 {
		t.Fatalf("second start: %d", c)
	}
	if c := e.do("admin", "DELETE", "/api/radio/autoset", nil, &st); c != 200 || st.State != "idle" {
		t.Fatalf("cancel: %d %+v", c, st)
	}
}
