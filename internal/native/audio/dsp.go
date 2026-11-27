package audio

import "math"

// Station audio conditioning: static gain, the PL (CTCSS) high-pass filter and
// the 75 microsecond pre-emphasis and de-emphasis. The filters are designed for
// any sample rate (the node runs them at SampleRate, 8 kHz). Processing is in
// place on a Frame, uses fixed-size state and allocates nothing.

// Limits of the static gain, in dB, each direction.
const (
	MinStaticGainDb = -20
	MaxStaticGainDb = 20
)

// Emphasis time constant (75 microseconds, corner 2122 Hz) and the PL filter
// corner.
const (
	EmphasisTau = 75e-6
	PLCornerHz  = 300.0
	// levelRefHz is where pre- and de-emphasis are normalized to 0 dB.
	levelRefHz = 1000.0
	// emphasisMatchFrac is the frequency, as a fraction of the sample rate,
	// where the digital emphasis is made to equal the analog one.
	emphasisMatchFrac = 0.375
)

// ApplyGain multiplies f by db decibels in place. 0 dB leaves the samples
// untouched. Peaks that would pass the knee of the soft clipper bend smoothly
// towards full scale instead of wrapping or hard clipping.
func ApplyGain(f *Frame, db float64) {
	if db == 0 {
		return
	}
	g := dbToLin(db)
	for i, s := range f {
		f[i] = toInt16(softClip(float64(s) / 32768 * g))
	}
}

// biquad is one second-order section in transposed direct form II.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	z1, z2             float64
}

func (q *biquad) step(x float64) float64 {
	y := q.b0*x + q.z1
	q.z1 = q.b1*x - q.a1*y + q.z2
	q.z2 = q.b2*x - q.a2*y
	return y
}

// highpass returns the RBJ high-pass biquad at fc with quality factor qf.
func highpass(fc, qf, fs float64) biquad {
	w := 2 * math.Pi * fc / fs
	cw, sw := math.Cos(w), math.Sin(w)
	alpha := sw / (2 * qf)
	a0 := 1 + alpha
	return biquad{
		b0: (1 + cw) / 2 / a0, b1: -(1 + cw) / a0, b2: (1 + cw) / 2 / a0,
		a1: -2 * cw / a0, a2: (1 - alpha) / a0,
	}
}

// HighPass is the PL filter: a 4th-order Butterworth high-pass (two biquads),
// which removes the sub-audible CTCSS tones (67 to 250 Hz) from received
// audio. It is not safe for concurrent use.
type HighPass struct{ s [2]biquad }

// NewHighPass returns a 4th-order Butterworth high-pass at fc Hz for sample
// rate fs.
func NewHighPass(fc, fs float64) *HighPass {
	// Butterworth Q values of a 4th-order filter: 1 / (2 cos(pi/8)) and
	// 1 / (2 cos(3 pi/8)).
	return &HighPass{s: [2]biquad{
		highpass(fc, 1/(2*math.Cos(math.Pi/8)), fs),
		highpass(fc, 1/(2*math.Cos(3*math.Pi/8)), fs),
	}}
}

// NewPLFilter returns the PL filter for the node's sample rate.
func NewPLFilter() *HighPass { return NewHighPass(PLCornerHz, SampleRate) }

// Reset clears the filter's memory.
func (h *HighPass) Reset() {
	for i := range h.s {
		h.s[i].z1, h.s[i].z2 = 0, 0
	}
}

// Process filters f in place.
func (h *HighPass) Process(f *Frame) {
	for i, s := range f {
		v := float64(s) / 32768
		v = h.s[0].step(v)
		v = h.s[1].step(v)
		f[i] = toInt16(softClip(v))
	}
}

// Response returns the filter's gain in dB at hz for sample rate fs.
func (h *HighPass) Response(hz, fs float64) float64 {
	return linToDb(biquadMag(h.s[0], hz, fs) * biquadMag(h.s[1], hz, fs))
}

func biquadMag(q biquad, hz, fs float64) float64 {
	w := 2 * math.Pi * hz / fs
	c1, s1 := math.Cos(w), math.Sin(w)
	c2, s2 := math.Cos(2*w), math.Sin(2*w)
	nr := q.b0 + q.b1*c1 + q.b2*c2
	ni := -(q.b1*s1 + q.b2*s2)
	dr := 1 + q.a1*c1 + q.a2*c2
	di := -(q.a1*s1 + q.a2*s2)
	return math.Hypot(nr, ni) / math.Hypot(dr, di)
}

// Emphasis is a first-order section: (g (b0 + b1 z^-1)) / (1 + a1 z^-1).
// It is a de-emphasis (low-pass shelf at 2122 Hz) or the matching
// pre-emphasis (high-frequency lift), both at 0 dB at 1 kHz. It is not safe
// for concurrent use.
type Emphasis struct {
	b0, b1, a1 float64
	z          float64
}

// firstOrder builds H(z) = g (n0 + n1 z^-1) / (d0 + d1 z^-1), then scales it so
// the gain at levelRefHz is exactly 1.
func firstOrder(n0, n1, d0, d1, fs float64) *Emphasis {
	e := &Emphasis{b0: n0 / d0, b1: n1 / d0, a1: d1 / d0}
	if m := e.mag(levelRefHz, fs); m > 0 {
		e.b0 /= m
		e.b1 /= m
	}
	return e
}

// emphasisDesign returns the pole p = exp(-1/(fs tau)) of the analog 75
// microsecond low-pass (matched pole) and the position -rho of a zero that makes
// the digital gain follow the analog one at emphasisMatchFrac of fs. A matched
// pole alone leaves too little droop near Nyquist and the bilinear transform
// too much; this zero keeps the response within a fraction of a dB of the
// analog filter across the voice band at 8 kHz, and nearly exactly at higher
// rates.
func emphasisDesign(fs float64) (p, rho float64) {
	p = math.Exp(-1 / (fs * EmphasisTau))
	fc := 1 / (2 * math.Pi * EmphasisTau)
	an := func(f float64) float64 { return 1 / (1 + (f/fc)*(f/fc)) } // power gain
	fm := emphasisMatchFrac * fs
	want := an(fm) / an(levelRefHz)
	r := func(rho, f float64) float64 {
		c := math.Cos(2 * math.Pi * f / fs)
		return (1 + rho*rho + 2*rho*c) / (1 + p*p - 2*p*c)
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 60; i++ {
		mid := (lo + hi) / 2
		if r(mid, fm)/r(mid, levelRefHz) > want {
			lo = mid
		} else {
			hi = mid
		}
	}
	return p, (lo + hi) / 2
}

// NewDeemphasis returns the 75 microsecond de-emphasis for sample rate fs
// (corner 2122 Hz, 6 dB per octave above it, as far as the sample rate
// allows), with 0 dB at 1 kHz.
func NewDeemphasis(fs float64) *Emphasis {
	p, rho := emphasisDesign(fs)
	return firstOrder(1, rho, 1, -p, fs)
}

// NewPreemphasis returns the 75 microsecond pre-emphasis for fs: the exact
// inverse of [NewDeemphasis] (stable: its pole is the de-emphasis zero), with
// 0 dB at 1 kHz.
func NewPreemphasis(fs float64) *Emphasis {
	p, rho := emphasisDesign(fs)
	return firstOrder(1, -p, 1, rho, fs)
}

// NewDeemphasisNode is [NewDeemphasis] at the node's sample rate.
func NewDeemphasisNode() *Emphasis { return NewDeemphasis(SampleRate) }

// NewPreemphasisNode is [NewPreemphasis] at the node's sample rate.
func NewPreemphasisNode() *Emphasis { return NewPreemphasis(SampleRate) }

// Reset clears the filter's memory.
func (e *Emphasis) Reset() { e.z = 0 }

// Process filters f in place.
func (e *Emphasis) Process(f *Frame) {
	for i, s := range f {
		x := float64(s) / 32768
		y := e.b0*x + e.z
		e.z = e.b1*x - e.a1*y
		f[i] = toInt16(softClip(y))
	}
}

func (e *Emphasis) mag(hz, fs float64) float64 {
	w := 2 * math.Pi * hz / fs
	nr := e.b0 + e.b1*math.Cos(w)
	ni := -e.b1 * math.Sin(w)
	dr := 1 + e.a1*math.Cos(w)
	di := -e.a1 * math.Sin(w)
	return math.Hypot(nr, ni) / math.Hypot(dr, di)
}

// Response returns the filter's gain in dB at hz for sample rate fs.
func (e *Emphasis) Response(hz, fs float64) float64 { return linToDb(e.mag(hz, fs)) }
