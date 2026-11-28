package fake_test

import (
	"bytes"
	"testing"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// The simplex persona has the shape of a real handheld call as a simplex
// hat reports it on DMR_DATA2: voice LC headers (control 0x41), voice burst
// A with sync (0x20) then B to F (1 to 5), and a terminator with LC (0x42).
func TestRXCallShape(t *testing.T) {
	ambe := make([]dmr.AMBE, 18) // 3 per burst: one superframe A to F
	call := fake.RXCall{Src: 1234567, Dst: 1, CC: 1, AMBE: ambe}
	var ctrl []byte
	for _, f := range call.Frames() {
		ctrl = append(ctrl, mmdvm.Control(f.FrameType, f.DTypeVSeq))
	}
	want := []byte{0x41, 0x41, 0x41, 0x20, 1, 2, 3, 4, 5, 0x42}
	if !bytes.Equal(ctrl, want) {
		t.Fatalf("controls % X", ctrl)
	}
}

// The hex of the wire frames decodes back to the LC and the AMBE frames:
// the fixture is built, not captured, with placeholder IDs.
func TestRXCallWireRoundTrip(t *testing.T) {
	ambe := []dmr.AMBE{{1, 2, 3, 4, 5, 6, 0x80}, {7, 8, 9, 10, 11, 12, 0x00}, {13, 14, 15, 16, 17, 18, 0x80}}
	call := fake.RXCall{Src: 1234567, Dst: 91, CC: 1, AMBE: ambe}
	var wire []byte
	for _, f := range call.Frames() {
		wire = append(wire, mmdvm.DMRData(2, mmdvm.Control(f.FrameType, f.DTypeVSeq), &f.Burst)...)
	}
	var dec mmdvm.Decoder
	rx := dmr.NewRX()
	var lc dmr.LC
	var got []dmr.AMBE
	var ended bool
	dec.Feed(wire, func(f mmdvm.Frame) {
		df, err := mmdvm.ParseDMRData(f)
		if err != nil {
			t.Fatal(err)
		}
		ft, v, err := mmdvm.ParseControl(df.Control)
		if err != nil {
			t.Fatal(err)
		}
		res := rx.Push(ft, v, &df.Burst)
		if res.LCValid {
			lc = res.LC
		}
		if res.HasVoice {
			got = append(got, res.Voice[:]...)
		}
		ended = ended || res.End
	})
	if lc.Src != 1234567 || lc.Dst != 91 || lc.Private() || !ended {
		t.Errorf("LC %+v ended %v", lc, ended)
	}
	if len(got) < len(ambe) {
		t.Fatalf("%d frames", len(got))
	}
	for i, a := range ambe {
		if got[i] != a {
			t.Errorf("frame %d: % X, want % X", i, got[i], a)
		}
	}
}
