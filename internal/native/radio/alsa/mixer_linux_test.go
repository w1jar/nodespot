package alsa

import "testing"

// Every hardware step survives the fraction a slider sends and the control
// reports, for the shapes real chips have.
func TestStepFractionRoundTrip(t *testing.T) {
	for _, ci := range []ControlInfo{
		{Min: 0, Max: 35, Step: 1, Count: 1},   // capture
		{Min: 0, Max: 151, Step: 1, Count: 2},  // playback
		{Min: -127, Max: 0, Step: 1, Count: 1}, // negative minimum
		{Min: 0, Max: 100, Step: 5, Count: 1},  // coarse
		{Min: 3, Max: 100, Step: 7, Count: 1},  // range not a multiple of the step
		{Min: 0, Max: 1, Step: 1, Count: 1},    // a switch-like control
	} {
		n := stepCount(ci)
		if n != int((ci.Max-ci.Min)/max(ci.Step, 1)) {
			t.Fatalf("%+v: %d steps", ci, n)
		}
		for k := 0; k <= n; k++ {
			f := float64(k) / float64(n)
			raw := fractionToRaw(ci, f)
			if raw != ci.Min+int64(k)*max(ci.Step, 1) {
				t.Errorf("%+v: step %d -> raw %d", ci, k, raw)
			}
			if got := rawToStep(ci, raw); got != k {
				t.Errorf("%+v: raw %d -> step %d, want %d", ci, raw, got, k)
			}
			// What the control reports is what the slider sends.
			if back := rawToFraction(ci, raw); fractionToRaw(ci, back) != raw || back != f {
				t.Errorf("%+v: step %d fraction %v came back %v", ci, k, f, back)
			}
		}
	}
}

func TestStepCountDegenerate(t *testing.T) {
	if stepCount(ControlInfo{Min: 5, Max: 5}) != 0 || rawToFraction(ControlInfo{Min: 5, Max: 5}, 5) != 0 {
		t.Error("a control with one position has no steps")
	}
}
