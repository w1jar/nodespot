package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// ErrUnavailable wraps every failure to reach the node (no socket, the
// daemon not running, a broken connection), as opposed to an error the
// node answered with.
var ErrUnavailable = errors.New("the node is not running")

// RemoteError is an error the node answered a request with. Its text is
// meant for the operator.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// Client talks to the node's control socket. Each call opens its own
// connection, so a Client is safe for concurrent use and survives a node
// restart without any reconnect logic.
type Client struct {
	path string
	// Timeout bounds one call when ctx has no earlier deadline (default
	// 15 s: a select may wait for a directory or master login).
	Timeout time.Duration
	seq     atomic.Uint64
}

// NewClient returns a client for the socket at path (DefaultSocket when
// empty).
func NewClient(path string) *Client {
	if path == "" {
		path = DefaultSocket
	}
	return &Client{path: path, Timeout: 15 * time.Second}
}

// Path is the socket path.
func (c *Client) Path() string { return c.path }

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return conn, nil
}

// Call sends one request and decodes the reply's data into out (which may
// be nil).
func (c *Client) Call(ctx context.Context, op string, args, out any) error {
	if _, ok := ctx.Deadline(); !ok && c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	req := struct {
		ID   uint64 `json:"id"`
		Op   string `json:"op"`
		Args any    `json:"args,omitempty"`
	}{c.seq.Add(1), op, args}
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("control %s: %w", op, err)
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("%w: control %s: %v", ErrUnavailable, op, err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	if !sc.Scan() {
		err := sc.Err()
		if err == nil {
			err = errors.New("connection closed")
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf("%w: control %s: %v", ErrUnavailable, op, err)
	}
	return decodeReply(op, sc.Bytes(), out)
}

type reply struct {
	ID    uint64          `json:"id"`
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

func decodeReply(op string, line []byte, out any) error {
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return fmt.Errorf("control %s: bad reply: %w", op, err)
	}
	if !r.OK {
		msg := r.Error
		if msg == "" {
			msg = "the node refused the request"
		}
		return &RemoteError{Msg: msg}
	}
	if out == nil || len(r.Data) == 0 || string(r.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("control %s: bad reply data: %w", op, err)
	}
	return nil
}

// Status returns the node's status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.Call(ctx, OpStatus, nil, &st)
	return st, err
}

// Modes lists every mode with its settings (secrets blank).
func (c *Client) Modes(ctx context.Context) ([]ModeInfo, error) {
	var l []ModeInfo
	err := c.Call(ctx, OpModes, nil, &l)
	return l, err
}

// Station returns the station settings.
func (c *Client) Station(ctx context.Context) (Station, error) {
	var st Station
	err := c.Call(ctx, OpConfig, nil, &st)
	return st, err
}

// SaveStation replaces the station settings (the active mode is kept).
func (c *Client) SaveStation(ctx context.Context, st Station) (Status, error) {
	var out Status
	err := c.Call(ctx, OpSaveStation, map[string]any{"station": st}, &out)
	return out, err
}

// SaveMode merges s into a mode's settings; a blank secret keeps the saved
// one.
func (c *Client) SaveMode(ctx context.Context, name string, s mode.Settings) ([]ModeInfo, error) {
	var out []ModeInfo
	err := c.Call(ctx, OpSaveMode, map[string]any{"mode": name, "settings": s}, &out)
	return out, err
}

// DeleteMode forgets a mode's settings.
func (c *Client) DeleteMode(ctx context.Context, name string) error {
	return c.Call(ctx, OpDeleteMode, map[string]any{"mode": name}, nil)
}

// Select makes name the active mode ("" for idle), connecting to t when
// it is not nil.
func (c *Client) Select(ctx context.Context, name string, t *mode.Target) (Status, error) {
	var out Status
	err := c.Call(ctx, OpSelect, map[string]any{"mode": name, "target": t}, &out)
	return out, err
}

// Connect links the active mode to t.
func (c *Client) Connect(ctx context.Context, t mode.Target) (Status, error) {
	var out Status
	err := c.Call(ctx, OpConnect, map[string]any{"target": t}, &out)
	return out, err
}

// Disconnect drops the active mode's link.
func (c *Client) Disconnect(ctx context.Context) (Status, error) {
	var out Status
	err := c.Call(ctx, OpDisconnect, nil, &out)
	return out, err
}

// LastHeard returns up to limit calls, newest first (0: all the node keeps).
func (c *Client) LastHeard(ctx context.Context, limit int) ([]mode.Heard, error) {
	var out []mode.Heard
	var args any
	if limit > 0 {
		args = map[string]int{"limit": limit}
	}
	err := c.Call(ctx, OpLastHeard, args, &out)
	return out, err
}

// NowCalling returns who is talking, or nil.
func (c *Client) NowCalling(ctx context.Context) (*Calling, error) {
	var out *Calling
	err := c.Call(ctx, OpNowCalling, nil, &out)
	return out, err
}

// RadioLevels returns the radio's mixer positions and meters.
func (c *Client) RadioLevels(ctx context.Context) (radio.Levels, error) {
	var out radio.Levels
	err := c.Call(ctx, OpRadioLevels, nil, &out)
	return out, err
}

// InputLevel returns the radio's current receive level and carrier.
func (c *Client) InputLevel(ctx context.Context) (InputLevel, error) {
	var out InputLevel
	err := c.Call(ctx, OpInputLevel, nil, &out)
	return out, err
}

// RadioTune sets the sound chip's receive and transmit levels and the
// receive boost (nil keeps one) and saves them.
func (c *Client) RadioTune(ctx context.Context, rx, tx *float64, boost *bool) (radio.Levels, error) {
	var out radio.Levels
	err := c.Call(ctx, OpRadioTune, map[string]any{"rxVolume": rx, "txVolume": tx, "rxBoost": boost}, &out)
	return out, err
}

// AutosetRx starts the receive auto-set, or cancels it.
func (c *Client) AutosetRx(ctx context.Context, cancel bool) (AutosetStatus, error) {
	var out AutosetStatus
	err := c.Call(ctx, OpAutosetRx, map[string]any{"cancel": cancel}, &out)
	return out, err
}

// AutosetStatus returns the state of the receive auto-set.
func (c *Client) AutosetStatus(ctx context.Context) (AutosetStatus, error) {
	var out AutosetStatus
	err := c.Call(ctx, OpAutosetStat, nil, &out)
	return out, err
}

// RadioInfo describes the radio interface: the preset, the pins, the
// detected chip and the presets to choose from.
func (c *Client) RadioInfo(ctx context.Context) (RadioDetail, error) {
	var out RadioDetail
	err := c.Call(ctx, OpRadioInfo, nil, &out)
	return out, err
}

// ModemStatus reports the MMDVM modem.
func (c *Client) ModemStatus(ctx context.Context) (ModemInfo, error) {
	var out ModemInfo
	err := c.Call(ctx, OpModemStatus, nil, &out)
	return out, err
}

// ModemReset closes the MMDVM modem and opens it again.
func (c *Client) ModemReset(ctx context.Context) (ModemInfo, error) {
	var out ModemInfo
	err := c.Call(ctx, OpModemReset, nil, &out)
	return out, err
}

// RadioProgram writes the saved channel to the SA818.
func (c *Client) RadioProgram(ctx context.Context) (Status, error) {
	var out Status
	err := c.Call(ctx, OpRadioProg, nil, &out)
	return out, err
}

// RadioTuningDismiss dismisses the "tune your audio" reminder for the
// radio interface in use and saves it.
func (c *Client) RadioTuningDismiss(ctx context.Context) (Status, error) {
	var out Status
	err := c.Call(ctx, OpTuningDismiss, nil, &out)
	return out, err
}

// TestTone keys the radio with the 1 kHz test tone for seconds (0: the
// default 5, at most 10), or with stop ends it early.
func (c *Client) TestTone(ctx context.Context, seconds float64, stop bool) (Status, error) {
	var out Status
	err := c.Call(ctx, OpTestTone, map[string]any{"seconds": seconds, "stop": stop}, &out)
	return out, err
}

// Reload makes the node read its config file again.
func (c *Client) Reload(ctx context.Context) (Status, error) {
	var out Status
	err := c.Call(ctx, OpReload, nil, &out)
	return out, err
}

// SearchDirectory searches the station directory of mode name (EchoLink).
func (c *Client) SearchDirectory(ctx context.Context, name, q string, limit int) (mode.DirResult, error) {
	var out mode.DirResult
	err := c.Call(ctx, OpDirSearch, map[string]any{"mode": name, "q": q, "limit": limit}, &out)
	if out.Results == nil {
		out.Results = []mode.DirEntry{}
	}
	return out, err
}

// Subscribe opens an event stream. It returns the status at the time of
// subscribing and a channel of events that is closed when the stream ends
// (ctx done, or the node went away).
func (c *Client) Subscribe(ctx context.Context) (Status, <-chan Event, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return Status{}, nil, err
	}
	fail := func(err error) (Status, <-chan Event, error) {
		conn.Close()
		return Status{}, nil, err
	}
	b, _ := json.Marshal(Request{ID: c.seq.Add(1), Op: OpSubscribe})
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fail(fmt.Errorf("%w: subscribe: %v", ErrUnavailable, err))
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	if !sc.Scan() {
		return fail(fmt.Errorf("%w: subscribe: no reply", ErrUnavailable))
	}
	var st Status
	if err := decodeReply(OpSubscribe, sc.Bytes(), &st); err != nil {
		return fail(err)
	}
	_ = conn.SetDeadline(time.Time{})
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		for sc.Scan() {
			var line struct {
				Event *Event `json:"event"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil || line.Event == nil {
				continue
			}
			select {
			case ch <- *line.Event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return st, ch, nil
}
