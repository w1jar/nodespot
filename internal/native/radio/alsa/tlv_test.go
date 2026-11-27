package alsa

import (
	"encoding/binary"
	"math"
	"testing"
)

// tlvBytes builds TLV bytes from 32-bit words, as the kernel returns them.
func tlvBytes(w ...uint32) []byte {
	b := make([]byte, 4*len(w))
	for i, v := range w {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return b
}

func neg(v int32) uint32 { return uint32(v) }

func TestTLVDbScale(t *testing.T) {
	// DB_SCALE: min -1200 (-12 dB), 50 (0.5 dB) per step, no mute.
	b := tlvBytes(tlvDBScale, 8, neg(-1200), 50)
	for raw, want := range map[int64]float64{0: -12, 1: -11.5, 35: 5.5} {
		if db, ok := tlvDb(b, 0, 35, raw); !ok || math.Abs(db-want) > 1e-9 {
			t.Errorf("raw %d = %v %v, want %v", raw, db, ok, want)
		}
	}
	// With the mute flag the lowest step is mute.
	m := tlvBytes(tlvDBScale, 8, neg(-9600), 50|tlvMuteFlag)
	if _, ok := tlvDb(m, 0, 10, 0); ok {
		t.Error("mute step has a dB")
	}
	if db, ok := tlvDb(m, 0, 10, 2); !ok || math.Abs(db+95) > 1e-9 {
		t.Errorf("above mute = %v %v", db, ok)
	}
}

func TestTLVDbMinMax(t *testing.T) {
	// DB_MINMAX: -1000..+500 (-10..+5 dB) over raw 10..40.
	b := tlvBytes(tlvDBMinMax, 8, neg(-1000), 500)
	if db, ok := tlvDb(b, 10, 40, 10); !ok || db != -10 {
		t.Errorf("min = %v %v", db, ok)
	}
	if db, ok := tlvDb(b, 10, 40, 40); !ok || db != 5 {
		t.Errorf("max = %v %v", db, ok)
	}
	if db, ok := tlvDb(b, 10, 40, 25); !ok || math.Abs(db+2.5) > 1e-9 {
		t.Errorf("mid = %v %v", db, ok)
	}
	mm := tlvBytes(tlvDBMinMaxMute, 8, neg(-1000), 500)
	if _, ok := tlvDb(mm, 10, 40, 10); ok {
		t.Error("mute minimum has a dB")
	}
}

func TestTLVDbRange(t *testing.T) {
	// Two ranges: raw 0..1 as a scale from -60 dB, raw 2..10 as min/max.
	b := tlvBytes(tlvDBRange, 4*(4+2)+4*(4+2),
		0, 1, tlvDBScale, 8, neg(-6000), 3000,
		2, 10, tlvDBMinMax, 8, neg(-3000), 0)
	for raw, want := range map[int64]float64{0: -60, 1: -30, 2: -30, 6: -15, 10: 0} {
		if db, ok := tlvDb(b, 0, 10, raw); !ok || math.Abs(db-want) > 1e-9 {
			t.Errorf("raw %d = %v %v, want %v", raw, db, ok, want)
		}
	}
	if _, ok := tlvDb(b, 0, 10, 11); ok {
		t.Error("raw outside every range has a dB")
	}
}

func TestTLVDbRejectsBadInput(t *testing.T) {
	for _, b := range [][]byte{nil, {1, 2, 3}, tlvBytes(tlvDBScale, 8, 0), tlvBytes(tlvDBScale, 7, 0, 0), tlvBytes(tlvDBLinear, 8, 0, 0), tlvBytes(99, 0)} {
		if _, ok := tlvDb(b, 0, 10, 3); ok {
			t.Errorf("accepted %v", b)
		}
	}
}
