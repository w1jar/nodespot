package node

import (
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// idReason says why an ID was sent.
type idReason string

const (
	idNone     idReason = ""
	idDeadline idReason = "deadline" // the interval ran out
	idTail     idReason = "tail"     // rode the tail of a transmission
	idEnd      idReason = "end"      // the final ID after activity ended
)

// idTimer decides when the station ID is due. It is RF only: the node
// calls it with what it puts on the radio, and the ID is never sent to a
// mode.
//
// An ID is owed once the station transmits anything other than an ID. It
// must go out within the interval of that first transmission and once more
// when the activity ends. Within the tail window before the deadline it
// rides the hang of a transmission rather than keying up on its own.
type idTimer struct {
	owed     bool
	deadline time.Time // owed: the latest time for the ID
	lastTx   time.Time // the last non-ID transmission
	lastID   time.Time
	lastWhy  idReason
}

// idParams are the ID times.
type idParams struct {
	on       bool
	interval time.Duration
	tail     time.Duration
	endQuiet time.Duration
}

// transmitted records that the station put non-ID audio on the air.
func (t *idTimer) transmitted(now time.Time, p idParams) {
	if !t.owed {
		t.owed = true
		t.deadline = now.Add(p.interval)
	}
	t.lastTx = now
}

// due reports whether the ID should start now and why. keyed says the
// transmitter is on; inTail says it is on only for the hang (nothing else
// to send); blocked says the ID may not start (a local station is
// transmitting into the receiver).
func (t *idTimer) due(now time.Time, p idParams, keyed, inTail, blocked bool) idReason {
	if !t.owed || !p.on || blocked {
		return idNone
	}
	switch {
	case !now.Before(t.deadline):
		return idDeadline
	case keyed && inTail && !now.Before(t.deadline.Add(-p.tail)):
		return idTail
	case !keyed && now.Sub(t.lastTx) >= p.endQuiet:
		return idEnd
	}
	return idNone
}

// sent records that the ID started.
func (t *idTimer) sent(now time.Time, why idReason) {
	t.owed = false
	t.lastID = now
	t.lastWhy = why
}

// leadIn is the silence before an ID that keys the transmitter itself, so
// the first element is not lost while the radio comes up.
const leadIn = 3 // frames (60 ms)

// silenceThen plays n frames of silence, then s.
type silenceThen struct {
	n int
	s audio.Source
}

// Next implements audio.Source.
func (p *silenceThen) Next(f *audio.Frame) bool {
	if p.n > 0 {
		p.n--
		*f = audio.Frame{}
		return true
	}
	return p.s.Next(f)
}
