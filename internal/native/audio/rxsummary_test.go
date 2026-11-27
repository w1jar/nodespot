package audio

import (
	"testing"
	"time"
)

// runRx feeds n frames of a sine with peak amplitude amp through a fresh
// AGC and accumulator, as the node's receive path does.
func runRx(n int, amp float64, agcOn bool) (*RxAccum, AGCParams) {
	p := DefaultAGCParams()
	agc, acc := NewAGC(), NewRxAccum()
	for _, f := range sineFrames(n, amp) {
		in, out := f, f
		if agcOn {
			agc.Process(&out, p)
		}
		acc.Add(&in, &out, agc.GainDb(), p.MaxGainDb, agcOn)
	}
	return acc, p
}

func TestRxSummaryQuietPinsAGC(t *testing.T) {
	acc, p := runRx(250, 0.01, true) // 5 s, RMS -43 dBFS: needs more than the 18 dB the AGC has,
	s, ok := acc.Summary(time.Unix(1, 0))
	if !ok {
		t.Fatal("no summary")
	}
	if !s.AGCAtMax {
		t.Errorf("AGC not at max: %+v", s)
	}
	near(t, "gain", s.AGCGainDb, p.MaxGainDb, 2)
	near(t, "out", s.OutDb, s.SpeechDb+s.AGCGainDb, 1)
	if s.Clipped != 0 || s.DurationMs != 5000 || !s.At.Equal(time.Unix(1, 0)) {
		t.Errorf("summary %+v", s)
	}
}

func TestRxSummaryClipped(t *testing.T) {
	acc, _ := runRx(100, 1, true)
	s, ok := acc.Summary(time.Time{})
	if !ok {
		t.Fatal("no summary")
	}
	if s.Clipped == 0 {
		t.Errorf("clipped = 0: %+v", s)
	}
	near(t, "peak", s.PeakDb, 0, 0.1)
	if s.AGCAtMax {
		t.Error("loud audio must not pin the AGC")
	}
}

func TestRxSummaryNormal(t *testing.T) {
	acc, p := runRx(200, 0.1, true) // peak -20 dBFS, RMS -23
	s, ok := acc.Summary(time.Time{})
	if !ok {
		t.Fatal("no summary")
	}
	near(t, "peak", s.PeakDb, -20, 0.2)
	near(t, "speech", s.SpeechDb, -23, 0.3)
	if s.AGCAtMax || s.Clipped != 0 {
		t.Errorf("summary %+v", s)
	}
	if s.OutDb < p.TargetDb-3 || s.OutDb > p.TargetDb+3 {
		t.Errorf("out %.1f not near target %.1f", s.OutDb, p.TargetDb)
	}
}

func TestRxSummaryAGCOff(t *testing.T) {
	acc, _ := runRx(100, 0.1, false)
	s, _ := acc.Summary(time.Time{})
	if s.AGCGainDb != 0 || s.AGCAtMax {
		t.Errorf("summary %+v", s)
	}
	near(t, "out == speech", s.OutDb, s.SpeechDb, 0.01)
}

func TestRxSummaryKerchunkAndSilence(t *testing.T) {
	k, _ := runRx(20, 0.1, true) // 0.4 s
	if _, ok := k.Summary(time.Time{}); ok {
		t.Error("a kerchunk must not give a summary")
	}
	q, _ := runRx(100, 0, true)
	if _, ok := q.Summary(time.Time{}); ok {
		t.Error("silence must not give a summary")
	}
	acc, _ := runRx(100, 0.1, true)
	acc.Reset()
	if _, ok := acc.Summary(time.Time{}); ok {
		t.Error("a reset accumulator has no summary")
	}
}
