// Command nodespot-helper is the privileged helper of nodespot. It runs as root,
// socket-activated by systemd, and performs a fixed set of operations for the
// unprivileged nodespot server (see internal/helper).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/w1jar/nodespot/internal/helper"
)

var version = "dev"

type uidList []uint32

func (u *uidList) String() string { return fmt.Sprint(*u) }
func (u *uidList) Set(s string) error {
	var n uint32
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return err
	}
	*u = append(*u, n)
	return nil
}

func main() {
	// The OS upgrade unit asks whether a failed apt-get update may be ignored.
	if len(os.Args) == 4 && os.Args[1] == "dv-step" && os.Args[2] == helper.StepAptUpdateCheck {
		if err := helper.NewOps().AptUpdateCheck(os.Args[3], os.Stdout); err != nil {
			fmt.Fprintln(os.Stdout, "error:", err)
			os.Exit(1)
		}
		return
	}
	// The port 80 revert timer (internal/helper/sys_webport.go).
	if len(os.Args) == 2 && os.Args[1] == "web-port-guard" {
		os.Exit(runGuard())
	}
	// One pass of the setup-hotspot watchdog, run by nodespot-wifi-watchdog.timer
	// (docs/DESIGN.md); it needs no running web server.
	if len(os.Args) == 2 && os.Args[1] == "wifi-watchdog" {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "wifi-watchdog must run as root")
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := helper.NewOps().RunWifiWatchdog(ctx, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "wifi-watchdog:", err)
			os.Exit(1)
		}
		return
	}
	// The session host of the browser terminal runs as the main process of
	// its own transient unit (internal/helper/termhost.go).
	if len(os.Args) >= 2 && os.Args[1] == "term-host" {
		os.Exit(helper.RunTermHost(os.Args[2:]))
	}
	// The installer (NODESPOT_VOCODER=1) installs the AMBE vocoder with the
	// same code as the web UI and waits for it (internal/helper/vocoder_wait.go).
	if len(os.Args) >= 2 && os.Args[1] == "vocoder-install" {
		os.Exit(runVocoderInstall(os.Args[2:]))
	}
	listen := flag.String("listen", "", "listen on this unix socket path instead of using systemd socket activation (testing)")
	var allow uidList
	flag.Var(&allow, "allow-uid", "additionally allow this uid (testing; repeatable)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("nodespot-helper", version)
		return
	}
	logger := log.New(os.Stderr, "nodespot-helper: ", 0) // journald adds timestamps

	var ln net.Listener
	var err error
	if *listen != "" {
		ln, err = helper.ListenUnix(*listen)
	} else {
		ln, err = helper.ListenerFromSystemd()
		if err == nil && ln == nil {
			err = fmt.Errorf("not socket-activated (LISTEN_FDS unset); use --listen PATH for manual runs")
		}
	}
	if err != nil {
		logger.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); ln.Close() }()

	srv := helper.NewServer(logger, allow...)
	logger.Printf("%s serving on %s", version, ln.Addr())
	if err := srv.Serve(ctx, ln); err != nil {
		logger.Fatal(err)
	}
}

// runGuard is the step of the transient revert timer started by the move to
// port 80: it undoes the move when nobody confirmed the new address.
func runGuard() int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "web-port-guard must run as root")
		return 1
	}
	if err := helper.NewOps().WebPortGuard(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "web-port-guard:", err)
		return 1
	}
	return 0
}

// runVocoderInstall is `nodespot-helper vocoder-install --wait`: install the
// vocoder and wait for the job. Exit 0 only when the vocoder is installed and
// running; a message on stderr says why otherwise.
func runVocoderInstall(args []string) int {
	if len(args) != 1 || args[0] != "--wait" {
		fmt.Fprintln(os.Stderr, "usage: nodespot-helper vocoder-install --wait")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "vocoder-install must run as root")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := helper.NewOps().VocoderInstallWait(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "vocoder-install:", err)
		return 1
	}
	fmt.Println("the vocoder is installed and running")
	return 0
}
