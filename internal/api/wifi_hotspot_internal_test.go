package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/w1jar/nodespot/internal/helper"
)

func reqOn(local string) *http.Request {
	r := httptest.NewRequest("GET", "/api/setup", nil)
	a, _ := net.ResolveTCPAddr("tcp", local)
	return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(a)))
}

func TestViaHotspot(t *testing.T) {
	if !viaHotspot(reqOn("10.42.0.1:8080")) {
		t.Error("hotspot address not recognised")
	}
	for _, a := range []string{"192.0.2.10:8080", "127.0.0.1:8080", "10.42.0.2:8080"} {
		if viaHotspot(reqOn(a)) {
			t.Errorf("%s taken for the hotspot", a)
		}
	}
	if viaHotspot(httptest.NewRequest("GET", "/", nil)) {
		t.Error("no local address must not be the hotspot")
	}
	if !viaWifi(reqOn("10.42.0.1:8080"), helper.WifiStatus{}) {
		t.Error("hotspot is WiFi")
	}
	st := helper.WifiStatus{Devices: []helper.WifiDevice{{Type: "ethernet", Addrs: []string{"192.0.2.10"}}, {Type: "wifi", Addrs: []string{"192.0.2.20"}}}}
	if viaWifi(reqOn("192.0.2.10:8080"), st) || !viaWifi(reqOn("192.0.2.20:8080"), st) {
		t.Error("viaWifi by device type")
	}
}
