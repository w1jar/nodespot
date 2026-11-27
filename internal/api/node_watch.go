package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/nowcalling"
	"github.com/w1jar/nodespot/internal/system"
)

// Node is the node daemon's control API as the server uses it.
// *node.Client implements it.
type Node interface {
	Status(ctx context.Context) (node.Status, error)
	Modes(ctx context.Context) ([]node.ModeInfo, error)
	Station(ctx context.Context) (node.Station, error)
	SaveStation(ctx context.Context, st node.Station) (node.Status, error)
	SaveMode(ctx context.Context, name string, s mode.Settings) ([]node.ModeInfo, error)
	DeleteMode(ctx context.Context, name string) error
	Select(ctx context.Context, name string, t *mode.Target) (node.Status, error)
	Connect(ctx context.Context, t mode.Target) (node.Status, error)
	Disconnect(ctx context.Context) (node.Status, error)
	LastHeard(ctx context.Context, limit int) ([]mode.Heard, error)
	RadioLevels(ctx context.Context) (radio.Levels, error)
	InputLevel(ctx context.Context) (node.InputLevel, error)
	RadioTune(ctx context.Context, rx, tx *float64, boost *bool) (radio.Levels, error)
	AutosetRx(ctx context.Context, cancel bool) (node.AutosetStatus, error)
	AutosetStatus(ctx context.Context) (node.AutosetStatus, error)
	RadioInfo(ctx context.Context) (node.RadioDetail, error)
	RadioProgram(ctx context.Context) (node.Status, error)
	RadioTuningDismiss(ctx context.Context) (node.Status, error)
	TestTone(ctx context.Context, seconds float64, stop bool) (node.Status, error)
	Reload(ctx context.Context) (node.Status, error)
	SearchDirectory(ctx context.Context, name, q string, limit int) (mode.DirResult, error)
	Subscribe(ctx context.Context) (node.Status, <-chan node.Event, error)
}

var _ Node = (*node.Client)(nil)

// errNoNode is returned when the server has no node client at all.
var errNoNode = errors.New("no node is configured")

// Mode names the server treats specially.
const (
	modeAllStar  = mode.AllStar
	modeEchoLink = mode.EchoLink
)

// NodeJSON is the API Node object (GET /api/node, the SSE event "node"):
// the node's status plus whether the daemon answers and whether the
// one-screen station setup still has to run.
type NodeJSON struct {
	node.Status
	// Running is false while the node daemon cannot be reached (Error says
	// why; the rest is the last status known).
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
	// SetupNeeded is true until the station has a callsign.
	SetupNeeded bool `json:"setupNeeded"`
}

// nodeWatch is the node's status as last reported, kept fresh from its
// event stream so that most reads never touch the socket.
type nodeWatch struct {
	mu      sync.RWMutex
	st      node.Status
	running bool
	err     string
	modes   []string // every mode name the node knows
	allstar string   // this station's AllStar node number ("" when none)
}

func newNodeWatch() *nodeWatch { return &nodeWatch{err: "not connected yet"} }

func (n *nodeWatch) status() (node.Status, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.st, n.running
}

func (n *nodeWatch) json() NodeJSON {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := NodeJSON{Status: n.st, Running: n.running, Error: n.err}
	out.SetupNeeded = n.running && !n.st.Configured
	return out
}

func (n *nodeWatch) set(st node.Status) {
	n.mu.Lock()
	n.st, n.running, n.err = st, true, ""
	n.mu.Unlock()
}

func (n *nodeWatch) down(err error) {
	n.mu.Lock()
	n.running = false
	n.err = "the node is not running"
	if err != nil && !errors.Is(err, node.ErrUnavailable) {
		n.err = err.Error()
	}
	n.mu.Unlock()
}

func (n *nodeWatch) setModes(l []node.ModeInfo) {
	names := make([]string, 0, len(l))
	as := ""
	for _, m := range l {
		names = append(names, m.Name)
		if m.Name == modeAllStar {
			as = m.Settings.Get("node", "")
		}
	}
	n.mu.Lock()
	n.modes, n.allstar = names, as
	n.mu.Unlock()
}

func (n *nodeWatch) modeNames() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.modes
}

func (n *nodeWatch) allStarNode() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.allstar
}

// linkedTo reports whether st has mode m active and linked to target (any
// target when target is empty).
func linkedTo(st node.Status, m, target string) bool {
	if st.Active != m || st.Mode == nil || st.Mode.Target == nil {
		return false
	}
	switch st.Mode.State {
	case mode.StateConnected, mode.StateConnecting:
	default:
		return false
	}
	return target == "" || strings.EqualFold(st.Mode.Target.ID, target)
}

// broadcastNodeStatus sends the cached status as the SSE event "node".
func (s *Server) broadcastNodeStatus() {
	f := frame("node", s.nodeJSON())
	s.hub.broadcast(sseMsg{all: f})
}

// nodeChanged refreshes the cached status soon (after a command; the event
// stream reports most changes too, this makes the reply and the next read
// agree).
func (s *Server) nodeChanged() {
	select {
	case s.nwKick <- struct{}{}:
	default:
	}
}

// adopt caches a status a command returned.
func (s *Server) adopt(st node.Status) {
	s.nw.set(st)
	s.broadcastNodeStatus()
}

// watchNode follows the node's event stream until ctx is done, and
// subscribes again (with backoff) whenever the stream ends.
func (s *Server) watchNode(ctx context.Context) {
	if s.d.Node == nil {
		return
	}
	go s.refreshLoop(ctx)
	wait := s.NodeRetry
	for ctx.Err() == nil {
		st, ch, err := s.d.Node.Subscribe(ctx)
		if err != nil {
			s.nw.down(err)
			s.broadcastNodeStatus()
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait < 8*s.NodeRetry {
				wait *= 2
			}
			continue
		}
		wait = s.NodeRetry
		// Modes first: once the status says running, the mode names
		// (for validating favorites and schedules) are known.
		s.refreshModes(ctx)
		s.nw.set(st)
		if h, err := s.d.Node.LastHeard(ctx, 2*nowcalling.RecentMax); err == nil {
			s.nc.tr.Seed(h)
		}
		s.broadcastNodeStatus()
		for ev := range ch {
			s.nodeEvent(ctx, ev)
		}
		if ctx.Err() != nil {
			return
		}
		s.nw.down(nil)
		s.nc.tr.TalkEnd("")
		s.broadcastNodeStatus()
	}
}

// refreshLoop re-reads the status when nodeChanged asked, at most a few
// times a second.
func (s *Server) refreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.nwKick:
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := s.d.Node.Status(cctx)
		cancel()
		if err == nil {
			s.adopt(st)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// refreshModes re-reads the mode list (names and the AllStar node number).
func (s *Server) refreshModes(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if l, err := s.d.Node.Modes(cctx); err == nil {
		s.nw.setModes(l)
	}
}

// nodeEvent folds one event of the node's stream.
func (s *Server) nodeEvent(ctx context.Context, ev node.Event) {
	switch ev.Kind {
	case node.EventPTT, node.EventCOS, node.EventRx, node.EventTone:
		if ev.On != nil {
			s.nw.mu.Lock()
			switch ev.Kind {
			case node.EventPTT:
				s.nw.st.Radio.PTT = *ev.On
			case node.EventCOS:
				s.nw.st.Radio.COS = *ev.On
			case node.EventTone:
				s.nw.st.Radio.TestTone = *ev.On
			default:
				// A local station's audio started or stopped going to
				// the active mode: the Dashboard's "Local station".
				s.nw.st.Radio.RxActive = *ev.On
			}
			s.nw.mu.Unlock()
			s.broadcastNodeStatus()
		}
	case node.EventMode:
		if ev.Mode == nil {
			return
		}
		switch ev.Mode.Kind {
		case mode.EventTalkStart:
			if ev.Mode.Talker != nil {
				s.nc.tr.TalkStart(ev.Mode.Mode, *ev.Mode.Talker)
			}
			s.nodeChanged()
		case mode.EventTalkEnd:
			s.nc.tr.TalkEnd(ev.Mode.Mode)
			s.nodeChanged()
		case mode.EventState:
			s.nodeChanged()
		case mode.EventError, mode.EventText:
			s.nodeChanged()
			s.broadcastNodeEvent(ev)
		}
	case node.EventSelect:
		s.nc.tr.TalkEnd("")
		s.nodeChanged()
	case node.EventConfig:
		s.refreshModes(ctx)
		s.nodeChanged()
		s.broadcastNodeEvent(ev)
	case node.EventID, node.EventLog:
		s.nodeChanged()
		s.broadcastNodeEvent(ev)
	}
}

// broadcastNodeEvent forwards an event worth showing (a log line, a mode
// error or text, a station ID, a config change) as the SSE event
// "nodeevent" to logged-in clients.
func (s *Server) broadcastNodeEvent(ev node.Event) {
	f := frame("nodeevent", ev)
	s.hub.broadcast(sseMsg{all: f})
}

// SystemNode adapts a node client to system.New: the start time and the
// active mode, for the System tab.
func SystemNode(n Node) func(ctx context.Context) (system.NodeInfo, error) {
	return func(ctx context.Context) (system.NodeInfo, error) {
		if n == nil {
			return system.NodeInfo{}, errNoNode
		}
		st, err := n.Status(ctx)
		if err != nil {
			return system.NodeInfo{}, err
		}
		return system.NodeInfo{Started: st.Started, Mode: st.Active}, nil
	}
}
