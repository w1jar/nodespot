package calib

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// Options are everything "nodespot-node calibrate" needs; the command fills
// them from its flags and tests fill them with fakes.
type Options struct {
	ConfigPath string
	// Device overrides the serial port of the config.
	Device string
	Sweep  bool
	// From, To and Step are the sweep, relative to the saved offset.
	From, To, Step int
	// Yes skips the questions: the service is stopped without asking and a
	// sweep result is saved when it is clear.
	Yes bool

	In  io.Reader
	Out io.Writer
	// Svc is the node's service (never nil).
	Svc Service
	// Euid is the effective user ID (default os.Geteuid).
	Euid func() int
	// Raw puts the terminal in raw mode for manual mode and returns the
	// undo function; nil, or an error, runs without (keys need Enter).
	Raw func() (restore func(), err error)
	// Settings builds the driver settings from the node's (RXOnly is
	// forced on by the session).
	Settings func(m node.MMDVMSettings) mmdvm.Settings
	// OpenWait is how long to wait for the modem to open (default 20 s).
	OpenWait time.Duration
	// Soft ends the sweep early (the first Ctrl-C) while the rest, saving
	// and the service restart, still runs. Optional.
	Soft context.Context
	// Save writes the offset (default [SaveOffset] on ConfigPath).
	Save func(off int) (int, error)
	// MinCall is the shortest call a sweep step takes (default 1 s).
	MinCall time.Duration
	// Prompt is called at each sweep step (tests).
	Prompt func(offset int)
	// Standalone runs without a node config: the modem settings come from
	// here, ConfigPath is never read, and nothing is saved.
	Standalone *Standalone
}

// Standalone is a modem described on the command line, for a Pi without a
// node installed. The setup is simplex and receive only.
type Standalone struct {
	// RXFreq is the receive frequency in Hz; TXFreq defaults to it.
	RXFreq, TXFreq uint32
	// Offset is the starting RX offset in Hz.
	Offset    int
	ColorCode int
	Baud      int
}

// standaloneNote says what to do with a result instead of saving it.
func standaloneNote(off int) string {
	return fmt.Sprintf("standalone: set the RX offset to %d Hz in Settings > Station > Radio", off)
}

// errStandalone is what saving returns in standalone mode: its text is the
// note, which manual mode prints as it does any save error.
type errStandalone int

func (e errStandalone) Error() string { return standaloneNote(int(e)) }

// settings returns the modem settings, from the config or standalone.
func (o Options) settings() (node.MMDVMSettings, string) {
	if sa := o.Standalone; sa != nil {
		ms := (*node.MMDVMConfig)(nil).Resolved()
		ms.Duplex = false
		ms.RXFreqHz, ms.TXFreqHz, ms.RXOffsetHz = sa.RXFreq, sa.TXFreq, sa.Offset
		if sa.Baud > 0 {
			ms.Baud = sa.Baud
		}
		if sa.ColorCode >= 0 {
			ms.ColorCode = sa.ColorCode
		}
		if o.Device != "" {
			ms.Device = o.Device
		}
		if ms.RXFreqHz == 0 {
			return ms, "standalone needs a receive frequency (-rxfreq)."
		}
		return ms, ""
	}
	cfg, err := node.LoadConfig(o.ConfigPath)
	if err != nil {
		return node.MMDVMSettings{}, err.Error()
	}
	if radio.Preset(cfg.Station.Radio.Preset) != radio.PresetMMDVM {
		return node.MMDVMSettings{}, fmt.Sprintf("the radio preset in %s is not mmdvm, so there is no modem to calibrate.\n"+
			"Pick the MMDVM preset in Settings > Station > Radio first.", o.ConfigPath)
	}
	ms := cfg.Station.Radio.MMDVM.Resolved()
	if o.Device != "" {
		ms.Device = o.Device
	}
	if ms.RXFreqHz == 0 {
		return ms, "no receive frequency is set: enter one under Settings > Station > Radio, then run this again."
	}
	return ms, ""
}

// Run runs the calibration and returns the exit code: 0 done, 1 refused or
// failed. The node's service is stopped for it if it runs and, once stopped
// by us, started again on every way out. ctx ending is a hard stop.
func Run(ctx context.Context, o Options) (code int) {
	out := o.Out
	say := func(f string, a ...any) { fmt.Fprintf(out, f+"\n", a...) }
	fail := func(f string, a ...any) int { say(f, a...); return 1 }
	in := bufio.NewReader(o.In)
	if o.Euid == nil {
		o.Euid = os.Geteuid
	}
	if o.Euid() != 0 {
		return fail("calibrate needs root: it stops the node service and opens the serial port.\n" +
			"Run it as: sudo nodespot-node calibrate")
	}

	ms, msg := o.settings()
	if msg != "" {
		return fail("%s", msg)
	}
	if err := ms.CheckFreq(); err != nil {
		return fail("the modem would refuse this frequency: %v", err)
	}
	saved := ms.RXOffsetHz

	if o.Standalone != nil {
		o.Save = func(off int) (int, error) { return saved, errStandalone(off) }
	} else if o.Save == nil {
		o.Save = func(off int) (int, error) { return SaveOffset(o.ConfigPath, off) }
	}
	if o.Settings == nil {
		o.Settings = func(node.MMDVMSettings) mmdvm.Settings { return mmdvm.Settings{} }
	}

	active, err := o.Svc.Active()
	if err != nil {
		return fail("%v", err)
	}
	if active {
		say("nodespot-node is running and holds the serial port (%s).", ms.Device)
		if !o.Yes {
			fmt.Fprint(out, "Stop nodespot-node for calibration? [y/N] ")
			ictx, cancel := either(ctx, o.Soft)
			ok := yes(ictx, in)
			cancel()
			if !ok {
				return fail("not stopped; nothing was changed.")
			}
		}
		say("stopping nodespot-node ...")
		if err := o.Svc.Stop(); err != nil {
			return fail("%v", err)
		}
		defer func() {
			say("starting nodespot-node again ...")
			if err := o.Svc.Start(); err != nil {
				say("%v\nStart it yourself: sudo systemctl start %s", err, DefaultUnit)
				code = 1
			}
		}()
	} else {
		say("nodespot-node is not running; the serial port is free.")
	}

	freq := func(off int) mmdvm.Freq {
		c := ms
		c.RXOffsetHz = off
		rx, tx, _ := c.ModemFreq()
		return mmdvm.Freq{RX: rx, TX: tx}
	}
	s := NewSession(o.Settings(ms), freq, ms.Rules(), saved)
	wait := o.OpenWait
	if wait <= 0 {
		wait = 20 * time.Second
	}
	say("opening the modem on %s at %d baud (receive only, RF power 0, never transmits) ...", ms.Device, ms.Baud)
	if err := s.Start(ctx, wait); err != nil {
		s.Close()
		return fail("%v\nCheck the device (-dev), that nothing else uses the port, and that the hat is seated.", err)
	}
	defer s.Close()
	st := s.Driver().Status()
	say("modem: %s", st.Description)
	say("rx %.6f MHz (saved offset %s), color code %d, simplex slot 2.", float64(freq(saved).RX)/1e6, hz(saved), ms.ColorCode)
	say("TX calibration comes with transmit support later; this command only measures reception.")

	if o.Sweep {
		return runSweep(ctx, o, s, ms, in, saved)
	}
	var restore func()
	if o.Raw != nil {
		var err error
		if restore, err = o.Raw(); err != nil {
			say("not a terminal (%v): press Enter after each key.", err)
			restore = nil
		}
	}
	mctx, cancel := either(ctx, o.Soft)
	defer cancel()
	now, err := Manual(mctx, s, ManualOptions{In: in, Out: out, Saved: saved, Save: o.Save})
	if restore != nil {
		restore()
	}
	fmt.Fprint(out, "\r\n")
	if err != nil {
		return fail("%v", err)
	}
	if off := s.Offset(); o.Standalone != nil {
		say("%s", standaloneNote(off))
	} else if off != now {
		say("offset %s was not saved (the config has %s).", hz(off), hz(now))
	}
	return 0
}

func runSweep(ctx context.Context, o Options, s *Session, ms node.MMDVMSettings, in *bufio.Reader, saved int) int {
	out := o.Out
	say := func(f string, a ...any) { fmt.Fprintf(out, f+"\n", a...) }
	offs, skipped, err := Plan(saved, o.From, o.To, o.Step, s.Check)
	for _, k := range skipped {
		say("skipping %s", k)
	}
	if err != nil {
		say("%v", err)
		return 1
	}
	say("sweeping the offset from %s to %s in %d Hz steps around the saved %s (%d steps).",
		hz(offs[0]), hz(offs[len(offs)-1]), o.Step, hz(saved), len(offs))
	say("At each step key up on a handheld for about 3 s (a call of at least 1 s counts). Ctrl-C ends the sweep early.")
	sctx, cancel := either(ctx, o.Soft)
	defer cancel()
	pts, err := Sweep(sctx, s, SweepOptions{Offsets: offs, MinCall: o.MinCall, Out: out, Prompt: o.Prompt})
	if err != nil {
		say("%v", err)
		return 1
	}
	if ctx.Err() != nil {
		say("stopped.")
		return 1
	}
	if sctx.Err() != nil {
		say("sweep ended early with %d of %d steps.", len(pts), len(offs))
	}
	if len(pts) == 0 {
		say("no measurements; nothing to suggest.")
		return 1
	}
	// Leave the modem on the saved offset while the user decides.
	_ = s.SetOffset(ctx, saved)
	sg, _ := Analyze(pts)
	say("")
	WriteTable(out, pts, sg)
	switch {
	case len(pts) < 3:
		say("too few steps for a suggestion.")
		return 0
	case sg.Flat:
		say("the BER hardly changes over this range, so the offset does not matter here (or no signal was strong enough to tell).")
		say("Nothing to save. Try a wider range (-from/-to) or a weaker signal, such as a handheld farther away.")
		return 0
	}
	say("suggested rxOffsetHz: %s (the middle of %s to %s; now %s)", hz(sg.Center), hz(sg.Lo), hz(sg.Hi), hz(saved))
	if sg.Edge {
		say("note: the good range reaches the end of the sweep, so the best offset may lie beyond it; widen -from/-to to check.")
	}
	if o.Standalone != nil {
		say("%s", standaloneNote(sg.Center))
		return 0
	}
	if sg.Center == saved {
		say("that is the offset already saved.")
		return 0
	}
	if o.Yes {
		if sg.Edge {
			say("not saved automatically because of the note above.")
			return 0
		}
	} else {
		fmt.Fprintf(out, "Save rxOffsetHz %s? [y/N] ", hz(sg.Center))
		if !yes(ctx, in) {
			say("not saved.")
			return 0
		}
	}
	old, err := o.Save(sg.Center)
	if err != nil {
		say("%v", err)
		return 1
	}
	say("saved rxOffsetHz: %s -> %s (the node picks it up when it starts)", hz(old), hz(sg.Center))
	return 0
}

// yes reads a line and reports whether it starts with y. A hard stop, or the
// end of the input, is a no.
func yes(ctx context.Context, in *bufio.Reader) bool {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := in.ReadString('\n')
		ch <- res{s, err}
	}()
	select {
	case <-ctx.Done():
		return false
	case r := <-ch:
		a := strings.ToLower(strings.TrimSpace(r.s))
		return a == "y" || a == "yes"
	}
}

// either is a context that ends when a or b does (b may be nil).
func either(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	if b != nil {
		go func() {
			select {
			case <-b.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
