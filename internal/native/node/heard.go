package node

import (
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// HeardMax is how many calls the last-heard list keeps.
const HeardMax = 200

// heardLog is the last-heard list, newest first, in one format for every
// mode. A call that is still going has a zero End.
type heardLog struct {
	mu    sync.Mutex
	calls []mode.Heard // oldest first
}

// start records that t started talking on m.
func (h *heardLog) start(m string, t mode.Talker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := h.open(m); i >= 0 {
		c := &h.calls[i]
		if c.Callsign == t.Callsign && c.ID == t.ID {
			// The same talker again (an update): refresh the details.
			c.Name, c.Place, c.Target, c.Via, c.Slot = t.Name, t.Place, t.Target, t.Via, t.Slot
			return
		}
		h.finish(i, t.Since)
	}
	h.calls = append(h.calls, mode.Heard{
		Mode: m, Callsign: t.Callsign, Name: t.Name, Place: t.Place,
		ID: t.ID, Target: t.Target, Via: t.Via, Slot: t.Slot, Start: t.Since,
	})
	if len(h.calls) > HeardMax {
		h.calls = append([]mode.Heard(nil), h.calls[len(h.calls)-HeardMax:]...)
	}
}

// end closes m's open call.
func (h *heardLog) end(m string, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := h.open(m); i >= 0 {
		h.finish(i, at)
	}
}

func (h *heardLog) open(m string) int {
	for i := len(h.calls) - 1; i >= 0; i-- {
		if h.calls[i].Mode == m && h.calls[i].End.IsZero() {
			return i
		}
	}
	return -1
}

func (h *heardLog) finish(i int, at time.Time) {
	c := &h.calls[i]
	if at.Before(c.Start) {
		at = c.Start
	}
	c.End = at
	c.Duration = at.Sub(c.Start)
}

// list returns up to n calls, newest first (all when n <= 0).
func (h *heardLog) list(n int) []mode.Heard {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n <= 0 || n > len(h.calls) {
		n = len(h.calls)
	}
	out := make([]mode.Heard, 0, n)
	for i := len(h.calls) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, h.calls[i])
	}
	return out
}

// EventKind says what a node Event reports.
type EventKind string

// Node event kinds.
const (
	EventMode EventKind = "mode" // Mode carries an event from a mode
	EventPTT  EventKind = "ptt"  // On: the transmitter keyed or unkeyed
	EventCOS  EventKind = "cos"  // On: a local signal opened or closed the squelch
	// EventRx: On: a local transmission started or stopped going to the
	// active mode (Status.Radio.RxActive).
	EventRx     EventKind = "rx"
	EventSelect EventKind = "select" // Text: the active mode changed (empty: idle)
	EventID     EventKind = "id"     // Text: the station ID started (why)
	EventConfig EventKind = "config" // the config was saved or reloaded
	EventLog    EventKind = "log"    // Text: a problem worth showing
	// EventTone: On: the transmit test tone started or ended (TestTone).
	EventTone EventKind = "tone"
)

// Event is what a control subscriber receives.
type Event struct {
	Kind EventKind   `json:"kind"`
	At   time.Time   `json:"at"`
	Mode *mode.Event `json:"mode,omitempty"`
	On   *bool       `json:"on,omitempty"`
	Text string      `json:"text,omitempty"`
}

// hub fans events out to subscribers. A slow subscriber loses events
// rather than holding the node up.
type hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func (h *hub) subscribe() chan Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[chan Event]struct{}{}
	}
	c := make(chan Event, 128)
	h.subs[c] = struct{}{}
	return c
}

func (h *hub) unsubscribe(c chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, c)
}

func (h *hub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- ev:
		default:
		}
	}
}
