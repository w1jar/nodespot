package api

import (
	"net/http"
	"time"

	"github.com/w1jar/nodespot/internal/favstats"
	"github.com/w1jar/nodespot/internal/store"
)

// initFavStats wires the favorites activity scan to this server's AllStar
// favorites, the station's own AllStar node and the SSE hub.
func (s *Server) initFavStats() {
	fs := s.d.FavStats
	if fs == nil {
		return
	}
	fs.Favorites = func() []string {
		if s.d.Store == nil {
			return nil
		}
		list, err := s.d.Store.ListFavorites(modeAllStar)
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(list))
		for _, f := range list {
			out = append(out, f.Target)
		}
		return out
	}
	// Local is this station's own AllStar node number (from the allstar
	// mode's settings), which the scan skips.
	fs.Local = func() []string {
		if n := s.nw.allStarNode(); n != "" {
			return []string{n}
		}
		return nil
	}
	fs.OnUpdate = func(u favstats.Update) {
		f := frame("favstats", u)
		s.hub.broadcast(sseMsg{priv: f, pub: f})
	}
	if s.d.Store != nil {
		fs.SetEnabled(s.settings().FavoritesScan)
	}
}

// handleFavStats serves the snapshot. It needs a session and is never part
// of the public page.
func (s *Server) handleFavStats(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.d.FavStats == nil {
		writeJSON(w, 200, favstats.Update{Stats: map[string]favstats.Activity{}, CheckedAt: map[string]time.Time{}, Removed: []string{}})
		return
	}
	writeJSON(w, 200, s.d.FavStats.Snapshot())
}

// handleFavScan switches the scan on or off (operators and admins, like the
// other favorites controls).
func (s *Server) handleFavScan(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, 400, "bad_request", "enabled (bool) is required")
		return
	}
	if s.d.FavStats == nil {
		writeErr(w, 503, "favstats_unavailable", "the favorites activity scan is not available")
		return
	}
	v := "false"
	if *in.Enabled {
		v = "true"
	}
	if err := s.d.Store.SetSetting(settingFavScan, v); err != nil {
		s.internal(w, err)
		return
	}
	s.d.Logger.Printf("setting %s=%s by %s", settingFavScan, v, u.Username)
	s.d.FavStats.SetEnabled(*in.Enabled)
	writeJSON(w, 200, s.d.FavStats.Snapshot())
}
