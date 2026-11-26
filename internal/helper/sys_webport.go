package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/config"
)

// Move nodespot to port 80 and back (plan 7, option A): a systemd drop-in gives
// the service CAP_NET_BIND_SERVICE and "listen" in nodespot.conf points at port
// 80. The port is never taken from a request: the only target is 80, and
// "back" restores the value saved before the move.
const (
	OpWebPortStatus = "web-port-status" // read only
	OpWebPortSet    = "web-port-set"    // SysRequest.WebPortAction

	WebPortActionMove    = "move"    // to port 80 (refused while something else holds it)
	WebPortActionBack    = "back"    // the saved port, or 8080
	WebPortActionConfirm = "confirm" // the new address answered: stop the revert timer

	// DefaultWebPortState is the helper's own root-only state: the listen
	// value from before the move, the pending marker and the revert note.
	DefaultWebPortState = "/var/lib/nodespot-webport"
	// DefaultDropinDir holds port80.conf, nodespot's own drop-in.
	DefaultDropinDir = "/etc/systemd/system/nodespot.service.d"
	dropinFile       = "port80.conf"
	// DefaultGuardDelay: how long the new address has to answer before the
	// helper moves back (plan 7.4).
	DefaultGuardDelay = 60 * time.Second

	guardUnit       = "nodespot-webport-guard"
	fileWebPrevious = "previous-listen"
	fileWebPending  = "pending"
	fileWebReverted = "reverted"
)

// port80Dropin is the content of port80.conf. The rest of the unit's
// hardening stays; NoNewPrivileges does not block ambient capabilities that
// systemd itself sets.
const port80Dropin = `# Written by nodespot (System tab, Web port). Removed again by "move back" and
# by "apt purge nodespot".
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
`

var (
	ErrPort80Busy      = errors.New("port 80 is in use by another program")
	ErrPort80Unchecked = errors.New("cannot check port 80 (ss is not available)")
	ErrAlreadyPort80   = errors.New("nodespot already uses port 80")
	ErrNotOnPort80     = errors.New("nodespot is not on port 80")
)

func init() {
	registerSysOp(OpWebPortStatus, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.WebPortStatus(ctx), nil
	})
	registerSysOp(OpWebPortSet, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		return o.WebPortSet(ctx, r.WebPortAction)
	})
}

func (o *Ops) webState() string  { return orDefault(o.Svc.StateDir, DefaultWebPortState) }
func (o *Ops) dropinDir() string { return orDefault(o.Svc.DropinDir, DefaultDropinDir) }
func (o *Ops) guardDelay() time.Duration {
	if o.Svc.GuardDelay > 0 {
		return o.Svc.GuardDelay
	}
	return DefaultGuardDelay
}

// WebPortPending is a move that waits for its confirmation.
type WebPortPending struct {
	Deadline string `json:"deadline"` // RFC 3339
	Seconds  int    `json:"seconds"`  // left
}

// WebPortStatus is the response of web-port-status and web-port-set.
type WebPortStatus struct {
	Available bool   `json:"available"` // nodespot.conf could be read
	Listen    string `json:"listen"`
	Port      int    `json:"port"`
	On80      bool   `json:"on80"`
	// CanMove: nodespot is not on 80 and port 80 is free. Reason says why not:
	// "already" (nodespot is on 80), "busy" (another program listens on 80) or
	// "unchecked" (ss is missing); "" when it can.
	CanMove      bool   `json:"canMove"`
	Reason       string `json:"reason"`
	ApacheActive bool   `json:"apacheActive"`
	// DropIn: port80.conf exists. Previous is the listen value saved before
	// the move; BackPort is the port "move back" would use.
	DropIn   bool            `json:"dropIn"`
	Previous string          `json:"previous"`
	BackPort int             `json:"backPort"`
	Pending  *WebPortPending `json:"pending,omitempty"`
	// Reverted: the last move was not confirmed in time and was undone.
	Reverted string `json:"reverted,omitempty"`
	// Firewall: what the move did in firewalld ("" when it did nothing).
	Firewall string `json:"firewall,omitempty"`
}

// listenWithPort returns listen with its port replaced, keeping the address
// (":8080" gives ":80", "192.0.2.10:8080" gives "192.0.2.10:80").
func listenWithPort(listen string, port int) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (o *Ops) readState(name string) string {
	b, err := os.ReadFile(filepath.Join(o.webState(), name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (o *Ops) writeState(name, content string) error {
	if err := os.MkdirAll(o.webState(), 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(o.webState(), name), []byte(content+"\n"))
}

func (o *Ops) removeState(name string) { _ = os.Remove(filepath.Join(o.webState(), name)) }

// savedListen returns the saved pre-move listen value when it is valid and
// not port 80.
func (o *Ops) savedListen() string {
	p := o.readState(fileWebPrevious)
	if n, err := config.ListenPort(p); err != nil || n == 80 {
		return ""
	}
	return p
}

// pending reads the marker (unix seconds of the deadline).
func (o *Ops) pending() *WebPortPending {
	s := o.readState(fileWebPending)
	if s == "" {
		return nil
	}
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	dl := time.Unix(sec, 0)
	left := int(dl.Sub(o.now()).Seconds())
	if left < 0 {
		left = 0
	}
	return &WebPortPending{Deadline: dl.UTC().Format(time.RFC3339), Seconds: left}
}

func (o *Ops) apacheActive(ctx context.Context) bool {
	out, code, err := o.Run.Run(ctx, "systemctl", "is-active", "apache2.service")
	return err == nil && code == 0 && strings.TrimSpace(out) == "active"
}

// WebPortStatus reads where nodespot listens and whether it can move to port 80.
func (o *Ops) WebPortStatus(ctx context.Context) WebPortStatus {
	res := WebPortStatus{}
	if c, err := config.Load(o.nodespotConf()); err == nil {
		if p, err := config.ListenPort(c.Listen); err == nil {
			res.Available, res.Listen, res.Port, res.On80 = true, c.Listen, p, p == 80
		}
	}
	res.Previous = o.readState(fileWebPrevious)
	res.BackPort = 8080
	if n, err := config.ListenPort(o.savedListen()); err == nil {
		res.BackPort = n
	}
	if _, err := os.Stat(filepath.Join(o.dropinDir(), dropinFile)); err == nil {
		res.DropIn = true
	}
	res.Pending = o.pending()
	res.Reverted = o.readState(fileWebReverted)
	res.ApacheActive = o.apacheActive(ctx)
	switch {
	case !res.Available:
		res.Reason = "unavailable"
	case res.On80:
		res.Reason = "already"
	default:
		if ls, ok := o.listeners(ctx); !ok {
			res.Reason = "unchecked"
		} else if listensOn(ls, 80) {
			res.Reason = "busy"
		}
	}
	res.CanMove = res.Available && res.Reason == ""
	return res
}

func listensOn(ls []SockListener, port int) bool {
	for _, l := range ls {
		if l.Port == port {
			return true
		}
	}
	return false
}

// WebPortSet runs one of the fixed actions and returns the new status.
func (o *Ops) WebPortSet(ctx context.Context, action string) (WebPortStatus, error) {
	switch action {
	case WebPortActionMove:
		return o.webPortMove(ctx)
	case WebPortActionBack:
		return o.webPortBack(ctx)
	case WebPortActionConfirm:
		return o.webPortConfirm(ctx)
	}
	return WebPortStatus{}, errors.New("invalid action")
}

// setListen writes listen into the [server] section of nodespot.conf, keeping
// everything else in the file, its owner and its mode.
func (o *Ops) setListen(listen string) error {
	path := o.nodespotConf()
	data, err := os.ReadFile(path)
	mode, uid, gid := fs.FileMode(0o644), 0, 0
	switch {
	case err == nil:
		if fi, serr := os.Stat(path); serr == nil {
			mode = fi.Mode().Perm()
			if u, g, ok := ownerIDs(fi); ok {
				uid, gid = u, g
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		data = nil
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	default:
		return err
	}
	out, _, _, _ := keepListen(data, []byte("[server]\nlisten = "+listen+"\n"))
	if got, _ := confListen(out); got != listen {
		return errors.New("cannot set listen in " + path)
	}
	if bytes.Equal(out, data) {
		return nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(parent, filepath.Base(path)), parent, out, uid, gid, mode)
}

func (o *Ops) writeDropin() error {
	if err := os.MkdirAll(o.dropinDir(), 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(o.dropinDir(), dropinFile), []byte(port80Dropin))
}

func (o *Ops) removeDropin() error {
	if err := os.Remove(filepath.Join(o.dropinDir(), dropinFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_ = os.Remove(o.dropinDir()) // only if empty
	return nil
}

// restartNodespot reloads systemd and restarts the web service without waiting,
// so the request is answered before systemd stops the process that asked.
func (o *Ops) restartNodespot(ctx context.Context) error {
	if err := o.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return o.systemctl(ctx, "--no-block", "restart", "nodespot.service")
}

// stopGuard cancels the revert timer; errors are ignored (it may not exist).
func (o *Ops) stopGuard(ctx context.Context) {
	_, _, _ = o.Run.Run(ctx, "systemctl", "stop", guardUnit+".timer", guardUnit+".service")
}

// startGuard arms the revert: a transient timer that runs the helper's
// web-port-guard step after the delay, in its own unit so it survives the
// restart of nodespot and of the helper itself.
func (o *Ops) startGuard(ctx context.Context) error {
	o.stopGuard(ctx)
	argv := []string{"systemd-run", "--unit=" + guardUnit, fmt.Sprintf("--on-active=%ds", int(o.guardDelay().Seconds())),
		"--collect", "--property=Type=oneshot", o.selfPath(), "web-port-guard"}
	out, code, err := o.Run.Run(ctx, argv...)
	if err != nil {
		return fmt.Errorf("systemd-run: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("systemd-run failed: %s", LastLines(out, 3))
	}
	return nil
}

// revertFiles undoes the move on disk: the drop-in goes and, when nodespot.conf
// still says port 80, listen returns to the saved value (or 8080).
func (o *Ops) revertFiles() error {
	var firstErr error
	if c, err := config.Load(o.nodespotConf()); err == nil {
		if p, err := config.ListenPort(c.Listen); err == nil && p == 80 {
			target := o.savedListen()
			if target == "" {
				target = listenWithPort(c.Listen, 8080)
			}
			if err := o.setListen(target); err != nil {
				firstErr = err
			}
		}
	}
	if err := o.removeDropin(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (o *Ops) webPortMove(ctx context.Context) (WebPortStatus, error) {
	o.Svc.mu.Lock()
	defer o.Svc.mu.Unlock()
	c, err := config.Load(o.nodespotConf())
	if err != nil {
		return WebPortStatus{}, err
	}
	cur, err := config.ListenPort(c.Listen)
	if err != nil {
		return WebPortStatus{}, err
	}
	if cur == 80 {
		return WebPortStatus{}, ErrAlreadyPort80
	}
	ls, ok := o.listeners(ctx)
	if !ok {
		return WebPortStatus{}, ErrPort80Unchecked
	}
	if listensOn(ls, 80) {
		return WebPortStatus{}, ErrPort80Busy
	}
	if err := o.writeState(fileWebPrevious, c.Listen); err != nil {
		return WebPortStatus{}, fmt.Errorf("cannot save the old port: %w", err)
	}
	o.removeState(fileWebReverted)
	if err := o.writeDropin(); err != nil {
		return WebPortStatus{}, fmt.Errorf("cannot write the drop-in: %w", err)
	}
	rollback := func() {
		o.removeState(fileWebPending)
		o.stopGuard(ctx)
		_ = o.revertFiles()
		_ = o.systemctl(ctx, "daemon-reload")
	}
	if err := o.setListen(listenWithPort(c.Listen, 80)); err != nil {
		rollback()
		return WebPortStatus{}, fmt.Errorf("cannot change nodespot.conf: %w", err)
	}
	// The marker first, then the timer that acts on it.
	deadline := o.now().Add(o.guardDelay())
	if err := o.writeState(fileWebPending, strconv.FormatInt(deadline.Unix(), 10)); err != nil {
		rollback()
		return WebPortStatus{}, fmt.Errorf("cannot write the marker: %w", err)
	}
	if err := o.systemctl(ctx, "daemon-reload"); err != nil {
		rollback()
		return WebPortStatus{}, err
	}
	if err := o.startGuard(ctx); err != nil {
		rollback()
		return WebPortStatus{}, fmt.Errorf("cannot arm the revert timer: %w", err)
	}
	fw := o.openPort80(ctx)
	if err := o.systemctl(ctx, "--no-block", "restart", "nodespot.service"); err != nil {
		rollback()
		return WebPortStatus{}, err
	}
	st := o.WebPortStatus(ctx)
	st.Firewall = fw
	return st, nil
}

// openPort80 opens 80/tcp in firewalld when it runs (the old port stays open
// until the operator confirms; the rule is permanent like the installer's).
// The result is a short note for the UI ("" when firewalld is not running).
func (o *Ops) openPort80(ctx context.Context) string {
	running, _ := o.fwServices(ctx)
	if !running {
		return ""
	}
	for _, argv := range [][]string{{"firewall-cmd", "--permanent", "--add-port=80/tcp"}, {"firewall-cmd", "--reload"}} {
		if out, code, err := o.Run.Run(ctx, argv...); err != nil || code != 0 {
			return "could not open TCP 80 in firewalld: " + LastLines(out, 2)
		}
	}
	return "opened TCP 80 in firewalld"
}

func (o *Ops) webPortBack(ctx context.Context) (WebPortStatus, error) {
	o.Svc.mu.Lock()
	defer o.Svc.mu.Unlock()
	c, err := config.Load(o.nodespotConf())
	if err != nil {
		return WebPortStatus{}, err
	}
	cur, err := config.ListenPort(c.Listen)
	if err != nil {
		return WebPortStatus{}, err
	}
	_, dropErr := os.Stat(filepath.Join(o.dropinDir(), dropinFile))
	if cur != 80 && dropErr != nil {
		return WebPortStatus{}, ErrNotOnPort80
	}
	o.stopGuard(ctx)
	o.removeState(fileWebPending)
	o.removeState(fileWebReverted)
	if err := o.revertFiles(); err != nil {
		return WebPortStatus{}, err
	}
	if err := o.restartNodespot(ctx); err != nil {
		return WebPortStatus{}, err
	}
	return o.WebPortStatus(ctx), nil
}

func (o *Ops) webPortConfirm(ctx context.Context) (WebPortStatus, error) {
	o.Svc.mu.Lock()
	defer o.Svc.mu.Unlock()
	if o.pending() != nil {
		o.removeState(fileWebPending)
		o.stopGuard(ctx)
	}
	o.removeState(fileWebReverted)
	return o.WebPortStatus(ctx), nil
}

// WebPortGuard is the step of the revert timer (nodespot-helper web-port-guard):
// when the marker is still there nobody confirmed the new address, so the
// move is undone. Without a marker it does nothing.
func (o *Ops) WebPortGuard(ctx context.Context) error {
	o.Svc.mu.Lock()
	defer o.Svc.mu.Unlock()
	if o.readState(fileWebPending) == "" {
		return nil
	}
	o.removeState(fileWebPending)
	err := o.revertFiles()
	if rerr := o.restartNodespot(ctx); err == nil {
		err = rerr
	}
	note := "the move to port 80 was not confirmed in time and was undone"
	if err != nil {
		note += " (with errors: " + err.Error() + ")"
	}
	_ = o.writeState(fileWebReverted, note)
	return err
}
