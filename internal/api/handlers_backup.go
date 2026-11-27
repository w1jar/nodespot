package api

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/backupsvc"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// resultOpenFor is how long the last restore result can be read without a
// login (the restore replaces the user database, so every session is gone).
const resultOpenFor = 30 * time.Minute

func (s *Server) backupRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/backup/info", s.authed(store.RoleAdmin, s.handleBackupInfo))
	m.HandleFunc("POST /api/backup", s.authed(store.RoleAdmin, s.handleBackupCreate))
	m.HandleFunc("POST /api/restore/upload", s.handleRestoreUpload)
	m.HandleFunc("POST /api/restore/{id}/apply", s.handleRestoreApply)
	m.HandleFunc("DELETE /api/restore/{id}", s.handleRestoreCancel)
	m.HandleFunc("GET /api/restore/result", s.handleRestoreResult)
}

func (s *Server) backupOK(w http.ResponseWriter) bool {
	if s.d.Backup == nil {
		writeErr(w, http.StatusServiceUnavailable, "backup_unavailable", "backup and restore are not available")
		return false
	}
	return true
}

// backupErr maps a service error to a response.
func (s *Server) backupErr(w http.ResponseWriter, err error) {
	var (
		tn *backupsvc.TooNewError
		be *backup.Error
		nf *backup.NewerFormatError
		un *helper.UnavailableError
	)
	switch {
	case errors.Is(err, backupsvc.ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "the backup is larger than 256 MiB")
	case errors.As(err, &tn):
		writeErr(w, http.StatusConflict, "backup_too_new", tn.Msg)
	case errors.As(err, &nf):
		writeErr(w, http.StatusConflict, "backup_too_new", nf.Error())
	case errors.As(err, &be):
		writeErr(w, http.StatusBadRequest, "bad_backup", be.Msg)
	case errors.Is(err, backupsvc.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "no such restore (it may have expired); upload the backup again")
	case errors.Is(err, backupsvc.ErrBusy), errors.Is(err, helper.ErrBusy):
		writeErr(w, http.StatusConflict, "busy", "another backup or restore is running")
	case errors.As(err, &un):
		writeErr(w, http.StatusServiceUnavailable, "helper_unavailable", "the privileged helper is not available")
	default:
		s.d.Logger.Printf("backup: %v", err)
		writeErr(w, http.StatusBadGateway, "backup_failed", "the operation failed; see the log for details")
	}
}

func (s *Server) handleBackupInfo(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.backupOK(w) {
		return
	}
	info, err := s.d.Backup.Info(r.Context())
	if err != nil {
		s.backupErr(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.backupOK(w) {
		return
	}
	var in helper.BackupRequest
	if !decode(w, r, &in, true) {
		return
	}
	c, err := s.d.Backup.Create(r.Context(), in, u.Username)
	if err != nil {
		s.backupErr(w, err)
		return
	}
	defer c.Close()
	s.audit(u, "backup.create", map[string]any{"secrets": in.Secrets, "size": c.Size})
	h := w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+c.Name+`"`)
	h.Set("Content-Length", strconv.FormatInt(c.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, c.File)
}

// restoreAuth decides who may upload or apply: an admin session or, only
// while no user exists, the setup code. A rejection has been written.
// password is checked for a session user only when needPassword is set.
func (s *Server) restoreAuth(w http.ResponseWriter, r *http.Request, code, password string, needPassword bool) (store.User, bool) {
	if u, ok := s.d.Auth.Authenticate(r); ok {
		if auth.RoleRank(u.Role) < auth.RoleRank(store.RoleAdmin) {
			writeErr(w, http.StatusForbidden, "forbidden", "insufficient permissions")
			return u, false
		}
		if !needPassword {
			return u, true
		}
		full, err := s.d.Store.UserByID(u.ID)
		if err != nil {
			s.internal(w, err)
			return u, false
		}
		key := auth.Key(auth.ClientIP(r), "pw:"+u.Username)
		if ok, _ := s.d.Auth.Limiter.Allow(key); !ok {
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
			return u, false
		}
		if ok, _ := auth.VerifyPassword(password, full.PWHash); !ok {
			s.d.Auth.Limiter.Fail(key)
			writeErr(w, http.StatusForbidden, "bad_password", "the password is incorrect")
			return u, false
		}
		s.d.Auth.Limiter.Reset(key)
		return u, true
	}
	need, err := s.d.Auth.SetupNeeded()
	if err != nil {
		s.internal(w, err)
		return store.User{}, false
	}
	if !need {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return store.User{}, false
	}
	err = s.d.Auth.CheckSetupCode(auth.ClientIP(r), code)
	switch {
	case err == nil:
		return store.User{Username: "setup"}, true
	case s.rateLimited(w, err):
	case errors.Is(err, auth.ErrBadSetupCode):
		writeErr(w, http.StatusForbidden, "bad_setup_code", "incorrect setup code")
	case errors.Is(err, auth.ErrSetupDone):
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
	default:
		s.internal(w, err)
	}
	return store.User{}, false
}

// preAuth rejects, before any byte of the body is read, a request that can
// never succeed: a non-admin session, or no session while setup is done.
func (s *Server) preAuth(w http.ResponseWriter, r *http.Request) bool {
	if u, ok := s.d.Auth.Authenticate(r); ok {
		if auth.RoleRank(u.Role) < auth.RoleRank(store.RoleAdmin) {
			writeErr(w, http.StatusForbidden, "forbidden", "insufficient permissions")
			return false
		}
		return true
	}
	need, err := s.d.Auth.SetupNeeded()
	if err != nil {
		s.internal(w, err)
		return false
	}
	if !need {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return false
	}
	return true
}

func (s *Server) handleRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if !s.backupOK(w) || !s.preAuth(w, r) {
		return
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "multipart/form-data expected")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, backup.MaxUpload+(1<<20))
	mr := multipart.NewReader(r.Body, params["boundary"])
	var password, code string
	var spool *os.File
	defer func() {
		if spool != nil {
			spool.Close()
			os.Remove(spool.Name())
		}
	}()
	tooLarge := func(err error) bool {
		var mbe *http.MaxBytesError
		return errors.As(err, &mbe)
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if tooLarge(err) {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "the backup is larger than 256 MiB")
				return
			}
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid upload")
			return
		}
		switch part.FormName() {
		case "password", "code":
			b, err := io.ReadAll(io.LimitReader(part, 4096))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_request", "invalid upload")
				return
			}
			if part.FormName() == "password" {
				password = string(b)
			} else {
				code = string(b)
			}
		case "file":
			if spool != nil {
				writeErr(w, http.StatusBadRequest, "bad_request", "one file expected")
				return
			}
			spool, err = s.d.Backup.SpoolTemp()
			if err != nil {
				s.internal(w, err)
				return
			}
			n, err := io.Copy(spool, part)
			if err != nil {
				if tooLarge(err) {
					writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "the backup is larger than 256 MiB")
					return
				}
				writeErr(w, http.StatusBadRequest, "bad_request", "invalid upload")
				return
			}
			if n > backup.MaxUpload {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "the backup is larger than 256 MiB")
				return
			}
		default:
			_, _ = io.Copy(io.Discard, part)
		}
	}
	// Credentials last: the fields may follow the file in the body.
	if _, ok := s.restoreAuth(w, r, code, password, true); !ok {
		return
	}
	if spool == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "the field \"file\" is missing")
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		s.internal(w, err)
		return
	}
	up, err := s.d.Backup.Upload(r.Context(), spool)
	if err != nil {
		s.backupErr(w, err)
		return
	}
	writeJSON(w, 200, up)
}

func (s *Server) handleRestoreApply(w http.ResponseWriter, r *http.Request) {
	if !s.backupOK(w) || !s.preAuth(w, r) {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	u, ok := s.restoreAuth(w, r, in.Code, "", false)
	if !ok {
		return
	}
	res, err := s.d.Backup.Apply(r.Context(), r.PathValue("id"), u.Username)
	if err != nil {
		s.backupErr(w, err)
		return
	}
	restart := res.OK && s.d.Restart != nil
	res.Restarting = restart
	// The audit entry of a successful restore is written into the restored
	// database by the service; the old database is replaced anyway.
	if !res.OK {
		s.audit(u, "backup.restore", map[string]any{"ok": false})
	}
	writeJSON(w, 200, res)
	if restart {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			s.d.Restart()
		}()
	}
}

func (s *Server) handleRestoreCancel(w http.ResponseWriter, r *http.Request) {
	if !s.backupOK(w) || !s.preAuth(w, r) {
		return
	}
	if _, ok := s.restoreAuth(w, r, r.URL.Query().Get("code"), "", false); !ok {
		return
	}
	s.d.Backup.Cancel(r.PathValue("id"))
	noContent(w)
}

func (s *Server) handleRestoreResult(w http.ResponseWriter, r *http.Request) {
	if !s.backupOK(w) {
		return
	}
	res, ok := s.d.Backup.LastResult()
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no restore result")
		return
	}
	if time.Since(res.At) >= resultOpenFor {
		u, ok := s.d.Auth.Authenticate(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if auth.RoleRank(u.Role) < auth.RoleRank(store.RoleAdmin) {
			writeErr(w, http.StatusForbidden, "forbidden", "insufficient permissions")
			return
		}
	}
	writeJSON(w, 200, res)
}
