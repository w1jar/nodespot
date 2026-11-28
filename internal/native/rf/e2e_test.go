package rf

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/dv"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// chanNet is a DMR network stand-in that reports on channels.
type chanNet struct {
	ran    chan network.Sink
	tunes  chan ctl.Target
	starts chan network.CallHeader
	voice  chan network.VoiceFrame
	ends   chan uint64
}

func newChanNet() *chanNet {
	return &chanNet{ran: make(chan network.Sink, 1), tunes: make(chan ctl.Target, 16),
		starts: make(chan network.CallHeader, 16), voice: make(chan network.VoiceFrame, 256), ends: make(chan uint64, 16)}
}

func (n *chanNet) Mode() string          { return "FAKE" }
func (n *chanNet) Codec() network.Codec  { return network.CodecAMBE }
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

// noAMBE fails the test if the vocoder is used: AMBE must pass through.
type noAMBE struct{ t *testing.T }

func (a noAMBE) Encode(context.Context, *[vocoder.PCMSamples]int16) ([vocoder.AMBEBytes]byte, error) {
	a.t.Error("the AMBE encoder was used")
	return [vocoder.AMBEBytes]byte{}, nil
}
func (a noAMBE) Decode(context.Context, [vocoder.AMBEBytes]byte) ([vocoder.PCMSamples]int16, error) {
	a.t.Error("the AMBE decoder was used")
	return [vocoder.PCMSamples]int16{}, nil
}

func testAMBE(n int) []dmr.AMBE {
	out := make([]dmr.AMBE, n)
	for i := range out {
		out[i] = dmr.AMBE{byte(i), byte(i * 5), 0xA5, byte(200 - i), 0x0F, byte(i * 3), 0x80}
	}
	return out
}

// decodeSent decodes the bursts a duplex modem was given for one slot.
func decodeSent(bursts []dmr.Burst) (lc dmr.LC, voice []dmr.AMBE, ended bool) {
	rx := dmr.NewRX()
	vseq := 0
	for _, b := range bursts {
		s, _ := dmr.MatchSync(&b, dmr.SyncMaxBitError)
		var res dmr.RXResult
		switch s {
		case dmr.SyncBSData:
			st, _ := b.SlotType()
			res = rx.Push(dmr.FrameDataSync, uint8(st.DataType), &b)
		case dmr.SyncBSVoice:
			vseq = 0
			res = rx.Push(dmr.FrameVoiceSync, 0, &b)
		default:
			vseq++
			res = rx.Push(dmr.FrameVoice, uint8(vseq), &b)
		}
		if res.LCValid {
			lc = res.LC
		}
		if res.HasVoice {
			voice = append(voice, res.Voice[:]...)
		}
		ended = ended || res.End
	}
	return lc, voice, ended
}

// TestEndToEnd runs a call from a fake modem through the mmdvm driver,
// the endpoint and a BrandMeister DV mode to a fake network, and one back
// from the network to the modem, comparing the AMBE bytes exactly.
func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	modem := fake.New(fake.Options{AutoDrain: true})
	drv := mmdvm.New(mmdvm.Settings{
		Open:           modem.Open,
		Modem:          mmdvm.Config{Duplex: true, ColorCode: 1, TXDelay: 100 * time.Millisecond, DMRLevel: 50, RXLevel: 50},
		AllowTX:        true,
		StatusInterval: 2 * time.Millisecond,
		BootWait:       time.Millisecond,
		VersionRetry:   5 * time.Millisecond,
		Playout:        time.Millisecond,
		TXHang:         time.Hour,
		RXTimeout:      time.Hour,
	})

	net := newChanNet()
	m, err := dv.New(mode.BM, mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: map[string]string{
		dv.KeyDMRID: "1234567", dv.KeyPassword: "pw-secret", dv.KeyAddress: "master.example.net:62031",
	}}, dv.Options{
		NewNetwork: func(ctl.NetConfig, ctl.Identity) (network.Network, error) { return net, nil },
		AMBE:       noAMBE{t}, TxTimeout: time.Hour, RxSilence: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	netSink := recv(t, net.ran, "network Run")
	if err := m.Connect(ctx, mode.Target{ID: "91"}); err != nil {
		t.Fatal(err)
	}
	netSink.Login(ctl.LoginOK, "")
	if tg := recv(t, net.tunes, "tune"); tg.TG != 91 {
		t.Fatalf("tuned %+v", tg)
	}

	// Real time drives the modem; the loop guard and hang are cut short
	// so the network call can follow the RF call by the same station.
	e := New(drv, Config{Hang: time.Nanosecond, LoopGuard: time.Nanosecond})
	if err := e.Link(2, m); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- e.Run(runCtx) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	if err := modem.Wait(ctx, func() bool { return drv.Status().Open }); err != nil {
		t.Fatal(err)
	}

	// RF to the network.
	ambe := testAMBE(18)
	tx := dmr.NewTX(dmr.TXConfig{Src: 1234567, Dst: 91, CC: 1, MSSync: true})
	fs := tx.Start()
	for _, a := range ambe {
		fs = append(fs, tx.Write(a)...)
	}
	fs = append(fs, tx.End()...)
	if err := modem.InjectFrames(2, fs); err != nil {
		t.Fatal(err)
	}
	h := recv(t, net.starts, "network StartTX")
	if h.SrcID != 1234567 || h.TG != 91 || h.Private {
		t.Errorf("network header %+v", h)
	}
	for i, a := range ambe {
		f := recv(t, net.voice, "network voice")
		if f.Call != h.Call || f.Codec != network.CodecAMBE || !bytes.Equal(f.Data, a[:]) {
			t.Fatalf("network frame %d: %+v, want % X", i, f, a)
		}
	}
	if c := recv(t, net.ends, "network EndTX"); c != h.Call {
		t.Errorf("EndTX %d, want %d", c, h.Call)
	}
	// Let the hang run out before the network calls back.
	if !e.do(ctx, e.housekeeping) {
		t.Fatal("the endpoint stopped")
	}
	if s := e.Status()[1]; s.State != StateIdle || s.Mode != mode.BM {
		t.Fatalf("slot 2 %+v", s)
	}

	// The network to RF.
	back := testAMBE(14)
	netSink.Header(network.CallHeader{Call: 50, Src: "N1CALL", SrcID: 1234567, TG: 91, Slot: 2})
	for i, a := range back {
		netSink.Voice(network.VoiceFrame{Call: 50, Seq: uint32(i), Codec: network.CodecAMBE, Data: append([]byte(nil), a[:]...)})
	}
	netSink.End(50)
	var bursts []dmr.Burst
	if err := modem.Wait(ctx, func() bool {
		bursts = modem.Sent(2)
		_, _, ended := decodeSent(bursts)
		return ended
	}); err != nil {
		t.Fatalf("waiting for the terminator: %v (%d bursts)", err, len(bursts))
	}
	if n := len(modem.Sent(1)); n != 0 {
		t.Errorf("%d bursts on slot 1", n)
	}
	lc, got, _ := decodeSent(bursts)
	if lc.Src != 1234567 || lc.Dst != 91 || lc.Private() {
		t.Errorf("LC %+v", lc)
	}
	if len(got) < len(back) {
		t.Fatalf("%d frames on the air, want %d", len(got), len(back))
	}
	for i, a := range back {
		if got[i] != a {
			t.Fatalf("air frame %d: % X, want % X", i, got[i], a)
		}
	}
	for _, a := range got[len(back):] {
		if a != dmr.SilenceAMBE {
			t.Errorf("padding % X is not silence", a)
		}
	}

	// Cancel stops the transmitter.
	stop()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	done <- nil
	if err := modem.Wait(ctx, func() bool { a := modem.State().Aborts; return a[0]+a[1] > 0 }); err != nil {
		t.Errorf("no DMR_ABORT after cancel: %v", err)
	}
}
