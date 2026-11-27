// Command nodespot is the nodespot server: the web control panel of the nodespot-node
// radio node (AllStar, EchoLink and the digital modes on one radio).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/w1jar/nodespot/internal/api"
	"github.com/w1jar/nodespot/internal/auth"
	"github.com/w1jar/nodespot/internal/backupsvc"
	"github.com/w1jar/nodespot/internal/config"
	"github.com/w1jar/nodespot/internal/demo"
	"github.com/w1jar/nodespot/internal/diag"
	"github.com/w1jar/nodespot/internal/favstats"
	"github.com/w1jar/nodespot/internal/hamdb"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/hostlists"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/nodelist"
	"github.com/w1jar/nodespot/internal/radioid"
	"github.com/w1jar/nodespot/internal/schedules"
	"github.com/w1jar/nodespot/internal/store"
	"github.com/w1jar/nodespot/internal/system"
	"github.com/w1jar/nodespot/internal/updates"
	"github.com/w1jar/nodespot/internal/webui"
)

var version = "dev"

const devOrigin = "http://localhost:5173"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(adminMain(os.Args[2:]))
	}
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n  nodespot [options]            run the web server\n  nodespot admin <command>      manage accounts (create, reset-password, list); see: nodespot admin --help\n\nOptions:\n")
		flag.PrintDefaults()
	}
	cfgPath := flag.String("config", config.DefaultConfigPath, "path to nodespot.conf")
	dev := flag.Bool("dev", false, "development mode: print setup code, allow Vite origin, no Secure cookie")
	demoMode := flag.Bool("demo", false, "run against a simulated node (no radio or network needed)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("nodespot", version)
		return
	}
	if err := run(*cfgPath, *dev, *demoMode); err != nil {
		if errors.Is(err, errRestart) {
			// A restore finished: systemd restarts the service on this
			// status (SuccessExitStatus/RestartForceExitStatus in the unit).
			os.Exit(restartExitCode)
		}
		log.Fatalf("nodespot: %v", err)
	}
}

// restartExitCode is the exit status that asks systemd for a restart after a
// restore (see packaging/systemd/nodespot.service).
const restartExitCode = 75

var errRestart = errors.New("restart requested")

func run(cfgPath string, dev, demoMode bool) error {
	// Keep the last log lines in memory for the diagnostics download.
	ring := diag.NewRing(500)
	logOut := io.MultiWriter(os.Stderr, ring)
	log.SetOutput(logOut)
	logger := log.New(logOut, "", log.LstdFlags)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	defaults := config.Defaults()
	if demoMode {
		if cfg.DataDir == defaults.DataDir {
			d, err := os.MkdirTemp("", "nodespot-demo-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(d)
			cfg.DataDir = d
		}
		if cfg.Listen == defaults.Listen {
			cfg.Listen = "127.0.0.1:8080"
		}
		cfg.NodelistURL = ""
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("datadir: %w", err)
	}

	// A restore stages its database; the swap happens before it is opened.
	if swapped, err := backupsvc.ApplyStaged(cfg.DataDir); err != nil {
		logger.Printf("restore: %v", err)
	} else if swapped {
		logger.Printf("restore: the restored database is in place (the previous one is nodespot.db.pre-restore)")
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "nodespot.db"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	as, err := auth.NewService(st, cfg.DataDir)
	if err != nil {
		return err
	}
	as.DevMode = dev

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	bg := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}

	ridOpts := radioid.Options{Dir: cfg.DataDir, Version: version, Settings: st, Logger: logger}
	if demoMode {
		ridOpts.Client, ridOpts.FirstDelay = demo.RadioIDClient(), time.Second
	}
	rid := radioid.New(ridOpts)
	hdb := hamdb.New(hamdb.Options{Dir: cfg.DataDir, Version: version, Settings: st, Logger: logger})
	nl := nodelist.New(nodelist.Options{URL: cfg.NodelistURL, DataDir: cfg.DataDir, Interval: cfg.NodelistRefresh, Logger: logger})

	// The node daemon (nodespot-node) owns the radio and the networks; the
	// panel only talks to its control socket.
	nodeSocket := cfg.NodeSocket
	if demoMode {
		dir, err := os.MkdirTemp("", "nodespot-dn")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		rig, err := startDemoNode(dir)
		if err != nil {
			return fmt.Errorf("demo node: %w", err)
		}
		defer rig.Close()
		nodeSocket = rig.Socket
		bg(func() { simulateTalk(ctx, rig) })
		nl.Set(demo.NodeList(), "demo", time.Now())
		if n, _ := st.CountUsers(); n == 0 {
			h, err := auth.HashPassword("demo")
			if err != nil {
				return err
			}
			if _, err := st.CreateUser("demo", h, store.RoleAdmin); err != nil {
				return err
			}
			fmt.Println("DEMO MODE: log in as user 'demo' with password 'demo' (admin)")
		}
		seedDemo(st, time.Now())
	} else {
		bg(func() { nl.Run(ctx) })
	}
	nodeClient := node.NewClient(nodeSocket)

	if code, err := as.EnsureSetupCode(); err != nil {
		return fmt.Errorf("setup code: %w", err)
	} else if code != "" {
		if dev {
			fmt.Printf("SETUP CODE: %s\n", code)
		}
		logger.Printf("first-run setup required: open %s (or run: nodespot admin create); code also in %s",
			setupLink(cfg.Listen, code, interfaceAddrs()), filepath.Join(cfg.DataDir, "setup-code"))
	}

	var upd *updates.Manager
	var power api.Power
	var sysHost api.SystemHost
	var bkHelper backupsvc.Helper
	var restart func()
	var restartReq atomic.Bool
	var firewall func(context.Context) (helper.Firewall, error)
	if demoMode {
		firewall = func(context.Context) (helper.Firewall, error) {
			return helper.Firewall{Present: true, Running: true, ServiceEnabled: true}, nil
		}
		// Backup and restore run for real on temporary directories.
		demoDir, err := os.MkdirTemp("", "nodespot-demo-node-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(demoDir)
		ops, err := helper.NewDemoOps(demoDir, "")
		if err != nil {
			return err
		}
		bkHelper = helper.LocalBackup{Ops: ops}
		dh := updates.NewDemoHelper(version)
		upd = updates.New(dh, version, dh.DemoFetch())
		upd.FirstCheck = time.Second
		upd.OS = updates.NewOS(dh, st)
		upd.OS.FirstCheck, upd.OS.Tick = time.Second, time.Second
		power = dh
		sysHost = helper.NewDemoSystem()
	} else {
		hc := helper.NewClient(cfg.HelperSocket)
		bkHelper = hc
		restart = func() { restartReq.Store(true); stop() }
		firewall = hc.FirewallStatus
		upd = updates.New(hc, version, updates.HTTPLatest(cfg.UpdatesURL, nil))
		upd.Logger = logger
		upd.OS = updates.NewOS(hc, st)
		upd.OS.Logger = logger
		power = hc
		sysHost = hc
	}
	bg(func() { upd.Run(ctx) })
	bg(func() { upd.OS.Run(ctx) })

	sys := system.New(version, api.SystemNode(nodeClient))
	var origins []string
	if dev {
		origins = []string{devOrigin}
	}
	var confPath string
	if !demoMode {
		confPath = cfgPath
	}
	bk := backupsvc.New(backupsvc.Options{Helper: bkHelper, Store: st, DataDir: cfg.DataDir, ConfPath: confPath,
		Version: version, Logger: logger, Demo: demoMode})
	bg(func() { backupMaintenance(ctx, bk, nodeClient) })
	fsOpts := favstats.Options{BaseURL: cfg.FavStatsURL, UserAgent: "nodespot/" + version, Logger: logger, LogRequests: cfg.FavStatsLog}
	if demoMode {
		// No real service to protect: a request every 2 s, each taking 0.8 s.
		fsOpts.Fetch = favstats.WithLatency(favstats.DemoFetcher(time.Now), 800*time.Millisecond)
		fsOpts.Interval = 2 * time.Second
	}
	favScan := favstats.New(fsOpts)
	favScan.SetRate(cfg.FavStatsRate)
	// Schedules: the API wires the engine's link controller and broadcast.
	sched := &schedules.Engine{Store: st, Logger: logger, StartDelay: 5 * time.Second}
	hlOpts := hostlists.Options{Dir: filepath.Join(cfg.DataDir, "hostlists"), Version: version, Settings: st, Logger: logger}
	if demoMode {
		hlOpts.FirstDelay = time.Hour // the demo never goes online
	}
	hl := hostlists.New(hlOpts)
	bg(func() { rid.Run(ctx) })
	bg(func() { hdb.Run(ctx) })
	bg(func() { hl.Run(ctx) })
	srv := api.New(api.Deps{
		Auth: as, Store: st, Node: nodeClient, Nodelist: nl, System: sys, Updates: upd, Power: power, SysHost: sysHost,
		HostLists: hl, RadioID: rid, HamDB: hdb, FavStats: favScan, Schedules: sched, Version: version,
		UI: webui.Handler(), AllowedOrigins: origins, Logger: logger,
		Log: ring, Config: cfg, ConfigPath: cfgPath, Firewall: firewall,
		PublicDefaultEnabled: demoMode,
		Backup:               bk, Restart: restart,
	})
	bg(func() { srv.Run(ctx) })
	bg(func() { cleanupSessions(ctx, st) })

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		stop()
		wg.Wait()
		return err
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          logger,
		// No WriteTimeout: SSE streams are long-lived (handlers set their own deadlines).
	}
	logger.Printf("nodespot %s listening on http://%s (node: %s)", version, ln.Addr(), nodeSocket)
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case err := <-errc:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
	}
	logger.Printf("shutting down")
	srv.Shutdown()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil {
		_ = httpSrv.Close()
	}
	wg.Wait()
	if restartReq.Load() {
		return errRestart
	}
	return nil
}

// backupMaintenance purges expired uploads and, after a restore, completes
// the health check once the node has had time to come back.
func backupMaintenance(ctx context.Context, bk *backupsvc.Service, n *node.Client) {
	bk.RunHealth(ctx, func(ctx context.Context) []backupsvc.Check {
		checks := []backupsvc.Check{{Name: "nodespot", OK: true, Detail: "restarted with the restored database"}}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := n.Status(sctx)
		cancel()
		c := backupsvc.Check{Name: "Node", OK: err == nil, Detail: "running"}
		if err != nil {
			c.Detail = err.Error()
		} else if st.Active != "" {
			c.Detail = "running, " + st.Active + " active"
		}
		return append(checks, c)
	})
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bk.Purge()
		}
	}
}

func cleanupSessions(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = st.DeleteExpiredSessions(time.Now())
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// seedDemoSchedules adds two example schedules to an empty demo database: a
// daily 15 minute window a few minutes after the next quarter hour, and a
// daily disconnect after it, so the Schedules tab has something to show.
func seedDemoSchedules(st *store.Store, now time.Time) {
	if l, _ := st.ListSchedules(); len(l) != 0 {
		return
	}
	start := now.Truncate(15 * time.Minute).Add(15*time.Minute + 3*time.Minute)
	end := start.Add(15 * time.Minute)
	off := end.Add(5 * time.Minute)
	all := []int{0, 1, 2, 3, 4, 5, 6}
	clock := func(t time.Time) string { return t.Format("15:04") }
	for _, x := range []schedules.Schedule{
		{Name: "Demo net", Enabled: true, Mode: mode.AllStar, Target: "2000", Action: schedules.ActionConnect,
			Days: all, Start: clock(start), End: clock(end)},
		{Name: "Tidy up", Enabled: true, Mode: mode.AllStar, Action: schedules.ActionDisconnect,
			Days: all, Start: clock(off)},
	} {
		if err := x.Normalize(); err != nil {
			continue
		}
		_, _ = st.AddSchedule(x)
	}
}
