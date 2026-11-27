package updates

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/helper"
)

// DemoHelper simulates the helper for --demo: applying an update "runs" for
// a few seconds and then the installed version is the candidate.
type DemoHelper struct {
	mu        sync.Mutex
	installed string
	channel   string
	target    string // version being installed
	started   time.Time
	finished  bool
	// RunTime is how long the simulated upgrade takes.
	RunTime time.Duration

	// Simulated OS updates.
	osStarted  time.Time
	osApplied  bool
	osReboot   bool
	osFinished bool
}

// DemoNotes are the release notes shown in demo mode.
const DemoNotes = "- Demo release notes\n- Faster dashboard updates\n- Fixes for **monitor** mode"

// NewDemoHelper returns a simulated helper; installed is the demo's version.
func NewDemoHelper(installed string) *DemoHelper {
	if !IsPackageVersion(installed) {
		installed = "0.2.0-demo"
	}
	return &DemoHelper{installed: installed, channel: ChannelStable, RunTime: 6 * time.Second}
}

// demoVersion is the version on offer in a channel.
func demoVersion(channel string) string {
	if channel == ChannelBeta {
		return "99.1.0~beta.1"
	}
	return "99.0.0"
}

// DemoFetch returns the fake latest json of a channel (an update is always on
// offer until the simulated upgrade has run).
func (d *DemoHelper) DemoFetch() FetchFunc {
	return func(_ context.Context, channel string, _ bool) (Notes, error) {
		notes := DemoNotes
		if channel == ChannelBeta {
			notes = "- Demo beta notes\n- Available to Beta users as soon as it is built"
		}
		return Notes{Version: demoVersion(channel), Notes: notes, Date: "2026-11-15"}, nil
	}
}

func (d *DemoHelper) status() helper.Status {
	st := helper.Status{Installed: d.installed, Channel: d.channel, Job: helper.JobIdle}
	switch {
	case !d.started.IsZero() && !d.finished && time.Since(d.started) < d.RunTime:
		st.Job = helper.JobRunning
		st.JobLog = "Reading package lists...\nUnpacking nodespot (" + d.target + ") over (" + d.installed + ") ..."
	case !d.started.IsZero():
		d.finished = true
		d.installed = d.target
		st.Installed = d.installed
		st.Job = helper.JobSucceeded
		st.JobLog = "Setting up nodespot (" + d.target + ") ...\n(demo: the server does not restart)"
	}
	return st
}

// UpdateStatus implements Helper.
func (d *DemoHelper) UpdateStatus(context.Context) (helper.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status(), nil
}

// UpdateApply implements Helper.
func (d *DemoHelper) UpdateApply(context.Context) (helper.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st := d.status(); st.Job == helper.JobRunning {
		return helper.Status{}, helper.ErrBusy
	}
	d.target = demoVersion(d.channel)
	d.started, d.finished = time.Now(), false
	return d.status(), nil
}

// SetChannel implements Helper.
func (d *DemoHelper) SetChannel(_ context.Context, channel string) (helper.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !ValidChannel(channel) {
		return helper.Status{}, errors.New("invalid channel")
	}
	if st := d.status(); st.Job == helper.JobRunning {
		return helper.Status{}, helper.ErrBusy
	}
	d.channel = channel
	return d.status(), nil
}

// demoOSPackages are the simulated upgradable packages.
func demoOSPackages() []helper.OSPackage {
	return []helper.OSPackage{
		{Name: "curl", From: "7.88.1-10+deb12u7", To: "7.88.1-10+deb12u8", Origin: "Debian:12.8/stable"},
		{Name: "libssl3", From: "3.0.11-1~deb12u1", To: "3.0.13-1~deb12u2", Origin: "Debian-Security:12/stable-security", Security: true},
		{Name: "linux-image-rpi-v8", From: "6.1.63-1+rpt1", To: "6.1.69-1+rpt1", Origin: "Raspberry Pi Foundation:bookworm"},
		{Name: "tzdata", From: "2024a-0+deb12u1", To: "2024b-0+deb12u1", Origin: "Debian:12.8/stable"},
	}
}

func (d *DemoHelper) osStatus() helper.OSStatus {
	st := helper.OSStatus{Packages: []helper.OSPackage{}, KeptBack: []string{}, RebootPkgs: []string{}, Job: helper.JobIdle}
	if !d.osStarted.IsZero() {
		if !d.osFinished && time.Since(d.osStarted) < d.RunTime {
			st.Job = helper.JobRunning
			st.JobLog = "Reading package lists...\nUnpacking libssl3 (3.0.13-1~deb12u2) over (3.0.11-1~deb12u1) ..."
		} else {
			if !d.osFinished {
				d.osFinished, d.osApplied, d.osReboot = true, true, true
			}
			st.Job = helper.JobSucceeded
			st.JobLog = "Setting up libssl3 (3.0.13-1~deb12u2) ...\nSetting up curl (7.88.1-10+deb12u8) ...\n(demo: nothing was installed)"
		}
	}
	if d.osReboot {
		st.RebootRequired, st.RebootPkgs = true, []string{"linux-image-rpi-v8"}
	}
	return st
}

// OSStatus implements OSHelper.
func (d *DemoHelper) OSStatus(context.Context) (helper.OSStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.osStatus(), nil
}

// OSCheck implements OSHelper.
func (d *DemoHelper) OSCheck(context.Context) (helper.OSStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.osStatus()
	if st.Job == helper.JobRunning {
		return helper.OSStatus{}, helper.ErrBusy
	}
	if !d.osApplied {
		st.Packages = demoOSPackages()
		st.KeptBack = []string{"raspi-firmware"}
	}
	return st, nil
}

// OSApply implements OSHelper.
func (d *DemoHelper) OSApply(context.Context) (helper.OSStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.osStatus().Job == helper.JobRunning || d.status().Job == helper.JobRunning {
		return helper.OSStatus{}, helper.ErrBusy
	}
	d.osStarted, d.osFinished = time.Now(), false
	return d.osStatus(), nil
}

// Reboot implements OSHelper: the demo only clears the reboot marker.
func (d *DemoHelper) Reboot(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.osStatus().Job == helper.JobRunning {
		return helper.ErrBusy
	}
	d.osReboot = false
	return nil
}

// RestartNodespot simulates the restart of the panel (nothing happens).
func (d *DemoHelper) RestartNodespot(context.Context) error { return nil }
