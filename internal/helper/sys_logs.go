package helper

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Journal viewer and service list of the System tab (plan 5.2). Read only.
const (
	OpLogsRead       = "logs-read"       // SysRequest.Unit, Lines, Since
	OpServicesStatus = "services-status" // fixed unit list
)

func init() {
	registerSysOp(OpLogsRead, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		return o.LogsRead(ctx, r.Unit, r.Lines, r.Since)
	})
	registerSysOp(OpServicesStatus, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.ServicesStatus(ctx), nil
	})
}

// Journal line limits and the output cap (bytes, the tail is kept).
const (
	LogLinesMin     = 50
	LogLinesMax     = 1000
	LogLinesDefault = 200
	logMaxBytes     = 128 << 10
)

// logUnits is the allowlist of the journal viewer: no unit name comes from a
// client except to select one of these. md380-emu is the AMBE vocoder the
// DMR, YSF, P25 and NXDN modes use.
func logUnits() []string {
	return []string{nodeService, "nodespot", "nodespot-helper", "md380-emu", "NetworkManager", "systemd-networkd", "nodespot-wifi-watchdog"}
}

// LogUnits returns the units the journal viewer accepts.
func LogUnits() []string { return logUnits() }

// serviceUnits are the units of the service list: the journal allowlist plus
// the web services an older install may still run (see the services
// switch) and the time and firewall services.
func serviceUnits() []string {
	return append(logUnits(), "cockpit.socket", "apache2", "allmon3", "firewalld", "chrony", "systemd-timesyncd")
}

// ValidLogUnit reports whether unit may be read.
func ValidLogUnit(unit string) bool {
	for _, u := range logUnits() {
		if u == unit {
			return true
		}
	}
	return false
}

// logSince maps the fixed choices to journalctl arguments.
var logSince = map[string][]string{
	"":     nil,
	"1h":   {"--since", "1 hour ago"},
	"24h":  {"--since", "24 hours ago"},
	"boot": {"-b"},
}

// ValidLogSince reports whether since is one of "", "1h", "24h", "boot".
func ValidLogSince(since string) bool { _, ok := logSince[since]; return ok }

// ClampLogLines returns n clamped to LogLinesMin..LogLinesMax (0: default).
func ClampLogLines(n int) int {
	switch {
	case n <= 0:
		return LogLinesDefault
	case n < LogLinesMin:
		return LogLinesMin
	case n > LogLinesMax:
		return LogLinesMax
	}
	return n
}

// LogsResult is the response of logs-read.
type LogsResult struct {
	Unit      string `json:"unit"`
	Lines     int    `json:"lines"`
	Since     string `json:"since"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Available bool   `json:"available"`
}

// LogsRead returns the last lines of one unit's journal.
func (o *Ops) LogsRead(ctx context.Context, unit string, lines int, since string) (LogsResult, error) {
	res := LogsResult{Unit: unit, Since: since}
	if !ValidLogUnit(unit) {
		return res, errors.New("invalid unit")
	}
	if !ValidLogSince(since) {
		return res, errors.New("invalid since")
	}
	res.Lines = ClampLogLines(lines)
	argv := []string{"journalctl", "-u", unit, "-n", fmt.Sprint(res.Lines), "--no-pager", "-o", "short-iso"}
	argv = append(argv, logSince[since]...)
	out, code, err := o.Run.Run(ctx, argv...)
	if err != nil {
		return res, nil // journalctl is not installed: Available stays false
	}
	res.Available = true
	if code != 0 {
		return res, fmt.Errorf("journalctl failed: %s", LastLines(out, 3))
	}
	res.Text, res.Truncated = capTail(out, logMaxBytes)
	return res, nil
}

// capTail keeps at most max bytes of s, from the end, starting at a line.
func capTail(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	s = s[len(s)-max:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s, true
}

// ServiceState is one entry of the service list.
type ServiceState struct {
	Unit    string `json:"unit"`
	Desc    string `json:"description"`
	Active  string `json:"active"`  // active, inactive, failed, activating ...
	Sub     string `json:"sub"`     // running, dead, listening ...
	Enabled string `json:"enabled"` // enabled, disabled, static, masked ...
	Since   string `json:"since"`   // ActiveEnterTimestamp text, "" when never started
}

// ServicesResult is the response of services-status.
type ServicesResult struct {
	Available bool           `json:"available"`
	Services  []ServiceState `json:"services"`
}

// parseServices reads "systemctl show" output for several units (blocks of
// Key=Value lines separated by blank lines). Units that are not installed
// (LoadState=not-found) are left out.
func parseServices(text string) []ServiceState {
	out := []ServiceState{}
	for _, block := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		m := parseKV(block)
		if m["Id"] == "" || m["LoadState"] == "not-found" {
			continue
		}
		out = append(out, ServiceState{Unit: m["Id"], Desc: m["Description"], Active: m["ActiveState"], Sub: m["SubState"],
			Enabled: m["UnitFileState"], Since: m["ActiveEnterTimestamp"]})
	}
	return out
}

// ServicesStatus reads the state of the fixed unit list.
func (o *Ops) ServicesStatus(ctx context.Context) ServicesResult {
	res := ServicesResult{Services: []ServiceState{}}
	argv := []string{"systemctl", "show", "--no-pager", "--property=Id,Description,LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp", "--"}
	for _, u := range serviceUnits() {
		if !strings.Contains(u, ".") {
			u += ".service"
		}
		argv = append(argv, u)
	}
	out, code, err := o.Run.Run(ctx, argv...)
	if err != nil || code != 0 {
		return res
	}
	res.Available = true
	res.Services = parseServices(out)
	return res
}
