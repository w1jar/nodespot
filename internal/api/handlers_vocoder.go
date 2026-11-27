package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// VocoderHost is the part of the helper that installs the AMBE vocoder
// (helper/vocoder.go). A SystemHost without it gives 503. The install is a
// helper job: POST starts it and the page polls GET until job is "done" or
// "failed".
type VocoderHost interface {
	VocoderStatus(ctx context.Context) (helper.VocoderStatus, error)
	VocoderInstall(ctx context.Context) (helper.VocoderStatus, error)
}

// VocoderJSON is the answer of GET /api/vocoder and POST /api/vocoder/install.
type VocoderJSON struct {
	// Installed: the md380-emu package is installed. Active: its service runs.
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
	// Needed lists the modes (by name) that need the vocoder.
	Needed []string `json:"needed"`
	// Supported is false, with Reason, on a host the install does not support.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	// Job is "", running, failed or done; JobLog is its latest output.
	Job    string `json:"job,omitempty"`
	JobLog string `json:"jobLog,omitempty"`
	// JobStarted is when that job started (RFC 3339); empty when unknown.
	JobStarted string `json:"jobStarted,omitempty"`
}

func (s *Server) vocoderRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/vocoder", s.authed(store.RoleAdmin, s.handleVocoderGet))
	m.HandleFunc("POST /api/vocoder/install", s.authed(store.RoleAdmin, s.handleVocoderInstall))
}

// vocoderHost returns the helper as a VocoderHost or answers 503.
func (s *Server) vocoderHost(w http.ResponseWriter) VocoderHost {
	if h, ok := s.d.SysHost.(VocoderHost); ok && h != nil {
		return h
	}
	writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: not installed from a package")
	return nil
}

// ambeModes returns the names of the modes that need the vocoder. An
// unreachable node gives none: the page then has nothing to ask for.
func (s *Server) ambeModes(r *http.Request) []string {
	out := []string{}
	if s.d.Node == nil {
		return out
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.Modes(ctx)
	if err != nil {
		return out
	}
	s.nw.setModes(l)
	for _, m := range l {
		if m.NeedsAmbe {
			out = append(out, m.Name)
		}
	}
	return out
}

func (s *Server) vocoderJSON(r *http.Request, st helper.VocoderStatus) VocoderJSON {
	return VocoderJSON{
		Installed: st.Installed, Active: st.Active, Needed: s.ambeModes(r),
		Supported: st.Supported, Reason: st.Reason, Job: st.Job, JobLog: st.JobLog, JobStarted: st.JobStarted,
	}
}

func (s *Server) handleVocoderGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.vocoderHost(w)
	if h == nil {
		return
	}
	st, err := h.VocoderStatus(r.Context())
	if err != nil {
		s.vocoderError(w, err)
		return
	}
	writeJSON(w, 200, s.vocoderJSON(r, st))
}

// handleVocoderInstall starts the install job (202). It answers 200 with
// no job when the vocoder is already installed and running, and 202 with the
// running job when one was already started.
func (s *Server) handleVocoderInstall(w http.ResponseWriter, r *http.Request, u store.User) {
	h := s.vocoderHost(w)
	if h == nil {
		return
	}
	st, err := h.VocoderInstall(r.Context())
	if err != nil {
		s.audit(u, "vocoder.install", map[string]any{"ok": false})
		s.vocoderError(w, err)
		return
	}
	code := http.StatusOK
	if st.Job == "running" {
		code = http.StatusAccepted
		s.d.Logger.Printf("vocoder: install started by %s", u.Username)
		s.audit(u, "vocoder.install", map[string]any{"ok": true})
	}
	writeJSON(w, code, s.vocoderJSON(r, st))
}

// vocoderError maps the helper's coded refusals and leaves the rest to
// systemError.
func (s *Server) vocoderError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "not_supported:"):
		writeErr(w, http.StatusConflict, "not_supported", strings.TrimSpace(strings.TrimPrefix(msg, "not_supported:")))
	case strings.HasPrefix(msg, "clock_unset:"):
		writeErr(w, http.StatusConflict, "clock_unset", strings.TrimSpace(strings.TrimPrefix(msg, "clock_unset:")))
	case strings.HasPrefix(msg, "key_mismatch:"):
		writeErr(w, http.StatusBadGateway, "key_mismatch", strings.TrimSpace(strings.TrimPrefix(msg, "key_mismatch:")))
	case errors.Is(err, helper.ErrAptBusy):
		writeErr(w, http.StatusConflict, "apt_busy", "apt is busy, try again later")
	default:
		s.systemError(w, err)
	}
}
