package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// SvcHost is the part of the helper behind the ASL services switch and the
// Web port section (sys_svc.go, sys_webport.go). The System helper in Deps
// provides it; a SystemHost without these methods gives 503.
type SvcHost interface {
	SvcStatus(ctx context.Context) (helper.SvcStatus, error)
	SvcSet(ctx context.Context, scope, action string, firewall bool) (helper.SvcSetResult, error)
	WebPortStatus(ctx context.Context) (helper.WebPortStatus, error)
	WebPortSet(ctx context.Context, action string) (helper.WebPortStatus, error)
}

func (s *Server) systemSvcRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/asl-services", s.authed(store.RoleAdmin, s.handleSvcGet))
	m.HandleFunc("PUT /api/system/asl-services/{scope}", s.authed(store.RoleAdmin, s.handleSvcPut))
	m.HandleFunc("GET /api/system/web-port", s.authed(store.RoleAdmin, s.handleWebPortGet))
	m.HandleFunc("PUT /api/system/web-port", s.authed(store.RoleAdmin, s.handleWebPortPut))
}

// svcHost returns the helper as an SvcHost or answers 503.
func (s *Server) svcHost(w http.ResponseWriter) SvcHost {
	if h, ok := s.d.SysHost.(SvcHost); ok && h != nil {
		return h
	}
	writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: not installed from a package")
	return nil
}

// svcError maps the typed failures of the two features to answers and
// leaves the rest to systemError.
func (s *Server) svcError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, helper.ErrPort80Held):
		writeErr(w, http.StatusConflict, "port80_held", "nodespot is using port 80. Move nodespot to another port first.")
	case errors.Is(err, helper.ErrSvcNotInstalled):
		writeErr(w, http.StatusNotFound, "not_installed", "that service is not installed on this node")
	case errors.Is(err, helper.ErrPort80Busy):
		writeErr(w, http.StatusConflict, "port80_busy", "port 80 is in use by another program (Apache?). Turn it off first.")
	case errors.Is(err, helper.ErrPort80Unchecked):
		writeErr(w, http.StatusConflict, "port80_unchecked", "cannot check whether port 80 is free (ss is not available)")
	case errors.Is(err, helper.ErrAlreadyPort80):
		writeErr(w, http.StatusConflict, "already_port80", "nodespot already uses port 80")
	case errors.Is(err, helper.ErrNotOnPort80):
		writeErr(w, http.StatusConflict, "not_port80", "nodespot is not on port 80")
	default:
		s.systemError(w, err)
	}
}

func (s *Server) handleSvcGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.svcHost(w)
	if h == nil {
		return
	}
	st, err := h.SvcStatus(r.Context())
	if err != nil {
		s.svcError(w, err)
		return
	}
	writeJSON(w, 200, st)
}

type svcPut struct {
	Action   string `json:"action"`   // enable | disable
	Firewall bool   `json:"firewall"` // also close (disable) or reopen (enable) the firewalld services
}

func (s *Server) handleSvcPut(w http.ResponseWriter, r *http.Request, u store.User) {
	scope := r.PathValue("scope")
	var body svcPut
	if !decode(w, r, &body, false) {
		return
	}
	if !helper.ValidSvcScope(scope) {
		writeErr(w, http.StatusNotFound, "not_found", "unknown service")
		return
	}
	if !helper.ValidSvcAction(body.Action) {
		writeErr(w, http.StatusBadRequest, "bad_request", "action must be enable or disable")
		return
	}
	h := s.svcHost(w)
	if h == nil {
		return
	}
	res, err := h.SvcSet(r.Context(), scope, body.Action, body.Firewall)
	if err != nil {
		s.svcError(w, err)
		return
	}
	s.d.Logger.Printf("service %s: %s by %s", scope, body.Action, u.Username)
	s.audit(u, "system.svc", map[string]any{"scope": scope, "action": body.Action, "units": res.Changed,
		"firewall": res.FirewallChanged, "firewallError": res.FirewallError})
	writeJSON(w, 200, res)
}

func (s *Server) handleWebPortGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.svcHost(w)
	if h == nil {
		return
	}
	st, err := h.WebPortStatus(r.Context())
	if err != nil {
		s.svcError(w, err)
		return
	}
	writeJSON(w, 200, st)
}

type webPortPut struct {
	Action string `json:"action"` // move | back | confirm
}

// handleWebPortPut: move and back restart nodespot, so their audit row is
// written before the helper call (the answer may never arrive over the old
// connection). confirm is written after it.
func (s *Server) handleWebPortPut(w http.ResponseWriter, r *http.Request, u store.User) {
	var body webPortPut
	if !decode(w, r, &body, false) {
		return
	}
	switch body.Action {
	case helper.WebPortActionMove, helper.WebPortActionBack, helper.WebPortActionConfirm:
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "action must be move, back or confirm")
		return
	}
	h := s.svcHost(w)
	if h == nil {
		return
	}
	ctx := r.Context()
	if body.Action != helper.WebPortActionConfirm {
		// Refuse before auditing what the helper would refuse anyway.
		before, err := h.WebPortStatus(ctx)
		if err != nil {
			s.svcError(w, err)
			return
		}
		switch {
		case body.Action == helper.WebPortActionMove && before.On80:
			s.svcError(w, helper.ErrAlreadyPort80)
			return
		case body.Action == helper.WebPortActionMove && !before.CanMove && before.Reason == "busy":
			s.svcError(w, helper.ErrPort80Busy)
			return
		case body.Action == helper.WebPortActionBack && !before.On80 && !before.DropIn:
			s.svcError(w, helper.ErrNotOnPort80)
			return
		}
		s.d.Logger.Printf("web port %s requested by %s", body.Action, u.Username)
		s.audit(u, "system.webport", map[string]any{"action": body.Action, "from": before.Port})
	}
	st, err := h.WebPortSet(ctx, body.Action)
	if err != nil {
		s.svcError(w, err)
		return
	}
	if body.Action == helper.WebPortActionConfirm {
		s.audit(u, "system.webport", map[string]any{"action": body.Action, "port": st.Port})
	}
	writeJSON(w, 200, st)
}
