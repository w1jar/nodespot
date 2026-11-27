package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// TuneFile is the name of the file, next to the node config, that holds
// each mode's remembered tune.
const TuneFile = "tune.json"

// tuneVersion is the version written to the tune file.
const tuneVersion = 1

// Tune is what one mode remembers: the last target it was linked to. The
// node reconnects to it when the mode is activated, unless the operator
// unlinked on purpose (Unlinked). A link that dropped or failed does not
// change it.
type Tune struct {
	Target   *mode.Target `json:"target,omitempty"`
	Unlinked bool         `json:"unlinked,omitempty"`
}

// tuneFile is the file format: a Tune per mode name.
type tuneFile struct {
	Version int             `json:"version"`
	Modes   map[string]Tune `json:"modes,omitempty"`
}

// loadTunes reads path. A missing or empty file is no memory yet. A file
// that cannot be read is an error; the caller keeps going with no memory.
func loadTunes(path string) (map[string]Tune, error) {
	out := map[string]Tune{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(strings.TrimSpace(string(b))) == 0) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	var f tuneFile
	if err := json.Unmarshal(b, &f); err != nil {
		return out, fmt.Errorf("%s: %w", path, err)
	}
	for n, t := range f.Modes {
		if t.Target != nil && strings.TrimSpace(t.Target.ID) == "" {
			t.Target = nil
		}
		if t.Target != nil {
			t.Unlinked = false
		}
		out[strings.ToLower(n)] = t
	}
	return out, nil
}

// saveTunes writes m to path atomically with mode 0600.
func saveTunes(path string, m map[string]Tune) error {
	b, err := json.MarshalIndent(tuneFile{Version: tuneVersion, Modes: m}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tune: %w", err)
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// writeFileAtomic writes b to path through a temporary file in the same
// directory, with mode 0600.
func writeFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".node-*.json")
	if err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	ok = true
	return nil
}

// tunePath is where the engine keeps the tunes: TunePath, else next to the
// config file; "" keeps them in memory only.
func (o Options) tunePath() string {
	if o.TunePath != "" {
		return o.TunePath
	}
	if o.ConfigPath != "" {
		return filepath.Join(filepath.Dir(o.ConfigPath), TuneFile)
	}
	return ""
}

func cloneTarget(t *mode.Target) *mode.Target {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// lastTarget returns the target mode n reconnects to when activated, or
// nil.
func (e *Engine) lastTarget(n string) *mode.Target {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneTarget(e.tunes[n].Target)
}

// tuneOf returns what mode n remembers. ok is false when it remembers
// nothing yet.
func (e *Engine) tuneOf(n string) (t Tune, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok = e.tunes[strings.ToLower(n)]
	t.Target = cloneTarget(t.Target)
	return t, ok
}

// setTune records mode n's tune and saves the tune file. Called with opMu.
func (e *Engine) setTune(n string, t Tune) error {
	e.mu.Lock()
	if cur, ok := e.tunes[n]; ok && cur.Unlinked == t.Unlinked && sameTarget(cur.Target, t.Target) {
		e.mu.Unlock()
		return nil
	}
	e.tunes[n] = t
	e.mu.Unlock()
	return e.saveTunes()
}

// forgetTune removes mode n's tune. Called with opMu.
func (e *Engine) forgetTune(n string) error {
	e.mu.Lock()
	_, had := e.tunes[n]
	delete(e.tunes, n)
	e.mu.Unlock()
	if !had {
		return nil
	}
	return e.saveTunes()
}

func (e *Engine) saveTunes() error {
	path := e.o.tunePath()
	if path == "" {
		return nil
	}
	e.mu.Lock()
	m := make(map[string]Tune, len(e.tunes))
	for k, v := range e.tunes {
		v.Target = cloneTarget(v.Target)
		m[k] = v
	}
	e.mu.Unlock()
	return saveTunes(path, m)
}

func sameTarget(a, b *mode.Target) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// migrate moves what older configs held into the tunes: the target in
// Station.Active, and a default reflector setting (Spec.Legacy), which
// seeds a mode's tune only when the mode remembers nothing yet. The old
// keys are then dropped from the config. Called by Start with opMu.
func (e *Engine) migrate() {
	e.mu.Lock()
	c := e.cfg.Clone()
	e.mu.Unlock()
	cfgChanged, tuneChanged := false, false
	e.mu.Lock()
	if a := c.Station.Active; a != nil && a.Target != nil {
		if _, ok := e.tunes[a.Mode]; !ok && a.Mode != "" {
			e.tunes[a.Mode] = Tune{Target: cloneTarget(a.Target)}
			tuneChanged = true
		}
		a.Target = nil
		cfgChanged = true
	}
	for name, s := range c.Modes {
		sp, ok := mode.Lookup(name)
		if !ok {
			continue
		}
		for _, k := range sp.Legacy {
			v, has := s.Values[k]
			if !has {
				continue
			}
			delete(s.Values, k)
			cfgChanged = true
			if _, ok := e.tunes[name]; ok {
				continue
			}
			if t := legacyTarget(name, s, v); t != nil {
				e.tunes[name] = Tune{Target: t}
				tuneChanged = true
			}
		}
	}
	if c.Station.AudioTuned == nil && c.Station.legacyTuned() {
		// A config from before the tuning record that already has audio
		// levels was set up: never remind it.
		e.markTuned(c, false)
		cfgChanged = true
	}
	e.mu.Unlock()
	if tuneChanged {
		if err := e.saveTunes(); err != nil {
			e.logf(e.now(), "saving the remembered tunes: %v", err)
		}
	}
	if cfgChanged {
		if err := e.commit(c, false); err != nil {
			e.logf(e.now(), "saving the migrated config: %v", err)
			e.mu.Lock()
			e.cfg = c
			e.mu.Unlock()
		}
	}
}

// legacyTarget turns an old default reflector address (host or host:port)
// into a target: the entry of the mode's reflector list with that address.
// Without such an entry the address names no talkgroup or module, so there
// is nothing to remember.
func legacyTarget(name string, s mode.Settings, addr string) *mode.Target {
	host, port := strings.TrimSpace(addr), 0
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[:i], ":") {
		var p int
		if _, err := fmt.Sscanf(host[i+1:], "%d", &p); err == nil {
			host, port = host[:i], p
		}
	}
	for _, r := range s.Reflectors {
		if !strings.EqualFold(strings.TrimSpace(r.Host), host) || (port != 0 && r.Port != 0 && r.Port != port) {
			continue
		}
		if name == mode.M17 && len(strings.Fields(r.ID)) != 2 {
			continue // a module is part of an M17 target
		}
		t := r
		return &t
	}
	return nil
}
