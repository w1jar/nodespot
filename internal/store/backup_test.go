package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/schedules"
)

func TestSnapshotInspectMigrate(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateUser("alice", "hash", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFavorite("allstar", "1999", "Hub", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByName("alice")
	if err := st.CreateSession("tok", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(dir, "snap.db")
	if err := st.Snapshot(snap); err != nil {
		t.Fatal(err)
	}
	in, err := Inspect(snap)
	if err != nil {
		t.Fatal(err)
	}
	if in.Version != SchemaVersion() || len(in.Users) != 1 || in.Users[0].Username != "alice" || in.Favorites != 1 {
		t.Fatalf("inspection %+v", in)
	}
	// no sessions in the snapshot
	st2, err := Open(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st2.SessionByHash("tok", time.Now()); err == nil {
		t.Error("session survived the snapshot")
	}
	st2.Close()

	if _, err := Inspect(filepath.Join(dir, "missing.db")); err == nil {
		t.Error("missing file accepted")
	}
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte("not sqlite at all, just text padding padding padding padding"), 0o600)
	if _, err := Inspect(junk); err == nil {
		t.Error("junk accepted")
	}
	if err := MigrateFile(snap, &Audit{Username: "alice", Action: "backup.restore", Detail: "{}"}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateFileDropsScheduleState(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddSchedule(schedules.Schedule{Name: "net", Mode: "allstar", Target: "2001", Action: "connect", Days: []int{3}, Start: "20:00", End: "20:30"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{schedules.KeyState: `[{"id":1}]`, schedules.KeyLastCancel: `{"ids":[1]}`, schedules.KeyResumeAt: "2026-11-18T00:00:00-05:00", schedules.KeyEnabled: "true", schedules.KeyPausedReason: "manual"} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	snap := filepath.Join(dir, "snap.db")
	if err := st.Snapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := MigrateFile(snap, nil); err != nil {
		t.Fatal(err)
	}
	in, err := Inspect(snap)
	if err != nil || in.Schedules != 1 {
		t.Fatalf("inspect %+v %v", in, err)
	}
	st2, err := Open(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if v, _ := st2.GetSetting(schedules.KeyState); v != "" {
		t.Errorf("schedules.state survived: %q", v)
	}
	if v, _ := st2.GetSetting(schedules.KeyLastCancel); v != "" {
		t.Errorf("schedules.last_cancel survived: %q", v)
	}
	if v, _ := st2.GetSetting(schedules.KeyResumeAt); v == "" {
		t.Errorf("schedules.resume_at lost")
	}
	if v, _ := st2.GetSetting(schedules.KeyEnabled); v != "true" {
		t.Errorf("enabled switch lost: %q", v)
	}
	if v, _ := st2.GetSetting(schedules.KeyPausedReason); v != "manual" {
		t.Errorf("pause reason lost: %q", v)
	}
}

func TestInspectBeforeSchedules(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(dir, "old.db")
	if err := st.Snapshot(snap); err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE schedules; DELETE FROM schema_version WHERE version>3`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	in, err := Inspect(snap)
	if err != nil || in.Schedules != 0 || in.Version != 3 {
		t.Fatalf("inspect %+v %v", in, err)
	}
}
