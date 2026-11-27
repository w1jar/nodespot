package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// The command line form of vocoder-install, used by packaging/install.sh when
// NODESPOT_VOCODER=1: `nodespot-helper vocoder-install --wait`. It runs the
// same VocoderInstall as the web UI (platform check, pinned key, apt source,
// transient install unit) and then waits for the job, so the installer can
// report the outcome. Nothing is duplicated in shell.

const (
	// VocoderWaitPoll is how often the install job is polled.
	VocoderWaitPoll = 3 * time.Second
	// VocoderWaitMax bounds the wait (apt on a slow Pi, with the apt lock wait).
	VocoderWaitMax = 30 * time.Minute
	// vocoderIdlePolls is how many polls may report no job at all right after
	// the unit was started before the install counts as lost.
	vocoderIdlePolls = 10
)

// VocoderInstallWait installs the vocoder and waits until the job ends. It
// writes progress (the job log, as it grows) to w. It returns nil when the
// vocoder is installed and active, and an error with a clear message when the
// platform is unsupported, the install is refused, or the job fails or times out.
func (o *Ops) VocoderInstallWait(ctx context.Context, w io.Writer) error {
	return waitVocoderInstall(ctx, w, o.VocoderInstall, o.VocoderStatus, VocoderWaitPoll, VocoderWaitMax)
}

// waitVocoderInstall is the loop of VocoderInstallWait with the two ops and
// the timing injected (tests).
func waitVocoderInstall(ctx context.Context, w io.Writer,
	install, status func(context.Context) (VocoderStatus, error), poll, maxWait time.Duration) error {
	st, err := install(ctx)
	if err != nil {
		return vocoderWaitErr(err)
	}
	if st.Installed && st.Active && st.Job != "running" {
		fmt.Fprintln(w, "the vocoder is already installed and running")
		return nil
	}
	fmt.Fprintln(w, "installing the vocoder (md380-emu); this can take a few minutes")
	deadline := time.Now().Add(maxWait)
	shown, idle := "", 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted; the install job keeps running in the background (check with: systemctl status %s)", vocoderJobUnit)
		case <-time.After(poll):
		}
		if st, err = status(ctx); err != nil {
			return vocoderWaitErr(err)
		}
		shown = printNewLog(w, shown, st.JobLog)
		switch st.Job {
		case "running":
			idle = 0
		case "failed":
			return fmt.Errorf("the vocoder install failed: %s", lastLogLine(st.JobLog))
		case "done":
			return vocoderDone(st)
		default: // the unit is not visible (yet)
			if st.Installed && st.Active {
				return nil
			}
			if idle++; idle >= vocoderIdlePolls {
				return errors.New("the vocoder install job vanished before it finished")
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the vocoder install did not finish in %s; it keeps running (check with: systemctl status %s)", maxWait, vocoderJobUnit)
		}
	}
}

func vocoderDone(st VocoderStatus) error {
	if st.Installed && st.Active {
		return nil
	}
	return fmt.Errorf("the vocoder install ended but md380-emu is not running: %s", lastLogLine(st.JobLog))
}

// vocoderWaitErr strips the machine code prefix ("not_supported: ...") the
// ops put on their errors for the API.
func vocoderWaitErr(err error) error {
	switch {
	case errors.Is(err, ErrVocoderUnsupported), errors.Is(err, ErrVocoderClockUnset), errors.Is(err, ErrVocoderKeyMismatch):
		msg := err.Error()
		if i := strings.Index(msg, ": "); i > 0 && !strings.ContainsAny(msg[:i], " ") {
			msg = msg[i+2:]
		}
		return errors.New(msg)
	case errors.Is(err, ErrBusy):
		return errors.New("the package manager is busy; try again in a few minutes")
	}
	return err
}

// printNewLog writes the part of log that was not shown yet and returns the
// new "shown" state. The log is the last lines of the job, so when it no
// longer starts with what was shown it is printed whole.
func printNewLog(w io.Writer, shown, log string) string {
	if log == "" || log == shown {
		return shown
	}
	if strings.HasPrefix(log, shown) {
		fmt.Fprint(w, strings.TrimPrefix(log, shown))
	} else {
		fmt.Fprint(w, log)
	}
	if !strings.HasSuffix(log, "\n") {
		fmt.Fprintln(w)
	}
	return log
}

func lastLogLine(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return "see: journalctl -u " + vocoderJobUnit
}
