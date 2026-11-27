package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/store"
)

func (s *Server) handleSetupGet(w http.ResponseWriter, r *http.Request) {
	need, err := s.d.Auth.SetupNeeded()
	if err != nil {
		s.internal(w, err)
		return
	}
	// hotspot: this request came in over the setup hotspot (the web UI warns
	// that a login typed there can be read by others in range). station:
	// the node runs but has no callsign yet, so the one-screen station setup
	// follows the first login.
	writeJSON(w, 200, map[string]bool{"needed": need, "hotspot": viaHotspot(r), "station": s.nodeJSON().SetupNeeded})
}

func (s *Server) rateLimited(w http.ResponseWriter, err error) bool {
	var rl *auth.RateLimitError
	if !errors.As(err, &rl) {
		return false
	}
	secs := int(rl.RetryAfter/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
	return true
}

func (s *Server) handleSetupPost(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code, Username, Password string }
	if !decode(w, r, &in, false) {
		return
	}
	tok, _, err := s.d.Auth.CompleteSetup(auth.ClientIP(r), in.Code, strings.TrimSpace(in.Username), in.Password)
	switch {
	case err == nil:
		s.d.Auth.SetCookie(w, r, tok)
		noContent(w)
	case s.rateLimited(w, err):
	case errors.Is(err, auth.ErrSetupDone):
		writeErr(w, http.StatusConflict, "setup_done", "setup has already been completed")
	case errors.Is(err, auth.ErrBadSetupCode):
		writeErr(w, http.StatusForbidden, "bad_setup_code", "incorrect setup code")
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, "exists", "user already exists")
	default:
		// Validation errors (username/password rules) are safe to show.
		if strings.HasPrefix(err.Error(), "username") || strings.HasPrefix(err.Error(), "password") {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.internal(w, err)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if !decode(w, r, &in, false) {
		return
	}
	tok, u, err := s.d.Auth.Login(auth.ClientIP(r), strings.TrimSpace(in.Username), in.Password)
	switch {
	case err == nil:
		s.d.Auth.SetCookie(w, r, tok)
		writeJSON(w, 200, userJSON(u))
	case s.rateLimited(w, err):
	case errors.Is(err, auth.ErrBadCredentials):
		writeErr(w, http.StatusUnauthorized, "bad_credentials", "incorrect username or password")
	default:
		s.internal(w, err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := auth.TokenFrom(r); tok != "" {
		_ = s.d.Auth.EndSession(tok)
	}
	s.d.Auth.ClearCookie(w, r)
	noContent(w)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, u store.User) {
	writeJSON(w, 200, userJSON(u))
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct{ Current, New string }
	if !decode(w, r, &in, false) {
		return
	}
	full, err := s.d.Store.UserByID(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	key := auth.Key(auth.ClientIP(r), "pw:"+u.Username)
	if ok, _ := s.d.Auth.Limiter.Allow(key); !ok {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
		return
	}
	ok, _ := auth.VerifyPassword(in.Current, full.PWHash)
	if !ok {
		s.d.Auth.Limiter.Fail(key)
		writeErr(w, http.StatusForbidden, "bad_credentials", "current password is incorrect")
		return
	}
	if err := auth.ValidatePassword(in.New); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.d.Store.UpdateUser(u.ID, nil, &hash); err != nil {
		s.internal(w, err)
		return
	}
	s.d.Auth.Limiter.Reset(key)
	// Sign out other devices; keep this session.
	_ = s.d.Store.DeleteUserSessions(u.ID, auth.SessionHash(auth.TokenFrom(r)))
	noContent(w)
}

// ------------------------------------------------------------------ users

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request, _ store.User) {
	us, err := s.d.Store.ListUsers()
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]UserJSON, len(us))
	for i, u := range us {
		out[i] = userJSON(u)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleUsersCreate(w http.ResponseWriter, r *http.Request, _ store.User) {
	var in struct{ Username, Password, Role string }
	if !decode(w, r, &in, false) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := auth.ValidateUsername(in.Username); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !store.ValidRole(in.Role) {
		writeErr(w, 400, "bad_request", "role must be admin, operator or viewer")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.internal(w, err)
		return
	}
	u, err := s.d.Store.CreateUser(in.Username, hash, in.Role)
	if errors.Is(err, store.ErrExists) {
		writeErr(w, 409, "exists", "a user with that name already exists")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, 201, userJSON(u))
}

func (s *Server) handleUsersUpdate(w http.ResponseWriter, r *http.Request, me store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Role     *string `json:"role"`
		Password *string `json:"password"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	target, err := s.d.Store.UserByID(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such user")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	var hash *string
	if in.Role != nil {
		if !store.ValidRole(*in.Role) {
			writeErr(w, 400, "bad_request", "role must be admin, operator or viewer")
			return
		}
		if target.Role == store.RoleAdmin && *in.Role != store.RoleAdmin {
			if n, _ := s.d.Store.CountAdmins(); n <= 1 {
				writeErr(w, 409, "last_admin", "cannot demote the last admin")
				return
			}
		}
	}
	if in.Password != nil {
		if err := auth.ValidatePassword(*in.Password); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		h, err := auth.HashPassword(*in.Password)
		if err != nil {
			s.internal(w, err)
			return
		}
		hash = &h
	}
	if err := s.d.Store.UpdateUser(id, in.Role, hash); err != nil {
		s.internal(w, err)
		return
	}
	// A password reset or role change signs the user out everywhere, except
	// the admin changing their own account on this device.
	if hash != nil || (in.Role != nil && *in.Role != target.Role) {
		keep := ""
		if id == me.ID {
			keep = auth.SessionHash(auth.TokenFrom(r))
		}
		_ = s.d.Store.DeleteUserSessions(id, keep)
	}
	u, err := s.d.Store.UserByID(id)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, 200, userJSON(u))
}

func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request, me store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if id == me.ID {
		writeErr(w, 409, "self_delete", "you cannot delete your own account")
		return
	}
	target, err := s.d.Store.UserByID(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such user")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if target.Role == store.RoleAdmin {
		if n, _ := s.d.Store.CountAdmins(); n <= 1 {
			writeErr(w, 409, "last_admin", "cannot delete the last admin")
			return
		}
	}
	if err := s.d.Store.DeleteUser(id); err != nil {
		s.internal(w, err)
		return
	}
	noContent(w)
}
