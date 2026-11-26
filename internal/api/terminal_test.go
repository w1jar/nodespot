package api_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/helper"
)

// fakeTerm is one session of the fake helper.
type fakeTerm struct {
	id       string
	owner    int64
	started  int64
	attached bool
	ended    bool
	srv      net.Conn // the current attachment, nil while detached
}

// termSys is a fake helper with terminal support. The "shell" echoes keyboard
// input back prefixed with "echo:" and records resizes. Sessions live until an
// explicit end (a FrameEnd or EndTerminal); a stream that just closes leaves
// them detached, as the real helper does.
type termSys struct {
	*fakeSys
	tmu      sync.Mutex
	opened   int
	nextID   int
	sessions map[string]*fakeTerm
	sizes    []string
	rows     uint16
	cols     uint16
	acked    []string
	ended    []helper.TerminalEnded
	hangups  chan struct{} // one per session ended explicitly
}

func newTermSys() *termSys {
	return &termSys{fakeSys: newFakeSys(), sessions: map[string]*fakeTerm{}, hangups: make(chan struct{}, 16)}
}

func (f *termSys) serve(t *fakeTerm, server net.Conn, replay bool) {
	f.tmu.Lock()
	t.attached, t.srv = true, server
	f.tmu.Unlock()
	go func() {
		defer func() {
			server.Close()
			f.tmu.Lock()
			if t.srv == server {
				t.srv, t.attached = nil, false
			}
			f.tmu.Unlock()
		}()
		if replay {
			if helper.WriteData(server, []byte("replay:"+t.id)) != nil {
				return
			}
		}
		if helper.WriteFrame(server, helper.FrameUser, []byte("alice")) != nil {
			return
		}
		for {
			typ, p, err := helper.ReadFrame(server)
			if err != nil {
				return
			}
			switch typ {
			case helper.FrameData:
				if helper.WriteData(server, append([]byte("echo:"), p...)) != nil {
					return
				}
			case helper.FrameResize:
				r, c, _ := helper.ParseResize(p)
				f.tmu.Lock()
				f.sizes = append(f.sizes, strconv.Itoa(int(r))+"x"+strconv.Itoa(int(c)))
				f.tmu.Unlock()
			case helper.FrameEnd:
				f.finish(t)
				return
			}
		}
	}()
}

func (f *termSys) finish(t *fakeTerm) {
	f.tmu.Lock()
	if t.ended {
		f.tmu.Unlock()
		return
	}
	t.ended = true
	delete(f.sessions, t.id)
	srv := t.srv
	f.tmu.Unlock()
	if srv != nil {
		srv.Close()
	}
	f.hangups <- struct{}{}
}

func (f *termSys) OpenTerminal(_ context.Context, rows, cols uint16, owner int64) (io.ReadWriteCloser, string, error) {
	f.tmu.Lock()
	f.opened++
	f.nextID++
	f.rows, f.cols = rows, cols
	t := &fakeTerm{id: fmt.Sprintf("%016x", f.nextID), owner: owner, started: time.Now().Unix()}
	f.sessions[t.id] = t
	f.tmu.Unlock()
	client, server := net.Pipe()
	f.serve(t, server, false)
	return client, t.id, nil
}

func (f *termSys) AttachTerminal(_ context.Context, id string, owner int64, rows, cols uint16) (io.ReadWriteCloser, error) {
	f.tmu.Lock()
	t := f.sessions[id]
	switch {
	case t == nil:
		f.tmu.Unlock()
		return nil, errors.New("no such session")
	case t.owner != owner:
		f.tmu.Unlock()
		return nil, errors.New("not your session")
	case t.attached:
		f.tmu.Unlock()
		return nil, errors.New("session is in use")
	}
	f.tmu.Unlock()
	client, server := net.Pipe()
	f.serve(t, server, true)
	return client, nil
}

func (f *termSys) ListTerminals(_ context.Context, ack []string) (helper.TerminalList, error) {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	f.acked = append(f.acked, ack...)
	l := helper.TerminalList{Sessions: []helper.TerminalInfo{}}
	for _, t := range f.sessions {
		l.Sessions = append(l.Sessions, helper.TerminalInfo{ID: t.id, Owner: t.owner, Started: t.started, Attached: t.attached,
			DetachedSince: time.Now().Unix(), LinuxUser: "alice"})
	}
	for _, e := range f.ended {
		skip := false
		for _, a := range f.acked {
			if a == e.ID {
				skip = true
			}
		}
		if !skip {
			l.Ended = append(l.Ended, e)
		}
	}
	return l, nil
}

func (f *termSys) EndTerminal(_ context.Context, id string) error {
	f.tmu.Lock()
	t := f.sessions[id]
	f.tmu.Unlock()
	if t == nil {
		return errors.New("no such session")
	}
	f.finish(t)
	return nil
}

func (f *termSys) isLive(id string) bool {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	return f.sessions[id] != nil
}

func (f *termSys) liveCount() int {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	return len(f.sessions)
}

func (f *termSys) detachedIDs() []string {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	var ids []string
	for _, t := range f.sessions {
		if !t.attached {
			ids = append(ids, t.id)
		}
	}
	return ids
}

func (f *termSys) sizeList() string {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	return strings.Join(f.sizes, ",")
}

func (f *termSys) openCount() int {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	return f.opened
}

func (e *env) termOn() {
	e.t.Helper()
	var st map[string]any
	if c := e.do("admin", "PUT", "/api/system/terminal", map[string]any{"enabled": true}, &st); c != 200 || st["enabled"] != true {
		e.t.Fatalf("enable: %d %v", c, st)
	}
}

func (e *env) ticket(role string) string {
	e.t.Helper()
	var out struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if c := e.do(role, "POST", "/api/system/terminal/ticket", nil, &out); c != 200 || out.Ticket == "" || out.ExpiresIn != 30 {
		e.t.Fatalf("ticket: %d %+v", c, out)
	}
	return out.Ticket
}

// ticket2 asks for a ticket to reattach to session id.
func (e *env) ticket2(role, id string) string {
	e.t.Helper()
	var out struct{ Ticket string }
	if c := e.do(role, "POST", "/api/system/terminal/ticket", map[string]any{"session": id}, &out); c != 200 || out.Ticket == "" {
		e.t.Fatalf("reattach ticket: %d %+v", c, out)
	}
	return out.Ticket
}

// dialWS opens the terminal WebSocket as role. origin "" is the panel's own,
// "-" sends none.
func (e *env) dialWS(role, query, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if c := e.ck[role]; c != "" {
		h.Set("Cookie", auth.CookieName+"="+c)
	}
	if origin == "" {
		origin = e.ts.URL
	}
	if origin != "-" {
		h.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/api/system/terminal/ws"+query, &websocket.DialOptions{HTTPHeader: h})
}

func refused(resp *http.Response, err error, code int) bool {
	return err != nil && resp != nil && resp.StatusCode == code
}

func readText(t *testing.T, c *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	for !strings.Contains(got, want) {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q, want %q)", err, got, want)
		}
		got += string(b)
	}
}

func termWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func (e *env) auditActions() string {
	l, _ := e.st.ListAudit(100)
	var s []string
	for _, a := range l {
		s = append(s, a.Action+" "+a.Detail)
	}
	return strings.Join(s, "\n")
}

func (e *env) termSessions() int {
	var st struct{ Sessions int }
	e.do("admin", "GET", "/api/system/terminal", nil, &st)
	return st.Sessions
}

func TestTerminalDefaultOffAndRoles(t *testing.T) {
	e := newEnv(t, opts{sysHost: newTermSys()})
	var st struct {
		Enabled, Available bool
		MaxSessions        int
	}
	if c := e.do("admin", "GET", "/api/system/terminal", nil, &st); c != 200 || st.Enabled || !st.Available || st.MaxSessions != 2 {
		t.Fatalf("%d %+v", c, st)
	}
	if c, code := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil); c != 403 || code != "terminal_disabled" {
		t.Fatalf("ticket while off: %d %s", c, code)
	}
	for _, role := range []string{"op", "viewer", ""} {
		want := 403
		if role == "" {
			want = 401
		}
		for _, r := range []struct{ m, p string }{{"GET", "/api/system/terminal"}, {"PUT", "/api/system/terminal"}, {"POST", "/api/system/terminal/ticket"}, {"GET", "/api/system/terminal/ws"}} {
			var body any
			if r.m == "PUT" {
				body = map[string]any{"enabled": true}
			}
			if c := e.do(role, r.m, r.p, body, nil); c != want {
				t.Errorf("%q %s %s = %d, want %d", role, r.m, r.p, c, want)
			}
		}
	}
	if c := e.do("admin", "GET", "/api/system/terminal", nil, &st); c != 200 || st.Enabled {
		t.Fatalf("enabled by a non-admin: %+v", st)
	}
}

func TestTerminalNoHelperSupport(t *testing.T) {
	e := newEnv(t, opts{sysHost: newFakeSys()}) // no OpenTerminal
	e.termOn()
	if c, code := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil); c != 503 || code != "system_unavailable" {
		t.Fatalf("%d %s", c, code)
	}
	e2 := newEnv(t, opts{})
	if c := e2.do("admin", "POST", "/api/system/terminal/ticket", nil, nil); c != 503 {
		t.Fatalf("no helper: %d", c)
	}
}

func TestTerminalTicketNeedsCSRFHeader(t *testing.T) {
	e := newEnv(t, opts{sysHost: newTermSys()})
	e.termOn()
	c, _, _ := e.raw("POST", "/api/system/terminal/ticket", nil, e.ck["admin"], map[string]string{"X-nodespot": ""})
	if c != 403 {
		t.Fatalf("POST without the CSRF header = %d", c)
	}
}

func TestTerminalRelayEndToEnd(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin")+"&rows=30&cols=100", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	ctx := context.Background()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	readText(t, c, "echo:ls\r")
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":50,"cols":132}`)); err != nil {
		t.Fatal(err)
	}
	// invalid ones are ignored
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":0,"cols":132}`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":9999,"cols":1}`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`not json`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"other","rows":5,"cols":5}`))
	termWait(t, "resize", func() bool { return fs.sizeList() == "50x132" })
	fs.tmu.Lock()
	if fs.rows != 30 || fs.cols != 100 {
		t.Errorf("initial size %dx%d", fs.rows, fs.cols)
	}
	fs.tmu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if got := fs.sizeList(); got != "50x132" {
		t.Errorf("invalid resizes got through: %s", got)
	}

	// A WebSocket that just closes is a transport loss: the session is
	// detached and keeps running.
	_ = c.Close(websocket.StatusNormalClosure, "")
	termWait(t, "detach audit", func() bool { return strings.Contains(e.auditActions(), "system.terminal.detach") })
	if fs.liveCount() != 1 || len(fs.detachedIDs()) != 1 {
		t.Fatalf("session not detached: live %d detached %v", fs.liveCount(), fs.detachedIDs())
	}
	a := e.auditActions()
	for _, w := range []string{"system.terminal.start", `"linuxUser":"alice"`, `"seconds":`} {
		if !strings.Contains(a, w) {
			t.Errorf("audit lacks %q:\n%s", w, a)
		}
	}
	if strings.Contains(a, "system.terminal.end") {
		t.Errorf("a lost transport was audited as an end:\n%s", a)
	}
	if strings.Contains(a, "echo:") || strings.Contains(a, "ls\r") {
		t.Errorf("audit holds terminal data:\n%s", a)
	}
}

func TestTerminalExplicitEndOverWebSocket(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	termWait(t, "session up", func() bool { return e.termSessions() == 1 })
	if err := c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"end"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fs.hangups:
	case <-time.After(5 * time.Second):
		t.Fatal("explicit end did not stop the session")
	}
	drain(c)
	termWait(t, "end audit", func() bool { return strings.Contains(e.auditActions(), "system.terminal.end") })
	a := e.auditActions()
	if !strings.Contains(a, `"reason":"closed"`) || strings.Contains(a, "system.terminal.detach") {
		t.Errorf("audit:\n%s", a)
	}
	if fs.liveCount() != 0 {
		t.Fatal("session still live after the end")
	}
}

func TestTerminalReattach(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	termWait(t, "session up", func() bool { return e.termSessions() == 1 })
	c.CloseNow()
	termWait(t, "detached", func() bool { return len(fs.detachedIDs()) == 1 })
	id := fs.detachedIDs()[0]

	// the list shows it, owner-only controls
	var list struct{ Sessions []map[string]any }
	if code := e.do("admin", "GET", "/api/system/terminal/sessions", nil, &list); code != 200 || len(list.Sessions) != 1 ||
		list.Sessions[0]["id"] != id || list.Sessions[0]["mine"] != true || list.Sessions[0]["attached"] != false || list.Sessions[0]["owner"] != "admin" {
		t.Fatalf("%d %+v", code, list)
	}

	// another administrator cannot take it over
	if _, err := e.st.CreateUser("admin2", testHash("admin2"), "admin"); err != nil {
		t.Fatal(err)
	}
	e.session("admin2")
	var out struct{ Ticket string }
	if code, ec := e.errCode("admin2", "POST", "/api/system/terminal/ticket", map[string]any{"session": id}); code != 403 || ec != "not_yours" {
		t.Fatalf("other admin: %d %s", code, ec)
	}
	if code, ec := e.errCode("admin", "POST", "/api/system/terminal/ticket", map[string]any{"session": "00000000000000ff"}); code != 404 || ec != "not_found" {
		t.Fatalf("unknown: %d %s", code, ec)
	}
	// even with a ticket for the owner, the other user's websocket cannot use it
	if code := e.do("admin", "POST", "/api/system/terminal/ticket", map[string]any{"session": id}, &out); code != 200 {
		t.Fatalf("own ticket: %d", code)
	}
	if _, resp, err := e.dialWS("admin2", "?ticket="+out.Ticket, ""); !refused(resp, err, 403) {
		t.Fatalf("ticket used by another user: %v", err)
	}

	// the owner reattaches: replay first, then live
	c, _, err = e.dialWS("admin", "?ticket="+e.ticket2("admin", id), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readText(t, c, "replay:"+id)
	_ = c.Write(context.Background(), websocket.MessageBinary, []byte("pwd\r"))
	readText(t, c, "echo:pwd\r")
	if fs.openCount() != 1 {
		t.Fatalf("reattach opened a new session (%d)", fs.openCount())
	}
	// while attached, a second ticket for it is refused
	if code, ec := e.errCode("admin", "POST", "/api/system/terminal/ticket", map[string]any{"session": id}); code != 409 || ec != "session_in_use" {
		t.Fatalf("in use: %d %s", code, ec)
	}
	a := e.auditActions()
	if !strings.Contains(a, "system.terminal.reattach") {
		t.Fatalf("no reattach audit:\n%s", a)
	}
}

func TestTerminalSessionLimitCountsDetached(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	for i := 0; i < 2; i++ {
		c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
		if err != nil {
			t.Fatal(err)
		}
		n := i + 1
		termWait(t, "up", func() bool { return fs.liveCount() == n })
		c.CloseNow()
		termWait(t, "detached", func() bool { return len(fs.detachedIDs()) == n })
	}
	if code, ec := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil); code != 429 || ec != "too_many_sessions" {
		t.Fatalf("detached sessions are not counted: %d %s", code, ec)
	}
	// ending one frees a slot
	id := fs.detachedIDs()[0]
	if code := e.do("admin", "POST", "/api/system/terminal/sessions/"+id+"/end", nil, nil); code != 200 {
		t.Fatalf("end: %d", code)
	}
	termWait(t, "slot", func() bool {
		c, _ := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil)
		return c == 200
	})
	a := e.auditActions()
	if !strings.Contains(a, `"reason":"admin-ended"`) {
		t.Fatalf("admin end not audited:\n%s", a)
	}
}

func TestTerminalAdminEndsAttachedAndRefusals(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	termWait(t, "up", func() bool { return fs.liveCount() == 1 })
	l, _ := fs.ListTerminals(context.Background(), nil)
	id := l.Sessions[0].ID
	if code := e.do("op", "POST", "/api/system/terminal/sessions/"+id+"/end", nil, nil); code != 403 {
		t.Fatalf("operator ended a session: %d", code)
	}
	if code := e.do("op", "GET", "/api/system/terminal/sessions", nil, nil); code != 403 {
		t.Fatalf("operator listed sessions: %d", code)
	}
	if code := e.do("admin", "POST", "/api/system/terminal/sessions/..%2f..%2fx/end", nil, nil); code == 200 {
		t.Fatal("bad id accepted")
	}
	if code := e.do("admin", "POST", "/api/system/terminal/sessions/00000000000000ff/end", nil, nil); code != 404 {
		t.Fatalf("unknown id: %d", code)
	}
	if code := e.do("admin", "POST", "/api/system/terminal/sessions/"+id+"/end", nil, nil); code != 200 {
		t.Fatalf("end: %d", code)
	}
	select {
	case <-fs.hangups:
	case <-time.After(5 * time.Second):
		t.Fatal("attached session survived the admin end")
	}
	drain(c)
	termWait(t, "audit", func() bool { return strings.Contains(e.auditActions(), `"reason":"admin-ended"`) })
}

// Sessions that ended on their own while detached are audited once, from the
// helper's records.
func TestTerminalReconcileAuditsHostEnds(t *testing.T) {
	fs := newTermSys()
	fs.ended = []helper.TerminalEnded{
		{ID: "00000000000000aa", Owner: 1, Started: 100, Ended: 400, Reason: helper.EndGrace, Detached: true, LinuxUser: "alice"},
		{ID: "00000000000000ab", Owner: 1, Started: 100, Ended: 400, Reason: helper.EndRequested, Detached: false},
		{ID: "00000000000000ac", Owner: 1, Started: 100, Ended: 900, Reason: helper.EndAptFinished, Detached: true},
	}
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	var list struct{ Sessions []any }
	if code := e.do("admin", "GET", "/api/system/terminal/sessions", nil, &list); code != 200 {
		t.Fatal(code)
	}
	a := e.auditActions()
	if !strings.Contains(a, `"reason":"grace"`) || !strings.Contains(a, `"reason":"apt-finished"`) || strings.Contains(a, "requested") {
		t.Fatalf("audit:\n%s", a)
	}
	if strings.Count(a, "system.terminal.end") != 2 {
		t.Fatalf("audit:\n%s", a)
	}
	// acknowledged: a second pass adds nothing
	e.do("admin", "GET", "/api/system/terminal/sessions", nil, &list)
	if n := strings.Count(e.auditActions(), "system.terminal.end"); n != 2 {
		t.Fatalf("audited twice: %d", n)
	}
	fs.tmu.Lock()
	acked := len(fs.acked)
	fs.tmu.Unlock()
	if acked != 3 {
		t.Fatalf("acked %d", acked)
	}
}

func TestTerminalTicketRules(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()

	// single use
	tk := e.ticket("admin")
	c, _, err := e.dialWS("admin", "?ticket="+tk, "")
	if err != nil {
		t.Fatal(err)
	}
	c.CloseNow()
	if _, resp, err := e.dialWS("admin", "?ticket="+tk, ""); !refused(resp, err, 403) {
		t.Fatalf("ticket reused: %v %v", err, resp)
	}
	// missing and bogus
	for _, q := range []string{"", "?ticket=", "?ticket=nope"} {
		if _, resp, err := e.dialWS("admin", q, ""); !refused(resp, err, 403) {
			t.Errorf("%q accepted: %v", q, err)
		}
	}
	// tied to the login session: a second login does not share tickets
	old := e.ticket("admin")
	e.session("admin")
	if _, resp, err := e.dialWS("admin", "?ticket="+old, ""); !refused(resp, err, 403) {
		t.Errorf("ticket used from another session: %v", err)
	}
	// an operator cannot use an admin's ticket (and is refused anyway)
	tk = e.ticket("admin")
	if _, resp, err := e.dialWS("op", "?ticket="+tk, ""); !refused(resp, err, 403) {
		t.Errorf("operator reached the terminal: %v", err)
	}
	if n := fs.openCount(); n != 1 {
		t.Errorf("%d terminals opened, want 1", n)
	}
}

func TestTerminalOriginChecked(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs, dev: true})
	e.termOn()
	for _, origin := range []string{"https://attacker.example", "http://127.0.0.1:1", "-"} {
		if _, resp, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), origin); !refused(resp, err, 403) {
			t.Errorf("origin %q accepted: %v", origin, err)
		}
	}
	if n := fs.openCount(); n != 0 {
		t.Fatalf("%d terminals opened by a bad origin", n)
	}
	// an allowed extra origin (dev server) works
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "http://localhost:5173")
	if err != nil {
		t.Fatalf("allowed origin refused: %v", err)
	}
	c.CloseNow()
}

func TestTerminalSessionLimit(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	var conns []*websocket.Conn
	for i := 0; i < 2; i++ {
		c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	termWait(t, "two sessions", func() bool { return e.termSessions() == 2 })
	if c, code := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil); c != 429 || code != "too_many_sessions" {
		t.Fatalf("third ticket: %d %s", c, code)
	}
	// a plain close only detaches (and still counts); an explicit end frees
	_ = conns[0].Write(context.Background(), websocket.MessageText, []byte(`{"type":"end"}`))
	conns[0].CloseNow()
	termWait(t, "slot freed", func() bool {
		c, _ := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil)
		return c == 200
	})
	conns[1].CloseNow()
}

func TestTerminalDisableEndsSessions(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	termWait(t, "session up", func() bool { return e.termSessions() == 1 })
	if code := e.do("admin", "PUT", "/api/system/terminal", map[string]any{"enabled": false}, nil); code != 200 {
		t.Fatal(code)
	}
	select {
	case <-fs.hangups:
	case <-time.After(5 * time.Second):
		t.Fatal("session survived the switch being turned off")
	}
	drain(c)
	termWait(t, "audit", func() bool { return strings.Contains(e.auditActions(), `"reason":"disabled"`) })
	if code, _ := e.errCode("admin", "POST", "/api/system/terminal/ticket", nil); code != 403 {
		t.Fatalf("ticket after disable: %d", code)
	}
	if !strings.Contains(e.auditActions(), "system.terminal {") {
		t.Errorf("setting change not audited:\n%s", e.auditActions())
	}
}

func TestTerminalIdleTimeout(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e.srv.SetTerminalLimits(0, 0, 0, 20*time.Millisecond, clock)
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	termWait(t, "session up", func() bool { return e.termSessions() == 1 })
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	// typing resets the timer; resizing does not
	adv(14 * time.Minute)
	_ = c.Write(context.Background(), websocket.MessageBinary, []byte("x"))
	readText(t, c, "echo:x")
	adv(14 * time.Minute)
	_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"resize","rows":10,"cols":10}`))
	time.Sleep(150 * time.Millisecond)
	select {
	case <-fs.hangups:
		t.Fatal("session ended before 15 idle minutes")
	default:
	}
	adv(2 * time.Minute)
	select {
	case <-fs.hangups:
	case <-time.After(5 * time.Second):
		t.Fatal("idle session not ended")
	}
	drain(c)
	termWait(t, "audit", func() bool { return strings.Contains(e.auditActions(), `"reason":"idle"`) })
}

func TestTerminalLoginSessionEnds(t *testing.T) {
	fs := newTermSys()
	e := newEnv(t, opts{sysHost: fs})
	e.termOn()
	c, _, err := e.dialWS("admin", "?ticket="+e.ticket("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	termWait(t, "session up", func() bool { return e.termSessions() == 1 })
	if code := e.do("admin", "POST", "/api/logout", nil, nil); code != 200 && code != 204 {
		t.Fatalf("logout: %d", code)
	}
	select {
	case <-fs.hangups:
	case <-time.After(15 * time.Second):
		t.Fatal("terminal outlived the login session")
	}
}

func TestTerminalCSPAllowsWebSocket(t *testing.T) {
	e := newEnv(t, opts{})
	_, _, h := e.raw("GET", "/api/health", nil, "", nil)
	csp := h.Get("Content-Security-Policy")
	host := strings.TrimPrefix(e.ts.URL, "http://")
	if !strings.Contains(csp, "connect-src 'self' ws://"+host+" wss://"+host) {
		t.Fatalf("CSP = %q", csp)
	}
}

// drain reads until the server closes the WebSocket, as a browser does; the
// server close handshake waits for the peer to answer.
func drain(c *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}
