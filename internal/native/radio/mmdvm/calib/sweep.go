package calib

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// SweepOptions are the settings of [Sweep].
type SweepOptions struct {
	// Offsets are the absolute offsets to visit, in order.
	Offsets []int
	// MinCall is the shortest call that counts (default 1 s).
	MinCall time.Duration
	Out     io.Writer
	// Prompt, when set, is called after the "key up" line of each step
	// (tests use it to play a call).
	Prompt func(offset int)
	// Remind is how often to say that the sweep is still waiting
	// (default 30 s).
	Remind time.Duration
}

// Sweep visits each offset: it retunes, asks for a call of at least MinCall,
// ignores shorter calls and calls that were mostly lost, records the BER of
// the first good one and moves on when that call ends. It returns the
// points measured so far when ctx ends.
func Sweep(ctx context.Context, s *Session, o SweepOptions) ([]Point, error) {
	if o.MinCall <= 0 {
		o.MinCall = time.Second
	}
	if o.Remind <= 0 {
		o.Remind = 30 * time.Second
	}
	var pts []Point
	for i, off := range o.Offsets {
		if ctx.Err() != nil {
			break
		}
		if err := s.SetOffset(ctx, off); err != nil {
			if ctx.Err() != nil {
				break
			}
			return pts, fmt.Errorf("offset %s: %w", hz(off), err)
		}
		drain(s.T.Calls())
		fmt.Fprintf(o.Out, "[%d/%d] key up for about 3 s now (offset %s)\n", i+1, len(o.Offsets), hz(off))
		if o.Prompt != nil {
			o.Prompt(off)
		}
		p, ok := waitCall(ctx, s, o)
		if !ok {
			break
		}
		p.Offset = off
		pts = append(pts, p)
	}
	return pts, nil
}

func drain(ch <-chan CallResult) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// waitCall waits for a call that counts.
func waitCall(ctx context.Context, s *Session, o SweepOptions) (Point, bool) {
	remind := time.NewTicker(o.Remind)
	defer remind.Stop()
	for {
		select {
		case <-ctx.Done():
			return Point{}, false
		case <-remind.C:
			fmt.Fprintln(o.Out, "  still waiting for a call (Ctrl-C ends the sweep)")
		case c := <-s.T.Calls():
			if why := ignoreReason(c, o.MinCall); why != "" {
				fmt.Fprintf(o.Out, "  ignored a call: %s\n", why)
				continue
			}
			b, _ := c.BER()
			fmt.Fprintf(o.Out, "  offset %s: BER %s (%.1f s, %s)\n", hz(c.Offset), fmtPct(b), c.Duration.Seconds(), idText(c))
			return Point{BER: b, Calls: 1}, true
		}
	}
}

func ignoreReason(c CallResult, min time.Duration) string {
	switch {
	case c.Changed:
		return "the offset changed during it"
	case c.Duration < min:
		return fmt.Sprintf("%.1f s is shorter than %.1f s", c.Duration.Seconds(), min.Seconds())
	case c.LostFraction() > 0.5:
		return fmt.Sprintf("%.0f%% of its bursts were lost", c.LostFraction()*100)
	}
	if _, ok := c.BER(); !ok {
		return "it had no voice"
	}
	return ""
}

// WriteTable prints the measurements, one line per offset with a bar, the
// good range marked, and the suggestion.
func WriteTable(w io.Writer, pts []Point, s Suggestion) {
	top := 0.0
	for _, p := range pts {
		top = max(top, p.BER)
	}
	fmt.Fprintln(w, "  offset        BER")
	for _, p := range sortedPoints(pts) {
		bar := ""
		if top > 0 {
			bar = strings.Repeat("#", int(p.BER/top*30+0.5))
		}
		mark := " "
		if p.Offset >= s.Lo && p.Offset <= s.Hi {
			mark = "*"
		}
		fmt.Fprintf(w, "  %9s  %6s  %s %s\n", hz(p.Offset), fmtPct(p.BER), mark, bar)
	}
	fmt.Fprintf(w, "  lowest BER %s at %s; good range (within %s of it, marked *): %s to %s\n",
		fmtPct(s.Best.BER), hz(s.Best.Offset), fmtPct(s.Threshold-s.Best.BER), hz(s.Lo), hz(s.Hi))
}

func sortedPoints(p []Point) []Point {
	out := append([]Point(nil), p...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Offset < out[j-1].Offset; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
