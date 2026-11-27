package vocoder

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultTimeout is the per-request read deadline.
	DefaultTimeout = 60 * time.Millisecond
	// DownAfter is the number of consecutive timeouts after which the
	// vocoder is reported down.
	DownAfter = 25
	// downBackoff is the minimum gap between attempts while down.
	downBackoff = time.Second
)

// ErrDown is returned (with a silence frame) while the vocoder is down and
// the retry backoff has not elapsed.
var ErrDown = errors.New("vocoder: the software AMBE vocoder is down")

// MD380 is a client of md380-emu's headerless UDP protocol: encode is 320
// bytes (160 s16 LE samples) out and 7 bytes in; decode is 7 out and 320 in.
//
// Each direction has its own socket, one request in flight per socket. After
// a timeout the socket is closed and the next request dials a new ephemeral
// port, so a late reply can never be paired with a later request.
type MD380 struct {
	pmu     sync.RWMutex
	addr    string
	timeout time.Duration
	enc     channel
	dec     channel

	// OnChange, if set before first use, is called when the down state
	// flips (from the goroutine making the request).
	OnChange func(down bool)

	frames   atomic.Uint64
	timeouts atomic.Uint64
	down     atomic.Bool
	consec   atomic.Int64
}

// NewMD380 returns a client for md380-emu at addr. A zero timeout selects
// DefaultTimeout.
func NewMD380(addr string, timeout time.Duration) *MD380 {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &MD380{addr: addr, timeout: timeout}
}

// Addr is the vocoder address.
func (m *MD380) Addr() string {
	m.pmu.RLock()
	defer m.pmu.RUnlock()
	return m.addr
}

func (m *MD380) params() (string, time.Duration) {
	m.pmu.RLock()
	defer m.pmu.RUnlock()
	return m.addr, m.timeout
}

// Reconfigure changes the address and timeout (config reload). Sockets are
// closed so the next request dials the new address.
func (m *MD380) Reconfigure(addr string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	m.pmu.Lock()
	changed := addr != m.addr
	m.addr, m.timeout = addr, timeout
	m.pmu.Unlock()
	if changed {
		m.enc.close()
		m.dec.close()
	}
}

// Close closes both sockets.
func (m *MD380) Close() error {
	m.enc.close()
	m.dec.close()
	return nil
}

// Stats returns the counters.
func (m *MD380) Stats() Stats {
	return Stats{Frames: m.frames.Load(), Timeouts: m.timeouts.Load(), Down: m.down.Load()}
}

// Encode converts PCM to an AMBE+2 frame. On a timeout it returns
// SilenceAMBE and a nil error (the timeout is counted in Stats).
func (m *MD380) Encode(ctx context.Context, pcm *[PCMSamples]int16) ([AMBEBytes]byte, error) {
	var req [PCMSamples * 2]byte
	for i, s := range pcm {
		req[2*i] = byte(s)
		req[2*i+1] = byte(s >> 8)
	}
	var out [AMBEBytes]byte
	reply, err := m.roundTrip(ctx, &m.enc, req[:], AMBEBytes)
	if reply == nil {
		return SilenceAMBE, err
	}
	copy(out[:], reply)
	return out, nil
}

// Decode converts an AMBE+2 frame to PCM. On a timeout it returns zero PCM
// and a nil error.
func (m *MD380) Decode(ctx context.Context, f [AMBEBytes]byte) ([PCMSamples]int16, error) {
	var pcm [PCMSamples]int16
	reply, err := m.roundTrip(ctx, &m.dec, f[:], PCMSamples*2)
	if reply == nil {
		return pcm, err
	}
	for i := range pcm {
		pcm[i] = int16(uint16(reply[2*i]) | uint16(reply[2*i+1])<<8)
	}
	return pcm, nil
}

func (m *MD380) roundTrip(ctx context.Context, ch *channel, req []byte, want int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if m.down.Load() && time.Since(ch.lastTry) < downBackoff {
		return nil, ErrDown
	}
	ch.lastTry = time.Now()
	addr, timeout := m.params()
	reply, err := ch.exchange(ctx, addr, timeout, req, want)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.timeouts.Add(1)
		if m.consec.Add(1) >= DownAfter && m.down.CompareAndSwap(false, true) && m.OnChange != nil {
			m.OnChange(true)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, nil
		}
		return nil, err
	}
	m.frames.Add(1)
	m.consec.Store(0)
	if m.down.CompareAndSwap(true, false) && m.OnChange != nil {
		m.OnChange(false)
	}
	return reply, nil
}

type channel struct {
	mu      sync.Mutex
	conn    *net.UDPConn
	buf     [PCMSamples*2 + 16]byte
	lastTry time.Time
}

func (c *channel) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drop()
}

func (c *channel) drop() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

var errBadReply = errors.New("vocoder: bad reply length")

// exchange sends req and reads a reply of exactly want bytes. Any failure
// (timeout, wrong length, I/O) closes the socket so the next call redials.
func (c *channel) exchange(ctx context.Context, addr string, timeout time.Duration, req []byte, want int) ([]byte, error) {
	if c.conn == nil {
		ra, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		conn, err := net.DialUDP("udp", nil, ra)
		if err != nil {
			return nil, err
		}
		c.conn = conn
	}
	dl := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	c.conn.SetDeadline(dl)
	if _, err := c.conn.Write(req); err != nil {
		c.drop()
		return nil, err
	}
	n, err := c.conn.Read(c.buf[:])
	if err != nil {
		c.drop()
		return nil, err
	}
	if n != want {
		c.drop()
		return nil, errBadReply
	}
	return c.buf[:n], nil
}
