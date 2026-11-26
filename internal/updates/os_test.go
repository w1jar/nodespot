package updates

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSettings) GetSetting(k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[k]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (s *memSettings) SetSetting(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[k] = v
	return nil
}

type osFake struct {
	mu     sync.Mutex
	checks int
	err    error
	job    string
	block  chan struct{}
}

func (f *osFake) OSCheck(context.Context) (helper.OSStatus, error) {
	f.mu.Lock()
	f.checks++
	err, blk := f.err, f.block
	f.mu.Unlock()
	if blk != nil {
		<-blk
	}
	if err != nil {
		return helper.OSStatus{}, err
	}
	return helper.OSStatus{Packages: []helper.OSPackage{{Name: "a", From: "1", To: "2"}, {Name: "b", From: "1", To: "2", Security: true}}}, nil
}
func (f *osFake) OSStatus(context.Context) (helper.OSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return helper.OSStatus{Job: f.job}, nil
}
func (f *osFake) OSApply(context.Context) (helper.OSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job == helper.JobRunning {
		return helper.OSStatus{}, helper.ErrBusy
	}
	f.job = helper.JobRunning
	return helper.OSStatus{Job: f.job}, nil
}
func (f *osFake) Reboot(context.Context) error { return nil }
func (f *osFake) count() int                   { f.mu.Lock(); defer f.mu.Unlock(); return f.checks }

func fastOS(h OSHelper, s Settings) *OSManager {
	m := NewOS(h, s)
	m.FirstCheck, m.Tick, m.Retry, m.BusyRetry, m.HelperTimeout = 10*time.Millisecond, 5*time.Millisecond, time.Hour, time.Hour, time.Second
	return m
}

func runFor(m *OSManager, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	m.Run(ctx)
}

func TestOSSchedulerChecksOnceAndPersists(t *testing.T) {
	f, s := &osFake{}, &memSettings{}
	m := fastOS(f, s)
	runFor(m, 200*time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("checks: %d (once per interval)", f.count())
	}
	c := m.Counts()
	if c.Count != 2 || c.Security != 1 {
		t.Fatalf("%+v", c)
	}
	// a restart within 24 h does not check again and still knows the result
	m2 := fastOS(f, s)
	runFor(m2, 200*time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("re-checked after a restart: %d", f.count())
	}
	if c := m2.Counts(); c.Count != 2 || c.Security != 1 {
		t.Fatalf("restored: %+v", c)
	}
	in := m2.Info(context.Background())
	if in.CheckedAt == nil || len(in.Packages) != 2 {
		t.Fatalf("%+v", in)
	}
}

func TestOSSchedulerStaleCheckRuns(t *testing.T) {
	f, s := &osFake{}, &memSettings{}
	_ = s.SetSetting(keyOSChecked, time.Now().Add(-25*time.Hour).UTC().Format(time.RFC3339))
	m := fastOS(f, s)
	runFor(m, 200*time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("checks: %d", f.count())
	}
	// "daily": a fake clock a day later makes the next one due
	var skew atomic.Int64
	f2, s2 := &osFake{}, &memSettings{}
	m2 := fastOS(f2, s2)
	m2.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	go func() { time.Sleep(100 * time.Millisecond); skew.Store(int64(25 * time.Hour)) }()
	runFor(m2, 300*time.Millisecond)
	if f2.count() < 2 {
		t.Fatalf("no daily re-check: %d", f2.count())
	}
}

func TestOSSchedulerFailureRetriesLater(t *testing.T) {
	f := &osFake{err: errors.New("boom")}
	s := &memSettings{}
	m := fastOS(f, s)
	runFor(m, 200*time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("a failed check was retried at once: %d", f.count())
	}
	in := m.Info(context.Background())
	if in.Error == nil || in.CheckedAt != nil {
		t.Fatalf("%+v", in)
	}
	if _, err := s.GetSetting(keyOSChecked); err == nil {
		t.Fatal("a failed check must not be recorded as done")
	}
}

func TestOSManualCheckAndConcurrency(t *testing.T) {
	f := &osFake{block: make(chan struct{})}
	m := fastOS(f, &memSettings{})
	ctx := context.Background()
	started, err := m.StartCheck(ctx)
	if err != nil || !started {
		t.Fatalf("%v %v", started, err)
	}
	if !m.Info(ctx).Checking {
		t.Fatal("not checking")
	}
	if started, err = m.StartCheck(ctx); err != nil || started {
		t.Fatalf("second check: %v %v", started, err)
	}
	close(f.block)
	deadline := time.Now().Add(2 * time.Second)
	for m.Info(ctx).Checking && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.count() != 1 || m.Counts().Count != 2 {
		t.Fatalf("%d %+v", f.count(), m.Counts())
	}
	// not while an upgrade runs
	f.mu.Lock()
	f.job = helper.JobRunning
	f.mu.Unlock()
	if _, err := m.StartCheck(ctx); !errors.Is(err, helper.ErrBusy) {
		t.Fatalf("%v", err)
	}
	if _, err := m.Apply(ctx); !errors.Is(err, helper.ErrBusy) {
		t.Fatalf("%v", err)
	}
}

func TestOSApplyRechecksAfterJob(t *testing.T) {
	f, s := &osFake{}, &memSettings{}
	m := fastOS(f, s)
	if _, err := m.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting(keyOSRecheck); v != "1" {
		t.Fatal("recheck flag not persisted")
	}
	time.Sleep(50 * time.Millisecond)
	if f.count() != 0 {
		t.Fatal("checked while the job runs")
	}
	f.mu.Lock()
	f.job = helper.JobSucceeded
	f.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for f.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.count() != 1 {
		t.Fatalf("checks: %d", f.count())
	}
	for i := 0; i < 400; i++ {
		if v, _ := s.GetSetting(keyOSRecheck); v == "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("flag not cleared")
}

func TestOSPendingRecheckSurvivesRestart(t *testing.T) {
	f, s := &osFake{}, &memSettings{}
	_ = s.SetSetting(keyOSChecked, time.Now().UTC().Format(time.RFC3339))
	_ = s.SetSetting(keyOSRecheck, "1")
	runFor(fastOS(f, s), 200*time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("checks: %d", f.count())
	}
}
