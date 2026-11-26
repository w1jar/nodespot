package api

import (
	"net/http"
	"strconv"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

func (s *Server) systemLogsRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/logs", s.authed(store.RoleAdmin, s.handleLogs))
	m.HandleFunc("GET /api/system/services", s.authed(store.RoleAdmin, s.handleServices))
}

// handleLogs: unit must be on the helper's allowlist, lines is clamped to
// 50..1000 (default 200), since is "", "1h", "24h" or "boot". Without a unit
// the answer is the list of units.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, _ store.User) {
	q := r.URL.Query()
	unit := q.Get("unit")
	if unit == "" {
		writeJSON(w, 200, map[string]any{"units": helper.LogUnits()})
		return
	}
	if !helper.ValidLogUnit(unit) {
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown unit")
		return
	}
	since := q.Get("since")
	if !helper.ValidLogSince(since) {
		writeErr(w, http.StatusBadRequest, "bad_request", `since must be "1h", "24h" or "boot"`)
		return
	}
	lines := 0
	if v := q.Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid lines")
			return
		}
		lines = n
	}
	h := s.sysHost(w)
	if h == nil {
		return
	}
	res, err := h.Logs(r.Context(), unit, helper.ClampLogLines(lines), since)
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.sysHost(w)
	if h == nil {
		return
	}
	res, err := h.Services(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, res)
}
