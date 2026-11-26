package api

import (
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

func (s *Server) systemHostRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/host", s.authed(store.RoleAdmin, s.handleHostGet))
	m.HandleFunc("PUT /api/system/host", s.authed(store.RoleAdmin, s.handleHostPut))
	m.HandleFunc("GET /api/system/timezones", s.authed(store.RoleAdmin, s.handleTimezones))
	m.HandleFunc("POST /api/system/poweroff", s.authed(store.RoleAdmin, s.handlePoweroff))
}

func (s *Server) handleHostGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.sysHost(w)
	if h == nil {
		return
	}
	st, err := h.HostStatus(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, st)
}

// hostPut: any of the three fields; the ones present are applied in the
// order hostname, timezone, NTP, each audited.
type hostPut struct {
	Hostname *string `json:"hostname"`
	Timezone *string `json:"timezone"`
	NTP      *bool   `json:"ntp"`
}

func (s *Server) handleHostPut(w http.ResponseWriter, r *http.Request, u store.User) {
	var body hostPut
	if !decode(w, r, &body, false) {
		return
	}
	if body.Hostname == nil && body.Timezone == nil && body.NTP == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "nothing to change")
		return
	}
	var name string
	if body.Hostname != nil {
		n, err := helper.ValidHostname(*body.Hostname)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		name = n
	}
	if body.Timezone != nil && !helper.ValidTimezoneSyntax(*body.Timezone) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid timezone")
		return
	}
	h := s.sysHost(w)
	if h == nil {
		return
	}
	ctx := r.Context()
	before, err := h.HostStatus(ctx)
	if err != nil {
		s.systemError(w, err)
		return
	}
	st := before
	if body.Hostname != nil && name != before.Hostname {
		if st, err = h.SetHostname(ctx, name); err != nil {
			s.systemError(w, err)
			return
		}
		s.d.Logger.Printf("hostname changed to %s by %s", name, u.Username)
		s.audit(u, "system.hostname", map[string]any{"from": before.Hostname, "to": name})
	}
	if body.Timezone != nil && *body.Timezone != before.Timezone {
		zones, err := h.Timezones(ctx)
		if err != nil {
			s.systemError(w, err)
			return
		}
		if !contains(zones, *body.Timezone) {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid timezone")
			return
		}
		if st, err = h.SetTimezone(ctx, *body.Timezone); err != nil {
			s.systemError(w, err)
			return
		}
		s.d.Logger.Printf("timezone changed to %s by %s", *body.Timezone, u.Username)
		s.audit(u, "system.timezone", map[string]any{"from": before.Timezone, "to": *body.Timezone})
	}
	if body.NTP != nil && *body.NTP != before.NTP {
		if st, err = h.SetNTP(ctx, *body.NTP); err != nil {
			s.systemError(w, err)
			return
		}
		s.d.Logger.Printf("NTP set to %v by %s", *body.NTP, u.Username)
		s.audit(u, "system.ntp", map[string]any{"from": before.NTP, "to": *body.NTP})
	}
	writeJSON(w, 200, st)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) handleTimezones(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.sysHost(w)
	if h == nil {
		return
	}
	zones, err := h.Timezones(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"timezones": zones})
}

func (s *Server) handlePoweroff(w http.ResponseWriter, r *http.Request, u store.User) {
	h := s.sysHost(w)
	if h == nil {
		return
	}
	if err := h.Poweroff(r.Context()); err != nil {
		s.systemError(w, err)
		return
	}
	s.d.Logger.Printf("poweroff requested by %s", u.Username)
	s.audit(u, "system.poweroff", nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}
