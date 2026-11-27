package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/w1jar/nodespot/internal/schedules"
)

// SchemaVersion is the schema version of this build.
func SchemaVersion() int { return len(migrations) }

// Snapshot writes a consistent copy of the database to dst (which must not
// exist) for a backup. Login sessions are left out: nobody stays logged in
// after a restore.
func (s *Store) Snapshot(dst string) error {
	if _, err := s.db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("store: snapshot: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+dst+"?"+url.Values{"_pragma": {"busy_timeout(5000)"}}.Encode())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`DELETE FROM sessions`); err != nil {
		os.Remove(dst)
		return fmt.Errorf("store: snapshot: %w", err)
	}
	return nil
}

// InspectedUser is an account of a database file.
type InspectedUser struct{ Username, Role string }

// Inspection is what a backup's database file holds.
type Inspection struct {
	Version   int
	Users     []InspectedUser
	Favorites int
	Schedules int
}

// ErrSchemaNewer means the database file has a newer schema than this build.
var ErrSchemaNewer = errors.New("store: database schema is newer than this build")

// Inspect opens the database file at path read-only (no migration) and
// checks that it is an intact nodespot database without triggers or views.
func Inspect(path string) (Inspection, error) {
	var in Inspection
	db, err := sql.Open("sqlite", "file:"+path+"?"+url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)"}}.Encode())
	if err != nil {
		return in, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var chk string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&chk); err != nil {
		return in, fmt.Errorf("not a database: %w", err)
	}
	if chk != "ok" {
		return in, fmt.Errorf("database is damaged: %s", chk)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type IN ('trigger','view')`).Scan(&n); err != nil {
		return in, err
	}
	if n > 0 {
		return in, errors.New("database contains triggers or views")
	}
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&in.Version); err != nil {
		return in, fmt.Errorf("not an nodespot database: %w", err)
	}
	if in.Version > len(migrations) {
		return in, ErrSchemaNewer
	}
	rows, err := db.Query(`SELECT username, role FROM users ORDER BY id`)
	if err != nil {
		return in, fmt.Errorf("not an nodespot database: %w", err)
	}
	defer rows.Close()
	in.Users = []InspectedUser{}
	for rows.Next() {
		var u InspectedUser
		if err := rows.Scan(&u.Username, &u.Role); err != nil {
			return in, err
		}
		if !ValidRole(u.Role) {
			return in, errors.New("database has an invalid role")
		}
		in.Users = append(in.Users, u)
	}
	if err := rows.Err(); err != nil {
		return in, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM favorites`).Scan(&in.Favorites); err != nil {
		return in, fmt.Errorf("not an nodespot database: %w", err)
	}
	// The schedules table exists from migration 4; older backups have none.
	if in.Version >= 4 {
		if err := db.QueryRow(`SELECT COUNT(*) FROM schedules`).Scan(&in.Schedules); err != nil {
			return in, fmt.Errorf("not an nodespot database: %w", err)
		}
	}
	return in, nil
}

// MigrateFile opens the database file at path (migrating it forward),
// optionally appends an audit entry, and closes it cleanly (WAL merged).
// It also removes the schedules.state setting (a running-window record) and
// the cancel notice that refers to it (schedules.last_cancel), so a restored
// database never acts on a window of the backed-up node. schedules.resume_at
// is kept: it belongs to the global switch, which is restored with it.
func MigrateFile(path string, audit *Audit) error {
	s, err := Open(path)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM settings WHERE k IN (?, ?)`, schedules.KeyState, schedules.KeyLastCancel); err != nil {
		s.Close()
		return err
	}
	if audit != nil {
		if err := s.AddAudit(*audit); err != nil {
			s.Close()
			return err
		}
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		s.Close()
		return err
	}
	return s.Close()
}
