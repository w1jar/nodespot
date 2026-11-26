// Package favstats polls the public AllStarLink stats API for the nodes on
// the favorites list and turns the answers into the Rx% / link-count /
// keyed-state activity shown next to each favorite (see docs/FAVSCAN.md).
//
// One poller serves every browser. It runs only while at least one
// logged-in client is connected, sends at most a few requests per minute
// (evenly spaced, one at a time; exponential backoff and an adaptive rate that
// drops after every HTTP 429 and recovers slowly) and keeps its results in memory.
// The raw response is never stored, logged or forwarded: it is decoded
// straight into the few fields used here, and the linkedNodes array (other
// operators' registration records) is skipped without being read.
package favstats

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults and limits.
const (
	DefaultBaseURL = "https://stats.allstarlink.org"
	DefaultRate    = 20 // requests per minute
	MaxRate        = 25 // the API documents 30 per minute per address, but answers 429 well below that
	MinRate        = 1

	adaptFloor  = 3  // the effective rate never drops below this (or the configured rate, if lower)
	adaptStreak = 20 // consecutive successful requests that raise the effective rate by 1

	minGap      = 3 * time.Second  // requests are never closer than this, whatever the rate
	backoffMin  = 15 * time.Second // first wait after a 429 or an error
	backoffMax  = 5 * time.Minute
	keepFor     = 10 * time.Minute // values are kept this long after the last viewer left
	slack       = time.Second      // a request this late is still on schedule (no drift)
	maxBody     = 4 << 20          // larger responses are refused (errTooLarge)
	skipFor     = 10 * time.Minute // a node whose answer was unusable is not asked again sooner
	reqTimeout  = 5 * time.Second
	nodeMin     = 2000   // numbers below 2000 are private nodes
	nodeMax     = 999999 // 7-digit numbers (3000000..3999999 = EchoLink) are skipped
	maxTxPlaus  = 2      // seconds of slack when judging a TX time delta
	minUptime   = 60
	minTxForPct = 10
)

// Activity is what is known about one favorite node.
type Activity struct {
	// RxPct is round(100*totaltxtime/apprptuptime), or nil ("-") while the
	// node has not been up or transmitting long enough for it to mean anything.
	RxPct   *int `json:"rxPct"`
	LinkCnt int  `json:"linkCnt"`
	// Active and WebTx come from the node's own registration (Status
	// "Active", access_webtransceiver).
	Active bool `json:"active"`
	WebTx  bool `json:"webTx"`
	// Keyed is derived from changes of the TX counters between distinct reports.
	Keyed bool `json:"keyed"`
	// TxAvg is a moving average (0..100) of the recent TX share.
	TxAvg    int       `json:"txAvg"`
	LastSeen time.Time `json:"lastSeen"`
}

// same reports whether two activities show the same thing (LastSeen aside).
func (a Activity) same(b Activity) bool {
	a.LastSeen, b.LastSeen = time.Time{}, time.Time{}
	if (a.RxPct == nil) != (b.RxPct == nil) || (a.RxPct != nil && *a.RxPct != *b.RxPct) {
		return false
	}
	a.RxPct, b.RxPct = nil, nil
	return a == b
}

// Update is the snapshot (GET /api/favorites/stats) and, with only the
// changed entries in Stats, the SSE "favstats" event.
type Update struct {
	Enabled bool `json:"enabled"`
	// Rate is the configured requests per minute; EffRate the rate in use
	// right now (lowered after a 429, raised again after successes).
	Rate    int `json:"rate"`
	EffRate int `json:"effRate"`
	// Backoff is true while polling is paused after a 429 or an error;
	// RetryAt is when it ends.
	Backoff bool       `json:"backoff"`
	RetryAt *time.Time `json:"retryAt"`
	// BackoffReason says why: "rate-limit" (HTTP 429), "service-error"
	// (HTTP 5xx) or "network-error" (timeout, connection error); "" when
	// not backing off.
	BackoffReason string `json:"backoffReason"`
	// Updated is the time of the last successful poll (null: none yet).
	// Kept for compatibility; the UI uses CheckedAt and NextAt instead.
	Updated *time.Time          `json:"updated"`
	Stats   map[string]Activity `json:"stats"`
	// Checking is the node being requested right now ("" when idle).
	// Kept for compatibility; the UI marks Next instead (a request takes a
	// fraction of a second, so a "checking now" mark only flashes).
	Checking string `json:"checking"`
	// Next is the node that will be requested next, chosen by the same
	// ordering the poller uses (never-asked first, then the one asked longest
	// ago, skipping nodes in their skip period; the node being requested
	// right now counts as asked). "" when the scan is off, nobody is
	// watching, nothing is eligible or every node is in its skip period.
	Next string `json:"next"`
	// NextAt is when the next request is due (the end of a backoff if that
	// is later); null while the scan is off, nobody is watching or there is
	// nothing to check.
	NextAt *time.Time `json:"nextAt"`
	// Total is the number of eligible favorites (valid node number, not one
	// of this machine's nodes, not a duplicate). Checked is how many of them
	// have an answer in the current pass: tried since the pass began, or in
	// their skip period after an unusable or offline answer. A pass ends when
	// every node that is not in its skip period has been tried; the next
	// request then starts a new pass and the count restarts.
	Checked int `json:"checked"`
	Total   int `json:"total"`
	// CheckedAt is the time of each node's last attempt, including offline
	// and no-data answers (which have no Stats entry). Events carry only the
	// nodes that changed.
	CheckedAt map[string]time.Time `json:"checkedAt"`
	// Removed lists nodes whose values were just cleared (offline, unknown,
	// no longer a favorite). Only the SSE event carries it; clients delete
	// these from what they hold. Always an array, empty in snapshots.
	Removed []string `json:"removed"`
}

// Sample is one decoded stats response.
type Sample struct {
	Uptime, TxTime, KeyUps float64
	HaveCounters           bool // uptime, txtime and keyups were all present
	Keyed                  bool // as reported (unreliable)
	Links                  int
	Active, WebTx          bool
	// DataTime is stats.data.time (unix seconds), 0 if absent. Active and
	// WebTx come from the registration record, which says "Active" even for
	// an offline node, so they only count while the data is live.
	DataTime float64
}

// liveWindow is how far stats.data.time may be from now for the node to count as online.
const liveWindow = 15 * time.Minute

// HTTPError is a non-200 answer.
type HTTPError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// Fetcher returns the sample for one node.
type Fetcher func(ctx context.Context, node string) (Sample, error)

// Options configure a Service. Zero values pick the defaults.
type Options struct {
	BaseURL   string
	UserAgent string
	Client    *http.Client
	Logger    *log.Logger
	// Now is the clock (tests inject a fake one).
	Now func() time.Time
	// Fetch replaces the HTTP fetcher (demo mode).
	Fetch Fetcher
	// NoLimit disables the pacing (tests only).
	NoLimit bool
	// Interval, if set, replaces the computed gap between requests (demo
	// mode, which has no rate limit to respect).
	Interval time.Duration
	// LogRequests logs one line per stats request and every rate change
	// ([favstats] log). Only the node, status, timing, rate and Retry-After
	// are logged, never anything from the response.
	LogRequests bool
}

// Service is the poller. Create it with New, set Favorites and Local, then
// call Run. All methods are safe for concurrent use.
type Service struct {
	// Favorites returns the favorite node numbers (in any order, with
	// duplicates); Local returns this machine's own node numbers, which are
	// never queried.
	Favorites func() []string
	Local     func() []string
	// OnUpdate receives every update (changes only, plus a periodic
	// heartbeat). It is called without locks held.
	OnUpdate func(Update)

	fetch    Fetcher
	now      func() time.Time
	log      *log.Logger
	noLimit  bool
	fixedGap time.Duration
	logReqs  bool

	mu         sync.Mutex
	enabled    bool
	rate       int
	effRate    int // adaptive, 0 = not set (use rate)
	okStreak   int
	lastTry    map[string]time.Time // last non-429 answer per node, for the polling order
	skipUntil  map[string]time.Time // nodes with an unusable answer: not queried before this time
	viewers    int
	idleSince  time.Time
	stats      map[string]Activity
	prev       map[string]counters
	updated    time.Time
	nextDue    time.Time // when the next request is due (even pacing)
	nextOK     time.Time // backoff end
	backoff    time.Duration
	backReason string    // why the current backoff runs
	checking   string    // node being requested
	sentNext   string    // Next of the last update that was emitted
	passStart  time.Time // start of the current pass over the favorites
}

type counters struct {
	tx, ku float64
	at     time.Time
	avg    int
	dtime  float64 // DataTime of the report the counters came from (0: unknown)
	keyed  bool    // derived keyed state for that report, repeated for the same report
}

// New builds a Service (disabled, no viewers).
func New(o Options) *Service {
	s := &Service{now: o.Now, log: o.Logger, noLimit: o.NoLimit, fixedGap: o.Interval, logReqs: o.LogRequests, rate: DefaultRate, effRate: DefaultRate,
		stats: map[string]Activity{}, prev: map[string]counters{}, lastTry: map[string]time.Time{}, skipUntil: map[string]time.Time{}}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = log.Default()
	}
	s.fetch = o.Fetch
	if s.fetch == nil {
		s.fetch = httpFetcher(o)
	}
	return s
}

// ValidNode reports whether node is an AllStarLink number the stats API
// lists: regular node numbers, 2000..999999. Numbers below 2000 are private
// and the 7-digit numbers 3000000..3999999 are EchoLink (3 + the EchoLink id).
func ValidNode(node string) bool {
	if node == "" || len(node) > 7 {
		return false
	}
	n, err := strconv.Atoi(node)
	return err == nil && strconv.Itoa(n) == node && n >= nodeMin && n <= nodeMax
}

// ClampRate limits a rate setting to MinRate..MaxRate.
func ClampRate(r int) int {
	if r < MinRate {
		return MinRate
	}
	if r > MaxRate {
		return MaxRate
	}
	return r
}

// SetEnabled turns the scan on or off. Turning it off forgets all values.
func (s *Service) SetEnabled(on bool) {
	list, _ := s.eligible()
	s.mu.Lock()
	changed := s.enabled != on
	s.enabled = on
	if changed && !on {
		s.stats, s.prev = map[string]Activity{}, map[string]counters{}
		s.lastTry, s.skipUntil = map[string]time.Time{}, map[string]time.Time{}
		s.updated = time.Time{}
		s.nextOK, s.backoff, s.backReason = time.Time{}, 0, ""
		s.nextDue, s.passStart, s.checking = time.Time{}, time.Time{}, ""
		s.effRate, s.okStreak = s.rate, 0
	}
	u := s.updateLocked(nil, nil, s.now(), list)
	if changed {
		s.sentNext = u.Next
	}
	s.mu.Unlock()
	if changed {
		s.emit(u)
	}
}

// Refresh re-evaluates which node is next (call it when the favorites or this
// machine's nodes changed) and emits an update if that is not what clients
// were last told.
func (s *Service) Refresh() {
	list, _ := s.eligible()
	s.mu.Lock()
	u := s.updateLocked(nil, nil, s.now(), list)
	if u.Next == s.sentNext {
		s.mu.Unlock()
		return
	}
	s.sentNext = u.Next
	s.mu.Unlock()
	s.emit(u)
}

// SetRate sets the configured request budget per minute (clamped to 1..25)
// and resets the adaptive rate to it.
func (s *Service) SetRate(perMin int) {
	s.mu.Lock()
	s.rate = ClampRate(perMin)
	s.effRate, s.okStreak = s.rate, 0
	s.mu.Unlock()
}

// floorLocked is the lowest effective rate.
func (s *Service) floorLocked() int {
	if s.rate < adaptFloor {
		return s.rate
	}
	return adaptFloor
}

// effLocked is the request rate in use (never above the configured rate).
func (s *Service) effLocked() int {
	if s.effRate <= 0 || s.effRate > s.rate {
		return s.rate
	}
	return s.effRate
}

// SetViewers tells the poller how many logged-in clients are connected.
func (s *Service) SetViewers(n int) {
	s.mu.Lock()
	if n > 0 {
		s.idleSince = time.Time{}
	} else if s.viewers > 0 || s.idleSince.IsZero() {
		s.idleSince = s.now()
	}
	s.viewers = n
	s.mu.Unlock()
}

// Snapshot returns the full current state.
func (s *Service) Snapshot() Update {
	list, _ := s.eligible()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expireLocked(now)
	all := make(map[string]Activity, len(s.stats))
	for k, v := range s.stats {
		all[k] = v
	}
	at := make(map[string]time.Time, len(list))
	for _, n := range list {
		if t, ok := s.lastTry[n]; ok {
			at[n] = t
		}
	}
	return s.updateLocked(all, at, now, list)
}

// updateLocked builds an Update; stats and checkedAt are the (changed or all)
// entries to carry, list the eligible favorites.
func (s *Service) updateLocked(stats map[string]Activity, checkedAt map[string]time.Time, now time.Time, list []string) Update {
	u := Update{Enabled: s.enabled, Rate: s.rate, EffRate: s.effLocked(), Backoff: now.Before(s.nextOK), Stats: stats,
		CheckedAt: checkedAt, Checking: s.checking, Total: len(list)}
	if u.CheckedAt == nil {
		u.CheckedAt = map[string]time.Time{}
	}
	for _, n := range list {
		if t, ok := s.lastTry[n]; (ok && !t.Before(s.passStart)) || now.Before(s.skipUntil[n]) {
			u.Checked++
		}
	}
	if s.enabled && s.viewers > 0 && len(list) > 0 {
		u.Next = s.pickLocked(list, now)
		t := s.nextDue
		if s.nextOK.After(t) {
			t = s.nextOK
		}
		if t.Before(now) {
			t = now
		}
		u.NextAt = &t
	}
	if u.Backoff {
		t := s.nextOK
		u.RetryAt = &t
		u.BackoffReason = s.backReason
	}
	if u.Stats == nil {
		u.Stats = map[string]Activity{}
	}
	u.Removed = []string{}
	if !s.updated.IsZero() {
		t := s.updated
		u.Updated = &t
	}
	return u
}

func (s *Service) expireLocked(now time.Time) {
	if s.viewers == 0 && !s.idleSince.IsZero() && now.Sub(s.idleSince) >= keepFor && len(s.stats) > 0 {
		s.stats, s.prev = map[string]Activity{}, map[string]counters{}
		s.lastTry, s.skipUntil = map[string]time.Time{}, map[string]time.Time{}
		s.updated = time.Time{}
	}
}

func (s *Service) emit(u Update) {
	if s.OnUpdate != nil {
		s.OnUpdate(u)
	}
}

// Run polls until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// eligible returns the distinct valid favorites that are not local nodes, in
// favorites order.
func (s *Service) eligible() ([]string, map[string]bool) {
	var favs, local []string
	if s.Favorites != nil {
		favs = s.Favorites()
	}
	if s.Local != nil {
		local = s.Local()
	}
	skip := map[string]bool{}
	for _, n := range local {
		skip[n] = true
	}
	seen := map[string]bool{}
	var list []string
	for _, n := range favs {
		n = strings.TrimSpace(n)
		if !ValidNode(n) || skip[n] || seen[n] {
			continue
		}
		seen[n] = true
		list = append(list, n)
	}
	return list, seen
}

// next picks the next node to query and returns it with the list of
// eligible favorites, or "". Nodes never queried come first (in favorites
// order), then the one answered longest ago, so a backoff does not starve the
// end of the list. Nodes in their skip period (unusable answer) are passed
// over; the node is "" if all are.
func (s *Service) next(now time.Time) (string, []string) {
	list, _ := s.eligible()
	if len(list) == 0 {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pickLocked(list, now), list
}

// pickLocked is the polling order: the node that is requested next out of
// list. It is used both to send the request and to announce it (Update.Next),
// so the two cannot disagree. A node being requested right now is treated as
// just answered, so the announcement moves on when a request starts.
func (s *Service) pickLocked(list []string, now time.Time) string {
	best := ""
	var tb time.Time
	okB := false
	for _, n := range list {
		if now.Before(s.skipUntil[n]) {
			continue
		}
		tn, okN := s.lastTry[n]
		if n == s.checking {
			tn, okN = now, true
		}
		if best == "" || (!okN && okB) || (okN && okB && tn.Before(tb)) {
			best, tb, okB = n, tn, okN
		}
	}
	return best
}

// gapLocked is the time between two requests: 60/effRate seconds, at least minGap.
func (s *Service) gapLocked() time.Duration {
	if s.fixedGap > 0 {
		return s.fixedGap
	}
	g := time.Minute / time.Duration(s.effLocked())
	if g < minGap {
		g = minGap
	}
	return g
}

// Tick does one scheduling step: it sends at most one request, if the scan
// is on, someone is watching, no backoff is running and the next request is
// due. Requests go out evenly, one every 60/effRate seconds (never closer
// than minGap), measured from the schedule rather than from when the last
// one finished. Run calls it every 250 ms; tests call it with a fake clock.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	s.expireLocked(now)
	if !s.enabled || s.viewers == 0 || now.Before(s.nextOK) || (!s.noLimit && now.Before(s.nextDue)) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	node, list := s.next(now)
	if node == "" {
		if len(list) == 0 {
			s.prune(nil)
		}
		return
	}
	s.mu.Lock()
	if !s.enabled {
		s.mu.Unlock()
		return
	}
	// Every node that can be asked has been tried in this pass: start a new one.
	if t, ok := s.lastTry[node]; ok && !t.Before(s.passStart) {
		s.passStart = now
	}
	base := now
	if !s.nextDue.IsZero() && now.Sub(s.nextDue) < slack {
		base = s.nextDue // on schedule: no drift from the polling tick
	}
	gap, eff := s.gapLocked(), s.effLocked()
	s.nextDue = base.Add(gap)
	s.checking = node
	u := s.updateLocked(nil, nil, now, list)
	s.sentNext = u.Next
	s.mu.Unlock()
	s.emit(u)

	smp, err := s.fetch(ctx, node)
	if s.logReqs && !ctxDone(err) {
		s.logRequest(node, err, s.now().Sub(now), eff, gap)
	}
	s.finish(node, list, smp, err)
}

// logRequest writes the one-line request log ([favstats] log = true).
func (s *Service) logRequest(node string, err error, took time.Duration, eff int, gap time.Duration) {
	status, ra := "200", "-"
	var he *HTTPError
	switch {
	case errors.As(err, &he):
		status = strconv.Itoa(he.Status)
		if he.RetryAfter > 0 {
			ra = strconv.Itoa(int(he.RetryAfter/time.Second)) + "s"
		}
	case errors.Is(err, errTooLarge):
		status = "too-large"
	case errors.Is(err, errNoData):
		status = "200-nodata"
	case isDecodeErr(err):
		status = "decode-error"
	case err != nil:
		status = errKind(err)
	}
	s.log.Printf("favstats: GET node=%s status=%s took=%dms rate=%d/min gap=%s retry-after=%s",
		node, status, took.Milliseconds(), eff, gap, ra)
}

func (s *Service) prune(keep []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, n := range keep {
		want[n] = true
	}
	for n := range s.stats {
		if !want[n] {
			delete(s.stats, n)
			delete(s.prev, n)
		}
	}
}

func (s *Service) finish(node string, list []string, smp Sample, err error) {
	now := s.now()
	s.mu.Lock()
	s.checking = ""
	if !s.enabled || ctxDone(err) {
		s.mu.Unlock()
		return
	}
	prevTry, hadTry := s.lastTry[node]
	var changed map[string]Activity
	var removed []string
	// drop forgets a node's values and notes it for the SSE event.
	drop := func(n string) {
		if _, ok := s.stats[n]; ok {
			removed = append(removed, n)
		}
		delete(s.stats, n)
		delete(s.prev, n)
	}
	var he *HTTPError
	_ = errors.As(err, &he)
	switch {
	case err == nil:
		s.backoff, s.nextOK, s.backReason = 0, time.Time{}, ""
		delete(s.skipUntil, node)
		s.lastTry[node] = now
		s.succeedLocked()
		s.updated = now
		a := s.compute(node, smp, now)
		if old, ok := s.stats[node]; !ok || !old.same(a) {
			changed = map[string]Activity{node: a}
		}
		s.stats[node] = a
	case nodeFailure(err):
		// This node's answer is unusable (decode error, too large, 404 or
		// another 4xx): skip just this node for a while, at the back of the
		// queue. No global pause; the success streak is left alone.
		s.lastTry[node] = now
		s.skipUntil[node] = now.Add(skipFor)
		if he != nil && he.Status == http.StatusNotFound {
			// Unknown to the stats service: drop its values.
			drop(node)
		}
		if errors.Is(err, errNoData) {
			// Offline or unknown node: forget its values, check again later, say nothing.
			drop(node)
		} else {
			s.log.Printf("favstats: node %s: %s, skipping for %s", node, failText(err), skipFor)
		}
	default:
		wait := backoffMin
		if s.backoff > 0 {
			wait = s.backoff * 2
		}
		if he != nil && he.RetryAfter > wait {
			wait = he.RetryAfter
		}
		if wait > backoffMax {
			wait = backoffMax
		}
		s.backoff, s.nextOK = wait, now.Add(wait)
		switch {
		case he != nil && he.Status == http.StatusTooManyRequests:
			s.backReason = "rate-limit"
		case he != nil:
			s.backReason = "service-error"
		default:
			s.backReason = "network-error"
		}
		s.okStreak = 0
		if he != nil && he.Status == http.StatusTooManyRequests {
			// A 429 is not an answer about the node: keep its values and
			// leave it first in line. Slow down.
			s.lowerLocked()
		} else {
			s.lastTry[node] = now
		}
		if he != nil {
			s.log.Printf("favstats: stats service answered HTTP %d, pausing %s", he.Status, wait)
		} else {
			s.log.Printf("favstats: request failed (%s), pausing %s", errKind(err), wait)
		}
	}
	// Forget nodes that are no longer favorites.
	want := map[string]bool{}
	for _, n := range list {
		want[n] = true
	}
	for n := range s.stats {
		if !want[n] {
			drop(n)
		}
	}
	for n := range s.lastTry {
		if !want[n] {
			delete(s.lastTry, n)
			delete(s.skipUntil, n)
		}
	}
	var checkedAt map[string]time.Time
	if t, ok := s.lastTry[node]; ok && (!hadTry || !t.Equal(prevTry)) {
		checkedAt = map[string]time.Time{node: t}
	}
	u := s.updateLocked(changed, checkedAt, now, list)
	s.sentNext = u.Next
	if len(removed) > 0 {
		sort.Strings(removed)
		u.Removed = removed
	}
	s.mu.Unlock()
	s.emit(u)
}

// lowerLocked halves the effective rate after a 429 (not below the floor).
func (s *Service) lowerLocked() {
	old := s.effLocked()
	n := old / 2
	if f := s.floorLocked(); n < f {
		n = f
	}
	s.effRate = n
	if s.logReqs && n != old {
		s.log.Printf("favstats: rate lowered to %d/min after 429", n)
	}
}

// succeedLocked counts a successful request; every adaptStreak in a row
// raise the effective rate by 1/min, up to the configured rate.
func (s *Service) succeedLocked() {
	s.okStreak++
	if s.okStreak < adaptStreak {
		return
	}
	s.okStreak = 0
	if old := s.effLocked(); old < s.rate {
		s.effRate = old + 1
		if s.logReqs {
			s.log.Printf("favstats: rate raised to %d/min", old+1)
		}
	}
}

func ctxDone(err error) bool {
	return errors.Is(err, context.Canceled)
}

// errTooLarge is returned when a response body exceeds maxBody.
var errTooLarge = errors.New("response too large")

// errNoData means the service answered 200 but has nothing about the node
// (empty body, null, {}, no stats data, an error object, an HTML page): the
// node is offline or unknown. It is not a failure.
var errNoData = errors.New("no node data")

// DecodeError is a body that is not usable JSON (as opposed to a transport
// error while reading it). Its text is the encoding/json error message,
// which names at most an offset or one character, never body content.
type DecodeError struct{ Err error }

func (e *DecodeError) Error() string { return e.Err.Error() }
func (e *DecodeError) Unwrap() error { return e.Err }

func isDecodeErr(err error) bool {
	var de *DecodeError
	return errors.As(err, &de)
}

// nodeFailure reports whether err concerns only the queried node (unusable
// answer) rather than the stats service or the network: decode errors,
// too-large bodies and HTTP answers other than 429 and 5xx.
func nodeFailure(err error) bool {
	if err == nil {
		return false
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status != http.StatusTooManyRequests && he.Status < 500
	}
	return errors.Is(err, errTooLarge) || isDecodeErr(err) || errors.Is(err, errNoData)
}

// failText is the reason shown in the per-node failure log line.
func failText(err error) string {
	var he *HTTPError
	switch {
	case errors.Is(err, errTooLarge):
		return fmt.Sprintf("response too large (>%d MB)", maxBody>>20)
	case isDecodeErr(err):
		return "invalid response (" + cleanReason(err.Error()) + ")"
	case errors.As(err, &he):
		return fmt.Sprintf("HTTP %d", he.Status)
	}
	return cleanReason(err.Error())
}

// cleanReason makes an error text safe for one log line: printable
// characters only, at most 120 of them.
func cleanReason(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == 120 {
			b.WriteString("...")
			break
		}
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// errKind describes a transport error without echoing URLs or addresses.
func errKind(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timeout"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return "connection error"
	}
	return "error"
}

// compute derives the Activity for one sample and updates the counters.
func (s *Service) compute(node string, smp Sample, now time.Time) Activity {
	a := Activity{LinkCnt: smp.Links, Active: smp.Active, WebTx: smp.WebTx, LastSeen: now}
	// Stale data (older than liveWindow): the node is not online, so it is
	// neither Active nor keyed, even when its last report said it was keyed.
	stale := smp.DataTime > 0 && math.Abs(float64(now.Unix())-smp.DataTime) > liveWindow.Seconds()
	if stale {
		a.Active, a.WebTx = false, false
	}
	a.RxPct = RxPct(smp.Uptime, smp.TxTime)
	p, had := s.prev[node]
	c := counters{tx: smp.TxTime, ku: smp.KeyUps, at: now, avg: p.avg, dtime: smp.DataTime}
	switch {
	case !smp.HaveCounters:
		// Nothing to derive from; fall back to the reported flag.
		a.Keyed = smp.Keyed && !stale
		c = p
		c.at = p.at
	case had && smp.DataTime > 0 && smp.DataTime == p.dtime:
		// The service publishes a new report only about every 30 s: the same
		// report again. Repeat the derived state and keep the stored counters.
		a.Keyed = p.keyed && !stale
		a.TxAvg = p.avg
		return a
	case !had:
		// First sample: no deltas yet; the reported flag is all there is.
		a.Keyed = smp.Keyed && !stale
		if a.Keyed {
			c.avg = 100
		}
	default:
		dt := now.Sub(p.at).Seconds()
		if smp.DataTime > 0 && p.dtime > 0 {
			dt = smp.DataTime - p.dtime // time between the two reports
		}
		dTx, dKu := smp.TxTime-p.tx, smp.KeyUps-p.ku
		if dTx < 0 || dKu < 0 || dt <= 0 || dTx > dt+maxTxPlaus {
			// Counter reset or an implausible jump: count as no activity.
			dTx, dKu = 0, 0
		}
		a.Keyed = (dTx > 0 || dKu > 0) && !stale
		txp := 0
		if dt > 0 {
			txp = int(math.Min(100, math.Round(100*dTx/dt)))
		}
		if a.Keyed {
			c.avg = 100
		} else {
			c.avg = int(math.Round(float64(p.avg)/2 + float64(txp)/2))
		}
	}
	a.TxAvg = c.avg
	c.keyed = a.Keyed
	if smp.HaveCounters || had {
		s.prev[node] = c
	}
	return a
}

// RxPct is the lifetime TX time as a percentage of uptime: nil unless the
// node has been up for more than a minute and transmitted for more than 10 s.
func RxPct(uptime, tx float64) *int {
	if uptime > minUptime && tx > minTxForPct {
		v := int(math.Round(100 * tx / uptime))
		return &v
	}
	return nil
}

// ------------------------------------------------------------------ HTTP

func httpFetcher(o Options) Fetcher {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	ua := o.UserAgent
	if ua == "" {
		ua = "nodespot"
	}
	c := o.Client
	if c == nil {
		c = &http.Client{Timeout: reqTimeout}
	}
	return func(ctx context.Context, node string) (Sample, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/stats/"+url.PathEscape(node), nil)
		if err != nil {
			return Sample{}, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return Sample{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			he := &HTTPError{Status: resp.StatusCode}
			if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
				he.RetryAfter = time.Duration(secs) * time.Second
			}
			return Sample{}, he
		}
		if resp.ContentLength > maxBody {
			return Sample{}, errTooLarge
		}
		return Decode(&capReader{r: resp.Body, left: maxBody})
	}
}

// capReader passes through at most left bytes and fails with errTooLarge
// when the body has more (instead of silently truncating it).
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var b [1]byte
		n, err := c.r.Read(b[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// scalar is one JSON value read leniently: string, number, bool, null, or
// a composite (object/array) that was skipped.
type scalar struct {
	kind byte // 's' string, 'n' number, 'b' bool, 'z' null, 'c' composite
	str  string
	b    bool
}

// float is the value as a finite number; strings holding numbers count.
func (v scalar) float() (float64, bool) {
	if v.kind != 's' && v.kind != 'n' {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v.str), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// truthy accepts true, non-zero numbers and "1"/"true"/"yes".
func (v scalar) truthy() bool {
	switch v.kind {
	case 'b':
		return v.b
	case 'n':
		f, ok := v.float()
		return ok && f != 0
	case 's':
		switch strings.ToLower(strings.TrimSpace(v.str)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}

// text is the value as text (empty for null, bool and composites).
func (v scalar) text() string {
	if v.kind == 's' || v.kind == 'n' {
		return v.str
	}
	return ""
}

// skipRest consumes tokens up to the close of the composite just opened.
// Nothing is retained.
func skipRest(dec *json.Decoder) error {
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// readScalar reads one value of any type; composites are skipped.
func readScalar(dec *json.Decoder) (scalar, error) {
	t, err := dec.Token()
	if err != nil {
		return scalar{}, err
	}
	switch v := t.(type) {
	case json.Delim:
		if v == '{' || v == '[' {
			return scalar{kind: 'c'}, skipRest(dec)
		}
	case string:
		return scalar{kind: 's', str: v}, nil
	case json.Number:
		return scalar{kind: 'n', str: v.String()}, nil
	case bool:
		return scalar{kind: 'b', b: v}, nil
	}
	return scalar{kind: 'z'}, nil
}

func skipValue(dec *json.Decoder) error {
	_, err := readScalar(dec)
	return err
}

// walkObject reads one value. If it is an object, fn is called for each key
// and must consume exactly one value; any other type (null, number, array...)
// is consumed and fn is never called.
func walkObject(dec *json.Decoder, fn func(key string) error) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, isDelim := t.(json.Delim)
	if !isDelim || d != '{' {
		if isDelim && d == '[' {
			return skipRest(dec)
		}
		return nil
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		if err := fn(key); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// readLinks counts the elements of a "links" array (elements of any type,
// without keeping them); haveLinks is false for anything but an array.
func readLinks(dec *json.Decoder) (n int, haveLinks bool, err error) {
	t, err := dec.Token()
	if err != nil {
		return 0, false, err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return 0, false, nil
	}
	if d != '[' {
		return 0, false, skipRest(dec)
	}
	for dec.More() {
		if err := skipValue(dec); err != nil {
			return 0, false, err
		}
		n++
	}
	_, err = dec.Token()
	return n, true, err
}

// Decode reads one stats response from r (already size-limited). It is
// tolerant: each field it reads may be a string, number, bool or null, and
// a field of the wrong type is simply left zero. Everything else, notably
// the linkedNodes array, is skipped token by token without being kept.
// Errors are *DecodeError (not JSON, or no usable data), errTooLarge
// (passed through from the reader) or the reader's own error.
func Decode(r io.Reader) (Sample, error) {
	br := bufio.NewReader(r)
	for {
		c, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return Sample{}, errNoData // empty body
			}
			return Sample{}, err
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			continue
		}
		if c == '<' {
			return Sample{}, errNoData // an HTML page
		}
		_ = br.UnreadByte()
		break
	}
	dec := json.NewDecoder(br)
	dec.UseNumber()
	var smp Sample
	var up, tx, ku bool
	var status string
	var webTx, haveLinks bool
	err := walkObject(dec, func(key string) error {
		switch key {
		case "stats":
			return walkObject(dec, func(k string) error {
				if k != "data" {
					return skipValue(dec)
				}
				return walkObject(dec, func(f string) error {
					if f == "links" {
						n, ok, err := readLinks(dec)
						smp.Links, haveLinks = n, ok
						return err
					}
					v, err := readScalar(dec)
					if err != nil {
						return err
					}
					switch f {
					case "apprptuptime":
						smp.Uptime, up = v.float()
					case "totaltxtime":
						smp.TxTime, tx = v.float()
					case "totalkeyups":
						smp.KeyUps, ku = v.float()
					case "keyed":
						smp.Keyed = v.truthy()
					case "time":
						smp.DataTime, _ = v.float()
					}
					return nil
				})
			})
		case "node":
			return walkObject(dec, func(f string) error {
				v, err := readScalar(dec)
				if err != nil {
					return err
				}
				switch f {
				case "Status":
					status = v.text()
				case "access_webtransceiver":
					webTx = v.truthy()
				}
				return nil
			})
		}
		return skipValue(dec)
	})
	if err != nil {
		return Sample{}, decodeErr(err)
	}
	smp.HaveCounters = up && tx && ku
	if !up && !tx && !ku && !haveLinks {
		return Sample{}, errNoData
	}
	smp.Active = strings.EqualFold(status, "Active")
	smp.WebTx = webTx
	return smp, nil
}

// decodeErr wraps JSON syntax problems; a reader error (timeout, reset,
// errTooLarge) is returned as it is.
func decodeErr(err error) error {
	var se *json.SyntaxError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &DecodeError{errors.New("unexpected end of response")}
	case errors.As(err, &se):
		return &DecodeError{err}
	}
	return err
}
