package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/w1jar/nodespot/internal/schedules"
	"github.com/w1jar/nodespot/internal/store"
)

// initSchedules wires the schedule engine to this server: its commands go
// to the node like the HTTP handlers' do, its changes to the SSE hub.
func (s *Server) initSchedules() {
	e := s.d.Schedules
	if e == nil {
		return
	}
	if e.Linker == nil {
		e.Linker = schedLinker{s}
	}
	e.OnChange = s.broadcastSchedules
	if e.Logger == nil {
		e.Logger = s.d.Logger
	}
}

// manualSchedules is called after a manual select, connect or disconnect
// on mode (never by the scheduler's own commands): the engine applies the
// configured manual action (pause, pause for today, or cancel the running
// windows).
func (s *Server) manualSchedules(mode, reason string) {
	if s.d.Schedules == nil {
		return
	}
	s.d.Logger.Printf("schedules manual action (%s): %s", s.d.Schedules.ManualActionMode(), reason)
	s.d.Schedules.ManualAction(mode, reason)
}

// schedConflict answers 409 when x overlaps an existing schedule (see
// docs/SCHEDULES.md, "Overlap check"); the caller holds schedMu. It reports
// whether it answered.
func (s *Server) schedConflict(w http.ResponseWriter, x schedules.Schedule) bool {
	list, err := s.d.Store.ListSchedules()
	if err != nil {
		s.internal(w, err)
		return true
	}
	if clash := schedules.Conflicts(x, list); len(clash) > 0 {
		writeErr(w, 409, "schedule_conflict", schedules.ConflictMessage(clash))
		return true
	}
	return false
}

// schedLinker adapts the server to schedules.Linker: a connect selects the
// mode with the target (the node has one radio, so it replaces whatever was
// active), a disconnect drops the link when that mode is active.
type schedLinker struct{ s *Server }

func (l schedLinker) Connect(ctx context.Context, m, target string) error {
	if l.s.d.Node == nil {
		return errNoNode
	}
	t := l.s.resolveTarget(m, target)
	_, err := l.s.d.Node.Select(ctx, m, &t)
	l.s.nodeChanged()
	return err
}

func (l schedLinker) Disconnect(ctx context.Context, m, target string) error {
	if l.s.d.Node == nil {
		return errNoNode
	}
	st, running := l.s.nw.status()
	if running && st.Active != m {
		return nil // another mode is active: nothing of this mode to drop
	}
	if target != "" && running && !linkedTo(st, m, target) {
		return nil
	}
	_, err := l.s.d.Node.Disconnect(ctx)
	l.s.nodeChanged()
	return err
}

func (l schedLinker) Linked(m, target string) (bool, bool) {
	st, running := l.s.nw.status()
	if !running {
		return false, false
	}
	return linkedTo(st, m, target), true
}

// schedulesReady answers 503 when the feature is not wired.
func (s *Server) schedulesReady(w http.ResponseWriter) bool {
	if s.d.Schedules == nil || s.d.Store == nil {
		writeErr(w, 503, "schedules_unavailable", "schedules are not available")
		return false
	}
	return true
}

func (s *Server) handleSchedList(w http.ResponseWriter, r *http.Request, _ store.User) {
	if !s.schedulesReady(w) {
		return
	}
	writeJSON(w, 200, s.d.Schedules.Snapshot())
}

// schedInput is the body of POST and PUT /api/schedules.
type schedInput struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"` // absent means true
	Mode    string `json:"mode"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Days    []int  `json:"days"`
	Date    string `json:"date"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// parseSchedule validates a request body into a Schedule; it writes the 400
// itself.
func (s *Server) parseSchedule(w http.ResponseWriter, r *http.Request) (schedules.Schedule, bool) {
	var in schedInput
	if !decode(w, r, &in, false) {
		return schedules.Schedule{}, false
	}
	x := schedules.Schedule{Name: in.Name, Enabled: in.Enabled == nil || *in.Enabled, Mode: in.Mode, Target: in.Target,
		Action: in.Action, Days: in.Days, Date: in.Date, Start: in.Start, End: in.End}
	if err := x.Normalize(); err != nil {
		writeErr(w, 400, "bad_request", strings.TrimPrefix(err.Error(), schedules.ErrInvalid.Error()+": "))
		return x, false
	}
	if ms := s.nw.modeNames(); len(ms) > 0 && !contains(ms, x.Mode) {
		writeErr(w, 400, "unknown_mode", "the node has no mode "+x.Mode)
		return x, false
	}
	return x, true
}

func (s *Server) handleSchedCreate(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.schedulesReady(w) {
		return
	}
	x, ok := s.parseSchedule(w, r)
	if !ok {
		return
	}
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	if s.schedConflict(w, x) {
		return
	}
	got, err := s.d.Store.AddSchedule(x)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.d.Logger.Printf("schedule %d created (%s %s -> %s) by %s", got.ID, got.Action, got.Mode, got.Target, u.Username)
	s.d.Schedules.Changed(got.ID)
	writeJSON(w, 201, got)
}

func (s *Server) handleSchedUpdate(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.schedulesReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	x, ok := s.parseSchedule(w, r)
	if !ok {
		return
	}
	x.ID = id
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	if s.schedConflict(w, x) {
		return
	}
	got, err := s.d.Store.UpdateSchedule(x)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such schedule")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.d.Logger.Printf("schedule %d updated by %s", id, u.Username)
	s.d.Schedules.Changed(id)
	writeJSON(w, 200, got)
}

func (s *Server) handleSchedDelete(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.schedulesReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.d.Store.DeleteSchedule(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such schedule")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.d.Logger.Printf("schedule %d deleted by %s", id, u.Username)
	s.d.Schedules.Changed(id)
	noContent(w)
}

func (s *Server) handleSchedEnabled(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, 400, "bad_request", "enabled (bool) is required")
		return
	}
	if !s.schedulesReady(w) {
		return
	}
	s.d.Logger.Printf("schedules enabled=%v by %s", *in.Enabled, u.Username)
	s.d.Schedules.SetEnabled(*in.Enabled)
	writeJSON(w, 200, s.d.Schedules.Snapshot())
}

func (s *Server) handleSchedSettings(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.schedulesReady(w) {
		return
	}
	var in struct {
		ManualAction *string `json:"manualAction"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.ManualAction == nil {
		writeErr(w, 400, "bad_request", "manualAction is required")
		return
	}
	if err := s.d.Schedules.SetManualAction(*in.ManualAction); err != nil {
		if !schedules.ValidManualAction(*in.ManualAction) {
			writeErr(w, 400, "bad_request", err.Error())
		} else {
			s.internal(w, err)
		}
		return
	}
	s.d.Logger.Printf("schedules manual action=%s by %s", *in.ManualAction, u.Username)
	writeJSON(w, 200, s.d.Schedules.Snapshot())
}
