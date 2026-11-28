package calib_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/calib"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

type runRig struct {
	m    *fake.Modem
	svc  *fakeSvc
	cfg  string
	out  strings.Builder
	opts calib.Options
}

func newRun(t *testing.T, mut func(*node.Config)) *runRig {
	t.Helper()
	m := fake.New(fake.Options{})
	r := &runRig{m: m, svc: &fakeSvc{active: true}, cfg: writeConfig(t, mut)}
	r.opts = calib.Options{
		ConfigPath: r.cfg, Out: &safeW{w: &r.out}, In: strings.NewReader(""), Svc: r.svc,
		Euid: func() int { return 0 }, OpenWait: 5 * time.Second,
		Settings: func(ms node.MMDVMSettings) mmdvm.Settings {
			s := fastSettings(m)
			s.Port, s.Baud = ms.Device, ms.Baud
			s.Modem.ColorCode = uint8(ms.ColorCode)
			return s
		},
	}
	return r
}

func (r *runRig) run(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return calib.Run(ctx, r.opts)
}

func TestRunRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*node.Config)
		euid int
		want string
	}{
		{"not root", nil, 1000, "sudo nodespot-node calibrate"},
		{"other preset", func(c *node.Config) { c.Station.Radio.Preset = "shari" }, 0, "not mmdvm"},
		{"no rx frequency", func(c *node.Config) { c.Station.Radio.MMDVM.RXFreqHz = 0 }, 0, "no receive frequency"},
		{"banned frequency", func(c *node.Config) {
			c.Station.Radio.MMDVM.RXFreqHz, c.Station.Radio.MMDVM.TXFreqHz = 436_000_000, 436_000_000
		}, 0, "bans"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRun(t, func(cfg *node.Config) {
				if c.mut != nil {
					c.mut(cfg)
				}
			})
			euid := c.euid
			r.opts.Euid = func() int { return euid }
			if code := r.run(t); code != 1 {
				t.Errorf("exit %d", code)
			}
			if !strings.Contains(r.out.String(), c.want) {
				t.Errorf("output %q lacks %q", r.out.String(), c.want)
			}
			if got := r.svc.log(); len(got) > 1 || (len(got) == 1 && got[0] != "active") {
				t.Errorf("service touched: %v", got)
			}
			if r.m.State().Opens != 0 {
				t.Error("the modem was opened")
			}
		})
	}
}

func TestRunDeclinedLeavesServiceAlone(t *testing.T) {
	r := newRun(t, nil)
	r.opts.In = strings.NewReader("n\n")
	if code := r.run(t); code != 1 {
		t.Errorf("exit %d", code)
	}
	if got := r.svc.log(); len(got) != 1 {
		t.Errorf("service calls %v", got)
	}
	if !strings.Contains(r.out.String(), "Stop nodespot-node for calibration? [y/N]") {
		t.Errorf("output %q", r.out.String())
	}
}

// The service is stopped before the modem opens and started again only
// after the modem is closed (idle, port closed), on every way out.
func TestRunStopsAndRestartsService(t *testing.T) {
	r := newRun(t, nil)
	r.opts.In = strings.NewReader("y\nq")
	var opensAtStop, opensAtStart int
	var modeAtStart byte
	var connectedAtStart bool
	r.svc.onCall = func(n string) {
		switch n {
		case "stop":
			opensAtStop = r.m.State().Opens
		case "start":
			opensAtStart = r.m.State().Opens
			cmds := r.m.Commands()
			for i := len(cmds) - 1; i >= 0; i-- {
				if cmds[i].Cmd == mmdvm.CmdSetMode {
					modeAtStart = cmds[i].Payload[0]
					break
				}
			}
			connectedAtStart = r.m.State().Connected
		}
	}
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	if got := strings.Join(r.svc.log(), ","); got != "active,stop,start" {
		t.Errorf("service calls %s", got)
	}
	if opensAtStop != 0 || opensAtStart != 1 || modeAtStart != mmdvm.ModeIdle {
		t.Errorf("opens at stop %d, at start %d, last mode %d", opensAtStop, opensAtStart, modeAtStart)
	}
	waitFor(t, func() bool { return !r.m.State().Connected })
	_ = connectedAtStart
	noTX(t, r.m)
	if !strings.Contains(r.out.String(), "TX calibration comes with transmit support later") {
		t.Errorf("no TX note: %q", r.out.String())
	}
}

func TestRunYesSkipsPromptAndNotRunningIsLeftStopped(t *testing.T) {
	r := newRun(t, nil)
	r.svc.active = false
	r.opts.In = strings.NewReader("q")
	r.opts.Yes = true
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.Join(r.svc.log(), ","); got != "active" {
		t.Errorf("service calls %s: a stopped node must stay stopped", got)
	}
	r2 := newRun(t, nil)
	r2.opts.In = strings.NewReader("q")
	r2.opts.Yes = true
	if code := r2.run(t); code != 0 || strings.Contains(r2.out.String(), "[y/N]") {
		t.Errorf("exit %d output %q", code, r2.out.String())
	}
	if got := strings.Join(r2.svc.log(), ","); got != "active,stop,start" {
		t.Errorf("service calls %s", got)
	}
}

func TestRunRestartsAfterErrors(t *testing.T) {
	// The modem does not open.
	r := newRun(t, nil)
	r.opts.Yes = true
	r.opts.OpenWait = 150 * time.Millisecond
	r.opts.Settings = func(node.MMDVMSettings) mmdvm.Settings {
		s := fastSettings(r.m)
		s.Open = func() (io.ReadWriteCloser, error) { return nil, syscall.EBUSY }
		return s
	}
	if code := r.run(t); code != 1 {
		t.Errorf("exit %d", code)
	}
	if got := strings.Join(r.svc.log(), ","); got != "active,stop,start" {
		t.Errorf("service calls %s", got)
	}
	if !strings.Contains(r.out.String(), "did not open") {
		t.Errorf("output %q", r.out.String())
	}
	// The service cannot be stopped: nothing else happens.
	r = newRun(t, nil)
	r.opts.Yes = true
	r.svc.stopE = io.ErrClosedPipe
	if code := r.run(t); code != 1 || strings.Join(r.svc.log(), ",") != "active,stop" || r.m.State().Opens != 0 {
		t.Errorf("exit %d calls %v", code, r.svc.log())
	}
}

// SIGTERM, a hard stop, mid-session: the modem is idled and closed and the
// service started.
func TestRunHardStop(t *testing.T) {
	r := newRun(t, nil)
	r.opts.Yes = true
	pr, pw := io.Pipe()
	defer pw.Close()
	r.opts.In = pr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- calib.Run(ctx, r.opts) }()
	waitFor(t, func() bool { return strings.Contains(safeString(&r.out), "Keys:") })
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if got := strings.Join(r.svc.log(), ","); got != "active,stop,start" {
		t.Errorf("service calls %s", got)
	}
	noTX(t, r.m)
}

// A whole sweep with -yes, and the saved config: only rxOffsetHz changes,
// mode and owner stay.
func TestRunSweepSavesCenter(t *testing.T) {
	r := newRun(t, nil)
	r.m.SetRXErrors(&fake.RXErrorModel{CenterHz: baseRX + 300, ErrsPerKHz: 8})
	r.opts.Yes = true
	r.opts.Sweep, r.opts.From, r.opts.To, r.opts.Step = true, -1000, 1000, 250
	r.opts.Prompt = func(int) {
		_ = r.m.PlayCall(context.Background(), fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: manyAMBE(60), Interval: time.Millisecond})
	}
	if err := os.Chmod(r.cfg, 0o640); err != nil {
		t.Fatal(err)
	}
	owned := os.Geteuid() == 0
	if owned {
		if err := os.Chown(r.cfg, 1234, 4321); err != nil {
			t.Fatal(err)
		}
	}
	before, err := node.LoadConfig(r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	out := r.out.String()
	for _, s := range []string{"[1/9] key up for about 3 s now (offset -900 Hz)", "suggested rxOffsetHz: ", "saved rxOffsetHz: +100 Hz -> "} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	after, err := node.LoadConfig(r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := after.Station.Radio.MMDVM.RXOffsetHz
	if got < 175 || got > 425 {
		t.Errorf("saved %d, want about 300", got)
	}
	// Everything else is as it was.
	after.Station.Radio.MMDVM.RXOffsetHz = 100
	a, _ := json.Marshal(after)
	b, _ := json.Marshal(before)
	if string(a) != string(b) {
		t.Errorf("config changed beyond rxOffsetHz:\n%s\n%s", a, b)
	}
	st, _ := os.Stat(r.cfg)
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if owned {
		sys := st.Sys().(*syscall.Stat_t)
		if sys.Uid != 1234 || sys.Gid != 4321 {
			t.Errorf("owner %d:%d", sys.Uid, sys.Gid)
		}
	}
	// The sweep left the modem on the saved offset it started with, and
	// never sent a transmit frame or power.
	noTX(t, r.m)
	if got := strings.Join(r.svc.log(), ","); got != "active,stop,start" {
		t.Errorf("service calls %s", got)
	}
}

func TestRunSweepAsksBeforeSaving(t *testing.T) {
	r := newRun(t, nil)
	r.svc.active = false
	r.m.SetRXErrors(&fake.RXErrorModel{CenterHz: baseRX + 300, ErrsPerKHz: 8})
	r.opts.Sweep, r.opts.From, r.opts.To, r.opts.Step = true, -1000, 1000, 250
	r.opts.In = strings.NewReader("n\n")
	r.opts.Prompt = func(int) {
		_ = r.m.PlayCall(context.Background(), fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: manyAMBE(60), Interval: time.Millisecond})
	}
	before := readFile(t, r.cfg)
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	if !strings.Contains(r.out.String(), "Save rxOffsetHz ") || !strings.Contains(r.out.String(), "not saved.") {
		t.Errorf("output %s", r.out.String())
	}
	if readFile(t, r.cfg) != before {
		t.Error("the config changed")
	}
}

// The first Ctrl-C ends the sweep, and the points so far are still shown
// and offered.
func TestRunSweepSoftStop(t *testing.T) {
	r := newRun(t, nil)
	r.svc.active = false
	r.m.SetRXErrors(&fake.RXErrorModel{CenterHz: baseRX + 300, ErrsPerKHz: 8})
	soft, stop := context.WithCancel(context.Background())
	r.opts.Soft = soft
	r.opts.Sweep, r.opts.From, r.opts.To, r.opts.Step = true, -1000, 1000, 250
	r.opts.In = strings.NewReader("n\n")
	n := 0
	r.opts.Prompt = func(int) {
		n++
		if n == 6 {
			stop()
			return
		}
		_ = r.m.PlayCall(context.Background(), fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: manyAMBE(60), Interval: time.Millisecond})
	}
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	o := r.out.String()
	if !strings.Contains(o, "sweep ended early with 5 of 9 steps") || !strings.Contains(o, "suggested rxOffsetHz") {
		t.Errorf("output %s", o)
	}
}

func TestSaveOffset(t *testing.T) {
	p := writeConfig(t, nil)
	old, err := calib.SaveOffset(p, -250)
	if err != nil || old != 100 {
		t.Fatalf("old %d err %v", old, err)
	}
	c, err := node.LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Station.Radio.MMDVM
	if m.RXOffsetHz != -250 || m.TXOffsetHz != -50 || m.Device != "/dev/ttyAMA0" || *m.ColorCode != 3 || *m.RFPower != 40 || m.RXFreqHz != baseRX {
		t.Errorf("%+v", m)
	}
	// Out of range is refused by the node's validation and nothing changes.
	before := readFile(t, p)
	if _, err := calib.SaveOffset(p, 200_000); err == nil {
		t.Error("an offset beyond the limit was saved")
	}
	if readFile(t, p) != before {
		t.Error("the file changed")
	}
	entries, _ := os.ReadDir(strings.TrimSuffix(p, "node.json"))
	if len(entries) != 1 {
		t.Errorf("leftover files %v", entries)
	}
}
