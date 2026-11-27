//go:build linux

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// term-host: `nodespot-helper term-host --id <hex> --sock <path> --rows R --cols C
// [--owner N]`. It is the main process of the transient unit
// nodespot-term-<id>.service, runs as root without a sandbox, and owns the
// pseudo terminal for the whole life of the session: it opens the PTY, starts
// login(1) on the slave as its child (new session, controlling terminal), and
// keeps the master. Because the master lives in the session's own cgroup, a
// restart of the helper or of the web server never hangs the terminal up.
//
// It listens on a root-only unix socket (termDir/<id>.sock) and speaks the
// terminal frames of terminal_frame.go to at most one client at a time (the
// helper's relay). With no client attached it keeps draining the PTY (so
// writes to the tty never block) into a 64 KiB ring buffer that a later
// client gets replayed. A detached session is ended after a grace period,
// unless apt, dpkg or similar run in it, in which case it waits for them.

// loginPaths are tried in order; the path is never taken from a client.
var loginPaths = []string{"/bin/login", "/usr/bin/login"}

const utmpPath = "/var/run/utmp"

// termHostEnv is the fixed environment of login (login -p keeps it).
var termHostEnv = []string{
	"TERM=xterm-256color",
	"LANG=C.UTF-8",
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
}

// RunTermHost is the entry point of the term-host subcommand; it returns the
// process exit status. It refuses to run unless the uid is 0.
func RunTermHost(args []string) int {
	logger := log.New(os.Stderr, "nodespot-term-host: ", 0)
	if os.Geteuid() != 0 {
		logger.Print("must run as root")
		return 1
	}
	a, err := parseTermHostArgs(args)
	if err != nil {
		logger.Print(err)
		return 2
	}
	var loginPath string
	for _, p := range loginPaths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			loginPath = p
			break
		}
	}
	if loginPath == "" {
		logger.Print("login not found")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cfg := termHostCfg{termHostArgs: a, Dir: termDir, Argv: []string{loginPath, "-p"}, Env: termHostEnv,
		Logf: logger.Printf}
	if _, err := runTermHost(ctx, cfg); err != nil {
		logger.Print(err)
		return 1
	}
	return 0
}

// termHostCfg is runTermHost's configuration; tests replace the command, the
// clock and the detectors.
type termHostCfg struct {
	termHostArgs
	Dir  string
	Argv []string
	Env  []string
	// Now is the clock (default time.Now), Busy says whether a package
	// manager runs in the session (default: scan the unit's cgroup), User
	// finds the Linux user (default: cgroup, then utmp).
	Now  func() time.Time
	Busy func() bool
	User func(line string) string
	// Grace, Poll and Cap default to termGrace, termAptPoll, termAptCap;
	// Tick is the housekeeping interval (default 1 s).
	Grace, Poll, Cap, Tick time.Duration
	Logf                   func(format string, a ...any)
}

type hostClient struct{ conn net.Conn }

type termHost struct {
	cfg    termHostCfg
	master *os.File
	cmd    *exec.Cmd
	line   string // "pts/N"
	now    func() time.Time

	// slave is term-host's own descriptor of the slave side, opened with
	// O_NOCTTY and kept until the child has exited: the master then never
	// sees "every other side closed" (EIO) just because login closed or hung
	// up its descriptors for a moment.
	slave *os.File

	childDone  chan struct{}
	masterGone chan struct{}
	endCh      chan string

	// How the child ended, set before childDone is closed.
	exitStatus string
	// First read error seen on the master, and the first bytes login wrote
	// (guarded by mu).
	firstReadErr string
	early        []byte

	mu       sync.Mutex
	ring     *ringBuf
	client   *hostClient
	gm       *graceMachine
	user     string
	started  time.Time
	detached time.Time
	closing  bool
}

func (h *termHost) logf(f string, a ...any) {
	if h.cfg.Logf != nil {
		h.cfg.Logf(f, a...)
	}
}

// runTermHost runs one session to its end and returns why it ended.
func runTermHost(ctx context.Context, cfg termHostCfg) (string, error) {
	if cfg.Dir == "" {
		cfg.Dir = termDir
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Grace == 0 {
		cfg.Grace = termGrace
	}
	if cfg.Poll == 0 {
		cfg.Poll = termAptPoll
	}
	if cfg.Cap == 0 {
		cfg.Cap = termAptCap
	}
	if cfg.Tick == 0 {
		cfg.Tick = time.Second
	}
	if cfg.Busy == nil {
		unit := termUnit(cfg.ID)
		cfg.Busy = func() bool { return termUnitBusy(unit) }
	}
	if cfg.User == nil {
		unit := termUnit(cfg.ID)
		cfg.User = func(line string) string { return lookupTermUser(unit, line) }
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(cfg.Dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Remove(cfg.Sock)
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", cfg.Sock)
	syscall.Umask(old)
	if err != nil {
		return "", err
	}
	_ = os.Chmod(cfg.Sock, 0o600)

	master, slave, pts, err := openPTY(cfg.Rows, cfg.Cols)
	if err != nil {
		ln.Close()
		os.Remove(cfg.Sock)
		return "", err
	}
	cmd := exec.Command(cfg.Argv[0], cfg.Argv[1:]...)
	cmd.Env = cfg.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		master.Close()
		ln.Close()
		os.Remove(cfg.Sock)
		return "", err
	}
	// The slave stays open here (see termHost.slave); it is closed when the
	// child has exited, so the last output can still be read to its end.

	h := &termHost{cfg: cfg, master: master, slave: slave, cmd: cmd, line: "pts/" + strconv.Itoa(pts), now: cfg.Now,
		childDone: make(chan struct{}), masterGone: make(chan struct{}), endCh: make(chan string, 1),
		ring: newRing(termRingSize), started: cfg.Now()}
	h.gm = newGraceMachine(cfg.Grace, cfg.Poll, cfg.Cap, cfg.Busy)
	// Starts "detached" from the file's point of view until the first client
	// attaches; the grace clock runs from the start as well, so a session
	// nobody ever attached to does not live forever.
	h.gm.Detach(h.now())
	h.detached = h.now()
	h.mu.Lock()
	h.writeStateLocked()
	h.mu.Unlock()

	go func() {
		werr := cmd.Wait()
		h.exitStatus = describeExit(filepath.Base(cfg.Argv[0]), cmd.ProcessState, werr)
		// Closing the slave lets the master report the end (EIO) once no
		// other process holds the terminal; the deadline covers background
		// processes that do.
		_ = slave.Close()
		_ = master.SetReadDeadline(time.Now().Add(termDrainTail))
		close(h.childDone)
	}()
	go h.drain()
	go h.acceptLoop(ln)
	go h.userPoll()

	reason := h.waitEnd(ctx)
	h.finish(reason, ln)
	return reason, nil
}

func (h *termHost) waitEnd(ctx context.Context) string {
	t := time.NewTicker(h.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return EndStopped
		case r := <-h.endCh:
			return r
		case <-h.childDone:
			h.drainWait()
			return EndExited
		case <-t.C:
			h.mu.Lock()
			r := h.gm.Tick(h.now())
			h.mu.Unlock()
			if r != "" {
				return r
			}
		}
	}
}

// drainWait lets the last output reach the client after login has exited.
func (h *termHost) drainWait() {
	select {
	case <-h.masterGone:
	case <-time.After(termDrainTail + 500*time.Millisecond):
	}
}

func (h *termHost) requestEnd(reason string) {
	select {
	case h.endCh <- reason:
	default:
	}
}

// finish records the end, hangs the session up where the end is ours to
// carry out, and removes the socket and the state file.
func (h *termHost) finish(reason string, ln net.Listener) {
	h.mu.Lock()
	h.closing = true
	// An explicit end comes from an attached client, which detaches on its
	// way out; it was not a detached session.
	wasDetached := h.client == nil && reason != EndRequested
	if h.client != nil {
		h.client.conn.Close()
		h.client = nil
	}
	rec := TerminalEnded{ID: h.cfg.ID, Owner: h.cfg.Owner, Started: h.started.Unix(), Ended: h.now().Unix(),
		Reason: reason, Detached: wasDetached, LinuxUser: h.user}
	h.mu.Unlock()
	ln.Close()
	rec.Detail = h.endDetail(reason)
	h.logf("session %s ended (%s)%s", h.cfg.ID, reason, endLogSuffix(rec.Detail))
	if out := h.earlyOutput(reason); out != "" {
		h.logf("session %s: first output of login: %s", h.cfg.ID, out)
	}
	_ = writeJSONFile(filepath.Join(h.cfg.Dir, h.cfg.ID+".ended.json"), rec)
	_ = os.Remove(filepath.Join(h.cfg.Dir, h.cfg.ID+".json"))
	_ = os.Remove(h.cfg.Sock)
	if reason == EndStopped || reason == EndExited {
		return // systemd or the child already ended it
	}
	// Hang the session up: SIGHUP to login, then closing the master hangs the
	// terminal up for the shell and whatever runs in the foreground. The
	// unit's KillMode cleans up what is left once this process exits.
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(syscall.SIGHUP)
	}
	_ = h.master.Close()
	select {
	case <-h.childDone:
	case <-time.After(2 * time.Second):
		_ = h.cmd.Process.Kill()
	}
}

// writeStateLocked rewrites <id>.json (h.mu held).
func (h *termHost) writeStateLocked() {
	if h.closing {
		return
	}
	info := TerminalInfo{ID: h.cfg.ID, Owner: h.cfg.Owner, Started: h.started.Unix(),
		Attached: h.client != nil, LinuxUser: h.user}
	if h.client == nil {
		info.DetachedSince = h.detached.Unix()
	}
	_ = writeJSONFile(filepath.Join(h.cfg.Dir, h.cfg.ID+".json"), info)
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// termClientWriteTimeout bounds one write towards the client; a client that
// stalls longer is detached so the PTY keeps draining.
const termClientWriteTimeout = 5 * time.Second

// termDrainTail bounds how long the last output is read after the child has
// exited.
const termDrainTail = 300 * time.Millisecond

// termEarly is how much of the first output is kept for the log of a session
// that ends at once; termEarlyWindow is what "at once" means.
const (
	termEarly       = 2048
	termEarlyWindow = 10 * time.Second
)

// termReadRetry is the pause after a read error on the master while the
// child is alive.
const termReadRetry = 50 * time.Millisecond

// drain moves PTY output into the ring and to the attached client. A read
// error is not the end of the session: util-linux login calls vhangup() on
// its terminal, and until it reopens it every read on the master returns
// EIO. The session ends when the child does (waitEnd); drain only stops
// then, or when the master is closed.
func (h *termHost) drain() {
	defer close(h.masterGone)
	buf := make([]byte, 16<<10)
	for {
		n, err := h.master.Read(buf)
		if n > 0 {
			h.mu.Lock()
			h.ring.Write(buf[:n])
			if room := termEarly - len(h.early); room > 0 {
				h.early = append(h.early, buf[:min(n, room)]...)
			}
			if c := h.client; c != nil {
				_ = c.conn.SetWriteDeadline(time.Now().Add(termClientWriteTimeout))
				if werr := WriteData(c.conn, buf[:n]); werr != nil {
					h.detachLocked(c)
				}
			}
			h.mu.Unlock()
		}
		if err == nil {
			continue
		}
		h.mu.Lock()
		if h.firstReadErr == "" {
			h.firstReadErr = err.Error()
			h.logf("session %s: first read error on the master after %s: %v (the session goes on while the child lives)",
				h.cfg.ID, h.now().Sub(h.started).Round(time.Millisecond), err)
		}
		closing := h.closing
		h.mu.Unlock()
		if closing || errors.Is(err, os.ErrClosed) {
			return
		}
		select {
		case <-h.childDone:
			return
		default:
		}
		time.Sleep(termReadRetry)
	}
}

// describeExit says how the child ended: "login exited with status 1" or
// "login killed by signal hangup".
func describeExit(name string, ps *os.ProcessState, err error) string {
	if ps == nil {
		return fmt.Sprintf("%s: %v", name, err)
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
		switch {
		case ws.Signaled():
			return fmt.Sprintf("%s killed by signal %s", name, strings.ToLower(ws.Signal().String()))
		case ws.Exited():
			return fmt.Sprintf("%s exited with status %d", name, ws.ExitStatus())
		}
	}
	return fmt.Sprintf("%s ended (%s)", name, ps.String())
}

// sanitizeTermOutput keeps printable ASCII, writes newlines as \n and drops
// everything else (escape sequences lose their ESC byte), so that what login
// printed on the terminal is safe to put in a journal line.
func sanitizeTermOutput(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '\n':
			sb.WriteString(`\n`)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// earlyOutput is the sanitized first output when the session ended on its
// own within termEarlyWindow of the start, else "".
func (h *termHost) earlyOutput(reason string) string {
	if reason != EndExited || h.now().Sub(h.started) >= termEarlyWindow {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return sanitizeTermOutput(h.early)
}

// endDetail is the precise cause recorded with an end that came from the
// child ("login exited with status 1 after 0.2s: login: some error"): how it
// ended, when, and a short piece of what it printed if that was early.
func (h *termHost) endDetail(reason string) string {
	if reason != EndExited {
		return ""
	}
	d := h.exitStatus + " after " + h.now().Sub(h.started).Round(100*time.Millisecond).String()
	if out := h.earlyOutput(reason); out != "" {
		if len(out) > 100 {
			out = out[:100]
		}
		d += ": " + out
	}
	return d
}

func endLogSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

func (h *termHost) detachLocked(c *hostClient) {
	if h.client != c {
		return
	}
	c.conn.Close()
	h.client = nil
	h.detached = h.now()
	h.gm.Detach(h.detached)
	h.writeStateLocked()
	h.logf("session %s detached", h.cfg.ID)
}

func (h *termHost) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go h.serve(c)
	}
}

// serve handles one connection. It counts as an attach only once its first
// frame, a resize, has arrived: a connect-and-close (the helper's liveness
// probe) changes nothing.
func (h *termHost) serve(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, p, err := ReadFrame(c)
	if err != nil || typ != FrameResize {
		c.Close()
		return
	}
	rows, cols, err := ParseResize(p)
	if err != nil || !ValidSize(rows, cols) {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	h.mu.Lock()
	if h.client != nil || h.closing {
		h.mu.Unlock()
		c.Close()
		return
	}
	cl := &hostClient{conn: c}
	h.client = cl
	h.gm.Attach()
	_ = h.resize(rows, cols)
	// Replay what the session wrote while nobody was attached, then the user
	// name if it is known; live output follows through drain.
	_ = c.SetWriteDeadline(time.Now().Add(termClientWriteTimeout))
	werr := WriteData(c, h.ring.Bytes())
	if werr == nil && h.user != "" {
		werr = WriteFrame(c, FrameUser, []byte(h.user))
	}
	if werr != nil {
		h.detachLocked(cl)
		h.mu.Unlock()
		return
	}
	h.writeStateLocked()
	h.logf("session %s attached", h.cfg.ID)
	h.mu.Unlock()

	for {
		typ, p, err := ReadFrame(c)
		if err != nil {
			break
		}
		switch typ {
		case FrameData:
			if _, err := h.master.Write(p); err != nil {
				goto out
			}
		case FrameResize:
			rows, cols, err := ParseResize(p)
			if err != nil || !ValidSize(rows, cols) {
				goto out
			}
			_ = h.resize(rows, cols)
		case FrameEnd:
			h.requestEnd(EndRequested)
			goto out
		default:
			goto out
		}
	}
out:
	h.mu.Lock()
	h.detachLocked(cl)
	h.mu.Unlock()
}

func (h *termHost) resize(rows, cols uint16) error {
	return ctl(h.master, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	})
}

// userPoll looks the Linux user up until it is known, then tells the client.
func (h *termHost) userPoll() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for i := 0; i < 300; i++ {
		select {
		case <-h.childDone:
			return
		case <-t.C:
		}
		if u := h.cfg.User(h.line); u != "" {
			h.mu.Lock()
			h.user = u
			if h.client != nil {
				_ = h.client.conn.SetWriteDeadline(time.Now().Add(termClientWriteTimeout))
				_ = WriteFrame(h.client.conn, FrameUser, []byte(u))
			}
			h.writeStateLocked()
			h.mu.Unlock()
			return
		}
	}
}

// lookupTermUser finds the Linux user once login has started their shell (a
// process of the unit's cgroup owned by a non-root uid), or in utmp (which
// also covers a root login). "" while unknown.
func lookupTermUser(unit, line string) string {
	for _, pid := range termUnitPIDs(unit) {
		b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
		if err != nil {
			continue
		}
		uid, ok := parseProcUID(string(b))
		if !ok || uid == 0 {
			continue
		}
		if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
			return u.Username
		}
	}
	if b, err := os.ReadFile(utmpPath); err == nil {
		return utmpUser(b, line)
	}
	return ""
}

// openPTY allocates a pseudo terminal of the given size from the host's
// devpts (the unit has no PrivateDevices).
func openPTY(rows, cols uint16) (master, slave *os.File, pts int, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, 0, err
	}
	fail := func(e error) (*os.File, *os.File, int, error) {
		master.Close()
		return nil, nil, 0, e
	}
	// The master's descriptor is only used inside Control: Fd() would put the
	// file in blocking mode and Close could no longer unblock a Read.
	if err := ctl(master, func(fd int) (e error) {
		if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
			return e
		}
		pts, e = unix.IoctlGetInt(fd, unix.TIOCGPTN)
		if e != nil {
			return e
		}
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	}); err != nil {
		return fail(err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(pts), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	return master, slave, pts, nil
}

func ctl(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}
