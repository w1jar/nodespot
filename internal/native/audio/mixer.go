package audio

import (
	"fmt"
	"sync"
)

// inputDepth is how many pushed frames an input queues before dropping the
// oldest (a little jitter absorption).
const inputDepth = 4

// Mixer mixes N inputs into N outputs. Each output hears every input except
// the one with its own name (no echo to the sender) and except any input it
// excludes. Inputs are pushed from any goroutine; the node calls Mix once per
// frame tick.
type Mixer struct {
	mu      sync.Mutex
	inputs  []*Input
	outputs []*outputCfg
}

// Input is one source feeding the mixer.
type Input struct {
	m      *Mixer
	name   string
	gain   float64 // linear
	queue  []Frame
	active bool // had a frame at the last Mix
	hang   int  // frames to stay active after the last frame
	left   int
}

type outputCfg struct {
	name    string
	gain    float64
	exclude map[string]bool
}

// NewMixer returns an empty mixer.
func NewMixer() *Mixer { return &Mixer{} }

// AddInput registers an input. Its name pairs it with the output of the same
// name, which will not hear it. activeHang is how many frames the input stays
// "active" after its last frame (smooths gaps between frames).
func (m *Mixer) AddInput(name string, gainDb float64, activeHang int) (*Input, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.inputs {
		if in.name == name {
			return nil, fmt.Errorf("audio: mixer input %q already exists", name)
		}
	}
	in := &Input{m: m, name: name, gain: dbToLin(gainDb), hang: activeHang}
	m.inputs = append(m.inputs, in)
	return in, nil
}

// AddOutput registers an output. It never hears the input of the same name,
// nor any input listed in exclude (for example the node keeps its ID
// input on the RF output only by excluding them elsewhere).
func (m *Mixer) AddOutput(name string, gainDb float64, exclude ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.outputs {
		if o.name == name {
			return fmt.Errorf("audio: mixer output %q already exists", name)
		}
	}
	ex := map[string]bool{}
	for _, e := range exclude {
		ex[e] = true
	}
	m.outputs = append(m.outputs, &outputCfg{name: name, gain: dbToLin(gainDb), exclude: ex})
	return nil
}

// Push queues a frame for the next Mix.
func (in *Input) Push(f Frame) {
	in.m.mu.Lock()
	defer in.m.mu.Unlock()
	if len(in.queue) >= inputDepth {
		in.queue = append(in.queue[:0], in.queue[1:]...)
	}
	in.queue = append(in.queue, f)
}

// SetGainDb changes the input's gain.
func (in *Input) SetGainDb(db float64) {
	in.m.mu.Lock()
	in.gain = dbToLin(db)
	in.m.mu.Unlock()
}

// Active reports whether the input had audio at the last Mix (or within its
// hang time), so the node can tell who is talking.
func (in *Input) Active() bool {
	in.m.mu.Lock()
	defer in.m.mu.Unlock()
	return in.active
}

// SetOutputGainDb changes an output's gain; it reports false for an unknown name.
func (m *Mixer) SetOutputGainDb(name string, db float64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.outputs {
		if o.name == name {
			o.gain = dbToLin(db)
			return true
		}
	}
	return false
}

// ActiveInputs lists the names of the inputs that are active.
func (m *Mixer) ActiveInputs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, in := range m.inputs {
		if in.active {
			out = append(out, in.name)
		}
	}
	return out
}

// Mix consumes one frame from every input that has one and returns the frame
// for each output, by output name. Sums are soft clipped.
func (m *Mixer) Mix() map[string]Frame {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := make([]*Frame, len(m.inputs))
	for i, in := range m.inputs {
		if len(in.queue) > 0 {
			f := in.queue[0]
			in.queue = append(in.queue[:0], in.queue[1:]...)
			cur[i] = &f
			in.left = in.hang
			in.active = true
		} else if in.left > 0 {
			in.left--
			in.active = true
		} else {
			in.active = false
		}
	}
	res := make(map[string]Frame, len(m.outputs))
	for _, o := range m.outputs {
		var acc [FrameSamples]float64
		for i, in := range m.inputs {
			if cur[i] == nil || in.name == o.name || o.exclude[in.name] {
				continue
			}
			g := in.gain / 32768
			for j, s := range cur[i] {
				acc[j] += float64(s) * g
			}
		}
		var f Frame
		for j, v := range acc {
			f[j] = toInt16(softClip(v * o.gain))
		}
		res[o.name] = f
	}
	return res
}
