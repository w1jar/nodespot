package sa818

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// DefaultStatePath is where the daemon keeps the applied settings.
const DefaultStatePath = "/etc/nodespot/sa818.json"

// stateVersion is the file format version.
const stateVersion = 1

// State is the saved copy of what the module was last programmed with.
type State struct {
	Version   int       `json:"version"`
	AppliedAt time.Time `json:"applied_at"`
	Settings  Settings  `json:"settings"`
}

// ErrNoState is returned by LoadState when the file does not exist.
var ErrNoState = errors.New("sa818: no saved settings")

// LoadState reads a saved state file.
func LoadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, ErrNoState
	}
	if err != nil {
		return State{}, fmt.Errorf("sa818: read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("sa818: parse state %s: %w", path, err)
	}
	if st.Version != stateVersion {
		return State{}, fmt.Errorf("sa818: state %s has version %d, want %d", path, st.Version, stateVersion)
	}
	return st, nil
}

// SaveState writes the state atomically (temporary file, then rename).
func SaveState(path string, st State) error {
	st.Version = stateVersion
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("sa818: encode state: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sa818-*.tmp")
	if err != nil {
		return fmt.Errorf("sa818: save state: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("sa818: save state: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("sa818: save state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sa818: save state: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("sa818: save state: %w", err)
	}
	return nil
}

// Programmer applies settings and keeps the saved copy.
type Programmer struct {
	Client    *Client
	StatePath string
	// Now defaults to time.Now.
	Now func() time.Time
}

// NewProgrammer returns a programmer on rw that saves to statePath (empty
// disables saving).
func NewProgrammer(rw io.ReadWriter, statePath string) *Programmer {
	return &Programmer{Client: NewClient(rw), StatePath: statePath, Now: time.Now}
}

// Apply programs the module and, only if that fully succeeded, saves the
// settings.
func (p *Programmer) Apply(s Settings) error {
	if err := p.Client.Apply(s); err != nil {
		return err
	}
	if p.StatePath == "" {
		return nil
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	return SaveState(p.StatePath, State{AppliedAt: now().UTC(), Settings: s})
}
