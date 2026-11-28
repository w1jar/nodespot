package calib

import (
	"errors"
	"fmt"
	"sort"
)

// Point is the BER measured at one RX offset (absolute, as saved in
// rxOffsetHz).
type Point struct {
	Offset int
	BER    float64
	Calls  int
}

// Plan lists the offsets of a sweep: saved+from to saved+to in steps of
// step, in order. from and to are relative to the saved offset, so a sweep
// always looks around where the board already is. An offset the node would
// refuse is left out; ok tells whether one is. The plan always includes the
// saved offset itself when it falls on a step.
func Plan(saved, from, to, step int, ok func(offset int) error) (offsets []int, skipped []string, err error) {
	if step <= 0 {
		return nil, nil, errors.New("the step must be above 0 Hz")
	}
	if from > to {
		return nil, nil, errors.New("-from must not be above -to")
	}
	if (to-from)/step > 200 {
		return nil, nil, errors.New("that is more than 200 steps; use a larger -step or a narrower range")
	}
	for r := from; r <= to; r += step {
		off := saved + r
		if ok != nil {
			if e := ok(off); e != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", hz(off), e))
				continue
			}
		}
		offsets = append(offsets, off)
	}
	if len(offsets) == 0 {
		return nil, skipped, errors.New("no offset in that range is allowed")
	}
	return offsets, skipped, nil
}

// Margin is how far above the lowest BER a point may be and still count as
// good: 1 percentage point (0.01) or a quarter of the lowest, whichever is
// more. One call is a few thousand bits, so its BER is only known to a few
// tenths of a percent; the margin keeps noise from picking one lucky step.
func Margin(min float64) float64 { return max(0.01, 0.25*min) }

// Suggestion is the result of [Analyze].
type Suggestion struct {
	// Best is the point with the lowest BER.
	Best Point
	// Lo and Hi are the first and last offset of the contiguous run of
	// points, around Best, whose BER is within Margin of the lowest.
	Lo, Hi int
	// Center is the middle of Lo and Hi: the offset to save.
	Center int
	// Threshold is the BER limit used.
	Threshold float64
	// Edge is true when the good range touches the end of the sweep, so
	// the real center may lie outside it.
	Edge bool
	// Flat is true when every point is good: the sweep shows no
	// preference, and Center is just the middle of the sweep.
	Flat bool
}

// Analyze finds the center of the good range. Points are the measured
// offsets; they are sorted by offset first. The good range is the run of
// neighbouring points around the lowest one whose BER is within [Margin] of
// it, and the suggestion is the middle of that run, not the single lowest
// step: a crystal error shows as a wide, flat-bottomed valley, and its
// middle is the safest place.
func Analyze(points []Point) (Suggestion, error) {
	if len(points) == 0 {
		return Suggestion{}, errors.New("no measurements")
	}
	p := append([]Point(nil), points...)
	sort.Slice(p, func(i, j int) bool { return p[i].Offset < p[j].Offset })
	best := 0
	for i := range p {
		if p[i].BER < p[best].BER { // the first lowest wins a tie
			best = i
		}
	}
	s := Suggestion{Best: p[best], Threshold: p[best].BER + Margin(p[best].BER)}
	lo, hi := best, best
	for lo > 0 && p[lo-1].BER <= s.Threshold {
		lo--
	}
	for hi < len(p)-1 && p[hi+1].BER <= s.Threshold {
		hi++
	}
	s.Lo, s.Hi = p[lo].Offset, p[hi].Offset
	s.Center = roundDiv(s.Lo+s.Hi, 2)
	s.Edge = len(p) > 1 && (lo == 0 || hi == len(p)-1)
	s.Flat = lo == 0 && hi == len(p)-1 && len(p) > 2
	return s, nil
}

// roundDiv divides a by n, rounding half away from zero.
func roundDiv(a, n int) int {
	if a < 0 {
		return -((-a + n/2) / n)
	}
	return (a + n/2) / n
}
