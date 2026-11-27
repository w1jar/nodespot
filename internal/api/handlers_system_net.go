package api

import (
	"net/http"

	"github.com/w1jar/nodespot/internal/store"
)

func (s *Server) systemNetRoutes() {
	s.mux.HandleFunc("GET /api/system/network", s.authed(store.RoleAdmin, s.handleNetwork))
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.sysHost(w)
	if h == nil {
		return
	}
	res, err := h.Network(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, res)
}
