package api_test

import (
	"testing"

	"github.com/w1jar/nodespot/internal/api"
	"github.com/w1jar/nodespot/internal/native/mode"
)

func TestEchoLinkSearch(t *testing.T) {
	e := newEnv(t, opts{})
	if c := e.do("", "GET", "/api/echolink/search?q=n0", nil, nil); c != 401 {
		t.Fatalf("anonymous: %d", c)
	}
	var r mode.DirResult
	for _, role := range []string{"viewer", "op", "admin"} {
		if c := e.do(role, "GET", "/api/echolink/search?q=n0", nil, &r); c != 200 || r.Loaded || r.Results == nil || len(r.Results) != 0 {
			t.Fatalf("%s before login: %d %+v", role, c, r)
		}
	}
	expectErr(t, e, "viewer", "GET", "/api/echolink/search?q=x&limit=0", nil, 400, "bad_request")
	expectErr(t, e, "viewer", "GET", "/api/echolink/search?q=x&limit=ten", nil, 400, "bad_request")

	put := mode.Settings{Values: map[string]string{"callsign": "N0CALL-L", "password": "echo-pass"}}
	if c := e.do("admin", "PUT", "/api/modes/echolink", put, nil); c != 200 {
		t.Fatal(c)
	}
	if c := e.do("op", "POST", "/api/select", map[string]string{"mode": "echolink"}, nil); c != 200 {
		t.Fatalf("select: %d", c)
	}
	e.fake(mode.EchoLink).SetDirectory([]mode.DirEntry{
		{Node: 1997, Callsign: "N0CALL-L", Location: "Springfield, ST", Status: "online", Kind: "link"},
		{Node: 9999, Callsign: "*ECHOTEST*", Location: "Test server", Status: "online", Kind: "conference"},
		{Node: 2001, Callsign: "N1CALL-R", Location: "Shelbyville, XX", Status: "busy", Kind: "repeater"},
		{Node: 2002, Callsign: "*TEST*", Location: "Test conference", Status: "online", Kind: "conference"},
	})
	if c := e.do("viewer", "GET", "/api/echolink/search?q=n1", nil, &r); c != 200 || !r.Loaded || len(r.Results) != 1 || r.Results[0].Node != 2001 || r.Results[0].Kind != "repeater" || r.Results[0].Status != "busy" {
		t.Fatalf("search: %d %+v", c, r)
	}
	e.do("viewer", "GET", "/api/echolink/search?q=test&limit=1", nil, &r)
	if len(r.Results) != 1 || r.Results[0].Callsign != "*TEST*" {
		t.Fatalf("limit: %+v", r)
	}
	e.do("viewer", "GET", "/api/echolink/search?q=20", nil, &r)
	if len(r.Results) != 2 {
		t.Fatalf("node prefix: %+v", r)
	}
	e.stopNode()
	expectErr(t, e, "viewer", "GET", "/api/echolink/search?q=n0", nil, 503, "node_unavailable")
}

func TestHiddenModes(t *testing.T) {
	e := newEnv(t, opts{})
	var s api.SettingsJSON
	if c := e.do("viewer", "GET", "/api/settings", nil, &s); c != 200 || s.HiddenModes == nil || len(s.HiddenModes) != 0 {
		t.Fatalf("fresh install hides nothing: %d %+v", c, s)
	}
	body := map[string]any{"hiddenModes": []string{"NXDN", "m17", "nxdn"}}
	expectErr(t, e, "op", "PUT", "/api/settings/hidden-modes", body, 403, "forbidden")
	expectErr(t, e, "viewer", "PUT", "/api/settings/hidden-modes", body, 403, "forbidden")
	if c := e.do("admin", "PUT", "/api/settings/hidden-modes", body, &s); c != 200 || len(s.HiddenModes) != 2 || s.HiddenModes[0] != "m17" || s.HiddenModes[1] != "nxdn" {
		t.Fatalf("put: %d %+v", c, s)
	}
	s = api.SettingsJSON{}
	if e.do("viewer", "GET", "/api/settings", nil, &s); len(s.HiddenModes) != 2 {
		t.Fatalf("persisted: %+v", s)
	}
	expectErr(t, e, "admin", "PUT", "/api/settings/hidden-modes", map[string]any{}, 400, "bad_request")
	expectErr(t, e, "admin", "PUT", "/api/settings/hidden-modes", map[string]any{"hiddenModes": []string{"Bad Name!"}}, 400, "bad_request")
	many := make([]string, 33)
	for i := range many {
		many[i] = "m" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	expectErr(t, e, "admin", "PUT", "/api/settings/hidden-modes", map[string]any{"hiddenModes": many}, 400, "bad_request")
	// Hiding keeps the mode: its settings and status are unchanged.
	var mods []map[string]any
	if c := e.do("admin", "GET", "/api/modes", nil, &mods); c != 200 || len(mods) == 0 {
		t.Fatalf("modes: %d", c)
	}
	if c := e.do("admin", "PUT", "/api/settings/hidden-modes", map[string]any{"hiddenModes": []string{}}, &s); c != 200 || len(s.HiddenModes) != 0 {
		t.Fatalf("show all: %d %+v", c, s)
	}
}
