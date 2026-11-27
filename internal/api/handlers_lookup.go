package api

import (
	"net/http"
	"strconv"

	"github.com/w1jar/nodespot/internal/store"
)

// The AllStar node list: lookup and search for the AllStar mode's pickers
// and the "Now calling" names.

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request, _ store.User) {
	node := r.PathValue("node")
	if s.d.Nodelist != nil {
		if in, ok := s.d.Nodelist.Lookup(node); ok {
			writeJSON(w, 200, in)
			return
		}
	}
	writeErr(w, 404, "not_found", "node not in the node list")
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, _ store.User) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, 400, "bad_request", "invalid limit")
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	if s.d.Nodelist == nil {
		writeJSON(w, 200, []struct{}{})
		return
	}
	writeJSON(w, 200, s.d.Nodelist.Search(r.URL.Query().Get("q"), limit))
}

// handleEchoLinkSearch searches the EchoLink directory's station list for
// the EchoLink mode's picker. The node holds the list while EchoLink is
// logged in; otherwise the answer is loaded false with no results.
func (s *Server) handleEchoLinkSearch(w http.ResponseWriter, r *http.Request, _ store.User) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, 400, "bad_request", "invalid limit")
			return
		}
		limit = min(n, 200)
	}
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	res, err := s.d.Node.SearchDirectory(ctx, modeEchoLink, r.URL.Query().Get("q"), limit)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleNodelist(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.d.Nodelist == nil {
		writeJSON(w, 200, map[string]any{"count": 0, "updated": nil, "source": "none", "error": nil})
		return
	}
	writeJSON(w, 200, s.d.Nodelist.Status())
}

func (s *Server) handleNodelistRefresh(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.d.Nodelist != nil {
		s.d.Nodelist.RefreshAsync()
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request, _ store.User) {
	writeJSON(w, 200, s.systemJSON(r.Context()))
}
