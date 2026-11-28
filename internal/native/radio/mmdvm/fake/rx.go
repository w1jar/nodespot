package fake

import (
	"context"
	"errors"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// RealBurstInterval is the spacing of the bursts of a real call on one
// slot: 60 ms.
const RealBurstInterval = 60 * time.Millisecond

// ErrNoRX is returned by [Modem.PlayCall] when a real hat would hear
// nothing: it is not configured, not in DMR mode, or (in duplex) not
// transmitting.
var ErrNoRX = errors.New("fake: the modem would report nothing (not configured, not in DMR mode, or duplex and not transmitting)")

// RXCall is a call the fake modem hears from a handheld, as a simplex hat
// reports it: three voice LC headers, voice bursts A to F (control 0x20 and
// 1 to 5) for every three AMBE frames, then a terminator with LC (control
// 0x42), on DMR_DATA2.
type RXCall struct {
	// Slot is the DMR_DATA slot the frames are reported on; 0 is 2, as a
	// simplex modem reports every call.
	Slot     int
	Src, Dst uint32
	Private  bool
	CC       uint8
	// AMBE are the voice frames. A count that is not a multiple of 3 is
	// padded with silence by the terminator.
	AMBE []dmr.AMBE
	// Interval is the spacing between bursts (default 60 ms, a real call).
	Interval time.Duration
	// Lost ends the call with DMR_LOST instead of a terminator.
	Lost bool
	// NoEnd sends neither: the call just stops.
	NoEnd bool
	// Headers is the number of voice LC headers (default 3, as heard).
	Headers int
}

// Frames builds the bursts of the call as the hat reports them (MS-sourced
// sync, as a handheld sends).
func (c RXCall) Frames() []dmr.TXFrame {
	tx := dmr.NewTX(dmr.TXConfig{Src: c.Src, Dst: c.Dst, Private: c.Private, CC: c.CC,
		MSSync: true, Headers: c.Headers})
	out := tx.Start()
	for _, a := range c.AMBE {
		out = append(out, tx.Write(a)...)
	}
	end := tx.End()
	if c.Lost || c.NoEnd {
		end = end[:len(end)-1] // keep the padded last voice burst, drop the terminator
	}
	return append(out, end...)
}

// PlayCall sends the call to the host burst by burst, Interval apart, and
// returns when it is done or ctx ends. Like the firmware it refuses
// ([ErrNoRX]) unless a receive is possible: configured, in DMR mode and,
// on a duplex modem, transmitting.
func (m *Modem) PlayCall(ctx context.Context, c RXCall) error {
	if c.Slot == 0 {
		c.Slot = 2
	}
	if c.Interval <= 0 {
		c.Interval = RealBurstInterval
	}
	m.mu.Lock()
	ok := m.configured && m.mode == mmdvm.ModeDMR && (!m.duplex || m.tx)
	m.mu.Unlock()
	if !ok {
		return ErrNoRX
	}
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for i, f := range c.Frames() {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
		if err := m.InjectDMR(c.Slot, mmdvm.Control(f.FrameType, f.DTypeVSeq), f.Burst); err != nil {
			return err
		}
	}
	if c.Lost {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		return m.InjectLost(c.Slot)
	}
	return nil
}
