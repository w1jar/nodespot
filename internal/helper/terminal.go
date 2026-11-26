package helper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Browser terminal (docs/PLAN-1.3.0.md section 8). A session is a term-host
// process in its own transient unit (termhost.go) that owns the pseudo
// terminal and runs login(1). The helper is only a relay: it starts the unit,
// connects to term-host's socket and moves framed bytes (terminal_frame.go)
// between that socket and the connection that carried the request. A helper
// restart therefore only drops relays; the sessions keep running.

// Terminal ops. terminal.open and terminal.attach switch the connection to a
// framed stream after the JSON reply; terminal.list and terminal.end are
// plain request and reply.
const (
	OpTerminalOpen   = "terminal.open"
	OpTerminalAttach = "terminal.attach"
	OpTerminalList   = "terminal.list"
	OpTerminalEnd    = "terminal.end"
)

func isTerminalOp(op string) bool {
	switch op {
	case OpTerminalOpen, OpTerminalAttach, OpTerminalList, OpTerminalEnd:
		return true
	}
	return false
}

// isTerminalStreamOp: the ops that keep the connection open for frames.
func isTerminalStreamOp(op string) bool { return op == OpTerminalOpen || op == OpTerminalAttach }

// TerminalRequest is the argument of the terminal ops. Sizes are optional
// (zero: 24 rows, 80 columns) and bounded; ID names a session (attach, end);
// Owner is the nodespot user id the web server acts for; Ack lists ended records
// to forget (list). Nothing else is accepted, so no client string reaches a
// command line or an environment.
type TerminalRequest struct {
	Rows  uint16   `json:"rows,omitempty"`
	Cols  uint16   `json:"cols,omitempty"`
	Owner int64    `json:"owner,omitempty"`
	ID    string   `json:"id,omitempty"`
	Ack   []string `json:"ack,omitempty"`
}

// TerminalOpened is the reply of terminal.open and terminal.attach.
type TerminalOpened struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

const (
	// terminalWriteTimeout bounds one write towards the web server.
	terminalWriteTimeout = 30 * time.Second
	maxTerminalAck       = 64
)

func (s *Server) handleTerminal(ctx context.Context, conn net.Conn, in io.Reader, req Request, uid uint32) {
	reply := func(v any) {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = json.NewEncoder(conn).Encode(v)
	}
	fail := func(msg string) { reply(errorResponse{Error: msg}) }
	tr := req.Terminal
	if tr == nil {
		tr = &TerminalRequest{}
	}
	if s.Term == nil {
		fail("terminal not available")
		return
	}
	if tr.Owner < 0 {
		fail("invalid owner")
		return
	}
	if (tr.Rows != 0 || tr.Cols != 0) && !ValidSize(tr.Rows, tr.Cols) {
		s.logf("uid %d: %s: invalid size", uid, req.Op)
		fail("invalid size")
		return
	}
	rows, cols := ClampSize(tr.Rows, tr.Cols)
	// Non-stream ops are never cut by the helper stopping.
	octx := context.WithoutCancel(ctx)

	switch req.Op {
	case OpTerminalList:
		if len(tr.Ack) > maxTerminalAck {
			fail("invalid request")
			return
		}
		s.Term.Ack(tr.Ack)
		l, err := s.Term.List(octx)
		if err != nil {
			s.logf("uid %d: %s: error: %v", uid, req.Op, err)
			fail(err.Error())
			return
		}
		reply(l)
		return
	case OpTerminalEnd:
		if !termIDRe.MatchString(tr.ID) {
			fail("invalid session")
			return
		}
		if err := s.Term.End(octx, tr.ID); err != nil {
			s.logf("uid %d: %s: error: %v", uid, req.Op, err)
			fail(err.Error())
			return
		}
		s.logf("uid %d: terminal session ended on request", uid)
		reply(OKResult{OK: true})
		return
	}

	var (
		id   string
		host net.Conn
		err  error
		what = "started"
	)
	if req.Op == OpTerminalAttach {
		id = tr.ID
		what = "reattached"
		host, err = s.Term.Attach(octx, id, tr.Owner, rows, cols)
	} else {
		id, host, err = s.Term.Open(octx, rows, cols, tr.Owner)
	}
	if err != nil {
		s.logf("uid %d: %s: error: %v", uid, req.Op, err)
		fail(err.Error())
		return
	}
	// Session start and end are logged, never what passes through.
	s.logf("uid %d: terminal session %s", uid, what)
	started := time.Now()
	// The request read set short deadlines; a session has none.
	_ = conn.SetDeadline(time.Time{})
	reply(TerminalOpened{OK: true, ID: id})
	_ = conn.SetWriteDeadline(time.Time{})
	ended := relayTerminal(ctx, conn, in, host, func() { _ = s.Term.End(context.WithoutCancel(ctx), id) })
	s.logf("uid %d: terminal relay %s after %s", uid, ended, time.Since(started).Round(time.Second))
}

// relayTerminal moves frames between the client connection and term-host until
// either side ends or ctx is done. A client that just goes away, and a
// helper that is stopping, only close the term-host connection: the session
// is detached and lives on. An end frame from the client calls stop (which
// runs systemctl stop on the session's unit). It returns how the relay ended:
// "detached", "ended" or "session over".
func relayTerminal(ctx context.Context, client net.Conn, in io.Reader, host net.Conn, stop func()) string {
	in = &skipLeadingSpace{r: in} // the newline of the request; frame types are 0..3
	var wmu sync.Mutex
	write := func(typ byte, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = client.SetWriteDeadline(time.Now().Add(terminalWriteTimeout))
		if typ == FrameData {
			return WriteData(client, b)
		}
		return WriteFrame(client, typ, b)
	}

	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }
	var closing atomic.Bool
	var mu sync.Mutex
	result := "detached"
	set := func(r string) { mu.Lock(); result = r; mu.Unlock() }

	// Client to term-host.
	go func() {
		defer finish()
		for {
			typ, payload, err := ReadFrame(in)
			if err != nil {
				return
			}
			switch typ {
			case FrameData:
				if _, err := host.Write(framed(FrameData, payload)); err != nil {
					set("session over")
					return
				}
			case FrameResize:
				rows, cols, err := ParseResize(payload)
				if err != nil {
					return
				}
				rows, cols = ClampSize(rows, cols)
				if err := WriteResize(host, rows, cols); err != nil {
					set("session over")
					return
				}
			case FrameEnd:
				set("ended")
				stop()
				return
			default:
				return // FrameUser only flows towards the web server
			}
		}
	}()

	// Term-host to client.
	go func() {
		defer finish()
		for {
			typ, payload, err := ReadFrame(host)
			if err != nil {
				if !closing.Load() {
					// term-host closed the connection: the session is over.
					// Say so, so the client can tell it from a lost transport.
					set("session over")
					_ = write(FrameEnd, nil)
				}
				return
			}
			if typ != FrameData && typ != FrameUser {
				return
			}
			if err := write(typ, payload); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	closing.Store(true)
	_ = host.Close()
	_ = client.Close()
	mu.Lock()
	defer mu.Unlock()
	return result
}

// framed encodes one frame (a single Write per frame keeps frames whole).
func framed(typ byte, payload []byte) []byte {
	b := make([]byte, 3+len(payload))
	b[0] = typ
	b[1], b[2] = byte(len(payload)>>8), byte(len(payload))
	copy(b[3:], payload)
	return b
}

// errTerminalUnsupported is returned where there is no pseudo terminal.
var errTerminalUnsupported = errors.New("terminal not available on this system")

// adoptTerminalsAtStart returns the production AdoptTerminals.
func adoptTerminalsAtStart(logger *log.Logger, b TermBackend) func(ctx context.Context) {
	if runtime.GOOS != "linux" || b == nil {
		return nil
	}
	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		adopted, stopped, err := b.Adopt(ctx)
		if logger == nil {
			return
		}
		if adopted > 0 {
			logger.Printf("adopted %d running terminal session(s)", adopted)
		}
		if stopped > 0 {
			logger.Printf("stopped %d broken terminal unit(s) with no reachable session", stopped)
		}
		if err != nil {
			logger.Printf("terminal adoption: %v", err)
		}
	}
}
