package backupsvc

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

func newSvc(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	ops, err := helper.NewDemoOps(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, "nodespot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.CreateUser("alice", "hash", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Helper: helper.LocalBackup{Ops: ops}, Store: st, DataDir: data, Version: "1.0.0",
		Logger: log.New(io.Discard, "", 0), Hostname: func() string { return "node-a" }, Timezone: func() string { return "UTC" }})
	return s, st, data
}

func TestStagedRestoreAndSwap(t *testing.T) {
	s, st, data := newSvc(t)
	c, err := s.Create(context.Background(), helper.BackupRequest{Secrets: true}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	up, err := s.Upload(context.Background(), c.File)
	if err != nil {
		t.Fatal(err)
	}
	pv := up.Preview
	if pv.Backup.Hostname != "node-a" || len(pv.Users) != 1 || !pv.Parts.Node || !pv.Parts.Secrets || !pv.Parts.SA818 ||
		pv.Radio == nil || len(pv.Modes) == 0 || len(pv.MissingSecrets) != 0 {
		t.Fatalf("preview %+v", pv)
	}
	if _, err := s.Apply(context.Background(), "nope", "alice"); err != ErrNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	res, err := s.Apply(context.Background(), up.ID, "alice")
	if err != nil || !res.OK || !res.Restarting {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if _, err := os.Stat(StagedPath(data)); err != nil {
		t.Fatalf("not staged: %v", err)
	}
	r, ok := s.LastResult()
	if !ok || !r.Health.Pending {
		t.Fatalf("result %+v %v", r, ok)
	}

	// the next start swaps the staged database in
	st.Close()
	swapped, err := ApplyStaged(data)
	if err != nil || !swapped {
		t.Fatalf("ApplyStaged: %v %v", swapped, err)
	}
	for _, p := range []string{"nodespot.db", "nodespot.db.pre-restore"} {
		if _, err := os.Stat(filepath.Join(data, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := os.Stat(StagedPath(data)); err == nil {
		t.Error("staged file remains")
	}
	st2, err := store.Open(filepath.Join(data, "nodespot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	aud, _ := st2.ListAudit(10)
	found := false
	for _, a := range aud {
		found = found || a.Action == "backup.restore"
	}
	if !found {
		t.Errorf("restore audit entry missing: %v", aud)
	}

	// health check completes the pending result
	s2 := New(s.o)
	s2.RunHealth(context.Background(), func(context.Context) []Check {
		return []Check{{Name: "x", OK: true, Detail: "fine"}}
	})
	r, _ = s2.LastResult()
	if r.Health.Pending || !r.Health.OK || len(r.Health.Checks) != 1 {
		t.Fatalf("health %+v", r.Health)
	}
	_ = time.Second
}

func TestApplyStagedRejectsJunk(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "nodespot.db")
	os.WriteFile(live, []byte("live"), 0o600)
	os.WriteFile(StagedPath(dir), []byte("junk junk junk junk junk junk junk junk junk junk"), 0o600)
	swapped, err := ApplyStaged(dir)
	if swapped || err == nil {
		t.Fatalf("junk staged db was accepted: %v %v", swapped, err)
	}
	if b, _ := os.ReadFile(live); string(b) != "live" {
		t.Error("live database touched")
	}
	if swapped, err := ApplyStaged(t.TempDir()); swapped || err != nil {
		t.Errorf("nothing staged: %v %v", swapped, err)
	}
}
