package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/legacy"
	"github.com/w1jar/nodespot/internal/schedules"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Version()
	if v != len(migrations) {
		t.Fatalf("version %d", v)
	}
	s.CreateUser("a", "h", RoleAdmin)
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, _ := s.CountUsers(); n != 1 {
		t.Fatal("data lost")
	}
	if v2, _ := s.Version(); v2 != v {
		t.Fatal("version changed")
	}
}

func TestUsers(t *testing.T) {
	s := open(t)
	u, err := s.CreateUser("Alice", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("alice", "x", RoleViewer); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists got %v", err)
	}
	got, err := s.UserByName("ALICE")
	if err != nil || got.ID != u.ID || got.PWHash != "hash" {
		t.Fatal(got, err)
	}
	r, h := RoleOperator, "h2"
	if err := s.UpdateUser(u.ID, &r, &h); err != nil {
		t.Fatal(err)
	}
	got, _ = s.UserByID(u.ID)
	if got.Role != RoleOperator || got.PWHash != "h2" {
		t.Fatal(got)
	}
	if n, _ := s.CountAdmins(); n != 0 {
		t.Fatal("admins", n)
	}
	l, _ := s.ListUsers()
	if len(l) != 1 || l[0].PWHash != "" {
		t.Fatal(l)
	}
	if _, err := s.UserByName("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.UpdateUser(999, &r, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestSessions(t *testing.T) {
	s := open(t)
	u, _ := s.CreateUser("bob", "h", RoleViewer)
	now := time.Now()
	if err := s.CreateSession("th", u.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	se, uu, err := s.SessionByHash("th", now)
	if err != nil || uu.Username != "bob" || se.UserID != u.ID {
		t.Fatal(err, uu)
	}
	if _, _, err := s.SessionByHash("th", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session valid")
	}
	if err := s.TouchSession("th", now, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionByHash("th", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.CreateSession("old", u.ID, now.Add(-time.Hour))
	if n, _ := s.DeleteExpiredSessions(now); n != 1 {
		t.Fatal(n)
	}
	s.CreateSession("other", u.ID, now.Add(time.Hour))
	s.DeleteUserSessions(u.ID, "th")
	if _, _, err := s.SessionByHash("other", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("other survived")
	}
	if _, _, err := s.SessionByHash("th", now); err != nil {
		t.Fatal("kept session removed")
	}
	// cascade
	s.DeleteUser(u.ID)
	if _, _, err := s.SessionByHash("th", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("cascade failed")
	}
}

func TestFavorites(t *testing.T) {
	s := open(t)
	a, _ := s.AddFavorite("allstar", "1997", "A", "")
	b, _ := s.AddFavorite("allstar", "1998", "B", "Hubs")
	c, _ := s.AddFavorite("allstar", "1999", "C", "Hubs")
	y, err := s.AddFavorite("ysf", "1997", "Room", "")
	if err != nil || y.Position != 0 {
		t.Fatalf("same target, other mode: %+v %v", y, err)
	}
	if a.Position != 0 || b.Position != 1 || c.Position != 2 {
		t.Fatal(a, b, c)
	}
	if _, err := s.AddFavorite("allstar", "1997", "again", ""); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	l, _ := s.ListFavorites("allstar")
	if len(l) != 3 || l[0].ID != a.ID || l[1].ID != b.ID {
		t.Fatal(l)
	}
	if all, _ := s.ListFavorites(""); len(all) != 4 {
		t.Fatal(all)
	}
	if err := s.ReorderFavorites("allstar", []int64{c.ID, b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	l, _ = s.ListFavorites("allstar")
	if l[0].ID != c.ID || l[1].ID != b.ID || l[2].ID != a.ID {
		t.Fatal(l)
	}
	bad := map[string][]int64{
		"short":      {c.ID, b.ID},
		"other mode": {c.ID, b.ID, y.ID},
		"repeated":   {c.ID, c.ID, b.ID},
		"unknown":    {c.ID, b.ID, 12345},
	}
	for name, ids := range bad {
		if err := s.ReorderFavorites("allstar", ids); !errors.Is(err, ErrMismatch) {
			t.Errorf("%s: %v", name, err)
		}
	}
	lbl := "Renamed"
	f, err := s.UpdateFavorite(a.ID, nil, &lbl, nil, nil)
	if err != nil || f.Label != "Renamed" || f.Target != "1997" || f.Mode != "allstar" {
		t.Fatal(f, err)
	}
	tgt := "1998"
	if _, err := s.UpdateFavorite(a.ID, &tgt, nil, nil, nil); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("update to a saved target: %v", err)
	}
	if _, err := s.UpdateFavorite(999, nil, &lbl, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := s.DeleteFavorite(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFavorite(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetFavorite(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for i := 0; i < MaxFavoritesPerMode; i++ {
		if _, err := s.AddFavorite("nxdn", strconv.Itoa(i+1), "x", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddFavorite("nxdn", "90000", "x", ""); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	if _, err := s.GetSetting("k"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	s.SetSetting("k", "1")
	s.SetSetting("k", "2")
	if v, _ := s.GetSetting("k"); v != "2" {
		t.Fatal(v)
	}
}

func TestAudit(t *testing.T) {
	s := open(t)
	for _, a := range []string{"config.apply", "config.read"} {
		if err := s.AddAudit(Audit{Username: "admin", Action: a, Detail: `{"path":"rpt.conf"}`}); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.ListAudit(10)
	if err != nil || len(l) != 2 || l[0].Action != "config.read" || l[0].Username != "admin" || l[0].Time.IsZero() {
		t.Fatalf("%+v %v", l, err)
	}
	if l, _ := s.ListAudit(1); len(l) != 1 {
		t.Fatal("limit")
	}
}

func TestAuditPage(t *testing.T) {
	s := open(t)
	for _, e := range [][2]string{{"admin", "system.hostname"}, {"op", "radio.save"}, {"admin", "system.reboot"}, {"admin", "config.apply"}, {"admin", "sys_tem%"}} {
		if err := s.AddAudit(Audit{Username: e[0], Action: e[1]}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.ListAuditPage(AuditFilter{})
	if len(all) != 5 || all[0].Action != "sys_tem%" {
		t.Fatalf("%+v", all)
	}
	// paging by id: no overlap, no gap, newest first
	p1, _ := s.ListAuditPage(AuditFilter{Limit: 2})
	p2, _ := s.ListAuditPage(AuditFilter{Limit: 2, BeforeID: p1[1].ID})
	p3, _ := s.ListAuditPage(AuditFilter{Limit: 2, BeforeID: p2[1].ID})
	if len(p1) != 2 || len(p2) != 2 || len(p3) != 1 || p1[1].ID <= p2[0].ID || p2[1].ID <= p3[0].ID {
		t.Fatalf("%+v %+v %+v", p1, p2, p3)
	}
	// a new entry does not shift the next page
	_ = s.AddAudit(Audit{Username: "admin", Action: "new"})
	if q, _ := s.ListAuditPage(AuditFilter{Limit: 2, BeforeID: p1[1].ID}); q[0].ID != p2[0].ID {
		t.Fatal("page shifted")
	}
	if l, _ := s.ListAuditPage(AuditFilter{User: "op"}); len(l) != 1 || l[0].Action != "radio.save" {
		t.Fatalf("%+v", l)
	}
	if l, _ := s.ListAuditPage(AuditFilter{ActionPrefix: "system."}); len(l) != 2 {
		t.Fatalf("%+v", l)
	}
	// wildcards in the prefix are literal
	if l, _ := s.ListAuditPage(AuditFilter{ActionPrefix: "sys_"}); len(l) != 1 || l[0].Action != "sys_tem%" {
		t.Fatalf("%+v", l)
	}
	if l, _ := s.ListAuditPage(AuditFilter{ActionPrefix: "%"}); len(l) != 0 {
		t.Fatalf("%+v", l)
	}
}

func TestSchedules(t *testing.T) {
	s := open(t)
	if v, _ := s.Version(); v < 4 {
		t.Fatalf("schema version %d", v)
	}
	l, err := s.ListSchedules()
	if err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}
	a, err := s.AddSchedule(schedules.Schedule{Name: "Net", Enabled: true, Mode: "allstar", Target: "1999", Action: "connect",
		Days: []int{2, 4}, Start: "19:00", End: "20:30"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Name != "Net" || !a.Enabled || a.Mode != "allstar" || a.Target != "1999" ||
		len(a.Days) != 2 || a.Days[0] != 2 || a.Days[1] != 4 || a.Start != "19:00" || a.End != "20:30" || a.LastRun != nil {
		t.Fatalf("added: %+v", a)
	}
	b, err := s.AddSchedule(schedules.Schedule{Mode: "ysf", Target: "", Action: "disconnect", Days: []int{}, Date: "2026-12-01", Start: "07:00"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Date != "2026-12-01" || len(b.Days) != 0 || b.Days == nil || b.Enabled {
		t.Fatalf("once: %+v", b)
	}
	a.Name, a.Days, a.End = "Net 2", []int{0}, ""
	u, err := s.UpdateSchedule(a)
	if err != nil || u.Name != "Net 2" || len(u.Days) != 1 || u.End != "" {
		t.Fatalf("update: %+v %v", u, err)
	}
	if _, err := s.UpdateSchedule(schedules.Schedule{ID: 999, Mode: "allstar", Target: "2", Action: "connect", Start: "00:00"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	at := time.Unix(1_800_000_000, 0)
	if err := s.SetScheduleResult(a.ID, at, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetScheduleEnabled(b.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetScheduleEnabled(999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable missing: %v", err)
	}
	g, _ := s.GetSchedule(a.ID)
	if g.LastRun == nil || !g.LastRun.Equal(at) || g.LastResult != "ok" {
		t.Fatalf("result: %+v", g)
	}
	if g, _ := s.GetSchedule(b.ID); !g.Enabled {
		t.Fatal("enable did not stick")
	}
	if err := s.DeleteSchedule(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := s.GetSchedule(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if l, _ := s.ListSchedules(); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
}

func TestFavoritePins(t *testing.T) {
	st := open(t)
	a, _ := st.AddFavorite("allstar", "1997", "A", "")
	if a.Pinned {
		t.Fatal("new favorites are not pinned")
	}
	yes := true
	if f, err := st.UpdateFavorite(a.ID, nil, nil, nil, &yes); err != nil || !f.Pinned {
		t.Fatalf("%+v %v", f, err)
	}
	lbl := "B"
	if f, _ := st.UpdateFavorite(a.ID, nil, &lbl, nil, nil); !f.Pinned {
		t.Fatal("an edit without pinned keeps the pin")
	}
	if fl, _ := st.ListFavorites(""); !fl[0].Pinned {
		t.Fatalf("list: %+v", fl)
	}
}

// openAt opens a new database at path migrated to version n only.
func openAt(t *testing.T, path string, n int) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	s = &Store{db: db}
	if err := s.migrateTo(n); err != nil {
		t.Fatal(err)
	}
	return s
}

// A 2.x database (schema 7) has AllStar favorites, digital modes favorites
// and node-based schedules; opening it merges them into the per-mode
// tables.
func TestMigrateNative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")
	s := openAt(t, path, 7)
	_, err := s.db.Exec(`INSERT INTO favorites(node,label,grp,position,pinned) VALUES
			('1999','Hub','Hubs',0,1), ('3001997','Link','',1,0), ('2000','Two','',2,0);
		INSERT INTO dv_favorites(mode,value,name,sort,created,pinned) VALUES
			('BM','91','World',0,1,1), ('YSF','12345','Room',0,1,0), ('LOOP','1','Gone',0,1,0);
		INSERT INTO schedules(name,enabled,node,target,action,mode,permanent,exclusive,days,date,start,end_time,created,updated) VALUES
			('Net',1,'1998','2000','connect','transceive',1,1,'2,4','','19:00','20:00',1,1),
			('EL',0,'1998','3001997','connect','monitor',0,0,'','2026-12-01','07:00','',1,1);
		INSERT INTO activity(node,link,start,duration_ms) VALUES ('1998','2000',1,1000);`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	favs, err := s.ListFavorites("")
	if err != nil {
		t.Fatal(err)
	}
	type fav struct {
		mode, target, label string
		pinned              bool
	}
	want := []fav{
		{"allstar", "1999", "Hub", true}, {"allstar", "2000", "Two", false},
		{"bm", "91", "World", true}, {"echolink", "1997", "Link", false}, {"ysf", "12345", "Room", false},
	}
	if len(favs) != len(want) {
		t.Fatalf("favorites %+v", favs)
	}
	for i, w := range want {
		f := favs[i]
		if (fav{f.Mode, f.Target, f.Label, f.Pinned}) != w {
			t.Errorf("favorite %d: %+v, want %+v", i, f, w)
		}
	}
	sch, err := s.ListSchedules()
	if err != nil || len(sch) != 2 {
		t.Fatalf("schedules %+v %v", sch, err)
	}
	for _, x := range sch {
		switch x.Name {
		case "Net":
			if x.Mode != "allstar" || x.Target != "2000" || !x.Enabled || len(x.Days) != 2 || x.End != "20:00" {
				t.Errorf("net: %+v", x)
			}
		case "EL":
			if x.Mode != "echolink" || x.Target != "1997" || x.Date != "2026-12-01" {
				t.Errorf("echolink: %+v", x)
			}
		default:
			t.Errorf("unexpected %+v", x)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('activity','dv_favorites')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("old tables left: %d %v", n, err)
	}
	if _, err := s.AddFavorite("allstar", "1999", "x", ""); !errors.Is(err, ErrDuplicate) {
		t.Errorf("the unique index is missing: %v", err)
	}
}

// A 1.x database holds the digital modes favorites table and the settings
// keys under the previous stack's names; opening it moves them.
func TestMigrateOldDVNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s := openAt(t, path, 5)
	_, err := s.db.Exec(`ALTER TABLE dv_favorites RENAME TO ` + legacy.OldFavoritesTable + `;
		INSERT INTO settings(k,v) VALUES ('` + legacy.OldSettingsPrefix + `node','1999'), ('` + legacy.OldSettingsPrefix + `resume','{"a":1}'), ('dv.resume','new'), ('other','x');
		INSERT INTO ` + legacy.OldFavoritesTable + `(mode,value,name,sort,created) VALUES ('YSF','00001','Test',0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.GetSetting("dv.node"); err != nil || v != "1999" {
		t.Errorf("dv.node = %q, %v", v, err)
	}
	if v, _ := s.GetSetting("dv.resume"); v != "new" {
		t.Errorf("existing dv.resume overwritten: %q", v)
	}
	if v, _ := s.GetSetting("other"); v != "x" {
		t.Errorf("other = %q", v)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE k LIKE ?`, legacy.OldSettingsPrefix+"%").Scan(&n); err != nil || n != 0 {
		t.Errorf("old keys left: %d %v", n, err)
	}
	favs, err := s.ListFavorites("ysf")
	if err != nil || len(favs) != 1 || favs[0].Label != "Test" || favs[0].Target != "00001" {
		t.Errorf("favorites %+v %v", favs, err)
	}
}
