package helper

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

// Pure parts of term-host (the process that owns a terminal session, see
// termhost.go): argument validation, the output ring buffer, the detach
// state machine, and the state files the helper reads. Nothing here touches
// a pseudo terminal, so it builds and is tested on every platform.

// termDir holds, per session, the socket <id>.sock, the state file <id>.json
// and, once the session is over, <id>.ended.json. Root only (0700). A
// variable for tests.
var termDir = "/run/nodespot/term"

const (
	// termRingSize is how much recent output term-host keeps for a reattach.
	termRingSize = 64 << 10
	// termGrace is how long a detached session lives before term-host ends it.
	termGrace = 2 * time.Minute
	// termAptPoll is how often a detached session whose grace has run out is
	// checked for a running package manager.
	termAptPoll = 5 * time.Second
	// termAptCap is the hard limit on waiting for a package manager, counted
	// from the moment of the detach.
	termAptCap = 2 * time.Hour
	// termEndedKeep is how long an unacknowledged <id>.ended.json is kept.
	termEndedKeep = 24 * time.Hour
)

// Why a session ended, as recorded in <id>.ended.json.
const (
	// EndRequested: an explicit end arrived on the socket.
	EndRequested = "requested"
	// EndStopped: systemd stopped the unit (systemctl stop, which is how the
	// helper ends a session).
	EndStopped = "stopped"
	// EndExited: login (the session's child) exited by itself, by status or
	// signal; the record's Detail says which.
	EndExited = "exited"
	// EndGrace: detached for the grace period (or the apt wait hit its cap).
	EndGrace = "grace"
	// EndAptFinished: detached, waited for apt or dpkg, and they finished.
	EndAptFinished = "apt-finished"
)

// termHostArgs are the validated arguments of `nodespot-helper term-host`.
type termHostArgs struct {
	ID    string
	Sock  string
	Rows  uint16
	Cols  uint16
	Owner int64 // nodespot user id that started the session (0: none)
}

// parseTermHostArgs validates everything: term-host runs as root and its
// command line is built by the helper, but it still trusts nothing.
func parseTermHostArgs(args []string) (termHostArgs, error) {
	fs := flag.NewFlagSet("term-host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "")
	sock := fs.String("sock", "", "")
	rows := fs.Uint("rows", 0, "")
	cols := fs.Uint("cols", 0, "")
	owner := fs.Int64("owner", 0, "")
	if err := fs.Parse(args); err != nil {
		return termHostArgs{}, err
	}
	if fs.NArg() != 0 {
		return termHostArgs{}, errors.New("unexpected argument")
	}
	if !termIDRe.MatchString(*id) {
		return termHostArgs{}, fmt.Errorf("invalid id %q", *id)
	}
	if *sock == "" || filepath.Clean(*sock) != filepath.Join(termDir, *id+".sock") {
		return termHostArgs{}, fmt.Errorf("socket must be %s", filepath.Join(termDir, *id+".sock"))
	}
	if *rows > MaxRows || *cols > MaxCols || !ValidSize(uint16(*rows), uint16(*cols)) {
		return termHostArgs{}, errors.New("invalid size")
	}
	if *owner < 0 {
		return termHostArgs{}, errors.New("invalid owner")
	}
	return termHostArgs{ID: *id, Sock: *sock, Rows: uint16(*rows), Cols: uint16(*cols), Owner: *owner}, nil
}

// ringBuf keeps the last max bytes written to it.
type ringBuf struct {
	max int
	buf []byte
}

func newRing(max int) *ringBuf { return &ringBuf{max: max} }

func (r *ringBuf) Write(p []byte) {
	if len(p) >= r.max {
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		n := copy(r.buf, r.buf[len(r.buf)-r.max:])
		r.buf = r.buf[:n]
	}
}

// Bytes returns a copy of what is kept, oldest first.
func (r *ringBuf) Bytes() []byte { return append([]byte(nil), r.buf...) }

// graceMachine decides when a detached session has to end. It owns no timer:
// the host calls Tick with the current time, so tests drive it with a fake
// clock and a fake package manager detector.
type graceMachine struct {
	grace, poll, cap time.Duration
	busy             func() bool // is apt, dpkg or similar running in the session?

	attached  bool
	since     time.Time // when it was detached
	waiting   bool      // the grace ran out while a package manager was running
	lastCheck time.Time
}

// newGraceMachine starts attached: a session always begins with a client.
func newGraceMachine(grace, poll, cap time.Duration, busy func() bool) *graceMachine {
	return &graceMachine{grace: grace, poll: poll, cap: cap, busy: busy, attached: true}
}

func (g *graceMachine) Attach() { g.attached = true; g.waiting = false }

func (g *graceMachine) Detach(now time.Time) {
	g.attached, g.since, g.waiting, g.lastCheck = false, now, false, time.Time{}
}

// Tick returns "" while the session lives on, or the reason it must end now:
// EndGrace after the grace period with no package manager running (or when
// the cap is reached), EndAptFinished when it had been waiting for one that
// has since exited.
func (g *graceMachine) Tick(now time.Time) string {
	if g.attached {
		return ""
	}
	elapsed := now.Sub(g.since)
	if elapsed < g.grace {
		return ""
	}
	if g.cap > 0 && elapsed >= g.cap {
		return EndGrace
	}
	if g.waiting && now.Sub(g.lastCheck) < g.poll {
		return ""
	}
	g.lastCheck = now
	if g.busy != nil && g.busy() {
		g.waiting = true
		return ""
	}
	if g.waiting {
		return EndAptFinished
	}
	return EndGrace
}

// TerminalInfo describes one live session (state file <id>.json).
type TerminalInfo struct {
	ID            string `json:"id"`
	Owner         int64  `json:"owner"`
	Started       int64  `json:"started"` // unix seconds
	Attached      bool   `json:"attached"`
	DetachedSince int64  `json:"detachedSince,omitempty"` // unix seconds, while detached
	LinuxUser     string `json:"linuxUser,omitempty"`
}

// TerminalEnded is the record of a session that is over (<id>.ended.json).
// Detached says it was detached when it ended, so no web session was there to
// notice (and audit) the end.
type TerminalEnded struct {
	ID        string `json:"id"`
	Owner     int64  `json:"owner"`
	Started   int64  `json:"started"`
	Ended     int64  `json:"ended"`
	Reason    string `json:"reason"`
	Detached  bool   `json:"detached"`
	LinuxUser string `json:"linuxUser,omitempty"`
	// Detail is the precise cause of an "exited" end, such as "login exited
	// with status 1 after 0.2s: login: some error".
	Detail string `json:"detail,omitempty"`
}

// TerminalList is the answer of terminal.list.
type TerminalList struct {
	Sessions []TerminalInfo  `json:"sessions"`
	Ended    []TerminalEnded `json:"ended,omitempty"`
}
