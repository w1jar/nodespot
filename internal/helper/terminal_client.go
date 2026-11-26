package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// terminalConn is the client end of a terminal stream: reads go through the
// buffer that read the JSON reply, so no frame byte is lost.
type terminalConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *terminalConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// OpenTerminal calls terminal.open for owner (the nodespot user id) and returns
// the session id and the framed stream (see terminal_frame.go). Closing the
// stream only detaches the session; send a FrameEnd first to end it. rows and
// cols of zero mean the default size.
func (c *Client) OpenTerminal(ctx context.Context, rows, cols uint16, owner int64) (io.ReadWriteCloser, string, error) {
	return c.openStream(ctx, Request{Op: OpTerminalOpen, Terminal: &TerminalRequest{Rows: rows, Cols: cols, Owner: owner}})
}

// AttachTerminal calls terminal.attach: the session's recent output is
// replayed, then it goes live.
func (c *Client) AttachTerminal(ctx context.Context, id string, owner int64, rows, cols uint16) (io.ReadWriteCloser, error) {
	s, _, err := c.openStream(ctx, Request{Op: OpTerminalAttach, Terminal: &TerminalRequest{ID: id, Owner: owner, Rows: rows, Cols: cols}})
	return s, err
}

// ListTerminals calls terminal.list; ack names ended records to forget.
func (c *Client) ListTerminals(ctx context.Context, ack []string) (TerminalList, error) {
	var l TerminalList
	err := c.do(ctx, Request{Op: OpTerminalList, Terminal: &TerminalRequest{Ack: ack}}, &l, 1<<20)
	return l, err
}

// EndTerminal calls terminal.end: the session's unit is stopped.
func (c *Client) EndTerminal(ctx context.Context, id string) error {
	return c.do(ctx, Request{Op: OpTerminalEnd, Terminal: &TerminalRequest{ID: id}}, nil, 1<<16)
}

func (c *Client) openStream(ctx context.Context, req Request) (io.ReadWriteCloser, string, error) {
	var d net.Dialer
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := d.DialContext(hctx, "unix", c.Socket)
	if err != nil {
		return nil, "", &UnavailableError{Reason: unavailableReason(err), Err: err}
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if dl, has := hctx.Deadline(); has {
		_ = conn.SetDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, "", fmt.Errorf("helper: %w", err)
	}
	br := bufio.NewReaderSize(conn, 4096)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return nil, "", fmt.Errorf("helper: %w", err)
	}
	var e errorResponse
	if json.Unmarshal(line, &e) == nil && e.Error != "" {
		return nil, "", errors.New(e.Error)
	}
	var res TerminalOpened
	if err := json.Unmarshal(line, &res); err != nil || !res.OK {
		return nil, "", errors.New("helper: bad response")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &terminalConn{Conn: conn, r: br}, res.ID, nil
}
