package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/calib"
)

const calibrateUsage = `usage: sudo nodespot-node calibrate [-sweep] [-from -2000 -to 2000 -step 250] [-config path] [-dev port] [-yes]
       sudo nodespot-node calibrate -rxfreq 433500000 [-txfreq Hz] [-offset Hz] [-cc 1] [-baud 115200] [-dev /dev/serial0] [-sweep ...]

Finds the receive offset (rxOffsetHz) that gives the fewest bit errors on a
DMR call from a handheld. It stops nodespot-node while it runs (the node
holds the serial port) and starts it again on the way out. It only receives:
RF power is 0 and nothing is ever transmitted.

  (default)  live display; keys + - (50 Hz), ] [ (500 Hz), 0, s, q, ?
  -sweep     step through offsets from..to around the saved one (relative,
             Hz) and suggest the center of the good range
  -yes       do not ask before stopping the node or saving a clear result

Standalone (-rxfreq given): for a Pi with no node installed. No config is
read and nothing is saved; s and the sweep end print the offset to enter in
Settings > Station > Radio. The modem is simplex on -rxfreq (-txfreq defaults
to it), starting at -offset Hz. The service is only stopped if it is running.
`

// runCalibrate is "nodespot-node calibrate".
func runCalibrate(args []string, in io.Reader, out, stderr io.Writer) int {
	fs := flag.NewFlagSet("nodespot-node calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, calibrateUsage); fs.PrintDefaults() }
	cfgPath := fs.String("config", node.DefaultConfigPath, "config file")
	dev := fs.String("dev", "", "serial port of the modem (default: the config's, or /dev/serial0 standalone)")
	rxFreq := fs.Uint64("rxfreq", 0, "standalone: receive frequency in Hz; no config is read, nothing is saved")
	txFreq := fs.Uint64("txfreq", 0, "standalone: transmit frequency in Hz (default: -rxfreq)")
	offset := fs.Int("offset", 0, "standalone: starting RX offset in Hz")
	cc := fs.Int("cc", 1, "standalone: DMR color code")
	baud := fs.Int("baud", 115200, "standalone: serial baud rate")
	sweep := fs.Bool("sweep", false, "sweep a range of offsets instead of the live display")
	from := fs.Int("from", -2000, "sweep start, Hz relative to the saved offset")
	to := fs.Int("to", 2000, "sweep end, Hz relative to the saved offset")
	step := fs.Int("step", 250, "sweep step, Hz")
	yes := fs.Bool("yes", false, "do not ask before stopping the node or saving")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	var sa *calib.Standalone
	if *rxFreq != 0 {
		if *rxFreq > math.MaxUint32 || *txFreq > math.MaxUint32 {
			fmt.Fprintln(stderr, "-rxfreq and -txfreq must be in Hz and fit in 32 bits")
			return 2
		}
		if *cc < 0 || *cc > 15 {
			fmt.Fprintln(stderr, "-cc must be 0 to 15")
			return 2
		}
		sa = &calib.Standalone{RXFreq: uint32(*rxFreq), TXFreq: uint32(*txFreq), Offset: *offset, ColorCode: *cc, Baud: *baud}
	} else {
		for _, n := range []string{"txfreq", "offset", "cc", "baud"} {
			set := false
			fs.Visit(func(f *flag.Flag) { set = set || f.Name == n })
			if set {
				fmt.Fprintf(stderr, "-%s needs -rxfreq (standalone mode)\n", n)
				return 2
			}
		}
	}

	// The first Ctrl-C (SIGINT) ends a sweep early; a second one, or
	// SIGTERM or SIGHUP, is a hard stop. Both leave through Run, which
	// starts the node again.
	hard, stopHard := context.WithCancel(context.Background())
	defer stopHard()
	soft, stopSoft := context.WithCancel(hard)
	defer stopSoft()
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	go func() {
		first := true
		for s := range sig {
			if s == syscall.SIGINT && first {
				first = false
				stopSoft()
				continue
			}
			stopHard()
		}
	}()

	log := slog.New(slog.DiscardHandler) // the display says what matters; log lines would break it
	return calib.Run(hard, calib.Options{
		ConfigPath: *cfgPath, Device: *dev, Standalone: sa,
		Sweep: *sweep, From: *from, To: *to, Step: *step, Yes: *yes,
		In: in, Out: out,
		Svc:  calib.Systemd(calib.DefaultUnit, nil),
		Soft: soft,
		Raw: func() (func(), error) {
			fd := int(os.Stdin.Fd())
			old, err := term.MakeRaw(fd)
			if err != nil {
				return nil, err
			}
			return func() { _ = term.Restore(fd, old) }, nil
		},
		Settings: func(m node.MMDVMSettings) mmdvm.Settings { return modemSettings(m, log) },
	})
}
