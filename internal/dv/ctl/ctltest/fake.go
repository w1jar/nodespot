// Package ctltest is a fake nodespot-dv on a temporary unix socket, for tests
// of code that uses ctl.Client.
package ctltest

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
)

// Fake answers the ctl protocol from canned state.
type Fake struct {
	path string
	ln   net.Listener

	mu     sync.Mutex
	status ctl.Status
	reqs   []ctl.Request
	subs   map[chan ctl.Event]struct{}
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// New starts a fake on a temp socket; it is closed at test cleanup.
func New(t testing.TB) *Fake {
	t.Helper()
	// Unix socket paths are short (108 bytes): use a short temp dir.
	dir, err := os.MkdirTemp("", "dvctl")
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{
		path:   filepath.Join(dir, "ctl.sock"),
		subs:   map[chan ctl.Event]struct{}{},
		conns:  map[net.Conn]struct{}{},
		status: ctl.Status{Version: "test", Started: time.Now()},
	}
	f.ln, err = net.Listen("unix", f.path)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	f.wg.Add(1)
	go f.accept()
	t.Cleanup(func() {
		f.Close()
		os.RemoveAll(dir)
	})
	return f
}

// Path is the socket path.
func (f *Fake) Path() string { return f.path }

// SetStatus replaces the status returned by every op.
func (f *Fake) SetStatus(s ctl.Status) {
	f.mu.Lock()
	f.status = s
	f.mu.Unlock()
}

// Emit sends ev to every subscriber.
func (f *Fake) Emit(ev ctl.Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Requests returns a copy of the requests received so far.
func (f *Fake) Requests() []ctl.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ctl.Request(nil), f.reqs...)
}

// Close stops the fake and drops all connections.
func (f *Fake) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.ln.Close()
	for c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

func (f *Fake) accept() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			c.Close()
			return
		}
		f.conns[c] = struct{}{}
		f.wg.Add(1)
		f.mu.Unlock()
		go f.serve(c)
	}
}

func (f *Fake) serve(c net.Conn) {
	defer f.wg.Done()
	defer func() {
		c.Close()
		f.mu.Lock()
		delete(f.conns, c)
		f.mu.Unlock()
	}()
	br := bufio.NewReaderSize(c, ctl.MaxLine)
	enc := json.NewEncoder(c)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var req ctl.Request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(ctl.Response{Code: ctl.CodeBadRequest, Error: "bad json"})
			return
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		st := f.status
		f.mu.Unlock()
		res, _ := json.Marshal(st)
		resp := ctl.Response{ID: req.ID, OK: true, Result: res}
		switch req.Op {
		case ctl.OpPing:
			resp.Result = nil
		case ctl.OpStatus, ctl.OpReload, ctl.OpMode, ctl.OpTune, ctl.OpUnlink:
		case ctl.OpSubscribe:
			ch := make(chan ctl.Event, 256)
			f.mu.Lock()
			f.subs[ch] = struct{}{}
			f.mu.Unlock()
			defer func() {
				f.mu.Lock()
				delete(f.subs, ch)
				f.mu.Unlock()
			}()
			if enc.Encode(resp) != nil {
				return
			}
			gone := make(chan struct{})
			go func() {
				br.ReadByte() // returns when the client hangs up or Close runs
				close(gone)
			}()
			for {
				select {
				case ev := <-ch:
					if enc.Encode(ev) != nil {
						return
					}
				case <-gone:
					return
				}
			}
		default:
			resp = ctl.Response{ID: req.ID, Code: ctl.CodeBadRequest, Error: "unknown op"}
		}
		if enc.Encode(resp) != nil {
			return
		}
	}
}
