package helper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The MMDVM hat's serial port (docs/MMDVM.md, Pi setup). rf-prepare frees
// the Pi's PL011 UART for the hat: no serial console on it, Bluetooth moved
// off it, the getty and Bluetooth UART service stopped. rf-device-check
// reports how the port looks now. Neither takes an argument: the files,
// units and device are fixed.
const (
	OpRFPrepare     = "rf-prepare"
	OpRFDeviceCheck = "rf-device-check"
)

// RFPaths are the files rf-prepare and rf-device-check read and edit; the
// zero value means the defaults. Injectable for tests.
type RFPaths struct {
	BootDir     string // default /boot/firmware (cmdline.txt, config.txt)
	ProcCmdline string // default /proc/cmdline: the running kernel's command line
	DevDir      string // default /dev
	Model       string // default /proc/device-tree/model
}

func (p RFPaths) boot() string    { return orDefault(p.BootDir, "/boot/firmware") }
func (p RFPaths) cmdline() string { return orDefault(p.ProcCmdline, "/proc/cmdline") }
func (p RFPaths) dev() string     { return orDefault(p.DevDir, "/dev") }
func (p RFPaths) model() string   { return orDefault(p.Model, "/proc/device-tree/model") }

// The UART the hat is on and the services that hold it on Raspberry Pi OS.
// hciuart is absent on newer releases (Debian 13); a missing unit is fine.
const (
	rfSerialLink = "serial0"
	rfUART       = "ttyAMA0"
)

var rfUnits = []string{"serial-getty@ttyAMA0.service", "hciuart.service"}

// maxBootFile bounds cmdline.txt and config.txt.
const maxBootFile = 64 << 10

func init() {
	registerSysOp(OpRFPrepare, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.RFPrepare(ctx)
	})
	registerSysOp(OpRFDeviceCheck, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.RFDeviceCheck(ctx)
	})
}

// RFUnit is what rf-prepare found and did for one service.
type RFUnit struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	// Active and Enabled are the state before rf-prepare; Disabled is true
	// when rf-prepare stopped and disabled it.
	Active   bool   `json:"active"`
	Enabled  bool   `json:"enabled"`
	Disabled bool   `json:"disabled"`
	Error    string `json:"error,omitempty"`
}

// RFPrepareResult is the result of rf-prepare.
type RFPrepareResult struct {
	Model string `json:"model"`
	// Changes says what was changed, in plain words; empty when the Pi
	// was already prepared.
	Changes []string `json:"changes"`
	Units   []RFUnit `json:"units"`
	// RebootNeeded is true until the Pi runs with the new boot settings;
	// Reasons say why.
	RebootNeeded bool     `json:"rebootNeeded"`
	Reasons      []string `json:"reasons,omitempty"`
}

// RFDeviceCheck is the result of rf-device-check.
type RFDeviceCheck struct {
	Model string `json:"model"`
	Pi    bool   `json:"pi"`
	// Device is the port the hat is opened on (/dev/serial0); LinkTarget
	// what it points to ("ttyAMA0" when the PL011 is free for the hat).
	Device     string `json:"device"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Exists     bool   `json:"exists"`
	// Owner, Group and Mode are the device node's ("crw-rw----").
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
	Mode  string `json:"mode,omitempty"`
	// NodeAccess is true when the node daemon's group can read and write
	// the device.
	NodeAccess bool `json:"nodeAccess"`
	// From the boot files and the running system.
	UARTEnabled    bool `json:"uartEnabled"`
	BTDisabled     bool `json:"btDisabled"`
	ConsoleOnUART  bool `json:"consoleOnUart"`  // cmdline.txt still has a serial console
	ConsoleRunning bool `json:"consoleRunning"` // the running kernel has one
	// RebootNeeded is true when the boot files are right but the running
	// system does not match them yet.
	RebootNeeded bool     `json:"rebootNeeded"`
	Problems     []string `json:"problems,omitempty"`
}

// errNotPi refuses rf-prepare on other hardware.
var errNotPi = errors.New("this is not a Raspberry Pi; rf-prepare edits the Raspberry Pi boot files only")

func (o *Ops) piModel() (string, bool) {
	b, _, err := readRegular(o.RF.model(), 4096)
	if err != nil {
		return "", false
	}
	m := strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
	return m, strings.HasPrefix(m, "Raspberry Pi")
}

// RFPrepare edits cmdline.txt and config.txt and disables the units that
// hold the UART. Running it again changes nothing.
func (o *Ops) RFPrepare(ctx context.Context) (RFPrepareResult, error) {
	res := RFPrepareResult{Changes: []string{}, Units: []RFUnit{}}
	model, pi := o.piModel()
	res.Model = model
	if !pi {
		return res, errNotPi
	}
	boot := o.RF.boot()
	cmdPath, cfgPath := filepath.Join(boot, "cmdline.txt"), filepath.Join(boot, "config.txt")

	cmd, cmdMode, err := readBootFile(cmdPath)
	if err != nil {
		return res, err
	}
	edited := false
	if out, removed := removeSerialConsole(cmd); len(removed) > 0 {
		edited = true
		if err := writeBootFile(cmdPath, out, cmdMode); err != nil {
			return res, fmt.Errorf("write cmdline.txt: %w", err)
		}
		res.Changes = append(res.Changes, "Removed the serial console ("+strings.Join(removed, " ")+") from cmdline.txt")
	}

	cfg, cfgMode, err := readBootFile(cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		cfg, cfgMode, err = nil, 0o644, nil
	}
	if err != nil {
		return res, err
	}
	if out, added := ensureUARTConfig(cfg); len(added) > 0 {
		edited = true
		if err := writeBootFile(cfgPath, out, cfgMode); err != nil {
			return res, fmt.Errorf("write config.txt: %w", err)
		}
		res.Changes = append(res.Changes, "Set "+strings.Join(added, " and ")+" under [all] in config.txt")
	}

	for _, u := range rfUnits {
		ru := o.rfDisableUnit(ctx, u)
		if ru.Disabled {
			res.Changes = append(res.Changes, "Stopped and disabled "+u)
		}
		res.Units = append(res.Units, ru)
	}

	res.Reasons = o.rfRebootReasons(edited)
	res.RebootNeeded = len(res.Reasons) > 0
	return res, nil
}

// rfDisableUnit stops and disables unit when it is present and running or
// enabled. A missing unit is reported as not present.
func (o *Ops) rfDisableUnit(ctx context.Context, unit string) RFUnit {
	ru := RFUnit{Name: unit}
	out, code, err := o.Run.Run(ctx, "systemctl", "show", "-p", "LoadState", "-p", "ActiveState", "-p", "UnitFileState", unit)
	if err != nil || code != 0 {
		ru.Error = "systemctl show failed"
		return ru
	}
	kv := parseKV(out)
	if ls := kv["LoadState"]; ls == "" || ls == "not-found" {
		return ru
	}
	ru.Present = true
	ru.Active = kv["ActiveState"] == "active" || kv["ActiveState"] == "activating"
	switch kv["UnitFileState"] {
	case "enabled", "enabled-runtime", "indirect", "generated":
		ru.Enabled = true
	}
	if !ru.Active && !ru.Enabled {
		return ru
	}
	out, code, err = o.Run.Run(ctx, "systemctl", "disable", "--now", unit)
	if err != nil || code != 0 {
		ru.Error = "systemctl disable failed: " + LastLines(out, 2)
		return ru
	}
	ru.Disabled = true
	return ru
}

// rfRebootReasons says why the running system does not match the boot
// files yet. edited is true when rf-prepare just changed a boot file.
func (o *Ops) rfRebootReasons(edited bool) []string {
	var why []string
	if edited {
		why = append(why, "The boot settings changed")
	}
	if b, _, err := readRegular(o.RF.cmdline(), maxBootFile); err == nil {
		if _, removed := removeSerialConsole(b); len(removed) > 0 {
			why = append(why, "The running system still has a serial console on the UART")
		}
	}
	if t, _ := os.Readlink(filepath.Join(o.RF.dev(), rfSerialLink)); t != rfUART && filepath.Base(t) != rfUART {
		why = append(why, "/dev/serial0 does not point at "+rfUART+" yet")
	}
	return why
}

// RFDeviceCheck reports the hat's serial port.
func (o *Ops) RFDeviceCheck(context.Context) (RFDeviceCheck, error) {
	var c RFDeviceCheck
	c.Model, c.Pi = o.piModel()
	dev := o.RF.dev()
	c.Device = "/dev/" + rfSerialLink
	link := filepath.Join(dev, rfSerialLink)
	if t, err := os.Readlink(link); err == nil {
		c.LinkTarget = t
	}
	if fi, err := os.Stat(link); err == nil {
		c.Exists = fi.Mode()&fs.ModeCharDevice != 0
		c.Mode = fi.Mode().String()
		if uid, gid, ok := ownerIDs(fi); ok {
			c.Owner, c.Group = userName(uid), groupName(gid)
			perm := fi.Mode().Perm()
			c.NodeAccess = perm&0o006 == 0o006 || (c.Group == o.nodeUser() && perm&0o060 == 0o060)
		}
	}
	boot := o.RF.boot()
	if b, _, err := readBootFile(filepath.Join(boot, "config.txt")); err == nil {
		c.UARTEnabled, c.BTDisabled = uartConfigState(b)
	}
	if b, _, err := readBootFile(filepath.Join(boot, "cmdline.txt")); err == nil {
		_, removed := removeSerialConsole(b)
		c.ConsoleOnUART = len(removed) > 0
	}
	if b, _, err := readRegular(o.RF.cmdline(), maxBootFile); err == nil {
		_, removed := removeSerialConsole(b)
		c.ConsoleRunning = len(removed) > 0
	}
	switch {
	case !c.Pi:
		c.Problems = append(c.Problems, "This is not a Raspberry Pi")
	case !c.UARTEnabled || !c.BTDisabled || c.ConsoleOnUART:
		c.Problems = append(c.Problems, "The Pi is not prepared for the hat yet (Prepare the Pi)")
	}
	ready := c.Pi && c.UARTEnabled && c.BTDisabled && !c.ConsoleOnUART
	running := filepath.Base(c.LinkTarget) == rfUART && !c.ConsoleRunning
	c.RebootNeeded = ready && !running
	if c.RebootNeeded {
		c.Problems = append(c.Problems, "Reboot to apply the boot settings")
	}
	if !c.Exists {
		c.Problems = append(c.Problems, "/dev/serial0 does not exist")
	} else if !c.NodeAccess {
		c.Problems = append(c.Problems, "The node service cannot open "+c.Device+" (group "+c.Group+", "+c.Mode+")")
	}
	return c, nil
}

func userName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func groupName(gid int) string {
	if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
		return g.Name
	}
	return strconv.Itoa(gid)
}

// isSerialConsole reports a console= argument on the hat's UART.
func isSerialConsole(arg string) bool {
	v, ok := strings.CutPrefix(arg, "console=")
	if !ok {
		return false
	}
	dev, _, _ := strings.Cut(v, ",")
	return dev == rfSerialLink || dev == rfUART
}

// removeSerialConsole drops console=serial0,<baud> (and console=ttyAMA0)
// from a kernel command line, keeping everything else in order. removed
// lists what was dropped; out is b unchanged when nothing was.
func removeSerialConsole(b []byte) (out []byte, removed []string) {
	line, rest, _ := bytes.Cut(b, []byte("\n"))
	var kept []string
	for _, f := range strings.Fields(string(line)) {
		if isSerialConsole(f) {
			removed = append(removed, f)
			continue
		}
		kept = append(kept, f)
	}
	if len(removed) == 0 {
		return b, nil
	}
	out = []byte(strings.Join(kept, " ") + "\n")
	if len(bytes.TrimSpace(rest)) > 0 { // cmdline.txt is one line; keep anything after it
		out = append(out, rest...)
	}
	return out, removed
}

// configLine is an active config.txt setting: "key=value", with comments
// and surrounding space removed.
func configLine(l string) string {
	if i := strings.IndexByte(l, '#'); i >= 0 {
		l = l[:i]
	}
	return strings.TrimSpace(l)
}

func isSection(l string) bool {
	return strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]")
}

// uartConfigState reports whether enable_uart=1 and the disable-bt overlay
// are set where they apply to every Pi: before any section or under [all].
func uartConfigState(b []byte) (uart, bt bool) {
	all := true
	for _, raw := range strings.Split(string(b), "\n") {
		l := configLine(raw)
		switch {
		case isSection(l):
			all = l == "[all]"
		case !all:
		case l == "enable_uart=1":
			uart = true
		case l == "enable_uart=0":
			uart = false
		case l == "dtoverlay=disable-bt" || l == "dtoverlay=pi3-disable-bt":
			bt = true
		}
	}
	return uart, bt
}

// ensureUARTConfig sets enable_uart=1 and dtoverlay=disable-bt under [all].
// An enable_uart=0 under [all] is changed in place; missing lines go at the
// end, after an [all] line unless the file already ends in the [all]
// section. added names what was set; out is b unchanged when nothing was.
func ensureUARTConfig(b []byte) (out []byte, added []string) {
	uart, bt := uartConfigState(b)
	if uart && bt {
		return b, nil
	}
	lines := strings.Split(string(b), "\n")
	if !uart {
		all := true
		for i, raw := range lines {
			l := configLine(raw)
			if isSection(l) {
				all = l == "[all]"
			} else if all && l == "enable_uart=0" {
				lines[i] = "enable_uart=1"
				uart = true
				added = append(added, "enable_uart=1")
			}
		}
	}
	var tail []string
	if !uart {
		tail = append(tail, "enable_uart=1")
		added = append(added, "enable_uart=1")
	}
	if !bt {
		tail = append(tail, "dtoverlay=disable-bt")
		added = append(added, "dtoverlay=disable-bt")
	}
	text := strings.Join(lines, "\n")
	if len(tail) > 0 {
		last := "[all]"
		for _, raw := range lines {
			if l := configLine(raw); isSection(l) {
				last = l
			}
		}
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if last != "[all]" {
			text += "\n[all]\n"
		}
		text += "# nodespot: the PL011 UART for the MMDVM hat\n" + strings.Join(tail, "\n") + "\n"
	}
	return []byte(text), added
}

// readBootFile reads a boot file (a regular file, not a symlink, bounded).
func readBootFile(path string) ([]byte, fs.FileMode, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file", path)
	}
	b, _, err := readRegular(path, maxBootFile)
	if err != nil {
		return nil, 0, err
	}
	return b, fi.Mode().Perm(), nil
}

// writeBootFile replaces path: a temp file in the same directory, fsync,
// rename. The boot partition is FAT, which has no owners or modes, so a
// failed chmod is not an error. The first edit keeps a copy of the
// original as <name>.nodespot-orig.
func writeBootFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	orig := path + ".nodespot-orig"
	if _, err := os.Lstat(orig); errors.Is(err, fs.ErrNotExist) {
		if old, _, err := readRegular(path, maxBootFile); err == nil {
			if err := writeBootTemp(dir, orig, old, mode); err != nil {
				return err
			}
		}
	}
	return writeBootTemp(dir, path, data, mode)
}

func writeBootTemp(dir, path string, data []byte, mode fs.FileMode) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".nodespot-tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if mode != 0 {
		_ = f.Chmod(mode)
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// RFPrepare calls rf-prepare.
func (c *Client) RFPrepare(ctx context.Context) (RFPrepareResult, error) {
	var v RFPrepareResult
	return v, c.sys(ctx, OpRFPrepare, nil, &v, 1<<20)
}

// RFDeviceCheck calls rf-device-check.
func (c *Client) RFDeviceCheck(ctx context.Context) (RFDeviceCheck, error) {
	var v RFDeviceCheck
	return v, c.sys(ctx, OpRFDeviceCheck, nil, &v, 1<<20)
}

// RFPrepare simulates rf-prepare: the demo Pi is already prepared.
func (d *DemoSystem) RFPrepare(context.Context) (RFPrepareResult, error) {
	return RFPrepareResult{Model: "Raspberry Pi 3 Model B Plus Rev 1.3", Changes: []string{},
		Units: []RFUnit{{Name: rfUnits[0]}, {Name: rfUnits[1]}}}, nil
}

// RFDeviceCheck simulates rf-device-check on a prepared Pi.
func (d *DemoSystem) RFDeviceCheck(context.Context) (RFDeviceCheck, error) {
	return RFDeviceCheck{Model: "Raspberry Pi 3 Model B Plus Rev 1.3", Pi: true, Device: "/dev/serial0",
		LinkTarget: rfUART, Exists: true, Owner: "root", Group: DefaultNodeUser, Mode: "Dcrw-rw----",
		NodeAccess: true, UARTEnabled: true, BTDisabled: true}, nil
}
