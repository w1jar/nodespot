package dv

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// chanNet is a network stand-in that reports what the mode does on
// channels, so tests wait on events instead of polling.
type chanNet struct {
	codec  network.Codec
	ran    chan network.Sink
	tunes  chan ctl.Target
	starts chan network.CallHeader
	voice  chan network.VoiceFrame
	ends   chan uint64
}

func newChanNet(c network.Codec) *chanNet {
	return &chanNet{codec: c, ran: make(chan network.Sink, 1), tunes: make(chan ctl.Target, 16),
		starts: make(chan network.CallHeader, 16), voice: make(chan network.VoiceFrame, 256), ends: make(chan uint64, 16)}
}

func (n *chanNet) Mode() string          { return "FAKE" }
func (n *chanNet) Codec() network.Codec  { return n.codec }
func (n *chanNet) Status() ctl.NetStatus { return ctl.NetStatus{} }
func (n *chanNet) Run(ctx context.Context, s network.Sink) error {
	n.ran <- s
	<-ctx.Done()
	return nil
}
func (n *chanNet) Tune(_ context.Context, t ctl.Target) error { n.tunes <- t; return nil }
func (n *chanNet) Unlink(context.Context) error               { return nil }
func (n *chanNet) StartTX(h network.CallHeader) error         { n.starts <- h; return nil }
func (n *chanNet) WriteVoice(f network.VoiceFrame) error      { n.voice <- f; return nil }
func (n *chanNet) EndTX(c uint64) error                       { n.ends <- c; return nil }

// noAMBE fails the test if the vocoder is used: frames must pass through.
type noAMBE struct{ t *testing.T }

func (a noAMBE) Encode(context.Context, *[vocoder.PCMSamples]int16) ([vocoder.AMBEBytes]byte, error) {
	a.t.Error("the AMBE encoder was used in frame mode")
	return [vocoder.AMBEBytes]byte{}, nil
}
func (a noAMBE) Decode(context.Context, [vocoder.AMBEBytes]byte) ([vocoder.PCMSamples]int16, error) {
	a.t.Error("the AMBE decoder was used in frame mode")
	return [vocoder.PCMSamples]int16{}, nil
}

func recvT[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// startFrames starts a mode linked to target and returns it with its
// network and sink.
func startFrames(t *testing.T, name string, target mode.Target, codec network.Codec) (*Mode, *chanNet, network.Sink) {
	t.Helper()
	n := newChanNet(codec)
	s := mode.Settings{Values: base[name], Reflectors: reflectors}
	m, err := New(name, mode.Env{Callsign: "N0CALL"}, s, Options{
		NewNetwork: func(ctl.NetConfig, ctl.Identity) (network.Network, error) { return n, nil },
		AMBE:       noAMBE{t}, TxTimeout: time.Hour, RxSilence: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.FrameCodec(); ok {
		t.Error("FrameCodec before Start")
	}
	if err := m.SendHeader(network.CallHeader{TG: 9}); !errors.Is(err, ErrNotStarted) {
		t.Errorf("SendHeader before Start: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	sink := recvT(t, n.ran, "network run")
	if err := m.Connect(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if m.d.login {
		if err := m.SendHeader(network.CallHeader{TG: 9}); !errors.Is(err, ErrNotLinked) {
			t.Errorf("SendHeader before login: %v", err)
		}
		sink.Login(ctl.LoginOK, "")
	}
	recvT(t, n.tunes, "tune")
	return m, n, sink
}

func ambeFrame(i int) []byte { return []byte{byte(i), 0x11, 0x22, 0x33, 0x44, 0x55, 0x80} }

func TestFramesReceive(t *testing.T) {
	m, _, sink := startFrames(t, mode.BM, mode.Target{ID: "91"}, network.CodecAMBE)
	m.SetFrames(true)
	if c, ok := m.FrameCodec(); !ok || c != network.CodecAMBE {
		t.Errorf("FrameCodec %v %v", c, ok)
	}
	sink.Header(network.CallHeader{Call: 50, Src: "N1CALL", SrcID: 1234567, TG: 91, Slot: 2})
	ev := recvT(t, m.RecvFrames(), "header")
	if ev.Kind != mode.FrameHeader || ev.Header.SrcID != 1234567 || ev.Header.Src != "N1CALL" ||
		ev.Header.TG != 91 || ev.Header.Call != ev.Call || ev.Call == 0 || ev.Call == 50 {
		t.Fatalf("header event %+v", ev)
	}
	call := ev.Call
	for i := range 6 {
		sink.Voice(network.VoiceFrame{Call: 50, Seq: uint32(i), Data: ambeFrame(i)})
	}
	sink.Voice(network.VoiceFrame{Call: 50, Data: make([]byte, 8)}) // bad length
	sink.Voice(network.VoiceFrame{Call: 51, Data: ambeFrame(9)})    // another call
	sink.End(50)
	for i := range 6 {
		ev := recvT(t, m.RecvFrames(), "voice")
		if ev.Kind != mode.FrameVoice || ev.Call != call || ev.Frame.Call != call ||
			ev.Frame.Seq != uint32(i) || !bytes.Equal(ev.Frame.Data, ambeFrame(i)) {
			t.Fatalf("voice %d: %+v", i, ev)
		}
	}
	if ev := recvT(t, m.RecvFrames(), "end"); ev.Kind != mode.FrameEnd || ev.Call != call {
		t.Fatalf("end %+v", ev)
	}
	if m.Talking() {
		t.Error("talking after the end")
	}
}

func TestFramesTransmit(t *testing.T) {
	m, n, _ := startFrames(t, mode.BM, mode.Target{ID: "91", Slot: 1}, network.CodecAMBE)
	m.SetFrames(true)
	if err := m.SendFrame(network.VoiceFrame{Data: ambeFrame(0)}); !errors.Is(err, ErrNoTX) {
		t.Errorf("SendFrame with no transmission: %v", err)
	}
	if err := m.SendHeader(network.CallHeader{Call: 77, Src: "N1CALL", SrcID: 1234567, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	h := recvT(t, n.starts, "StartTX")
	if h.SrcID != 1234567 || h.Src != "N1CALL" || h.TG != 91 || h.Slot != 1 || h.Private || h.Call == 77 {
		t.Errorf("StartTX %+v", h)
	}
	for i := range 4 {
		if err := m.SendFrame(network.VoiceFrame{Call: 1, Seq: 99, Data: ambeFrame(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.SendFrame(network.VoiceFrame{Codec: network.CodecIMBE, Data: make([]byte, 11)}); !errors.Is(err, ErrCodec) {
		t.Errorf("IMBE frame: %v", err)
	}
	if err := m.SendFrame(network.VoiceFrame{Data: make([]byte, 6)}); !errors.Is(err, ErrCodec) {
		t.Errorf("short frame: %v", err)
	}
	for i := range 4 {
		f := recvT(t, n.voice, "voice")
		if f.Call != h.Call || f.Seq != uint32(i) || f.Codec != network.CodecAMBE || !bytes.Equal(f.Data, ambeFrame(i)) {
			t.Fatalf("voice %d: %+v", i, f)
		}
	}
	if err := m.EndFrames(); err != nil {
		t.Fatal(err)
	}
	if c := recvT(t, n.ends, "EndTX"); c != h.Call {
		t.Errorf("EndTX %d, want %d", c, h.Call)
	}

	// The radio's talkgroup and private flag win over the target.
	if err := m.SendHeader(network.CallHeader{SrcID: 1234567, TG: 9, Private: true}); err != nil {
		t.Fatal(err)
	}
	h2 := recvT(t, n.starts, "StartTX")
	if h2.TG != 9 || !h2.Private || h2.Src != "N0CALL" || h2.Call == h.Call {
		t.Errorf("private StartTX %+v", h2)
	}
	// A new header ends the transmission under way.
	if err := m.SendHeader(network.CallHeader{SrcID: 1234567}); err != nil {
		t.Fatal(err)
	}
	if c := recvT(t, n.ends, "EndTX"); c != h2.Call {
		t.Errorf("EndTX %d, want %d", c, h2.Call)
	}
	if h3 := recvT(t, n.starts, "StartTX"); h3.TG != 91 {
		t.Errorf("StartTX %+v", h3)
	}
}

func TestFramesTransmitCutsReceive(t *testing.T) {
	m, n, sink := startFrames(t, mode.BM, mode.Target{ID: "91"}, network.CodecAMBE)
	m.SetFrames(true)
	sink.Header(network.CallHeader{Call: 50, SrcID: 1234567, TG: 91})
	ev := recvT(t, m.RecvFrames(), "header")
	if err := m.SendHeader(network.CallHeader{SrcID: 1234567}); err != nil {
		t.Fatal(err)
	}
	if e := recvT(t, m.RecvFrames(), "end"); e.Kind != mode.FrameEnd || e.Call != ev.Call {
		t.Errorf("end %+v", e)
	}
	recvT(t, n.starts, "StartTX")
}

func TestFramesReflector(t *testing.T) {
	m, n, _ := startFrames(t, mode.YSF, mode.Target{ID: "Alpha"}, network.CodecAMBE)
	// The talkgroup means nothing to a reflector network: the room is used.
	if err := m.SendHeader(network.CallHeader{SrcID: 1234567, TG: 9}); err != nil {
		t.Fatal(err)
	}
	if h := recvT(t, n.starts, "StartTX"); h.Room != "Alpha" || h.TG != 0 {
		t.Errorf("StartTX %+v", h)
	}
	_ = m.Disconnect(context.Background())
	_ = m.EndFrames()
	if err := m.SendHeader(network.CallHeader{SrcID: 1234567, TG: 9}); !errors.Is(err, ErrNotLinked) {
		t.Errorf("SendHeader with no link: %v", err)
	}
}

func TestFramesOffDecodes(t *testing.T) {
	n := newChanNet(network.CodecAMBE)
	m, err := New(mode.BM, mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: base[mode.BM]}, Options{
		NewNetwork: func(ctl.NetConfig, ctl.Identity) (network.Network, error) { return n, nil },
		AMBE:       fakeAMBE{}, RxSilence: time.Hour, TxTimeout: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	sink := recvT(t, n.ran, "run")
	sink.Header(network.CallHeader{Call: 5, SrcID: 1234567, TG: 91})
	sink.Voice(network.VoiceFrame{Call: 5, Data: ambeFrame(42)})
	f := recvT(t, m.Recv(), "audio")
	if f[0] != 42 {
		t.Errorf("decoded %d", f[0])
	}
	select {
	case ev := <-m.RecvFrames():
		t.Errorf("frame event %+v with frames off", ev)
	default:
	}
}
