package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The terminal's login runs in its own transient systemd unit
// (nodespot-term-<id>.service), not as a child of the helper. This file holds
// the parts that do not touch a pseudo terminal, so they build and are tested
// on every platform: the unit name and the systemd-run command line, the
// validation of what goes into them, stopping a unit, finding its processes,
// and the sweep of units left behind by an earlier helper.

const (
	termUnitPrefix = "nodespot-term-"
	termUnitSuffix = ".service"
	// terminalStopTimeout bounds systemctl stop (the unit's own
	// TimeoutStopSec=5s turns SIGTERM into SIGKILL before that).
	terminalStopTimeout = 20 * time.Second
	terminalStartLimit  = 30 * time.Second
	// terminalPollEvery is how often the end of the unit is checked.
	terminalPollEvery = time.Second
)

var (
	termIDRe   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	termUnitRe = regexp.MustCompile(`^nodespot-term-[0-9a-f]{16}\.service$`)
)

// newTermID returns a random 16 digit hex id.
func newTermID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// termUnit is the unit name of id; id must be valid.
func termUnit(id string) string { return termUnitPrefix + id + termUnitSuffix }

// termUnitArgv is the systemd-run command line that starts term-host as the
// main process of the session's own unit. An argument vector, no shell; every
// value is validated. The unit has no sandbox properties: it is a plain login
// session, outside the helper's cgroup and mount namespace. term-host sets
// the fixed environment of login itself, so nothing is passed with --setenv.
//
// There are no TTYPath or Utmp* properties: term-host opens the PTY and
// starts login on it, and login(1) records the session in utmp and wtmp
// itself (see docs/DESIGN.md), so `who` and `last` keep working.
//
// --service-type=exec makes systemd-run return once term-host has been
// exec'd. The default KillMode (control-group) makes "systemctl stop" end
// every process of the session, and cleans up whatever is left when term-host
// exits; TimeoutStopSec bounds the SIGTERM grace.
func termUnitArgv(id, self string, rows, cols uint16, owner int64) ([]string, error) {
	if !termIDRe.MatchString(id) {
		return nil, fmt.Errorf("invalid terminal id %q", id)
	}
	if !strings.HasPrefix(self, "/") || strings.ContainsAny(self, " \t\n=") {
		return nil, fmt.Errorf("invalid helper path %q", self)
	}
	if !ValidSize(rows, cols) {
		return nil, errors.New("invalid size")
	}
	if owner < 0 {
		return nil, errors.New("invalid owner")
	}
	return []string{
		"systemd-run", "--quiet",
		"--unit=" + termUnitPrefix + id,
		"--description=nodespot web terminal",
		"--service-type=exec", "--collect",
		"--property=TimeoutStopSec=5s",
		"--", self, "term-host",
		"--id=" + id,
		"--sock=" + filepath.Join(termDir, id+".sock"),
		"--rows=" + strconv.Itoa(int(rows)),
		"--cols=" + strconv.Itoa(int(cols)),
		"--owner=" + strconv.FormatInt(owner, 10),
	}, nil
}

// stopTermUnit stops the unit; systemd kills its whole cgroup. A unit that is
// already gone is not an error.
func stopTermUnit(r Runner, id string) error {
	if !termIDRe.MatchString(id) {
		return fmt.Errorf("invalid terminal id %q", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminalStopTimeout)
	defer cancel()
	out, code, err := r.Run(ctx, "systemctl", "stop", termUnit(id))
	if err != nil {
		return fmt.Errorf("systemctl stop: %w", err)
	}
	if code != 0 && !strings.Contains(out, "not loaded") {
		return fmt.Errorf("systemctl stop failed: %s", LastLines(out, 3))
	}
	return nil
}

// termUnitEnded says whether the unit is over, from
// `systemctl show -p ActiveState --value`: inactive or failed. A unit that
// does not exist (it was collected) reads as inactive.
func termUnitEnded(r Runner, id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, code, err := r.Run(ctx, "systemctl", "show", "-p", "ActiveState", "--value", termUnit(id))
	if err != nil || code != 0 {
		return false // cannot tell; the master's EOF also ends the session
	}
	switch strings.TrimSpace(out) {
	case "inactive", "failed":
		return true
	}
	return false
}

// cgroupRoots are where a system service's cgroup directory lives (unified
// hierarchy first, then the v1 systemd hierarchy). A variable for tests.
var cgroupRoots = []string{"/sys/fs/cgroup/system.slice", "/sys/fs/cgroup/systemd/system.slice"}

// procRoot is /proc; a variable for tests.
var procRoot = "/proc"

// termUnitPIDs lists the processes of the unit's cgroup, nil when it has none
// or does not exist.
func termUnitPIDs(unit string) []int {
	if !termUnitRe.MatchString(unit) {
		return nil
	}
	for _, root := range cgroupRoots {
		b, err := os.ReadFile(filepath.Join(root, unit, "cgroup.procs"))
		if err != nil {
			continue
		}
		var pids []int
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil && n > 0 {
				pids = append(pids, n)
			}
		}
		return pids
	}
	return nil
}

// packageManagerComms are the process names (as in /proc/<pid>/comm, cut to
// 15 characters) that make a leftover unit busy: an apt or dpkg run is not
// cut off by the sweep.
var packageManagerComms = map[string]bool{
	"apt": true, "apt-get": true, "aptitude": true, "dpkg": true,
	"apt.systemd.dai": true, "unattended-upgr": true,
}

func termUnitBusy(unit string) bool {
	for _, pid := range termUnitPIDs(unit) {
		b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
		if err == nil && packageManagerComms[strings.TrimSpace(string(b))] {
			return true
		}
	}
	return false
}

// listTermUnits returns the nodespot-term-* service units systemd knows about.
func listTermUnits(ctx context.Context, r Runner) ([]string, error) {
	out, code, err := r.Run(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "--type=service", "nodespot-term-*")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("systemctl list-units failed: %s", LastLines(out, 3))
	}
	var units []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && termUnitRe.MatchString(f[0]) {
			units = append(units, f[0])
		}
	}
	return units, nil
}

// isConnRefused reports ECONNREFUSED from a dial.
func isConnRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
