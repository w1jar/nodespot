package alsa

import "encoding/binary"

// TLV types of a control's dB information (SNDRV_CTL_TLVT_*).
const (
	tlvDBScale      = 1 // min, step (0.01 dB; bit 16 is the mute flag)
	tlvDBLinear     = 2 // not supported here
	tlvDBRange      = 3 // nested TLVs for ranges of raw values
	tlvDBMinMax     = 4 // min and max, 0.01 dB, in between linear in dB
	tlvDBMinMaxMute = 5 // as MinMax, the minimum raw value is mute
)

const tlvMuteFlag = 0x10000

// VolumeStep describes where a volume control sits on its hardware steps.
type VolumeStep struct {
	// Steps is the number of steps above the lowest: the control has Steps+1
	// positions, 0..Steps.
	Steps int
	// Step is the current position, 0..Steps.
	Step int
	// Db is the gain at that position in dB, nil when the control has no dB
	// information, or the step is mute.
	Db *float64
	// Dbs is the gain of every step 0..Steps (NaN where unknown or mute);
	// nil when the control has no dB information.
	Dbs []float64
}

// tlvWords reads little-endian 32-bit words, dropping a partial one.
func tlvWords(b []byte) []uint32 {
	w := make([]uint32, len(b)/4)
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return w
}

// tlvDb returns the gain in dB at raw for a control whose raw range is
// lo..hi, from its TLV bytes ([type][length in bytes][data...], as the
// kernel returns them). ok is false for an unsupported or malformed TLV and
// for a mute step.
func tlvDb(b []byte, lo, hi, raw int64) (db float64, ok bool) {
	return tlvDbWords(tlvWords(b), lo, hi, raw, 0)
}

func tlvDbWords(w []uint32, lo, hi, raw int64, depth int) (float64, bool) {
	if len(w) < 2 || depth > 4 {
		return 0, false
	}
	typ, n := w[0], int(w[1])/4
	if n < 0 || 2+n > len(w) || w[1]%4 != 0 {
		return 0, false
	}
	data := w[2 : 2+n]
	switch typ {
	case tlvDBScale:
		if n < 2 {
			return 0, false
		}
		min, step := int64(int32(data[0])), int64(data[1]&0xffff)
		if data[1]&tlvMuteFlag != 0 && raw == lo {
			return 0, false
		}
		return float64(min+(raw-lo)*step) / 100, true
	case tlvDBMinMax, tlvDBMinMaxMute:
		if n < 2 {
			return 0, false
		}
		min, max := int64(int32(data[0])), int64(int32(data[1]))
		if typ == tlvDBMinMaxMute && raw == lo {
			return 0, false
		}
		if hi <= lo {
			return float64(min) / 100, true
		}
		return (float64(min) + float64(max-min)*float64(raw-lo)/float64(hi-lo)) / 100, true
	case tlvDBRange:
		// Each entry: raw min, raw max, then a TLV for that range.
		for i := 0; i+4 <= len(data); {
			rmin, rmax := int64(int32(data[i])), int64(int32(data[i+1]))
			sub := int(data[i+3]) / 4
			if sub < 0 || i+4+sub > len(data) {
				return 0, false
			}
			if raw >= rmin && raw <= rmax {
				return tlvDbWords(data[i+2:i+4+sub], rmin, rmax, raw, depth+1)
			}
			i += 4 + sub
		}
	}
	return 0, false
}
