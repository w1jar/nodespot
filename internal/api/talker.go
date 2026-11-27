package api

import (
	"github.com/w1jar/nodespot/internal/hamdb"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// The talker of the node status (GET /api/node, the SSE event "node") and
// the rows of the last-heard list (GET /api/heard) carry only what the
// network sent: a BrandMeister call often has a DMR ID and nothing else.
// The API fills in the callsign, name and place from the same lists as "Now
// calling" (nowcalling.go, ncResolve): the radioid.net list by DMR ID, then
// by callsign, then the cache of hamdb.org. Every lookup is local and never
// waits: the radioid.net index keeps a small cache of the IDs it read, and
// a callsign hamdb.org has not answered yet is only queued. Only empty
// fields are filled. AllStar (a node's callsign is not a person; a bare node
// number is resolved through the node list instead) and the
// echo test (the station itself) are left as they are.

// person is who a DMR ID or callsign belongs to.
type person struct {
	Callsign, Name, Place string
}

// placeText is "City, ST" for a US caller and "City, State, Country"
// otherwise, the rule of hamdb.Info.Place.
func placeText(city, state, country string) string {
	return hamdb.Info{City: city, State: state, Country: country}.Place()
}

// lookupPerson finds the person behind id or call ("" and 0 skip a key).
func (s *Server) lookupPerson(id uint32, call string) (person, bool) {
	call = baseCall(call)
	if s.d.RadioID != nil {
		if id != 0 {
			if u, ok := s.d.RadioID.ByID(id); ok {
				return person{u.Callsign, u.Name(), placeText(u.City, u.State, u.Country)}, true
			}
		}
		if call != "" {
			if u, ok := s.d.RadioID.ByCallsign(call); ok {
				return person{u.Callsign, u.Name(), placeText(u.City, u.State, u.Country)}, true
			}
		}
	}
	if s.d.HamDB != nil && call != "" {
		if in, ok := s.d.HamDB.Lookup(call); ok {
			return person{call, in.Name, in.Place()}, true
		}
	}
	return person{}, false
}

// enrichable reports whether calls on mode m are looked up.
func enrichable(m string) bool { return m != "" && m != mode.AllStar && m != mode.EchoTest }

// allstarNode resolves an AllStar call that carries only a node number (all
// digits) through the node list, the same way "Now calling" does (ncResolve):
// the node's callsign and its description, which the web shows as the second
// line. A call that is not a bare number, or not in the list, is left alone.
func (s *Server) allstarNode(call string) (callsign, desc string, ok bool) {
	if call == "" || s.d.Nodelist == nil {
		return "", "", false
	}
	for _, r := range call {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	in, found := s.d.Nodelist.Lookup(call)
	if !found {
		return "", "", false
	}
	return firstWord(in.Callsign), in.Description, true
}

// fill sets the empty ones of callsign, name and place from p.
func (p person) fill(callsign, name, place *string) {
	if *callsign == "" {
		*callsign = p.Callsign
	}
	if *name == "" {
		*name = p.Name
	}
	if *place == "" {
		*place = p.Place
	}
}

// enrichTalker returns t with its empty fields filled (t itself when there
// is nothing to look up or nothing found; t is never modified).
func (s *Server) enrichTalker(m string, t *mode.Talker) *mode.Talker {
	if m == mode.AllStar && t != nil {
		if cs, name, ok := s.allstarNode(t.Callsign); ok {
			c := *t
			c.Callsign, c.Name, c.Place = cs, name, ""
			return &c
		}
		return t
	}
	if t == nil || !enrichable(m) || (t.Callsign != "" && t.Name != "" && t.Place != "") || (t.ID == 0 && t.Callsign == "") {
		return t
	}
	p, ok := s.lookupPerson(t.ID, t.Callsign)
	if !ok {
		return t
	}
	c := *t
	p.fill(&c.Callsign, &c.Name, &c.Place)
	return &c
}

// enrichStatus returns st with the talker of its mode filled in. The mode
// status is copied, never changed in place: st shares it with the cache.
func (s *Server) enrichStatus(st *mode.Status) *mode.Status {
	if st == nil || st.Talker == nil {
		return st
	}
	t := s.enrichTalker(st.Mode, st.Talker)
	if t == st.Talker {
		return st
	}
	c := *st
	c.Talker = t
	return &c
}

// enrichHeard fills the empty callsign, name and place of the rows of the
// last-heard list (l is changed in place; it is the API's own copy).
func (s *Server) enrichHeard(l []mode.Heard) {
	for i := range l {
		h := &l[i]
		if h.Mode == mode.AllStar {
			if cs, name, ok := s.allstarNode(h.Callsign); ok {
				h.Callsign, h.Name, h.Place = cs, name, ""
			}
			continue
		}
		if !enrichable(h.Mode) || (h.Callsign != "" && h.Name != "" && h.Place != "") || (h.ID == 0 && h.Callsign == "") {
			continue
		}
		if p, ok := s.lookupPerson(h.ID, h.Callsign); ok {
			p.fill(&h.Callsign, &h.Name, &h.Place)
		}
	}
}

// nodeJSON is the cached node status as the API serves and streams it, the
// talker filled in.
func (s *Server) nodeJSON() NodeJSON {
	out := s.nw.json()
	out.Status.Mode = s.enrichStatus(out.Status.Mode)
	return out
}
