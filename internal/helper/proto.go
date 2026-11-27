// Package helper implements the wire protocol, the server logic and the
// client of nodespot-helper, the small root-owned process that performs the only
// privileged operations nodespot needs (package and OS updates, firewalld,
// services, backup and restore, the System tab).
//
// Protocol: one JSON request per connection, one JSON response. Operations
// are a fixed set; requests carry no free-form arguments (only the strictly typed fields
// of a few ops), so nothing a client sends can reach a shell or alter a
// command line.
package helper

// Operation names.
const (
	OpUpdateStatus   = "update-status"
	OpUpdateCheck    = "update-check"
	OpUpdateApply    = "update-apply"
	OpFirewallStatus = "firewall-status"
	OpFirewallOpen   = "firewall-open"
	// OpChannelStable and OpChannelBeta switch the apt suite of the nodespot
	// source between "stable" and "beta" (the update channel).
	OpChannelStable = "update-channel-stable"
	OpChannelBeta   = "update-channel-beta"

	// Operating system (apt) updates. None takes an argument: the commands
	// are fixed, no package name ever comes from a client.
	OpOSCheck  = "os-check"  // apt-get update + simulated upgrade
	OpOSStatus = "os-status" // job state and log, reboot-required (cheap)
	OpOSApply  = "os-apply"  // start the upgrade in a transient unit
	OpReboot   = "reboot"    // systemctl reboot
	// OpNodespotRestart restarts the nodespot service (systemctl --no-block).
	OpNodespotRestart = "nodespot-restart"
)

// Update channels (the suite of the nodespot apt source).
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// Job states reported in Status.Job.
const (
	JobIdle      = "idle"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	// JobNoop: the job finished but apt had nothing to upgrade (the package
	// index lags behind latest.json).
	JobNoop = "noop"
)

// Request is the single request object.
type Request struct {
	Op string `json:"op"`
	// Backup is accepted by backup-create only, Restore by backup-restore
	// only (see backup_proto.go).
	Backup  *BackupRequest  `json:"backup,omitempty"`
	Restore *RestoreRequest `json:"restore,omitempty"`
	// Sys is accepted by the System tab ops only (sys.go).
	Sys *SysRequest `json:"sys,omitempty"`
	// Terminal is accepted by the terminal.* ops only (terminal.go).
	Terminal *TerminalRequest `json:"terminal,omitempty"`
	// Diag is accepted by diag-collect only (sys_diag.go).
	Diag *DiagRequest `json:"diag,omitempty"`
}

// Status is the response of the update-* operations. Candidate and
// Upgradable are kept for protocol compatibility but are no longer filled in:
// the server compares versions itself (latest.json), apt is not consulted.
type Status struct {
	Installed  string `json:"installed"`
	Candidate  string `json:"candidate"`
	Upgradable bool   `json:"upgradable"`
	Job        string `json:"job"`
	JobLog     string `json:"jobLog"`
	// Channel is the suite of the nodespot apt source: "stable", "beta", or ""
	// when it cannot be determined.
	Channel string `json:"channel"`
}

// OSPackage is one package an apt upgrade would install.
type OSPackage struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Origin   string `json:"origin,omitempty"`
	Security bool   `json:"security"`
}

// OSStatus is the response of os-check, os-status and os-apply. Packages and
// KeptBack are filled by os-check only.
type OSStatus struct {
	Packages       []OSPackage `json:"packages"`
	KeptBack       []string    `json:"keptBack"`
	RebootRequired bool        `json:"rebootRequired"`
	RebootPkgs     []string    `json:"rebootPkgs"`
	Job            string      `json:"job"`
	JobLog         string      `json:"jobLog"`
	// Warnings are non-fatal notes of os-check, e.g. that the software AMBE vocoder
	// repository could not be refreshed.
	Warnings []string `json:"warnings,omitempty"`
}

// Firewall is the response of the firewall-* operations.
type Firewall struct {
	// Present: firewall-cmd is installed.
	Present bool `json:"present"`
	// Running: firewalld is running.
	Running bool `json:"running"`
	// ServiceEnabled: the packaged "nodespot" firewalld service (TCP 8080) is
	// enabled in the runtime or permanent configuration. Only meaningful
	// when Port is 8080.
	ServiceEnabled bool `json:"serviceEnabled"`
	// Port is the TCP port from the [server] listen setting of nodespot.conf
	// (read by the helper, never sent by the caller); 0 when unknown.
	Port int `json:"port"`
	// PortOpen: Port/tcp is open in the runtime or permanent configuration.
	PortOpen bool `json:"portOpen"`
	// Open: the web port is open (the service for 8080, or the port).
	Open bool `json:"open"`
}

// errorResponse is sent for any failure.
type errorResponse struct {
	Error string `json:"error"`
}

// OKResult is the result of reboot and nodespot-restart.
type OKResult struct {
	OK bool `json:"ok"`
}
