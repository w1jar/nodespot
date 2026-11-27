package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// DiagBundler is the part of the helper that builds the diagnostics bundle.
// SysHost implements it when the bundle is available (optional on purpose:
// a SystemHost without it answers 503 system_unavailable).
type DiagBundler interface {
	DiagBundle(ctx context.Context, req helper.DiagRequest) (io.ReadCloser, error)
}

func (s *Server) systemDiagRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/diagnostics", s.authed(store.RoleAdmin, s.handleDiagPlan))
	m.HandleFunc("GET /api/system/diagnostics/bundle", s.authed(store.RoleAdmin, s.handleDiagBundle))
}

// DiagPlanJSON is the answer of GET /api/system/diagnostics: what the bundle
// holds, so the page can show it before anything is built.
type DiagPlanJSON struct {
	helper.DiagPlan
	// Available is false when this install has no privileged helper.
	Available bool `json:"available"`
	// MaskDefault is the default of the "mask my callsign and IP" checkbox.
	MaskDefault bool `json:"maskDefault"`
}

func (s *Server) handleDiagPlan(w http.ResponseWriter, _ *http.Request, _ store.User) {
	_, ok := s.d.SysHost.(DiagBundler)
	writeJSON(w, 200, DiagPlanJSON{DiagPlan: helper.DiagPlanInfo(), Available: ok && s.d.SysHost != nil, MaskDefault: true})
}

// diagAuditText renders the newest audit rows for audit.txt.
func (s *Server) diagAuditText() string {
	if s.d.Store == nil {
		return ""
	}
	rows, err := s.d.Store.ListAuditPage(store.AuditFilter{Limit: 200})
	if err != nil {
		return "audit log not available\n"
	}
	var b strings.Builder
	for _, a := range rows {
		fmt.Fprintf(&b, "%s %s %s %s\n", a.Time.UTC().Format(time.RFC3339), a.Username, a.Action, a.Detail)
		if b.Len() > helper.DiagMaxAudit-1024 {
			break
		}
	}
	return b.String()
}

// parseMask reads ?mask= (default on).
func parseMask(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "", "1", "true", "on", "yes":
		return true, true
	case "0", "false", "off", "no":
		return false, true
	}
	return false, false
}

// handleDiagBundle streams the .tar.gz built by the helper. The audit row is
// written before the first byte is sent.
func (s *Server) handleDiagBundle(w http.ResponseWriter, r *http.Request, u store.User) {
	mask, ok := parseMask(r.URL.Query().Get("mask"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request", "mask must be 0 or 1")
		return
	}
	if s.sysHost(w) == nil {
		return
	}
	hb, ok := s.d.SysHost.(DiagBundler)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: not installed from a package")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	rep := s.buildReport(ctx)
	cancel()
	req := helper.DiagRequest{
		MaskIdentity: mask,
		Version:      s.d.Version,
		Report:       capHead(rep.Render(), helper.DiagMaxReport),
		Audit:        capHead(s.diagAuditText(), helper.DiagMaxAudit),
		Secrets:      diagSecrets(s.knownSecrets()),
		Callsigns:    diagCallsigns(rep.KeepCalls()),
	}
	rc, err := hb.DiagBundle(r.Context(), req)
	if err != nil {
		if errors.Is(err, helper.ErrDiagBusy) {
			writeErr(w, http.StatusConflict, "diag_busy", "a diagnostics bundle is already being built; try again in a minute")
			return
		}
		s.systemError(w, err)
		return
	}
	defer rc.Close()
	s.audit(u, "system.diagnostics.download", map[string]any{"maskIdentity": mask})
	h := w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="nodespot-diagnostics-`+time.Now().Format("20060102-1504")+`.tar.gz"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// capHead keeps at most n bytes of s, cutting at a line end.
func capHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		s = s[:i+1]
	}
	return s
}

// diagSecrets keeps the literals the helper request allows.
func diagSecrets(in []string) []string {
	var out []string
	for _, v := range in {
		if len(v) >= 1 && len(v) <= 256 && len(out) < 20 {
			out = append(out, v)
		}
	}
	return out
}

var reDiagCall = regexp.MustCompile(`^[A-Za-z0-9/\-]{2,16}$`)

// diagCallsigns keeps the callsigns the helper request accepts (a node list
// entry with an odd name must not fail the whole download).
func diagCallsigns(in []string) []string {
	var out []string
	for _, v := range in {
		if reDiagCall.MatchString(v) && len(out) < 20 {
			out = append(out, v)
		}
	}
	return out
}
