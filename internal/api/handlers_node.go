package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/hostlists"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/nodelist"
	"github.com/w1jar/nodespot/internal/store"
)

// The node (docs/NATIVE_NODE.md): its status, its modes and their
// settings, the station settings, the one link of the active mode, the last
// heard list and the radio. Everything goes to the node's control socket;
// the status is served from the copy node_watch.go keeps.

var reModeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,23}$`)

func (s *Server) nodeRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/node", s.authed(store.RoleViewer, s.handleNodeGet))
	m.HandleFunc("POST /api/node/reload", s.authed(store.RoleAdmin, s.handleNodeReload))
	m.HandleFunc("GET /api/modes", s.authed(store.RoleViewer, s.handleModesGet))
	m.HandleFunc("PUT /api/modes/{mode}", s.authed(store.RoleAdmin, s.handleModePut))
	m.HandleFunc("DELETE /api/modes/{mode}", s.authed(store.RoleAdmin, s.handleModeDelete))
	m.HandleFunc("GET /api/station", s.authed(store.RoleAdmin, s.handleStationGet))
	m.HandleFunc("PUT /api/station", s.authed(store.RoleAdmin, s.handleStationPut))
	m.HandleFunc("POST /api/select", s.authed(store.RoleOperator, s.handleSelect))
	m.HandleFunc("POST /api/connect", s.authed(store.RoleOperator, s.handleConnect))
	m.HandleFunc("POST /api/disconnect", s.authed(store.RoleOperator, s.handleDisconnect))
	m.HandleFunc("GET /api/heard", s.authed(store.RoleViewer, s.handleHeard))
	m.HandleFunc("GET /api/radio", s.authed(store.RoleAdmin, s.handleRadioGet))
	m.HandleFunc("GET /api/radio/input", s.authed(store.RoleAdmin, s.handleRadioInput))
	m.HandleFunc("PUT /api/radio", s.authed(store.RoleAdmin, s.handleRadioPut))
	m.HandleFunc("GET /api/radio/info", s.authed(store.RoleAdmin, s.handleRadioInfo))
	m.HandleFunc("POST /api/radio/program", s.authed(store.RoleAdmin, s.handleRadioProgram))
	m.HandleFunc("GET /api/radio/autoset", s.authed(store.RoleAdmin, s.handleAutosetGet))
	m.HandleFunc("POST /api/radio/autoset", s.authed(store.RoleAdmin, s.handleAutosetStart))
	m.HandleFunc("DELETE /api/radio/autoset", s.authed(store.RoleAdmin, s.handleAutosetCancel))
	m.HandleFunc("POST /api/radio/tone", s.authed(store.RoleAdmin, s.handleTestTone))
	m.HandleFunc("DELETE /api/radio/tone", s.authed(store.RoleAdmin, s.handleTestToneStop))
	m.HandleFunc("GET /api/hostlists", s.authed(store.RoleOperator, s.handleHostLists))
	m.HandleFunc("GET /api/hostlists/status", s.authed(store.RoleOperator, s.handleHostListsStatus))
	m.HandleFunc("POST /api/hostlists/refresh", s.authed(store.RoleAdmin, s.handleHostListsRefresh))
}

// nodeCtx bounds one node call made for a request.
func nodeCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 20*time.Second)
}

// nodeReady answers 503 when there is no node client.
func (s *Server) nodeReady(w http.ResponseWriter) bool {
	if s.d.Node == nil {
		writeErr(w, http.StatusServiceUnavailable, "node_unavailable", "the node is not available")
		return false
	}
	return true
}

// nodeErr answers a failed node call: 503 when the node cannot be reached,
// 409 with the node's own message when it refused, 502 otherwise.
func (s *Server) nodeErr(w http.ResponseWriter, err error) {
	var re *node.RemoteError
	switch {
	case errors.As(err, &re):
		writeErr(w, http.StatusConflict, "node_refused", re.Msg)
	case errors.Is(err, node.ErrUnavailable), errors.Is(err, errNoNode),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeErr(w, http.StatusServiceUnavailable, "node_unavailable", "the node is not running")
	default:
		s.d.Logger.Printf("node: %v", err)
		writeErr(w, http.StatusBadGateway, "node_error", "the node sent a reply that could not be read")
	}
}

// handleNodeGet serves the cached status.
func (s *Server) handleNodeGet(w http.ResponseWriter, _ *http.Request, _ store.User) {
	writeJSON(w, 200, s.nw.json())
}

func (s *Server) handleNodeReload(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.Reload(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "node.reload", nil)
	s.adopt(st)
	s.refreshModes(r.Context())
	writeJSON(w, 200, s.nw.json())
}

// handleModesGet lists the modes. Viewers and operators get what the Modes
// view needs; the settings (values, reflectors) are for admins only.
func (s *Server) handleModesGet(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.Modes(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.nw.setModes(l)
	writeJSON(w, 200, s.modesJSON(l, u.Role == store.RoleAdmin))
}

func (s *Server) modesJSON(l []node.ModeInfo, admin bool) []node.ModeInfo {
	if l == nil {
		l = []node.ModeInfo{}
	}
	if admin {
		return l
	}
	out := make([]node.ModeInfo, len(l))
	for i, m := range l {
		m.Fields, m.Settings, m.SecretsSet = nil, mode.Settings{}, nil
		out[i] = m
	}
	return out
}

func (s *Server) pathMode(w http.ResponseWriter, r *http.Request) (string, bool) {
	m := strings.ToLower(r.PathValue("mode"))
	if !reModeName.MatchString(m) {
		writeErr(w, 400, "bad_mode", "invalid mode name")
		return "", false
	}
	return m, true
}

// handleModePut merges the body (mode.Settings: values, favorites,
// reflectors) into a mode's saved settings. A blank or absent secret keeps
// the saved one; secrets are never sent back.
func (s *Server) handleModePut(w http.ResponseWriter, r *http.Request, u store.User) {
	m, ok := s.pathMode(w, r)
	if !ok || !s.nodeReady(w) {
		return
	}
	var in mode.Settings
	if !decode(w, r, &in, false) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.SaveMode(ctx, m, in)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	keys := make([]string, 0, len(in.Values))
	for k := range in.Values {
		keys = append(keys, k)
	}
	s.audit(u, "mode.save", map[string]any{"mode": m, "keys": keys})
	s.nw.setModes(l)
	s.nodeChanged()
	writeJSON(w, 200, s.modesJSON(l, true))
}

func (s *Server) handleModeDelete(w http.ResponseWriter, r *http.Request, u store.User) {
	m, ok := s.pathMode(w, r)
	if !ok || !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	if err := s.d.Node.DeleteMode(ctx, m); err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "mode.delete", map[string]any{"mode": m})
	s.refreshModes(r.Context())
	s.nodeChanged()
	noContent(w)
}

func (s *Server) handleStationGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.Station(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

// handleStationPut replaces the station settings (callsign, radio, ID,
// advanced); the active selection is the node's own. It answers the new
// node status.
func (s *Server) handleStationPut(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	var in node.Station
	if !decode(w, r, &in, false) {
		return
	}
	in.Callsign = strings.ToUpper(strings.TrimSpace(in.Callsign))
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.SaveStation(ctx, in)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "station.save", map[string]any{"callsign": in.Callsign})
	s.adopt(st)
	writeJSON(w, 200, s.nw.json())
}

// targetInput is the target of POST /api/select and /api/connect: an ID
// (node number, talkgroup, reflector) and, for modes that need one, an
// address. A missing address is filled from the host lists.
type targetInput struct {
	Target  string `json:"target"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Slot    int    `json:"slot"`
	Private bool   `json:"private"`
}

func (s *Server) target(m string, in targetInput) (mode.Target, string) {
	id, ok := cleanText(in.Target, maxTarget)
	if !ok || id == "" {
		return mode.Target{}, "target must be 1 to 64 characters without control characters"
	}
	host, ok := cleanText(in.Host, 253)
	if !ok || strings.ContainsAny(host, " /") {
		return mode.Target{}, "host is not a host name"
	}
	if in.Port < 0 || in.Port > 65535 {
		return mode.Target{}, "port must be 1 to 65535"
	}
	if in.Slot < 0 || in.Slot > 2 {
		return mode.Target{}, "slot must be 1 or 2"
	}
	name, _ := cleanText(in.Name, maxLabel)
	t := mode.Target{ID: id, Name: name, Host: host, Port: in.Port, Slot: in.Slot, Private: in.Private}
	if t.Host == "" {
		r := s.resolveTarget(m, id)
		t.ID, t.Host, t.Port = r.ID, r.Host, r.Port
		if t.Name == "" {
			t.Name = r.Name
		}
	}
	return t, ""
}

// resolveTarget turns a picked value into a target, with the reflector's
// address and name from the host lists when they know it.
func (s *Server) resolveTarget(m, value string) mode.Target {
	t := mode.Target{ID: value}
	if s.d.HostLists == nil || !hostlists.ValidMode(m) {
		return t
	}
	if r, ok := s.d.HostLists.Resolve(m, value); ok {
		if r.Room != "" {
			t.ID = r.Room
		}
		t.Name, t.Host, t.Port = r.Name, r.Host, r.Port
	}
	return t
}

// handleSelect makes a mode the active one, optionally connecting it:
// {mode, target?, host?, port?, slot?, private?}. mode "" makes the node
// idle. It answers the node status.
func (s *Server) handleSelect(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	var in struct {
		Mode string `json:"mode"`
		targetInput
	}
	if !decode(w, r, &in, false) {
		return
	}
	m := strings.ToLower(strings.TrimSpace(in.Mode))
	if m != "" && !reModeName.MatchString(m) {
		writeErr(w, 400, "bad_mode", "invalid mode name")
		return
	}
	var t *mode.Target
	if strings.TrimSpace(in.Target) != "" {
		if m == "" {
			writeErr(w, 400, "bad_request", "a target needs a mode")
			return
		}
		tt, msg := s.target(m, in.targetInput)
		if msg != "" {
			writeErr(w, 400, "bad_target", msg)
			return
		}
		t = &tt
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.Select(ctx, m, t)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	detail := map[string]any{"mode": m}
	if t != nil {
		detail["target"] = t.ID
	}
	s.audit(u, "node.select", detail)
	s.adopt(st)
	s.manualSchedules(m, "select by "+u.Username)
	writeJSON(w, 200, s.nw.json())
}

// handleConnect links the active mode to a target.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	var in targetInput
	if !decode(w, r, &in, false) {
		return
	}
	cur, _ := s.nw.status()
	t, msg := s.target(cur.Active, in)
	if msg != "" {
		writeErr(w, 400, "bad_target", msg)
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.Connect(ctx, t)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "node.connect", map[string]any{"mode": st.Active, "target": t.ID})
	s.adopt(st)
	s.manualSchedules(st.Active, "connect by "+u.Username)
	writeJSON(w, 200, s.nw.json())
}

// handleDisconnect drops the active mode's link (the mode stays active).
func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.Disconnect(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "node.disconnect", map[string]any{"mode": st.Active})
	s.adopt(st)
	s.manualSchedules(st.Active, "disconnect by "+u.Username)
	writeJSON(w, 200, s.nw.json())
}

// handleHeard serves the node's last heard list across modes, newest first
// (?limit=, default 50, at most 500).
func (s *Server) handleHeard(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, 400, "bad_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.LastHeard(ctx, limit)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	if l == nil {
		l = []mode.Heard{}
	}
	writeJSON(w, 200, l)
}

func (s *Server) handleRadioGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.RadioLevels(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, l)
}

// handleRadioInput answers the radio's live receive level, {peakDb, rmsDb,
// cos} over the last 100 ms. It is small and cheap for the Radio tune page
// to poll a few times a second. (The same level is in GET /api/node as
// radio.level, as fresh as that status.)
func (s *Server) handleRadioInput(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.InputLevel(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, l)
}

// handleRadioPut sets the sound chip's levels {rxVolume, txVolume} (0 to 1)
// and receive boost {rxBoost} (an absent one is kept); the node saves them.
func (s *Server) handleRadioPut(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	var in struct {
		RxVolume *float64 `json:"rxVolume"`
		TxVolume *float64 `json:"txVolume"`
		RxBoost  *bool    `json:"rxBoost"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	for _, v := range []*float64{in.RxVolume, in.TxVolume} {
		if v != nil && (*v < 0 || *v > 1) {
			writeErr(w, 400, "bad_request", "levels must be 0 to 1")
			return
		}
	}
	if in.RxVolume == nil && in.TxVolume == nil && in.RxBoost == nil {
		writeErr(w, 400, "bad_request", "set rxVolume, txVolume or rxBoost")
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	l, err := s.d.Node.RadioTune(ctx, in.RxVolume, in.TxVolume, in.RxBoost)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "radio.tune", map[string]any{"rxVolume": in.RxVolume, "txVolume": in.TxVolume, "rxBoost": in.RxBoost})
	writeJSON(w, 200, l)
}

// handleRadioInfo describes the radio interface: the detected chip and USB
// id, the settings in use, the configured preset and the presets to pick.
func (s *Server) handleRadioInfo(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	d, err := s.d.Node.RadioInfo(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

// handleRadioProgram writes the saved channel (station radio settings) to
// the SA818.
func (s *Server) handleRadioProgram(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.RadioProgram(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "radio.program", nil)
	s.adopt(st)
	writeJSON(w, 200, s.nw.json())
}

// handleAutosetGet answers the state of the receive auto-set.
func (s *Server) handleAutosetGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.AutosetStatus(ctx)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

// handleAutosetStart starts the receive auto-set and answers its state
// ({state:"waiting"}); the page polls GET until it is done or failed. The
// node refuses (409) with no radio, a sound chip without a receive level
// control, or a run already going.
func (s *Server) handleAutosetStart(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.AutosetRx(ctx, false)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.audit(u, "radio.autoset", nil)
	writeJSON(w, 200, st)
}

// handleAutosetCancel stops a running receive auto-set (none is not an error).
func (s *Server) handleAutosetCancel(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.AutosetRx(ctx, true)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

// handleTestTone keys the radio with the 1 kHz test tone {seconds?} (1 to
// 10, default 5). The node refuses it (409) while it transmits, while the
// radio receives and while the active mode is talking. It answers the node
// status, whose radio.testTone is true while the tone plays.
func (s *Server) handleTestTone(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.nodeReady(w) {
		return
	}
	var in struct {
		Seconds float64 `json:"seconds"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	if in.Seconds != 0 && (in.Seconds < 1 || in.Seconds > node.MaxTestTone.Seconds()) {
		writeErr(w, 400, "bad_request", "seconds must be 1 to 10")
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.TestTone(ctx, in.Seconds, false)
	if err != nil {
		s.nodeErr(w, err) // a refusal is 409 node_refused with the node's reason
		return
	}
	s.audit(u, "radio.test_tone", map[string]any{"seconds": in.Seconds})
	s.adopt(st)
	writeJSON(w, 200, s.nw.json())
}

// handleTestToneStop ends the test tone early (no error when none plays).
func (s *Server) handleTestToneStop(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.nodeReady(w) {
		return
	}
	ctx, cancel := nodeCtx(r)
	defer cancel()
	st, err := s.d.Node.TestTone(ctx, 0, true)
	if err != nil {
		s.nodeErr(w, err)
		return
	}
	s.adopt(st)
	writeJSON(w, 200, s.nw.json())
}

// handleHostLists serves a host list for the pickers: ?mode=dmr, ysf, p25,
// nxdn, m17, bm (the BrandMeister masters of the DMR list) or dmr-other
// (the rest of it, without BrandMeister and TGIF).
func (s *Server) handleHostLists(w http.ResponseWriter, r *http.Request, _ store.User) {
	m := strings.ToLower(r.URL.Query().Get("mode"))
	bm, other := m == "bm", m == "dmr-other"
	if bm || other {
		m = "dmr"
	}
	if !hostlists.ValidMode(m) {
		writeErr(w, http.StatusBadRequest, "bad_request", "mode must be dmr, ysf, p25, nxdn, m17, bm or dmr-other")
		return
	}
	if s.d.HostLists == nil {
		writeErr(w, http.StatusServiceUnavailable, "hostlists_unavailable", "the host lists are not available")
		return
	}
	l, err := s.d.HostLists.Get(m)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "hostlists_unavailable", err.Error())
		return
	}
	if bm {
		l = hostlists.List{Mode: "bm", Items: hostlists.BMMasters(l.Items), ETag: l.ETag}
		m = "bm"
	}
	if other {
		l = hostlists.List{Mode: "dmr-other", Items: hostlists.OtherDMRMasters(l.Items), ETag: l.ETag}
		m = "dmr-other"
	}
	tag := `"` + m + "-" + l.ETag + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, max-age=300")
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, 200, l)
}

// hostListsStatus is the state of every downloaded list: the Pi-Star host
// lists (lists, busy, nextManual) and the AllStar node list (nodelist).
type hostListsStatus struct {
	hostlists.Status
	Nodelist *nodelist.Status `json:"nodelist,omitempty"`
}

func (s *Server) hostListsStatus() hostListsStatus {
	var out hostListsStatus
	if s.d.HostLists != nil {
		out.Status = s.d.HostLists.Status()
	} else {
		out.Lists = map[string]hostlists.ListStatus{}
	}
	if s.d.Nodelist != nil {
		ns := s.d.Nodelist.Status()
		out.Nodelist = &ns
	}
	return out
}

// handleHostListsStatus reports the count, update time and last error of
// every list (GET /api/hostlists/status; the AllStar node list is
// "nodelist").
func (s *Server) handleHostListsStatus(w http.ResponseWriter, _ *http.Request, _ store.User) {
	writeJSON(w, 200, s.hostListsStatus())
}

// handleHostListsRefresh fetches the host lists and the AllStar node list now.
// The host lists allow it at most once every ten minutes; a refusal is 409
// with the reason and nothing is fetched. A download that fails is not a
// refusal: the answer is 200 with the error in that list's status.
func (s *Server) handleHostListsRefresh(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.d.HostLists == nil {
		writeErr(w, http.StatusServiceUnavailable, "hostlists_unavailable", "the host lists are not available")
		return
	}
	err := s.d.HostLists.Refresh(r.Context())
	if errors.Is(err, hostlists.ErrBusy) || errors.Is(err, hostlists.ErrTooSoon) || errors.Is(err, hostlists.ErrDisabled) {
		s.audit(u, "hostlists.refresh.refused", map[string]any{"error": err.Error()})
		writeErr(w, http.StatusConflict, "hostlists_busy", err.Error())
		return
	}
	detail := map[string]any{}
	if err != nil {
		detail["error"] = err.Error()
	}
	if s.d.Nodelist != nil {
		if nerr := s.d.Nodelist.Refresh(r.Context()); nerr != nil {
			detail["nodelistError"] = nerr.Error()
		}
	}
	s.audit(u, "hostlists.refresh", detail)
	writeJSON(w, 200, s.hostListsStatus())
}
