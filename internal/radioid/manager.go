package radioid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultURL is the public user list of radioid.net.
	DefaultURL = "https://radioid.net/static/user.csv"
	// SettingKey is the settings table key of the on/off switch; the list is
	// on unless the value is "false".
	SettingKey = "radioid.enabled"
	// FileName and metaName live in the data directory.
	FileName = "user.csv"
	metaName = "user.csv.meta"

	// MaxSize caps one download (the list is 15 to 20 MB).
	MaxSize = 64 << 20
	// Refresh is the interval between checks.
	Refresh = 24 * time.Hour

	downloadTimeout = 5 * time.Minute
	firstDelay      = 45 * time.Second
	tick            = time.Hour
)

// Settings is the part of the store the switch is kept in.
type Settings interface {
	GetSetting(k string) (string, error)
	SetSetting(k, v string) error
}

// Options configure a Manager.
type Options struct {
	// Dir is the data directory the list is kept in (required).
	Dir      string
	URL      string // default DefaultURL
	Version  string // for the User-Agent "nodespot/<version>"
	Client   *http.Client
	Settings Settings // nil: the switch is on and not persisted
	Logger   *log.Logger
	Now      func() time.Time
	MaxSize  int64 // default MaxSize
	// FirstDelay is the wait before the first check (default 45 s).
	FirstDelay time.Duration
	// OnChange is called after the status changed (new list, error, switch).
	OnChange func()
}

// Status is what the admin UI shows.
type Status struct {
	Enabled bool `json:"enabled"`
	// Entries is the number of rows in the list in use (0: none).
	Entries int `json:"entries"`
	// Updated is when the list was last replaced (RFC 3339, empty: never);
	// Checked is the last successful check, including "not modified".
	Updated  string `json:"updated,omitempty"`
	Checked  string `json:"checked,omitempty"`
	Error    string `json:"error,omitempty"`
	Busy     bool   `json:"busy,omitempty"`
	SizeByte int64  `json:"size,omitempty"`
}

type meta struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	Updated      time.Time `json:"updated,omitempty"`
	Checked      time.Time `json:"checked,omitempty"`
}

// Manager downloads user.csv on a schedule and serves lookups from it.
type Manager struct {
	o Options

	mu      sync.Mutex
	idx     *Index
	m       meta
	lastErr string
	busy    bool
	enabled bool
}

// New returns a Manager; Run loads the file and keeps it fresh.
func New(o Options) *Manager {
	if o.URL == "" {
		o.URL = DefaultURL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxSize <= 0 {
		o.MaxSize = MaxSize
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: downloadTimeout}
	}
	mg := &Manager{o: o, enabled: true}
	if o.Settings != nil {
		if v, err := o.Settings.GetSetting(SettingKey); err == nil && v == "false" {
			mg.enabled = false
		}
	}
	return mg
}

func (g *Manager) path() string { return filepath.Join(g.o.Dir, FileName) }

func (g *Manager) changed() {
	if g.o.OnChange != nil {
		g.o.OnChange()
	}
}

// Load indexes the file on disk (if the list is on and the file is there).
func (g *Manager) Load() {
	g.mu.Lock()
	if !g.enabled {
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	if b, err := os.ReadFile(filepath.Join(g.o.Dir, metaName)); err == nil {
		var m meta
		if json.Unmarshal(b, &m) == nil {
			g.mu.Lock()
			g.m = m
			g.mu.Unlock()
		}
	}
	idx, err := BuildIndex(g.path())
	g.mu.Lock()
	switch {
	case err == nil:
		g.idx = idx
	case !errors.Is(err, os.ErrNotExist):
		g.lastErr = "the saved list is unusable: " + err.Error()
	}
	g.mu.Unlock()
	g.changed()
}

// Enabled reports the switch.
func (g *Manager) Enabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.enabled
}

// SetEnabled saves the switch. Off: the list is dropped from memory (the
// file stays on disk). On: the file is loaded, and fetched when missing.
func (g *Manager) SetEnabled(on bool) error {
	if g.o.Settings != nil {
		v := "true"
		if !on {
			v = "false"
		}
		if err := g.o.Settings.SetSetting(SettingKey, v); err != nil {
			return err
		}
	}
	g.mu.Lock()
	g.enabled = on
	old := g.idx
	if !on {
		g.idx = nil
	}
	g.mu.Unlock()
	if !on {
		old.Close()
		g.changed()
		return nil
	}
	g.Load()
	return nil
}

// Status reports the state.
func (g *Manager) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := Status{Enabled: g.enabled, Entries: g.idx.Entries(), Error: g.lastErr, Busy: g.busy}
	if g.idx != nil {
		s.SizeByte = g.idx.size
	}
	if !g.m.Updated.IsZero() {
		s.Updated = g.m.Updated.UTC().Format(time.RFC3339)
	}
	if !g.m.Checked.IsZero() {
		s.Checked = g.m.Checked.UTC().Format(time.RFC3339)
	}
	return s
}

func (g *Manager) index() *Index {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.idx
}

// ByID looks a DMR ID up; false when the list is off, missing or has no such ID.
func (g *Manager) ByID(id uint32) (User, bool) { return g.index().ByID(id) }

// ByCallsign looks a callsign up (YSF, and AllStar callsigns).
func (g *Manager) ByCallsign(call string) (User, bool) { return g.index().ByCallsign(call) }

// Run loads the list, then checks for a newer one every hour once the last
// check is a day old, until ctx is done.
func (g *Manager) Run(ctx context.Context) {
	g.Load()
	d := g.o.FirstDelay
	if d <= 0 {
		d = firstDelay
	}
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		g.mu.Lock()
		due := g.enabled && (g.idx == nil || g.o.Now().Sub(g.m.Checked) >= Refresh)
		g.mu.Unlock()
		if due {
			if err := g.Download(ctx, false); err != nil {
				g.o.Logger.Printf("radioid: %v", err)
			}
		}
		t.Reset(tick)
	}
}

// ErrBusy: a download is already running.
var ErrBusy = errors.New("a download is already running")

// ErrOff: the list is switched off.
var ErrOff = errors.New("the radioid.net list is switched off")

// claim marks a download as running.
func (g *Manager) claim() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.enabled {
		return ErrOff
	}
	if g.busy {
		return ErrBusy
	}
	g.busy = true
	return nil
}

// DownloadAsync starts a download in the background (the Download now
// button): the error is only ErrOff or ErrBusy, the outcome is in Status.
func (g *Manager) DownloadAsync(ctx context.Context, force bool) error {
	if err := g.claim(); err != nil {
		return err
	}
	g.changed()
	go func() { _ = g.run(ctx, force) }()
	return nil
}

// Download fetches the list and waits. force skips If-None-Match and
// If-Modified-Since. A 304 keeps the file. On any failure the old file and
// index stay and the error is kept for the status.
func (g *Manager) Download(ctx context.Context, force bool) error {
	if err := g.claim(); err != nil {
		return err
	}
	g.changed()
	return g.run(ctx, force)
}

// run is a download that claim started.
func (g *Manager) run(ctx context.Context, force bool) (err error) {
	g.mu.Lock()
	m := g.m
	have := g.idx != nil
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.busy = false
		if err != nil {
			g.lastErr = err.Error()
		} else {
			g.lastErr = ""
		}
		g.mu.Unlock()
		g.changed()
	}()

	if g.o.Dir == "" {
		return errors.New("no data directory")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.o.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nodespot/"+g.o.Version)
	if !force && have {
		if m.ETag != "" {
			req.Header.Set("If-None-Match", m.ETag)
		}
		if m.LastModified != "" {
			req.Header.Set("If-Modified-Since", m.LastModified)
		}
	}
	resp, err := g.o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && have:
		g.mu.Lock()
		g.m.Checked = g.o.Now()
		g.saveMetaLocked()
		g.mu.Unlock()
		return nil
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > g.o.MaxSize {
		return fmt.Errorf("the list is larger than %d MB", g.o.MaxSize>>20)
	}
	tmp, err := os.CreateTemp(g.o.Dir, ".user.csv-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, g.o.MaxSize+1))
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if n > g.o.MaxSize {
		return fmt.Errorf("the list is larger than %d MB", g.o.MaxSize>>20)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		return err
	}
	// BuildIndex validates the header and the rows before anything is replaced.
	idx, err := BuildIndex(tmpName)
	if err != nil {
		return fmt.Errorf("the downloaded list was rejected: %w", err)
	}
	if err := os.Rename(tmpName, g.path()); err != nil {
		idx.Close()
		return err
	}
	keep = true
	tmp.Close()
	now := g.o.Now()
	g.mu.Lock()
	old := g.idx
	if g.enabled {
		g.idx = idx
	} else {
		idx.Close()
	}
	g.m = meta{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Updated: now, Checked: now}
	g.saveMetaLocked()
	g.mu.Unlock()
	old.Close()
	return nil
}

func (g *Manager) saveMetaLocked() {
	b, err := json.Marshal(g.m)
	if err != nil {
		return
	}
	p := filepath.Join(g.o.Dir, metaName)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o640) == nil {
		_ = os.Rename(tmp, p)
	}
}
