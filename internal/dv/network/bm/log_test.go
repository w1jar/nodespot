package bm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
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
