package echotest

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/dv/vocoder/fake"
	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newMode(t *testing.T, vals map[string]string, env mode.Env, opt Options) (*Mode, *clock) {
	t.Helper()
	ck := &clock{t: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	env.Now = ck.now
	opt.Tick = -1 // the test calls Step
	m, err := New(env, mode.Settings{Values: vals}, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	return m, ck
}

// frameN is a frame whose samples all equal n (n is the frame's identity).
func frameN(n int) audio.Frame {
	var f audio.Frame
	for i := range f {
		f[i] = int16(n)
	}
	return f
}

func sineFrame(amp float64, k0 int) audio.Frame {
	var f audio.Frame
	for i := range f {
		f[i] = int16(amp * math.Sin(2*math.Pi*400*float64(k0+i)/audio.SampleRate))
	}
	return f
}

func waitReady(t *testing.T, m *Mode) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		r := m.ready
		m.mu.Unlock()
		if r {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the recording was not coded in time")
}

// drain steps the mode at now and collects what it delivers until the
// playback is over.
func drain(t *testing.T, m *Mode, ck *clock) []audio.Frame {
	t.Helper()
	var got []audio.Frame
	for i := 0; i < 10000; i++ {
		m.Step(ck.now())
		for {
			select {
			case f := <-m.Recv():
				got = append(got, f)
				continue
			default:
			}
			break
		}
		if !m.Talking() {
			return got
		}
		ck.add(audio.FrameDuration)
	}
	t.Fatal("the playback did not end")
	return nil
}

func TestRecordThenPlaybackAfterDelay(t *testing.T) {
	m, ck := newMode(t, nil, mode.Env{}, Options{})
	if st := m.Status(); st.State != mode.StateConnected || st.Target == nil {
		t.Fatalf("status %+v", st)
	}
	for i := 1; i <= 10; i++ {
		m.Send(frameN(i * 100))
		ck.add(audio.FrameDuration)
		m.Step(ck.now())
		if len(m.Recv()) != 0 || m.Talking() {
			t.Fatal("played back while still recording")
		}
	}
	m.EndTx()
	waitReady(t, m)
	end := ck.now()

	ck.add(900 * time.Millisecond)
	m.Step(ck.now())
	if m.Talking() || len(m.Recv()) != 0 {
		t.Fatal("played back before the delay")
	}
	ck.add(100 * time.Millisecond) // 1 s after the end
	if !ck.now().Equal(end.Add(time.Second)) {
		t.Fatal("clock")
	}
	m.Step(ck.now())
	if !m.Talking() {
		t.Fatal("no playback after the delay")
	}
	got := drain(t, m, ck)
	if len(got) != 10 {
		t.Fatalf("%d frames played back, want 10", len(got))
	}
	for i, f := range got {
		if f != frameN((i+1)*100) {
			t.Fatalf("frame %d out of order or changed", i)
		}
	}
	if m.Talking() || m.phaseName() != "idle" {
		t.Fatalf("still %s", m.phaseName())
	}
}

func TestPlaybackFollowsTheNodesPace(t *testing.T) {
	m, ck := newMode(t, nil, mode.Env{}, Options{})
	for i := 0; i < 20; i++ {
		m.Send(frameN(i))
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	m.Step(ck.now())
	if n := len(m.Recv()); n != queued {
		t.Fatalf("%d frames queued, want %d (the rest waits for the node)", n, queued)
	}
	m.Step(ck.now())
	if n := len(m.Recv()); n != queued {
		t.Fatalf("%d frames queued", n)
	}
	<-m.Recv()
	m.Step(ck.now())
	if n := len(m.Recv()); n != queued {
		t.Fatalf("%d frames queued after the node took one", n)
	}
}

func TestRecordingCapsAtSixtySeconds(t *testing.T) {
	m, ck := newMode(t, nil, mode.Env{}, Options{})
	for i := 0; i < MaxFrames+100; i++ {
		m.Send(frameN(1000))
	}
	m.EndTx()
	rep := m.Status().Levels
	if rep == nil || rep.Frames != MaxFrames || rep.DurationMs != 60000 {
		t.Fatalf("report %+v", rep)
	}
	waitReady(t, m)
	ck.add(2 * time.Second)
	if got := drain(t, m, ck); len(got) != MaxFrames {
		t.Fatalf("%d frames played back, want %d", len(got), MaxFrames)
	}
}

func TestNewTransmissionStopsPlaybackAndRecordsAgain(t *testing.T) {
	m, ck := newMode(t, nil, mode.Env{}, Options{})
	for i := 0; i < 20; i++ {
		m.Send(frameN(111))
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	m.Step(ck.now())
	if !m.Talking() || len(m.Recv()) == 0 {
		t.Fatal("playback did not start")
	}
	// The station keys up again during the playback.
	m.Send(frameN(222))
	if m.Talking() || len(m.Recv()) != 0 {
		t.Fatal("the playback was not stopped")
	}
	if m.phaseName() != "recording" {
		t.Fatalf("phase %s", m.phaseName())
	}
	m.Send(frameN(222))
	m.Step(ck.now())
	if m.Talking() {
		t.Fatal("played while recording")
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	got := drain(t, m, ck)
	if len(got) != 2 || got[0] != frameN(222) || got[1] != frameN(222) {
		t.Fatalf("played back %d frames, the first %v", len(got), got[:1])
	}
}

func TestNewTransmissionDuringTheDelayDropsTheOldRecording(t *testing.T) {
	m, ck := newMode(t, nil, mode.Env{}, Options{})
	m.Send(frameN(1))
	m.EndTx()
	ck.add(500 * time.Millisecond)
	m.Send(frameN(2))
	m.Send(frameN(2))
	m.EndTx()
	waitReady(t, m)
	ck.add(1100 * time.Millisecond)
	got := drain(t, m, ck)
	if len(got) != 2 || got[0] != frameN(2) {
		t.Fatalf("got %d frames", len(got))
	}
}

func TestEndTxWithoutRecordingIsIgnored(t *testing.T) {
	m, _ := newMode(t, nil, mode.Env{}, Options{})
	m.EndTx()
	if m.Status().Levels != nil || m.phaseName() != "idle" {
		t.Fatal("EndTx with nothing recorded did something")
	}
}

func TestStopEndsEverything(t *testing.T) {
	m, ck := newMode(t, nil, mode.Env{}, Options{})
	for i := 0; i < 10; i++ {
		m.Send(frameN(5))
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	m.Step(ck.now())
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if m.Talking() || len(m.Recv()) != 0 || m.Status().State != mode.StateIdle {
		t.Fatalf("after stop: %+v", m.Status())
	}
	m.Send(frameN(5)) // ignored while stopped
	if m.phaseName() != "idle" {
		t.Fatal("recorded while stopped")
	}
}

// Every codec gives back as many frames as it got, whatever it does to the
// samples; the plain one changes nothing.
func TestCodecRoundTripKeepsFrameCount(t *testing.T) {
	for _, codec := range []string{CodecPlain, CodecIMBE, CodecCodec2, CodecGSM, CodecULaw} {
		t.Run(codec, func(t *testing.T) {
			m, ck := newMode(t, map[string]string{KeyCodec: codec}, mode.Env{}, Options{})
			const n = 40
			var in []audio.Frame
			for i := 0; i < n; i++ {
				f := sineFrame(8000, i*audio.FrameSamples)
				in = append(in, f)
				m.Send(f)
			}
			m.EndTx()
			waitReady(t, m)
			ck.add(2 * time.Second)
			got := drain(t, m, ck)
			if len(got) != n {
				t.Fatalf("%d frames out, %d in", len(got), n)
			}
			if st := m.Status(); st.Error != "" {
				t.Fatalf("error %q", st.Error)
			}
			switch codec {
			case CodecPlain:
				for i := range got {
					if got[i] != in[i] {
						t.Fatalf("plain changed frame %d", i)
					}
				}
			case CodecULaw:
				for i := range got {
					for j := range got[i] {
						if d := int(got[i][j]) - int(in[i][j]); d > 400 || d < -400 {
							t.Fatalf("ulaw frame %d sample %d: %d vs %d", i, j, got[i][j], in[i][j])
						}
					}
				}
			default:
				if rep := audio.Analyze(time.Time{}, got); rep.VoicedFrames == 0 {
					t.Fatal("the codec output is silent")
				}
			}
		})
	}
}

// fakeAMBE is a vocoder whose frame carries the first sample.
type fakeAMBE struct {
	mu   sync.Mutex
	encs int
	decs int
	err  error
}

func (v *fakeAMBE) Encode(_ context.Context, p *[vocoder.PCMSamples]int16) ([vocoder.AMBEBytes]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.encs++
	if v.err != nil {
		return vocoder.SilenceAMBE, v.err
	}
	return [vocoder.AMBEBytes]byte{9, byte(p[0] >> 8), byte(p[0])}, nil
}

func (v *fakeAMBE) Decode(_ context.Context, f [vocoder.AMBEBytes]byte) ([vocoder.PCMSamples]int16, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.decs++
	var out [vocoder.PCMSamples]int16
	for i := range out {
		out[i] = int16(uint16(f[1])<<8|uint16(f[2])) + 1
	}
	return out, nil
}

func TestAMBEThroughFakeVocoder(t *testing.T) {
	v := &fakeAMBE{}
	m, ck := newMode(t, map[string]string{KeyCodec: "ambe"}, mode.Env{}, Options{AMBE: v})
	for i := 1; i <= 5; i++ {
		m.Send(frameN(i * 10))
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	got := drain(t, m, ck)
	if len(got) != 5 || v.encs != 5 || v.decs != 5 {
		t.Fatalf("%d frames, %d encodes, %d decodes", len(got), v.encs, v.decs)
	}
	for i, f := range got {
		if f != frameN((i+1)*10+1) { // the fake adds one so the codec shows
			t.Fatalf("frame %d not coded: %d", i, f[0])
		}
	}
	if m.Status().Error != "" {
		t.Fatalf("error %q", m.Status().Error)
	}
}

func TestAMBEVocoderErrorStillPlaysBack(t *testing.T) {
	v := &fakeAMBE{err: vocoder.ErrDown}
	m, ck := newMode(t, map[string]string{KeyCodec: "ambe"}, mode.Env{}, Options{AMBE: v})
	for i := 1; i <= 4; i++ {
		m.Send(frameN(i * 10))
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	got := drain(t, m, ck)
	if len(got) != 4 || got[0] != frameN(10) {
		t.Fatalf("expected the recording unchanged, got %d frames", len(got))
	}
	if st := m.Status(); !strings.Contains(st.Error, "AMBE vocoder") || st.State != mode.StateConnected {
		t.Fatalf("status %+v", st)
	}
}

// The default vocoder is the software AMBE vocoder client, here against the fake
// server the DV tests use.
func TestAMBEThroughMD380Client(t *testing.T) {
	srv, err := fake.New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	m, ck := newMode(t, map[string]string{KeyCodec: "ambe"}, mode.Env{MD380: srv.Addr(), MD380Timeout: time.Second}, Options{})
	m.Send(frameN(700))
	m.Send(frameN(900))
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	got := drain(t, m, ck)
	if len(got) != 2 || got[0] != frameN(700) || got[1] != frameN(900) {
		t.Fatalf("got %d frames", len(got))
	}
	if srv.Requests() != 4 {
		t.Fatalf("%d vocoder requests", srv.Requests())
	}
}

func TestAMBEVocoderDownIsReported(t *testing.T) {
	srv, err := fake.New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr()
	srv.Close() // nothing listens any more
	m, ck := newMode(t, map[string]string{KeyCodec: "ambe"}, mode.Env{MD380: addr, MD380Timeout: 20 * time.Millisecond}, Options{})
	for i := 1; i <= 3; i++ {
		m.Send(frameN(i * 10))
	}
	m.EndTx()
	waitReady(t, m)
	ck.add(2 * time.Second)
	got := drain(t, m, ck)
	if len(got) != 3 || got[1] != frameN(20) {
		t.Fatalf("expected the recording as it was, got %d frames", len(got))
	}
	st := m.Status()
	if !strings.Contains(st.Error, "software AMBE vocoder") || !strings.Contains(st.Detail, "AMBE vocoder not working") {
		t.Fatalf("status %+v", st)
	}
}

func TestStatusCarriesTheLevelReport(t *testing.T) {
	m, _ := newMode(t, nil, mode.Env{}, Options{})
	for i := 0; i < 25; i++ {
		m.Send(sineFrame(3277, i*audio.FrameSamples)) // -20 dBFS peak
	}
	m.EndTx()
	rep := m.Status().Levels
	if rep == nil || rep.DurationMs != 500 || len(rep.Warnings) != 0 {
		t.Fatalf("report %+v", rep)
	}
	if math.Abs(rep.PeakDb+20) > 0.2 || math.Abs(rep.RMSDb+23) > 0.3 {
		t.Fatalf("levels %+v", rep)
	}
	// A second recording replaces it.
	m.Send(sineFrame(32767, 0))
	m.EndTx()
	rep = m.Status().Levels
	if rep.DurationMs != 20 || len(rep.Warnings) == 0 {
		t.Fatalf("report %+v", rep)
	}
}

func TestNeedsAMBEFollowsTheCodec(t *testing.T) {
	sp := Spec()
	for codec, want := range map[string]bool{"": false, "plain": false, "ambe": true, "AMBE": true,
		"imbe": false, "codec2": false, "gsm": false, "ulaw": false} {
		s := mode.Settings{Values: map[string]string{}}
		if codec != "" {
			s.Values[KeyCodec] = codec
		}
		if got := sp.AMBENeeded(s); got != want {
			t.Errorf("codec %q: needs AMBE = %v", codec, got)
		}
	}
	if sp.AMBENeeded(mode.Settings{}) {
		t.Error("an unconfigured echo test needs AMBE")
	}
	if sp.NeedsAMBE {
		t.Error("the static flag must stay off")
	}
}

func TestSpecChecksSettings(t *testing.T) {
	sp := Spec()
	for _, c := range Codecs {
		if err := sp.Check(mode.Settings{Values: map[string]string{KeyCodec: c}}); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	if err := sp.Check(mode.Settings{Values: map[string]string{KeyCodec: "opus"}}); err == nil {
		t.Error("an unknown codec was accepted")
	}
	if err := sp.Check(mode.Settings{Values: map[string]string{KeyDelay: "-5"}}); err == nil {
		t.Error("a negative delay was accepted")
	}
	if _, err := mode.Build(mode.EchoTest, mode.Env{}, mode.Settings{}); err != nil {
		t.Fatal(err)
	}
}
