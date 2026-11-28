package fake

import (
	"encoding/binary"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// RXErrorModel makes the fake modem hear voice with bit errors that grow
// with how far its receive frequency (the SET_FREQ value, offset included, once SET_CONFIG or a mode change has programmed it)
// is from the frequency the transmitter really is on, as a hat whose
// crystal is off.
//
// Per voice burst the number of corrected bit errors is
// min(MaxErrs, round(ErrsPerKHz * max(0, |RX - CenterHz| - DeadbandHz) / 1000)) plus a
// deterministic jitter of up to +-Noise, never below 0. The errors are put
// in the FEC-protected bits so that the receiver counts exactly that many
// (up to 6 per AMBE frame, 18 per burst, which is also the practical
// MaxErrs limit). Nothing is random: the same calls give the same errors.
type RXErrorModel struct {
	// CenterHz is the receive frequency with no errors.
	CenterHz uint32
	// ErrsPerKHz is the corrected errors per burst for each kHz of distance.
	ErrsPerKHz float64
	// DeadbandHz is a flat bottom: no distance counts within it, so there
	// are no errors anywhere in CenterHz +- DeadbandHz.
	DeadbandHz uint32
	// MaxErrs caps the errors per burst (default and limit 18).
	MaxErrs int
	// Noise is the largest jitter added to or taken off a burst's errors.
	Noise int
}

// MaxBurstErrors is the most corrected bit errors the fake puts in one
// burst: 3 in C0 and 3 in C1 of each of the three AMBE frames.
const MaxBurstErrors = 18

// SetRXErrors turns the error model on, or off with nil.
func (m *Modem) SetRXErrors(e *RXErrorModel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rxErr = nil
	if e != nil {
		c := *e
		if c.MaxErrs <= 0 || c.MaxErrs > MaxBurstErrors {
			c.MaxErrs = MaxBurstErrors
		}
		m.rxErr = &c
		m.rxBursts = 0
	}
}

// RXFreq is the receive frequency the host last set, in Hz, and false
// before any SET_FREQ.
func (m *Modem) RXFreq() (uint32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.freq) < 5 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(m.freq[1:]), true
}

// TunedRXFreq is the receive frequency the modem is actually tuned to, in
// Hz, and false before it is programmed. SET_FREQ alone does not change it:
// see [Modem] and CIO::ifConf in MMDVM_HS.
func (m *Modem) TunedRXFreq() (uint32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tuned) < 5 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(m.tuned[1:]), true
}

// RXPower is the RF power of the last SET_FREQ.
func (m *Modem) RXPower() (byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.freq) < 10 {
		return 0, false
	}
	return m.freq[9], true
}

// errorsFor is the number of errors for the next voice burst. m.mu is held.
func (m *Modem) errorsFor() int {
	e := m.rxErr
	if e == nil || len(m.tuned) < 5 {
		return 0
	}
	rx := int64(binary.LittleEndian.Uint32(m.tuned[1:]))
	d := rx - int64(e.CenterHz)
	if d < 0 {
		d = -d
	}
	d = max(0, d-int64(e.DeadbandHz))
	n := int(e.ErrsPerKHz*float64(d)/1000 + 0.5)
	if e.Noise > 0 {
		m.rxBursts++
		// A small deterministic hash of the burst counter.
		h := (m.rxBursts*2654435761 + 12345) >> 7
		n += int(h%uint64(2*e.Noise+1)) - e.Noise
	}
	return min(max(n, 0), e.MaxErrs)
}

// corrupt flips bits in the three AMBE frames of a voice burst so that the
// receiver corrects exactly n errors, spread over the frames.
func corrupt(b *dmr.Burst, n int) {
	if n <= 0 {
		return
	}
	v := b.Voice72()
	_, base := b.Voice()
	want := base + n
	got := base
	for round := 0; got < want && round < 6; round++ {
		for k := range v {
			if got >= want {
				break
			}
			// Try bit positions in a fixed scrambled order until one adds
			// exactly one corrected error to this frame.
			_, before := dmr.DecodeAMBE(v[k])
			for j := 0; j < 72; j++ {
				i := (j*29 + k*11 + round*5) % 72
				v[k][i/8] ^= 0x80 >> (i % 8)
				if _, after := dmr.DecodeAMBE(v[k]); after == before+1 {
					got++
					break
				}
				v[k][i/8] ^= 0x80 >> (i % 8)
			}
		}
	}
	b.PutVoice72(v)
}

// applyErrors puts the model's errors in b if it is a voice burst.
func (m *Modem) applyErrors(control byte, b *dmr.Burst) {
	ft, _, err := mmdvm.ParseControl(control)
	if err != nil || (ft != dmr.FrameVoice && ft != dmr.FrameVoiceSync) {
		return
	}
	m.mu.Lock()
	n := m.errorsFor()
	m.mu.Unlock()
	corrupt(b, n)
}
