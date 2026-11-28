package calib

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Key steps of manual mode, in Hz.
const (
	FineStep   = 50
	CoarseStep = 500
)

// ManualOptions are the settings of [Manual].
type ManualOptions struct {
	// In delivers the keys, one byte at a time; Out gets the display. Lines
	// end in CR LF so they look right on a terminal in raw mode.
	In  io.Reader
	Out io.Writer
	// Saved is the offset in the config file now.
	Saved int
	// Save writes an offset to the config and returns the one it replaced.
	Save func(off int) (old int, err error)
	// Refresh is how often the status line is redrawn (default 250 ms).
	Refresh time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

const helpText = "Keys:  +  -  change the offset by 50 Hz    ]  [  by 500 Hz    0  back to the saved offset\r\n" +
	"       s  save it to the config    l  list the last calls    ?  this help    q  quit\r\n" +
	"Key up on a handheld near the hat (a few seconds at a time) and watch the BER: the lower the better.\r\n"

// Manual shows the live BER and lets the keys move the offset, until q,
// Ctrl-C, the end of the input or ctx, and returns the offset that is saved
// in the config by then. The status line is redrawn in place;
// finished calls are printed above it.
func Manual(ctx context.Context, s *Session, o ManualOptions) (saved int, err error) {
	if o.Refresh <= 0 {
		o.Refresh = 250 * time.Millisecond
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &manual{s: s, o: o, saved: o.Saved}
	keys := make(chan byte)
	go func() {
		defer close(keys)
		buf := make([]byte, 1)
		for {
			n, err := o.In.Read(buf)
			if n > 0 {
				select {
				case keys <- buf[0]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	m.printf("%s", helpText)
	m.status()
	tick := time.NewTicker(o.Refresh)
	defer tick.Stop()
	esc := 0
	for {
		select {
		case <-ctx.Done():
			m.clear()
			return m.saved, nil
		case <-tick.C:
			m.status()
		case c := <-s.T.Calls():
			m.call(c)
		case k, ok := <-keys:
			if !ok {
				m.clear()
				return m.saved, nil
			}
			switch {
			case esc == 1 && k == '[':
				esc = 2
				continue
			case esc == 2: // an arrow key: up and down fine, right and left coarse
				esc = 0
				switch k {
				case 'A':
					k = '+'
				case 'B':
					k = '-'
				case 'C':
					k = ']'
				case 'D':
					k = '['
				default:
					continue
				}
			case k == 0x1b:
				esc = 1
				continue
			default:
				esc = 0
			}
			if m.key(ctx, k) {
				m.clear()
				return m.saved, nil
			}
			m.status()
		}
	}
}

type manual struct {
	s     *Session
	o     ManualOptions
	saved int
}

func (m *manual) printf(f string, a ...any) { fmt.Fprintf(m.o.Out, f, a...) }

// clear wipes the status line.
func (m *manual) clear() { m.printf("\r\x1b[K") }

// line prints a line above the status line.
func (m *manual) line(f string, a ...any) {
	m.clear()
	m.printf(f+"\r\n", a...)
}

// key handles one key and reports whether to quit.
func (m *manual) key(ctx context.Context, k byte) bool {
	cur := m.s.Offset()
	step := func(d int) {
		to := cur + d
		if err := m.s.SetOffset(ctx, to); err != nil {
			m.line("cannot use %s: %v", hz(to), err)
		}
	}
	switch k {
	case '+', '=':
		step(FineStep)
	case '-', '_':
		step(-FineStep)
	case ']':
		step(CoarseStep)
	case '[':
		step(-CoarseStep)
	case '0':
		if err := m.s.SetOffset(ctx, m.saved); err != nil {
			m.line("cannot use %s: %v", hz(m.saved), err)
		}
	case 's', 'S':
		if m.o.Save == nil {
			m.line("saving is not available")
			break
		}
		old, err := m.o.Save(cur)
		if err != nil {
			m.line("%v", err)
			break
		}
		m.saved = cur
		m.line("saved rxOffsetHz: %s -> %s", hz(old), hz(cur))
	case 'l', 'L':
		h := m.s.T.History()
		if len(h) == 0 {
			m.line("no calls yet")
		}
		for _, c := range h[max(0, len(h)-5):] {
			m.line("%s", callLine(c))
		}
	case '?', 'h', 'H':
		m.clear()
		m.printf("%s", helpText)
	case 'q', 'Q', 0x03, 0x04:
		return true
	}
	return false
}

func (m *manual) call(c CallResult) {
	m.line("%s", callLine(c))
	m.status()
}

func callLine(c CallResult) string {
	extra := ""
	switch {
	case c.Lost:
		extra = "  (ended by DMR_LOST)"
	case c.Changed:
		extra = "  (offset changed during the call)"
	}
	return fmt.Sprintf("%s  %-18s %5.1f s  offset %-9s BER %s%s",
		clock(c.Start), idText(c), c.Duration.Seconds(), hz(c.Offset), berText(c), extra)
}

// status redraws the status line.
func (m *manual) status() {
	now := m.o.Now()
	sn := m.s.T.Snapshot(now)
	var b strings.Builder
	fmt.Fprintf(&b, "offset %s (saved %s) rx %.6f MHz | frames %d lost %d | ",
		hz(sn.Offset), hz(m.saved), float64(m.s.freq(sn.Offset).RX)/1e6, sn.Frames, sn.Lost)
	switch {
	case sn.Live != nil:
		ber := "-"
		if v, ok := sn.Live.BER(); ok {
			ber = fmtPct(v)
		}
		roll := "-"
		if sn.RollingOK {
			roll = fmtPct(sn.Rolling)
		}
		fmt.Fprintf(&b, "RX %s call BER %s, 1 s BER %s", idText(*sn.Live), ber, roll)
	case sn.Last != nil:
		fmt.Fprintf(&b, "idle, last call BER %s at %s", berText(*sn.Last), hz(sn.Last.Offset))
	default:
		b.WriteString("idle, no call yet")
	}
	m.clear()
	m.printf("%s", b.String())
}
