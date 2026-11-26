package mode

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// Buffer sizes of the channels Base owns.
const (
	RecvBuffer  = 50 // one second of audio
	EventBuffer = 64
)

// Base holds what every mode keeps the same way: the Recv and Events
// channels and the State, Target, Detail, Error and Talker parts of Status.
// A mode embeds it and calls its setters; Base sends the matching events.
// The zero value is not usable: call Init.
type Base struct {
	name  string
	title string
	now   func() time.Time

	recv   chan audio.Frame
	events chan Event
	// overflow counts frames Deliver dropped because the queue was full.
	overflow atomic.Uint64

	mu     sync.Mutex
	st     Status
	talker *Talker
}

// Init prepares b for the named mode.
func (b *Base) Init(name, title string, now func() time.Time) {
	b.InitBuffer(name, title, now, RecvBuffer)
}

// InitBuffer is Init with a Recv queue of recv frames, for a mode whose
// network may send audio in bursts faster than real time (the EchoLink
// echo test plays a recording back): the queue holds a burst for the
// radio instead of dropping it.
func (b *Base) InitBuffer(name, title string, now func() time.Time, recv int) {
	if now == nil {
		now = time.Now
	}
	if recv <= 0 {
		recv = RecvBuffer
	}
	b.name, b.title, b.now = name, title, now
	b.recv = make(chan audio.Frame, recv)
	b.events = make(chan Event, EventBuffer)
	b.st = Status{Mode: name, Title: title, State: StateIdle, Since: now()}
}

// Name is the mode's name.
func (b *Base) Name() string { return b.name }

// Recv carries network audio for the radio.
func (b *Base) Recv() <-chan audio.Frame { return b.recv }

// Events carries the mode's events.
func (b *Base) Events() <-chan Event { return b.events }

// Now is the mode's clock.
func (b *Base) Now() time.Time { return b.now() }

// Deliver queues one frame for the radio, dropping the oldest frame when
// the node is not keeping up. Dropped frames are counted (Overflow).
func (b *Base) Deliver(f audio.Frame) {
	select {
	case b.recv <- f:
		return
	default:
	}
	select {
	case <-b.recv:
		b.overflow.Add(1)
	default:
	}
	select {
	case b.recv <- f:
	default:
		b.overflow.Add(1)
	}
}

// Overflow is how many frames Deliver has dropped because the queue for
// the radio was full, over the life of the mode.
func (b *Base) Overflow() uint64 { return b.overflow.Load() }

// Emit sends ev, filling Mode and At, dropping it when nobody reads.
func (b *Base) Emit(ev Event) {
	ev.Mode = b.name
	if ev.At.IsZero() {
		ev.At = b.now()
	}
	select {
	case b.events <- ev:
	default:
	}
}

// SetState records a state change. err may be "" (it clears the error,
// except for StateError which keeps the last one when err is "").
func (b *Base) SetState(s State, detail, err string) {
	b.mu.Lock()
	changed := b.st.State != s || b.st.Detail != detail || (err != "" && b.st.Error != err)
	if b.st.State != s {
		b.st.Since = b.now()
	}
	b.st.State = s
	b.st.Detail = detail
	if err != "" || s != StateError {
		b.st.Error = err
	}
	var tgt *Target
	if b.st.Target != nil {
		t := *b.st.Target
		tgt = &t
	}
	b.mu.Unlock()
	if changed {
		b.Emit(Event{Kind: EventState, State: s, Target: tgt, Text: detail, Err: err})
	}
	if err != "" {
		b.Emit(Event{Kind: EventError, Err: err})
	}
}

// SetTarget records the target (nil = none).
func (b *Base) SetTarget(t *Target) {
	b.mu.Lock()
	if t == nil {
		b.st.Target = nil
	} else {
		c := *t
		b.st.Target = &c
	}
	b.mu.Unlock()
}

// SetLinks records the far side's link or member list.
func (b *Base) SetLinks(l []string) {
	b.mu.Lock()
	b.st.Links = append([]string(nil), l...)
	b.mu.Unlock()
}

// SetRegistration records the AllStar registration.
func (b *Base) SetRegistration(r *Registration) {
	b.mu.Lock()
	b.st.Registration = r
	b.mu.Unlock()
}

// SetLinkStats records the counters of the current link (nil clears them).
func (b *Base) SetLinkStats(l *LinkStats) {
	b.mu.Lock()
	b.st.Link = l
	b.mu.Unlock()
}

// SetLevels records the level report of the last recording (nil clears it).
func (b *Base) SetLevels(r *audio.Report) {
	b.mu.Lock()
	if r == nil {
		b.st.Levels = nil
	} else {
		c := *r
		c.Warnings = append([]string(nil), r.Warnings...)
		b.st.Levels = &c
	}
	b.mu.Unlock()
}

// TalkStart records that t started talking. A talker already marked is
// ended first when it is someone else; the same talker only updates.
func (b *Base) TalkStart(t Talker) {
	if t.Since.IsZero() {
		t.Since = b.now()
	}
	b.mu.Lock()
	prev := b.talker
	if prev != nil && prev.Callsign == t.Callsign && prev.ID == t.ID && prev.Target == t.Target {
		// Same talker: fill in details that arrived late.
		if t.Name != "" {
			prev.Name = t.Name
		}
		if t.Place != "" {
			prev.Place = t.Place
		}
		b.mu.Unlock()
		return
	}
	c := t
	b.talker = &c
	b.mu.Unlock()
	if prev != nil {
		p := *prev
		b.Emit(Event{Kind: EventTalkEnd, Talker: &p})
	}
	e := t
	b.Emit(Event{Kind: EventTalkStart, Talker: &e})
}

// UpdateTalker fills in name and place of the current talker (EchoLink
// sends them after the audio starts). It emits a fresh TalkStart so the UI
// and last-heard list pick them up.
func (b *Base) UpdateTalker(callsign, name, place string) {
	b.mu.Lock()
	t := b.talker
	if t == nil || (callsign != "" && t.Callsign != callsign) {
		b.mu.Unlock()
		return
	}
	if t.Name == name && t.Place == place {
		b.mu.Unlock()
		return
	}
	if name != "" {
		t.Name = name
	}
	if place != "" {
		t.Place = place
	}
	c := *t
	b.mu.Unlock()
	b.Emit(Event{Kind: EventTalkStart, Talker: &c})
}

// TalkEnd records that the current talker stopped.
func (b *Base) TalkEnd() {
	b.mu.Lock()
	prev := b.talker
	b.talker = nil
	b.mu.Unlock()
	if prev != nil {
		b.Emit(Event{Kind: EventTalkEnd, Talker: prev})
	}
}

// Talker returns the current talker, or nil.
func (b *Base) Talker() *Talker {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.talker == nil {
		return nil
	}
	c := *b.talker
	return &c
}

// Talking reports whether someone is talking.
func (b *Base) Talking() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.talker != nil
}

// State returns the current state.
func (b *Base) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st.State
}

// Status returns a snapshot.
func (b *Base) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.st
	if st.Target != nil {
		t := *st.Target
		st.Target = &t
	}
	if b.talker != nil {
		t := *b.talker
		st.Talker = &t
	}
	if st.Registration != nil {
		r := *st.Registration
		st.Registration = &r
	}
	if st.Link != nil {
		l := *st.Link
		st.Link = &l
	}
	st.Links = append([]string(nil), st.Links...)
	if st.Levels != nil {
		r := *st.Levels
		r.Warnings = append([]string(nil), r.Warnings...)
		st.Levels = &r
	}
	return st
}

// Flush drops queued audio (after a disconnect).
func (b *Base) Flush() {
	for {
		select {
		case <-b.recv:
		default:
			return
		}
	}
}
