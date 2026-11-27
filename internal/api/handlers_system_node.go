package api

import (
	"context"
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// nodeUnit is the systemd unit of the native node engine.
const nodeUnit = "nodespot-node.service"

// NodeSvcHost is the part of the helper that restarts the node service
// (sys_node.go). A SystemHost without it gives 503 on the restart route.
type NodeSvcHost interface {
	NodeRestart(ctx context.Context) error
}

// nodeServiceState is the answer of GET /api/system/node-service.
type nodeServiceState struct {
	Available bool                 `json:"available"` // false: the unit is not installed or systemd cannot be asked
	Service   *helper.ServiceState `json:"service,omitempty"`
}

func (s *Server) systemNodeRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/node-service", s.authed(store.RoleAdmin, s.handleNodeServiceGet))
	m.HandleFunc("POST /api/system/node-service/restart", s.authed(store.RoleAdmin, s.handleNodeServiceRestart))
}

// handleNodeServiceGet reports the systemd state of nodespot-node.service.
func (s *Server) handleNodeServiceGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.sysHost(w)
	if h == nil {
		return
	}
	res, err := h.Services(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	out := nodeServiceState{}
	if res.Available {
		for i := range res.Services {
			if res.Services[i].Unit == nodeUnit {
				st := res.Services[i]
				out.Available, out.Service = true, &st
				break
			}
		}
	}
	writeJSON(w, 200, out)
}

// handleNodeServiceRestart restarts nodespot-node.service and waits for it.
// The node status watcher reconnects to the control socket by itself.
func (s *Server) handleNodeServiceRestart(w http.ResponseWriter, r *http.Request, u store.User) {
	h, ok := s.d.SysHost.(NodeSvcHost)
	if !ok || h == nil {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: not installed from a package")
		return
	}
	if err := h.NodeRestart(r.Context()); err != nil {
		s.audit(u, "node.restart", map[string]any{"ok": false})
		s.systemError(w, err)
		return
	}
	s.audit(u, "node.restart", map[string]any{"ok": true})
	s.d.Logger.Printf("node: nodespot-node restarted by %s", u.Username)
	writeJSON(w, 200, map[string]any{"ok": true})
}
