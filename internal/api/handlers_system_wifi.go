package api

import (
	"context"
	"net"
	"net/http"

	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

// WifiHost is the WiFi part of the helper (sys_wifi.go). It is a separate
// interface: the SystemHost the server was given may also implement it
// (helper.Client and helper.DemoSystem do); without it the endpoints answer
// 503 like every System tab endpoint without a helper.
type WifiHost interface {
	WifiStatus(ctx context.Context) (helper.WifiStatus, error)
	WifiScan(ctx context.Context) (helper.WifiScanResult, error)
	WifiList(ctx context.Context) (helper.WifiList, error)
	WifiConnect(ctx context.Context, ssid, password string, hidden bool) (helper.WifiConnectResult, error)
	WifiForget(ctx context.Context, id string) (helper.OKResult, error)
	WifiPriority(ctx context.Context, order []string) (helper.WifiList, error)
	WifiHotspotSet(ctx context.Context, enabled bool, delaySeconds int) (helper.WifiStatus, error)
}

func (s *Server) systemWifiRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/system/wifi", s.authed(store.RoleAdmin, s.handleWifiGet))
	m.HandleFunc("POST /api/system/wifi/scan", s.authed(store.RoleAdmin, s.handleWifiScan))
	m.HandleFunc("POST /api/system/wifi/networks", s.authed(store.RoleAdmin, s.handleWifiAdd))
	m.HandleFunc("DELETE /api/system/wifi/networks/{id}", s.authed(store.RoleAdmin, s.handleWifiForget))
	m.HandleFunc("PUT /api/system/wifi/order", s.authed(store.RoleAdmin, s.handleWifiOrder))
	m.HandleFunc("PUT /api/system/wifi/hotspot", s.authed(store.RoleAdmin, s.handleWifiHotspot))
}

// wifiHost returns the WiFi helper or answers 503.
func (s *Server) wifiHost(w http.ResponseWriter) WifiHost {
	if s.sysHost(w) == nil {
		return nil
	}
	h, ok := s.d.SysHost.(WifiHost)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: the helper has no WiFi support")
		return nil
	}
	return h
}

// wifiReady returns the status and answers 503 "not available" when the node
// has no NetworkManager or no WiFi device (changes would only fail).
func (s *Server) wifiReady(w http.ResponseWriter, r *http.Request, h WifiHost) (helper.WifiStatus, bool) {
	st, err := h.WifiStatus(r.Context())
	if err != nil {
		s.systemError(w, err)
		return st, false
	}
	if !st.Available || !st.HasWifi {
		reason := st.Reason
		if reason == "" {
			reason = "no WiFi device found"
		}
		writeErr(w, http.StatusServiceUnavailable, "system_unavailable", "not available: "+reason)
		return st, false
	}
	return st, true
}

// localIP is the address the request arrived on ("" when unknown).
func localIP(r *http.Request) string {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return host
}

// viaHotspot: the request came in over the setup hotspot (the server's
// address on it is the fixed hotspot gateway address).
func viaHotspot(r *http.Request) bool { return localIP(r) == helper.HotspotAddr }

// viaWifi: the request came in over the node's own WiFi connection, so a
// change of WiFi may cut the connection the admin is using.
func viaWifi(r *http.Request, st helper.WifiStatus) bool {
	ip := localIP(r)
	if ip == "" {
		return false
	}
	if ip == helper.HotspotAddr {
		return true
	}
	for _, d := range st.Devices {
		if d.Type != "wifi" {
			continue
		}
		for _, a := range d.Addrs {
			if a == ip {
				return true
			}
		}
	}
	return false
}

type wifiView struct {
	helper.WifiStatus
	Networks   []helper.WifiNetwork `json:"networks"`
	ViaWifi    bool                 `json:"viaWifi"`
	ViaHotspot bool                 `json:"viaHotspot"`
}

func (s *Server) wifiView(r *http.Request, h WifiHost, st helper.WifiStatus) (wifiView, error) {
	v := wifiView{WifiStatus: st, Networks: []helper.WifiNetwork{}, ViaWifi: viaWifi(r, st), ViaHotspot: viaHotspot(r)}
	if st.Available && st.HasWifi {
		l, err := h.WifiList(r.Context())
		if err != nil {
			return v, err
		}
		v.Networks = l.Networks
	}
	return v, nil
}

func (s *Server) handleWifiGet(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.wifiHost(w)
	if h == nil {
		return
	}
	st, err := h.WifiStatus(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	v, err := s.wifiView(r, h, st)
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleWifiScan(w http.ResponseWriter, r *http.Request, _ store.User) {
	h := s.wifiHost(w)
	if h == nil {
		return
	}
	if _, ok := s.wifiReady(w, r, h); !ok {
		return
	}
	res, err := h.WifiScan(r.Context())
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

type wifiAdd struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	Hidden   bool   `json:"hidden"`
}

func (s *Server) handleWifiAdd(w http.ResponseWriter, r *http.Request, u store.User) {
	var in wifiAdd
	if !decode(w, r, &in, false) {
		return
	}
	// Validation errors never repeat the password.
	if err := helper.ValidSSID(in.SSID); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := helper.ValidWifiPassword(in.Password); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	h := s.wifiHost(w)
	if h == nil {
		return
	}
	st, ok := s.wifiReady(w, r, h)
	if !ok {
		return
	}
	res, err := h.WifiConnect(r.Context(), in.SSID, in.Password, in.Hidden)
	if err != nil {
		s.systemError(w, err)
		return
	}
	// The audit row names the network and whether it is protected, never the key.
	s.audit(u, "system.wifi.add", map[string]any{"ssid": in.SSID, "secured": in.Password != "", "hidden": in.Hidden, "updated": res.Updated})
	v, err := s.wifiView(r, h, mustStatus(r, h, st))
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"result": res, "wifi": v})
}

// mustStatus re-reads the status after a change; on failure the old one is
// used (the change itself succeeded).
func mustStatus(r *http.Request, h WifiHost, old helper.WifiStatus) helper.WifiStatus {
	if st, err := h.WifiStatus(r.Context()); err == nil {
		return st
	}
	return old
}

func (s *Server) handleWifiForget(w http.ResponseWriter, r *http.Request, u store.User) {
	id := r.PathValue("id")
	if !helper.ValidConnID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid network id")
		return
	}
	h := s.wifiHost(w)
	if h == nil {
		return
	}
	st, ok := s.wifiReady(w, r, h)
	if !ok {
		return
	}
	// Name the network in the audit row: look it up before it is gone.
	name := ""
	if l, err := h.WifiList(r.Context()); err == nil {
		for _, n := range l.Networks {
			if n.ID == id {
				name = n.SSID
			}
		}
	}
	if _, err := h.WifiForget(r.Context(), id); err != nil {
		s.systemError(w, err)
		return
	}
	s.audit(u, "system.wifi.forget", map[string]any{"ssid": name})
	v, err := s.wifiView(r, h, mustStatus(r, h, st))
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleWifiOrder(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct {
		Order []string `json:"order"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if len(in.Order) == 0 || len(in.Order) > 50 {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid order")
		return
	}
	seen := map[string]bool{}
	for _, id := range in.Order {
		if !helper.ValidConnID(id) || seen[id] {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid order")
			return
		}
		seen[id] = true
	}
	h := s.wifiHost(w)
	if h == nil {
		return
	}
	st, ok := s.wifiReady(w, r, h)
	if !ok {
		return
	}
	if _, err := h.WifiPriority(r.Context(), in.Order); err != nil {
		s.systemError(w, err)
		return
	}
	s.audit(u, "system.wifi.order", map[string]any{"count": len(in.Order)})
	v, err := s.wifiView(r, h, mustStatus(r, h, st))
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleWifiHotspot(w http.ResponseWriter, r *http.Request, u store.User) {
	var in struct {
		Enabled      *bool `json:"enabled"`
		DelaySeconds int   `json:"delaySeconds"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "enabled is required")
		return
	}
	if in.DelaySeconds != 0 && (in.DelaySeconds < 30 || in.DelaySeconds > 3600) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid delay: 30 to 3600 seconds")
		return
	}
	h := s.wifiHost(w)
	if h == nil {
		return
	}
	st, ok := s.wifiReady(w, r, h)
	if !ok {
		return
	}
	// Turning the hotspot off while connected through it ends the session.
	if _, err := h.WifiHotspotSet(r.Context(), *in.Enabled, in.DelaySeconds); err != nil {
		s.systemError(w, err)
		return
	}
	s.audit(u, "system.wifi.hotspot", map[string]any{"enabled": *in.Enabled, "delaySeconds": in.DelaySeconds})
	v, err := s.wifiView(r, h, mustStatus(r, h, st))
	if err != nil {
		s.systemError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
