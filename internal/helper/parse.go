package helper

import (
	"regexp"
	"strings"
)

// ParseShow parses `systemctl show` KEY=VALUE output.
func ParseShow(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// jobNoise are journal lines that carry no information about the job. systemd
// logs the "Failed to open /run/systemd/transient/..." line when it removes a
// --collect'ed transient unit.
func jobNoise(l string) bool {
	return strings.Contains(l, "Failed to open /run/systemd/transient/")
}

var progressRe = regexp.MustCompile(`^\(Reading database \.\.\.( \d+%)?$`)

// CleanLog prepares journal output for display and classification: noise
// lines are dropped, only the latest run is kept (from the last "Starting
// nodespot-upgrade.service" line) and dpkg's "(Reading database ... N%"
// progress, which arrives as carriage-return separated fragments, is removed.
func CleanLog(log string) string { return cleanLogUnit(log, upgradeUnit) }

// cleanLogUnit is CleanLog for the journal of one upgrade unit.
func cleanLogUnit(log, unit string) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		if jobNoise(l) {
			continue
		}
		lines = append(lines, l)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "Starting "+unit+".service") {
			lines = lines[i:]
			break
		}
	}
	out := lines[:0:0]
	for _, l := range lines {
		if strings.Contains(l, "\r") || strings.Contains(l, `\r`) {
			var keep []string
			for _, seg := range crSplit.Split(l, -1) {
				if progressRe.MatchString(strings.TrimSpace(seg)) || strings.TrimSpace(seg) == "" {
					continue
				}
				keep = append(keep, seg)
			}
			if len(keep) == 0 {
				continue
			}
			l = strings.Join(keep, " ")
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

var crSplit = regexp.MustCompile(`\r|\\r`)

var noopRe = regexp.MustCompile(`(?m)^0 upgraded,`)

// IsNoop reports whether a job log shows that apt had nothing to upgrade.
func IsNoop(log string) bool { return noopRe.MatchString(log) }

// JobStateFrom derives the job state from `systemctl show nodespot-upgrade`
// properties and the tail of its journal. The transient unit is started with
// --collect, so once it has finished systemd forgets its result; the final
// systemd message of the latest run in the journal ("Deactivated
// successfully" or "Failed with result") tells how it ended.
func JobStateFrom(show map[string]string, log string) string {
	return jobStateFrom(show, log, upgradeUnit)
}

func jobStateFrom(show map[string]string, log, unit string) string {
	switch show["ActiveState"] {
	case "active", "activating", "deactivating", "reloading":
		return JobRunning
	case "failed":
		return JobFailed
	}
	if r := show["Result"]; r != "" && r != "success" {
		return JobFailed
	}
	clean := cleanLogUnit(log, unit)
	lines := strings.Split(strings.TrimSpace(clean), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		switch {
		case strings.Contains(l, "Failed with result"), strings.Contains(l, "Failed to start"):
			return JobFailed
		case strings.Contains(l, "Deactivated successfully"), strings.Contains(l, "Succeeded."):
			if IsNoop(clean) {
				return JobNoop
			}
			return JobSucceeded
		}
	}
	return JobIdle
}

// LastLines returns at most n trailing lines of s.
func LastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
