// Package mode defines what one network looks like to the native node.
//
// AllStar, EchoLink, BrandMeister, the other DMR (Homebrew) masters, YSF,
// P25, NXDN and M17 are each a Mode, and all of them are equal: the node has
// one radio, exactly one mode is active at a time, the mode's audio goes out
// to the radio and the radio's audio goes out to the mode. Networks are never
// bridged to each other.
//
// Audio is 8 kHz audio.Frame both ways. A Mode never generates local
// telemetry: the ID belongs to the node and reaches the radio
// only.
package mode

import (
	"context"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// Mode names. They are the keys of the modes section of the node config
// and the names the control API uses.
const (
	AllStar  = "allstar"
	EchoLink = "echolink"
	BM       = "bm"   // BrandMeister
	DMR      = "dmr"  // any other Homebrew master
	TGIF     = "tgif" // the TGIF Homebrew master
	YSF      = "ysf"
	P25      = "p25"
	NXDN     = "nxdn"
	M17      = "m17"
	EchoTest = "echotest" // local echo test, no network
)

// Target is where a mode connects: a node number (AllStar), a callsign,
// conference or node number (EchoLink), a talkgroup (BM, DMR, P25, NXDN), a
// reflector (YSF) or a reflector and module (M17, "M17-XXX B"). Each mode's
// Spec says what ID means for it. Host and Port name a reflector directly
// when the mode needs an address and its reflector list does not have the
// ID.
type Target struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	Slot    int    `json:"slot,omitempty"`    // DMR timeslot 1 or 2; 0 = the mode's default
	Private bool   `json:"private,omitempty"` // DMR private call
}

// State is where a mode is in its life.
type State string

// States.
const (
	StateIdle       State = "idle"       // not active, or active with no target
	StateStarting   State = "starting"   // logging in (directory, master)
	StateReady      State = "ready"      // logged in, no target
	StateConnecting State = "connecting" // linking to the target
	StateConnected  State = "connected"  // linked to the target
	StateError      State = "error"      // the last attempt failed (Status.Error)
)

// Talker is who is talking on the network now (or was, in Heard).
type Talker struct {
	Callsign string    `json:"callsign,omitempty"`
	Name     string    `json:"name,omitempty"`
	Place    string    `json:"place,omitempty"`
	ID       uint32    `json:"id,omitempty"`     // DMR or NXDN radio id, when the network sends one
	Target   string    `json:"target,omitempty"` // talkgroup or room the call is on
	Since    time.Time `json:"since"`
}

// EventKind says what an Event reports.
type EventKind string

// Event kinds.
const (
	EventTalkStart EventKind = "talk_start" // Talker started talking
	EventTalkEnd   EventKind = "talk_end"   // Talker stopped
	EventState     EventKind = "state"      // State, Target or Detail changed
	EventError     EventKind = "error"      // Err is a problem worth showing
	EventText      EventKind = "text"       // Text arrived (EchoLink chat, AllStar text)
)

// Event is something a mode reports. Events are for display: the node does
// not need them for audio, which flows through Recv and Talking.
type Event struct {
	Kind   EventKind `json:"kind"`
	Mode   string    `json:"mode"`
	At     time.Time `json:"at"`
	Talker *Talker   `json:"talker,omitempty"`
	State  State     `json:"state,omitempty"`
	Target *Target   `json:"target,omitempty"`
	Text   string    `json:"text,omitempty"`
	Err    string    `json:"error,omitempty"`
}

// Registration is the AllStarLink registration, for the UI.
type Registration struct {
	Registered  bool      `json:"registered"`
	Ready       bool      `json:"ready"` // registered and our DNS record shows our IP
	PublicIP    string    `json:"publicIp,omitempty"`
	DNSOK       bool      `json:"dnsOk"`
	LastAttempt time.Time `json:"lastAttempt,omitempty"`
	LastSuccess time.Time `json:"lastSuccess,omitempty"`
	// Since is when the node was first accepted at its current address.
	Since      time.Time `json:"since,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
	HTTPStatus int       `json:"httpStatus,omitempty"`
}

// LinkStats are the counters of the current AllStar or EchoLink link, for
// diagnosing audio problems. Proto says which: "" (AllStar, the IAX2
// counters) or "echolink" (audio packets; RxVoiceFull is the packets
// received, TxVoice the packets sent, Late the duplicate or late packets
// dropped, ParseErrors the undecodable ones, Dropped the frames the node
// did not take in time and TxErrors the packets the socket refused).
type LinkStats struct {
	Proto       string `json:"proto,omitempty"`
	RxVoiceFull uint64 `json:"rxVoiceFull"` // voice full frames received
	RxVoiceMini uint64 `json:"rxVoiceMini"` // voice mini frames received
	TxVoice     uint64 `json:"txVoice"`
	VNAKSent    uint64 `json:"vnakSent"`
	VNAKRecv    uint64 `json:"vnakRecv"`
	Retransmits uint64 `json:"retransmits"`
	ParseErrors uint64 `json:"parseErrors"`
	Dropped     uint64 `json:"dropped"`
	Gaps        uint64 `json:"gaps"`
	Late        uint64 `json:"late,omitempty"`
	TxErrors    uint64 `json:"txErrors,omitempty"`
}

// Status is a snapshot of a mode for the UI. It never carries a password.
type Status struct {
	Mode   string    `json:"mode"`
	Title  string    `json:"title"`
	Active bool      `json:"active"`
	State  State     `json:"state"`
	Target *Target   `json:"target,omitempty"`
	Since  time.Time `json:"since,omitempty"` // when State was entered
	// Detail is a short line about the login or link ("TG 91", "logged in
	// to the directory").
	Detail string  `json:"detail,omitempty"`
	Error  string  `json:"error,omitempty"` // why the last attempt failed
	Talker *Talker `json:"talker,omitempty"`
	// Registration: AllStar only.
	Registration *Registration `json:"registration,omitempty"`
	// Link: counters of the current link (AllStar and EchoLink).
	Link *LinkStats `json:"link,omitempty"`
	// Links: the far side's link list (AllStar) or conference members, when known.
	Links []string `json:"links,omitempty"`
	// Levels: the level report of the last recording (echo test only).
	Levels *audio.Report `json:"levels,omitempty"`
}

// Heard is one finished (or, with a zero End, ongoing) call, in the one
// format the node keeps for every mode.
type Heard struct {
	Mode     string        `json:"mode"`
	Callsign string        `json:"callsign,omitempty"`
	Name     string        `json:"name,omitempty"`
	Place    string        `json:"place,omitempty"`
	ID       uint32        `json:"id,omitempty"`
	Target   string        `json:"target,omitempty"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end,omitempty"`
	Duration time.Duration `json:"durationNs,omitempty"`
}

// Mode is one network. The node builds it from its settings (Spec.New),
// calls Start when the operator selects it and Stop when another mode is
// selected; a Mode can be started again after Stop. Methods are safe for
// concurrent use.
type Mode interface {
	// Name is the mode's name (AllStar, EchoLink, ...).
	Name() string
	// Start brings the mode up: a directory or master login when it has
	// one. It returns once the start is under way; progress shows in
	// Status and Events.
	Start(ctx context.Context) error
	// Connect links to t, dropping a link the mode already has. It
	// returns once the attempt is under way (or with an error when t is
	// not valid for the mode); the outcome shows in Status and Events.
	Connect(ctx context.Context, t Target) error
	// Disconnect drops the link. The mode stays started.
	Disconnect(ctx context.Context) error
	// Stop drops the link and logs out. It waits for the mode's
	// goroutines (bounded).
	Stop() error

	// Send is one frame of the radio's audio for the network, called
	// every 20 ms while the local station transmits. It never blocks.
	Send(f audio.Frame)
	// EndTx says the local station stopped transmitting.
	EndTx()
	// Recv carries the network's audio for the radio. The channel stays
	// the same for the life of the Mode.
	Recv() <-chan audio.Frame
	// Talking reports whether the network is sending audio now (keyed),
	// including short gaps between frames.
	Talking() bool

	// Events carries what the mode reports. The channel stays the same
	// for the life of the Mode; events are dropped when it is full.
	Events() <-chan Event
	// Status is a snapshot for the UI.
	Status() Status
}

// Background is implemented by a mode that has work to do whenever it is
// configured, active or not: AllStar keeps its registration current so
// that selecting it is instant. The node runs RunBackground in its own
// goroutine until ctx ends (the settings changed or the node stops).
type Background interface {
	RunBackground(ctx context.Context)
}
