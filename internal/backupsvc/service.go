// Package backupsvc implements backup creation and full restore for the
// server (docs/BACKUP.md): it assembles the archive from the helper's system
// part and nodespot's own database, validates uploads, previews and applies
// them, and carries the staged database swap across the restart.
package backupsvc

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// Helper is what the service needs from the privileged helper.
type Helper interface {
	BackupInfo(ctx context.Context) (helper.BackupInfo, error)
	BackupCreate(ctx context.Context, req helper.BackupRequest) (*helper.BackupStream, error)
	BackupRestore(ctx context.Context, req helper.RestoreRequest, archive io.Reader) (helper.RestoreResult, error)
}

// Errors the API maps to status codes.
var (
	ErrNotFound = errors.New("no such restore")
	ErrTooLarge = errors.New("the backup is larger than 256 MiB")
	ErrBusy     = errors.New("a restore is already running")
)

// TooNewError means the backup comes from a newer nodespot.
type TooNewError struct{ Msg string }

func (e *TooNewError) Error() string { return e.Msg }

// Options configure the service.
type Options struct {
	Helper  Helper
	Store   *store.Store
	DataDir string
	// ConfPath is the nodespot.conf included in the backup ("" or unreadable:
	// left out).
	ConfPath string
	Version  string
	Hostname func() string
	Timezone func() string
	Now      func() time.Time
	Logger   *log.Logger
	// Demo: the nodespot database is not replaced and no restart follows (the
	// demo has nothing to restart into); the result is complete at once.
	Demo bool
}

// Service creates backups and runs restores.
type Service struct {
	o Options

	mu      sync.Mutex
	pending map[string]*pending
	apply   sync.Mutex
}

type pending struct {
	id      string
	dir     string
	created time.Time
	m       *backup.Manifest
	prev    Preview
}

const (
	pendingTTL = time.Hour
	maxPending = 3
	tmpName    = "tmp"
)

// New creates the service and removes leftovers of a previous run.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.Hostname == nil {
		o.Hostname = func() string { h, _ := os.Hostname(); return h }
	}
	if o.Timezone == nil {
		o.Timezone = systemTimezone
	}
	s := &Service{o: o, pending: map[string]*pending{}}
	_ = os.RemoveAll(s.tmpDir())
	return s
}

func (s *Service) tmpDir() string { return filepath.Join(s.o.DataDir, tmpName) }

func (s *Service) newDir(prefix string) (string, error) {
	if err := os.MkdirAll(s.tmpDir(), 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(s.tmpDir(), prefix)
}

func systemTimezone() string {
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" && len(tz) < 100 {
			return tz
		}
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(l, "zoneinfo/"); i >= 0 {
			return l[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

// Info reports which optional parts exist on this node.
func (s *Service) Info(ctx context.Context) (helper.BackupInfo, error) {
	return s.o.Helper.BackupInfo(ctx)
}

// ---------------------------------------------------------------- create

// Created is a finished backup archive in a private temporary file.
type Created struct {
	Name string
	Size int64
	File *os.File
	dir  string
}

// Close removes the temporary archive.
func (c *Created) Close() error {
	err := c.File.Close()
	_ = os.RemoveAll(c.dir)
	return err
}

var reHostChars = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

func fileName(host string, now time.Time) string {
	h := strings.Trim(reHostChars.ReplaceAllString(host, "-"), "-.")
	if h == "" {
		h = "node"
	}
	if len(h) > 60 {
		h = h[:60]
	}
	return "nodespot-backup-" + h + "-" + now.Format("20060102-1504") + ".tar.gz"
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// Create builds a backup archive for who. The caller must Close it.
func (s *Service) Create(ctx context.Context, req helper.BackupRequest, who string) (*Created, error) {
	dir, err := s.newDir("backup-")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	bs, err := s.o.Helper.BackupCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	defer bs.Close()
	h := bs.Header

	type extra struct {
		e   backup.Entry
		src string
	}
	var extras []extra
	dbPath := filepath.Join(dir, "nodespot.db")
	if err := s.o.Store.Snapshot(dbPath); err != nil {
		return nil, err
	}
	n, sum, err := hashFile(dbPath)
	if err != nil {
		return nil, err
	}
	extras = append(extras, extra{backup.Entry{Path: backup.PathNodespotDB, Type: backup.TypeFile, Size: n, SHA256: sum,
		Owner: "nodespot", Group: "nodespot", Mode: "0600"}, dbPath})
	skipped := append([]string{}, h.Skipped...)
	if s.o.ConfPath != "" {
		if fi, err := os.Stat(s.o.ConfPath); err == nil && fi.Mode().IsRegular() && fi.Size() <= 1<<20 {
			if n, sum, err := hashFile(s.o.ConfPath); err == nil {
				extras = append(extras, extra{backup.Entry{Path: backup.PathNodespotConf, Type: backup.TypeFile, Size: n, SHA256: sum,
					Owner: "root", Group: "nodespot", Mode: backup.FormatMode(uint32(fi.Mode().Perm()) & 0o664)}, s.o.ConfPath})
			} else {
				skipped = append(skipped, "nodespot.conf (unreadable)")
			}
		} else {
			skipped = append(skipped, "nodespot.conf (unreadable)")
		}
	}

	now := s.o.Now()
	m := &backup.Manifest{
		Format: backup.FormatVersion, Created: now.UTC().Truncate(time.Second), CreatedBy: who, NodespotVersion: s.o.Version,
		Hostname: s.o.Hostname(), Timezone: s.o.Timezone(), Arch: h.Arch, OSID: h.OSID, OSVersionCodename: h.OSCodename,
		OSPrettyName: h.OSName, Callsign: h.Callsign, Channel: h.Channel, Parts: h.Parts, Skipped: skipped,
	}
	order := append([]backup.Entry(nil), h.Entries...) // NewWriter sorts m.Files in place
	m.Files = append(append([]backup.Entry(nil), h.Entries...), func() []backup.Entry {
		var es []backup.Entry
		for _, x := range extras {
			es = append(es, x.e)
		}
		return es
	}()...)

	arcPath := filepath.Join(dir, "archive.tar.gz")
	out, err := os.OpenFile(arcPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	closeOut := true
	defer func() {
		if closeOut {
			out.Close()
		}
	}()
	w, err := backup.NewWriter(out, m)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	tr := tar.NewReader(bs.Body)
	for _, e := range order {
		hd, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("the helper's backup stream ended early: %w", err)
		}
		if hd.Name != e.Path {
			return nil, fmt.Errorf("the helper's backup stream is out of order at %q", e.Path)
		}
		if err := w.Add(e.Path, tr); err != nil {
			return nil, err
		}
	}
	for _, x := range extras {
		f, err := os.Open(x.src)
		if err != nil {
			return nil, err
		}
		err = w.Add(x.e.Path, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	fi, err := out.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > backup.MaxUpload {
		return nil, ErrTooLarge
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok, closeOut = true, false
	return &Created{Name: fileName(m.Hostname, now), Size: fi.Size(), File: out, dir: dir}, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
