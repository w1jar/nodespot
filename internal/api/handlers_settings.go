package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/w1jar/nodespot/internal/store"
)

const (
	settingFavScan     = "favstats.enabled"
	settingHiddenModes = "modes.hidden" // a JSON array of mode names
)

// maxHiddenModes bounds the hidden-modes list.
const maxHiddenModes = 32

// SettingsJSON is the API Settings object. The connect defaults ("Disconnect
// before Connect" on, "Disconnect before Monitor" off) are fixed in the UI and
// are not settings; a user's own choice is kept in their browser.
type SettingsJSON struct {
	// FavoritesScan is the favorites activity scan switch (default off).
	FavoritesScan bool `json:"favoritesScan"`
	// HiddenModes are the modes an admin hid from the Modes row and the
	// mode pickers (default none). Hiding keeps a mode's settings, its
	// favorites and its schedules.
	HiddenModes []string `json:"hiddenModes"`
}

// settings reads the global settings; unset or unreadable values fall back
// to their defaults.
func (s *Server) settings() SettingsJSON {
	out := SettingsJSON{HiddenModes: []string{}}
	if s.d.Store == nil {
		return out
	}
	if v, err := s.d.Store.GetSetting(settingFavScan); err == nil {
		out.FavoritesScan = v == "true"
	}
	if v, err := s.d.Store.GetSetting(settingHiddenModes); err == nil && v != "" {
		var l []string
		if json.Unmarshal([]byte(v), &l) == nil {
			if l, ok := cleanModeNames(l); ok {
				out.HiddenModes = l
			}
		}
	}
	return out
}

// cleanModeNames lower-cases, sorts and dedupes mode names; ok is false
// when one is not a mode name or there are too many.
func cleanModeNames(in []string) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range in {
		n = strings.ToLower(strings.TrimSpace(n))
		if !reModeName.MatchString(n) {
			return nil, false
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) > maxHiddenModes {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

// handleHiddenModesPut replaces the hidden-modes list (admin). The modes
// themselves are not touched.
func (s *Server) handleHiddenModesPut(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct {
		HiddenModes *[]string `json:"hiddenModes"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.HiddenModes == nil {
		writeErr(w, 400, "bad_request", "hiddenModes (a list of mode names) is required")
		return
	}
	l, ok := cleanModeNames(*in.HiddenModes)
	if !ok {
		writeErr(w, 400, "bad_request", "hiddenModes must be at most 32 mode names")
		return
	}
	b, _ := json.Marshal(l)
	if err := s.d.Store.SetSetting(settingHiddenModes, string(b)); err != nil {
		s.internal(w, err)
		return
	}
	s.audit(u, "modes.hidden", map[string]any{"hiddenModes": l})
	out := s.settings()
	f := frame("settings", out)
	s.hub.broadcast(sseMsg{all: f})
	writeJSON(w, 200, out)
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	writeJSON(w, 200, s.settings())
}
