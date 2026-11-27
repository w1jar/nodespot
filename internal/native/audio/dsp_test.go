package audio

import (
	"math"
	"testing"
)

// tone fills frames with a sine of amplitude amp (fraction of full scale) and
// returns the RMS of the output after the filter has settled, relative to the
// input RMS, in dB.
func measure(hz, amp float64, process func(*Frame)) float64 {
	const frames = 60
	var inSum, outSum float64
	n := 0
	for fr := 0; fr < frames; fr++ {
		var f Frame
		for i := range f {
			f[i] = int16(math.Round(amp * 32767 * math.Sin(2*math.Pi*hz*float64(n+i)/SampleRate)))
		}
		in := f
		process(&f)
		if fr >= frames/2 {
			for i := range f {
				inSum += float64(in[i]) * float64(in[i])
				outSum += float64(f[i]) * float64(f[i])
			}
		}
		n += FrameSamples
	}
	return 10 * math.Log10(outSum/inSum)
}

func closeTo(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.2f dB, want %.2f +/- %.2f", what, got, want, tol)
	}
}

func TestGainExact(t *testing.T) {
	for _, db := range []float64{-20, -6.0206, 0, 6.0206, 12, 20} {
		var f Frame
		for i := range f {
			f[i] = int16(i*20 - 1500) // peak 1700, +20 dB stays under the knee
		}
		in := f
		if db > 12 {
			for i := range f {
				f[i] /= 10
			}
			in = f
		}
		ApplyGain(&f, db)
		g := math.Pow(10, db/20)
		for i := range f {
			want := math.Round(float64(in[i]) * g)
			if math.Abs(float64(f[i])-want) > 1 {
				t.Fatalf("%.2f dB: sample %d = %d, want %v", db, i, f[i], want)
			}
		}
	}
	var z, o Frame
	o[3] = 1234
	z = o
	ApplyGain(&z, 0)
	if z != o {
		t.Error("0 dB changed the frame")
	}
}

func TestGainClipSafe(t *testing.T) {
	var f Frame
	for i := range f {
		if i%2 == 0 {
			f[i] = 32767
		} else {
			f[i] = -32768
		}
	}
	ApplyGain(&f, 20)
	for i, s := range f {
		if (i%2 == 0 && s <= 0) || (i%2 == 1 && s >= 0) {
			t.Fatalf("sample %d wrapped: %d", i, s)
		}
	}
}

func TestPLFilter(t *testing.T) {
	// Analytic response of the coefficients.
	h := NewPLFilter()
	if g := h.Response(100, SampleRate); g > -20 {
		t.Errorf("analytic 100 Hz = %.1f dB", g)
	}
	closeTo(t, "analytic corner", h.Response(PLCornerHz, SampleRate), -3.01, 0.1)
	// And on real samples.
	at := func(hz float64) float64 {
		h := NewPLFilter()
		return measure(hz, 0.3, h.Process)
	}
	if g := at(100); g > -20 {
		t.Errorf("100 Hz = %.1f dB, want at most -20", g)
	}
	if g := at(67); g > -30 {
		t.Errorf("67 Hz = %.1f dB, want at most -30", g)
	}
	closeTo(t, "1 kHz", at(1000), 0, 1)
	closeTo(t, "2 kHz", at(2000), 0, 1)
	closeTo(t, "500 Hz", at(500), 0, 1)
}

func TestDeemphasisSlope(t *testing.T) {
	// Against the analog 1/(1+s tau), normalized to 0 dB at 1 kHz, over the
	// voice band at 8 kHz.
	fc := 1 / (2 * math.Pi * EmphasisTau)
	analog := func(f float64) float64 {
		return -10*math.Log10(1+(f/fc)*(f/fc)) + 10*math.Log10(1+(1000/fc)*(1000/fc))
	}
	d := NewDeemphasis(SampleRate)
	closeTo(t, "1 kHz", d.Response(1000, SampleRate), 0, 0.01)
	for _, f := range []float64{300, 500, 2000, 2500, 3000} {
		closeTo(t, "8 kHz analog match", d.Response(f, SampleRate)-analog(f), 0, 1.0)
	}
	if d.Response(3000, SampleRate) >= d.Response(2000, SampleRate) {
		t.Error("response does not fall above the corner")
	}
	// At a high sample rate the full slope is visible: 6 dB per octave
	// above the 2.1 kHz corner (as the analog filter approaches it).
	const hi = 48000
	d48 := NewDeemphasis(hi)
	closeTo(t, "48k 1 kHz", d48.Response(1000, hi), 0, 0.01)
	closeTo(t, "48k slope 8.5k to 17k", d48.Response(8488, hi)-d48.Response(16976, hi), 6, 0.6)
	closeTo(t, "48k slope 4.2k to 8.5k", d48.Response(4244, hi)-d48.Response(8488, hi), 5.3, 0.8)
	// On samples.
	e := NewDeemphasis(SampleRate)
	closeTo(t, "measured 1 kHz", measure(1000, 0.3, e.Process), 0, 0.5)
	e = NewDeemphasis(SampleRate)
	closeTo(t, "measured 3 kHz", measure(3000, 0.3, e.Process), analog(3000), 1.0)
}

func TestPreemphasisIsInverse(t *testing.T) {
	de, pre := NewDeemphasis(SampleRate), NewPreemphasis(SampleRate)
	closeTo(t, "pre 1 kHz", pre.Response(1000, SampleRate), 0, 0.01)
	for _, f := range []float64{100, 300, 600, 1000, 2000, 2500, 3000} {
		closeTo(t, "pre x de", pre.Response(f, SampleRate)+de.Response(f, SampleRate), 0, 0.01)
	}
	if pre.Response(3000, SampleRate) < 3.5 {
		t.Errorf("pre-emphasis at 3 kHz = %.1f dB, want a lift", pre.Response(3000, SampleRate))
	}
	// Chained on samples, flat in the voice band.
	for _, f := range []float64{400, 1000, 2500} {
		p, d := NewPreemphasis(SampleRate), NewDeemphasis(SampleRate)
		closeTo(t, "round trip", measure(f, 0.1, func(fr *Frame) { p.Process(fr); d.Process(fr) }), 0, 0.7)
	}
	// Stable: bounded output on a long full-scale Nyquist-rate input.
	p := NewPreemphasis(SampleRate)
	var f Frame
	for fr := 0; fr < 100; fr++ {
		for i := range f {
			f[i] = 20000 * int16(1-2*(i%2))
		}
		p.Process(&f)
	}
}

func TestDSPNoAlloc(t *testing.T) {
	var f Frame
	for i := range f {
		f[i] = int16(i * 50)
	}
	pl, de, pre := NewPLFilter(), NewDeemphasisNode(), NewPreemphasisNode()
	n := testing.AllocsPerRun(100, func() {
		ApplyGain(&f, 3)
		pl.Process(&f)
		de.Process(&f)
		pre.Process(&f)
	})
	if n != 0 {
		t.Errorf("%v allocations per frame", n)
	}
}
