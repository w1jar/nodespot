package calib

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Service is the node's systemd unit, which holds the serial port.
type Service interface {
	// Active reports whether the unit is running (or starting).
	Active() (bool, error)
	Stop() error
	Start() error
}

// Exec runs a command and returns its combined output.
type Exec func(name string, args ...string) ([]byte, error)

// RealExec runs the command for real.
func RealExec(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// DefaultUnit is the node's unit name.
const DefaultUnit = "nodespot-node.service"

// Systemd controls a unit through systemctl; run is how it is executed (nil
// runs systemctl for real, tests pass their own).
func Systemd(unit string, run Exec) Service {
	if run == nil {
		run = RealExec
	}
	return systemd{unit, run}
}

type systemd struct {
	unit string
	run  Exec
}

func (s systemd) Active() (bool, error) {
	out, err := s.run("systemctl", "is-active", s.unit)
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil // no systemd, so no unit to stop
	}
	state := strings.TrimSpace(string(out))
	switch state {
	case "active", "activating", "reloading", "deactivating":
		return true, nil
	case "inactive", "failed", "unknown", "dead":
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot ask systemctl about %s: %w", s.unit, err)
	}
	return false, nil
}

func (s systemd) Stop() error  { return s.do("stop") }
func (s systemd) Start() error { return s.do("start") }

func (s systemd) do(verb string) error {
	out, err := s.run("systemctl", verb, s.unit)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New("systemctl " + verb + " " + s.unit + ": " + msg)
	}
	return nil
}
