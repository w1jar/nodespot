// Package hamdb looks up the name and place of a callsign on hamdb.org, as a
// fallback for the callsign-only digital modes (YSF, P25, NXDN, M17) when the
// radioid.net list has no entry.
//
// The service is a free public one that many nodes may use, so the lookup is
// opt-in and deliberately gentle:
//
//   - it is off until an admin turns it on;
//   - it only asks for a callsign that was actually heard, that looks like an
//     amateur callsign, and that is neither cached nor already queued;
//   - answers are kept on disk (found 30 days, not found 7 days, at most 5000
//     entries, oldest dropped) and survive a restart;
//   - one request at a time, at least 10 s apart, at most 100 a day, and a
//     queue of 20 that drops what does not fit;
//   - 429 and 503 are waited out (Retry-After, else exponential back-off with
//     jitter, at most 1 h), and 5 failures in a row pause everything for 1 h.
//
// There are no timers: a lookup is started by a heard callsign, and the only
// waits are the delays above.
package hamdb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// SettingKey is the settings table key of the switch; the lookup is off
	// unless the value is "true".
	SettingKey = "hamdb.enabled"
	// FileName is the cache file in the data directory.
	FileName = "hamdb-cache.json"
	// DefaultBaseURL is the service. The documented path /<call>/json/<app>
	// answers with a redirect to /v1/<call>/json/<app>, so the latter is used.
	DefaultBaseURL = "https://api.hamdb.org"
	// AppName is the app name hamdb.org asks to be sent in the URL.
	AppName = "nodespot"

	FoundTTL    = 30 * 24 * time.Hour
	NotFoundTTL = 7 * 24 * time.Hour
	MaxEntries  = 5000
	QueueMax    = 20
	DailyMax    = 100
	MinInterval = 10 * time.Second
	Timeout     = 5 * time.Second
	// MaxPause caps the exponential back-off and is the circuit breaker's pause.
	MaxPause = time.Hour
	// MaxRetryAfter caps a server's Retry-After.
	MaxRetryAfter = 24 * time.Hour
	// BreakerAfter is the number of failures in a row that opens the breaker.
	BreakerAfter = 5
	backoffBase  = 30 * time.Second
	maxBody      = 64 << 10
)

// Settings is the part of the store the switch is kept in.
type Settings interface {
	GetSetting(k string) (string, error)
	SetSetting(k, v string) error
}

// Options configure a Manager.
type Options struct {
	// Dir is the data directory the cache is kept in (required for a cache that survives a restart).
	Dir     string
	BaseURL string // default DefaultBaseURL
	Version string // for the User-Agent "nodespot/<version>"
	Client  *http.Client
	// Settings holds the switch; nil: off and not persisted.
	Settings Settings
	Logger   *log.Logger
	Now      func() time.Time
	// Rand returns a number in [0, 1) for the jitter.
	Rand func() float64
	// OnChange is called (without locks held) after a new answer was cached.
	OnChange func()
}

// Info is what hamdb.org knows about a callsign.
type Info struct {
	// First is the first name ("" for a club), Name the full name.
	First   string
	Name    string
	City    string
	State   string
	Country string
}

// Place is "City, ST" (the country is added when it is not the United States).
func (i Info) Place() string {
	var p []string
	if i.City != "" {
		p = append(p, i.City)
	}
	if i.State != "" {
		p = append(p, i.State)
	}
	if i.Country != "" && !strings.EqualFold(i.Country, "United States") {
		p = append(p, i.Country)
	}
	return strings.Join(p, ", ")
}

// Status is what the admin UI shows.
type Status struct {
	Enabled bool `json:"enabled"`
	// Cached is the number of answers kept, Today the requests of the last 24 hours.
	Cached   int `json:"cached"`
	Today    int `json:"today"`
	DailyMax int `json:"dailyMax"`
	// PausedUntil is set (RFC 3339) while a back-off or the circuit breaker holds requests back.
	PausedUntil string `json:"pausedUntil,omitempty"`
}

type entry struct {
	Found   bool   `json:"f,omitempty"`
	First   string `json:"fn,omitempty"`
	Name    string `json:"n,omitempty"`
	City    string `json:"c,omitempty"`
	State   string `json:"s,omitempty"`
	Country string `json:"co,omitempty"`
	At      int64  `json:"at"` // unix seconds the answer was fetched
}

type fileData struct {
	Entries map[string]entry `json:"entries"`
	// Reqs are the unix times of the requests of the last 24 hours (the daily cap survives a restart).
	Reqs []int64 `json:"reqs,omitempty"`
}

// Manager answers lookups from its cache and fetches missing callsigns in the background.
type Manager struct {
	o Options

	mu      sync.Mutex
	enabled bool
	cache   map[string]entry
	reqs    []time.Time
	queue   []string
	pending map[string]bool // queued or in flight
	// pausedUntil is the end of a back-off or breaker pause.
	pausedUntil time.Time
	failures    int

	onChange func()

	saveMu sync.Mutex
	wake   chan struct{}
}

// New returns a Manager and reads the cache file; Run does the lookups.
func New(o Options) *Manager {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Float64
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: Timeout}
	}
	// A redirect is not followed: it would leave the host that was meant.
	c := *o.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	o.Client = &c
	m := &Manager{o: o, onChange: o.OnChange, cache: map[string]entry{}, pending: map[string]bool{}, wake: make(chan struct{}, 1)}
	if o.Settings != nil {
		if v, err := o.Settings.GetSetting(SettingKey); err == nil && v == "true" {
			m.enabled = true
		}
	}
	m.load()
	return m
}

func (m *Manager) path() string { return filepath.Join(m.o.Dir, FileName) }

func (m *Manager) load() {
	if m.o.Dir == "" {
		return
	}
	b, err := os.ReadFile(m.path())
	if err != nil {
		return
	}
	var f fileData
	if json.Unmarshal(b, &f) != nil {
		return
	}
	now := m.o.Now()
	for k, e := range f.Entries {
		if ValidCall(k) && !m.expired(e, now) {
			m.cache[k] = e
		}
	}
	for len(m.cache) > MaxEntries {
		m.evictOldestLocked()
	}
	for _, t := range f.Reqs {
		if at := time.Unix(t, 0); now.Sub(at) < 24*time.Hour && !at.After(now) {
			m.reqs = append(m.reqs, at)
		}
	}
}

func (m *Manager) expired(e entry, now time.Time) bool {
	ttl := NotFoundTTL
	if e.Found {
		ttl = FoundTTL
	}
	return now.Sub(time.Unix(e.At, 0)) >= ttl
}

func (m *Manager) evictOldestLocked() {
	oldest, at := "", int64(1<<62)
	for k, e := range m.cache {
		if e.At < at {
			oldest, at = k, e.At
		}
	}
	delete(m.cache, oldest)
}

// save writes the cache atomically (called without m.mu held).
func (m *Manager) save() {
	if m.o.Dir == "" {
		return
	}
	m.mu.Lock()
	f := fileData{Entries: make(map[string]entry, len(m.cache))}
	for k, e := range m.cache {
		f.Entries[k] = e
	}
	for _, t := range m.reqs {
		f.Reqs = append(f.Reqs, t.Unix())
	}
	m.mu.Unlock()
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	tmp := m.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		m.o.Logger.Printf("hamdb: saving the cache: %v", err)
		return
	}
	if err := os.Rename(tmp, m.path()); err != nil {
		m.o.Logger.Printf("hamdb: saving the cache: %v", err)
		_ = os.Remove(tmp)
	}
}

// SetOnChange sets the function called after a new answer was cached (it
// replaces Options.OnChange; the server uses it to refresh what it shows).
func (m *Manager) SetOnChange(f func()) {
	m.mu.Lock()
	m.onChange = f
	m.mu.Unlock()
}

// Enabled reports the switch.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// SetEnabled saves the switch. Off: waiting lookups are dropped.
func (m *Manager) SetEnabled(on bool) error {
	if m.o.Settings != nil {
		v := "false"
		if on {
			v = "true"
		}
		if err := m.o.Settings.SetSetting(SettingKey, v); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.enabled = on
	if !on {
		m.clearQueueLocked()
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) clearQueueLocked() {
	for _, c := range m.queue {
		delete(m.pending, c)
	}
	m.queue = nil
}

func (m *Manager) pruneReqsLocked(now time.Time) {
	i := 0
	for i < len(m.reqs) && now.Sub(m.reqs[i]) >= 24*time.Hour {
		i++
	}
	m.reqs = m.reqs[i:]
}

// Status reports the state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.o.Now()
	m.pruneReqsLocked(now)
	s := Status{Enabled: m.enabled, Cached: len(m.cache), Today: len(m.reqs), DailyMax: DailyMax}
	if m.pausedUntil.After(now) {
		s.PausedUntil = m.pausedUntil.UTC().Format(time.RFC3339)
	}
	return s
}

// ValidCall reports whether s looks like an amateur callsign worth a lookup:
// 3 to 10 letters and digits with at least one of each (so "Echo test", a DMR
// ID and "Private call" are not).
func ValidCall(s string) bool {
	if len(s) < 3 || len(s) > 10 {
		return false
	}
	letter, digit := false, false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			return false
		}
	}
	return letter && digit
}

// Lookup returns the cached answer for call. It never blocks and never makes
// a request itself: a callsign that is not cached is queued (when the switch
// is on, the callsign is valid and there is room) and the answer is there on a
// later call, after OnChange.
func (m *Manager) Lookup(call string) (Info, bool) {
	call = strings.ToUpper(strings.TrimSpace(call))
	if !ValidCall(call) {
		return Info{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return Info{}, false
	}
	now := m.o.Now()
	if e, ok := m.cache[call]; ok && !m.expired(e, now) {
		if !e.Found {
			return Info{}, false
		}
		return Info{First: e.First, Name: e.Name, City: e.City, State: e.State, Country: e.Country}, true
	}
	m.pruneReqsLocked(now)
	if m.pending[call] || len(m.queue) >= QueueMax || len(m.reqs) >= DailyMax {
		return Info{}, false
	}
	m.pending[call] = true
	m.queue = append(m.queue, call)
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return Info{}, false
}

// Cached returns the cached answer for call and nothing else: unlike Lookup it
// never queues a callsign, so it is safe to call for a request from an
// untrusted visitor (the public page). It reports false while the switch is
// off, for an invalid callsign, and for one that is not cached or has expired.
func (m *Manager) Cached(call string) (Info, bool) {
	call = strings.ToUpper(strings.TrimSpace(call))
	if !ValidCall(call) {
		return Info{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return Info{}, false
	}
	e, ok := m.cache[call]
	if !ok || !e.Found || m.expired(e, m.o.Now()) {
		return Info{}, false
	}
	return Info{First: e.First, Name: e.Name, City: e.City, State: e.State, Country: e.Country}, true
}

// Run does the queued lookups until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	for ctx.Err() == nil {
		w := m.pump(ctx)
		switch {
		case w == 0:
		case w < 0:
			select {
			case <-m.wake:
			case <-ctx.Done():
			}
		default:
			t := time.NewTimer(w)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
			}
		}
	}
}

// pump makes at most one request. It returns 0 after a request, a positive
// wait when the next one is not allowed yet, and a negative value when there
// is nothing to do.
func (m *Manager) pump(ctx context.Context) time.Duration {
	m.mu.Lock()
	if !m.enabled || len(m.queue) == 0 {
		m.mu.Unlock()
		return -1
	}
	now := m.o.Now()
	m.pruneReqsLocked(now)
	if len(m.reqs) >= DailyMax {
		m.clearQueueLocked() // the lookups would only grow old; the next heard call asks again
		m.mu.Unlock()
		return -1
	}
	next := m.pausedUntil
	if n := len(m.reqs); n > 0 {
		if t := m.reqs[n-1].Add(MinInterval); t.After(next) {
			next = t
		}
	}
	if now.Before(next) {
		m.mu.Unlock()
		return next.Sub(now)
	}
	call := m.queue[0]
	m.queue = m.queue[1:]
	m.reqs = append(m.reqs, now)
	m.mu.Unlock()

	res, err := m.fetch(ctx, call)

	m.mu.Lock()
	changed := false
	onChange := m.onChange
	delete(m.pending, call)
	now = m.o.Now()
	switch {
	case err == nil:
		m.failures = 0
		res.At = now.Unix()
		m.cache[call] = res
		for len(m.cache) > MaxEntries {
			m.evictOldestLocked()
		}
		changed = true
	default:
		m.failures++
		var he *httpError
		throttled := errors.As(err, &he) && (he.status == http.StatusTooManyRequests || he.status == http.StatusServiceUnavailable)
		if m.failures >= BreakerAfter {
			m.pausedUntil = now.Add(m.jitter(MaxPause, 0.9, 1))
			m.failures = 0
			m.o.Logger.Printf("hamdb: %d failures in a row, paused for an hour (%v)", BreakerAfter, err)
		} else if d := m.retryAfter(err); d > 0 {
			m.pausedUntil = now.Add(d)
		} else {
			d := backoffBase << uint(m.failures-1)
			if d > MaxPause || d <= 0 {
				d = MaxPause
			}
			m.pausedUntil = now.Add(m.jitter(d, 0.5, 1.5))
		}
		if throttled && len(m.queue) < QueueMax {
			m.queue = append([]string{call}, m.queue...) // asked again after the wait
			m.pending[call] = true
		}
	}
	m.mu.Unlock()
	m.save()
	if changed && onChange != nil {
		onChange()
	}
	return 0
}

// jitter is d times a random factor between lo and hi, at most MaxPause.
func (m *Manager) jitter(d time.Duration, lo, hi float64) time.Duration {
	j := time.Duration(float64(d) * (lo + (hi-lo)*m.o.Rand()))
	if j > MaxPause {
		j = MaxPause
	}
	return j
}

// retryAfter is the wait a 429 or 503 asked for (with up to 10% jitter on
// top), 0 when there is none.
func (m *Manager) retryAfter(err error) time.Duration {
	var he *httpError
	if !errors.As(err, &he) || he.retryAfter <= 0 {
		return 0
	}
	d := he.retryAfter
	if d > MaxRetryAfter {
		d = MaxRetryAfter
	}
	return d + time.Duration(float64(d)*0.1*m.o.Rand())
}

type httpError struct {
	status     int
	retryAfter time.Duration
}

func (e *httpError) Error() string { return "hamdb.org answered HTTP " + strconv.Itoa(e.status) }

// parseRetryAfter reads seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

type reply struct {
	HamDB struct {
		Callsign struct {
			First   string `json:"fname"`
			Last    string `json:"name"`
			City    string `json:"addr2"`
			State   string `json:"state"`
			Country string `json:"country"`
		} `json:"callsign"`
		Messages struct {
			Status string `json:"status"`
		} `json:"messages"`
	} `json:"hamdb"`
}

func (m *Manager) fetch(ctx context.Context, call string) (entry, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	u := strings.TrimRight(m.o.BaseURL, "/") + "/v1/" + url.PathEscape(call) + "/json/" + AppName
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return entry{}, err
	}
	req.Header.Set("User-Agent", "nodespot/"+m.o.Version)
	req.Header.Set("Accept", "application/json")
	resp, err := m.o.Client.Do(req)
	if err != nil {
		return entry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return entry{}, &httpError{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), m.o.Now())}
	}
	var r reply
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&r); err != nil {
		return entry{}, err
	}
	switch r.HamDB.Messages.Status {
	case "NOT_FOUND":
		return entry{}, nil
	case "OK":
	default:
		return entry{}, errors.New("hamdb.org status " + r.HamDB.Messages.Status)
	}
	c := r.HamDB.Callsign
	first, last := tidy(c.First), tidy(c.Last)
	name := strings.TrimSpace(first + " " + last)
	if name == "" {
		return entry{}, nil
	}
	return entry{Found: true, First: first, Name: name, City: tidy(c.City), State: strings.TrimSpace(c.State), Country: tidy(c.Country)}, nil
}

// tidy trims s and turns an all capitals text ("SPRINGFIELD") into
// capitalised words ("Springfield"), the way the answers of hamdb.org come.
func tidy(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || s != strings.ToUpper(s) {
		return s
	}
	w := strings.Fields(strings.ToLower(s))
	for i, x := range w {
		r := []rune(x)
		w[i] = strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	return strings.Join(w, " ")
}
