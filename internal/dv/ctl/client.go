package ctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

// dialTimeout bounds connect plus one request/response round trip when the
// caller's context has no earlier deadline.
const dialTimeout = 2 * time.Second

// enterTimeout bounds mode and tune: entering an on-demand network waits for
// its login (the engine gives up after bridge.DefaultLoginWait, 12 s).
const enterTimeout = 20 * time.Second

// Error is returned for a response with ok:false.
type Error struct{ Code, Msg string }

func (e *Error) Error() string {
	if e.Msg == "" {
		return "nodespot-dv: " + e.Code
	}
	return "nodespot-dv: " + e.Code + ": " + e.Msg
}

// Client talks to nodespot-dv's control socket: one short connection per
// request, plus Subscribe for the event stream.
type Client struct {
	path string
	id   atomic.Uint64
}

// NewClient returns a client for the socket at path (SocketPath if empty).
func NewClient(path string) *Client {
	if path == "" {
		path = SocketPath
	}
	return &Client{path: path}
}

// Path returns the socket path.
func (c *Client) Path() string { return c.path }

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "unix", c.path)
}

func (c *Client) newRequest(op string, args any) (Request, error) {
	r := Request{ID: c.id.Add(1), Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return r, err
		}
		r.Args = b
	}
	return r, nil
}

func readResponse(br *bufio.Reader) (Response, error) {
	line, err := readLine(br)
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return resp, fmt.Errorf("nodespot-dv: bad response: %w", err)
	}
	return resp, nil
}

// ErrLineTooLong is returned for a line over MaxLine bytes.
var ErrLineTooLong = errors.New("nodespot-dv: line too long")

// readLine reads one NDJSON line of at most MaxLine bytes.
func readLine(br *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		part, err := br.ReadSlice('\n')
		out = append(out, part...)
		if len(out) > MaxLine {
			return nil, ErrLineTooLong
		}
		if err == nil {
			return out, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

func respError(resp Response) error {
	if resp.OK {
		return nil
	}
	return &Error{Code: resp.Code, Msg: resp.Error}
}

func (c *Client) do(ctx context.Context, op string, args any) (Status, error) {
	req, err := c.newRequest(op, args)
	if err != nil {
		return Status{}, err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return Status{}, err
	}
	defer conn.Close()
	dl := time.Now().Add(dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	if op == OpMode || op == OpTune {
		dl = time.Now().Add(enterTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(dl) {
			dl = d
		}
	}
	conn.SetDeadline(dl)
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Status{}, ctxErr(ctx, err)
	}
	resp, err := readResponse(bufio.NewReaderSize(conn, 4096))
	if err != nil {
		return Status{}, ctxErr(ctx, err)
	}
	if err := respError(resp); err != nil {
		return Status{}, err
	}
	var st Status
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &st); err != nil {
			return st, fmt.Errorf("nodespot-dv: bad status: %w", err)
		}
	}
	return st, nil
}

func ctxErr(ctx context.Context, err error) error {
	if ce := ctx.Err(); ce != nil {
		return ce
	}
	return err
}

// Status returns the engine status.
func (c *Client) Status(ctx context.Context) (Status, error) { return c.do(ctx, OpStatus, nil) }

// Reload asks the engine to re-read its config file.
func (c *Client) Reload(ctx context.Context) (Status, error) { return c.do(ctx, OpReload, nil) }

// Mode switches the active network mode.
func (c *Client) Mode(ctx context.Context, a ModeArgs) (Status, error) {
	return c.do(ctx, OpMode, a)
}

// Tune points the active network at a target.
func (c *Client) Tune(ctx context.Context, a TuneArgs) (Status, error) {
	return c.do(ctx, OpTune, a)
}

// Unlink disconnects the current (or named) network target.
func (c *Client) Unlink(ctx context.Context, a UnlinkArgs) (Status, error) {
	return c.do(ctx, OpUnlink, a)
}

// Subscribe calls first with the initial Status, then fn with each Event,
// until ctx ends or the connection drops (the error is returned; the caller
// reconnects). Ping events are passed to fn like any other.
func (c *Client) Subscribe(ctx context.Context, first func(Status), fn func(Event)) error {
	req, err := c.newRequest(OpSubscribe, nil)
	if err != nil {
		return err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	b, _ := json.Marshal(req)
	conn.SetWriteDeadline(time.Now().Add(dialTimeout))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return ctxErr(ctx, err)
	}
	conn.SetWriteDeadline(time.Time{})
	br := bufio.NewReaderSize(conn, 8192)
	conn.SetReadDeadline(time.Now().Add(dialTimeout))
	resp, err := readResponse(br)
	if err != nil {
		return ctxErr(ctx, err)
	}
	conn.SetReadDeadline(time.Time{})
	if err := respError(resp); err != nil {
		return err
	}
	var st Status
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &st); err != nil {
			return fmt.Errorf("nodespot-dv: bad status: %w", err)
		}
	}
	if first != nil {
		first(st)
	}
	for {
		line, err := readLine(br)
		if err != nil {
			return ctxErr(ctx, err)
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("nodespot-dv: bad event: %w", err)
		}
		if ev.Type == EvOverflow {
			if fn != nil {
				fn(ev)
			}
			return errors.New("nodespot-dv: subscriber overflow")
		}
		if fn != nil {
			fn(ev)
		}
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
