package api

import (
	"net/http"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/nowcalling"
	"github.com/w1jar/nodespot/internal/public"
	"github.com/w1jar/nodespot/internal/store"
)

// PublicSettingsJSON is the API object of GET/PUT /api/public (admin).
type PublicSettingsJSON struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
	// ShowCallsigns shows who is talking; ShowHeard the last heard calls.
	ShowCallsigns  bool   `json:"showCallsigns"`
	ShowHeard      bool   `json:"showHeard"`
	AllowEmbedding bool   `json:"allowEmbedding"`
	TrustProxy     bool   `json:"trustProxy"`
	DefaultTitle   string `json:"defaultTitle"`
	Clients        int    `json:"clients"`
}

func (s *Server) publicJSON() PublicSettingsJSON {
	c := s.pub.Settings()
	out := PublicSettingsJSON{
		Enabled: c.Enabled, Title: c.Title, ShowCallsigns: c.ShowCallsigns, ShowHeard: c.ShowHeard,
		AllowEmbedding: c.AllowEmbedding, TrustProxy: c.TrustProxy,
		DefaultTitle: "Node status", Clients: s.pub.Clients(),
	}
	if st, _ := s.nw.status(); st.Callsign != "" {
		out.DefaultTitle = st.Callsign + " node status"
	}
	return out
}

func (s *Server) handlePublicGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	writeJSON(w, 200, s.publicJSON())
}

func (s *Server) handlePublicPut(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct {
		Enabled        *bool   `json:"enabled"`
		Title          *string `json:"title"`
		ShowCallsigns  *bool   `json:"showCallsigns"`
		ShowHeard      *bool   `json:"showHeard"`
		AllowEmbedding *bool   `json:"allowEmbedding"`
		TrustProxy     *bool   `json:"trustProxy"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	c := s.pub.Settings()
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	set(&c.Enabled, in.Enabled)
	set(&c.ShowCallsigns, in.ShowCallsigns)
	set(&c.ShowHeard, in.ShowHeard)
	set(&c.AllowEmbedding, in.AllowEmbedding)
	set(&c.TrustProxy, in.TrustProxy)
	if in.Title != nil {
		c.Title = *in.Title
	}
	if err := s.pub.Update(c); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	s.d.Logger.Printf("public page settings changed by %s (enabled=%v)", u.Username, c.Enabled)
	writeJSON(w, 200, s.publicJSON())
}

// publicInput is what the public view is built from: the cached node
// status and the "Now calling" tracker. The public package filters it.
func (s *Server) publicInput() public.Input {
	st, running := s.nw.status()
	in := public.Input{Callsign: st.Callsign}
	if running {
		in.Mode = st.Active
		in.TxKeyed, in.RxKeyed = st.Radio.PTT, st.Radio.COS
		if st.Mode != nil {
			in.State = string(st.Mode.State)
			if st.Mode.Target != nil && st.Mode.State == mode.StateConnected {
				in.Target = st.Mode.Target.ID
			}
		}
	} else {
		in.State = "offline"
	}
	sn := s.nc.tr.Snapshot()
	if sn.Current != nil {
		c := callIn(*sn.Current)
		in.Calling = &c
	}
	for _, c := range sn.Recent {
		in.Heard = append(in.Heard, callIn(c))
	}
	return in
}

func callIn(c nowcalling.Call) public.CallIn {
	out := public.CallIn{Mode: c.Kind, Call: c.Call, Node: c.Node, Target: c.Target, Name: c.Name, Place: joinPlace(c.City, c.State)}
	if out.Place == "" {
		out.Place = c.Location
	}
	out.Start, _ = time.Parse(time.RFC3339, c.Start)
	if c.End != "" {
		out.End, _ = time.Parse(time.RFC3339, c.End)
	}
	return out
}
