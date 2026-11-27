package public

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// The structs below are what the public page receives. They are deliberately
// separate from the admin API structs: a field added to an admin struct can
// never leak here, and none of these has a place for an IP address, a host
// name, a login, an ID or system information.

// Station is the node itself.
type Station struct {
	// Callsign is the station's own callsign (it is sent on the air).
	Callsign string `json:"callsign"`
	// Mode is the active mode ("" when idle) and ModeTitle its display name.
	Mode      string `json:"mode"`
	ModeTitle string `json:"modeTitle"`
	// Target is the node, talkgroup or reflector the mode is on ("" when
	// none, or when it looks like an address).
	Target string `json:"target"`
	// State is the mode's state (idle, connecting, ready, connected, ...).
	State string `json:"state"`
	// TxKeyed: the radio transmits (the network talks); RxKeyed: the radio
	// receives (the local user talks).
	TxKeyed bool `json:"txKeyed"`
	RxKeyed bool `json:"rxKeyed"`
}

// Call is one call heard from the network. Callsign, Name and Location are
// omitted when callsigns are hidden.
type Call struct {
	Mode     string `json:"mode"`
	Callsign string `json:"callsign,omitempty"`
	Target   string `json:"target,omitempty"`
	At       int64  `json:"at"`       // start, unix seconds
	Duration int64  `json:"duration"` // seconds; 0 while on the air
	Name     string `json:"name,omitempty"`
	Location string `json:"location,omitempty"`
}

// Snapshot is the whole page state (status.json and the SSE "snapshot").
type Snapshot struct {
	Title     string  `json:"title"`
	Now       int64   `json:"now"` // server time, unix seconds
	Callsigns bool    `json:"callsigns"`
	ShowHeard bool    `json:"showHeard"`
	Station   Station `json:"station"`
	// Calling is the call on the air (nil when nobody talks).
	Calling *Call `json:"calling"`
	// Heard are the last heard calls, newest first (only with ShowHeard).
	Heard []Call `json:"heard,omitempty"`
}

// HeardLimit is the number of heard calls shown.
const HeardLimit = 15

// CallIn is one call as the server hands it over. It is not sent anywhere;
// Build copies the fields that may be published.
type CallIn struct {
	Mode string
	// Call is the caller's callsign; Node the AllStar or EchoLink node
	// number when the caller is a node.
	Call, Node string
	Target     string
	Start, End time.Time
	// Name and Place are known from a cache only (never looked up for a
	// visitor).
	Name, Place string
}

// Input is what Options.Input returns.
type Input struct {
	Callsign         string
	Mode, Target     string
	State            string
	TxKeyed, RxKeyed bool
	Calling          *CallIn
	Heard            []CallIn
}

var (
	reIPv4   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	reIPv6   = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{1,4}:){4,7}[0-9a-f]{0,4}\b|[0-9a-f:]*::[0-9a-f:]*[0-9a-f]`)
	reTarget = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _#-]{0,23}$`) // no dots or colons: never a host name or an address
	reCall   = regexp.MustCompile(`^[A-Za-z0-9/-]{3,12}$`)
	reNode   = regexp.MustCompile(`^[0-9]{1,7}$`)
	reState  = regexp.MustCompile(`^[a-z]{1,16}$`)
)

// hasIP reports whether s contains something that looks like an IP address.
func hasIP(s string) bool { return reIPv4.MatchString(s) || reIPv6.MatchString(s) }

// scrub removes IP-address-like text from free text (defense in depth).
func scrub(s string) string {
	if !hasIP(s) {
		return s
	}
	s = reIPv4.ReplaceAllString(s, "")
	s = reIPv6.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// callsign accepts what looks like an amateur callsign (letters and a
// digit): a DMR ID, "Echo test" and the like are dropped.
func callsign(s string) string {
	s = strings.TrimSpace(s)
	if !reCall.MatchString(s) || strings.IndexFunc(s, unicode.IsLetter) < 0 || strings.IndexFunc(s, unicode.IsDigit) < 0 {
		return ""
	}
	return strings.ToUpper(s)
}

func text(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune("<>\"&", r) {
			return -1
		}
		return r
	}, scrub(strings.TrimSpace(s)))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return strings.TrimSpace(s)
}

func target(s string) string {
	s = strings.TrimSpace(s)
	if reTarget.MatchString(s) {
		return s
	}
	return ""
}

// titles are the modes the page shows, with fallback display names (the
// registered spec's title wins when its package is linked in).
var titles = map[string]string{
	mode.AllStar: "AllStar", mode.EchoLink: "EchoLink", mode.BM: "BrandMeister", mode.TGIF: "TGIF",
	mode.DMR: "DMR", mode.YSF: "YSF", mode.P25: "P25", mode.NXDN: "NXDN", mode.M17: "M17", mode.EchoTest: "Echo test",
}

// modeTitle returns the display name of a known mode.
func modeTitle(name string) (string, bool) {
	t, ok := titles[name]
	if !ok {
		return "", false
	}
	if sp, ok := mode.Lookup(name); ok && sp.Title != "" {
		t = sp.Title
	}
	return t, true
}

func buildCall(in CallIn, callsigns bool) (Call, bool) {
	if _, ok := modeTitle(in.Mode); !ok {
		return Call{}, false
	}
	c := Call{Mode: in.Mode, Target: target(in.Target), At: in.Start.Unix()}
	if !in.End.IsZero() {
		c.Duration = int64(in.End.Sub(in.Start).Round(time.Second) / time.Second)
		if c.Duration < 1 {
			c.Duration = 1
		}
	}
	if callsigns {
		c.Callsign = callsign(in.Call)
		if c.Callsign == "" && reNode.MatchString(in.Node) {
			c.Callsign = in.Node
		}
		if c.Callsign != "" {
			c.Name, c.Location = text(in.Name, 40), text(in.Place, 40)
		}
	}
	return c, true
}

// Build converts the server's input to the public form under the settings.
// Calls of an unknown mode are left out; with callsigns hidden the callsign,
// name and place are left out.
func Build(in Input, cfg Settings, now time.Time) Snapshot {
	snap := Snapshot{Now: now.Unix(), Callsigns: cfg.ShowCallsigns, ShowHeard: cfg.ShowHeard}
	snap.Station = Station{Callsign: callsign(in.Callsign), TxKeyed: in.TxKeyed, RxKeyed: in.RxKeyed}
	if t, ok := modeTitle(in.Mode); ok {
		snap.Station.Mode, snap.Station.ModeTitle = in.Mode, t
		snap.Station.Target = target(in.Target)
		if reState.MatchString(in.State) {
			snap.Station.State = in.State
		}
	}
	// A down node shows "offline" even though no mode is known.
	if snap.Station.State == "" && in.State == "offline" {
		snap.Station.State = in.State
	}
	if in.Calling != nil {
		if c, ok := buildCall(*in.Calling, cfg.ShowCallsigns); ok {
			snap.Calling = &c
		}
	}
	if cfg.ShowHeard {
		snap.Heard = []Call{}
		for _, h := range in.Heard {
			if len(snap.Heard) >= HeardLimit {
				break
			}
			if c, ok := buildCall(h, cfg.ShowCallsigns); ok {
				snap.Heard = append(snap.Heard, c)
			}
		}
	}
	snap.Title = text(cfg.Title, MaxTitle)
	if snap.Title == "" {
		snap.Title = "Node status"
		if snap.Station.Callsign != "" {
			snap.Title = snap.Station.Callsign + " node status"
		}
	}
	return snap
}
