package helper

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/native/mode"
	_ "github.com/w1jar/nodespot/internal/native/mode/all" // the mode specs name the secrets
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

// Default locations of the backup parts.
const (
	DefaultNodespotConf = "/etc/nodespot/nodespot.conf"
	DefaultOSRelease    = "/etc/os-release"
	// DefaultNodeUser owns the node's files.
	DefaultNodeUser = "nodespot-node"
	defaultTempDir  = "/var/tmp"
	nodeService     = "nodespot-node"

	maxNodeFile = 4 << 20
)

func (o *Ops) nodeConfig() string   { return orDefault(o.NodeConfig, node.DefaultConfigPath) }
func (o *Ops) sa818State() string   { return orDefault(o.SA818State, sa818.DefaultStatePath) }
func (o *Ops) nodeUser() string     { return orDefault(o.NodeUser, DefaultNodeUser) }
func (o *Ops) nodespotConf() string { return orDefault(o.NodespotConf, DefaultNodespotConf) }
func (o *Ops) osRelease() string    { return orDefault(o.OSRelease, DefaultOSRelease) }
func (o *Ops) tempDirRoot() string  { return orDefault(o.TempDir, defaultTempDir) }

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

func (o *Ops) arch(ctx context.Context) string {
	out, code, err := o.Run.Run(ctx, "dpkg", "--print-architecture")
	if a := strings.TrimSpace(out); err == nil && code == 0 && a != "" && len(a) < 32 && !strings.ContainsAny(a, " \n\t") {
		return a
	}
	return runtime.GOARCH
}

func (o *Ops) osInfo() backup.OSInfo {
	if b, err := os.ReadFile(o.osRelease()); err == nil {
		return backup.ParseOSRelease(b)
	}
	return backup.OSInfo{}
}

// ---------------------------------------------------------------- node config

// parseNodeConfig decodes a node config like node.LoadConfig does (unknown
// fields refused, newer versions refused, normalized) without validating it.
func parseNodeConfig(b []byte) (*node.Config, error) {
	var c node.Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("the node config is not valid: %w", err)
	}
	if c.Version > node.ConfigVersion {
		return nil, fmt.Errorf("the node config has version %d, newer than this nodespot knows (%d); update nodespot first", c.Version, node.ConfigVersion)
	}
	if c.Modes == nil {
		c.Modes = map[string]mode.Settings{}
	}
	c.Normalize()
	return &c, nil
}

func encodeNodeConfig(c *node.Config) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// stripSecrets blanks every secret mode setting. A mode this build does not
// know keeps no values at all (its secrets are not known).
func stripSecrets(c *node.Config) {
	for name, s := range c.Modes {
		sp, ok := mode.Lookup(name)
		if !ok {
			s.Values = nil
			c.Modes[name] = s
			continue
		}
		red, _ := sp.Redact(s)
		c.Modes[name] = red
	}
}

// hasSecrets reports whether any mode holds a secret.
func hasSecrets(c *node.Config) bool {
	for name, s := range c.Modes {
		if sp, ok := mode.Lookup(name); ok {
			if len(sp.Secrets(s)) > 0 {
				return true
			}
		} else if len(s.Values) > 0 {
			return true
		}
	}
	return false
}

// validSA818 checks a saved SA818 channel file.
func validSA818(b []byte) error {
	var st sa818.State
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("the SA818 settings are not valid: %w", err)
	}
	if st.Version != 1 {
		return fmt.Errorf("the SA818 settings have version %d, which this nodespot does not know", st.Version)
	}
	return nil
}

// ---------------------------------------------------------------- info

// BackupInfo reports what this node has to back up.
func (o *Ops) BackupInfo(ctx context.Context) (BackupInfo, error) {
	var info BackupInfo
	if b, _, err := readRegular(o.nodeConfig(), maxNodeFile); err == nil {
		info.Node = true
		if c, err := parseNodeConfig(b); err == nil {
			info.Secrets = hasSecrets(c)
		}
	}
	if _, _, err := readRegular(o.sa818State(), maxNodeFile); err == nil {
		info.SA818 = true
	}
	return info, nil
}

// ---------------------------------------------------------------- create

type nodePart struct {
	e    backup.Entry
	data []byte
}

func (o *Ops) entry(path string, data []byte, fi os.FileInfo) backup.Entry {
	e := backup.Entry{Path: path, Type: backup.TypeFile, Owner: "root", Group: "root", Mode: backup.FormatMode(uint32(fi.Mode().Perm()))}
	if uid, gid, ok := ownerIDs(fi); ok {
		if n := lookupName(uid, false); backup.ValidName(n) {
			e.Owner = n
		}
		if n := lookupName(gid, true); backup.ValidName(n) {
			e.Group = n
		}
	}
	e.Size, e.SHA256 = backup.Sum(data)
	return e
}

// BackupCreate writes the header line and the tar stream of the node part
// (node.json, sa818.json) to w. sent reports whether anything was written
// (so a failure before the header can still be answered with an error
// line).
func (o *Ops) BackupCreate(ctx context.Context, req *BackupRequest, w io.Writer) (sent bool, err error) {
	if req == nil {
		req = &BackupRequest{}
	}
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	osi := o.osInfo()
	hdr := BackupHeader{Arch: o.arch(ctx), OSID: osi.ID, OSCodename: osi.Codename, OSName: osi.Pretty,
		Skipped: []string{}, Entries: []backup.Entry{}}
	hdr.Channel, _ = readChannel(o.sourceList())
	var parts []nodePart
	if b, fi, err := readRegular(o.nodeConfig(), maxNodeFile); err == nil {
		c, err := parseNodeConfig(b)
		if err != nil {
			return false, err
		}
		hdr.Callsign = c.Station.Callsign
		if !req.Secrets {
			stripSecrets(c)
		}
		if b, err = encodeNodeConfig(c); err != nil {
			return false, err
		}
		parts = append(parts, nodePart{o.entry(backup.PathNodeConfig, b, fi), b})
		hdr.Parts.Node, hdr.Parts.Secrets = true, req.Secrets
	} else if !errors.Is(err, os.ErrNotExist) {
		hdr.Skipped = append(hdr.Skipped, "node.json (unreadable)")
	}
	if b, fi, err := readRegular(o.sa818State(), maxNodeFile); err == nil {
		parts = append(parts, nodePart{o.entry(backup.PathSA818, b, fi), b})
		hdr.Parts.SA818 = true
	} else if !errors.Is(err, os.ErrNotExist) {
		hdr.Skipped = append(hdr.Skipped, "sa818.json (unreadable)")
	}
	for _, p := range parts {
		hdr.Entries = append(hdr.Entries, p.e)
	}
	line, err := json.Marshal(hdr)
	if err != nil {
		return false, err
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return true, err
	}
	tw := tar.NewWriter(w)
	now := time.Now()
	for _, p := range parts {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		h := &tar.Header{Name: p.e.Path, Mode: 0o600, ModTime: now, Typeflag: tar.TypeReg, Size: p.e.Size}
		if err := tw.WriteHeader(h); err != nil {
			return true, err
		}
		if _, err := tw.Write(p.data); err != nil {
			return true, err
		}
	}
	return true, tw.Close()
}
