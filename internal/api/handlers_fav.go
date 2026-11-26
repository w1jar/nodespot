package api

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/w1jar/nodespot/internal/store"
)

const (
	maxLabel = 120
	maxGroup = 60
	// maxTarget is the longest favorite target (a node number, talkgroup or
	// reflector name).
	maxTarget = 64
)

// defaultLabel is the label of a favorite saved without one: for an
// AllStar node its callsign and description from the node list, for a
// reflector the name the host lists know, else the target itself.
func (s *Server) defaultLabel(m, target string) string {
	if m == modeAllStar && s.d.Nodelist != nil {
		if in, ok := s.d.Nodelist.Lookup(target); ok {
			l := strings.TrimSpace(in.Callsign + " " + in.Description)
			if l != "" {
				return truncate(l, maxLabel)
			}
		}
	}
	if t := s.resolveTarget(m, target); t.Name != "" && t.Name != target {
		return truncate(t.Name, maxLabel)
	}
	return target
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func cleanText(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// cleanTarget checks a favorite's target: not empty, short, no control
// characters, and for AllStar and EchoLink a node number.
func cleanTarget(m, t string) (string, string) {
	t, ok := cleanText(t, maxTarget)
	if !ok || t == "" {
		return "", "target must be 1 to 64 characters without control characters"
	}
	if (m == modeAllStar || m == modeEchoLink) && !isDigits(t) {
		return "", "target must be a node number"
	}
	return t, ""
}

func isDigits(s string) bool {
	if s == "" || len(s) > 7 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// favMode reads and checks a mode name from a request ("" allowed when
// optional).
func (s *Server) favMode(w http.ResponseWriter, m string, optional bool) (string, bool) {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" && optional {
		return "", true
	}
	if m == "" || !reModeName.MatchString(m) {
		writeErr(w, 400, "bad_mode", "mode must be one of the node's modes")
		return "", false
	}
	if ms := s.nw.modeNames(); len(ms) > 0 && !contains(ms, m) {
		writeErr(w, 400, "bad_mode", "the node has no mode "+m)
		return "", false
	}
	return m, true
}

func (s *Server) favErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "no such favorite")
	case errors.Is(err, store.ErrDuplicate):
		writeErr(w, 409, "duplicate", "that target is a favorite already")
	case errors.Is(err, store.ErrLimit):
		writeErr(w, 409, "limit", "too many favorites for this mode")
	case errors.Is(err, store.ErrMismatch):
		writeErr(w, 400, "bad_request", "ids must list every favorite of the mode once")
	default:
		s.internal(w, err)
	}
}

// handleFavList serves GET /api/favorites?mode= (every mode when absent).
func (s *Server) handleFavList(w http.ResponseWriter, r *http.Request, _ store.User) {
	m, ok := s.favMode(w, r.URL.Query().Get("mode"), true)
	if !ok {
		return
	}
	favs, err := s.favoritesJSON(m)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, 200, favs)
}

func (s *Server) handleFavCreate(w http.ResponseWriter, r *http.Request, _ store.User) {
	var in struct {
		Mode   string `json:"mode"`
		Target string `json:"target"`
		Label  string `json:"label"`
		Group  string `json:"group"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	m, ok := s.favMode(w, in.Mode, false)
	if !ok {
		return
	}
	target, msg := cleanTarget(m, in.Target)
	if msg != "" {
		writeErr(w, 400, "bad_target", msg)
		return
	}
	label, ok1 := cleanText(in.Label, maxLabel)
	group, ok2 := cleanText(in.Group, maxGroup)
	if !ok1 || !ok2 {
		writeErr(w, 400, "bad_request", "label or group is too long or has control characters")
		return
	}
	if label == "" {
		label = s.defaultLabel(m, target)
	}
	f, err := s.d.Store.AddFavorite(m, target, label, group)
	if err != nil {
		s.favErr(w, err)
		return
	}
	s.broadcastFavorites()
	writeJSON(w, 201, s.favJSON(f))
}

func (s *Server) handleFavUpdate(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Target *string `json:"target"`
		Label  *string `json:"label"`
		Group  *string `json:"group"`
		Pinned *bool   `json:"pinned"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	cur, err := s.d.Store.GetFavorite(id)
	if err != nil {
		s.favErr(w, err)
		return
	}
	var target, label, group *string
	if in.Target != nil {
		t, msg := cleanTarget(cur.Mode, *in.Target)
		if msg != "" {
			writeErr(w, 400, "bad_target", msg)
			return
		}
		target = &t
	}
	if in.Label != nil {
		l, ok := cleanText(*in.Label, maxLabel)
		if !ok {
			writeErr(w, 400, "bad_request", "label is too long or has control characters")
			return
		}
		if l == "" {
			t := cur.Target
			if target != nil {
				t = *target
			}
			l = s.defaultLabel(cur.Mode, t)
		}
		label = &l
	}
	if in.Group != nil {
		g, ok := cleanText(*in.Group, maxGroup)
		if !ok {
			writeErr(w, 400, "bad_request", "group is too long or has control characters")
			return
		}
		group = &g
	}
	f, err := s.d.Store.UpdateFavorite(id, target, label, group, in.Pinned)
	if err != nil {
		s.favErr(w, err)
		return
	}
	s.broadcastFavorites()
	writeJSON(w, 200, s.favJSON(f))
}

func (s *Server) handleFavDelete(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.d.Store.DeleteFavorite(id); err != nil {
		s.favErr(w, err)
		return
	}
	s.broadcastFavorites()
	noContent(w)
}

// handleFavReorder serves POST /api/favorites/reorder {mode, ids}: ids is
// every favorite of the mode in the new order.
func (s *Server) handleFavReorder(w http.ResponseWriter, r *http.Request, _ store.User) {
	var in struct {
		Mode string  `json:"mode"`
		IDs  []int64 `json:"ids"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	m, ok := s.favMode(w, in.Mode, false)
	if !ok {
		return
	}
	if err := s.d.Store.ReorderFavorites(m, in.IDs); err != nil {
		s.favErr(w, err)
		return
	}
	s.broadcastFavorites()
	noContent(w)
}
