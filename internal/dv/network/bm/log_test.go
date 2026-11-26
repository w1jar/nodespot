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
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_ = n.Run(ctx, nil)
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "bm: ") || !strings.Contains(lines[0], "nohost.invalid") {
		t.Fatalf("log lines: %q", lines)
	}
}
