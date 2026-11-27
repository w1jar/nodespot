package bm

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
)

func TestRunLogsFailedSessionsOnce(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	old := logf
	logf = func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	defer func() { logf = old }()

	// a name that cannot resolve: every attempt fails with the same reason
	n, err := New(ctl.NetConfig{Mode: ctl.ModeBM, Enabled: true, Address: "nohost.invalid", Password: "example-pass",
		Options: map[string]string{"backoff_min_ms": "100", "backoff_max_ms": "1000"}}, ctl.Identity{Callsign: "N0CALL", DMRID: 1234567})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the first failure instead of trusting a fixed run time: a slow
	// resolver on a loaded runner can take longer than the whole window.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan struct{})
	go func() { _ = n.Run(ctx, nil); close(ran) }()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(lines) }
	for deadline := time.Now().Add(10 * time.Second); count() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no failure logged")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Several more attempts (backoff 100, 200, 400 ms) fail the same way.
	time.Sleep(800 * time.Millisecond)
	cancel()
	<-ran
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "bm: ") || !strings.Contains(lines[0], "nohost.invalid") {
		t.Fatalf("log lines: %q", lines)
	}
}

// syncBuf is a goroutine-safe log buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *syncBuf) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

// The debug lines of a parrot call: login, the private subscription, tx
// start/end and rx start/end of the playback; an unexpected server packet is
// logged once a minute per type.
func TestDebugLogs(t *testing.T) {
	buf := &syncBuf{}
	m := newMaster(t)
	m.mu.Lock()
	m.parrot = true
	m.mu.Unlock()
	n, err := New(ctl.NetConfig{Mode: ctl.ModeBM, Enabled: true, Address: "127.0.0.1", Port: m.port(),
		Password: testPass, Options: fastOpts}, ctl.Identity{Callsign: "N0CALL", DMRID: testID})
	if err != nil {
		t.Fatal(err)
	}
	n.SetLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	s := &testSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx, s); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "login", func() bool { return s.loginIs(ctl.LoginOK) })
	n.Tune(ctx, ctl.Target{TG: ParrotTG})
	n.StartTX(network.CallHeader{Call: 7, SrcID: testID})
	for i, f := range frames(6, 1) {
		n.WriteVoice(network.VoiceFrame{Call: 7, Seq: uint32(i), Data: f})
	}
	n.EndTX(7)
	waitFor(t, "echo end", func() bool { return s.nEnds() == 1 })
	for i := 0; i < 3; i++ {
		m.reply(m.lastAddr(), typFailureCode, 0, []byte("example failure"))
	}
	waitFor(t, "failure line", func() bool { return strings.Contains(buf.String(), "example failure") })
	want := []string{
		`msg="bm: logged in"`,
		`msg="bm: subscription sent" session=private type=5 number=1234567 target=9990`,
		`msg="bm: tx start" session=private dst=9990 src=1234567`,
		`msg="bm: tx end" session=private dst=9990 src=1234567 packets=2`,
		`msg="bm: rx start" session=private src=9990 dst=1234567 from="voice header"`,
		`msg="bm: rx end" session=private src=9990 dst=1234567 packets=2 frames=6 reason=terminator`,
		`msg="bm: unexpected packet from the master" type=0x0929`,
	}
	out := buf.String()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in\n%s", w, out)
		}
	}
	if c := strings.Count(out, "type=0x0929"); c != 1 {
		t.Errorf("failure code logged %d times", c)
	}
	if strings.Contains(out, testPass) {
		t.Error("password logged")
	}
}
