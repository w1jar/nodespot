package mmdvm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RXEventKind says what an [RXEvent] is.
type RXEventKind int

// The kinds of [RXEvent].
const (
	// RXBurst is one received DMR_DATA burst. Voice is true for a voice
	// burst (three AMBE frames), and FECErrors then counts the bit errors
	// the FEC corrected in them.
	RXBurst RXEventKind = iota + 1
	// RXLost is a DMR_LOST: the modem lost the signal.
	RXLost
	// RXCallStart is the first decoded call header (or late-entry LC) of a
	// call: Src, Dst and Private are set.
	RXCallStart
	// RXCallEnd is the end of the call the driver was tracking, whatever
	// the cause (terminator, DMR_LOST, silence, a different call).
	RXCallEnd
)

// RXEvent is what [Settings].RXMonitor sees.
type RXEvent struct {
	Kind      RXEventKind
	Slot      int
	Time      time.Time
	Voice     bool
	FECErrors int
	Src, Dst  uint32
	Private   bool
}

// monitor passes e to the monitor, if any. rxMu must be held.
func (d *Driver) monitor(e RXEvent) {
	if d.s.RXMonitor != nil {
		d.s.RXMonitor(e)
	}
}

// ErrRetune is returned by [Driver.Retune] when the driver is not in
// receive-only mode.
var ErrRetune = errors.New("mmdvm: the frequency can only be changed while receiving only")

type retuneReq struct {
	f    Freq
	done chan error
}

// Retune sends a new SET_FREQ (and SET_CONFIG, which programs it) to the open modem without reopening it. It is
// for receive-only drivers (Settings.RXOnly); the RF power stays 0 whatever
// f says. The modem goes idle, takes the frequency and returns to DMR mode,
// so the transceiver is reprogrammed in a mode it expects. A frequency the
// modem NAKs is returned as an error, the driver stays up, and the earlier
// frequency is kept for the next open. Retune waits for the modem to
// answer, until ctx ends or the driver is closed.
func (d *Driver) Retune(ctx context.Context, f Freq) error {
	if !d.s.RXOnly {
		return ErrRetune
	}
	f.Power = 0
	r := retuneReq{f: f, done: make(chan error, 1)}
	select {
	case d.retuneCh <- r:
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closed:
		return ErrClosed
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closed:
		return ErrClosed
	}
}

// doRetune runs inside the session loop, which is the only reader of the
// modem's replies. An error returned here ends the session; a NAK or a
// missing reply is only the answer to the caller.
func (d *Driver) doRetune(ctx context.Context, w *writer, frames <-chan Frame, rdErr <-chan error, r retuneReq) error {
	// SET_FREQ only stores the frequency; the chip is programmed by SET_CONFIG
	// (CIO::ifConf), which idle then DMR alone would not trigger.
	cfg, err := SetConfig(d.proto, d.s.Modem)
	if err != nil {
		r.done <- err
		return nil
	}
	steps := [][]byte{SetMode(ModeIdle), SetFreq(r.f), cfg, SetMode(ModeDMR)}
	var failed error
	for i, b := range steps {
		err := d.command(ctx, w, frames, rdErr, b)
		if err == nil {
			continue
		}
		var nak *NAKError
		switch {
		case errors.As(err, &nak):
			failed = err
			if nak.Cmd == CmdSetFreq {
				failed = fmt.Errorf("%w (%s)", err, FreqHint())
			}
		case ctx.Err() != nil:
			r.done <- ctx.Err()
			return ctx.Err()
		default:
			r.done <- err
			return err
		}
		if i == 1 { // SET_FREQ refused: back to DMR on the old frequency
			continue
		}
		if i == 2 { // SET_CONFIG refused: still try to listen again
			continue
		}
		break
	}
	if failed == nil {
		d.s.Freq = r.f
	}
	r.done <- failed
	return nil
}
