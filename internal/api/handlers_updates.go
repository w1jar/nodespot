package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
	"github.com/w1jar/nodespot/internal/system"
	"github.com/w1jar/nodespot/internal/updates"
)

// SystemJSON is System plus the cached "update available" flag.
type SystemJSON struct {
	system.System
	UpdateAvailable bool   `json:"updateAvailable"`
	UpdateVersion   string `json:"updateVersion"`
}

func (s *Server) systemJSON(ctx context.Context) SystemJSON {
	out := SystemJSON{System: s.d.System.Collect(ctx)}
	if s.d.Updates != nil {
		out.UpdateAvailable, out.UpdateVersion = s.d.Updates.Pending()
	}
	return out
}

func (s *Server) handleUpdatesGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.d.Updates == nil {
		writeJSON(w, 200, updates.Info{Reason: strPtr("not installed from a package"), Job: helper.JobIdle})
		return
	}
	writeJSON(w, 200, s.d.Updates.Info(r.Context()))
}

func (s *Server) handleUpdatesCheck(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.Updates == nil {
		s.updatesError(w, updates.ErrUnavailable)
		return
	}
	info, err := s.d.Updates.Check(r.Context())
	if err != nil {
		s.updatesError(w, err)
		return
	}
	s.d.Logger.Printf("update check by %s: installed=%s latest=%s", u.Username, info.Installed, info.Candidate)
	writeJSON(w, 200, info)
}

func (s *Server) handleUpdatesApply(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.Updates == nil {
		s.updatesError(w, updates.ErrUnavailable)
		return
	}
	info, err := s.d.Updates.Apply(r.Context())
	if err != nil {
		s.updatesError(w, err)
		return
	}
	s.d.Logger.Printf("update to %s started by %s", info.Candidate, u.Username)
	writeJSON(w, http.StatusAccepted, info)
}

// handleUpdatesChannel switches the update channel (PUT, admin only).
func (s *Server) handleUpdatesChannel(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Channel string `json:"channel"`
	}
	if !decode(w, r, &body, false) {
		return
	}
	if !updates.ValidChannel(body.Channel) {
		writeErr(w, http.StatusBadRequest, "bad_request", `channel must be "stable" or "beta"`)
		return
	}
	if s.d.Updates == nil {
		s.updatesError(w, updates.ErrUnavailable)
		return
	}
	info, err := s.d.Updates.SetChannel(r.Context(), body.Channel)
	if err != nil {
		s.updatesError(w, err)
		return
	}
	s.d.Logger.Printf("update channel set to %s by %s", info.Channel, u.Username)
	writeJSON(w, 200, info)
}

// Power is the part of the helper that restarts the panel and the machine.
type Power interface {
	RestartNodespot(ctx context.Context) error
	Reboot(ctx context.Context) error
}

// UpdatesBadge is the admin-only SSE event "updates": counts only, never
// package names.
type UpdatesBadge struct {
	Nodespot        bool   `json:"nodespot"`
	NodespotVersion string `json:"nodespotVersion"`
	OS              int    `json:"os"`
	Security        int    `json:"security"`
	Reboot          bool   `json:"reboot"`
}

func (s *Server) updatesBadge() UpdatesBadge {
	var b UpdatesBadge
	if s.d.Updates == nil {
		return b
	}
	b.Nodespot, b.NodespotVersion = s.d.Updates.Pending()
	if s.d.Updates.OS != nil {
		c := s.d.Updates.OS.Counts()
		b.OS, b.Security, b.Reboot = c.Count, c.Security, c.Reboot
	}
	return b
}

// broadcastUpdates sends the badge counts to admin clients only.
func (s *Server) broadcastUpdates() {
	s.hub.broadcast(sseMsg{admin: frame("updates", s.updatesBadge())})
}

func (s *Server) osManager(w http.ResponseWriter) *updates.OSManager {
	if s.d.Updates == nil || s.d.Updates.OS == nil {
		s.updatesError(w, updates.ErrUnavailable)
		return nil
	}
	return s.d.Updates.OS
}

// handleOSCheck starts a check in the background (202); the page polls GET.
func (s *Server) handleOSCheck(w http.ResponseWriter, r *http.Request, u store.User) {
	om := s.osManager(w)
	if om == nil {
		return
	}
	started, err := om.StartCheck(r.Context())
	if err != nil {
		s.updatesError(w, err)
		return
	}
	if started {
		s.d.Logger.Printf("OS update check started by %s", u.Username)
		s.broadcastUpdates()
	}
	writeJSON(w, http.StatusAccepted, s.d.Updates.Info(r.Context()))
}

func (s *Server) handleOSApply(w http.ResponseWriter, r *http.Request, u store.User) {
	om := s.osManager(w)
	if om == nil {
		return
	}
	names := om.Names(50)
	if _, err := om.Apply(r.Context()); err != nil {
		s.updatesError(w, err)
		return
	}
	c := om.Counts()
	s.d.Logger.Printf("OS update (%d packages) started by %s", c.Count, u.Username)
	s.audit(u, "updates.os.apply", map[string]any{"packages": names, "count": c.Count, "security": c.Security})
	writeJSON(w, http.StatusAccepted, s.d.Updates.Info(r.Context()))
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.Power == nil {
		s.updatesError(w, updates.ErrUnavailable)
		return
	}
	if err := s.d.Power.Reboot(r.Context()); err != nil {
		s.updatesError(w, err)
		return
	}
	s.d.Logger.Printf("reboot requested by %s", u.Username)
	s.audit(u, "system.reboot", nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) handleRestartNodespot(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.Power == nil {
		s.updatesError(w, updates.ErrUnavailable)
		return
	}
	if err := s.d.Power.RestartNodespot(r.Context()); err != nil {
		s.updatesError(w, err)
		return
	}
	s.d.Logger.Printf("nodespot restart requested by %s", u.Username)
	s.audit(u, "nodespot.restart", nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) updatesError(w http.ResponseWriter, err error) {
	var ue *helper.UnavailableError
	var ce *updates.CheckError
	switch {
	case errors.As(err, &ce):
		s.d.Logger.Printf("updates: %v", err)
		writeErr(w, http.StatusBadGateway, "check_failed", err.Error())
	case errors.Is(err, updates.ErrNoSource):
		writeErr(w, http.StatusServiceUnavailable, "updates_unavailable", err.Error())
	case errors.Is(err, updates.ErrUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "updates_unavailable", "updates are not available: not installed from a package")
	case errors.As(err, &ue):
		writeErr(w, http.StatusServiceUnavailable, "updates_unavailable", "updates are not available: "+ue.Reason)
	case errors.Is(err, helper.ErrBusy):
		writeErr(w, http.StatusConflict, "update_running", "an update is already running")
	case errors.Is(err, helper.ErrAptBusy):
		writeErr(w, http.StatusConflict, "apt_busy", "apt is busy, try again later")
	default:
		s.d.Logger.Printf("updates: %v", err)
		writeErr(w, http.StatusBadGateway, "helper_error", err.Error())
	}
}

func strPtr(s string) *string { return &s }
