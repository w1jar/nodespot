package loopback

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
)

type sink struct {
	mu     sync.Mutex
	hdr    []network.CallHeader
	frames []network.VoiceFrame
	ends   []uint64
	done   chan struct{}
}

func (s *sink) Header(h network.CallHeader) { s.mu.Lock(); s.hdr = append(s.hdr, h); s.mu.Unlock() }
func (s *sink) Voice(f network.VoiceFrame) {
	s.mu.Lock()
	s.frames = append(s.frames, f)
	s.mu.Unlock()
}
func (s *sink) End(c uint64) {
	s.mu.Lock()
	s.ends = append(s.ends, c)
	s.mu.Unlock()
	select {
	case s.done <- struct{}{}:
	default:
	}
}
func (s *sink) Login(ctl.LoginState, string) {}

func start(t *testing.T) (*Loopback, *sink, context.CancelFunc) {
	t.Helper()
	l := New(ctl.NetConfig{Mode: ctl.ModeLoop, Enabled: true, Options: map[string]string{OptPlaybackDelayMS: "10"}})
	s := &sink{done: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan struct{})
	go func() { l.Run(ctx, s); close(fin) }()
	for l.Status().Login != ctl.LoginOK {
		time.Sleep(time.Millisecond)
	}
	return l, s, func() { cancel(); <-fin }
}

func TestEchoTest(t *testing.T) {
	l, s, stop := start(t)
	defer stop()
	l.StartTX(network.CallHeader{Call: 7})
	for i := 0; i < 10; i++ {
		l.WriteVoice(network.VoiceFrame{Call: 7, Data: []byte{byte(i), 1, 2, 3, 4, 5, 6}})
	}
	l.EndTX(7)
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("no playback")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hdr) != 1 || s.hdr[0].Src != "ECHO TEST" || s.hdr[0].TG != 0 || s.hdr[0].Private {
		t.Fatalf("%+v", s.hdr)
	}
	if len(s.frames) != 10 {
		t.Fatalf("%d frames", len(s.frames))
	}
	for i, f := range s.frames {
		if f.Data[0] != byte(i) || f.Call != s.hdr[0].Call {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if s.ends[0] != s.hdr[0].Call {
		t.Fatal("end call mismatch")
	}
}

func TestRecordingCap(t *testing.T) {
	l := New(ctl.NetConfig{})
	l.StartTX(network.CallHeader{Call: 1})
	for i := 0; i < MaxFrames+50; i++ {
		l.WriteVoice(network.VoiceFrame{Call: 1, Data: make([]byte, 7)})
	}
	if len(l.rec) != MaxFrames {
		t.Fatalf("%d", len(l.rec))
	}
}

func TestStartTXCutsPlayback(t *testing.T) {
	l, s, stop := start(t)
	defer stop()
	l.StartTX(network.CallHeader{Call: 1})
	for i := 0; i < 100; i++ {
		l.WriteVoice(network.VoiceFrame{Call: 1, Data: make([]byte, 7)})
	}
	l.EndTX(1)
	time.Sleep(100 * time.Millisecond)
	l.StartTX(network.CallHeader{Call: 2}) // cut
	s.mu.Lock()
	n, ends := len(s.frames), len(s.ends)
	s.mu.Unlock()
	if n >= 100 || ends != 1 {
		t.Fatalf("frames=%d ends=%d", n, ends)
	}
}

func TestRegistered(t *testing.T) {
	n, err := network.New(ctl.NetConfig{Mode: ctl.ModeLoop}, ctl.Identity{})
	if err != nil || n.Mode() != ctl.ModeLoop {
		t.Fatal(err)
	}
	_, err = network.New(ctl.NetConfig{Mode: ctl.ModeBM}, ctl.Identity{})
	if e, ok := err.(*network.Error); !ok || e.Code != ctl.CodeNotAvailable {
		t.Fatalf("%v", err)
	}
}
