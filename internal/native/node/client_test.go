package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// clientRig serves a rig's engine on a temp socket and returns a client.
func clientRig(t *testing.T) (*rig, *Client) {
	t.Helper()
	g, s, path := ctlRig(t)
	sock := filepath.Join(filepath.Dir(path), "c.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	c := NewClient(sock)
	c.Timeout = 5 * time.Second
	return g, c
}

func TestClientOps(t *testing.T) {
	_, c := clientRig(t)
	ctx := context.Background()
	f := 0.5
	tests := []struct {
		name string
		call func() error
		ok   func() error
	}{
		{"status", func() error { _, err := c.Status(ctx); return err }, nil},
		{"modes", func() error {
			l, err := c.Modes(ctx)
			if err == nil && len(l) == 0 {
				return errors.New("no modes")
			}
			return err
		}, nil},
		{"station", func() error {
			st, err := c.Station(ctx)
			if err == nil && st.Callsign != "N0CALL" {
				return errors.New("callsign " + st.Callsign)
			}
			return err
		}, nil},
		{"save station", func() error {
			st, _ := c.Station(ctx)
			st.Callsign = "N1CALL"
			out, err := c.SaveStation(ctx, st)
			if err == nil && out.Callsign != "N1CALL" {
				return errors.New("not saved")
			}
			return err
		}, nil},
		{"save mode", func() error {
			_, err := c.SaveMode(ctx, mode.EchoTest, mode.Settings{})
			return err
		}, nil},
		{"select", func() error {
			st, err := c.Select(ctx, mode.EchoTest, nil)
			if err == nil && st.Active != mode.EchoTest {
				return errors.New("active " + st.Active)
			}
			return err
		}, nil},
		{"connect", func() error { _, err := c.Connect(ctx, mode.Target{ID: "1999"}); return err }, nil},
		{"disconnect", func() error { _, err := c.Disconnect(ctx); return err }, nil},
		{"last heard", func() error { _, err := c.LastHeard(ctx, 5); return err }, nil},
		{"now calling", func() error {
			n, err := c.NowCalling(ctx)
			if err == nil && n != nil {
				return errors.New("someone is calling")
			}
			return err
		}, nil},
		{"levels", func() error { _, err := c.RadioLevels(ctx); return err }, nil},
		{"tune", func() error { _, err := c.RadioTune(ctx, &f, nil, nil); return err }, nil},
		{"idle", func() error { _, err := c.Select(ctx, "", nil); return err }, nil},
	}
	for _, tt := range tests {
		if err := tt.call(); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

func TestClientErrors(t *testing.T) {
	_, c := clientRig(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		call   func() error
		remote bool
	}{
		{"bad args", func() error { return c.Call(ctx, OpSelect, map[string]int{"bogus": 1}, nil) }, true},
		{"unknown op", func() error { return c.Call(ctx, "nosuch", nil, nil) }, true},
		{"no socket", func() error {
			_, err := NewClient(filepath.Join(t.TempDir(), "none.sock")).Status(ctx)
			return err
		}, false},
	}
	for _, tt := range tests {
		err := tt.call()
		var re *RemoteError
		switch {
		case err == nil:
			t.Errorf("%s: no error", tt.name)
		case tt.remote && !errors.As(err, &re):
			t.Errorf("%s: want a remote error, got %v", tt.name, err)
		case !tt.remote && !errors.Is(err, ErrUnavailable):
			t.Errorf("%s: want unavailable, got %v", tt.name, err)
		}
	}
}

func TestClientSubscribe(t *testing.T) {
	_, c := clientRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, ch, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select(context.Background(), mode.EchoTest, nil); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(10 * time.Second)
	for found := false; !found; {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream ended")
			}
			found = ev.Kind == EventSelect && ev.Text == mode.EchoTest
		case <-timeout:
			t.Fatal("no select event")
		}
	}
	cancel()
	for range ch {
	}
}
