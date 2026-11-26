package api

import (
	"net/http"

	"github.com/w1jar/nodespot/internal/store"
)

const settingFavScan = "favstats.enabled"

// SettingsJSON is the API Settings object. The connect defaults ("Disconnect
// before Connect" on, "Disconnect before Monitor" off) are fixed in the UI and
// are not settings; a user's own choice is kept in their browser.
type SettingsJSON struct {
	// FavoritesScan is the favorites activity scan switch (default off).
	FavoritesScan bool `json:"favoritesScan"`
}

// settings reads the global settings; unset or unreadable values fall back
// to their defaults.
func (s *Server) settings() SettingsJSON {
	var out SettingsJSON
	if s.d.Store == nil {
		return out
	}
	if v, err := s.d.Store.GetSetting(settingFavScan); err == nil {
		out.FavoritesScan = v == "true"
	}
	return out
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	writeJSON(w, 200, s.settings())
}
