// Package store is the SQLite persistence layer (users, sessions,
// favorites, settings).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/legacy"
	"github.com/w1jar/nodespot/internal/schedules"
	_ "modernc.org/sqlite" // pure-Go driver
)

// Errors returned by the store.
var (
	ErrNotFound = errors.New("store: not found")
	ErrExists   = errors.New("store: already exists")
)

// Roles.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { return r == RoleAdmin || r == RoleOperator || r == RoleViewer }

// Store wraps the database.
type Store struct{ db *sql.DB }

// User is an account. PWHash is only filled by lookups that need it.
type User struct {
	ID       int64
	Username string
	Role     string
	PWHash   string
	Created  time.Time
}

// Session is a login session; Token is never stored, only its hash.
type Session struct {
	TokenHash string
	UserID    int64
	Expires   time.Time
	Created   time.Time
	LastSeen  time.Time
}

var migrations = []string{
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		pw_hash TEXT NOT NULL,
		role TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
		created INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires INTEGER NOT NULL,
		created INTEGER NOT NULL,
		last_seen INTEGER NOT NULL
	);
	CREATE INDEX sessions_user ON sessions(user_id);
	CREATE TABLE favorites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		grp TEXT NOT NULL DEFAULT '',
		position INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);`,
	// 2: recent activity (link and local-receiver keyups).
	`CREATE TABLE activity (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node TEXT NOT NULL,
		link TEXT NOT NULL,
		start INTEGER NOT NULL,
		duration_ms INTEGER NOT NULL,
		kerchunk INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX activity_start ON activity(start);
	CREATE INDEX activity_node ON activity(node, start);`,
	// 3: audit log of privileged actions (never holds secret values).
	`CREATE TABLE audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts INTEGER NOT NULL,
		username TEXT NOT NULL,
		action TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX audit_ts ON audit(ts);`,
	// 4: scheduled connects and disconnects (docs/SCHEDULES.md).
	`CREATE TABLE schedules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		node TEXT NOT NULL,
		target TEXT NOT NULL,
		action TEXT NOT NULL,
		mode TEXT NOT NULL DEFAULT '',
		permanent INTEGER NOT NULL DEFAULT 0,
		exclusive INTEGER NOT NULL DEFAULT 0,
		days TEXT NOT NULL DEFAULT '',
		date TEXT NOT NULL DEFAULT '',
		start TEXT NOT NULL,
		end_time TEXT NOT NULL DEFAULT '',
		last_run INTEGER NOT NULL DEFAULT 0,
		last_result TEXT NOT NULL DEFAULT '',
		created INTEGER NOT NULL,
		updated INTEGER NOT NULL
	);`,
	// 5: digital modes favourites, shared node-wide (docs/DV.md). Databases
	// of 1.x installs hold the table under its old name; migrate() renames it.
	`CREATE TABLE dv_favorites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mode TEXT NOT NULL,
		value TEXT NOT NULL,
		name TEXT NOT NULL,
		sort INTEGER NOT NULL DEFAULT 0,
		created INTEGER NOT NULL,
		UNIQUE(mode, value)
	);`,
	// 6: settings keys of 1.x installs carry the previous stack's prefix.
	migrationOldKeys(),
	// 7: pinned favourites (shown on the overview), shared node-wide like the favourites.
	`ALTER TABLE favorites ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE dv_favorites ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;`,
	// 8: the native node (3.0). One favorites list per mode replaces the
	// AllStar favorites and the digital modes favorites; schedules name a
	// mode instead of a local node; the activity table of the AllStar
	// monitor goes away.
	migrationNative,
}

// migrationNative is migration 8. An AllStar favorite of a 7-digit number
// starting with 3 is an EchoLink node reached through AllStar: it becomes
// an EchoLink favorite of the number after the 3 (leading zeros dropped). Digital modes
// favorites keep their mode in lower case (BM becomes bm); modes the node
// does not have are dropped.
const migrationNative = `CREATE TABLE favorites8 (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mode TEXT NOT NULL,
		target TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		grp TEXT NOT NULL DEFAULT '',
		position INTEGER NOT NULL DEFAULT 0,
		pinned INTEGER NOT NULL DEFAULT 0,
		UNIQUE(mode, target)
	);
	INSERT OR IGNORE INTO favorites8(mode,target,label,grp,position,pinned)
		SELECT CASE WHEN length(node)=7 AND substr(node,1,1)='3' THEN 'echolink' ELSE 'allstar' END,
			CASE WHEN length(node)=7 AND substr(node,1,1)='3' THEN CAST(CAST(substr(node,2) AS INTEGER) AS TEXT) ELSE node END,
			label, grp, position, pinned FROM favorites ORDER BY position, id;
	INSERT OR IGNORE INTO favorites8(mode,target,label,grp,position,pinned)
		SELECT lower(mode), value, name, '', sort, pinned FROM dv_favorites
		WHERE lower(mode) IN ('bm','tgif','dmr','ysf','p25','nxdn','m17') ORDER BY mode, sort, id;
	DROP TABLE favorites;
	DROP TABLE dv_favorites;
	ALTER TABLE favorites8 RENAME TO favorites;
	CREATE TABLE schedules8 (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		mode TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		days TEXT NOT NULL DEFAULT '',
		date TEXT NOT NULL DEFAULT '',
		start TEXT NOT NULL,
		end_time TEXT NOT NULL DEFAULT '',
		last_run INTEGER NOT NULL DEFAULT 0,
		last_result TEXT NOT NULL DEFAULT '',
		created INTEGER NOT NULL,
		updated INTEGER NOT NULL
	);
	INSERT INTO schedules8(id,name,enabled,mode,target,action,days,date,start,end_time,last_run,last_result,created,updated)
		SELECT id, name, enabled,
			CASE WHEN length(target)=7 AND substr(target,1,1)='3' THEN 'echolink' ELSE 'allstar' END,
			CASE WHEN length(target)=7 AND substr(target,1,1)='3' THEN CAST(CAST(substr(target,2) AS INTEGER) AS TEXT) ELSE target END,
			action, days, date, start, end_time, last_run, last_result, created, updated FROM schedules;
	DROP TABLE schedules;
	ALTER TABLE schedules8 RENAME TO schedules;
	DROP TABLE activity;`

// migrationOldKeys is migration 6: it moves the settings keys of a 1.x
// database to the dv. prefix (an existing dv. key wins).
func migrationOldKeys() string {
	n := len(legacy.OldSettingsPrefix)
	return fmt.Sprintf(`UPDATE OR IGNORE settings SET k='dv.'||substr(k,%d) WHERE substr(k,1,%d)='%s';
	DELETE FROM settings WHERE substr(k,1,%d)='%s';`, n+1, n, legacy.OldSettingsPrefix, n, legacy.OldSettingsPrefix)
}

// renameOldFavorites renames the favourites table of a 1.x database (SQLite
// has no conditional ALTER, so it runs after the numbered migrations).
func (s *Store) renameOldFavorites() error {
	var old, cur int
	q := `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`
	if err := s.db.QueryRow(q, legacy.OldFavoritesTable).Scan(&old); err != nil || old == 0 {
		return err
	}
	if err := s.db.QueryRow(q, "dv_favorites").Scan(&cur); err != nil || cur != 0 {
		return err
	}
	_, err := s.db.Exec(`ALTER TABLE ` + legacy.OldFavoritesTable + ` RENAME TO dv_favorites`)
	return err
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialises access; the workload is tiny
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Version returns the current schema version.
func (s *Store) Version() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&v)
	return v, err
}

func (s *Store) migrate() error { return s.migrateTo(len(migrations)) }

// migrateTo runs the migrations up to version n (tests build old databases
// with it).
func (s *Store) migrateTo(n int) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	cur, err := s.Version()
	if err != nil {
		return err
	}
	if cur >= 5 && cur < 8 { // a 1.x database holds the DV favourites under the old name; migrations 7 and 8 need the new one
		if err := s.renameOldFavorites(); err != nil {
			return err
		}
	}
	if cur > len(migrations) {
		return fmt.Errorf("store: database schema %d is newer than this build (%d)", cur, len(migrations))
	}
	for i := cur; i < n; i++ {
		if i == 7 { // migration 8 merges dv_favorites: it must have its current name
			if err := s.renameOldFavorites(); err != nil {
				return err
			}
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) int64     { return t.Unix() }
func fromTS(n int64) time.Time { return time.Unix(n, 0).UTC() }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Users -----------------------------------------------------------------

// CreateUser inserts a user.
func (s *Store) CreateUser(username, pwHash, role string) (User, error) {
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO users(username,pw_hash,role,created) VALUES (?,?,?,?)`, username, pwHash, role, ts(now))
	if err != nil {
		if isUnique(err) {
			return User{}, ErrExists
		}
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: role, Created: fromTS(ts(now))}, nil
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var c int64
	err := row.Scan(&u.ID, &u.Username, &u.PWHash, &u.Role, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	u.Created = fromTS(c)
	return u, err
}

const userCols = `id,username,pw_hash,role,created`

// UserByName looks a user up case-insensitively (includes PWHash).
func (s *Store) UserByName(name string) (User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, name))
}

// UserByID looks a user up by id (includes PWHash).
func (s *Store) UserByID(id int64) (User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ListUsers returns all users by id.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		u.PWHash = ""
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of users.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins returns the number of admin users.
func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&n)
	return n, err
}

// UpdateUser changes the role and/or password hash (nil leaves unchanged).
func (s *Store) UpdateUser(id int64, role, pwHash *string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if role != nil {
		res, err := tx.Exec(`UPDATE users SET role=? WHERE id=?`, *role, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	if pwHash != nil {
		res, err := tx.Exec(`UPDATE users SET pw_hash=? WHERE id=?`, *pwHash, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// DeleteUser removes a user (sessions cascade).
func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Sessions --------------------------------------------------------------

// CreateSession stores a session by token hash.
func (s *Store) CreateSession(tokenHash string, userID int64, expires time.Time) error {
	now := ts(time.Now())
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,expires,created,last_seen) VALUES (?,?,?,?,?)`,
		tokenHash, userID, ts(expires), now, now)
	return err
}

// SessionByHash returns the session and its user (no PWHash). Expired
// sessions are reported as ErrNotFound.
func (s *Store) SessionByHash(tokenHash string, now time.Time) (Session, User, error) {
	var se Session
	var u User
	var exp, cr, ls, uc int64
	err := s.db.QueryRow(`SELECT s.token_hash,s.user_id,s.expires,s.created,s.last_seen,u.id,u.username,u.role,u.created
		FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires>?`, tokenHash, ts(now)).
		Scan(&se.TokenHash, &se.UserID, &exp, &cr, &ls, &u.ID, &u.Username, &u.Role, &uc)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, err
	}
	se.Expires, se.Created, se.LastSeen, u.Created = fromTS(exp), fromTS(cr), fromTS(ls), fromTS(uc)
	return se, u, nil
}

// TouchSession extends a session (sliding expiry).
func (s *Store) TouchSession(tokenHash string, now, expires time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_seen=?, expires=? WHERE token_hash=?`, ts(now), ts(expires), tokenHash)
	return err
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// DeleteUserSessions removes all of a user's sessions, except keep (if non-empty).
func (s *Store) DeleteUserSessions(userID int64, keep string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=? AND token_hash<>?`, userID, keep)
	return err
}

// DeleteExpiredSessions purges expired sessions.
func (s *Store) DeleteExpiredSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires<=?`, ts(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Schedules -------------------------------------------------------------

const scheduleCols = `id,name,enabled,mode,target,action,days,date,start,end_time,last_run,last_result`

func daysString(d []int) string {
	parts := make([]string, len(d))
	for i, v := range d {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func parseDays(s string) []int {
	out := []int{}
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, v)
		}
	}
	return out
}

type rowScanner interface{ Scan(dest ...any) error }

func scanSchedule(r rowScanner) (schedules.Schedule, error) {
	var x schedules.Schedule
	var en int
	var days string
	var last int64
	err := r.Scan(&x.ID, &x.Name, &en, &x.Mode, &x.Target, &x.Action,
		&days, &x.Date, &x.Start, &x.End, &last, &x.LastResult)
	if err != nil {
		return x, err
	}
	x.Enabled = en != 0
	x.Days = parseDays(days)
	if last > 0 {
		t := fromTS(last)
		x.LastRun = &t
	}
	return x, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListSchedules returns all schedules ordered by id.
func (s *Store) ListSchedules() ([]schedules.Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + scheduleCols + ` FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedules.Schedule{}
	for rows.Next() {
		x, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// GetSchedule returns one schedule.
func (s *Store) GetSchedule(id int64) (schedules.Schedule, error) {
	x, err := scanSchedule(s.db.QueryRow(`SELECT `+scheduleCols+` FROM schedules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return schedules.Schedule{}, ErrNotFound
	}
	return x, err
}

// AddSchedule inserts a schedule (ID, LastRun and LastResult are ignored).
func (s *Store) AddSchedule(x schedules.Schedule) (schedules.Schedule, error) {
	now := ts(time.Now())
	res, err := s.db.Exec(`INSERT INTO schedules(name,enabled,mode,target,action,days,date,start,end_time,created,updated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, x.Name, b2i(x.Enabled), x.Mode, x.Target, x.Action,
		daysString(x.Days), x.Date, x.Start, x.End, now, now)
	if err != nil {
		return schedules.Schedule{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetSchedule(id)
}

// UpdateSchedule replaces the editable fields of schedule x.ID.
func (s *Store) UpdateSchedule(x schedules.Schedule) (schedules.Schedule, error) {
	res, err := s.db.Exec(`UPDATE schedules SET name=?,enabled=?,mode=?,target=?,action=?,
		days=?,date=?,start=?,end_time=?,updated=? WHERE id=?`, x.Name, b2i(x.Enabled), x.Mode, x.Target, x.Action,
		daysString(x.Days), x.Date, x.Start, x.End, ts(time.Now()), x.ID)
	if err != nil {
		return schedules.Schedule{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return schedules.Schedule{}, ErrNotFound
	}
	return s.GetSchedule(x.ID)
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM schedules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetScheduleResult records the last time the scheduler acted and the outcome.
func (s *Store) SetScheduleResult(id int64, at time.Time, result string) error {
	_, err := s.db.Exec(`UPDATE schedules SET last_run=?, last_result=? WHERE id=?`, ts(at), result, id)
	return err
}

// SetScheduleEnabled switches one schedule on or off.
func (s *Store) SetScheduleEnabled(id int64, enabled bool) error {
	res, err := s.db.Exec(`UPDATE schedules SET enabled=?, updated=? WHERE id=?`, b2i(enabled), ts(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Settings --------------------------------------------------------------

// GetSetting returns a setting or ErrNotFound.
func (s *Store) GetSetting(k string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting upserts a setting.
func (s *Store) SetSetting(k, v string) error {
	_, err := s.db.Exec(`INSERT INTO settings(k,v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// Audit is one audit-log entry. Detail is a short JSON document; callers
// must never put secret values in it.
type Audit struct {
	ID       int64
	Time     time.Time
	Username string
	Action   string
	Detail   string
}

// AddAudit appends an entry.
func (s *Store) AddAudit(a Audit) error {
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO audit(ts,username,action,detail) VALUES (?,?,?,?)`,
		ts(a.Time), a.Username, a.Action, a.Detail)
	return err
}

// ListAudit returns the newest entries first (limit capped at 1000).
func (s *Store) ListAudit(limit int) ([]Audit, error) {
	return s.ListAuditPage(AuditFilter{Limit: limit})
}

// AuditFilter selects a page of the audit log. Zero values select nothing:
// BeforeID 0 starts at the newest entry, User and ActionPrefix "" match all.
type AuditFilter struct {
	Limit        int    // capped at 1000 (0: 1000)
	BeforeID     int64  // only entries with a smaller id (paging)
	User         string // exact username
	ActionPrefix string // literal prefix of the action, e.g. "system."
}

// likeEscape escapes the LIKE wildcards of s (escape character '\').
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListAuditPage returns the newest entries first that match f. Paging is by
// id, so entries added meanwhile do not shift the pages.
func (s *Store) ListAuditPage(f AuditFilter) ([]Audit, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 1000
	}
	q := `SELECT id,ts,username,action,detail FROM audit WHERE 1=1`
	var args []any
	if f.BeforeID > 0 {
		q += ` AND id < ?`
		args = append(args, f.BeforeID)
	}
	if f.User != "" {
		q += ` AND username = ?`
		args = append(args, f.User)
	}
	if f.ActionPrefix != "" {
		q += ` AND action LIKE ? ESCAPE '\'`
		args = append(args, likeEscape(f.ActionPrefix)+"%")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Audit{}
	for rows.Next() {
		var a Audit
		var t int64
		if err := rows.Scan(&a.ID, &t, &a.Username, &a.Action, &a.Detail); err != nil {
			return nil, err
		}
		a.Time = fromTS(t)
		out = append(out, a)
	}
	return out, rows.Err()
}
