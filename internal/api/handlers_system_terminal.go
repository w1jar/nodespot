package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// TerminalOpener is implemented by the helper client (and by the demo
// helper) next to SystemHost: the API finds it by type assertion on
// Deps.SysHost, so a helper without it answers 503. The streams carry the
// frames of helper/terminal_frame.go. Closing a stream only detaches the
// session (it keeps running in its own unit); a FrameEnd ends it.
type TerminalOpener interface {
	// OpenTerminal starts a session for the nodespot user owner and returns its
	// id and stream.
	OpenTerminal(ctx context.Context, rows, cols uint16, owner int64) (io.ReadWriteCloser, string, error)
	// AttachTerminal reattaches to a detached session of owner: the recent
	// output is replayed, then it goes live.
	AttachTerminal(ctx context.Context, id string, owner int64, rows, cols uint16) (io.ReadWriteCloser, error)
	// ListTerminals lists the live sessions and the ended ones nobody has
	// acknowledged yet; ack forgets ended records.
	ListTerminals(ctx context.Context, ack []string) (helper.TerminalList, error)
	// EndTerminal ends a session (attached or not).
	EndTerminal(ctx context.Context, id string) error
}

func (s *Server) systemTerminalRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/terminal", s.authed(store.RoleAdmin, s.handleTerminalGet))
	m.HandleFunc("PUT /api/system/terminal", s.authed(store.RoleAdmin, s.handleTerminalPut))
	m.HandleFunc("POST /api/system/terminal/ticket", s.authed(store.RoleAdmin, s.handleTerminalTicket))
	m.HandleFunc("GET /api/system/terminal/ws", s.authed(store.RoleAdmin, s.handleTerminalWS))
	m.HandleFunc("GET /api/system/terminal/sessions", s.authed(store.RoleAdmin, s.handleTerminalSessions))
	m.HandleFunc("POST /api/system/terminal/sessions/{id}/end", s.authed(store.RoleAdmin, s.handleTerminalSessionEnd))
}

// terminalEnabled is the admin switch (default off).
func (s *Server) terminalEnabled() bool {
	if s.d.Store == nil {
		return false
	}
	v, err := s.d.Store.GetSetting(settingTerminal)
	return err == nil && v == "true"
}

// terminalOpener returns the helper's terminal support or answers 503.
func (s *Server) terminalOpener(w http.ResponseWriter) TerminalOpener {
	if s.sysHost(w) == nil {
		return nil
	}
	op, ok := s.d.SysHost.(TerminalOpener)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: the helper has no terminal support")
		return nil
	}
	return op
}

// TerminalStatus is the answer of GET and PUT /api/system/terminal.
type TerminalStatus struct {
	Enabled bool `json:"enabled"`
	// Available: a helper with terminal support is wired in.
	Available   bool `json:"available"`
	Sessions    int  `json:"sessions"`
	MaxSessions int  `json:"maxSessions"`
	IdleMinutes int  `json:"idleMinutes"`
	MaxHours    int  `json:"maxHours"`
}

func (s *Server) terminalStatus() TerminalStatus {
	maxLive, idle, maxLen := s.term.limits()
	_, avail := s.d.SysHost.(TerminalOpener)
	n := s.term.count()
	if avail && s.d.SysHost != nil {
		if l, ok := s.terminalList(context.Background()); ok {
			n += detachedCount(l, "")
		}
	}
	return TerminalStatus{
		Enabled: s.terminalEnabled(), Available: s.d.SysHost != nil && avail,
		Sessions: n, MaxSessions: maxLive,
		IdleMinutes: int(idle / time.Minute), MaxHours: int(maxLen / time.Hour),
	}
}

func (s *Server) handleTerminalGet(w http.ResponseWriter, _ *http.Request, _ store.User) {
	writeJSON(w, 200, s.terminalStatus())
}

func (s *Server) handleTerminalPut(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body, false) {
		return
	}
	if body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "nothing to change")
		return
	}
	if s.d.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available")
		return
	}
	before := s.terminalEnabled()
	if before != *body.Enabled {
		v := "false"
		if *body.Enabled {
			v = "true"
		}
		if err := s.d.Store.SetSetting(settingTerminal, v); err != nil {
			s.internal(w, err)
			return
		}
		s.d.Logger.Printf("terminal %s by %s", map[bool]string{true: "enabled", false: "disabled"}[*body.Enabled], u.Username)
		s.audit(u, "system.terminal", map[string]any{"from": before, "to": *body.Enabled})
	}
	if !*body.Enabled {
		s.term.endAll("disabled")
	}
	writeJSON(w, 200, s.terminalStatus())
}

func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request, u store.User) {
	op := s.terminalOpener(w)
	if op == nil {
		return
	}
	if !s.terminalEnabled() {
		writeErr(w, http.StatusForbidden, "terminal_disabled", "the terminal is turned off")
		return
	}
	var body struct {
		Session string `json:"session"`
	}
	if !decode(w, r, &body, true) {
		return
	}
	maxLive, _, _ := s.term.limits()
	l, _ := s.terminalList(r.Context())
	if body.Session == "" {
		if s.term.count()+detachedCount(l, "") >= maxLive {
			writeErr(w, http.StatusTooManyRequests, "too_many_sessions", "too many terminal sessions are open")
			return
		}
	} else {
		// Reattach: the session must exist, be detached and be the user's own.
		var found *helper.TerminalInfo
		for i := range l.Sessions {
			if l.Sessions[i].ID == body.Session {
				found = &l.Sessions[i]
			}
		}
		switch {
		case found == nil:
			writeErr(w, http.StatusNotFound, "not_found", "no such session")
			return
		case found.Owner != u.ID:
			writeErr(w, http.StatusForbidden, "not_yours", "that session belongs to another user")
			return
		case found.Attached:
			writeErr(w, http.StatusConflict, "session_in_use", "that session is open in another window")
			return
		}
	}
	tok, err := s.term.tickets.issueFor(u.ID, auth.SessionHash(auth.TokenFrom(r)), body.Session)
	if err != nil {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many tickets, try again in a moment")
		return
	}
	writeJSON(w, 200, map[string]any{"ticket": tok, "expiresIn": int(terminalTicketTTL / time.Second)})
}

// terminalList asks the helper for its sessions; ok is false when it cannot.
func (s *Server) terminalList(ctx context.Context) (helper.TerminalList, bool) {
	op, ok := s.d.SysHost.(TerminalOpener)
	if !ok {
		return helper.TerminalList{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	l, err := op.ListTerminals(ctx, nil)
	return l, err == nil
}

// detachedCount counts the sessions nobody is attached to, apart from the one
// named exclude. Attached ones are in the registry already.
func detachedCount(l helper.TerminalList, exclude string) int {
	n := 0
	for _, t := range l.Sessions {
		if !t.Attached && t.ID != exclude {
			n++
		}
	}
	return n
}

// TerminalSession is one row of GET /api/system/terminal/sessions.
type TerminalSession struct {
	ID            string `json:"id"`
	Owner         string `json:"owner"`
	Mine          bool   `json:"mine"`
	Started       int64  `json:"started"`
	Attached      bool   `json:"attached"`
	DetachedSince int64  `json:"detachedSince,omitempty"`
	LinuxUser     string `json:"linuxUser,omitempty"`
}

func (s *Server) handleTerminalSessions(w http.ResponseWriter, r *http.Request, u store.User) {
	if s.terminalOpener(w) == nil {
		return
	}
	s.reconcileTerminals(r.Context())
	l, ok := s.terminalList(r.Context())
	if !ok {
		writeErr(w, http.StatusBadGateway, "system_unavailable", "the helper did not answer")
		return
	}
	out := []TerminalSession{}
	for _, t := range l.Sessions {
		out = append(out, TerminalSession{ID: t.ID, Owner: s.userName(t.Owner), Mine: t.Owner == u.ID, Started: t.Started,
			Attached: t.Attached, DetachedSince: t.DetachedSince, LinuxUser: t.LinuxUser})
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

func (s *Server) userName(id int64) string {
	if s.d.Store == nil {
		return ""
	}
	if u, err := s.d.Store.UserByID(id); err == nil {
		return u.Username
	}
	return ""
}

func validTermID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// handleTerminalSessionEnd lets an administrator end any session, detached or
// not.
func (s *Server) handleTerminalSessionEnd(w http.ResponseWriter, r *http.Request, u store.User) {
	op := s.terminalOpener(w)
	if op == nil {
		return
	}
	id := r.PathValue("id")
	if !validTermID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid session id")
		return
	}
	l, ok := s.terminalList(r.Context())
	if !ok {
		writeErr(w, http.StatusBadGateway, "system_unavailable", "the helper did not answer")
		return
	}
	var info *helper.TerminalInfo
	for i := range l.Sessions {
		if l.Sessions[i].ID == id {
			info = &l.Sessions[i]
		}
	}
	if info == nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if live := s.term.liveByID(id); live != nil {
		live.end("admin-ended") // its own relay stops the session and audits it
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if err := op.EndTerminal(r.Context(), id); err != nil {
		s.systemError(w, err)
		return
	}
	linux := info.LinuxUser
	if linux == "" {
		linux = "unknown"
	}
	secs := int(time.Since(time.Unix(info.Started, 0)) / time.Second)
	s.d.Logger.Printf("terminal session of %s ended (admin-ended) by %s", s.userName(info.Owner), u.Username)
	s.audit(u, "system.terminal.end", map[string]any{"seconds": secs, "reason": "admin-ended", "linuxUser": linux, "owner": s.userName(info.Owner), "id": id[:8]})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// reconcileTerminals audits the sessions that ended on their own while
// detached (grace period over, apt finished, login exited): no web session
// was there to see it, so the helper keeps a record until it is acknowledged.
func (s *Server) reconcileTerminals(ctx context.Context) {
	op, ok := s.d.SysHost.(TerminalOpener)
	if !ok || s.d.SysHost == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	l, err := op.ListTerminals(cctx, nil)
	if err != nil || len(l.Ended) == 0 {
		return
	}
	var ack []string
	for _, e := range l.Ended {
		if len(ack) < 64 {
			ack = append(ack, e.ID)
		}
		if !e.Detached || (e.Reason != helper.EndGrace && e.Reason != helper.EndAptFinished && e.Reason != helper.EndExited) {
			continue // a web session was attached and audited it, or it was ended on request
		}
		u := store.User{ID: e.Owner, Username: s.userName(e.Owner)}
		if u.Username == "" {
			u.Username = "unknown"
		}
		linux := e.LinuxUser
		if linux == "" {
			linux = "unknown"
		}
		secs := int(e.Ended - e.Started)
		s.d.Logger.Printf("terminal session of %s ended (%s)", u.Username, e.Reason)
		s.audit(u, "system.terminal.end", map[string]any{"seconds": secs, "reason": e.Reason, "linuxUser": linux, "id": e.ID[:8]})
	}
	_, _ = op.ListTerminals(cctx, ack)
}

// terminalLoop reconciles every 30 seconds.
func (s *Server) terminalLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.terminalEnabled() {
				s.reconcileTerminals(ctx)
			}
		}
	}
}

// queryDim reads rows or cols from the query: absent or invalid is 0 (the
// default size), a value above the maximum is cut.
func queryDim(r *http.Request, name string) uint16 {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < 1 {
		return 0
	}
	if n > helper.MaxRows {
		n = helper.MaxRows
	}
	return uint16(n)
}

// wsResize is the one text message the browser may send.
type wsResize struct {
	Type string `json:"type"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request, u store.User) {
	// Cross-site WebSocket hijacking is the main risk: the Origin must name
	// this host, whatever the cookie says.
	if !terminalOriginOK(r, s.d.AllowedOrigins) {
		writeErr(w, http.StatusForbidden, "csrf", "cross-origin request rejected")
		return
	}
	op := s.terminalOpener(w)
	if op == nil {
		return
	}
	if !s.terminalEnabled() {
		writeErr(w, http.StatusForbidden, "terminal_disabled", "the terminal is turned off")
		return
	}
	target, okTicket := s.term.tickets.redeemFor(r.URL.Query().Get("ticket"), u.ID, auth.SessionHash(auth.TokenFrom(r)))
	if !okTicket {
		writeErr(w, http.StatusForbidden, "bad_ticket", "invalid or expired ticket")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	list, _ := s.terminalList(r.Context())
	sess, ok := s.term.reserve(cancel, u.ID, detachedCount(list, target))
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "too_many_sessions", "too many terminal sessions are open")
		return
	}
	defer s.term.release(sess)

	rows, cols := queryDim(r, "rows"), queryDim(r, "cols")
	var stream io.ReadWriteCloser
	var err error
	id := target
	if target == "" {
		stream, id, err = op.OpenTerminal(r.Context(), rows, cols, u.ID)
	} else {
		stream, err = op.AttachTerminal(r.Context(), target, u.ID, rows, cols)
		for _, t := range list.Sessions {
			if t.ID == target && t.Started > 0 {
				sess.started = time.Unix(t.Started, 0) // the length limit counts from the start
			}
		}
	}
	if err != nil {
		if target == "" && s.terminalStartFailed(w, r, err) {
			return
		}
		s.systemError(w, err)
		return
	}
	sess.setID(id)
	// Leaving without an explicit end only detaches the session.
	defer stream.Close()

	// The Origin was checked above with the panel's own rule; the library's
	// default (same host only) would refuse the allowed extra origins.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		s.d.Logger.Printf("terminal: websocket upgrade: %v", err)
		// Nobody is attached and nobody asked to end it: it is detached.
		return
	}
	ws.SetReadLimit(64 << 10)

	ip := auth.ClientIP(r)
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	if target == "" {
		s.d.Logger.Printf("terminal session started by %s", u.Username)
		s.audit(u, "system.terminal.start", map[string]any{"ip": ip, "id": short})
	} else {
		s.d.Logger.Printf("terminal session of %s reattached", u.Username)
		s.audit(u, "system.terminal.reattach", map[string]any{"ip": ip, "id": short})
	}
	s.relayTerminal(ctx, ws, stream, sess, r, op, id)
	reason := sess.endReason()
	d := s.term.clock().Sub(sess.started)
	linux := sess.user()
	if linux == "" {
		linux = "unknown"
	}
	if isDetachReason(reason) {
		s.d.Logger.Printf("terminal session of %s detached", u.Username)
		s.audit(u, "system.terminal.detach", map[string]any{"seconds": int(d / time.Second), "linuxUser": linux, "ip": ip, "id": short})
		return
	}
	s.d.Logger.Printf("terminal session of %s ended (%s)", u.Username, reason)
	s.audit(u, "system.terminal.end", map[string]any{"seconds": int(d / time.Second), "reason": reason, "linuxUser": linux, "ip": ip, "id": short})
}

// terminalStartFailed reports a failed start of a new session (not an
// unavailable helper) to the page. A browser cannot read the body of a
// refused WebSocket upgrade, so the upgrade is accepted and closed at once
// with the reason in the close frame ("start: ..."), which the page shows. It
// returns false when the error is of another kind, which systemError handles.
func (s *Server) terminalStartFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var ue *helper.UnavailableError
	if errors.As(err, &ue) || errors.Is(err, helper.ErrBusy) {
		return false
	}
	s.d.Logger.Printf("system: %v", err)
	ws, aerr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if aerr != nil {
		return true // Accept has answered
	}
	msg := err.Error()
	if i := strings.Index(msg, "terminal not available: "); i >= 0 {
		msg = msg[i+len("terminal not available: "):]
	}
	reason := "start: " + strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, msg)
	if len(reason) > 120 { // a close reason is at most 123 bytes
		reason = reason[:120]
	}
	_ = ws.Close(websocket.StatusInternalError, reason)
	return true
}

// cleanLinuxUser accepts a plausible user name from the helper and nothing else.
func cleanLinuxUser(b []byte) string {
	if len(b) == 0 || len(b) > 64 {
		return ""
	}
	for _, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == '$':
		default:
			return ""
		}
	}
	return string(b)
}

// relayTerminal moves bytes between the WebSocket and the helper stream until
// the session ends or detaches. It never inspects or stores the bytes.
//
// Browser to helper: binary messages are keyboard input (they reset the idle
// timer); text messages are JSON control: {"type":"resize","rows","cols"} and
// {"type":"end"}. Only an explicit end (this message, the idle and length
// limits, a logout, the switch, an administrator) ends the session. A
// WebSocket that just closes, a stream that fails and a web server that is
// shutting down detach it: the session keeps running in its own unit.
// Helper to browser: data frames become binary messages; a user frame names
// the Linux user for the audit record; an end frame says the session is over.
func (s *Server) relayTerminal(ctx context.Context, ws *websocket.Conn, stream io.ReadWriteCloser, sess *termSession, req *http.Request, op TerminalOpener, id string) {
	_, idle, maxLen := s.term.limits()
	var wg sync.WaitGroup
	var over atomic.Bool // the helper said the session is over

	// Browser to helper.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sess.end("detach") // no-op if a reason is set already
		for {
			typ, data, err := ws.Read(context.Background()) // not ctx: the close frame needs a reader
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				sess.lastInput.Store(s.term.clock().UnixNano())
				if err := helper.WriteData(stream, data); err != nil {
					sess.end("error")
					return
				}
			case websocket.MessageText:
				var m wsResize
				if json.Unmarshal(data, &m) != nil {
					continue
				}
				if m.Type == "end" {
					sess.end("closed")
					return
				}
				if m.Type != "resize" || m.Rows < 1 || m.Cols < 1 || m.Rows > helper.MaxRows || m.Cols > helper.MaxCols {
					continue
				}
				if err := helper.WriteResize(stream, uint16(m.Rows), uint16(m.Cols)); err != nil {
					sess.end("error")
					return
				}
			}
		}
	}()

	// Helper to browser.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sess.end("error") // a bare end of the stream is a lost transport
		for {
			typ, p, err := helper.ReadFrame(stream)
			if err != nil {
				return
			}
			switch typ {
			case helper.FrameData:
				wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := ws.Write(wctx, websocket.MessageBinary, p)
				cancel()
				if err != nil {
					return
				}
			case helper.FrameUser:
				sess.setLinuxUser(cleanLinuxUser(p))
			case helper.FrameEnd:
				over.Store(true)
				sess.end("closed")
				return
			}
		}
	}()

	// Limits: idle, maximum length, login session, the switch.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(s.term.interval())
		defer t.Stop()
		lastAuth := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				sess.end("detach") // the web server is stopping: the session lives on
				return
			case <-t.C:
			}
			if reason := terminalExpiry(s.term.clock(), sess.started, time.Unix(0, sess.lastInput.Load()), idle, maxLen); reason != "" {
				sess.end(reason)
				return
			}
			if !s.terminalEnabled() {
				sess.end("disabled")
				return
			}
			if time.Since(lastAuth) >= 5*time.Second {
				lastAuth = time.Now()
				if u, ok := s.d.Auth.Authenticate(req); !ok || auth.RoleRank(u.Role) < auth.RoleRank(store.RoleAdmin) {
					sess.end("logout")
					return
				}
			}
		}
	}()

	<-ctx.Done()
	reason := sess.endReason()
	if !isDetachReason(reason) && !over.Load() {
		// An explicit end: tell the helper, which stops the session's unit.
		// If that cannot be said on the stream, ask for it by itself.
		if err := helper.WriteFrame(stream, helper.FrameEnd, nil); err != nil {
			ectx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_ = op.EndTerminal(ectx, id)
			cancel()
		}
	}
	_ = stream.Close() // unblocks the helper reader; without an end frame this is a detach
	_ = ws.Close(websocket.StatusNormalClosure, reason)
	wg.Wait()
}

// SetTerminalLimits overrides the terminal limits (tests): concurrent
// sessions, idle timeout, maximum length, the interval of the limit watcher
// and the clock. Zero values keep the defaults. Call it before serving.
func (s *Server) SetTerminalLimits(maxLive int, idle, maxLen, tick time.Duration, now func() time.Time) {
	s.term.cfgMu.Lock()
	s.term.maxLive, s.term.idle, s.term.maxLen, s.term.tick, s.term.now = maxLive, idle, maxLen, tick, now
	s.term.cfgMu.Unlock()
	s.term.tickets.mu.Lock()
	s.term.tickets.now = now
	s.term.tickets.mu.Unlock()
}
