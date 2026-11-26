package diag

import (
	"fmt"
	"strings"
	"time"
)

// NodeState is what the node daemon reported (no peer addresses).
type NodeState struct {
	Socket  string
	Running bool
	Error   string // why it could not be asked
	// Callsign is the station's own callsign (kept by Redact).
	Callsign     string
	Started      time.Time
	Configured   bool
	RadioPresent bool
	// Active is the active mode ("" idle), State its state, Target the
	// target it is linked to, Detail and ModeErr its last lines.
	Active  string
	State   string
	Target  string
	Detail  string
	ModeErr string
	// Modes lists the configured modes.
	Modes []string
}

// Report holds everything the diagnostics file shows.
type Report struct {
	Now      time.Time
	Version  string
	Platform Platform
	Username string // not printed; reserved

	Uptime       int64 // host seconds
	Load         [3]float64
	MemTotal     uint64
	MemAvailable uint64
	CPUTemp      *float64

	Node NodeState

	NodelistCount   int
	NodelistSource  string
	NodelistUpdated *time.Time
	NodelistError   string

	UpdatesAvailable bool
	UpdatesReason    string
	UpdatesInstalled string
	UpdatesCandidate string
	UpdatesJob       string
	UpdatesJobLog    string
	UpdatesChecked   string

	// Helper / firewall (HelperErr set when the helper cannot be reached).
	HelperErr       string
	FirewallPresent bool
	FirewallRunning bool
	FirewallEnabled bool
	HaveFirewall    bool

	// Config lines (already secret-masked) and log lines.
	Config []string
	Log    []string

	// Redaction.
	Secrets []string
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "n/a"
	}
	return s
}

// KeepCalls returns the station's own callsign (kept by Redact).
func (r Report) KeepCalls() []string {
	if r.Node.Callsign == "" {
		return nil
	}
	return []string{r.Node.Callsign}
}

// Render produces the plain-text report and runs it through Redact.
func (r Report) Render() string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	h := func(t string) { p("\n== %s ==", t) }

	p("nodespot diagnostics")
	p("Generated: %s", r.Now.UTC().Format(time.RFC3339))
	p("Private data (IP addresses, secrets, emails, other stations' callsigns) is removed from this file; see the nodespot docs.")

	h("Versions and platform")
	p("nodespot:    %s", orNA(r.Version))
	p("OS:       %s", orNA(r.Platform.Distro))
	p("Kernel:   %s", orNA(r.Platform.Kernel))
	p("Arch:     %s/%s", r.Platform.GOOS, r.Platform.GOARCH)
	p("Hardware: %s (%s)", orNA(r.Platform.Model), r.Platform.Hardware)

	h("Host")
	p("Uptime: %ds", r.Uptime)
	p("Load:   %.2f %.2f %.2f", r.Load[0], r.Load[1], r.Load[2])
	p("Memory: %d MiB available of %d MiB", r.MemAvailable>>20, r.MemTotal>>20)
	if r.CPUTemp != nil {
		p("CPU temperature: %.1f C", *r.CPUTemp)
	} else {
		p("CPU temperature: n/a")
	}

	h("Node")
	n := r.Node
	p("Socket:  %s", orNA(n.Socket))
	p("Running: %s", yesno(n.Running))
	if n.Error != "" {
		p("Error:   %s", n.Error)
	}
	if n.Running {
		p("Station: %s  configured=%s  radio=%s", orNA(n.Callsign), yesno(n.Configured), yesno(n.RadioPresent))
		if !n.Started.IsZero() {
			p("Started: %s", n.Started.UTC().Format(time.RFC3339))
		}
		p("Modes configured: %s", orNA(strings.Join(n.Modes, " ")))
		if n.Active == "" {
			p("Active mode: none")
		} else {
			p("Active mode: %s state=%s target=%s", n.Active, orNA(n.State), orNA(n.Target))
			if n.Detail != "" {
				p("  detail: %s", n.Detail)
			}
			if n.ModeErr != "" {
				p("  last error: %s", n.ModeErr)
			}
		}
	}

	h("Node list")
	p("Entries: %d  source: %s", r.NodelistCount, orNA(r.NodelistSource))
	if r.NodelistUpdated != nil {
		p("Updated: %s", r.NodelistUpdated.UTC().Format(time.RFC3339))
	} else {
		p("Updated: never")
	}
	if r.NodelistError != "" {
		p("Error: %s", r.NodelistError)
	}

	h("Updates and packages")
	p("Helper available: %s", yesno(r.UpdatesAvailable))
	if r.UpdatesReason != "" {
		p("Reason: %s", r.UpdatesReason)
	}
	if r.UpdatesAvailable {
		p("Installed: %s  candidate: %s  job: %s  last check: %s", orNA(r.UpdatesInstalled), orNA(r.UpdatesCandidate), orNA(r.UpdatesJob), orNA(r.UpdatesChecked))
		if strings.TrimSpace(r.UpdatesJobLog) != "" {
			p("Update log:")
			for _, l := range strings.Split(strings.TrimRight(r.UpdatesJobLog, "\n"), "\n") {
				p("  %s", l)
			}
		}
	}
	switch {
	case r.HelperErr != "":
		p("Firewall (helper): unavailable: %s", r.HelperErr)
	case r.HaveFirewall:
		p("Firewall (helper): firewall-cmd installed=%s firewalld running=%s web port open=%s", yesno(r.FirewallPresent), yesno(r.FirewallRunning), yesno(r.FirewallEnabled))
	default:
		p("Firewall (helper): n/a")
	}

	h("Configuration (secrets masked)")
	if len(r.Config) == 0 {
		p("(none)")
	}
	for _, l := range r.Config {
		p("%s", l)
	}

	h(fmt.Sprintf("Server log (last %d lines)", len(r.Log)))
	if len(r.Log) == 0 {
		p("(empty)")
	}
	for _, l := range r.Log {
		p("%s", l)
	}

	return Redact(b.String(), Options{Secrets: r.Secrets, KeepCalls: r.KeepCalls()})
}
