package calib_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/calib"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

// standaloneRun is a run with no config at all: the path does not exist.
func standaloneRun(t *testing.T) *runRig {
	t.Helper()
	r := newRun(t, nil)
	r.svc.active = false
	missing := filepath.Join(t.TempDir(), "nope.json")
	r.opts.ConfigPath = missing
	r.opts.Standalone = &calib.Standalone{RXFreq: baseRX, Offset: 200, ColorCode: 1, Baud: 115200}
	r.opts.Save = func(int) (int, error) { t.Error("Save was called"); return 0, nil }
	return r
}

func TestStandaloneManualNoSave(t *testing.T) {
	r := standaloneRun(t)
	r.opts.In = strings.NewReader("+s")
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	out := r.out.String()
	for _, s := range []string{"standalone: set the RX offset to 250 Hz in Settings > Station > Radio", "color code 1", "(saved offset +200 Hz)"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if got := strings.Join(r.svc.log(), ","); got != "active" {
		t.Errorf("service calls %s", got)
	}
	if _, err := os.Stat(r.opts.ConfigPath); err == nil {
		t.Error("a config file appeared")
	}
	noTX(t, r.m)
}

func TestStandaloneSweepNoSave(t *testing.T) {
	r := standaloneRun(t)
	r.m.SetRXErrors(&fake.RXErrorModel{CenterHz: baseRX + 500, ErrsPerKHz: 8})
	r.opts.Yes = true
	r.opts.Sweep, r.opts.From, r.opts.To, r.opts.Step = true, -1000, 1000, 250
	r.opts.Prompt = func(int) {
		_ = r.m.PlayCall(context.Background(), fake.RXCall{Src: testSrc, Dst: 91, CC: 1, AMBE: manyAMBE(60), Interval: time.Millisecond})
	}
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	out := r.out.String()
	if !strings.Contains(out, "standalone: set the RX offset to ") || strings.Contains(out, "[y/N]") || strings.Contains(out, "saved rxOffsetHz") {
		t.Errorf("output:\n%s", out)
	}
	noTX(t, r.m)
}

func TestStandaloneRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		sa   calib.Standalone
		want string
	}{
		{"banned", calib.Standalone{RXFreq: 436_000_000, ColorCode: 1}, "bans"},
		{"zero", calib.Standalone{}, "-rxfreq"},
	} {
		r := standaloneRun(t)
		r.opts.Standalone = &c.sa
		if code := r.run(t); code != 1 || !strings.Contains(r.out.String(), c.want) {
			t.Errorf("%s: exit %d output %q", c.name, code, r.out.String())
		}
		if len(r.svc.log()) != 0 {
			t.Errorf("%s: service calls %v", c.name, r.svc.log())
		}
	}
	// Still root only.
	r := standaloneRun(t)
	r.opts.Euid = func() int { return 1000 }
	if code := r.run(t); code != 1 || !strings.Contains(r.out.String(), "needs root") {
		t.Errorf("exit %d %s", code, r.out.String())
	}
}

// A running node is still stopped and started in standalone mode.
func TestStandaloneStopsRunningService(t *testing.T) {
	r := standaloneRun(t)
	r.svc.active = true
	r.opts.Yes = true
	r.opts.In = strings.NewReader("q")
	if code := r.run(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.Join(r.svc.log(), ","); got != "active,stop,start" {
		t.Errorf("service calls %s", got)
	}
}

// Settings the driver gets: simplex, power 0, RX only, offset applied.
func TestStandaloneSettings(t *testing.T) {
	r := standaloneRun(t)
	var got node.MMDVMSettings
	inner := r.opts.Settings
	r.opts.Settings = func(ms node.MMDVMSettings) mmdvm.Settings { got = ms; return inner(ms) }
	r.opts.Device = ""
	r.opts.In = strings.NewReader("q")
	r.run(t)
	if got.Device != "/dev/serial0" || got.Duplex || got.RFPower != 0 || got.TXFreqHz != 0 || got.RXOffsetHz != 200 || got.Baud != 115200 {
		t.Errorf("settings %+v", got)
	}
}

// Without a unit (or without systemctl) the node is simply not running.
func TestSystemdMissingUnit(t *testing.T) {
	for _, run := range []calib.Exec{
		func(string, ...string) ([]byte, error) { return []byte("inactive\n"), &exec.ExitError{} },
		func(string, ...string) ([]byte, error) { return nil, exec.ErrNotFound },
		func(string, ...string) ([]byte, error) { return []byte("unknown\n"), &exec.ExitError{} },
	} {
		if on, err := calib.Systemd(calib.DefaultUnit, run).Active(); on || err != nil {
			t.Errorf("active %v err %v", on, err)
		}
	}
}
