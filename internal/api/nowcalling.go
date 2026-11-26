package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/nowcalling"
	"github.com/w1jar/nodespot/internal/radioid"
	"github.com/w1jar/nodespot/internal/store"
)

// "Now calling" (docs/API.md, "Now calling"): who is talking right now on
// the active mode and the last few talkers, from the node's talk events
// (node_watch.go feeds them). internal/nowcalling folds them; this file
// fills in names and places and sends the snapshot as the SSE event
// "nowcalling" and GET /api/nowcalling.
//
// Callsigns and names are personal data heard on air. They go to every
// logged-in role over the event stream and the REST call; the public status
// view shows them only when the admin turned that on.

type ncState struct {
	tr *nowcalling.Tracker
}

func (s *Server) initNowCalling() {
	n := &ncState{tr: nowcalling.New()}
	n.tr.Resolve = s.ncResolve
	n.tr.OnChange = func(sn nowcalling.Snapshot) {
		f := frame("nowcalling", sn)
		s.hub.broadcast(sseMsg{priv: f, pub: f})
		s.pub.Changed()
	}
	s.nc = n
}

// ncResolve fills the details of a new call. An AllStar call gets its
// callsign, description and location from the node list and nothing else:
// the radioid.net list is never consulted for it (a node's callsign is not
// a person). Every other caller gets name and place from the radioid.net
// list, by DMR ID when the network sent one, else by callsign, and then
// from the cache of hamdb.org. A talkgroup gets its name from the
// favorites of the mode.
func (s *Server) ncResolve(c *nowcalling.Call) {
	if c.Kind == mode.AllStar {
		if c.Node != "" && s.d.Nodelist != nil {
			if in, ok := s.d.Nodelist.Lookup(c.Node); ok {
				c.Call = firstWord(in.Callsign)
				c.Desc, c.Location = in.Description, in.Location
			}
		}
		return
	}
	c.TargetName = s.ncTargetName(c.Kind, c.Target)
	if c.Kind == mode.EchoTest {
		return
	}
	var u radioid.User
	var ok bool
	if s.d.RadioID != nil {
		if c.DMRID != "" {
			u, ok = s.d.RadioID.ByID(parseID(c.DMRID))
		}
		if !ok && c.Call != "" {
			u, ok = s.d.RadioID.ByCallsign(baseCall(c.Call))
		}
	}
	if !ok {
		s.ncHamDB(c)
		return
	}
	if c.Call == "" {
		c.Call = u.Callsign
	}
	c.First, c.Name, c.City, c.State, c.Country = u.First, u.Name(), u.City, u.State, u.Country
	c.Location = ""
}

// baseCall strips an EchoLink suffix ("-L", "-R") and a portable or SSID
// part ("/P", "-7") from a callsign for a list lookup.
func baseCall(call string) string {
	if i := strings.IndexAny(call, "-/ "); i > 0 {
		return call[:i]
	}
	return call
}

// ncHamDB fills the person of a callsign-only call from the cache of
// hamdb.org when the radioid.net list had no entry. It never waits: a
// callsign that is not cached is queued and found by hamdbChanged later.
// Reports whether the call changed.
func (s *Server) ncHamDB(c *nowcalling.Call) bool {
	if s.d.HamDB == nil || c.Call == "" || c.Kind == mode.AllStar || c.Kind == mode.EchoTest {
		return false
	}
	in, ok := s.d.HamDB.Lookup(baseCall(c.Call))
	if !ok {
		return false
	}
	c.First, c.Name, c.City, c.State, c.Country, c.Source = in.First, in.Name, in.City, in.State, in.Country, "hamdb.org"
	c.Location = ""
	return true
}

// hamdbChanged is called when hamdb.org answered for a new callsign: the
// strip and the recent calls pick the name up.
func (s *Server) hamdbChanged() {
	if s.nc != nil && s.nc.tr != nil {
		s.nc.tr.Refill(s.ncHamDB)
	}
}

// callerPerson is the name and "City, ST" of a callsign-only caller: the
// radioid.net list first, then the cache of hamdb.org ("" when neither knows).
func (s *Server) callerPerson(call string) (name, place string) {
	if s.d.RadioID != nil {
		if u, ok := s.d.RadioID.ByCallsign(call); ok {
			return u.Name(), joinPlace(u.City, u.State)
		}
	}
	if s.d.HamDB != nil {
		if in, ok := s.d.HamDB.Lookup(call); ok {
			return in.Name, in.Place()
		}
	}
	return "", ""
}

func joinPlace(city, state string) string {
	switch {
	case city != "" && state != "":
		return city + ", " + state
	case city != "":
		return city
	}
	return state
}

// ncTargetName is the label of the favorite of mode m whose target is t
// ("" when there is none).
func (s *Server) ncTargetName(m, t string) string {
	if t == "" || s.d.Store == nil {
		return ""
	}
	favs, err := s.d.Store.ListFavorites(m)
	if err != nil {
		return ""
	}
	for _, f := range favs {
		if strings.EqualFold(f.Target, t) {
			return f.Label
		}
	}
	return ""
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func parseID(s string) uint32 {
	var n uint64
	for _, r := range s {
		if r < '0' || r > '9' || n > 1<<32 {
			return 0
		}
		n = n*10 + uint64(r-'0')
	}
	return uint32(n)
}

func (s *Server) nowCallingRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/nowcalling", s.authed(store.RoleViewer, s.handleNowCalling))
	m.HandleFunc("GET /api/dv/userlist", s.authed(store.RoleAdmin, s.handleUserListGet))
	m.HandleFunc("PUT /api/dv/userlist", s.authed(store.RoleAdmin, s.handleUserListSet))
	m.HandleFunc("GET /api/dv/hamdb", s.authed(store.RoleAdmin, s.handleHamDBGet))
	m.HandleFunc("PUT /api/dv/hamdb", s.authed(store.RoleAdmin, s.handleHamDBSet))
	m.HandleFunc("POST /api/dv/userlist/download", s.authed(store.RoleAdmin, s.handleUserListDownload))
}

func (s *Server) handleNowCalling(w http.ResponseWriter, _ *http.Request, _ store.User) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.nc.tr.Snapshot())
}

func (s *Server) userListUnavailable(w http.ResponseWriter) {
	writeErr(w, http.StatusServiceUnavailable, "userlist_unavailable", "the radioid.net user list is not available on this node")
}

func (s *Server) handleUserListGet(w http.ResponseWriter, _ *http.Request, _ store.User) {
	if s.d.RadioID == nil {
		s.userListUnavailable(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.d.RadioID.Status())
}

func (s *Server) handleUserListSet(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.RadioID == nil {
		s.userListUnavailable(w)
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &b, false) {
		return
	}
	if b.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "enabled must be true or false")
		return
	}
	if err := s.d.RadioID.SetEnabled(*b.Enabled); err != nil {
		s.d.Logger.Printf("radioid: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "could not save the setting")
		return
	}
	s.audit(u, "dv.userlist", map[string]any{"enabled": *b.Enabled})
	if *b.Enabled && s.d.RadioID.Status().Entries == 0 {
		_ = s.d.RadioID.DownloadAsync(context.Background(), false)
	}
	writeJSON(w, 200, s.d.RadioID.Status())
}

func (s *Server) handleUserListDownload(w http.ResponseWriter, _ *http.Request, u store.User) {
	if s.d.RadioID == nil {
		s.userListUnavailable(w)
		return
	}
	switch err := s.d.RadioID.DownloadAsync(context.Background(), true); err {
	case nil:
	case radioid.ErrOff:
		writeErr(w, http.StatusConflict, "userlist_off", err.Error())
		return
	default:
		writeErr(w, http.StatusConflict, "userlist_busy", err.Error())
		return
	}
	s.audit(u, "dv.userlist.download", nil)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, s.d.RadioID.Status())
}

func (s *Server) handleHamDBGet(w http.ResponseWriter, _ *http.Request, _ store.User) {
	if s.d.HamDB == nil {
		writeErr(w, http.StatusServiceUnavailable, "hamdb_unavailable", "the hamdb.org lookup is not available on this node")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.d.HamDB.Status())
}

func (s *Server) handleHamDBSet(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.HamDB == nil {
		writeErr(w, http.StatusServiceUnavailable, "hamdb_unavailable", "the hamdb.org lookup is not available on this node")
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &b, false) {
		return
	}
	if b.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "enabled must be true or false")
		return
	}
	if err := s.d.HamDB.SetEnabled(*b.Enabled); err != nil {
		s.d.Logger.Printf("hamdb: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "could not save the setting")
		return
	}
	s.audit(u, "dv.hamdb", map[string]any{"enabled": *b.Enabled})
	writeJSON(w, 200, s.d.HamDB.Status())
}
