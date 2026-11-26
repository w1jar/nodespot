package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/nowcalling"
	"github.com/w1jar/nodespot/internal/public"
)

var ipLike = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)

func enablePublic(t *testing.T, e *env, patch map[string]any) {
	t.Helper()
	if patch == nil {
		patch = map[string]any{}
	}
	patch["enabled"] = true
	if code := e.do("admin", "PUT", "/api/public", patch, nil); code != 200 {
		t.Fatalf("enable: %d", code)
	}
}

// openPublic opens /api/public/events; the stream is nil for a non-200 status.
func openPublic(t *testing.T, e *env, hdr map[string]string) (*sseStream, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/public/events", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, resp.StatusCode
	}
	s := &sseStream{events: make(chan sseEvent, 1000), pings: make(chan struct{}, 1000), cancel: cancel, closed: make(chan struct{}), resp: resp, t: t}
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
	t.Cleanup(cancel)
	return s, 200
}

func TestPublicDisabledByDefault(t *testing.T) {
	e := newEnv(t, opts{})
	var info struct {
		Enabled  bool
		Title    string
		LoggedIn bool
	}
	if code := e.do("", "GET", "/api/public/info", nil, &info); code != 200 || info.Enabled || info.LoggedIn {
		t.Fatalf("%d %+v", code, info)
	}
	var adm struct{ LoggedIn bool }
	if code := e.do("admin", "GET", "/api/public/info", nil, &adm); code != 200 || !adm.LoggedIn {
		t.Fatalf("%d %+v", code, adm)
	}
	expectErr(t, e, "", "GET", "/api/public/status", nil, 404, "not_found")
	// public endpoints are GET only (anything else falls through to the catch-all)
	if code, _, _ := e.raw("POST", "/api/public/status", nil, "", nil); code != 404 && code != 405 {
		t.Fatalf("POST: %d", code)
	}
	if _, code := openPublic(t, e, nil); code != 404 {
		t.Fatalf("events while disabled: %d", code)
	}
	enablePublic(t, e, nil)
	if code := e.do("", "GET", "/api/public/info", nil, &info); code != 200 || !info.Enabled || !strings.Contains(info.Title, "node status") {
		t.Fatalf("%d %+v", code, info)
	}
	if code := e.do("", "GET", "/api/public/status", nil, nil); code != 200 {
		t.Fatal(code)
	}
	// disabling again closes the door (and open streams)
	s, _ := openPublic(t, e, nil)
	s.first()
	if code := e.do("admin", "PUT", "/api/public", map[string]any{"enabled": false}, nil); code != 200 {
		t.Fatal(code)
	}
	select {
	case <-s.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream not closed on disable")
	}
	expectErr(t, e, "", "GET", "/api/public/status", nil, 404, "not_found")
}

func TestPublicDoesNotOpenOtherAPIRoutes(t *testing.T) {
	e := newEnv(t, opts{})
	enablePublic(t, e, nil)
	for _, p := range []string{"/api/node", "/api/modes", "/api/heard", "/api/favorites", "/api/settings", "/api/system", "/api/public",
		"/api/events", "/api/users", "/api/about", "/api/lookup/2000", "/api/lookup?q=a", "/api/nodelist", "/api/updates",
		"/api/diagnostics", "/api/session", "/api/favorites/stats", "/api/nowcalling"} {
		if code := e.do("", "GET", p, nil, nil); code != 401 {
			t.Errorf("anonymous GET %s: %d, want 401", p, code)
		}
	}
	// the admin-only settings stay admin-only
	for _, role := range []string{"op", "viewer"} {
		if code := e.do(role, "GET", "/api/public", nil, nil); code != 403 {
			t.Errorf("%s GET /api/public: %d", role, code)
		}
		if code := e.do(role, "PUT", "/api/public", map[string]any{"enabled": false}, nil); code != 403 {
			t.Errorf("%s PUT /api/public: %d", role, code)
		}
	}
	if code := e.do("", "PUT", "/api/public", map[string]any{"enabled": false}, nil); code != 401 {
		t.Errorf("anonymous PUT: %d", code)
	}
}

func TestPublicDataHasNoIPsOrSystemInfo(t *testing.T) {
	e := newEnv(t, opts{})
	enablePublic(t, e, map[string]any{"showHeard": true})
	// a reflector with an address: the public page never shows it
	e.do("op", "POST", "/api/select", map[string]any{"mode": "ysf", "target": "1999", "host": "192.0.2.58", "port": 42000}, nil)
	_, body, hdr := e.raw("GET", "/api/public/status", nil, "", nil)
	if ipLike.Match(body) || strings.Contains(strings.ToLower(string(body)), `"ip"`) || strings.Contains(string(body), "42000") {
		t.Fatalf("address in public status: %s", body)
	}
	for _, k := range []string{"hostname", "version", "load", "cpuTemp", "secret", "favorites", "password", "dmr_id"} {
		if strings.Contains(string(body), k) {
			t.Errorf("public status mentions %q: %s", k, body)
		}
	}
	if hdr.Get("Cache-Control") != "no-store" || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Errorf("headers %v", hdr)
	}
	if c := hdr.Get("Set-Cookie"); c != "" {
		t.Errorf("cookie set: %q", c)
	}
	var snap public.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("%v %s", err, body)
	}
	if snap.Station.Callsign != "N0CALL" || snap.Title != "N0CALL node status" {
		t.Fatalf("%+v", snap)
	}

	// SSE: snapshot and changes carry no IPs either
	s, code := openPublic(t, e, nil)
	if code != 200 {
		t.Fatal(code)
	}
	ev := s.first()
	if ev.name != "snapshot" || ipLike.MatchString(ev.data) {
		t.Fatalf("%s %s", ev.name, ev.data)
	}
	e.fake(mode.YSF).Key("N1CALL")
	ev = s.wait(func(ev sseEvent) bool { return ev.name == "snapshot" && strings.Contains(ev.data, `"N1CALL"`) })
	if ipLike.MatchString(ev.data) {
		t.Fatalf("snapshot: %s", ev.data)
	}
	if got := s.resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatal(got)
	}
}

func TestPublicStationAndCalls(t *testing.T) {
	e := newEnv(t, opts{})
	enablePublic(t, e, map[string]any{"showHeard": true})
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil)
	f := e.fake(mode.AllStar)
	f.Key("N1CALL")
	time.Sleep(nowcalling.MinDuration + 100*time.Millisecond) // shorter is a glitch
	var snap public.Snapshot
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.do("", "GET", "/api/public/status", nil, &snap); snap.Calling != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap.Station.Mode != mode.AllStar || snap.Station.Target != "2000" || snap.Station.State != "connected" {
		t.Fatalf("station: %+v", snap.Station)
	}
	if snap.Calling == nil || snap.Calling.Callsign != "N1CALL" || snap.Calling.Mode != mode.AllStar {
		t.Fatalf("calling: %+v", snap.Calling)
	}
	f.Unkey()
	for time.Now().Before(deadline) {
		if e.do("", "GET", "/api/public/status", nil, &snap); snap.Calling == nil && len(snap.Heard) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(snap.Heard) != 1 || snap.Heard[0].Callsign != "N1CALL" {
		t.Fatalf("heard: %+v", snap.Heard)
	}
	// the node going away shows as offline
	e.stopNode()
	for time.Now().Before(deadline) {
		if e.do("", "GET", "/api/public/status", nil, &snap); snap.Station.State == "offline" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap.Station.State != "offline" {
		t.Fatalf("down: %+v", snap.Station)
	}
}

func TestPublicCallsignHidingAndTitle(t *testing.T) {
	e := newEnv(t, opts{})
	enablePublic(t, e, map[string]any{"showCallsigns": false, "showHeard": true, "title": "Club <b>node</b>"})
	e.do("op", "POST", "/api/select", map[string]string{"mode": "allstar", "target": "2000"}, nil)
	f := e.fake(mode.AllStar)
	f.Key("N1CALL")
	time.Sleep(nowcalling.MinDuration + 100*time.Millisecond)
	f.Unkey()
	var snap public.Snapshot
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.do("", "GET", "/api/public/status", nil, &snap); len(snap.Heard) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap.Callsigns || snap.Title != "Club bnode/b" {
		t.Fatalf("%+v", snap)
	}
	if len(snap.Heard) != 1 || snap.Heard[0].Callsign != "" || snap.Heard[0].Mode != mode.AllStar {
		t.Fatalf("heard: %+v", snap.Heard)
	}
	_, raw, _ := e.raw("GET", "/api/public/status", nil, "", nil)
	if strings.Contains(string(raw), "N1CALL") {
		t.Fatalf("callsign leaked: %s", raw)
	}
	// the heard list can be switched off
	e.do("admin", "PUT", "/api/public", map[string]any{"showHeard": false}, nil)
	_, raw, _ = e.raw("GET", "/api/public/status", nil, "", nil)
	if strings.Contains(string(raw), `"heard":[`) {
		t.Fatalf("%s", raw)
	}
}

func TestPublicSettingsValidation(t *testing.T) {
	e := newEnv(t, opts{})
	expectErr(t, e, "admin", "PUT", "/api/public", map[string]any{"title": strings.Repeat("x", 81)}, 400, "bad_request")
	expectErr(t, e, "admin", "PUT", "/api/public", map[string]any{"title": "a\x00b"}, 400, "bad_request")
	var out struct {
		TrustProxy   bool
		DefaultTitle string
	}
	if code := e.do("admin", "PUT", "/api/public", map[string]any{"trustProxy": true}, &out); code != 200 {
		t.Fatal(code)
	}
	if !out.TrustProxy || out.DefaultTitle != "N0CALL node status" {
		t.Fatalf("%+v", out)
	}
	// persisted in the settings table
	if v, err := e.st.GetSetting("public.trust_proxy"); err != nil || v != "true" {
		t.Fatalf("%q %v", v, err)
	}
}

func TestPublicSSELimits(t *testing.T) {
	e := newEnv(t, opts{})
	enablePublic(t, e, nil)
	e.srv.Public().MaxPerIP = 2
	e.srv.Public().MaxClients = 3
	e.srv.Public().SetRateLimit(1000, 1000)

	s1, c := openPublic(t, e, nil)
	s2, c2 := openPublic(t, e, nil)
	if c != 200 || c2 != 200 {
		t.Fatal(c, c2)
	}
	s1.first()
	s2.first()
	if _, c := openPublic(t, e, nil); c != 429 {
		t.Fatalf("third connection from one address: %d, want 429", c)
	}
	// Without trust-proxy the header is ignored: still the same client.
	if _, c := openPublic(t, e, map[string]string{"X-Forwarded-For": "203.0.113.7"}); c != 429 {
		t.Fatalf("spoofed X-Forwarded-For must not help: %d", c)
	}
	// With trust-proxy it identifies the client, and the global cap applies.
	e.do("admin", "PUT", "/api/public", map[string]any{"trustProxy": true}, nil)
	s3, c := openPublic(t, e, map[string]string{"X-Forwarded-For": "198.51.100.1, 203.0.113.7"})
	if c != 200 {
		t.Fatalf("other client behind the proxy: %d", c)
	}
	s3.first()
	if _, c := openPublic(t, e, map[string]string{"X-Forwarded-For": "198.51.100.2"}); c != 503 {
		t.Fatalf("global cap: %d, want 503", c)
	}
	// closing one frees a slot
	s1.cancel()
	deadline := time.Now().Add(3 * time.Second)
	for e.srv.Public().Clients() > 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, c := openPublic(t, e, map[string]string{"X-Forwarded-For": "198.51.100.2"}); c != 200 {
		t.Fatalf("after a disconnect: %d", c)
	}
}

func TestPublicRateLimit(t *testing.T) {
	e := newEnv(t, opts{})
	enablePublic(t, e, nil)
	e.srv.Public().SetRateLimit(0.001, 3)
	got := map[int]int{}
	for i := 0; i < 8; i++ {
		code, body, hdr := e.raw("GET", "/api/public/status", nil, "", nil)
		got[code]++
		if code == 429 && (hdr.Get("Retry-After") == "" || !strings.Contains(string(body), "rate_limited")) {
			t.Fatalf("%v %s", hdr, body)
		}
	}
	if got[200] != 3 || got[429] != 5 {
		t.Fatalf("%v", got)
	}
	// the admin API is not subject to it
	if code := e.do("admin", "GET", "/api/node", nil, nil); code != 200 {
		t.Fatal(code)
	}
}

func TestEmbedFramingHeaders(t *testing.T) {
	e := newEnv(t, opts{})
	fa := func(path string) (string, string) {
		_, _, h := e.raw("GET", path, nil, "", nil)
		return h.Get("Content-Security-Policy"), h.Get("X-Frame-Options")
	}
	has := func(csp, want string) bool { return strings.Contains(csp, "frame-ancestors "+want+";") }
	// default: nothing can be framed
	for _, p := range []string{"/", "/embed", "/login", "/api/public/info"} {
		if csp, xfo := fa(p); !has(csp, "'none'") || xfo != "DENY" {
			t.Fatalf("%s: %q %q", p, csp, xfo)
		}
	}
	// embedding on but the public page off: still not framable
	e.do("admin", "PUT", "/api/public", map[string]any{"allowEmbedding": true}, nil)
	if csp, _ := fa("/embed"); !has(csp, "'none'") {
		t.Fatalf("embed while public is disabled: %q", csp)
	}
	enablePublic(t, e, nil)
	csp, xfo := fa("/embed")
	if !has(csp, "*") || xfo != "" {
		t.Fatalf("/embed: %q %q", csp, xfo)
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Fatal(csp)
	}
	// everything else, the login page included, stays unframable
	for _, p := range []string{"/", "/embedded", "/login", "/index.html", "/api/public/status", "/api/session"} {
		if csp, xfo := fa(p); !has(csp, "'none'") || xfo != "DENY" {
			t.Fatalf("%s: %q %q", p, csp, xfo)
		}
	}
	e.do("admin", "PUT", "/api/public", map[string]any{"allowEmbedding": false}, nil)
	if csp, _ := fa("/embed"); !has(csp, "'none'") {
		t.Fatalf("embed after switching it off: %q", csp)
	}
}
