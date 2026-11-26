package vocoder_test

import (
	"context"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/dv/vocoder/fake"
)

var _ vocoder.AMBE = (*vocoder.MD380)(nil)

func newPair(t *testing.T, timeout time.Duration) (*vocoder.MD380, *fake.Server) {
	t.Helper()
	srv, err := fake.New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	m := vocoder.NewMD380(srv.Addr(), timeout)
	t.Cleanup(func() { m.Close() })
	return m, srv
}

func frame(v int16) *[vocoder.PCMSamples]int16 {
	var p [vocoder.PCMSamples]int16
	for i := range p {
		p[i] = v
	}
	return &p
}

func TestEncodeDecode(t *testing.T) {
	m, _ := newPair(t, time.Second)
	ctx := context.Background()
	a, err := m.Encode(ctx, frame(-1234))
	if err != nil {
		t.Fatal(err)
	}
	if a[0] != 0xA5 || int16(uint16(a[1])<<8|uint16(a[2])) != -1234 || a[6] != 0 {
		t.Fatalf("% x", a)
	}
	pcm, err := m.Decode(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pcm[0] != -1234 || pcm[159] != -1234 {
		t.Fatalf("pcm %d", pcm[0])
	}
	if s := m.Stats(); s.Frames != 2 || s.Timeouts != 0 || s.Down {
		t.Fatalf("%+v", s)
	}
}

// A reply later than the timeout must not be handed to the next request.
func TestLateReplyNotPaired(t *testing.T) {
	m, srv := newPair(t, 40*time.Millisecond)
	ctx := context.Background()
	srv.SetDelay(150 * time.Millisecond)
	a, err := m.Encode(ctx, frame(111))
	if err != nil {
		t.Fatal(err)
	}
	if a != vocoder.SilenceAMBE {
		t.Fatalf("want silence, got % x", a)
	}
	srv.SetDelay(0)
	// The late reply for 111 arrives during this call's lifetime.
	time.Sleep(200 * time.Millisecond)
	b, err := m.Encode(ctx, frame(222))
	if err != nil {
		t.Fatal(err)
	}
	if int16(uint16(b[1])<<8|uint16(b[2])) != 222 {
		t.Fatalf("paired a stale reply: % x", b)
	}
	if s := m.Stats(); s.Timeouts != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestDropAndDecodeSilence(t *testing.T) {
	m, srv := newPair(t, 40*time.Millisecond)
	ctx := context.Background()
	srv.SetDrop(func(n uint64) bool { return n == 1 })
	pcm, err := m.Decode(ctx, [7]byte{0, 0x12, 0x34})
	if err != nil || pcm != [160]int16{} {
		t.Fatalf("%v %v", pcm[0], err)
	}
	pcm, _ = m.Decode(ctx, [7]byte{0, 0x12, 0x34})
	if pcm[0] != 0x1234 {
		t.Fatalf("%x", pcm[0])
	}
	if s := m.Stats(); s.Timeouts != 1 || s.Frames != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestDownAndRecover(t *testing.T) {
	m, srv := newPair(t, 5*time.Millisecond)
	var changes []bool
	m.OnChange = func(d bool) { changes = append(changes, d) }
	ctx := context.Background()
	srv.SetDrop(func(uint64) bool { return true })
	for i := 0; i < vocoder.DownAfter; i++ {
		m.Encode(ctx, frame(1))
	}
	if !m.Stats().Down || len(changes) != 1 || !changes[0] {
		t.Fatalf("%+v %v", m.Stats(), changes)
	}
	// While down and inside the backoff, requests return fast.
	start := time.Now()
	if _, err := m.Encode(ctx, frame(1)); err != vocoder.ErrDown {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 3*time.Millisecond {
		t.Fatal("slow while down")
	}
	srv.SetDrop(nil)
	time.Sleep(1100 * time.Millisecond)
	if _, err := m.Encode(ctx, frame(1)); err != nil {
		t.Fatal(err)
	}
	if m.Stats().Down || len(changes) != 2 || changes[1] {
		t.Fatalf("%+v %v", m.Stats(), changes)
	}
}

func TestCancelled(t *testing.T) {
	m, _ := newPair(t, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Encode(ctx, frame(1)); err == nil {
		t.Fatal("want error")
	}
}
