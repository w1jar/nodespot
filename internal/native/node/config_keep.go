package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SaveConfigKeep is [SaveConfig] for a tool that runs as another user than
// the node (root, for the calibration command): the file is replaced
// atomically as before, but a file that exists keeps its permission bits and
// owner, so the node can still read and write it. A new file is created as
// [SaveConfig] does (mode 0600, the caller's own owner). c is validated
// first.
func SaveConfigKeep(path string, c *Config) error {
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')
	old, statErr := os.Stat(path)
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
	mode := os.FileMode(0o600)
	if statErr == nil {
		mode = old.Mode().Perm()
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if statErr == nil {
		if err := keepOwner(f, old); err != nil {
			f.Close()
			return fmt.Errorf("save %s: %w", filepath.Base(path), err)
		}
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
