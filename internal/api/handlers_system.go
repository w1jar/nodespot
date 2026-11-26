package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
)

// SystemHost is the part of the helper behind the System tab (sys.go in the
// helper package). nil in Deps: 503 system_unavailable.
type SystemHost interface {
	HostStatus(ctx context.Context) (helper.HostStatus, error)
	SetHostname(ctx context.Context, name string) (helper.HostStatus, error)
	SetTimezone(ctx context.Context, tz string) (helper.HostStatus, error)
	SetNTP(ctx context.Context, on bool) (helper.HostStatus, error)
	Timezones(ctx context.Context) ([]string, error)
	Poweroff(ctx context.Context) error
	Logs(ctx context.Context, unit string, lines int, since string) (helper.LogsResult, error)
	Services(ctx context.Context) (helper.ServicesResult, error)
	Network(ctx context.Context) (helper.NetStatus, error)
}

// systemRoutes registers the admin-only System tab endpoints. Every feature
// has its own file with its own routes function; add one line here for a new
// sub-section.
func (s *Server) systemRoutes() {
	s.systemHostRoutes()
	s.systemLogsRoutes()
	s.systemNetRoutes()
	s.systemSvcRoutes()
	s.systemNodeRoutes()
	s.vocoderRoutes()
	s.systemWifiRoutes()
	s.systemTerminalRoutes()
	s.systemDiagRoutes()
	s.auditRoutes()
}

// sysHost returns the helper or answers 503.
func (s *Server) sysHost(w http.ResponseWriter) SystemHost {
	if s.d.SysHost == nil {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: not installed from a package")
		return nil
	}
	return s.d.SysHost
}

// systemError maps a helper failure to an HTTP answer.
func (s *Server) systemError(w http.ResponseWriter, err error) {
	var ue *helper.UnavailableError
	switch {
	case errors.As(err, &ue):
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: "+ue.Reason)
	case errors.Is(err, helper.ErrBusy):
		writeErr(w, http.StatusConflict, "update_running", "an update is running")
	default:
		s.d.Logger.Printf("system: %v", err)
		writeErr(w, http.StatusBadGateway, "helper_error", err.Error())
	}
}
