// Package updates reports and drives nodespot package updates. Checking needs no
// package manager: the manager fetches <updates url>/latest.json (release
// channel) or latest-beta.json (beta channel) and compares its version with
// the installed one (the running binary's version, which is
// the Debian package version) using the dpkg algorithm. Installing goes
// through the privileged helper and always needs an explicit apply.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/debver"
	"github.com/w1jar/nodespot/internal/helper"
)

// Helper is the part of the helper client the manager needs.
type Helper interface {
	UpdateStatus(ctx context.Context) (helper.Status, error)
	UpdateApply(ctx context.Context) (helper.Status, error)
	// SetChannel switches the apt suite ("stable" or "beta").
	SetChannel(ctx context.Context, channel string) (helper.Status, error)
}

// Notes is the content of latest.json.
type Notes struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	Date    string `json:"date"`
}

// Update channels.
const (
	ChannelStable = helper.ChannelStable
	ChannelBeta   = helper.ChannelBeta
)

// ValidChannel reports whether c is exactly "stable" or "beta".
func ValidChannel(c string) bool { return c == ChannelStable || c == ChannelBeta }

// FetchFunc fetches latest.json (channel "stable") or latest-beta.json
// (channel "beta"). bust asks for a fresh copy (the CDN caches the file for
// minutes).
type FetchFunc func(ctx context.Context, channel string, bust bool) (Notes, error)

// Info is the API object of GET /api/updates.
type Info struct {
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
	// ApplyReason is set when checks work but the Update button cannot
	// (helper not running, no permission).
	ApplyReason *string `json:"applyReason"`
	// Channel is the update channel: "stable" or "beta" (stable when unknown).
	Channel    string  `json:"channel"`
	Installed  string  `json:"installed"`
	Candidate  string  `json:"candidate"`
	Upgradable bool    `json:"upgradable"`
	Job        string  `json:"job"`
	JobLog     string  `json:"jobLog"`
	Notes      *string `json:"notes"`
	Checked    *string `json:"checked"`
	// OS is the operating system (apt) update state; nil when the server has
	// no OS update manager.
	OS *OSInfo `json:"os,omitempty"`
}

// ArmhfReason is the ApplyReason of a 32-bit (armhf) install.
const ArmhfReason = "this is a 32-bit (armhf) system and nodespot is no longer built for it (ASL3 has no armhf packages); reinstall with Raspberry Pi OS Lite (64-bit) to get updates"

// Errors.
var (
	// ErrUnavailable is returned when updates cannot be used at all.
	ErrUnavailable = errors.New("updates are not available")
	// ErrNoSource is returned by Check when no update URL is configured.
	ErrNoSource = errors.New("update checks are disabled: no [updates] url configured")
)

// CheckError is a failed latest.json fetch.
type CheckError struct{ Err error }

func (e *CheckError) Error() string { return "could not check for updates: " + e.Err.Error() }
func (e *CheckError) Unwrap() error { return e.Err }

// IsPackageVersion reports whether a binary version string looks like a
// Debian package version, as opposed to "dev" or a git describe string.
func IsPackageVersion(v string) bool {
	if v == "" || v == "dev" || strings.Contains(v, "~git") || gitDescribe.MatchString(v) {
		return false
	}
	return debver.Valid(v)
}

var gitDescribe = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$|-dirty$`)

// Manager caches update information.
type Manager struct {
	Helper Helper // nil: installing unavailable
	Fetch  FetchFunc
	// OS manages operating system (apt) updates (nil: not offered).
	OS     *OSManager
	Logger interface{ Printf(string, ...any) }
	// FirstCheck and Interval control the background check.
	FirstCheck time.Duration
	Interval   time.Duration
	// HelperTimeout bounds the live job status request of Info.
	HelperTimeout time.Duration

	// GOARCH is the architecture of the running binary (default
	// runtime.GOARCH); "arm" is a 32-bit armhf install, which gets no updates.
	GOARCH string

	binVersion string // installed version from the binary, "" when not a package build

	checkMu sync.Mutex // serialises latest.json fetches

	mu      sync.Mutex
	checked time.Time
	latest  Notes
	have    bool
	latestC string // channel latest was fetched for
	dpkgVer string // last installed version reported by the helper
	channel string // channel last reported by the helper, "" unknown
}

// New returns a Manager. installed is the running binary's version; when it
// is not a package version the helper's dpkg-query answer is used instead.
// fetch may be nil (update checks disabled).
func New(h Helper, installed string, fetch FetchFunc) *Manager {
	m := &Manager{GOARCH: runtime.GOARCH, Helper: h, Fetch: fetch, FirstCheck: time.Minute, Interval: 6 * time.Hour, HelperTimeout: 5 * time.Second}
	if IsPackageVersion(installed) {
		m.binVersion = strings.TrimSpace(installed)
	}
	return m
}

// reasonFor turns a helper error into a user-facing reason.
func reasonFor(err error) string {
	var ue *helper.UnavailableError
	if errors.As(err, &ue) {
		return ue.Reason
	}
	return err.Error()
}

// channelLocked returns the effective update channel (stable when unknown).
func (m *Manager) channelLocked() string {
	if m.channel == ChannelBeta {
		return ChannelBeta
	}
	return ChannelStable
}

// learn records what the helper reported.
func (m *Manager) learn(st helper.Status) {
	if st.Installed != "" {
		m.dpkgVer = st.Installed
	}
	if ValidChannel(st.Channel) {
		m.channel = st.Channel
	}
}

// installedLocked returns the installed version. The binary's version wins,
// unless dpkg already has a newer one (upgrade finished, restart pending).
func (m *Manager) installedLocked() string {
	switch {
	case m.binVersion == "":
		return m.dpkgVer
	case m.dpkgVer != "" && debver.Compare(m.dpkgVer, m.binVersion) > 0:
		return m.dpkgVer
	}
	return m.binVersion
}

// Info returns the cached state plus the helper's cheap live job status. It
// never runs a package manager and never touches the network.
func (m *Manager) Info(ctx context.Context) Info {
	var st helper.Status
	var herr error
	if m.Helper != nil {
		hctx, cancel := context.WithTimeout(ctx, m.HelperTimeout)
		st, herr = m.Helper.UpdateStatus(hctx)
		cancel()
	}
	in := m.compose(st, herr)
	if m.OS != nil {
		os := m.OS.Info(ctx)
		in.OS = &os
	}
	return in
}

func (m *Manager) compose(st helper.Status, herr error) Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	if herr == nil && m.Helper != nil {
		m.learn(st)
	}
	installed := m.installedLocked()
	if installed == "" {
		reason := "not installed from a package"
		if herr != nil {
			reason = reasonFor(herr)
		}
		return Info{Reason: &reason, Channel: m.channelLocked(), Job: helper.JobIdle}
	}
	in := Info{Available: true, Installed: installed, Channel: m.channelLocked(), Job: helper.JobIdle}
	switch {
	case m.Helper == nil:
		in.ApplyReason = ptr("helper not available")
	case herr != nil:
		in.ApplyReason = ptr(reasonFor(herr))
	default:
		in.Job, in.JobLog = st.Job, st.JobLog
	}
	if m.GOARCH == "arm" {
		// latest.json is not per architecture, but the apt repository has no
		// new armhf packages: an apply would end with "already newest".
		in.ApplyReason = ptr(ArmhfReason)
	}
	if m.have && m.latestC == m.channelLocked() && debver.Valid(m.latest.Version) {
		in.Candidate = m.latest.Version
		in.Upgradable = debver.Compare(m.latest.Version, installed) > 0
		if in.Upgradable && m.latest.Notes != "" {
			in.Notes = ptr(m.latest.Notes)
		}
	}
	if !m.checked.IsZero() {
		in.Checked = ptr(m.checked.UTC().Format(time.RFC3339))
	}
	return in
}

// syncChannel asks the helper (cheap: no apt) for the channel and installed
// version, so a channel changed on the command line is noticed.
func (m *Manager) syncChannel(ctx context.Context) {
	if m.Helper == nil {
		return
	}
	hctx, cancel := context.WithTimeout(ctx, m.HelperTimeout)
	defer cancel()
	if st, err := m.Helper.UpdateStatus(hctx); err == nil {
		m.mu.Lock()
		m.learn(st)
		m.mu.Unlock()
	}
}

// refresh fetches the channel's latest json and records it.
func (m *Manager) refresh(ctx context.Context, bust bool) error {
	if m.Fetch == nil {
		return ErrNoSource
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	m.syncChannel(ctx)
	m.mu.Lock()
	ch := m.channelLocked()
	m.mu.Unlock()
	n, err := m.Fetch(ctx, ch, bust)
	if err == nil && !debver.Valid(n.Version) {
		err = fmt.Errorf("%s: invalid version %q", latestFile(ch), n.Version)
	}
	if err != nil {
		return &CheckError{Err: err}
	}
	m.mu.Lock()
	m.latest, m.latestC, m.have, m.checked = n, ch, true, time.Now()
	m.mu.Unlock()
	return nil
}

// SetChannel switches the update channel through the helper (refused with
// helper.ErrBusy while an upgrade runs), then runs a fresh check for the new
// channel. A failed check does not fail the switch: it is logged and the
// returned Info simply has no candidate yet.
func (m *Manager) SetChannel(ctx context.Context, channel string) (Info, error) {
	if !ValidChannel(channel) {
		return Info{}, fmt.Errorf("invalid channel %q", channel)
	}
	if m.Helper == nil {
		return Info{}, ErrUnavailable
	}
	st, err := m.Helper.SetChannel(ctx, channel)
	if err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	m.learn(st)
	m.channel = channel
	m.mu.Unlock()
	if m.Fetch != nil {
		if err := m.refresh(ctx, true); err != nil && m.Logger != nil {
			m.Logger.Printf("updates: check after channel change: %v", err)
		}
	}
	return m.Info(ctx), nil
}

// Check fetches latest.json right now (with a cache buster) and returns the
// resulting state. The helper is only asked for the cheap job status.
func (m *Manager) Check(ctx context.Context) (Info, error) {
	if m.Helper == nil && m.binVersion == "" {
		return Info{}, ErrUnavailable
	}
	if err := m.refresh(ctx, true); err != nil {
		return Info{}, err
	}
	return m.Info(ctx), nil
}

// Apply starts the upgrade.
func (m *Manager) Apply(ctx context.Context) (Info, error) {
	if m.Helper == nil {
		return Info{}, ErrUnavailable
	}
	st, err := m.Helper.UpdateApply(ctx)
	if err != nil {
		return Info{}, err
	}
	in := m.compose(st, nil)
	if m.OS != nil {
		os := m.OS.Info(ctx)
		in.OS = &os
	}
	return in, nil
}

// Pending reports, from cached data only, whether an update is available and
// its version.
func (m *Manager) Pending() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.installedLocked()
	if !m.have || m.latestC != m.channelLocked() || inst == "" || !debver.Valid(m.latest.Version) || debver.Compare(m.latest.Version, inst) <= 0 {
		return false, ""
	}
	return true, m.latest.Version
}

// Run performs the periodic background check until ctx is done: the first one
// shortly after start, then every Interval. It only refreshes information.
func (m *Manager) Run(ctx context.Context) {
	if m.Fetch == nil || (m.Helper == nil && m.binVersion == "") {
		return
	}
	t := time.NewTimer(m.FirstCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// A non-package build learns its installed version from the helper;
		// make sure it is known before the first Pending call.
		m.syncChannel(ctx)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := m.refresh(cctx, false); err != nil && m.Logger != nil {
			m.Logger.Printf("updates: background check: %v", err)
		}
		cancel()
		t.Reset(m.Interval)
	}
}

func ptr[T any](v T) *T { return &v }

const (
	// maxNotes bounds the stored release notes.
	maxNotes = 8000
	// maxLatest bounds the latest.json download.
	maxLatest = 64 << 10
	// fetchTimeout bounds one latest.json request.
	fetchTimeout = 10 * time.Second
)

// latestFile is the file of a channel under the update URL.
func latestFile(channel string) string {
	if channel == ChannelBeta {
		return "latest-beta.json"
	}
	return "latest.json"
}

// HTTPLatest returns a FetchFunc fetching <baseURL>/latest.json (stable) or
// <baseURL>/latest-beta.json (beta). An empty URL
// yields nil (update checks disabled). With bust a ?t=<unix> query defeats
// CDN caching.
func HTTPLatest(baseURL string, client *http.Client) FetchFunc {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	return func(ctx context.Context, channel string, bust bool) (Notes, error) {
		ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		file := latestFile(channel)
		u := baseURL + "/" + file
		if bust {
			u += "?t=" + strconv.FormatInt(time.Now().Unix(), 10)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return Notes{}, err
		}
		req.Header.Set("Cache-Control", "no-cache")
		resp, err := client.Do(req)
		if err != nil {
			return Notes{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return Notes{}, fmt.Errorf("%s: %s", file, resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxLatest+1))
		if err != nil {
			return Notes{}, fmt.Errorf("%s: %w", file, err)
		}
		if len(data) > maxLatest {
			return Notes{}, fmt.Errorf("%s: too large", file)
		}
		var n Notes
		if err := json.Unmarshal(data, &n); err != nil {
			return Notes{}, fmt.Errorf("%s: %w", file, err)
		}
		n.Version = strings.TrimSpace(n.Version)
		if r := []rune(n.Notes); len(r) > maxNotes {
			n.Notes = string(r[:maxNotes]) + "…"
		}
		return n, nil
	}
}
