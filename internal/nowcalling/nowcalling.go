// Package nowcalling keeps who is talking right now on the active mode and
// the last few talkers, for the dashboard's "Now calling" strip. The node
// reports every call of every mode the same way (a talk start and a talk
// end), so the tracker is the same for AllStar, EchoLink and the digital
// modes. State is in memory; a restart of nodespot seeds it from the node's
// last heard list.
package nowcalling

import (
	"strconv"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
)

const (
	// RecentMax is how many finished calls are kept.
	RecentMax = 10
	// MinDuration: shorter keyups are glitches and are not recorded.
	MinDuration = 500 * time.Millisecond
)

// Call is one talker. Fields are filled from the node's report and then by
// the Resolve hook (name and place from the radioid.net list, hamdb.org or
// the AllStar node list).
type Call struct {
	ID string `json:"id"`
	// Kind is the mode name (allstar, echolink, bm, dmr, tgif, ysf, p25,
	// nxdn, m17, echotest).
	Kind string `json:"kind"`
	// Call is the callsign ("" when only an ID is known).
	Call string `json:"call,omitempty"`
	// First is the first name, Name the full name.
	First   string `json:"first,omitempty"`
	Name    string `json:"name,omitempty"`
	City    string `json:"city,omitempty"`
	State   string `json:"state,omitempty"`
	Country string `json:"country,omitempty"`
	// Location is free text (the node's report, or the AllStar node list)
	// when the place is not split into city and state.
	Location string `json:"location,omitempty"`
	// Source names where the person came from when it was not the
	// radioid.net list or the node ("hamdb.org"); empty otherwise.
	Source string `json:"source,omitempty"`
	// DMRID is the talker's radio ID when the network sends one.
	DMRID string `json:"dmrId,omitempty"`
	// Node is the AllStar node number of the talker, Desc its description.
	Node string `json:"node,omitempty"`
	Desc string `json:"desc,omitempty"`
	// Target is the talkgroup, room or reflector the call is on, and
	// TargetName its name when a favorite knows it.
	Target     string `json:"target,omitempty"`
	TargetName string `json:"targetName,omitempty"`
	// Start is RFC 3339 UTC. End is empty while talking.
	Start  string `json:"start"`
	End    string `json:"end,omitempty"`
	Active bool   `json:"active"`

	start time.Time
}

// Snapshot is what the page shows.
type Snapshot struct {
	// Current is the talker shown (nil: nobody is talking).
	Current *Call `json:"current"`
	// Recent holds finished calls, newest first.
	Recent []Call `json:"recent"`
}

// Tracker folds the node's talk events. The zero value is not usable; use
// New.
type Tracker struct {
	// Now is the clock (tests).
	Now func() time.Time
	// Resolve fills the person and place of a new call (may be nil).
	Resolve func(*Call)
	// OnChange is called without locks held when the snapshot changed.
	OnChange func(Snapshot)

	mu     sync.Mutex
	cur    *Call
	recent []Call
}

// New returns a Tracker.
func New() *Tracker { return &Tracker{Now: time.Now} }

func id(m string, start time.Time, call string) string {
	return m + ":" + start.UTC().Format("20060102150405.000") + ":" + call
}

// fromTalker builds a call from what the node reported.
func fromTalker(m string, t mode.Talker, start time.Time) Call {
	c := Call{Kind: m, Call: t.Callsign, Name: t.Name, Location: t.Place, Target: t.Target, start: start}
	if t.ID != 0 {
		c.DMRID = strconv.FormatUint(uint64(t.ID), 10)
	}
	if m == mode.AllStar && isDigits(t.Callsign) {
		// An AllStar talker is a node number until the node list names it.
		c.Node, c.Call = t.Callsign, ""
	}
	c.ID = id(m, start, t.Callsign)
	c.Start = start.UTC().Format(time.RFC3339)
	return c
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TalkStart reports that t started talking on mode m. A new talker
// replaces the one shown (the older call is finished).
func (t *Tracker) TalkStart(m string, tk mode.Talker) {
	now := t.Now()
	start := tk.Since
	if start.IsZero() || start.After(now) {
		start = now
	}
	c := fromTalker(m, tk, start)
	c.Active = true
	if t.Resolve != nil {
		t.Resolve(&c)
	}
	t.mu.Lock()
	if t.cur != nil && t.cur.ID == c.ID {
		t.mu.Unlock()
		return
	}
	t.finishLocked(now)
	t.cur = &c
	s := t.snapshotLocked()
	t.mu.Unlock()
	t.emit(s)
}

// TalkEnd reports that the talker on mode m stopped ("" ends whoever is
// talking).
func (t *Tracker) TalkEnd(m string) {
	t.mu.Lock()
	if t.cur == nil || (m != "" && t.cur.Kind != m) {
		t.mu.Unlock()
		return
	}
	t.finishLocked(t.Now())
	s := t.snapshotLocked()
	t.mu.Unlock()
	t.emit(s)
}

// Seed fills the recent list from the node's last heard list (newest
// first), keeping what the tracker already holds.
func (t *Tracker) Seed(heard []mode.Heard) {
	var add []Call
	for _, h := range heard {
		if h.End.IsZero() {
			continue
		}
		c := fromTalker(h.Mode, mode.Talker{Callsign: h.Callsign, Name: h.Name, Place: h.Place, ID: h.ID, Target: h.Target}, h.Start)
		c.End = h.End.UTC().Format(time.RFC3339)
		if t.Resolve != nil {
			t.Resolve(&c)
		}
		add = append(add, c)
	}
	t.mu.Lock()
	seen := map[string]bool{}
	for _, r := range t.recent {
		seen[r.ID] = true
	}
	for _, c := range add {
		if !seen[c.ID] {
			t.recent = append(t.recent, c)
			seen[c.ID] = true
		}
	}
	t.sortLocked()
	s := t.snapshotLocked()
	t.mu.Unlock()
	t.emit(s)
}

// finishLocked moves the current call to the recent list.
func (t *Tracker) finishLocked(now time.Time) {
	c := t.cur
	t.cur = nil
	if c == nil || now.Sub(c.start) < MinDuration {
		return
	}
	c.Active = false
	c.End = now.UTC().Format(time.RFC3339)
	t.recent = append([]Call{*c}, t.recent...)
	t.sortLocked()
}

func (t *Tracker) sortLocked() {
	r := t.recent
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j].start.After(r[j-1].start); j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
	if len(r) > RecentMax {
		t.recent = r[:RecentMax]
	}
}

// Snapshot returns the current state.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *Tracker) snapshotLocked() Snapshot {
	s := Snapshot{Recent: append([]Call{}, t.recent...)}
	if t.cur != nil {
		c := *t.cur
		s.Current = &c
	}
	return s
}

func (t *Tracker) emit(s Snapshot) {
	if t.OnChange != nil {
		t.OnChange(s)
	}
}

// Refill runs f on the talking call and on the finished calls that still
// have no name (an online lookup may have answered since). When f reports a
// change, OnChange gets the new snapshot.
func (t *Tracker) Refill(f func(*Call) bool) {
	t.mu.Lock()
	changed := false
	one := func(c *Call) {
		if c.Name == "" && f(c) {
			changed = true
		}
	}
	if t.cur != nil {
		one(t.cur)
	}
	for i := range t.recent {
		one(&t.recent[i])
	}
	s := t.snapshotLocked()
	t.mu.Unlock()
	if changed {
		t.emit(s)
	}
}
