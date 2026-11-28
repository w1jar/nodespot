package helper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	rfCmdline  = "console=serial0,115200 console=tty1 root=PARTUUID=1234567-02 rootfstype=ext4 fsck.repair=yes rootwait\n"
	rfConfigTX = "# For more options and information see\n[pi4]\narm_boost=1\n\n[all]\ndtparam=audio=on\n"
	rfShowGone = "LoadState=not-found\nActiveState=inactive\nUnitFileState=\n"
	rfShowOn   = "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\n"
	rfShowOff  = "LoadState=loaded\nActiveState=inactive\nUnitFileState=disabled\n"
)

// rfRig is a fake Pi: boot files, /proc/cmdline, /dev and the model.
type rfRig struct {
	dir, boot, dev, proc string
}

func newRFRig(t *testing.T, model, cmdline, config string) rfRig {
	t.Helper()
	d := t.TempDir()
	g := rfRig{dir: d, boot: filepath.Join(d, "boot"), dev: filepath.Join(d, "dev"), proc: filepath.Join(d, "cmdline")}
	for _, p := range []string{g.boot, g.dev} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(d, "model"), model+"\x00")
	write(filepath.Join(g.boot, "cmdline.txt"), cmdline)
	if config != "" {
		write(filepath.Join(g.boot, "config.txt"), config)
	}
	write(g.proc, cmdline)
	return g
}

func (g rfRig) paths() RFPaths {
	return RFPaths{BootDir: g.boot, ProcCmdline: g.proc, DevDir: g.dev, Model: filepath.Join(g.dir, "model")}
}

func (g rfRig) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g.boot, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// boot "reboots" the rig: the running command line becomes cmdline.txt's
// and serial0 points at the PL011.
func (g rfRig) reboot(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(g.proc, []byte(g.read(t, "cmdline.txt")), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(g.dev, "serial0"))
	if err := os.Symlink("ttyAMA0", filepath.Join(g.dev, "serial0")); err != nil {
		t.Fatal(err)
	}
}

func rfOps(t *testing.T, g rfRig, r map[string]resp) (*Ops, *fakeRunner) {
	o, f := osOps(t, r)
	o.RF = g.paths()
	return o, f
}

func TestRFPrepare(t *testing.T) {
	g := newRFRig(t, "Raspberry Pi 3 Model B Plus Rev 1.3", rfCmdline, rfConfigTX)
	if err := os.Symlink("ttyS0", filepath.Join(g.dev, "serial0")); err != nil {
		t.Fatal(err)
	}
	rules := map[string]resp{
		"systemctl show -p LoadState -p ActiveState -p UnitFileState serial-getty@ttyAMA0.service": {out: rfShowOn},
		"systemctl show -p LoadState -p ActiveState -p UnitFileState hciuart.service":              {out: rfShowGone},
		"systemctl disable --now serial-getty@ttyAMA0.service":                                     {},
	}
	o, f := rfOps(t, g, rules)
	res, err := o.RFPrepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := g.read(t, "cmdline.txt"); got != strings.Replace(rfCmdline, "console=serial0,115200 ", "", 1) {
		t.Errorf("cmdline.txt %q", got)
	}
	if got := g.read(t, "cmdline.txt.nodespot-orig"); got != rfCmdline {
		t.Errorf("backup %q", got)
	}
	cfg := g.read(t, "config.txt")
	if !strings.HasPrefix(cfg, rfConfigTX) || !strings.HasSuffix(cfg, "\nenable_uart=1\ndtoverlay=disable-bt\n") ||
		strings.Count(cfg, "[all]") != 1 {
		t.Errorf("config.txt %q", cfg)
	}
	if uart, bt := uartConfigState([]byte(cfg)); !uart || !bt {
		t.Errorf("config state %v %v", uart, bt)
	}
	if len(res.Changes) != 3 || !res.RebootNeeded || len(res.Reasons) != 3 || res.Model != "Raspberry Pi 3 Model B Plus Rev 1.3" {
		t.Errorf("result %+v", res)
	}
	if len(res.Units) != 2 || !res.Units[0].Present || !res.Units[0].Disabled || res.Units[1].Present || res.Units[1].Error != "" {
		t.Errorf("units %+v", res.Units)
	}
	if f.called("systemctl disable") != 1 {
		t.Errorf("calls %v", f.calls)
	}

	// Again before the reboot: nothing to change, still a reboot to do.
	rules["systemctl show -p LoadState -p ActiveState -p UnitFileState serial-getty@ttyAMA0.service"] = resp{out: rfShowOff}
	res, err = o.RFPrepare(context.Background())
	if err != nil || len(res.Changes) != 0 || !res.RebootNeeded || len(res.Reasons) != 2 {
		t.Errorf("second run %+v %v", res, err)
	}
	if f.called("systemctl disable") != 1 {
		t.Errorf("disabled again: %v", f.calls)
	}

	// After the reboot: prepared.
	g.reboot(t)
	before := g.read(t, "config.txt")
	res, err = o.RFPrepare(context.Background())
	if err != nil || len(res.Changes) != 0 || res.RebootNeeded || len(res.Reasons) != 0 {
		t.Errorf("after reboot %+v %v", res, err)
	}
	if g.read(t, "config.txt") != before {
		t.Error("config.txt rewritten")
	}
}

func TestRFPrepareNotPi(t *testing.T) {
	g := newRFRig(t, "Generic x86 board", rfCmdline, rfConfigTX)
	o, f := rfOps(t, g, map[string]resp{})
	if _, err := o.RFPrepare(context.Background()); !errors.Is(err, errNotPi) {
		t.Fatalf("err %v", err)
	}
	if g.read(t, "cmdline.txt") != rfCmdline || len(f.calls) != 0 {
		t.Error("changed a non-Pi")
	}
	// No model file at all.
	o.RF.Model = filepath.Join(g.dir, "missing")
	if _, err := o.RFPrepare(context.Background()); !errors.Is(err, errNotPi) {
		t.Fatalf("err %v", err)
	}
}

// A unit that cannot be disabled is a note, not a failure; a symlinked boot
// file is refused.
func TestRFPrepareErrors(t *testing.T) {
	g := newRFRig(t, "Raspberry Pi 5 Model B Rev 1.0", "root=/dev/mmcblk0p2 rootwait\n", "")
	o, _ := rfOps(t, g, map[string]resp{
		"systemctl show -p LoadState -p ActiveState -p UnitFileState serial-getty@ttyAMA0.service": {out: rfShowOn},
		"systemctl show -p LoadState -p ActiveState -p UnitFileState hciuart.service":              {code: 1},
		"systemctl disable --now serial-getty@ttyAMA0.service":                                     {code: 1, out: "Failed to disable unit"},
	})
	res, err := o.RFPrepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Units[0].Disabled || !strings.Contains(res.Units[0].Error, "Failed to disable") || res.Units[1].Error == "" {
		t.Errorf("units %+v", res.Units)
	}
	// No config.txt: one is created.
	if cfg := g.read(t, "config.txt"); !strings.HasSuffix(cfg, "enable_uart=1\ndtoverlay=disable-bt\n") || strings.Contains(cfg, "[all]") {
		t.Errorf("config.txt %q", cfg)
	}
	if len(res.Changes) != 1 {
		t.Errorf("changes %v", res.Changes)
	}

	p := filepath.Join(g.boot, "cmdline.txt")
	if err := os.Rename(p, p+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("cmdline.txt.real", p); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RFPrepare(context.Background()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlinked cmdline.txt: %v", err)
	}
}

func TestRemoveSerialConsole(t *testing.T) {
	for in, want := range map[string]string{
		"console=serial0,115200 console=tty1 rootwait\n": "console=tty1 rootwait\n",
		"rootwait console=ttyAMA0,115200 quiet":          "rootwait quiet\n",
		"console=tty1 console=serial1,115200 rootwait\n": "console=tty1 console=serial1,115200 rootwait\n",
		"console=serial0 rootwait\n":                     "rootwait\n",
		"rootwait\n":                                     "rootwait\n",
		"serial0,115200 xconsole=serial0,1\n":            "serial0,115200 xconsole=serial0,1\n",
	} {
		if got, _ := removeSerialConsole([]byte(in)); string(got) != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestEnsureUARTConfig(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "# nodespot: the PL011 UART for the MMDVM hat\nenable_uart=1\ndtoverlay=disable-bt\n"},
		{"enable_uart=1\ndtoverlay=disable-bt\n", "enable_uart=1\ndtoverlay=disable-bt\n"},
		{"enable_uart=1 # on\n[all]\ndtoverlay=pi3-disable-bt\n", "enable_uart=1 # on\n[all]\ndtoverlay=pi3-disable-bt\n"},
		{"enable_uart=0\ndtoverlay=disable-bt", "enable_uart=1\ndtoverlay=disable-bt"},
		{"[all]\nx=1\n[pi4]\nenable_uart=1\n",
			"[all]\nx=1\n[pi4]\nenable_uart=1\n\n[all]\n# nodespot: the PL011 UART for the MMDVM hat\nenable_uart=1\ndtoverlay=disable-bt\n"},
		{"#enable_uart=1\n[all]\ndtparam=audio=on",
			"#enable_uart=1\n[all]\ndtparam=audio=on\n# nodespot: the PL011 UART for the MMDVM hat\nenable_uart=1\ndtoverlay=disable-bt\n"},
	} {
		got, _ := ensureUARTConfig([]byte(tc.in))
		if string(got) != tc.want {
			t.Errorf("%q:\n got %q\nwant %q", tc.in, got, tc.want)
		}
		again, added := ensureUARTConfig(got)
		if string(again) != string(got) || len(added) != 0 {
			t.Errorf("%q: not idempotent (%v)", tc.in, added)
		}
	}
}

func TestRFDeviceCheck(t *testing.T) {
	g := newRFRig(t, "Raspberry Pi 4 Model B Rev 1.5", rfCmdline, rfConfigTX)
	o, _ := rfOps(t, g, map[string]resp{})
	c, err := o.RFDeviceCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Pi || c.Exists || c.UARTEnabled || c.BTDisabled || !c.ConsoleOnUART || !c.ConsoleRunning || c.RebootNeeded ||
		c.Device != "/dev/serial0" || len(c.Problems) != 2 {
		t.Errorf("unprepared %+v", c)
	}
	// Prepared but not rebooted.
	if err := os.WriteFile(filepath.Join(g.boot, "cmdline.txt"), []byte("console=tty1 rootwait\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.boot, "config.txt"), []byte("enable_uart=1\ndtoverlay=disable-bt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = o.RFDeviceCheck(context.Background())
	if !c.UARTEnabled || !c.BTDisabled || c.ConsoleOnUART || !c.ConsoleRunning || !c.RebootNeeded {
		t.Errorf("prepared %+v", c)
	}
	// serial0 pointing at a regular file (no character device here) reads
	// as missing; the link target is still reported.
	g.reboot(t)
	if err := os.WriteFile(filepath.Join(g.dev, "ttyAMA0"), nil, 0o660); err != nil {
		t.Fatal(err)
	}
	c, _ = o.RFDeviceCheck(context.Background())
	if c.LinkTarget != "ttyAMA0" || c.Exists || c.RebootNeeded || c.ConsoleRunning {
		t.Errorf("rebooted %+v", c)
	}
	// A real character device: /dev/null stands in for the UART.
	_ = os.Remove(filepath.Join(g.dev, "serial0"))
	if err := os.Symlink("/dev/null", filepath.Join(g.dev, "serial0")); err != nil {
		t.Fatal(err)
	}
	c, _ = o.RFDeviceCheck(context.Background())
	if !c.Exists || c.Owner == "" || c.Group == "" || !strings.HasPrefix(c.Mode, "Dc") || !c.NodeAccess {
		t.Errorf("char device %+v", c)
	}
}

func TestRFOverSocket(t *testing.T) {
	g := newRFRig(t, "Raspberry Pi Zero 2 W Rev 1.0", "rootwait\n", "enable_uart=1\ndtoverlay=disable-bt\n")
	g.reboot(t)
	o, _ := rfOps(t, g, map[string]resp{"systemctl show": {out: rfShowGone}})
	c, _ := startServer(t, o, PeerUID, []uint32{0, uint32(os.Getuid())})
	res, err := c.RFPrepare(context.Background())
	if err != nil || len(res.Changes) != 0 || res.RebootNeeded || len(res.Units) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	chk, err := c.RFDeviceCheck(context.Background())
	if err != nil || !chk.Pi || chk.LinkTarget != "ttyAMA0" {
		t.Fatalf("%+v %v", chk, err)
	}
	o.RF.Model = filepath.Join(g.dir, "missing")
	if _, err := c.RFPrepare(context.Background()); err == nil || !strings.Contains(err.Error(), "not a Raspberry Pi") {
		t.Fatalf("not a Pi: %v", err)
	}
}
