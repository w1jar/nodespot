package fake

import (
	"encoding/binary"
	"testing"
)

func setFreqPayload(rx, tx, pocsag uint32, short bool) []byte {
	p := make([]byte, 14)
	binary.LittleEndian.PutUint32(p[1:], rx)
	binary.LittleEndian.PutUint32(p[5:], tx)
	binary.LittleEndian.PutUint32(p[10:], pocsag)
	if short {
		return p[:10]
	}
	return p
}

func TestFreqChecks(t *testing.T) {
	cases := []struct {
		name         string
		rx, tx, pocs uint32
		short        bool
		dual         bool
		ok           bool
	}{
		{"simplex uhf", 433_500_000, 433_500_000, 433_500_000, false, false, true},
		{"split uhf", 441_000_000, 433_500_000, 441_000_000, false, false, true},
		{"pocsag zero", 433_500_000, 433_500_000, 0, false, false, false},
		{"short form uses tx", 433_500_000, 433_500_000, 0, true, false, true},
		{"out of band", 400_000_000, 400_000_000, 433_500_000, false, false, false},
		{"pocsag out of band", 433_500_000, 433_500_000, 400_000_000, false, false, false},
		{"band max is exclusive", 475_000_000, 475_000_000, 441_000_000, false, false, false},
		{"band min is inclusive", 420_000_000, 420_000_000, 420_000_000, false, false, true},
		{"banned 70cm rx", 436_000_000, 441_000_000, 441_000_000, false, false, false},
		{"banned edge", 438_000_000, 441_000_000, 441_000_000, false, false, false},
		{"just above ban", 438_000_001, 441_000_000, 441_000_000, false, false, true},
		{"banned pocsag", 441_000_000, 441_000_000, 435_000_000, false, false, false},
		{"banned 2m", 145_900_000, 145_900_000, 145_900_000, false, true, false},
		{"2m on duplex board", 145_000_000, 145_000_000, 145_000_000, false, false, false},
		{"2m on dual band", 145_000_000, 145_000_000, 145_000_000, false, true, true},
	}
	for _, c := range cases {
		m := New(Options{DualBand: c.dual})
		if got := m.freqOK(setFreqPayload(c.rx, c.tx, c.pocs, c.short)); got != c.ok {
			t.Errorf("%s: got %v, want %v", c.name, got, c.ok)
		}
	}
}
