package hamdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type memSettings map[string]string

func (s memSettings) GetSetting(k string) (string, error) { return s[k], nil }
func (s memSettings) SetSetting(k, v string) error        { s[k] = v; return nil }

// fake is a HamDB stand-in with placeholder data.
type fake struct {
	mu     sync.Mutex
	hits   []string
	agents []string
	status int               // 0: 200
	header map[string]string // extra headers of a non-200 answer
	known  map[string]string // call -> JSON body of the callsign object
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = append(f.hits, r.URL.Path)
	f.agents = append(f.agents, r.UserAgent())
	if f.status != 0 {
		for k, v := range f.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(f.status)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // v1, CALL, json, nodespot
	if len(parts) != 4 || parts[0] != "v1" || parts[2] != "json" || parts[3] != "nodespot" {
		http.NotFound(w, r)
		return
	}
	if c, ok := f.known[parts[1]]; ok {
		fmt.Fprintf(w, `{"hamdb":{"version":"1","callsign":%s,"messages":{"status":"OK"}}}`, c)
		return
	}
	fmt.Fprint(w, `{"hamdb":{"version":"1","callsign":{"call":"NOT_FOUND","name":"NOT_FOUND"},"messages":{"status":"NOT_FOUND"}}}`)
}

func (f *fake) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.hits) }

const pat = `{"call":"N0CALL","fname":"PAT","mi":"Q","name":"EXAMPLE","addr2":"SPRINGFIELD","state":"XX","country":"United States"}`

type rig struct {
	m   *Manager
	f   *fake
	clk *clock
	set memSettings
	dir string
	ch  int
}

func newRig(t *testing.T, on bool) *rig {
	t.Helper()
	f := &fake{known: map[string]string{"N0CALL": pat}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	r := &rig{f: f, clk: &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}, set: memSettings{}, dir: t.TempDir()}
	if on {
		r.set[SettingKey] = "true"
	}
	r.m = r.make(srv.URL)
	return r
}

func (r *rig) make(base string) *Manager {
	return New(Options{Dir: r.dir, BaseURL: base, Version: "9.9.9", Settings: r.set, Now: r.clk.Now, Rand: func() float64 { return 0.5 }, OnChange: func() { r.ch++ }})
}

// step pumps once and returns the result.
func (r *rig) step() time.Duration { return r.m.pump(context.Background()) }

func TestLookupFetchesAndCaches(t *testing.T) {
	r := newRig(t, true)
	if _, ok := r.m.Lookup("n0call"); ok {
		t.Fatal("first lookup must miss (never blocks)")
	}
	if r.f.count() != 0 {
		t.Fatal("Lookup made a request itself")
	}
	if w := r.step(); w != 0 {
		t.Fatalf("pump = %v", w)
	}
	in, ok := r.m.Lookup("N0CALL")
	if !ok || in.Name != "Pat Example" || in.First != "Pat" || in.Place() != "Springfield, XX" {
		t.Fatalf("got %+v %v", in, ok)
	}
	if r.ch != 1 {
		t.Fatalf("OnChange %d", r.ch)
	}
	// cache hit: no second request, nothing queued
	r.m.Lookup("N0CALL")
	if w := r.step(); w >= 0 || r.f.count() != 1 {
		t.Fatalf("w=%v hits=%d", w, r.f.count())
	}
	if r.f.hits[0] != "/v1/N0CALL/json/nodespot" || r.f.agents[0] != "nodespot/9.9.9" {
		t.Fatalf("%v %v", r.f.hits, r.f.agents)
	}
}

func TestNotFoundCachedForSevenDays(t *testing.T) {
	r := newRig(t, true)
	r.m.Lookup("N1CALL")
	r.step()
	if _, ok := r.m.Lookup("N1CALL"); ok {
		t.Fatal("not found must stay a miss")
	}
	if r.step() >= 0 || r.f.count() != 1 {
		t.Fatalf("asked again: %d", r.f.count())
	}
	r.clk.Add(6*24*time.Hour + time.Hour)
	r.m.Lookup("N1CALL")
	if r.step() >= 0 || r.f.count() != 1 {
		t.Fatal("asked again before 7 days")
	}
	r.clk.Add(24 * time.Hour)
	r.m.Lookup("N1CALL")
	if r.step() != 0 || r.f.count() != 2 {
		t.Fatalf("not asked after 7 days: %d", r.f.count())
	}
}

func TestFoundCachedForThirtyDays(t *testing.T) {
	r := newRig(t, true)
	r.m.Lookup("N0CALL")
	r.step()
	r.clk.Add(29 * 24 * time.Hour)
	if _, ok := r.m.Lookup("N0CALL"); !ok {
		t.Fatal("expired too early")
	}
	r.clk.Add(2 * 24 * time.Hour)
	if _, ok := r.m.Lookup("N0CALL"); ok {
		t.Fatal("served after 30 days")
	}
	if r.step() != 0 || r.f.count() != 2 {
		t.Fatal("not refreshed")
	}
}

func TestCacheSurvivesRestart(t *testing.T) {
	r := newRig(t, true)
	r.m.Lookup("N0CALL")
	r.step()
	m2 := r.make("http://192.0.2.1:1") // would fail if it asked
	if in, ok := m2.Lookup("N0CALL"); !ok || in.Name != "Pat Example" {
		t.Fatalf("after restart: %+v %v", in, ok)
	}
	if len(m2.reqs) != 1 {
		t.Fatalf("the daily count was not kept: %d", len(m2.reqs))
	}
}

func TestSettingOffNeverCalls(t *testing.T) {
	r := newRig(t, false)
	if r.m.Enabled() {
		t.Fatal("must be off by default")
	}
	r.m.Lookup("N0CALL")
	if r.step() >= 0 || r.f.count() != 0 {
		t.Fatal("asked while off")
	}
	// on, queue one, off again: dropped
	if err := r.m.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if r.set[SettingKey] != "true" {
		t.Fatal("not saved")
	}
	r.m.Lookup("N0CALL")
	_ = r.m.SetEnabled(false)
	if r.step() >= 0 || r.f.count() != 0 || r.set[SettingKey] != "false" {
		t.Fatal("asked after being turned off")
	}
}

func TestInvalidCallsSkipped(t *testing.T) {
	r := newRig(t, true)
	for _, c := range []string{"Echo test", "Private call", "1234567", "ABCDEF", "N0", "N0CALLXXXXXX", "N0/CALL", "", "N0 CALL"} {
		r.m.Lookup(c)
	}
	if r.step() >= 0 || r.f.count() != 0 {
		t.Fatal("an invalid callsign was looked up")
	}
	for _, c := range []string{"N0CALL", "n1call", "9A1AB", "KA1ABC"} {
		if !ValidCall(c) {
			t.Errorf("%s should be valid", c)
		}
	}
}

func TestDedupe(t *testing.T) {
	r := newRig(t, true)
	for i := 0; i < 5; i++ {
		r.m.Lookup("N0CALL")
		r.m.Lookup("n0call")
	}
	if len(r.m.queue) != 1 {
		t.Fatalf("queue %v", r.m.queue)
	}
	r.step()
	if r.step() >= 0 || r.f.count() != 1 {
		t.Fatalf("hits %d", r.f.count())
	}
}

func TestMinimumInterval(t *testing.T) {
	r := newRig(t, true)
	r.m.Lookup("N0CALL")
	r.m.Lookup("N1CALL")
	if r.step() != 0 {
		t.Fatal("first")
	}
	if w := r.step(); w != MinInterval {
		t.Fatalf("wait %v", w)
	}
	r.clk.Add(4 * time.Second)
	if w := r.step(); w != MinInterval-4*time.Second || r.f.count() != 1 {
		t.Fatalf("wait %v hits %d", w, r.f.count())
	}
	r.clk.Add(6 * time.Second)
	if r.step() != 0 || r.f.count() != 2 {
		t.Fatal("second not made after 10 s")
	}
}

func TestDailyCap(t *testing.T) {
	r := newRig(t, true)
	for i := 0; i < DailyMax; i++ {
		r.m.Lookup(fmt.Sprintf("N%dAB", i%10) + string(rune('A'+i/10)))
		if r.step() != 0 {
			t.Fatalf("request %d not made", i)
		}
		r.clk.Add(MinInterval)
	}
	if r.f.count() != DailyMax {
		t.Fatalf("hits %d", r.f.count())
	}
	r.m.Lookup("N5ZZZ")
	if r.step() >= 0 || r.f.count() != DailyMax || len(r.m.queue) != 0 {
		t.Fatal("the cap was passed")
	}
	if s := r.m.Status(); s.Today != DailyMax || s.DailyMax != DailyMax {
		t.Fatalf("%+v", s)
	}
	// the window slides: the first request is a day old
	r.clk.Add(24 * time.Hour)
	r.m.Lookup("N5ZZZ")
	if r.step() != 0 {
		t.Fatal("not allowed again after a day")
	}
}

func TestQueueDropsExtras(t *testing.T) {
	r := newRig(t, true)
	for i := 0; i < QueueMax+10; i++ {
		r.m.Lookup(fmt.Sprintf("N%dXY", i) + string(rune('A'+i)))
	}
	if len(r.m.queue) != QueueMax {
		t.Fatalf("queue %d", len(r.m.queue))
	}
	// a dropped one is not remembered: it is queued again when there is room
	r.step()
	r.m.Lookup("N9ZZZ")
	if len(r.m.queue) != QueueMax {
		t.Fatalf("queue after a slot freed: %d", len(r.m.queue))
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	r := newRig(t, true)
	r.f.status, r.f.header = 429, map[string]string{"Retry-After": "120"}
	r.m.Lookup("N0CALL")
	r.step()
	if r.f.count() != 1 {
		t.Fatal("no request")
	}
	if len(r.m.queue) != 1 {
		t.Fatal("the call must be asked again after the wait")
	}
	w := r.step()
	if w < 120*time.Second || w > 132*time.Second {
		t.Fatalf("wait %v, want 120 s plus at most 10%%", w)
	}
	r.f.status = 0
	r.clk.Add(w)
	if r.step() != 0 {
		t.Fatal("not retried after the wait")
	}
	if _, ok := r.m.Lookup("N0CALL"); !ok {
		t.Fatal("no answer after the retry")
	}
	if s := r.m.Status(); s.PausedUntil != "" {
		t.Fatalf("still paused: %+v", s)
	}
}

func TestBackoffWithoutRetryAfterGrowsAndIsCapped(t *testing.T) {
	r := newRig(t, true)
	r.f.status = 503
	r.m.Lookup("N0CALL")
	var last time.Duration
	for i := 1; i <= 4; i++ {
		r.step()
		w := r.step()
		// Rand is 0.5, so the factor is 1.0: 30 s, 60 s, 120 s, 240 s
		want := 30 * time.Second << uint(i-1)
		if w != want || w <= last {
			t.Fatalf("failure %d: wait %v want %v", i, w, want)
		}
		last = w
		r.clk.Add(w)
	}
	if got := r.m.jitter(10*time.Hour, 0.5, 1.5); got != MaxPause {
		t.Fatalf("jitter not capped: %v", got)
	}
}

func TestCircuitBreaker(t *testing.T) {
	r := newRig(t, true)
	r.f.status = 500
	for i := 0; i < BreakerAfter; i++ {
		r.m.Lookup(fmt.Sprintf("N%dAB", i))
	}
	for i := 0; i < BreakerAfter; i++ {
		if r.step() != 0 {
			t.Fatalf("request %d", i)
		}
		if i < BreakerAfter-1 {
			r.clk.Add(time.Hour) // longer than any back-off
		}
	}
	r.m.Lookup("N9ZZZ")
	w := r.step()
	if w < 50*time.Minute || w > time.Hour {
		t.Fatalf("breaker wait %v", w)
	}
	if r.f.count() != BreakerAfter {
		t.Fatalf("hits %d", r.f.count())
	}
	if s := r.m.Status(); s.PausedUntil == "" {
		t.Fatal("status does not show the pause")
	}
	r.f.status = 0
	r.clk.Add(w)
	if r.step() != 0 || r.f.count() != BreakerAfter+1 {
		t.Fatal("not resumed after the pause")
	}
}

func TestSuccessResetsFailures(t *testing.T) {
	r := newRig(t, true)
	r.f.status = 500
	r.m.Lookup("N1CALL")
	r.step()
	r.f.status = 0
	r.clk.Add(time.Hour)
	r.m.Lookup("N0CALL")
	r.step()
	if r.m.failures != 0 {
		t.Fatalf("failures %d", r.m.failures)
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if d := parseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now); d != 90*time.Second {
		t.Fatalf("%v", d)
	}
	if parseRetryAfter("soon", now) != 0 || parseRetryAfter("-5", now) != 0 {
		t.Fatal("bad values must be 0")
	}
}

func TestCacheBoundEvictsOldest(t *testing.T) {
	r := newRig(t, true)
	for i := 0; i < MaxEntries; i++ {
		r.m.cache[fmt.Sprintf("N%dA%d", i%10, i)] = entry{At: r.clk.Now().Unix() - int64(i)}
	}
	oldest := fmt.Sprintf("N%dA%d", (MaxEntries-1)%10, MaxEntries-1)
	r.m.Lookup("N0CALL")
	r.step()
	if len(r.m.cache) != MaxEntries {
		t.Fatalf("cache %d", len(r.m.cache))
	}
	if _, ok := r.m.cache[oldest]; ok {
		t.Fatal("the oldest entry was kept")
	}
	if _, ok := r.m.cache["N0CALL"]; !ok {
		t.Fatal("the new entry is missing")
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	r := newRig(t, true)
	r.f.status, r.f.header = 302, map[string]string{"Location": "http://203.0.113.9/x"}
	r.m.Lookup("N0CALL")
	r.step()
	if r.f.count() != 1 || r.m.failures != 1 {
		t.Fatalf("hits %d failures %d", r.f.count(), r.m.failures)
	}
}

func TestTidy(t *testing.T) {
	for in, want := range map[string]string{"SPRINGFIELD": "Springfield", "NEW  YORK": "New York", "McDonald": "McDonald", "": "", " Pat ": "Pat"} {
		if got := tidy(in); got != want {
			t.Errorf("tidy(%q) = %q, want %q", in, got, want)
		}
	}
	if p := (Info{City: "Exampleton", State: "XX", Country: "Exampleland"}).Place(); p != "Exampleton, XX, Exampleland" {
		t.Fatal(p)
	}
}
