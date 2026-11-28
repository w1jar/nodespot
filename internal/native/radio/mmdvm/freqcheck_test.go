package mmdvm_test

import (
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

func TestCheckFreq(t *testing.T) {
	uhf := mmdvm.FreqRules{UHFOnly: true}
	any := mmdvm.FreqRules{}
	cases := []struct {
		name   string
		rx, tx uint32
		r      mmdvm.FreqRules
		bad    string // substring of the error, empty when accepted
	}{
		{"uhf ok", 441_000_000, 441_000_000, uhf, ""},
		{"uhf offset ok", 441_000_000, 446_000_000, uhf, ""},
		{"vhf on a dual hat", 144_800_000, 144_800_000, uhf, "below 420"},
		{"vhf on a dual band", 144_800_000, 144_800_000, any, ""},
		{"banned 435 to 438", 436_000_000, 436_000_000, uhf, "bans"},
		{"banned edge 438", 438_000_000, 441_000_000, uhf, "bans"},
		{"band edge 420 ok", 420_000_000, 420_000_000, uhf, ""},
		{"band top 475 out", 475_000_000, 475_000_000, uhf, "none of the bands"},
		{"banned 145.9", 145_900_000, 145_900_000, any, "bans"},
		{"gap 300 MHz", 300_000_000, 300_000_000, any, "none of the bands"},
		{"tx only out of band", 441_000_000, 300_000_000, any, "none of the bands"},
		{"900 MHz", 900_000_000, 900_000_000, any, ""},
	}
	for _, c := range cases {
		err := mmdvm.CheckFreq(c.rx, c.tx, c.r)
		if (err == nil) != (c.bad == "") || (err != nil && !strings.Contains(err.Error(), c.bad)) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.bad)
		}
	}
}

// CheckFreq accepts exactly what the firmware mirror accepts, with TX in
// the POCSAG field.
func TestCheckFreqMatchesFirmware(t *testing.T) {
	pts := []uint32{0, 144_000_000, 145_799_999, 145_800_000, 145_900_000, 146_000_000, 146_000_001, 147_999_999,
		148_000_000, 219_000_000, 224_999_999, 225_000_000, 300_000_000, 419_999_999, 420_000_000, 434_999_999,
		435_000_000, 437_999_999, 438_000_000, 438_000_001, 441_000_000, 474_999_999, 475_000_000, 842_000_000,
		949_999_999, 950_000_000}
	for _, r := range []mmdvm.FreqRules{{}, {UHFOnly: true}} {
		for _, rx := range pts {
			for _, tx := range pts {
				if rx == 0 || tx == 0 {
					continue
				}
				got := mmdvm.CheckFreq(rx, tx, r) == nil
				if want := mmdvm.Accepts(rx, tx, tx, r); got != want {
					t.Fatalf("rx %d tx %d %+v: CheckFreq %v, firmware %v", rx, tx, r, got, want)
				}
			}
		}
	}
}
