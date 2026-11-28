package fake

import (
	"testing"

	"github.com/w1jar/nodespot/internal/dv/dmr"
)

func voiceBurst() dmr.Burst {
	var b dmr.Burst
	b.PutVoice([3]dmr.AMBE{{1, 2, 3, 4, 5, 6, 0x80}, {9, 8, 7, 6, 5, 4, 0}, {0xAA, 0x55, 1, 2, 3, 4, 0x80}})
	return b
}

func TestCorruptExactCount(t *testing.T) {
	for n := 0; n <= MaxBurstErrors; n++ {
		b := voiceBurst()
		corrupt(&b, n)
		if _, got := b.Voice(); got != n {
			t.Errorf("asked for %d errors, receiver corrects %d", n, got)
		}
	}
}

func TestErrorModel(t *testing.T) {
	m := New(Options{})
	m.tuned = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	set := func(rx uint32) {
		m.tuned[1], m.tuned[2], m.tuned[3], m.tuned[4] = byte(rx), byte(rx>>8), byte(rx>>16), byte(rx>>24)
	}
	m.SetRXErrors(&RXErrorModel{CenterHz: 433_500_000, ErrsPerKHz: 8, MaxErrs: 10, DeadbandHz: 100})
	for _, c := range []struct {
		rx   uint32
		want int
	}{{433_500_000, 0}, {433_500_100, 0}, {433_499_900, 0}, {433_500_600, 4}, {433_499_400, 4}, {433_502_000, 10}, {433_490_000, 10}} {
		set(c.rx)
		if got := m.errorsFor(); got != c.want {
			t.Errorf("rx %d: %d errors, want %d", c.rx, got, c.want)
		}
	}
	m.SetRXErrors(nil)
	if m.errorsFor() != 0 {
		t.Error("errors with the model off")
	}
}

func TestErrorModelNoiseIsDeterministic(t *testing.T) {
	run := func() []int {
		m := New(Options{})
		m.tuned = make([]byte, 14)
		m.SetRXErrors(&RXErrorModel{CenterHz: 0, Noise: 2})
		var out []int
		for i := 0; i < 50; i++ {
			n := m.errorsFor()
			if n < 0 || n > 2 {
				t.Fatalf("noise gave %d", n)
			}
			out = append(out, n)
		}
		return out
	}
	a, b := run(), run()
	same, nonzero := true, false
	for i := range a {
		same = same && a[i] == b[i]
		nonzero = nonzero || a[i] != 0
	}
	if !same || !nonzero {
		t.Errorf("same=%v nonzero=%v: %v", same, nonzero, a)
	}
}
