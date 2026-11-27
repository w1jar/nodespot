// Package hostlists keeps the reflector and master lists the Modes page
// offers (DMR masters, YSF and M17 reflectors, P25 and NXDN talkgroups): it
// downloads them once a day (sooner after a failure), serves them to the
// pickers and resolves a picked entry to a tune target.
package hostlists

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	proto "github.com/w1jar/nodespot/internal/dv/m17"
)

const (
	// DefaultBaseURL is where the files are downloaded from: <base><file>.
	DefaultBaseURL = "https://www.pistar.uk/downloads/"
	// SettingBaseURL overrides DefaultBaseURL (a mirror, a test server).
	SettingBaseURL = "hostlists.base_url"

	// MaxFile caps one download.
	MaxFile = 4 << 20
	// Interval is the period between checks, Jitter the random spread (plus
	// or minus) added to it.
	Interval = 24 * time.Hour
	Jitter   = time.Hour
	// FirstDelay is the wait before the first check after the start.
	FirstDelay = 60 * time.Second
	// RetryAfter is the wait before the next check after a failed one (no
	// network yet at boot, a clock not yet set, the server down).
	RetryAfter = 10 * time.Minute
	// ManualEvery is the least time between two manual refreshes.
	ManualEvery = 10 * time.Minute
	// Timeout bounds one file's download.
	Timeout = 30 * time.Second

	metaName = "meta.json"
)

// Modes are the accepted list modes, in display order.
var Modes = []string{"dmr", "ysf", "p25", "nxdn", "m17"}

var files = map[string]string{
	"dmr":  "DMR_Hosts.txt",
	"ysf":  "YSF_Hosts.txt",
	"p25":  "P25_Hosts.txt",
	"nxdn": "NXDN_Hosts.txt",
	"m17":  "M17_Hosts.txt",
}

// ValidMode reports whether m is one of dmr, ysf, p25, nxdn, m17.
func ValidMode(m string) bool { _, ok := files[m]; return ok }

var (
	// ErrNoList: no list has been downloaded for the mode yet.
	ErrNoList = errors.New("no_list: the list is not available on this node yet")
	// ErrBusy: a refresh is already running.
	ErrBusy = errors.New("a refresh is already running")
	// ErrTooSoon: a manual refresh was done less than ManualEvery ago.
	ErrTooSoon = errors.New("the lists were refreshed a moment ago")
	// ErrDisabled: digital modes are off, so the lists are not kept.
	ErrDisabled = errors.New("digital modes are not enabled")
)

// Settings is the part of the store the base URL override is read from.
type Settings interface {
	GetSetting(k string) (string, error)
}

// Options configure a Manager.
type Options struct {
	// Dir is the directory the files are kept in (required).
	Dir      string
	Version  string // for the User-Agent "nodespot/<version>"
	Client   *http.Client
	Settings Settings // nil: the default base URL
	Logger   *log.Logger
	Now      func() time.Time
	// Enabled says whether digital modes are on; the lists are downloaded
	// only then. Nil: always.
	Enabled func() bool
	// FirstDelay overrides the wait before the first check.
	FirstDelay time.Duration
	// Interval overrides the period between checks (Jitter is added).
	Interval time.Duration
	// Retry overrides RetryAfter.
	Retry time.Duration
	// OnChange is called after the status changed.
	OnChange func()
}

// ListStatus is the state of one list.
type ListStatus struct {
	Entries int    `json:"entries"`
	Updated string `json:"updated,omitempty"` // RFC 3339: the file was last replaced
	Checked string `json:"checked,omitempty"` // the last successful check
	Error   string `json:"error,omitempty"`
}

// Status is what the admin sees.
type Status struct {
	Busy  bool                  `json:"busy,omitempty"`
	Lists map[string]ListStatus `json:"lists"`
	// NextManual is when a manual refresh is allowed again (RFC 3339,
	// empty: now).
	NextManual string `json:"nextManual,omitempty"`
}

type fileMeta struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	Updated      time.Time `json:"updated,omitempty"`
	Checked      time.Time `json:"checked,omitempty"`
}

type loaded struct {
	items []Item
	etag  string
}

// Manager downloads the lists on a schedule and serves lookups from them.
type Manager struct {
	o Options

	mu         sync.Mutex
	lists      map[string]*loaded
	meta       map[string]fileMeta
	errs       map[string]string
	busy       bool
	lastManual time.Time
}

// New returns a Manager; Run loads the files and keeps them fresh.
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: Timeout}
	}
	if o.FirstDelay <= 0 {
		o.FirstDelay = FirstDelay
	}
	if o.Interval <= 0 {
		o.Interval = Interval
	}
	if o.Retry <= 0 {
		o.Retry = RetryAfter
	}
	return &Manager{o: o, lists: map[string]*loaded{}, meta: map[string]fileMeta{}, errs: map[string]string{}}
}

func (m *Manager) path(mode string) string { return filepath.Join(m.o.Dir, files[mode]) }

func (m *Manager) changed() {
	if m.o.OnChange != nil {
		m.o.OnChange()
	}
}

func (m *Manager) enabled() bool { return m.o.Enabled == nil || m.o.Enabled() }

// baseURL is the setting when it is a valid http(s) URL, else the default,
// always with a trailing slash.
func (m *Manager) baseURL() string {
	b := DefaultBaseURL
	if m.o.Settings != nil {
		if v, err := m.o.Settings.GetSetting(SettingBaseURL); err == nil && strings.TrimSpace(v) != "" {
			if u, err := url.Parse(strings.TrimSpace(v)); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
				b = strings.TrimSpace(v)
			}
		}
	}
	if !strings.HasSuffix(b, "/") {
		b += "/"
	}
	return b
}

// load parses data into a list; it has to hold at least one entry.
func load(mode string, data []byte) (*loaded, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	items := parse(mode, data)
	if len(items) == 0 {
		return nil, errors.New("the list has no usable entries")
	}
	if len(items) > Max {
		items = items[:Max]
	}
	sum := sha256.Sum256(data)
	return &loaded{items: items, etag: hex.EncodeToString(sum[:8])}, nil
}

// Load reads the files and the meta data from disk. A missing file is not an
// error; an unusable one is reported in the status.
func (m *Manager) Load() {
	var meta map[string]fileMeta
	if b, err := os.ReadFile(filepath.Join(m.o.Dir, metaName)); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	for _, mode := range Modes {
		data, err := readFile(m.path(mode))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var l *loaded
		if err == nil {
			l, err = load(mode, data)
		}
		m.mu.Lock()
		if err != nil {
			m.errs[mode] = "the saved list is unusable: " + err.Error()
		} else {
			m.lists[mode] = l
			if mt, ok := meta[mode]; ok {
				m.meta[mode] = mt
			}
		}
		m.mu.Unlock()
	}
	m.changed()
}

func readFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFile {
		return nil, errors.New("file too large")
	}
	return data, nil
}

// Get returns one list; ErrNoList when none was downloaded yet.
func (m *Manager) Get(mode string) (List, error) {
	if !ValidMode(mode) {
		return List{}, errors.New("invalid mode")
	}
	m.mu.Lock()
	l := m.lists[mode]
	m.mu.Unlock()
	if l == nil {
		return List{}, ErrNoList
	}
	return List{Mode: mode, Items: append([]Item(nil), l.items...), ETag: l.etag, Truncated: false}, nil
}

// Resolve turns a picked value into a tune target. mode is YSF, P25, NXDN or
// M17 (any case). For YSF the value is a reflector id, a name or host:port; for
// P25 and NXDN a talkgroup; for M17 "M17-XXX C" (reflector and module), whose
// Room is that same text. ok is false for a value that is not in the list
// (a literal YSF host:port needs no list).
func (m *Manager) Resolve(mode, value string) (ctl.Target, bool) {
	mode = strings.ToLower(mode)
	if mode == "ysf" {
		if host, port, ok := splitHostPort(value); ok {
			return ctl.Target{Host: host, Port: port}, true
		}
	}
	if mode != "ysf" && mode != "p25" && mode != "nxdn" && mode != "m17" {
		return ctl.Target{}, false
	}
	module := ""
	if mode == "m17" {
		// "M17-XXX C": look the reflector up, keep the module in the Room.
		refl, mod, err := proto.ParseTarget(value)
		if err != nil {
			return ctl.Target{}, false
		}
		value, module = refl, " "+string(mod)
	}
	m.mu.Lock()
	l := m.lists[mode]
	m.mu.Unlock()
	if l == nil {
		return ctl.Target{}, false
	}
	for _, it := range l.items {
		if it.Value == value || (mode == "ysf" && it.Name == value) {
			return ctl.Target{Room: it.Value + module, Name: it.Name, Host: it.Address, Port: it.Port}, true
		}
	}
	return ctl.Target{}, false
}

func splitHostPort(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i < 1 || !reHost.MatchString(s[:i]) {
		return "", 0, false
	}
	p := s[i+1:]
	if len(p) > 1 && p[0] == '0' {
		return "", 0, false
	}
	n, ok := listPort(p)
	return s[:i], n, ok
}

// Status reports the state of every list.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Busy: m.busy, Lists: map[string]ListStatus{}}
	for _, mode := range Modes {
		ls := ListStatus{Error: m.errs[mode]}
		if l := m.lists[mode]; l != nil {
			ls.Entries = len(l.items)
		}
		if mt, ok := m.meta[mode]; ok {
			if !mt.Updated.IsZero() {
				ls.Updated = mt.Updated.UTC().Format(time.RFC3339)
			}
			if !mt.Checked.IsZero() {
				ls.Checked = mt.Checked.UTC().Format(time.RFC3339)
			}
		}
		st.Lists[mode] = ls
	}
	if !m.lastManual.IsZero() {
		if next := m.lastManual.Add(ManualEvery); next.After(m.o.Now()) {
			st.NextManual = next.UTC().Format(time.RFC3339)
		}
	}
	return st
}

// Run loads the files, checks for new ones after FirstDelay and then about
// once a day (with jitter), until ctx ends. A failed check is tried again
// after Retry instead of a day later.
func (m *Manager) Run(ctx context.Context) {
	m.Load()
	d := m.o.FirstDelay
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		next := m.o.Interval + time.Duration(rand.Int63n(int64(2*Jitter))) - Jitter
		if m.enabled() {
			if err := m.refresh(ctx, false); err != nil && !errors.Is(err, ErrBusy) {
				m.o.Logger.Printf("hostlists: %v (trying again in %s)", err, m.o.Retry)
				next = m.o.Retry
			}
		} else {
			next = 5 * time.Minute // look again soon: the enable may come any time
		}
		t.Reset(next)
	}
}

// Refresh downloads the lists now (the admin's button). It waits, is
// refused while another refresh runs (ErrBusy) or when the last manual one
// was less than ManualEvery ago (ErrTooSoon), and while digital modes are off.
func (m *Manager) Refresh(ctx context.Context) error {
	if !m.enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	if !m.lastManual.IsZero() && m.o.Now().Sub(m.lastManual) < ManualEvery {
		m.mu.Unlock()
		return ErrTooSoon
	}
	if m.busy {
		m.mu.Unlock()
		return ErrBusy
	}
	m.lastManual = m.o.Now()
	m.mu.Unlock()
	err := m.refresh(ctx, true)
	if err != nil && !errors.Is(err, ErrBusy) {
		// A refresh that got nothing does not count: the admin may try again
		// at once (the network was down, say).
		m.mu.Lock()
		if len(m.errs) >= len(Modes) {
			m.lastManual = time.Time{}
		}
		m.mu.Unlock()
	}
	return err
}

// refresh downloads every list; the first error is returned, the others are
// kept in the status. force skips the conditional headers.
func (m *Manager) refresh(ctx context.Context, force bool) error {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return ErrBusy
	}
	m.busy = true
	m.mu.Unlock()
	m.changed()
	defer func() {
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
		m.changed()
	}()
	if m.o.Dir == "" {
		return errors.New("no data directory")
	}
	if err := os.MkdirAll(m.o.Dir, 0o755); err != nil {
		return err
	}
	var first error
	for _, mode := range Modes {
		err := m.fetch(ctx, mode, force)
		m.mu.Lock()
		if err != nil {
			m.errs[mode] = err.Error()
		} else {
			delete(m.errs, mode)
		}
		m.mu.Unlock()
		if err != nil && first == nil {
			first = fmt.Errorf("%s: %w", mode, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	m.mu.Lock()
	m.saveMetaLocked()
	m.mu.Unlock()
	return first
}

// fetch downloads one file. On any failure the last good copy stays.
func (m *Manager) fetch(ctx context.Context, mode string, force bool) error {
	m.mu.Lock()
	mt := m.meta[mode]
	have := m.lists[mode] != nil
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL()+files[mode], nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nodespot/"+m.o.Version)
	if !force && have {
		if mt.ETag != "" {
			req.Header.Set("If-None-Match", mt.ETag)
		}
		if mt.LastModified != "" {
			req.Header.Set("If-Modified-Since", mt.LastModified)
		}
	}
	resp, err := m.o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	now := m.o.Now()
	switch {
	case resp.StatusCode == http.StatusNotModified && have:
		m.mu.Lock()
		mt.Checked = now
		m.meta[mode] = mt
		m.mu.Unlock()
		return nil
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxFile {
		return fmt.Errorf("the list is larger than %d MiB", MaxFile>>20)
	}
	tmp, err := os.CreateTemp(m.o.Dir, ".dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // gone after a successful rename
	}()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, MaxFile+1))
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if n > MaxFile {
		return fmt.Errorf("the list is larger than %d MiB", MaxFile>>20)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	data, err := readFile(tmpName)
	if err != nil {
		return err
	}
	l, err := load(mode, data)
	if err != nil {
		return fmt.Errorf("the downloaded list was rejected: %w", err)
	}
	if err := os.Rename(tmpName, m.path(mode)); err != nil {
		return err
	}
	m.mu.Lock()
	m.lists[mode] = l
	m.meta[mode] = fileMeta{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Updated: now, Checked: now}
	m.mu.Unlock()
	return nil
}

func (m *Manager) saveMetaLocked() {
	b, err := json.Marshal(m.meta)
	if err != nil {
		return
	}
	p := filepath.Join(m.o.Dir, metaName)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}
