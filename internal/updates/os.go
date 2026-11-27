package updates

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
)

// OSHelper is the part of the helper client the OS update manager needs.
type OSHelper interface {
	OSCheck(ctx context.Context) (helper.OSStatus, error)
	OSStatus(ctx context.Context) (helper.OSStatus, error)
	OSApply(ctx context.Context) (helper.OSStatus, error)
	Reboot(ctx context.Context) error
}

// Settings is the persistent key/value store (store.Store).
type Settings interface {
	GetSetting(k string) (string, error)
	SetSetting(k, v string) error
}

// Setting keys of the persisted OS check.
const (
	keyOSChecked = "updates.os.checked" // RFC3339 time of the last successful check
	keyOSResult  = "updates.os.result"  // JSON osResult
	keyOSRecheck = "updates.os.recheck" // "1": re-check once the running job ends
)

// OSInfo is the "os" object of GET /api/updates.
type OSInfo struct {
	Available      bool               `json:"available"`
	Reason         *string            `json:"reason"`
	CheckedAt      *string            `json:"checkedAt"`
	Checking       bool               `json:"checking"`
	Error          *string            `json:"error"`
	Packages       []helper.OSPackage `json:"packages"`
	KeptBack       []string           `json:"keptBack"`
	RebootRequired bool               `json:"rebootRequired"`
	RebootPkgs     []string           `json:"rebootPkgs"`
	Job            string             `json:"job"`
	JobLog         string             `json:"jobLog"`
	// Warnings of the last check, e.g. that a package repository could not
	// be refreshed.
	Warnings []string `json:"warnings"`
}

// OSCounts is what the header badge needs: no package names.
type OSCounts struct {
	Count    int  `json:"count"`
	Security int  `json:"security"`
	Reboot   bool `json:"reboot"`
}

type osResult struct {
	Packages   []helper.OSPackage `json:"packages"`
	KeptBack   []string           `json:"keptBack"`
	Reboot     bool               `json:"reboot"`
	RebootPkgs []string           `json:"rebootPkgs"`
	Warnings   []string           `json:"warnings,omitempty"`
}

// OSManager checks for operating system (apt) updates once a day, on demand,
// and after an upgrade, and drives the upgrade and the reboot through the
// helper. The last result and its time are persisted so a restart does not
// check again.
type OSManager struct {
	Helper   OSHelper
	Store    Settings // nil: nothing persisted
	Logger   interface{ Printf(string, ...any) }
	OnChange func() // called after the cached state changed

	// FirstCheck is the delay of the first check after start (when the last
	// one is older than Interval); Interval the time between checks; Tick
	// how often Run looks at the clock; Retry and BusyRetry the delays after
	// a failed check and after "apt is busy".
	FirstCheck time.Duration
	Interval   time.Duration
	Tick       time.Duration
	Retry      time.Duration
	BusyRetry  time.Duration
	// HelperTimeout bounds the cheap job status request of Info.
	HelperTimeout time.Duration
	// CheckTimeout bounds one whole check.
	CheckTimeout time.Duration
	Now          func() time.Time

	mu       sync.Mutex
	base     context.Context
	loaded   bool
	checked  time.Time
	res      osResult
	checking bool
	errMsg   string
	recheck  bool
}

// NewOS returns an OSManager with the production timings.
func NewOS(h OSHelper, s Settings) *OSManager {
	return &OSManager{Helper: h, Store: s, FirstCheck: 5 * time.Minute, Interval: 24 * time.Hour, Tick: time.Minute,
		Retry: time.Hour, BusyRetry: 10 * time.Minute, HelperTimeout: 5 * time.Second, CheckTimeout: 8 * time.Minute, Now: time.Now}
}

func (m *OSManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *OSManager) logf(f string, a ...any) {
	if m.Logger != nil {
		m.Logger.Printf(f, a...)
	}
}

func (m *OSManager) changed() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

// loadLocked reads the persisted state once.
func (m *OSManager) loadLocked() {
	if m.loaded {
		return
	}
	m.loaded = true
	if m.Store == nil {
		return
	}
	if v, err := m.Store.GetSetting(keyOSChecked); err == nil {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			m.checked = t
		}
	}
	if v, err := m.Store.GetSetting(keyOSResult); err == nil {
		_ = json.Unmarshal([]byte(v), &m.res)
	}
	if v, err := m.Store.GetSetting(keyOSRecheck); err == nil && v == "1" {
		m.recheck = true
	}
}

func (m *OSManager) persistLocked() {
	if m.Store == nil {
		return
	}
	b, _ := json.Marshal(m.res)
	recheck := ""
	if m.recheck {
		recheck = "1"
	}
	kv := map[string]string{keyOSResult: string(b), keyOSRecheck: recheck}
	if !m.checked.IsZero() {
		kv[keyOSChecked] = m.checked.UTC().Format(time.RFC3339)
	}
	for k, v := range kv {
		if err := m.Store.SetSetting(k, v); err != nil {
			m.logf("updates: saving %s: %v", k, err)
		}
	}
}

func norm[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Info returns the cached check result plus the live job state and
// reboot-required marker (cheap helper request).
func (m *OSManager) Info(ctx context.Context) OSInfo {
	var st helper.OSStatus
	var herr error
	hctx, cancel := context.WithTimeout(ctx, m.HelperTimeout)
	st, herr = m.Helper.OSStatus(hctx)
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadLocked()
	in := OSInfo{Available: herr == nil, Checking: m.checking, Job: helper.JobIdle,
		Packages: norm(m.res.Packages), KeptBack: norm(m.res.KeptBack), RebootRequired: m.res.Reboot, RebootPkgs: norm(m.res.RebootPkgs), Warnings: norm(m.res.Warnings)}
	if herr != nil {
		in.Reason = ptr(reasonFor(herr))
	} else {
		in.Job, in.JobLog = st.Job, st.JobLog
		if st.RebootRequired != m.res.Reboot {
			m.res.Reboot, m.res.RebootPkgs = st.RebootRequired, st.RebootPkgs
			in.RebootRequired, in.RebootPkgs = st.RebootRequired, norm(st.RebootPkgs)
		}
	}
	if !m.checked.IsZero() {
		in.CheckedAt = ptr(m.checked.UTC().Format(time.RFC3339))
	}
	if m.errMsg != "" {
		in.Error = ptr(m.errMsg)
	}
	return in
}

// Counts returns the numbers for the header badge, from cached data only.
func (m *OSManager) Counts() OSCounts {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadLocked()
	c := OSCounts{Count: len(m.res.Packages), Reboot: m.res.Reboot}
	for _, p := range m.res.Packages {
		if p.Security {
			c.Security++
		}
	}
	return c
}

// StartCheck starts a check in the background. started is false when one is
// already running. helper.ErrBusy is returned while an upgrade runs.
func (m *OSManager) StartCheck(ctx context.Context) (started bool, err error) {
	hctx, cancel := context.WithTimeout(ctx, m.HelperTimeout)
	st, herr := m.Helper.OSStatus(hctx)
	cancel()
	if herr != nil {
		return false, herr
	}
	if st.Job == helper.JobRunning {
		return false, helper.ErrBusy
	}
	m.mu.Lock()
	m.loadLocked()
	if m.checking {
		m.mu.Unlock()
		return false, nil
	}
	m.checking = true
	base := m.base
	m.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	go m.runCheck(base)
	return true, nil
}

// CheckNow runs a check and waits for it (used by Run and tests).
func (m *OSManager) CheckNow(ctx context.Context) error {
	m.mu.Lock()
	m.loadLocked()
	if m.checking {
		m.mu.Unlock()
		return nil
	}
	m.checking = true
	m.mu.Unlock()
	return m.runCheck(ctx)
}

// runCheck performs the check (m.checking is already set) and records the
// outcome.
func (m *OSManager) runCheck(ctx context.Context) error {
	timeout := m.CheckTimeout
	if timeout == 0 {
		timeout = 8 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	st, err := m.Helper.OSCheck(cctx)
	cancel()
	m.mu.Lock()
	m.checking = false
	if err != nil && m.recheck {
		// Do not retry every tick; the scheduler's retry delay applies.
		m.recheck = false
		m.persistLocked()
	}
	switch {
	case err == nil:
		m.checked = m.now()
		m.res = osResult{Packages: norm(st.Packages), KeptBack: norm(st.KeptBack), Reboot: st.RebootRequired, RebootPkgs: norm(st.RebootPkgs), Warnings: st.Warnings}
		m.errMsg = ""
		m.recheck = false
		m.persistLocked()
	case errors.Is(err, helper.ErrAptBusy):
		m.errMsg = "apt is busy, try again later"
	case errors.Is(err, helper.ErrBusy):
		m.errMsg = "an update is running, try again later"
	default:
		m.errMsg = reasonFor(err)
	}
	m.mu.Unlock()
	if err != nil {
		m.logf("updates: OS check: %v", err)
	}
	m.changed()
	return err
}

// Apply starts the upgrade of all upgradable packages (a plain "apt-get
// upgrade" in a transient unit). A re-check follows when it has finished.
func (m *OSManager) Apply(ctx context.Context) (OSInfo, error) {
	if _, err := m.Helper.OSApply(ctx); err != nil {
		return OSInfo{}, err
	}
	m.mu.Lock()
	m.loadLocked()
	m.recheck = true
	m.persistLocked()
	base := m.base
	m.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	go m.watchJob(base)
	m.changed()
	return m.Info(ctx), nil
}

// Reboot asks the helper to reboot the machine.
func (m *OSManager) Reboot(ctx context.Context) error { return m.Helper.Reboot(ctx) }

// jobDone reports whether no OS upgrade is running (false when unknown).
func (m *OSManager) jobDone(ctx context.Context) bool {
	hctx, cancel := context.WithTimeout(ctx, m.HelperTimeout)
	defer cancel()
	st, err := m.Helper.OSStatus(hctx)
	return err == nil && st.Job != helper.JobRunning
}

// watchJob re-checks once the upgrade has finished. If the server restarts
// meanwhile (the nodespot package was upgraded), Run picks the persisted flag up.
func (m *OSManager) watchJob(ctx context.Context) {
	tick := m.Tick
	if tick <= 0 || tick > 5*time.Second {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	deadline := time.After(3 * time.Hour)
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-t.C:
		}
		if m.jobDone(ctx) {
			_ = m.CheckNow(ctx)
			return
		}
	}
}

// Run is the scheduler: the first check FirstCheck after start if the last
// successful one is older than Interval, then one per Interval; failed
// checks retry after Retry (apt busy: BusyRetry). A pending re-check after
// an upgrade runs as soon as no upgrade is running.
func (m *OSManager) Run(ctx context.Context) {
	m.mu.Lock()
	m.base = ctx
	m.loadLocked()
	start := m.now()
	next := m.checked.Add(m.Interval)
	if first := start.Add(m.FirstCheck); next.Before(first) {
		next = first
	}
	m.mu.Unlock()
	tick := m.Tick
	if tick <= 0 {
		tick = time.Minute
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.mu.Lock()
		due := !m.now().Before(next)
		recheck := m.recheck && !m.checking
		busy := m.checking
		m.mu.Unlock()
		if busy {
			continue
		}
		if recheck && !m.jobDone(ctx) {
			continue
		}
		if !due && !recheck {
			continue
		}
		err := m.CheckNow(ctx)
		switch {
		case err == nil:
			next = m.now().Add(m.Interval)
		case errors.Is(err, helper.ErrAptBusy) || errors.Is(err, helper.ErrBusy):
			next = m.now().Add(m.BusyRetry)
		default:
			next = m.now().Add(m.Retry)
		}
	}
}

// joinNames returns the names of up to max packages.
func joinNames(pkgs []helper.OSPackage, max int) []string {
	var out []string
	for _, p := range pkgs {
		if len(out) >= max {
			break
		}
		out = append(out, p.Name)
	}
	return out
}

// Names returns up to max package names of the cached result (for the audit
// log).
func (m *OSManager) Names(max int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return joinNames(m.res.Packages, max)
}
