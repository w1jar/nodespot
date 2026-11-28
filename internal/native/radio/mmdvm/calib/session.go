package calib

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// FreqFunc is the SET_FREQ content for an RX offset: the receive frequency
// with the offset, and the transmit frequency as the node sends it.
type FreqFunc func(offset int) mmdvm.Freq

// Session is the modem, opened receive-only, with the tracker watching it.
type Session struct {
	d     *mmdvm.Driver
	T     *Tracker
	freq  FreqFunc
	rules mmdvm.FreqRules

	mu     sync.Mutex
	offset int
	done   chan struct{}
	cancel context.CancelFunc
}

// ErrNotOpen is returned when the modem did not open in time.
type ErrNotOpen struct{ Last string }

func (e ErrNotOpen) Error() string {
	if e.Last == "" {
		return "the modem did not answer"
	}
	return "the modem did not open: " + e.Last
}

// NewSession builds a receive-only driver from s (RXOnly is forced on,
// DetectOnly off, AllowTX off) at the starting offset. Nothing is opened
// until Start.
func NewSession(s mmdvm.Settings, freq FreqFunc, rules mmdvm.FreqRules, offset int) *Session {
	c := &Session{freq: freq, rules: rules, offset: offset, T: NewTracker(offset), done: make(chan struct{})}
	s.RXOnly, s.DetectOnly, s.AllowTX = true, false, false
	s.Freq = freq(offset)
	s.Freq.Power = 0
	s.RXMonitor = c.T.Observe
	c.d = mmdvm.New(s)
	return c
}

// Driver is the modem driver, for status.
func (c *Session) Driver() *mmdvm.Driver { return c.d }

// Start runs the driver and waits up to wait for the modem to open.
func (c *Session) Start(ctx context.Context, wait time.Duration) error {
	rctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go func() {
		defer close(c.done)
		_ = c.d.Run(rctx, discard{})
	}()
	go func() { // ends calls that go silent without DMR_LOST
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case now := <-t.C:
				c.T.Tick(now)
			}
		}
	}()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if st := c.d.Status(); st.Open {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrNotOpen{Last: c.d.Status().Error}
		case <-tick.C:
		}
	}
}

// Close stops the driver: it sets the modem idle, closes the port and
// returns when that is done.
func (c *Session) Close() {
	_ = c.d.Close()
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
}

// Offset is the offset in force.
func (c *Session) Offset() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offset
}

// Check says why an offset cannot be used, or nil.
func (c *Session) Check(off int) error {
	if off < -node.MaxMMDVMOffsetHz || off > node.MaxMMDVMOffsetHz {
		return fmt.Errorf("an offset must be within +-%d Hz", node.MaxMMDVMOffsetHz)
	}
	f := c.freq(off)
	return mmdvm.CheckFreq(f.RX, f.TX, c.rules)
}

// SetOffset retunes the receive frequency to the offset with a new
// SET_FREQ (RF power 0), without reopening the modem.
func (c *Session) SetOffset(ctx context.Context, off int) error {
	if err := c.Check(off); err != nil {
		return err
	}
	f := c.freq(off)
	f.Power = 0
	if err := c.d.Retune(ctx, f); err != nil {
		return err
	}
	c.mu.Lock()
	c.offset = off
	c.mu.Unlock()
	c.T.SetOffset(off)
	return nil
}

// Err is the driver's last error, if the modem is not open.
func (c *Session) Err() error {
	st := c.d.Status()
	if st.Open || st.Error == "" {
		return nil
	}
	return errors.New(st.Error)
}

// discard is a sink that ignores the call: calibration listens, it does not
// pass audio on.
type discard struct{}

func (discard) Header(network.CallHeader)    {}
func (discard) Voice(network.VoiceFrame)     {}
func (discard) End(uint64)                   {}
func (discard) Login(ctl.LoginState, string) {}
