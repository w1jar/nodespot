package api

import (
	"context"
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// RFHost is the part of the helper that prepares the Pi for an MMDVM hat
// (sys_rf.go). A SystemHost without it gives 503 on those routes.
type RFHost interface {
	RFPrepare(ctx context.Context) (helper.RFPrepareResult, error)
	RFDeviceCheck(ctx context.Context) (helper.RFDeviceCheck, error)
}

var (
	_ RFHost = (*helper.Client)(nil)
	_ RFHost = (*helper.DemoSystem)(nil)
)

// modemRoutes are the MMDVM modem (the mmdvm radio preset). The modem's
// unique ID is never part of an answer: the node does not pass it on.
func (s *Server) modemRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/radio/modem", s.authed(store.RoleAdmin, s.handleModemGet))
	m.HandleFunc("POST /api/radio/modem/reset", s.authed(store.RoleAdmin, s.handleModemReset))
	m.HandleFunc("GET /api/radio/device-check", s.authed(store.RoleAdmin, s.handleRFDeviceCheck))
	m.HandleFunc("POST /api/radio/prepare", s.authed(store.RoleAdmin, s.handleRFPrepare))
}

// handleModemGet reports the modem: board, firmware, state and flags.
func (s *Server) handleModemGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	mi, err := s.d.Node.ModemStatus(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, mi)
}

// handleModemReset closes the modem's serial port and opens it again.
func (s *Server) handleModemReset(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	mi, err := s.d.Node.ModemReset(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "radio.modem.reset", nil)
	writeJSON(w, 200, mi)
}

func (s *Server) rfHost(w http.ResponseWriter) RFHost {
	h, ok := s.d.SysHost.(RFHost)
	if !ok || h == nil {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: not installed from a package")
		return nil
	}
	return h
}

// handleRFDeviceCheck reports the hat's serial port and the boot settings.
func (s *Server) handleRFDeviceCheck(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.rfHost(w)
	if h == nil {
		return
	}
	c, err := h.RFDeviceCheck(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

// handleRFPrepare frees the Pi's UART for the hat (serial console off,
// Bluetooth moved off it) and says whether a reboot is needed.
func (s *Server) handleRFPrepare(w http.ResponseWriter, r *http.Request, u store.User) {
	h := s.rfHost(w)
	if h == nil {
		return
	}
	res, err := h.RFPrepare(r.Context())
	if err != nil {
		s.audit(u, "radio.prepare", map[string]any{"ok": false})
		s.systemError(w, err)
		return
	}
	s.audit(u, "radio.prepare", map[string]any{"ok": true, "changes": len(res.Changes), "reboot": res.RebootNeeded})
	s.d.Logger.Printf("radio: Pi prepared for the MMDVM hat by %s (%d changes, reboot needed: %v)", u.Username, len(res.Changes), res.RebootNeeded)
	writeJSON(w, 200, res)
}
