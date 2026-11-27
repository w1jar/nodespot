// Package nodelist maintains the AllStarLink node database (allmondb /
// astdb.txt format) used to show callsigns, descriptions and locations.
package nodelist

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info describes one node.
type Info struct {
	Node        string `json:"node"`
	Callsign    string `json:"callsign"`
	Description string `json:"description"`
	Location    string `json:"location"`
}

// Status is the API view of the database state.
type Status struct {
	Count   int        `json:"count"`
	Updated *time.Time `json:"updated"`
	Source  string     `json:"source"`
	Error   *string    `json:"error"`
	// Busy is true while a download runs.
	Busy bool `json:"busy,omitempty"`
}

// Options configure a DB.
type Options struct {
	URL      string // empty disables downloading
	DataDir  string // cache at <DataDir>/astdb.txt; empty disables caching
	Interval time.Duration
	// RetryMin and RetryMax bound the retry backoff while the list is empty
	// (defaults 30 s doubling to 10 min). With a list loaded a failure is
	// retried after an hour.
	RetryMin time.Duration
	RetryMax time.Duration
	Client   *http.Client
	MaxBytes int64 // download size cap, default 20 MB
	Timeout  time.Duration
	Logger   *log.Logger
}

type entry struct {
	Info
	hay string // lower-cased callsign+description+location
}

// DB is the in-memory node index.
type DB struct {
	opt Options

	mu      sync.RWMutex
	byNode  map[string]*entry
	list    []*entry
	updated time.Time
	source  string
	lastErr string

	refreshMu  sync.Mutex
	refreshing bool
}

// New creates an empty DB.
func New(opt Options) *DB {
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = 20 << 20
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}
	if opt.Client == nil {
		opt.Client = &http.Client{}
	}
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	return &DB{opt: opt, byNode: map[string]*entry{}, source: "none"}
}

// Parse reads astdb-style lines: node|callsign|description|location. Anything
// after the third "|" belongs to the location. Comment lines (; or #), blank
// lines and malformed lines (non-numeric node, fewer than two fields) are
// skipped; later duplicates win.
func Parse(r io.Reader) ([]Info, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	idx := map[string]int{}
	var out []Info
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		// A few real entries contain "|" inside the location; keep it there.
		f := strings.SplitN(line, "|", 4)
		if len(f) < 2 {
			continue
		}
		node := strings.TrimSpace(f[0])
		if !validNode(node) {
			continue
		}
		in := Info{Node: node, Callsign: strings.TrimSpace(f[1])}
		if len(f) > 2 {
			in.Description = strings.TrimSpace(f[2])
		}
		if len(f) > 3 {
			in.Location = strings.TrimSpace(f[3])
		}
		if i, ok := idx[node]; ok {
			out[i] = in
			continue
		}
		idx[node] = len(out)
		out = append(out, in)
	}
	return out, sc.Err()
}

func validNode(s string) bool {
	if s == "" || len(s) > 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Set replaces the data (used by Load, Refresh and demo mode).
func (d *DB) Set(infos []Info, source string, updated time.Time) {
	byNode := make(map[string]*entry, len(infos))
	list := make([]*entry, 0, len(infos))
	for _, in := range infos {
		e := &entry{Info: in, hay: strings.ToLower(in.Callsign + "\x00" + in.Description + "\x00" + in.Location)}
		byNode[in.Node] = e
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return nodeLess(list[i].Node, list[j].Node) })
	d.mu.Lock()
	d.byNode, d.list, d.source, d.updated, d.lastErr = byNode, list, source, updated, ""
	d.mu.Unlock()
}

func nodeLess(a, b string) bool {
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	if x != y {
		return x < y
	}
	return a < b
}

func (d *DB) cachePath() string {
	if d.opt.DataDir == "" {
		return ""
	}
	return filepath.Join(d.opt.DataDir, "astdb.txt")
}

// LoadCache loads <datadir>/astdb.txt if present.
func (d *DB) LoadCache() error {
	p := d.cachePath()
	if p == "" {
		return errors.New("nodelist: no cache configured")
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	infos, err := Parse(f)
	if err != nil {
		return err
	}
	var mt time.Time
	if fi, err := f.Stat(); err == nil {
		mt = fi.ModTime().UTC()
	}
	d.Set(infos, "cache", mt)
	return nil
}

func (d *DB) setErr(err error) {
	d.mu.Lock()
	d.lastErr = err.Error()
	d.mu.Unlock()
}

// Refresh downloads the list, validates it, atomically saves the cache and
// swaps the index. On failure the previous data stays and Status carries the
// error. Concurrent calls collapse into one.
func (d *DB) Refresh(ctx context.Context) error {
	if d.opt.URL == "" {
		err := errors.New("no nodelist URL configured")
		d.setErr(err)
		return err
	}
	if !d.beginRefresh() {
		return nil
	}
	defer d.endRefresh()
	err := d.download(ctx)
	if err != nil {
		d.setErr(err)
		d.opt.Logger.Printf("nodelist: refresh failed: %v", err)
	}
	return err
}

func (d *DB) beginRefresh() bool {
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	if d.refreshing {
		return false
	}
	d.refreshing = true
	return true
}

func (d *DB) endRefresh() {
	d.refreshMu.Lock()
	d.refreshing = false
	d.refreshMu.Unlock()
}

// RefreshAsync starts a background refresh unless one is running.
func (d *DB) RefreshAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), d.opt.Timeout+5*time.Second)
		defer cancel()
		_ = d.Refresh(ctx)
	}()
}

func (d *DB) download(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, d.opt.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.opt.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nodespot-nodelist")
	resp, err := d.opt.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > d.opt.MaxBytes {
		return fmt.Errorf("download: too large (%d bytes)", resp.ContentLength)
	}

	var tmp *os.File
	var body io.Reader = io.LimitReader(resp.Body, d.opt.MaxBytes+1)
	if p := d.cachePath(); p != "" {
		if err := os.MkdirAll(d.opt.DataDir, 0o750); err != nil {
			return err
		}
		tmp, err = os.CreateTemp(d.opt.DataDir, ".astdb-*.tmp")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		body = io.TeeReader(body, tmp)
	}
	cr := &countReader{r: body}
	infos, err := Parse(cr)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if cr.n > d.opt.MaxBytes {
		return fmt.Errorf("download: exceeds %d byte limit", d.opt.MaxBytes)
	}
	if len(infos) == 0 {
		return errors.New("download: no valid nodes in response")
	}
	if tmp != nil {
		if err := tmp.Sync(); err != nil {
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		_ = os.Chmod(tmp.Name(), 0o644)
		if err := os.Rename(tmp.Name(), d.cachePath()); err != nil {
			return err
		}
	}
	d.Set(infos, d.opt.URL, time.Now().UTC())
	return nil
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Run loads the cache, then refreshes on startup (if stale) and every
// Interval until ctx is done. It never fails startup.
func (d *DB) Run(ctx context.Context) {
	if err := d.LoadCache(); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.opt.Logger.Printf("nodelist: cache: %v", err)
	}
	iv := d.opt.Interval
	if iv <= 0 {
		iv = 24 * time.Hour
	}
	if d.opt.URL == "" {
		return
	}
	d.mu.RLock()
	age := time.Since(d.updated)
	have := len(d.list) > 0
	d.mu.RUnlock()
	wait := time.Duration(0)
	if have && age < iv {
		wait = iv - age
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := d.Refresh(ctx)
			switch {
			case err == nil:
				fails = 0
				t.Reset(iv)
			case d.Len() == 0:
				// nothing to use yet: retry soon, backing off
				fails++
				t.Reset(d.retryWait(fails))
			default:
				fails++
				w := time.Hour // retry sooner than a day after a failure
				if iv < w {
					w = iv
				}
				t.Reset(w)
			}
		}
	}
}

// Len is the number of nodes in the list.
func (d *DB) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.list)
}

// retryWait is the wait after the nth failure in a row while the list is
// empty: RetryMin (30 s), doubled each time, capped at RetryMax (10 min).
func (d *DB) retryWait(n int) time.Duration {
	lo, hi := d.opt.RetryMin, d.opt.RetryMax
	if lo <= 0 {
		lo = 30 * time.Second
	}
	if hi <= 0 {
		hi = 10 * time.Minute
	}
	w := lo
	for i := 1; i < n && w < hi; i++ {
		w *= 2
	}
	if w > hi {
		w = hi
	}
	return w
}

// Lookup returns a node's info.
func (d *DB) Lookup(node string) (Info, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e, ok := d.byNode[node]
	if !ok {
		return Info{}, false
	}
	return e.Info, true
}

// Search matches the node number prefix and case-insensitive substrings of
// callsign, description and location. Exact and prefix node matches rank
// first, then callsign prefixes, then other matches (by node number).
func (d *DB) Search(q string, limit int) []Info {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return []Info{}
	}
	type hit struct {
		e    *entry
		rank int
	}
	var hits []hit
	d.mu.RLock()
	for _, e := range d.list {
		rank := -1
		switch {
		case e.Node == q:
			rank = 0
		case strings.HasPrefix(e.Node, q):
			rank = 1
		case strings.HasPrefix(strings.ToLower(e.Callsign), q):
			rank = 2
		case strings.Contains(e.hay, q):
			rank = 3
		}
		if rank >= 0 {
			hits = append(hits, hit{e, rank})
		}
	}
	d.mu.RUnlock()
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Info, len(hits))
	for i, h := range hits {
		out[i] = h.e.Info
	}
	return out
}

// Status returns the current status.
func (d *DB) Status() Status {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := Status{Count: len(d.list), Source: d.source}
	d.refreshMu.Lock()
	s.Busy = d.refreshing
	d.refreshMu.Unlock()
	if !d.updated.IsZero() {
		u := d.updated
		s.Updated = &u
	}
	if d.lastErr != "" {
		e := d.lastErr
		s.Error = &e
	}
	return s
}
