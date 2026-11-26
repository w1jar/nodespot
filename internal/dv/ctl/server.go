package ctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"sync"
	"time"
)

const (
	// DefaultPing is the interval of ping events on subscriber connections.
	DefaultPing = 15 * time.Second
	// DefaultIdle closes a request connection that sends nothing.
	DefaultIdle = 5 * time.Minute
	// SubscriberBuffer is the event buffer of one subscriber.
	SubscriberBuffer = 256

	writeTimeout = 5 * time.Second
)

// Handler executes control ops. The bridge router implements it.
type Handler interface {
	Status(ctx context.Context) (Status, error)
	Mode(ctx context.Context, a ModeArgs) (Status, error)
	Tune(ctx context.Context, a TuneArgs) (Status, error)
	Unlink(ctx context.Context, a UnlinkArgs) (Status, error)
	Reload(ctx context.Context) (Status, error)
}

// ServerOptions configures a Server.
type ServerOptions struct {
	// AllowUIDs are the peer uids that may connect. Empty means root and
	// the user "nodespot" (looked up when Serve starts).
	AllowUIDs []int
	Ping      time.Duration // default DefaultPing
	Idle      time.Duration // default DefaultIdle
	Log       *log.Logger
}

// Server serves the control socket.
type Server struct {
	h   Handler
	o   ServerOptions
	log *log.Logger

	mu    sync.Mutex
	subs  map[*subscriber]struct{}
	conns map[net.Conn]struct{}
	allow map[int]bool
	wg    sync.WaitGroup
}

type subscriber struct {
	ch       chan Event
	overflow chan struct{}
	once     sync.Once
}

// NewServer builds a Server around h.
func NewServer(h Handler, o ServerOptions) *Server {
	if o.Ping <= 0 {
		o.Ping = DefaultPing
	}
	if o.Idle <= 0 {
		o.Idle = DefaultIdle
	}
	l := o.Log
	if l == nil {
		l = log.New(io.Discard, "", 0)
	}
	return &Server{h: h, o: o, log: l, subs: map[*subscriber]struct{}{}, conns: map[net.Conn]struct{}{}}
}

// Listen creates the unix socket at path with mode 0660, replacing a stale
// socket file.
func Listen(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Publish sends ev to every subscriber without blocking. A subscriber whose
// buffer is full is dropped with an overflow event.
func (s *Server) Publish(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
			delete(s.subs, sub)
			sub.once.Do(func() { close(sub.overflow) })
		}
	}
}

func (s *Server) allowed(uid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allow[uid]
}

func (s *Server) buildAllow() {
	m := map[int]bool{}
	if len(s.o.AllowUIDs) > 0 {
		for _, u := range s.o.AllowUIDs {
			m[u] = true
		}
	} else {
		m[0] = true
		if u, err := user.Lookup("nodespot"); err == nil {
			if n, err := strconv.Atoi(u.Uid); err == nil {
				m[n] = true
			}
		}
	}
	s.mu.Lock()
	s.allow = m
	s.mu.Unlock()
}

// Serve accepts connections on ln until ctx ends, then closes everything.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.buildAllow()
	stop := context.AfterFunc(ctx, func() {
		ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
	})
	defer stop()
	defer s.wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		if ctx.Err() != nil {
			c.Close()
		}
		go s.conn(ctx, c)
	}
}

type connWriter struct {
	mu sync.Mutex
	c  net.Conn
}

func (w *connWriter) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	w.c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = w.c.Write(b)
	return err
}

func fail(id uint64, code, msg string) Response {
	return Response{ID: id, Code: code, Error: msg}
}

func (s *Server) conn(ctx context.Context, c net.Conn) {
	defer s.wg.Done()
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	w := &connWriter{c: c}
	uid, err := peerUID(c)
	if err != nil || !s.allowed(uid) {
		s.log.Printf("ctl: rejected connection (uid %d, err %v)", uid, err)
		w.send(fail(0, CodeForbidden, "forbidden"))
		return
	}
	br := bufio.NewReaderSize(c, 4096)
	for {
		c.SetReadDeadline(time.Now().Add(s.o.Idle))
		line, err := readLine(br)
		if err != nil {
			if errors.Is(err, ErrLineTooLong) {
				w.send(fail(0, CodeBadRequest, "line too long"))
			}
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			if w.send(fail(0, CodeBadRequest, "bad json")) != nil {
				return
			}
			continue
		}
		if req.Op == OpSubscribe {
			s.subscribe(ctx, c, br, w, req)
			return
		}
		resp := s.dispatch(ctx, req)
		if w.send(resp) != nil {
			return
		}
	}
}

func statusResp(id uint64, st Status, err error) Response {
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return fail(id, ce.Code, ce.Msg)
		}
		return fail(id, CodeInternal, err.Error())
	}
	b, _ := json.Marshal(st)
	return Response{ID: id, OK: true, Result: b}
}

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	bad := func(msg string) Response { return fail(req.ID, CodeBadRequest, msg) }
	switch req.Op {
	case OpPing:
		return Response{ID: req.ID, OK: true}
	case OpStatus:
		st, err := s.h.Status(ctx)
		return statusResp(req.ID, st, err)
	case OpReload:
		st, err := s.h.Reload(ctx)
		return statusResp(req.ID, st, err)
	case OpMode:
		var a ModeArgs
		if err := decodeArgs(req.Args, &a); err != nil {
			return bad("bad args")
		}
		st, err := s.h.Mode(ctx, a)
		return statusResp(req.ID, st, err)
	case OpTune:
		var a TuneArgs
		if err := decodeArgs(req.Args, &a); err != nil || a.Mode == "" {
			return bad("bad args")
		}
		st, err := s.h.Tune(ctx, a)
		return statusResp(req.ID, st, err)
	case OpUnlink:
		var a UnlinkArgs
		if err := decodeArgs(req.Args, &a); err != nil {
			return bad("bad args")
		}
		st, err := s.h.Unlink(ctx, a)
		return statusResp(req.ID, st, err)
	}
	return bad("unknown op")
}

func (s *Server) subscribe(ctx context.Context, c net.Conn, br *bufio.Reader, w *connWriter, req Request) {
	sub := &subscriber{ch: make(chan Event, SubscriberBuffer), overflow: make(chan struct{})}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}()
	st, err := s.h.Status(ctx)
	resp := statusResp(req.ID, st, err)
	if w.send(resp) != nil || !resp.OK {
		return
	}
	// A subscriber has no idle timeout. Further requests are rejected.
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			c.SetReadDeadline(time.Time{})
			line, err := readLine(br)
			if err != nil {
				return
			}
			var r Request
			json.Unmarshal(line, &r)
			if w.send(fail(r.ID, CodeBadRequest, "connection is subscribed")) != nil {
				return
			}
		}
	}()
	ping := time.NewTicker(s.o.Ping)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case <-sub.overflow:
			w.send(Event{Type: EvOverflow, At: time.Now()})
			return
		case ev := <-sub.ch:
			if w.send(ev) != nil {
				return
			}
		case <-ping.C:
			if w.send(Event{Type: EvPing, At: time.Now()}) != nil {
				return
			}
		}
	}
}
