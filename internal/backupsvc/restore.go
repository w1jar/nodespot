package backupsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
	"github.com/w1jar/nodespot/internal/store"
)

// ---------------------------------------------------------------- types

// Warning is a notice about the backup that does not stop the restore.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// User is an account the restored database holds.
type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// ChecklistItem is one thing the backup does not cover.
type ChecklistItem struct {
	Item  string `json:"item"`
	Value string `json:"value"`
}

// Radio is the SA818 channel of the backup.
type Radio struct {
	// Bandwidth is "narrow" (12.5 kHz) or "wide" (25 kHz).
	Bandwidth string  `json:"bandwidth"`
	RxMHz     float64 `json:"rxMHz"`
	TxMHz     float64 `json:"txMHz"`
	// RxTone and TxTone are "none", "CTCSS 100.0" or "DCS 023N".
	RxTone  string `json:"rxTone"`
	TxTone  string `json:"txTone"`
	Squelch int    `json:"squelch"`
}

// BackupInfo describes the backup itself.
type BackupInfo struct {
	Hostname        string `json:"hostname"`
	Created         string `json:"created"`
	CreatedBy       string `json:"createdBy"`
	NodespotVersion string `json:"nodespotVersion"`
	Arch            string `json:"arch"`
	OSID            string `json:"osId,omitempty"`
	OSCodename      string `json:"osCodename,omitempty"`
	OSName          string `json:"osName,omitempty"`
	Callsign        string `json:"callsign"`
	Channel         string `json:"channel"`
}

// NodeInfo describes this node, for comparison with the backup.
type NodeInfo struct {
	Arch       string `json:"arch"`
	OSID       string `json:"osId,omitempty"`
	OSCodename string `json:"osCodename,omitempty"`
	OSName     string `json:"osName,omitempty"`
	Callsign   string `json:"callsign"`
}

// PartsPresent says which parts the backup holds besides the database.
type PartsPresent struct {
	// Node: the node settings (station and every mode).
	Node bool `json:"node"`
	// SA818: the radio module's channel.
	SA818 bool `json:"sa818"`
	// Secrets: the node settings keep their network passwords.
	Secrets bool `json:"secrets"`
	// Conf: nodespot.conf.
	Conf bool `json:"conf"`
}

// Preview is what the restore page shows before anything changes.
type Preview struct {
	Backup    BackupInfo      `json:"backup"`
	Node      NodeInfo        `json:"node"`
	Warnings  []Warning       `json:"warnings"`
	Users     []User          `json:"users"`
	Favorites int             `json:"favorites"`
	Schedules int             `json:"schedules"`
	Checklist []ChecklistItem `json:"checklist"`
	Radio     *Radio          `json:"radio"`
	Parts     PartsPresent    `json:"parts"`
	// Modes are the modes the restored node will have set up.
	Modes []string `json:"modes"`
	// MissingSecrets are the modes left out: the backup has no password
	// for them and this node has none either. Set them up again after the
	// restore.
	MissingSecrets []string `json:"missingSecrets"`
}

// Upload is the answer to a validated upload.
type Upload struct {
	ID      string  `json:"id"`
	Preview Preview `json:"preview"`
}

// Step is one step of an apply.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// ApplyResult is the answer to an apply.
type ApplyResult struct {
	OK         bool   `json:"ok"`
	Steps      []Step `json:"steps"`
	Restarting bool   `json:"restarting"`
	// Error is set when OK is false (additive to the contract).
	Error string `json:"error,omitempty"`
}

// Check is one health check.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Health is the health check after the restart. Pending is true while it
// still runs.
type Health struct {
	OK      bool    `json:"ok"`
	Pending bool    `json:"pending"`
	Checks  []Check `json:"checks"`
}

// Result is the last restore, kept so the page can show it after the
// restart.
type Result struct {
	At            time.Time       `json:"at"`
	BackupCreated time.Time       `json:"backupCreated"`
	Steps         []Step          `json:"steps"`
	Health        Health          `json:"health"`
	Checklist     []ChecklistItem `json:"checklist"`
}

type stored struct {
	Result
}

const resultFile = "restore-result.json"

// ---------------------------------------------------------------- upload

func (s *Service) purgeLocked(now time.Time) {
	for id, p := range s.pending {
		if now.Sub(p.created) > pendingTTL {
			os.RemoveAll(p.dir)
			delete(s.pending, id)
		}
	}
	for len(s.pending) >= maxPending {
		var oldest *pending
		for _, p := range s.pending {
			if oldest == nil || p.created.Before(oldest.created) {
				oldest = p
			}
		}
		os.RemoveAll(oldest.dir)
		delete(s.pending, oldest.id)
	}
}

// Purge removes expired uploads.
func (s *Service) Purge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Now()
	for id, p := range s.pending {
		if now.Sub(p.created) > pendingTTL {
			os.RemoveAll(p.dir)
			delete(s.pending, id)
		}
	}
}

// Upload stores, validates and previews a backup read from r.
func (s *Service) Upload(ctx context.Context, r io.Reader) (Upload, error) {
	s.mu.Lock()
	s.purgeLocked(s.o.Now())
	s.mu.Unlock()
	dir, err := s.newDir("restore-")
	if err != nil {
		return Upload{}, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	arc := filepath.Join(dir, "archive.tar.gz")
	f, err := os.OpenFile(arc, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Upload{}, err
	}
	n, err := io.Copy(f, io.LimitReader(r, backup.MaxUpload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Upload{}, err
	}
	if n > backup.MaxUpload {
		return Upload{}, ErrTooLarge
	}
	in, err := os.Open(arc)
	if err != nil {
		return Upload{}, err
	}
	var sa818 []byte
	dbPath := filepath.Join(dir, "nodespot.db")
	m, err := backup.Read(in, func(e backup.Entry, body io.Reader) error {
		switch e.Path {
		case backup.PathNodespotDB:
			w, err := os.OpenFile(dbPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, body)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			return err
		case backup.PathSA818:
			if e.Size > 1<<20 {
				return nil
			}
			var b bytes.Buffer
			_, err := io.Copy(&b, body)
			sa818 = b.Bytes()
			return err
		}
		return nil
	})
	in.Close()
	if err != nil {
		var nf *backup.NewerFormatError
		if errors.As(err, &nf) {
			return Upload{}, &TooNewError{Msg: "this backup was made by a newer nodespot (backup format " + fmt.Sprint(nf.Version) + "); update nodespot first"}
		}
		return Upload{}, err
	}
	if backup.VersionNewer(m.NodespotVersion, s.o.Version) {
		return Upload{}, &TooNewError{Msg: fmt.Sprintf("this backup was made by nodespot %s, newer than the installed %s; update nodespot first", m.NodespotVersion, s.o.Version)}
	}
	if m.Find(backup.PathNodespotDB) == nil {
		return Upload{}, &backup.Error{Msg: "the backup has no nodespot database"}
	}
	insp, err := store.Inspect(dbPath)
	if err != nil {
		if errors.Is(err, store.ErrSchemaNewer) {
			return Upload{}, &TooNewError{Msg: "the database in this backup is newer than this nodespot understands; update nodespot first"}
		}
		return Upload{}, &backup.Error{Msg: "the nodespot database in the backup is not usable: " + err.Error()}
	}
	in, err = os.Open(arc)
	if err != nil {
		return Upload{}, err
	}
	plan, err := s.o.Helper.BackupRestore(ctx, helper.RestoreRequest{DryRun: true}, in)
	in.Close()
	if err != nil {
		var un *helper.UnavailableError
		if errors.As(err, &un) || errors.Is(err, helper.ErrBusy) {
			return Upload{}, err
		}
		// The helper re-validates the archive as root; what it rejects is
		// a bad backup.
		return Upload{}, &backup.Error{Msg: err.Error()}
	}
	prev := s.preview(m, plan, insp, sa818)
	id := newID()
	final := filepath.Join(s.tmpDir(), "restore-"+id)
	if err := os.Rename(dir, final); err != nil {
		return Upload{}, err
	}
	ok = true
	s.mu.Lock()
	s.pending[id] = &pending{id: id, dir: final, created: s.o.Now(), m: m, prev: prev}
	s.mu.Unlock()
	return Upload{ID: id, Preview: prev}, nil
}

func (s *Service) preview(m *backup.Manifest, plan helper.RestoreResult, insp store.Inspection, sa818 []byte) Preview {
	p := Preview{
		Backup: BackupInfo{Hostname: m.Hostname, Created: m.Created.UTC().Format(time.RFC3339), CreatedBy: m.CreatedBy,
			NodespotVersion: m.NodespotVersion, Arch: m.Arch, OSID: m.OSID, OSCodename: m.OSVersionCodename, OSName: m.OSPrettyName,
			Callsign: m.Callsign, Channel: m.Channel},
		Node:     NodeInfo{Arch: plan.Arch, OSID: plan.OSID, OSCodename: plan.OSCodename, OSName: plan.OSName, Callsign: plan.Callsign},
		Warnings: []Warning{}, Users: []User{}, Checklist: []ChecklistItem{},
		Favorites: insp.Favorites,
		Schedules: insp.Schedules,
		Parts: PartsPresent{Node: m.Parts.Node, SA818: m.Parts.SA818, Secrets: m.Parts.Secrets,
			Conf: m.Find(backup.PathNodespotConf) != nil},
		Modes:          nonNil(plan.Modes),
		MissingSecrets: nonNil(plan.MissingSecrets),
	}
	for _, u := range insp.Users {
		p.Users = append(p.Users, User{Username: u.Username, Role: u.Role})
	}

	// warnings
	if bc, nc := m.OSVersionCodename, plan.OSCodename; bc != "" && nc != "" && bc != nc {
		p.Warnings = append(p.Warnings, Warning{"os_release", crossReleaseMessage(bc, nc)})
	}
	if m.Arch != "" && plan.Arch != "" && m.Arch != plan.Arch {
		p.Warnings = append(p.Warnings, Warning{"arch",
			fmt.Sprintf("The backup was made on a %s system; this node is %s. The settings are portable, but check the radio's audio and PTT settings.", m.Arch, plan.Arch)})
	}
	if len(m.Skipped) > 0 {
		p.Warnings = append(p.Warnings, Warning{"skipped_files",
			fmt.Sprintf("%d item(s) could not be included when the backup was made (for example unreadable files).", len(m.Skipped))})
	}
	if m.Parts.Node && !m.Parts.Secrets {
		p.Warnings = append(p.Warnings, Warning{"no_secrets",
			"The backup was made without the network passwords. The passwords already saved on this node are kept."})
	}
	if len(p.MissingSecrets) > 0 {
		p.Warnings = append(p.Warnings, Warning{"missing_secrets",
			"No password for " + strings.Join(p.MissingSecrets, ", ") + " in the backup or on this node: set these modes up again after the restore."})
	}
	if !m.Parts.Node {
		p.Warnings = append(p.Warnings, Warning{"no_node",
			"The backup holds no node settings; this node's station and modes stay as they are."})
	}

	if len(sa818) > 0 {
		p.Radio = parseRadio(sa818)
	}
	p.Checklist = checklist(m)
	return p
}

// crossReleaseMessage is the warning for a backup made on another Debian
// release than the one this node runs.
func crossReleaseMessage(backupRel, nodeRel string) string {
	return fmt.Sprintf("This backup was made on %s; this node runs %s. nodespot's settings carry over between releases.",
		backup.ReleaseLabel(backupRel), backup.ReleaseLabel(nodeRel))
}

func checklist(m *backup.Manifest) []ChecklistItem {
	c := []ChecklistItem{
		{"Hostname", m.Hostname}, {"Timezone", m.Timezone},
		{"Network and Wi-Fi", "Not included in the backup"},
		{"SSH keys and OS users", "Not included in the backup"},
		{"Firewall rules", "Only nodespot's own rule is set up by the package"},
		{"Cron jobs", "Not included in the backup"},
	}
	if !m.Parts.Secrets && m.Parts.Node {
		c = append(c, ChecklistItem{"Network passwords", "Not included in this backup; the ones saved on this node are kept"})
	}
	if m.Parts.SA818 {
		c = append(c, ChecklistItem{"SA818 radio module", "The node programs the restored channel when it starts"})
	}
	return c
}

// parseRadio summarizes a saved SA818 channel (sa818.json); nil when it
// cannot be read.
func parseRadio(data []byte) *Radio {
	var st sa818.State
	if json.Unmarshal(data, &st) != nil || st.Version != 1 {
		return nil
	}
	set := st.Settings
	r := &Radio{Bandwidth: "narrow", RxMHz: set.RxMHz, TxMHz: set.TxMHz, Squelch: set.Squelch,
		RxTone: toneLabel(set.RxTone), TxTone: toneLabel(set.TxTone)}
	if set.Bandwidth == sa818.Wide {
		r.Bandwidth = "wide"
	}
	return r
}

func toneLabel(t sa818.Tone) string {
	switch t.Kind {
	case sa818.ToneCTCSS:
		return fmt.Sprintf("CTCSS %.1f", t.CTCSSHz)
	case sa818.ToneDCS:
		pol := "N"
		if t.DCSInverted {
			pol = "I"
		}
		return fmt.Sprintf("DCS %03d%s", t.DCSCode, pol)
	}
	return "none"
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Cancel discards an upload.
func (s *Service) Cancel(id string) {
	s.mu.Lock()
	p := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if p != nil {
		os.RemoveAll(p.dir)
	}
}

// Has reports whether an upload exists.
func (s *Service) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[id]
	return ok
}

// ---------------------------------------------------------------- apply

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// StagedPath is the staged database ApplyStaged swaps in at startup.
func StagedPath(dataDir string) string { return filepath.Join(dataDir, "nodespot.db.restore") }

// Apply runs the restore of upload id for who: the helper applies the system
// part, then nodespot's database is staged for the swap at the next start. On
// success the caller must restart the server (Restarting is true).
func (s *Service) Apply(ctx context.Context, id string, who string) (ApplyResult, error) {
	if !s.apply.TryLock() {
		return ApplyResult{}, ErrBusy
	}
	defer s.apply.Unlock()
	s.mu.Lock()
	p := s.pending[id]
	s.mu.Unlock()
	if p == nil {
		return ApplyResult{}, ErrNotFound
	}
	staged := StagedPath(s.o.DataDir)
	part := staged + ".part"
	_ = os.Remove(part)
	if err := copyFile(filepath.Join(p.dir, "nodespot.db"), part); err != nil {
		return ApplyResult{}, err
	}
	detail, _ := json.Marshal(map[string]any{"backupCreated": p.m.Created.UTC().Format(time.RFC3339), "backupHostname": p.m.Hostname})
	au := store.Audit{Username: who, Action: "backup.restore", Detail: string(detail), Time: s.o.Now()}
	if err := store.MigrateFile(part, &au); err != nil {
		os.Remove(part)
		return ApplyResult{}, &backup.Error{Msg: "the nodespot database in the backup cannot be upgraded: " + err.Error()}
	}
	for _, suf := range []string{"-wal", "-shm"} {
		_ = os.Remove(part + suf)
	}
	arc, err := os.Open(filepath.Join(p.dir, "archive.tar.gz"))
	if err != nil {
		os.Remove(part)
		return ApplyResult{}, err
	}
	res, err := s.o.Helper.BackupRestore(ctx, helper.RestoreRequest{}, arc)
	arc.Close()
	if err != nil {
		os.Remove(part)
		return ApplyResult{}, err
	}
	out := ApplyResult{Steps: []Step{}}
	for _, st := range res.Steps {
		out.Steps = append(out.Steps, Step{Name: st.Name, OK: st.OK, Detail: st.Detail})
	}
	if !res.OK {
		os.Remove(part)
		out.Error = res.Error
		return out, nil
	}
	health := Health{Pending: true, Checks: []Check{}}
	if s.o.Demo {
		os.Remove(part)
		out.Steps = append(out.Steps, Step{Name: "Stage nodespot database", OK: true, Detail: "skipped in demo mode"})
		out.OK = true
		health = Health{OK: true, Checks: []Check{{Name: "Demo node", OK: true, Detail: "nothing to restart in demo mode"}}}
	} else if err := os.Rename(part, staged); err != nil {
		os.Remove(part)
		out.Steps = append(out.Steps, Step{Name: "Stage nodespot database", OK: false, Detail: err.Error()})
		out.Error = "Stage nodespot database: " + err.Error()
		return out, nil
	}
	if !s.o.Demo {
		out.Steps = append(out.Steps, Step{Name: "Stage nodespot database", OK: true,
			Detail: "replaced when nodespot restarts; the previous database is kept as nodespot.db.pre-restore"})
		out.OK, out.Restarting = true, true
	}
	r := stored{Result: Result{At: s.o.Now().UTC(), BackupCreated: p.m.Created.UTC(), Steps: out.Steps,
		Health: health, Checklist: p.prev.Checklist}}
	if err := s.saveResult(r); err != nil {
		s.o.Logger.Printf("restore: saving the result: %v", err)
	}
	s.Cancel(id)
	return out, nil
}

// ---------------------------------------------------------------- result

func (s *Service) resultPath() string { return filepath.Join(s.o.DataDir, resultFile) }

func (s *Service) saveResult(r stored) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := s.resultPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.resultPath())
}

func (s *Service) loadResult() (stored, bool) {
	b, err := os.ReadFile(s.resultPath())
	if err != nil {
		return stored{}, false
	}
	var r stored
	if json.Unmarshal(b, &r) != nil {
		return stored{}, false
	}
	if r.Steps == nil {
		r.Steps = []Step{}
	}
	if r.Health.Checks == nil {
		r.Health.Checks = []Check{}
	}
	if r.Checklist == nil {
		r.Checklist = []ChecklistItem{}
	}
	return r, true
}

// LastResult returns the last restore, if any.
func (s *Service) LastResult() (Result, bool) {
	r, ok := s.loadResult()
	return r.Result, ok
}

// RunHealth completes the health check of a restore that is waiting for it
// (after the restart): run is retried until every check passes or the time
// is up, and each state is saved.
func (s *Service) RunHealth(ctx context.Context, run func(ctx context.Context) []Check) {
	r, ok := s.loadResult()
	if !ok || !r.Health.Pending {
		return
	}
	deadline := s.o.Now().Add(3 * time.Minute)
	for {
		checks := run(ctx)
		all := true
		for _, c := range checks {
			all = all && c.OK
		}
		done := all || s.o.Now().After(deadline)
		r.Health = Health{OK: all, Pending: !done, Checks: nonNilChecks(checks)}
		if err := s.saveResult(r); err != nil {
			s.o.Logger.Printf("restore: saving the health check: %v", err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func nonNilChecks(c []Check) []Check {
	if c == nil {
		return []Check{}
	}
	return c
}

// ---------------------------------------------------------------- startup

// ApplyStaged swaps a staged restore database in before the store is opened
// (nodespot restarts after a restore). The previous database is kept as
// nodespot.db.pre-restore. A staged file that does not check out is set aside
// as nodespot.db.restore-rejected and the current database stays.
func ApplyStaged(dataDir string) (swapped bool, err error) {
	staged := StagedPath(dataDir)
	if _, err := os.Stat(staged); err != nil {
		return false, nil
	}
	if _, err := store.Inspect(staged); err != nil {
		_ = os.Rename(staged, staged+"-rejected")
		return false, fmt.Errorf("staged restore database rejected: %w", err)
	}
	live := filepath.Join(dataDir, "nodespot.db")
	prev := live + ".pre-restore"
	for _, suf := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(prev + suf)
	}
	for _, suf := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(live+suf, prev+suf); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("moving the current database aside: %w", err)
		}
	}
	for _, suf := range []string{"-wal", "-shm"} {
		_ = os.Remove(staged + suf)
	}
	if err := os.Rename(staged, live); err != nil {
		return false, fmt.Errorf("installing the restored database: %w", err)
	}
	return true, nil
}

// SpoolTemp creates a private temporary file for an upload in transit.
func (s *Service) SpoolTemp() (*os.File, error) {
	if err := os.MkdirAll(s.tmpDir(), 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(s.tmpDir(), "upload-")
}
