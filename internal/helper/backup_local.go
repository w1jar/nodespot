package helper

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalBackup serves the backup operations from an in-process Ops (demo mode
// and tests) with the same shape as the socket Client.
type LocalBackup struct{ Ops *Ops }

// BackupInfo implements the backup service's helper interface.
func (l LocalBackup) BackupInfo(ctx context.Context) (BackupInfo, error) {
	return l.Ops.BackupInfo(ctx)
}

// BackupCreate runs backup-create into a pipe and returns the parsed stream.
func (l LocalBackup) BackupCreate(ctx context.Context, req BackupRequest) (*BackupStream, error) {
	pr, pw := io.Pipe()
	go func() {
		_, err := l.Ops.BackupCreate(ctx, &req, pw)
		_ = pw.CloseWithError(err)
	}()
	return ReadBackupStream(pr, pr)
}

// BackupRestore implements the backup service's helper interface.
func (l LocalBackup) BackupRestore(ctx context.Context, req RestoreRequest, archive io.Reader) (RestoreResult, error) {
	return l.Ops.BackupRestore(ctx, &req, archive)
}

// demoRunner answers the commands of a made-up node: nothing runs.
type demoRunner struct{}

func (demoRunner) Run(ctx context.Context, argv ...string) (string, int, error) {
	if strings.Join(argv, " ") == "dpkg --print-architecture" {
		return "amd64\n", 0, nil
	}
	return "", 0, nil
}

// NewDemoOps builds an Ops whose files are all under dir, so that backup and
// restore can be tried without touching the host. nodeConfig is the node
// config to back up ("" for a made-up one under dir).
func NewDemoOps(dir, nodeConfig string) (*Ops, error) {
	o := &Ops{
		Run:          demoRunner{},
		NodeConfig:   nodeConfig,
		SA818State:   filepath.Join(dir, "etc", "nodespot", "sa818.json"),
		NodespotConf: filepath.Join(dir, "etc", "nodespot", "nodespot.conf"),
		SourceList:   filepath.Join(dir, "etc", "nodespot.list"),
		OSRelease:    filepath.Join(dir, "etc", "os-release"),
		NodeUser:     "-", // no such user: the files keep the demo's owner
		TempDir:      dir,
	}
	files := map[string]string{
		o.SA818State:   "{\"version\":1,\"applied_at\":\"2026-11-20T12:00:00Z\",\"settings\":{\"bandwidth\":0,\"rx_mhz\":146.52,\"tx_mhz\":146.52,\"squelch\":4}}\n",
		o.NodespotConf: "[server]\nlisten = 127.0.0.1:8080\n",
		o.SourceList:   "deb [signed-by=/etc/apt/keyrings/nodespot.gpg] https://example.invalid/apt stable main\n",
		o.OSRelease:    "ID=debian\nVERSION_CODENAME=trixie\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n",
	}
	if nodeConfig == "" {
		o.NodeConfig = filepath.Join(dir, "etc", "nodespot", "node.json")
		files[o.NodeConfig] = "{\"version\":1,\"station\":{\"callsign\":\"N0CALL\",\"radio\":{},\"id\":{},\"advanced\":{}}," +
			"\"modes\":{\"allstar\":{\"values\":{\"node\":\"1999\",\"password\":\"demo-node-secret\"}}}}\n"
	}
	for p, c := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			return nil, err
		}
	}
	return o, nil
}
