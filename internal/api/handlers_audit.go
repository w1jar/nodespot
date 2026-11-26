package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/w1jar/nodespot/internal/store"
)

func (s *Server) auditRoutes() {
	s.mux.HandleFunc("GET /api/audit", s.authed(store.RoleAdmin, s.handleAuditList))
}

// AuditEntry is one row of GET /api/audit. Detail is the stored JSON text
// (secret-free by the rule of Server.audit).
type AuditEntry struct {
	ID     int64  `json:"id"`
	Time   string `json:"time"`
	User   string `json:"user"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

// handleAuditList pages the audit log, newest first, by entry id:
// ?limit= (1..200, default 50), ?before= (id of the last entry of the page
// before), ?user= (exact) and ?action= (prefix). "next" is the value for
// before, or null on the last page.
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.d.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "audit_unavailable", "the audit log is not available")
		return
	}
	q := r.URL.Query()
	f := store.AuditFilter{User: q.Get("user"), ActionPrefix: q.Get("action"), Limit: auditDefaultLimit}
	if len(f.User) > 64 || len(f.ActionPrefix) > 64 {
		writeErr(w, http.StatusBadRequest, "bad_request", "filter too long")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid limit")
			return
		}
		f.Limit = min(n, auditMaxLimit)
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid before")
			return
		}
		f.BeforeID = n
	}
	want := f.Limit
	f.Limit++ // one more tells whether there is a next page
	rows, err := s.d.Store.ListAuditPage(f)
	if err != nil {
		s.internal(w, err)
		return
	}
	var next *int64
	if len(rows) > want {
		rows = rows[:want]
		next = &rows[want-1].ID
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, a := range rows {
		out = append(out, AuditEntry{ID: a.ID, Time: a.Time.UTC().Format(time.RFC3339), User: a.Username, Action: a.Action, Detail: a.Detail})
	}
	writeJSON(w, 200, map[string]any{"entries": out, "next": next})
}

// audit records an action; detail must never contain secret values or file
// contents. Failures are logged only.
func (s *Server) audit(u store.User, action string, detail map[string]any) {
	if s.d.Store == nil {
		return
	}
	d := ""
	if detail != nil {
		b, _ := json.Marshal(detail)
		d = string(b)
	}
	if err := s.d.Store.AddAudit(store.Audit{Username: u.Username, Action: action, Detail: d}); err != nil {
		s.d.Logger.Printf("audit: %v", err)
	}
}
