package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// DefaultSocket is the control socket. There is no TCP listener: the web
// app talks to the node through this socket only.
const DefaultSocket = "/run/nodespot-node/control.sock"

// MaxRequest bounds one request line.
const MaxRequest = 1 << 20

// Request is one control request, one JSON object per line.
type Request struct {
	ID   uint64          `json:"id,omitempty"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response answers a Request with the same ID.
type Response struct {
	ID    uint64 `json:"id,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// MaxDirResults bounds one directory search.
const MaxDirResults = 200

// Control ops.
const (
	OpStatus        = "status"       // -> Status
	OpModes         = "modes"        // -> []ModeInfo (passwords never returned)
	OpSaveStation   = "save_station" // {station} -> Status
	OpSaveMode      = "save_mode"    // {mode, settings} -> ModeInfo list
	OpDeleteMode    = "delete_mode"  // {mode}
	OpSelect        = "select"       // {mode, target?} -> Status ("" or "none": idle)
	OpConnect       = "connect"      // {target} on the active mode -> Status
	OpDisconnect    = "disconnect"   // -> Status
	OpLastHeard     = "last_heard"   // {limit?} -> []mode.Heard
	OpNowCalling    = "now_calling"  // -> *Calling (null when nobody is talking)
	OpRadioLevels   = "radio_levels" // -> radio.Levels
	OpInputLevel    = "input_level"  // -> InputLevel
	OpRadioTune     = "radio_tune"   // {rxVolume?, txVolume?, rxBoost?} -> radio.Levels
	OpRadioProg     = "radio_program"
	OpRadioInfo     = "radio_info"           // -> RadioDetail
	OpTestTone      = "test_tone"            // {seconds?, stop?} -> Status (seconds 0: 5, at most 10)
	OpAutosetRx     = "autoset_rx"           // {cancel?} -> AutosetStatus (starts, or cancels, the receive auto-set)
	OpAutosetStat   = "autoset_status"       // -> AutosetStatus
	OpTuningDismiss = "radio_tuning_dismiss" // -> Status (dismisses the "tune your audio" reminder)
	OpConfig        = "config"               // -> the station settings (no mode passwords)
	OpReload        = "reload"               // read the config file again
	OpDirSearch     = "directory_search"     // {mode, q, limit?} -> mode.DirResult (EchoLink's station list)
	OpSubscribe     = "subscribe"            // the connection then streams Event lines
)

// Server serves the control API on a Unix socket.
type Server struct {
	e     *Engine
	log   *slog.Logger
	allow func(uid int) bool
}

// ServerOptions configures a Server.
type ServerOptions struct {
	// Allow decides which peer uids may connect (default: root and our
	// own uid; the socket's mode and group do the rest).
	Allow func(uid int) bool
	Log   *slog.Logger
}

// NewServer returns a control server for e.
func NewServer(e *Engine, o ServerOptions) *Server {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Allow == nil {
		me := os.Getuid()
		o.Allow = func(uid int) bool { return uid == 0 || uid == me }
	}
	return &Server{e: e, log: o.Log, allow: o.Allow}
}

// Listen creates the Unix socket at path with mode 0660, replacing a stale
// one.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("control socket: %w", err)
	}
	return ln, nil
}

// Serve accepts connections on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() { <-ctx.Done(); ln.Close() }()
	var wg sync.WaitGroup
	defer wg.Wait()
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
			return fmt.Errorf("control socket: %w", err)
		}
		uid, err := peerUID(c)
		if err != nil || !s.allow(uid) {
			s.log.Warn("control: refused a connection", "uid", uid, "err", err)
			c.Close()
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); s.conn(ctx, c) }()
	}
}

type lineWriter struct {
	mu sync.Mutex
	c  net.Conn
}

func (w *lineWriter) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = w.c.Write(b)
	return err
}

func (s *Server) conn(ctx context.Context, c net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); c.Close() }()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), MaxRequest)
	w := &lineWriter{c: c}
	for sc.Scan() {
		var req Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = w.send(Response{Error: "bad request: " + err.Error()})
			continue
		}
		if req.Op == OpSubscribe {
			s.subscribe(ctx, w, req)
			return
		}
		if err := w.send(s.Do(ctx, req)); err != nil {
			return
		}
	}
}

func (s *Server) subscribe(ctx context.Context, w *lineWriter, req Request) {
	evs, stop := s.e.Subscribe()
	defer stop()
	if err := w.send(Response{ID: req.ID, OK: true, Data: s.e.Status()}); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-evs:
			if err := w.send(struct {
				Event Event `json:"event"`
			}{ev}); err != nil {
				return
			}
		}
	}
}

func args(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// Do runs one request. It is the whole control API, without the socket.
func (s *Server) Do(ctx context.Context, req Request) Response {
	data, err := s.do(ctx, req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	return Response{ID: req.ID, OK: true, Data: data}
}

func (s *Server) do(ctx context.Context, req Request) (any, error) {
	e := s.e
	switch req.Op {
	case OpStatus:
		return e.Status(), nil
	case OpModes:
		return e.Modes(), nil
	case OpConfig:
		c := e.Config()
		return c.Station, nil
	case OpSaveStation:
		var a struct {
			Station Station `json:"station"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if err := e.SaveStation(a.Station); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpSaveMode:
		var a struct {
			Mode     string        `json:"mode"`
			Settings mode.Settings `json:"settings"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if err := e.SaveMode(a.Mode, a.Settings); err != nil {
			return nil, err
		}
		return e.Modes(), nil
	case OpDeleteMode:
		var a struct {
			Mode string `json:"mode"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		return nil, e.DeleteMode(ctx, a.Mode)
	case OpSelect:
		var a struct {
			Mode   string       `json:"mode"`
			Target *mode.Target `json:"target"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if err := e.Select(ctx, a.Mode, a.Target); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpConnect:
		var a struct {
			Target mode.Target `json:"target"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if err := e.Connect(ctx, a.Target); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpDisconnect:
		if err := e.Disconnect(ctx); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpLastHeard:
		var a struct {
			Limit int `json:"limit"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		return e.Heard(a.Limit), nil
	case OpNowCalling:
		return e.NowCalling(), nil
	case OpRadioLevels:
		return e.Levels(), nil
	case OpInputLevel:
		return e.InputLevel(), nil
	case OpRadioTune:
		var a struct {
			RxVolume *float64 `json:"rxVolume"`
			TxVolume *float64 `json:"txVolume"`
			RxBoost  *bool    `json:"rxBoost"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if err := e.Tune(a.RxVolume, a.TxVolume, a.RxBoost); err != nil {
			return nil, err
		}
		return e.Levels(), nil
	case OpRadioInfo:
		return e.RadioInfo(), nil
	case OpTuningDismiss:
		if err := e.DismissTuning(); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpAutosetStat:
		return e.AutosetStatus(), nil
	case OpAutosetRx:
		var a struct {
			Cancel bool `json:"cancel"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if a.Cancel {
			return e.AutosetCancel(), nil
		}
		return e.AutosetRx()
	case OpTestTone:
		var a struct {
			Seconds float64 `json:"seconds"`
			Stop    bool    `json:"stop"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if a.Stop {
			e.StopTestTone()
			return e.Status(), nil
		}
		if math.IsNaN(a.Seconds) || a.Seconds < 0 || a.Seconds > MaxTestTone.Seconds() {
			return nil, ErrToneLength
		}
		if err := e.TestTone(time.Duration(a.Seconds * float64(time.Second))); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpRadioProg:
		if err := e.ProgramRadio(); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case OpDirSearch:
		var a struct {
			Mode  string `json:"mode"`
			Q     string `json:"q"`
			Limit int    `json:"limit"`
		}
		if err := args(req.Args, &a); err != nil {
			return nil, err
		}
		if a.Limit <= 0 || a.Limit > MaxDirResults {
			a.Limit = MaxDirResults
		}
		return e.SearchDirectory(ctx, a.Mode, a.Q, a.Limit)
	case OpReload:
		if err := e.Reload(ctx); err != nil {
			return nil, err
		}
		return e.Status(), nil
	case "":
		return nil, errors.New("no op")
	}
	return nil, fmt.Errorf("unknown op %q", req.Op)
}
