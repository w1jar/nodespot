package node

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
)

func ctlRig(t *testing.T) (*rig, *Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "nd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := quietConfig()
	cfg.Modes = map[string]mode.Settings{}
	path := filepath.Join(dir, "node.json")
	g := newRig(t, cfg)
	g.e.o.ConfigPath = path
	return g, NewServer(g.e, ServerOptions{}), path
}

func do(t *testing.T, s *Server, op string, a any) Response {
	t.Helper()
	var raw json.RawMessage
	if a != nil {
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	r := s.Do(context.Background(), Request{ID: 7, Op: op, Args: raw})
	if r.ID != 7 {
		t.Fatalf("id %d", r.ID)
	}
	return r
}

func TestControlOps(t *testing.T) {
	g, s, path := ctlRig(t)

	r := do(t, s, OpSaveMode, map[string]any{
		"mode":     mode.AllStar,
		"settings": mode.Settings{Values: map[string]string{"node": "1999", "password": "hunter2"}},
	})
	if !r.OK {
		t.Fatal(r.Error)
	}
	b, _ := json.Marshal(do(t, s, OpModes, nil))
	if strings.Contains(string(b), "hunter2") {
		t.Fatal("modes returned a password")
	}
	var mods []ModeInfo
	if err := json.Unmarshal(mustJSON(t, do(t, s, OpModes, nil).Data), &mods); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mods {
		if m.Name == mode.AllStar {
			found = true
			if !m.Configured || !m.SecretsSet["password"] || m.Settings.Values["node"] != "1999" {
				t.Fatalf("allstar = %+v", m)
			}
		}
	}
	if !found || len(mods) != 10 {
		t.Fatalf("%d modes listed", len(mods))
	}
	// A blank password keeps the saved one.
	if r := do(t, s, OpSaveMode, map[string]any{
		"mode": mode.AllStar, "settings": mode.Settings{Values: map[string]string{"node": "2001", "password": ""}},
	}); !r.OK {
		t.Fatal(r.Error)
	}
	if v := g.e.Config().Modes[mode.AllStar].Values["password"]; v != "hunter2" {
		t.Fatalf("password = %q", v)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v %v", fi, err)
	}

	// Bad settings are refused with a clear error and nothing is saved.
	if r := do(t, s, OpSaveMode, map[string]any{
		"mode": mode.AllStar, "settings": mode.Settings{Values: map[string]string{"node": "12"}},
	}); r.OK || !strings.Contains(r.Error, "node") {
		t.Fatalf("bad node accepted: %+v", r)
	}

	if r := do(t, s, OpSelect, map[string]any{"mode": mode.AllStar, "target": mode.Target{ID: "2000"}}); !r.OK {
		t.Fatal(r.Error)
	}
	var st Status
	if err := json.Unmarshal(mustJSON(t, do(t, s, OpStatus, nil).Data), &st); err != nil {
		t.Fatal(err)
	}
	if st.Active != mode.AllStar || st.Mode.Target == nil || st.Mode.Target.ID != "2000" || !st.Configured {
		t.Fatalf("status = %+v", st)
	}
	g.fake(mode.AllStar).Key("N1CALL")
	if r := do(t, s, OpNowCalling, nil); !r.OK || r.Data == nil {
		t.Fatalf("now calling = %+v", r)
	}
	waitFor(t, func() bool {
		r := do(t, s, OpLastHeard, map[string]int{"limit": 5})
		h, _ := r.Data.([]mode.Heard)
		return len(h) == 1
	})
	if r := do(t, s, OpDisconnect, nil); !r.OK {
		t.Fatal(r.Error)
	}

	lv := 0.25
	if r := do(t, s, OpRadioTune, map[string]any{"rxVolume": lv}); !r.OK {
		t.Fatal(r.Error)
	}
	if v := g.r.Levels().CaptureVolume; v != lv {
		t.Fatalf("capture volume %v", v)
	}
	if r := do(t, s, OpRadioTune, map[string]any{"txVolume": 2}); r.OK {
		t.Fatal("out of range level accepted")
	}

	if r := do(t, s, OpSaveStation, map[string]any{"station": Station{Callsign: "HELLO"}}); r.OK {
		t.Fatal("bad callsign accepted")
	}
	if r := do(t, s, OpSaveStation, map[string]any{"station": Station{Callsign: "n1call"}}); !r.OK {
		t.Fatal(r.Error)
	}
	if c := g.e.Config().Station; c.Callsign != "N1CALL" || c.Active == nil || c.Active.Mode != mode.AllStar {
		t.Fatalf("station = %+v", c)
	}
	if r := do(t, s, OpRadioProg, nil); r.OK {
		t.Fatal("programmed with no channel set")
	}

	if r := do(t, s, "launch", nil); r.OK || !strings.Contains(r.Error, "unknown op") {
		t.Fatalf("unknown op: %+v", r)
	}
	if r := do(t, s, OpSelect, map[string]any{"mode": mode.BM, "colour": 1}); r.OK {
		t.Fatal("unknown argument accepted")
	}

	// Reload picks up an edit made to the file.
	c := g.e.Config()
	c.Station.Callsign = "N0CALL"
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	if r := do(t, s, OpReload, nil); !r.OK {
		t.Fatal(r.Error)
	}
	if g.e.Config().Station.Callsign != "N0CALL" {
		t.Fatal("reload did not apply")
	}

	if r := do(t, s, OpDeleteMode, map[string]string{"mode": mode.AllStar}); !r.OK {
		t.Fatal(r.Error)
	}
	if g.e.Status().Active != "" {
		t.Fatal("deleted mode still active")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestControlSocket(t *testing.T) {
	_, s, path := ctlRig(t)
	sock := filepath.Join(filepath.Dir(path), "c.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	send := func(line string) map[string]any {
		t.Helper()
		if _, err := c.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		l, err := br.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := send(`{"id":1,"op":"status"}`); m["ok"] != true || m["id"].(float64) != 1 {
		t.Fatalf("status: %v", m)
	}
	if m := send(`not json`); m["ok"] == true {
		t.Fatal("bad line accepted")
	}

	// Subscribe on a second connection and see a selection.
	c2, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	br2 := bufio.NewReader(c2)
	if _, err := c2.Write([]byte(`{"id":2,"op":"subscribe"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br2.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	if m := send(`{"id":3,"op":"select","args":{"mode":"echotest"}}`); m["ok"] != true {
		t.Fatalf("select: %v", m)
	}
	for {
		l, err := br2.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var ev struct{ Event Event }
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Event.Kind == EventSelect && ev.Event.Text == mode.EchoTest {
			break
		}
	}
}
