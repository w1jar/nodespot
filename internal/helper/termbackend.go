package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TermBackend is what the helper's terminal ops need from the sessions: they
// live in term-host processes, one per transient unit, and the helper only
// connects to them.
type TermBackend interface {
	// Open starts a session and returns its id and a connection to its
	// term-host (first frame, the size, already sent).
	Open(ctx context.Context, rows, cols uint16, owner int64) (string, net.Conn, error)
	// Attach connects to a live, detached session of owner.
	Attach(ctx context.Context, id string, owner int64, rows, cols uint16) (net.Conn, error)
	// List returns the live sessions and the unacknowledged ended ones.
	List(ctx context.Context) (TerminalList, error)
	// Ack forgets ended records.
	Ack(ids []string)
	// End stops the session's unit.
	End(ctx context.Context, id string) error
	// Adopt runs when the helper starts: sessions with a reachable term-host
	// stay as they are (detached from the helper's point of view); units
	// without one are stopped.
	Adopt(ctx context.Context) (adopted, stopped int, err error)
}

// selfHelperPath is the helper binary term-host is started from.
const selfHelperPath = "/usr/libexec/nodespot/nodespot-helper"

type termBackend struct {
	run  Runner
	self string
	dir  string
	// ready bounds the wait for a new term-host's socket.
	ready time.Duration
}

// newTermBackend returns the production backend.
func newTermBackend(run Runner) TermBackend {
	self := selfHelperPath
	if _, err := os.Stat(self); err != nil {
		if exe, err := os.Executable(); err == nil {
			self = strings.TrimSuffix(exe, " (deleted)")
		}
	}
	return &termBackend{run: run, self: self, dir: termDir, ready: 10 * time.Second}
}

func (b *termBackend) ensureDir() error {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(b.dir, 0o700)
}

func (b *termBackend) sock(id string) string { return filepath.Join(b.dir, id+".sock") }

// dial connects to the term-host of id and sends the size (which is what
// makes the connection an attach).
func (b *termBackend) dial(ctx context.Context, id string, rows, cols uint16) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", b.sock(id))
	if err != nil {
		return nil, err
	}
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := WriteResize(c, rows, cols); err != nil {
		c.Close()
		return nil, err
	}
	_ = c.SetWriteDeadline(time.Time{})
	return c, nil
}

// probe says whether a term-host answers on the socket of id, without
// attaching (a connection that sends nothing is not an attach).
func (b *termBackend) probe(id string) (alive bool, gone bool) {
	c, err := net.DialTimeout("unix", b.sock(id), 2*time.Second)
	if err == nil {
		c.Close()
		return true, false
	}
	return false, errors.Is(err, os.ErrNotExist) || isConnRefused(err)
}

func (b *termBackend) Open(ctx context.Context, rows, cols uint16, owner int64) (string, net.Conn, error) {
	id, err := newTermID()
	if err != nil {
		return "", nil, fmt.Errorf("terminal not available: %w", err)
	}
	if err := b.ensureDir(); err != nil {
		return "", nil, fmt.Errorf("terminal not available: %w", err)
	}
	argv, err := termUnitArgv(id, b.self, rows, cols, owner)
	if err != nil {
		return "", nil, fmt.Errorf("terminal not available: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, terminalStartLimit)
	defer cancel()
	out, code, err := b.run.Run(rctx, argv...)
	if err != nil {
		_ = stopTermUnit(b.run, id)
		return "", nil, fmt.Errorf("terminal not available: systemd-run: %w", err)
	}
	if code != 0 {
		return "", nil, fmt.Errorf("terminal not available: systemd-run failed: %s", LastLines(out, 3))
	}
	deadline := time.Now().Add(b.ready)
	for {
		c, err := b.dial(ctx, id, rows, cols)
		if err == nil {
			return id, c, nil
		}
		// A session that is already over (login exited at once) leaves its
		// record; its reason is what the operator needs to see.
		if why := b.startFailure(id); why != "" {
			_ = stopTermUnit(b.run, id)
			return "", nil, errors.New("terminal not available: the session did not start (" + why + ")")
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			_ = stopTermUnit(b.run, id)
			return "", nil, errors.New("terminal not available: the session did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startFailure reads and removes the ended record of a session that never
// came up and returns its short reason ("login exited with status 1 after
// 0.2s: login: some error"), or "" when there is none. The record is removed
// because the failure is reported to the caller directly.
func (b *termBackend) startFailure(id string) string {
	path := filepath.Join(b.dir, id+".ended.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var rec TerminalEnded
	if json.Unmarshal(data, &rec) != nil || rec.ID != id {
		return ""
	}
	_ = os.Remove(path)
	why := rec.Detail
	if why == "" {
		why = "ended: " + rec.Reason
	}
	return why
}

func (b *termBackend) readInfo(id string) (TerminalInfo, error) {
	var info TerminalInfo
	data, err := os.ReadFile(filepath.Join(b.dir, id+".json"))
	if err != nil {
		return info, errors.New("no such session")
	}
	if json.Unmarshal(data, &info) != nil || info.ID != id {
		return info, errors.New("no such session")
	}
	return info, nil
}

func (b *termBackend) Attach(ctx context.Context, id string, owner int64, rows, cols uint16) (net.Conn, error) {
	if !termIDRe.MatchString(id) {
		return nil, errors.New("invalid session")
	}
	info, err := b.readInfo(id)
	if err != nil {
		return nil, err
	}
	if info.Owner != owner {
		return nil, errors.New("not your session")
	}
	if info.Attached {
		return nil, errors.New("session is in use")
	}
	c, err := b.dial(ctx, id, rows, cols)
	if err != nil {
		return nil, errors.New("no such session")
	}
	return c, nil
}

func (b *termBackend) List(ctx context.Context) (TerminalList, error) {
	res := TerminalList{Sessions: []TerminalInfo{}}
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	for _, e := range entries {
		name := e.Name()
		if id, ok := strings.CutSuffix(name, ".ended.json"); ok {
			var rec TerminalEnded
			data, err := os.ReadFile(filepath.Join(b.dir, name))
			if err != nil || !termIDRe.MatchString(id) || json.Unmarshal(data, &rec) != nil || rec.ID != id {
				continue
			}
			if time.Since(time.Unix(rec.Ended, 0)) > termEndedKeep {
				_ = os.Remove(filepath.Join(b.dir, name))
				continue
			}
			res.Ended = append(res.Ended, rec)
			continue
		}
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || !termIDRe.MatchString(id) {
			continue
		}
		info, err := b.readInfo(id)
		if err != nil {
			continue
		}
		alive, gone := b.probe(id)
		if !alive {
			if gone { // term-host died without cleaning up
				_ = os.Remove(filepath.Join(b.dir, name))
				_ = os.Remove(b.sock(id))
			}
			continue
		}
		res.Sessions = append(res.Sessions, info)
	}
	return res, nil
}

func (b *termBackend) Ack(ids []string) {
	for _, id := range ids {
		if termIDRe.MatchString(id) {
			_ = os.Remove(filepath.Join(b.dir, id+".ended.json"))
		}
	}
}

func (b *termBackend) End(ctx context.Context, id string) error {
	return stopTermUnit(b.run, id)
}

func (b *termBackend) Adopt(ctx context.Context) (adopted, stopped int, err error) {
	if err := b.ensureDir(); err != nil {
		return 0, 0, err
	}
	units, err := listTermUnits(ctx, b.run)
	if err != nil {
		return 0, 0, err
	}
	for _, u := range units {
		id := strings.TrimSuffix(strings.TrimPrefix(u, termUnitPrefix), termUnitSuffix)
		if alive, _ := b.probe(id); alive {
			adopted++
			continue
		}
		// A unit without a reachable term-host is broken (or still starting
		// up under an earlier helper, which cannot be: nothing is started
		// before the helper accepts connections).
		if e := stopTermUnit(b.run, id); e != nil {
			err = e
			continue
		}
		stopped++
	}
	// Files of sessions whose unit is gone.
	_, _ = b.List(ctx)
	return adopted, stopped, err
}
