package ctl_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/ctl/ctltest"
)

type stub struct {
	mu   sync.Mutex
	mode string
	last ctl.TuneArgs
}

func (s *stub) st() ctl.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ctl.Status{Version: "t", Mode: s.mode}
}
func (s *stub) Status(context.Context) (ctl.Status, error) { return s.st(), nil }
func (s *stub) Mode(_ context.Context, a ctl.ModeArgs) (ctl.Status, error) {
	if a.Mode == "BM" {
		return ctl.Status{}, &ctl.Error{Code: ctl.CodeNotAvailable, Msg: "no"}
	}
	s.mu.Lock()
	s.mode = a.Mode
	s.mu.Unlock()
	return s.st(), nil
}
func (s *stub) Tune(_ context.Context, a ctl.TuneArgs) (ctl.Status, error) {
	s.mu.Lock()
	s.last, s.mode = a, a.Mode
	s.mu.Unlock()
	return s.st(), nil
}
func (s *stub) Unlink(context.Context, ctl.UnlinkArgs) (ctl.Status, error) { return s.st(), nil }
func (s *stub) Reload(context.Context) (ctl.Status, error) {
	return ctl.Status{}, errors.New("boom")
}

func startServer(t *testing.T, opts ctl.ServerOptions) (*ctl.Server, *stub, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dvs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	ln, err := ctl.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o660 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if opts.AllowUIDs == nil {
		opts.AllowUIDs = []int{os.Getuid()}
	}
	h := &stub{}
	s := ctl.NewServer(h, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, h, path
}

func TestClientOps(t *testing.T) {
	_, h, path := startServer(t, ctl.ServerOptions{})
	c := ctl.NewClient(path)
	ctx := context.Background()
	if st, err := c.Status(ctx); err != nil || st.Version != "t" {
		t.Fatal(st, err)
	}
	if st, err := c.Mode(ctx, ctl.ModeArgs{Mode: ctl.ModeLoop}); err != nil || st.Mode != "LOOP" {
		t.Fatal(st, err)
	}
	_, err := c.Mode(ctx, ctl.ModeArgs{Mode: "BM"})
	var ce *ctl.Error
	if !errors.As(err, &ce) || ce.Code != ctl.CodeNotAvailable {
		t.Fatalf("%v", err)
	}
	if _, err := c.Tune(ctx, ctl.TuneArgs{Mode: "DMR", Target: ctl.Target{TG: 1997}}); err != nil || h.last.Target.TG != 1997 {
		t.Fatal(err)
	}
	if _, err := c.Tune(ctx, ctl.TuneArgs{}); !errors.As(err, &ce) || ce.Code != ctl.CodeBadRequest {
		t.Fatalf("%v", err)
	}
	if _, err := c.Unlink(ctx, ctl.UnlinkArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reload(ctx); !errors.As(err, &ce) || ce.Code != ctl.CodeInternal {
		t.Fatalf("%v", err)
	}
}

func rawConn(t *testing.T, path string) (net.Conn, *bufio.Reader) {
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, bufio.NewReader(c)
}

func TestForbidden(t *testing.T) {
	_, _, path := startServer(t, ctl.ServerOptions{AllowUIDs: []int{os.Getuid() + 4242}})
	c, br := rawConn(t, path)
	c.Write([]byte(`{"id":1,"op":"status"}` + "\n"))
	line, _ := br.ReadString('\n')
	var r ctl.Response
	json.Unmarshal([]byte(line), &r)
	if r.OK || r.Code != ctl.CodeForbidden {
		t.Fatalf("%q", line)
	}
	if _, err := br.ReadString('\n'); err == nil {
		t.Fatal("connection stayed open")
	}
}

func TestProtocolErrors(t *testing.T) {
	_, _, path := startServer(t, ctl.ServerOptions{})
	c, br := rawConn(t, path)
	for _, tc := range []struct{ in, code string }{
		{`not json`, ctl.CodeBadRequest},
		{`{"id":2,"op":"nope"}`, ctl.CodeBadRequest},
		{`{"id":3,"op":"mode","args":{"mode":"LOOP","extra":1}}`, ctl.CodeBadRequest},
	} {
		c.Write([]byte(tc.in + "\n"))
		line, _ := br.ReadString('\n')
		var r ctl.Response
		json.Unmarshal([]byte(line), &r)
		if r.OK || r.Code != tc.code {
			t.Fatalf("%s -> %q", tc.in, line)
		}
	}
	// Multiple requests on one connection are answered in order.
	c.Write([]byte(`{"id":10,"op":"ping"}` + "\n" + `{"id":11,"op":"status"}` + "\n"))
	for _, id := range []uint64{10, 11} {
		line, _ := br.ReadString('\n')
		var r ctl.Response
		json.Unmarshal([]byte(line), &r)
		if !r.OK || r.ID != id {
			t.Fatalf("%q", line)
		}
	}
	// A line over MaxLine closes the connection.
	c.Write([]byte(strings.Repeat("x", ctl.MaxLine+10) + "\n"))
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "bad_request") {
		t.Fatalf("%q", line)
	}
}

func TestSubscribe(t *testing.T) {
	s, _, path := startServer(t, ctl.ServerOptions{Ping: 50 * time.Millisecond})
	c := ctl.NewClient(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan ctl.Status, 1)
	evs := make(chan ctl.Event, 16)
	errc := make(chan error, 1)
	go func() {
		errc <- c.Subscribe(ctx, func(st ctl.Status) { first <- st }, func(e ctl.Event) { evs <- e })
	}()
	select {
	case st := <-first:
		if st.Version != "t" {
			t.Fatal(st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no first status")
	}
	s.Publish(ctl.Event{Type: ctl.EvTalkStart, Talk: &ctl.Talk{ID: 5}})
	gotTalk, gotPing := false, false
	deadline := time.After(3 * time.Second)
	for !gotTalk || !gotPing {
		select {
		case e := <-evs:
			gotTalk = gotTalk || (e.Type == ctl.EvTalkStart && e.Talk.ID == 5)
			gotPing = gotPing || e.Type == ctl.EvPing
		case <-deadline:
			t.Fatalf("talk=%v ping=%v", gotTalk, gotPing)
		}
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe did not return")
	}
}

func TestSubscribeOverflow(t *testing.T) {
	s, _, path := startServer(t, ctl.ServerOptions{})
	c, br := rawConn(t, path)
	c.Write([]byte(`{"id":1,"op":"subscribe"}` + "\n"))
	if line, _ := br.ReadString('\n'); !strings.Contains(line, `"ok":true`) {
		t.Fatalf("%q", line)
	}
	// Further requests are rejected.
	c.Write([]byte(`{"id":2,"op":"status"}` + "\n"))
	if line, _ := br.ReadString('\n'); !strings.Contains(line, "bad_request") {
		t.Fatalf("%q", line)
	}
	// Do not read; flood far beyond the buffers.
	big := strings.Repeat("x", 1024)
	for i := 0; i < 20000; i++ {
		s.Publish(ctl.Event{Type: ctl.EvStatus, Status: &ctl.Status{Version: big}})
	}
	sawOverflow := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, `"overflow"`) {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Fatal("no overflow event")
	}
}

func TestFake(t *testing.T) {
	f := ctltest.New(t)
	f.SetStatus(ctl.Status{Version: "fake", Mode: "LOOP"})
	c := ctl.NewClient(f.Path())
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || st.Mode != "LOOP" {
		t.Fatal(st, err)
	}
	if _, err := c.Tune(ctx, ctl.TuneArgs{Mode: "LOOP"}); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 || reqs[0].Op != ctl.OpStatus || reqs[1].Op != ctl.OpTune {
		t.Fatalf("%+v", reqs)
	}
	sctx, cancel := context.WithCancel(ctx)
	got := make(chan ctl.Event, 4)
	ready := make(chan struct{})
	go c.Subscribe(sctx, func(ctl.Status) { close(ready) }, func(e ctl.Event) { got <- e })
	<-ready
	f.Emit(ctl.Event{Type: ctl.EvLogin})
	select {
	case e := <-got:
		if e.Type != ctl.EvLogin {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	cancel()
}

func TestClientNoServer(t *testing.T) {
	c := ctl.NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
