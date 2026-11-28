package rf

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/dv"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// rxRig is a simplex receive-only modem, driver, endpoint and a BrandMeister
// mode on a fake network.
type rxRig struct {
	modem   *fake.Modem
	drv     *mmdvm.Driver
	ep      *Endpoint
	m       *dv.Mode
	net     *chanNet
	netSink network.Sink
	ctx     context.Context
}

func newRXRig(t *testing.T) *rxRig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	r := &rxRig{ctx: ctx, modem: fake.New(fake.Options{}), net: newChanNet()}
	r.drv = mmdvm.New(mmdvm.Settings{
		Open:           r.modem.Open,
		Modem:          mmdvm.Config{ColorCode: 1, TXDelay: 100 * time.Millisecond, DMRLevel: 50, RXLevel: 50},
		Freq:           mmdvm.Freq{RX: 441_000_000, TX: 441_000_000},
		RXOnly:         true,
		AllowTX:        true, // forced off
		StatusInterval: 2 * time.Millisecond,
		BootWait:       time.Millisecond,
		VersionRetry:   5 * time.Millisecond,
		Playout:        time.Millisecond,
		RXTimeout:      time.Hour,
	})
	var err error
	r.m, err = dv.New(mode.BM, mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: map[string]string{
		dv.KeyDMRID: "1234567", dv.KeyPassword: "pw-secret", dv.KeyAddress: "master.example.net:62031",
	}}, dv.Options{
		NewNetwork: func(ctl.NetConfig, ctl.Identity) (network.Network, error) { return r.net, nil },
		AMBE:       noAMBE{t}, TxTimeout: time.Hour, RxSilence: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.m.Stop() })
	r.netSink = recv(t, r.net.ran, "network Run")
	if err := r.m.Connect(ctx, mode.Target{ID: "91"}); err != nil {
		t.Fatal(err)
	}
	r.netSink.Login(ctl.LoginOK, "")
	recv(t, r.net.tunes, "tune")

	r.ep = New(r.drv, Config{ReceiveOnly: true})
	if got := r.ep.Slots(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("slots %v", got)
	}
	if err := r.ep.Link(2, r.m); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.ep.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	if err := r.modem.Wait(ctx, func() bool { return r.drv.Status().Open }); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rxRig) noTX(t *testing.T) {
	t.Helper()
	for _, c := range []byte{mmdvm.CmdDMRData1, mmdvm.CmdDMRData2, mmdvm.CmdDMRStart, mmdvm.CmdDMRShortLC} {
		if n := r.modem.Count(c); n != 0 {
			t.Errorf("%s was written %d times", mmdvm.CommandName(c), n)
		}
	}
}

// talkEvent waits for the next talk event of kind.
func (r *rxRig) talkEvent(t *testing.T, kind mode.EventKind) *mode.Talker {
	t.Helper()
	for {
		select {
		case ev := <-r.m.Events():
			if ev.Kind == kind {
				return ev.Talker
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

// An RF call from the handheld reaches the DMR network with its AMBE bytes
// unchanged, shows as an RF talker, and the modem is never written a
// transmit frame.
func TestSimplexReceiveToNetwork(t *testing.T) {
	r := newRXRig(t)
	ambe := testAMBE(18)
	call := fake.RXCall{Src: 1234567, Dst: 91, CC: 1, AMBE: ambe, Interval: time.Millisecond}
	if err := r.modem.PlayCall(r.ctx, call); err != nil {
		t.Fatal(err)
	}
	h := recv(t, r.net.starts, "network StartTX")
	if h.SrcID != 1234567 || h.TG != 91 || h.Private {
		t.Errorf("network header %+v", h)
	}
	tk := r.talkEvent(t, mode.EventTalkStart)
	if tk.Via != "rf" || tk.ID != 1234567 || tk.Target != "91" || tk.Slot != 2 {
		t.Errorf("talker %+v", tk)
	}
	for i, a := range ambe {
		f := recv(t, r.net.voice, "network voice")
		if f.Call != h.Call || f.Codec != network.CodecAMBE || !bytes.Equal(f.Data, a[:]) {
			t.Fatalf("network frame %d: %+v, want % X", i, f, a)
		}
	}
	if c := recv(t, r.net.ends, "network EndTX"); c != h.Call {
		t.Errorf("EndTX %d, want %d", c, h.Call)
	}
	if tk := r.talkEvent(t, mode.EventTalkEnd); tk.Via != "rf" {
		t.Errorf("talk end %+v", tk)
	}

	// A call from the network is ignored: nothing goes to the radio.
	r.netSink.Header(network.CallHeader{Call: 50, Src: "N1CALL", SrcID: 7654321, TG: 91, Slot: 2})
	r.netSink.Voice(network.VoiceFrame{Call: 50, Codec: network.CodecAMBE, Data: ambe[0][:]})
	r.netSink.End(50)
	if err := r.modem.Wait(r.ctx, func() bool { return r.ep.Status()[0].NetSkipped > 0 }); err != nil {
		t.Fatal(err)
	}
	r.noTX(t)
	if n := r.drv.BlockedTX(); n != 0 {
		t.Errorf("the write filter dropped %d frames: something asked to transmit", n)
	}
}

// A DMR_LOST from the modem ends the RF call on the network.
func TestSimplexReceiveLost(t *testing.T) {
	r := newRXRig(t)
	ambe := testAMBE(6)
	call := fake.RXCall{Src: 1234567, Dst: 91, CC: 1, AMBE: ambe, Interval: time.Millisecond, Lost: true}
	if err := r.modem.PlayCall(r.ctx, call); err != nil {
		t.Fatal(err)
	}
	h := recv(t, r.net.starts, "network StartTX")
	for i, a := range ambe {
		f := recv(t, r.net.voice, "network voice")
		if !bytes.Equal(f.Data, a[:]) {
			t.Fatalf("frame %d: % X, want % X", i, f.Data, a)
		}
	}
	if c := recv(t, r.net.ends, "network EndTX"); c != h.Call {
		t.Errorf("EndTX %d, want %d", c, h.Call)
	}
	if tk := r.talkEvent(t, mode.EventTalkEnd); tk.Via != "rf" {
		t.Errorf("talk end %+v", tk)
	}
	r.noTX(t)
}

// With no mode linked the RF call is dropped and counted.
func TestSimplexReceiveNoMode(t *testing.T) {
	r := newRXRig(t)
	if err := r.ep.Link(2, nil); err != nil {
		t.Fatal(err)
	}
	call := fake.RXCall{Src: 1234567, Dst: 91, CC: 1, AMBE: testAMBE(3), Interval: time.Millisecond}
	if err := r.modem.PlayCall(r.ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := r.modem.Wait(r.ctx, func() bool { return r.ep.Status()[0].NoMode > 0 }); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-r.net.starts:
		t.Errorf("network got %+v", h)
	default:
	}
}
