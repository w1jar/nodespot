package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/store"
)

// sseMsg is a pre-framed SSE message in two variants: for privileged
// clients (admin/operator) and for viewers.
type sseMsg struct {
	priv, pub []byte
	// admin, when set, is sent to admin clients instead of priv/pub; a
	// message with only admin set reaches admins and nobody else.
	admin []byte
}

func frame(event string, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte("null")
	}
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
}

type client struct {
	ch    chan sseMsg
	priv  atomic.Bool
	admin atomic.Bool
	done  chan struct{}
	once  sync.Once
}

func (c *client) close() { c.once.Do(func() { close(c.done) }) }

// hub fans events out to SSE clients. A client whose buffer is full is
// dropped (its handler returns, the browser reconnects and gets full state).
type hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	onCount func(n int)
	dropped atomic.Uint64
}

func newHub(onCount func(int)) *hub {
	return &hub{clients: map[*client]struct{}{}, onCount: onCount}
}

const clientBuffer = 64

func (h *hub) add(priv bool) *client {
	c := &client{ch: make(chan sseMsg, clientBuffer), done: make(chan struct{})}
	c.priv.Store(priv)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	n := len(h.clients)
	h.mu.Unlock()
	h.onCount(n)
	return c
}

func (h *hub) remove(c *client) {
	h.mu.Lock()
	_, ok := h.clients[c]
	delete(h.clients, c)
	n := len(h.clients)
	h.mu.Unlock()
	c.close()
	if ok {
		h.onCount(n)
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hub) broadcast(m sseMsg) {
	h.mu.Lock()
	var dropped []*client
	for c := range h.clients {
		select {
		case c.ch <- m:
		default:
			dropped = append(dropped, c)
			delete(h.clients, c)
		}
	}
	n := len(h.clients)
	h.mu.Unlock()
	for _, c := range dropped {
		c.close()
		h.dropped.Add(1)
	}
	if len(dropped) > 0 {
		h.onCount(n)
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	cs := h.clients
	h.clients = map[*client]struct{}{}
	h.mu.Unlock()
	for c := range cs {
		c.close()
	}
}

func (s *Server) broadcastFavorites() {
	if s.d.FavStats != nil {
		s.d.FavStats.Refresh() // the next node to scan may have changed
	}
	favs, err := s.favoritesJSON("")
	if err != nil {
		s.d.Logger.Printf("favorites broadcast: %v", err)
		return
	}
	f := frame("favorites", favs)
	s.hub.broadcast(sseMsg{priv: f, pub: f})
}

// broadcastSchedules sends the schedules snapshot (the same frame for all
// roles).
func (s *Server) broadcastSchedules() {
	if s.d.Schedules == nil {
		return
	}
	f := frame("schedules", s.d.Schedules.Snapshot())
	s.hub.broadcast(sseMsg{priv: f, pub: f})
}

// updateInterval follows the number of SSE clients; the argument is
// ignored.
func (s *Server) updateInterval(int) {
	if s.d.FavStats != nil {
		s.d.FavStats.SetViewers(s.hub.count())
	}
}

// systemLoop pushes System to clients every SystemInterval while any are
// connected.
func (s *Server) systemLoop(ctx context.Context) {
	t := time.NewTicker(s.SystemInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hub.count() == 0 {
				continue
			}
			f := frame("system", s.systemJSON(ctx))
			s.hub.broadcast(sseMsg{priv: f, pub: f})
			if s.d.Updates != nil {
				s.broadcastUpdates()
			}
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, u store.User) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	c := s.hub.add(auth.RoleRank(u.Role) >= auth.RoleRank(store.RoleOperator))
	c.admin.Store(u.Role == store.RoleAdmin)
	defer s.hub.remove(c)

	nj := s.nodeJSON()
	if !write(frame("hello", map[string]any{"user": userJSON(u), "version": s.d.Version, "instance": s.instance,
		"settings": s.settings(), "setupNeeded": nj.SetupNeeded})) ||
		!write(frame("node", nj)) {
		return
	}
	if !write(frame("nowcalling", s.nc.tr.Snapshot())) {
		return
	}
	if c.admin.Load() && s.d.Updates != nil && !write(frame("updates", s.updatesBadge())) {
		return
	}
	ping := time.NewTicker(s.PingInterval)
	defer ping.Stop()
	token := auth.TokenFrom(r)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.stop:
			return
		case <-c.done:
			return
		case m := <-c.ch:
			b := m.pub
			if c.priv.Load() {
				b = m.priv
			}
			if m.admin != nil {
				b = nil
				if c.admin.Load() {
					b = m.admin
				}
			}
			if len(b) == 0 {
				continue
			}
			if !write(b) {
				return
			}
		case <-ping.C:
			// Re-validate: logout, deletion or a role change take effect.
			cu, err := s.d.Auth.Validate(token)
			if err != nil {
				return
			}
			c.priv.Store(auth.RoleRank(cu.Role) >= auth.RoleRank(store.RoleOperator))
			c.admin.Store(cu.Role == store.RoleAdmin)
			if !write([]byte(": ping\n\n")) {
				return
			}
		}
	}
}
