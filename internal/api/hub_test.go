package api

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestHubDropsSlowClients(t *testing.T) {
	var last atomic.Int64
	h := newHub(func(n int) { last.Store(int64(n)) })
	slow := h.add()
	fast := h.add()
	if h.count() != 2 || last.Load() != 2 {
		t.Fatal("count")
	}
	msg := sseMsg{all: []byte("p")}
	start := time.Now()
	// The fast client keeps up; the slow one never reads.
	for i := 0; i < clientBuffer; i++ {
		h.broadcast(msg)
		<-fast.ch
	}
	if h.count() != 2 {
		t.Fatal("client dropped before its buffer was full")
	}
	h.broadcast(msg) // slow client's buffer is now full: it is dropped, not waited for
	<-fast.ch
	if time.Since(start) > time.Second {
		t.Fatal("broadcast blocked")
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow client not dropped")
	}
	if h.count() != 1 || last.Load() != 1 || h.dropped.Load() != 1 {
		t.Fatalf("count=%d last=%d dropped=%d", h.count(), last.Load(), h.dropped.Load())
	}
	select {
	case <-fast.done:
		t.Fatal("fast client dropped")
	default:
	}
	h.remove(fast)
	if h.count() != 0 || last.Load() != 0 {
		t.Fatal("remove")
	}
	h.remove(fast) // idempotent
}
