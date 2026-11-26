package api

import (
	"github.com/w1jar/nodespot/internal/nodelist"
	"github.com/w1jar/nodespot/internal/store"
)

// UserJSON is the API User object.
type UserJSON struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func userJSON(u store.User) UserJSON { return UserJSON{ID: u.ID, Username: u.Username, Role: u.Role} }

// FavoriteJSON is the API Favorite object: a saved target of one mode.
type FavoriteJSON struct {
	ID       int64  `json:"id"`
	Mode     string `json:"mode"`
	Target   string `json:"target"`
	Label    string `json:"label"`
	Group    string `json:"group"`
	Position int    `json:"position"`
	Pinned   bool   `json:"pinned"`
	// Info is the AllStar node list entry (allstar favorites only).
	Info *nodelist.Info `json:"info"`
}

func (s *Server) info(node string) *nodelist.Info {
	if s.d.Nodelist == nil {
		return nil
	}
	if in, ok := s.d.Nodelist.Lookup(node); ok {
		return &in
	}
	return nil
}

func (s *Server) favJSON(f store.Favorite) FavoriteJSON {
	out := FavoriteJSON{ID: f.ID, Mode: f.Mode, Target: f.Target, Label: f.Label, Group: f.Group, Position: f.Position, Pinned: f.Pinned}
	if f.Mode == modeAllStar {
		out.Info = s.info(f.Target)
	}
	return out
}

// favoritesJSON lists the favorites of mode ("" for every mode).
func (s *Server) favoritesJSON(mode string) ([]FavoriteJSON, error) {
	fs, err := s.d.Store.ListFavorites(mode)
	if err != nil {
		return nil, err
	}
	out := make([]FavoriteJSON, len(fs))
	for i, f := range fs {
		out[i] = s.favJSON(f)
	}
	return out, nil
}
