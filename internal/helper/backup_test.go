package helper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/node"
)

// Placeholder node config with two secrets.
const bkNodeJSON = `{
  "version": 1,
  "station": {"callsign": "N0CALL", "active": {"mode": "echolink"}},
  "modes": {
    "allstar": {"values": {"node": "1999", "password": "example-pass-iax"}},
    "echolink": {"values": {"callsign": "N0CALL-L", "password": "example-pass-echo"}}
  }
}
`

const bkSA818 = `{"version":1,"settings":{"bandwidth":0,"rx_mhz":446.1,"tx_mhz":446.1}}` + "\n"

type bkNode struct {
	t    *testing.T
	dir  string
	o    *Ops
	f    *fakeRunner
	conf string
}

func newBkNode(t *testing.T, nodeJSON string) *bkNode {
	t.Helper()
	dir := t.TempDir()
	f := &fakeRunner{rules: map[string]resp{
		"dpkg --print-architecture": {out: "arm64\n"},
		"systemctl restart":         {},
	}}
	n := &bkNode{t: t, dir: dir, f: f}
	n.o = &Ops{Run: f, Now: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
		NodeConfig: filepath.Join(dir, "node.json"), SA818State: filepath.Join(dir, "sa818.json"),
		NodespotConf: filepath.Join(dir, "nodespot.conf"), NodeUser: "-",
		OSRelease: filepath.Join(dir, "os-release"), SourceList: filepath.Join(dir, "nodespot.list")}
	n.put("os-release", "ID=debian\nVERSION_CODENAME=bookworm\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n")
	if nodeJSON != "" {
		n.put("node.json", nodeJSON)
	}
	return n
}

func (n *bkNode) put(rel, content string) {
	n.t.Helper()
	if err := os.WriteFile(filepath.Join(n.dir, rel), []byte(content), 0o600); err != nil {
		n.t.Fatal(err)
	}
}

func (n *bkNode) get(rel string) string {
	n.t.Helper()
	b, err := os.ReadFile(filepath.Join(n.dir, rel))
	if err != nil {
		n.t.Fatal(err)
	}
	return string(b)
}

// create runs BackupCreate and returns the header and the files of the tar.
func (n *bkNode) create(req BackupRequest) (BackupHeader, map[string][]byte) {
	n.t.Helper()
	var buf bytes.Buffer
	if _, err := n.o.BackupCreate(context.Background(), &req, &buf); err != nil {
		n.t.Fatal(err)
	}
	bs, err := ReadBackupStream(&buf, nil)
	if err != nil {
		n.t.Fatal(err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(bs.Body)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			n.t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = b
	}
	return bs.Header, files
}

// archive packs a full format 2 backup (as the server does) from the
// helper's part plus the database and nodespot.conf.
func archive(t *testing.T, hdr BackupHeader, files map[string][]byte, conf string, format int) []byte {
	t.Helper()
	m := &backup.Manifest{Format: backup.FormatVersion, Created: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy: "admin", NodespotVersion: "0.0.1", Hostname: "node.example.net", Timezone: "UTC", Arch: hdr.Arch,
		OSID: hdr.OSID, OSVersionCodename: hdr.OSCodename, Callsign: hdr.Callsign, Parts: hdr.Parts}
	all := map[string][]byte{backup.PathNodespotDB: []byte("SQLITE")}
	for p, b := range files {
		all[p] = b
	}
	if conf != "" {
		all[backup.PathNodespotConf] = []byte(conf)
	}
	for p, b := range all {
		e := backup.Entry{Path: p, Type: backup.TypeFile, Owner: "root", Group: "root", Mode: "0640"}
		e.Size, e.SHA256 = backup.Sum(b)
		m.Files = append(m.Files, e)
	}
	var buf bytes.Buffer
	w, err := backup.NewWriter(&buf, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Files {
		if err := w.Add(e.Path, bytes.NewReader(all[e.Path])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if format != 0 {
		// Rewrite the manifest's format, as an older or newer nodespot would.
		return rewriteFormat(t, buf.Bytes(), format)
	}
	return buf.Bytes()
}

func rewriteFormat(t *testing.T, gzTar []byte, format int) []byte {
	t.Helper()
	var m *backup.Manifest
	files := map[string][]byte{}
	// Read with a handler that keeps everything; the manifest is valid.
	m, err := backup.Read(bytes.NewReader(gzTar), func(e backup.Entry, body io.Reader) error {
		b, err := io.ReadAll(body)
		files[e.Path] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Format = format
	raw, _ := json.Marshal(m)
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	_ = tw.WriteHeader(&tar.Header{Name: backup.ManifestName, Mode: 0o600, Size: int64(len(raw))})
	_, _ = tw.Write(raw)
	for _, e := range m.Files {
		_ = tw.WriteHeader(&tar.Header{Name: e.Path, Mode: 0o640, Size: e.Size, Typeflag: tar.TypeReg})
		_, _ = tw.Write(files[e.Path])
	}
	_ = tw.Close()
	_ = gzw.Close()
	return buf.Bytes()
}

func TestBackupCreateSecrets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets bool
	}{{"without passwords", false}, {"with passwords", true}} {
		t.Run(tc.name, func(t *testing.T) {
			n := newBkNode(t, bkNodeJSON)
			n.put("sa818.json", bkSA818)
			hdr, files := n.create(BackupRequest{Secrets: tc.secrets})
			if hdr.Callsign != "N0CALL" || hdr.Arch != "arm64" || hdr.OSCodename != "bookworm" ||
				!hdr.Parts.Node || !hdr.Parts.SA818 || hdr.Parts.Secrets != tc.secrets || len(hdr.Entries) != 2 {
				t.Fatalf("header %+v", hdr)
			}
			nj := string(files[backup.PathNodeConfig])
			for _, p := range []string{"example-pass-iax", "example-pass-echo"} {
				if strings.Contains(nj, p) != tc.secrets {
					t.Errorf("secrets=%v but %s present=%v:\n%s", tc.secrets, p, !tc.secrets, nj)
				}
			}
			if !strings.Contains(nj, "N0CALL-L") || !strings.Contains(nj, `"1999"`) {
				t.Errorf("settings lost:\n%s", nj)
			}
			if string(files[backup.PathSA818]) != bkSA818 {
				t.Errorf("sa818 %q", files[backup.PathSA818])
			}
			for _, e := range hdr.Entries {
				size, sum := backup.Sum(files[e.Path])
				if e.Size != size || e.SHA256 != sum {
					t.Errorf("%s: entry does not match the body", e.Path)
				}
			}
		})
	}
}

func TestBackupCreateEmptyNode(t *testing.T) {
	n := newBkNode(t, "")
	hdr, files := n.create(BackupRequest{})
	if hdr.Parts.Node || hdr.Parts.SA818 || len(files) != 0 || len(hdr.Entries) != 0 {
		t.Errorf("%+v %v", hdr, files)
	}
	info, err := n.o.BackupInfo(context.Background())
	if err != nil || info.Node || info.SA818 || info.Secrets {
		t.Errorf("%+v %v", info, err)
	}
	n2 := newBkNode(t, bkNodeJSON)
	if info, _ := n2.o.BackupInfo(context.Background()); !info.Node || !info.Secrets || info.SA818 {
		t.Errorf("%+v", info)
	}
}

func TestMergeSecrets(t *testing.T) {
	conf := func(active string, modes map[string]map[string]string) *node.Config {
		c := &node.Config{Version: 1, Station: node.Station{Callsign: "N0CALL"}, Modes: map[string]mode.Settings{}}
		if active != "" {
			c.Station.Active = &node.Selection{Mode: active}
		}
		for m, v := range modes {
			c.Modes[m] = mode.Settings{Values: v}
		}
		return c
	}
	for _, tc := range []struct {
		name             string
		restored, cur    *node.Config
		wantMissing      []string
		wantEcho, wantAS string // the passwords afterward ("-" = mode left out)
		wantActive       string
	}{
		{"blank filled from this node",
			conf("echolink", map[string]map[string]string{"echolink": {"callsign": "N0CALL-L"}, "allstar": {"node": "1999"}}),
			conf("", map[string]map[string]string{"echolink": {"password": "example-pass-echo"}, "allstar": {"password": "example-pass-iax"}}),
			nil, "example-pass-echo", "example-pass-iax", "echolink"},
		{"backup password wins",
			conf("", map[string]map[string]string{"echolink": {"callsign": "N0CALL-L", "password": "example-pass-new"}}),
			conf("", map[string]map[string]string{"echolink": {"password": "example-pass-echo"}}),
			nil, "example-pass-new", "-", ""},
		{"required password missing everywhere",
			conf("echolink", map[string]map[string]string{"echolink": {"callsign": "N0CALL-L"}, "allstar": {"node": "1999"}}),
			nil,
			[]string{"EchoLink"}, "-", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing := mergeSecrets(tc.restored, tc.cur)
			if strings.Join(missing, ",") != strings.Join(tc.wantMissing, ",") {
				t.Errorf("missing %v, want %v", missing, tc.wantMissing)
			}
			got := func(m string) string {
				s, ok := tc.restored.Modes[m]
				if !ok {
					return "-"
				}
				return s.Values["password"]
			}
			if got("echolink") != tc.wantEcho || got("allstar") != tc.wantAS {
				t.Errorf("echolink %q allstar %q", got("echolink"), got("allstar"))
			}
			active := ""
			if a := tc.restored.Station.Active; a != nil {
				active = a.Mode
			}
			if active != tc.wantActive {
				t.Errorf("active %q, want %q", active, tc.wantActive)
			}
		})
	}
}

func TestBackupRestore(t *testing.T) {
	src := newBkNode(t, bkNodeJSON)
	src.put("sa818.json", bkSA818)
	hdrNoSec, filesNoSec := src.create(BackupRequest{})
	hdrSec, filesSec := src.create(BackupRequest{Secrets: true})
	const conf = "[server]\nlisten = :8080\n"

	for _, tc := range []struct {
		name        string
		archive     []byte
		current     string // node.json on the target ("" = none)
		dryRun      bool
		wantErr     string
		wantMissing []string
		wantPass    string // echolink password on the target afterward
		wantRestart int
	}{
		{"with passwords", archive(t, hdrSec, filesSec, conf, 0), "", false, "", nil, "example-pass-echo", 1},
		{"without passwords keeps this node's", archive(t, hdrNoSec, filesNoSec, conf, 0),
			strings.ReplaceAll(bkNodeJSON, "example-pass-echo", "example-pass-here"), false, "", nil, "example-pass-here", 1},
		{"without passwords on a fresh node", archive(t, hdrNoSec, filesNoSec, conf, 0), "", false, "",
			[]string{"EchoLink"}, "-", 1},
		{"dry run changes nothing", archive(t, hdrSec, filesSec, conf, 0), "", true, "", nil, "", 0},
		{"format 1 refused", archive(t, hdrSec, filesSec, conf, 1), "", false, "Asterisk", nil, "", 0},
		{"newer format refused", archive(t, hdrSec, filesSec, conf, 3), "", false, "update nodespot first", nil, "", 0},
		{"not an archive", []byte("not a backup"), "", false, "", nil, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := newBkNode(t, tc.current)
			res, err := dst.o.BackupRestore(context.Background(), &RestoreRequest{DryRun: tc.dryRun}, bytes.NewReader(tc.archive))
			if tc.name == "not an archive" {
				if err == nil {
					t.Fatal("garbage accepted")
				}
				return
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want %q", err, tc.wantErr)
				}
				if _, statErr := os.Stat(filepath.Join(dst.dir, "sa818.json")); statErr == nil {
					t.Error("a refused archive changed the node")
				}
				return
			}
			if err != nil || !res.OK {
				t.Fatalf("%+v %v", res, err)
			}
			if strings.Join(res.MissingSecrets, ",") != strings.Join(tc.wantMissing, ",") {
				t.Errorf("missing %v", res.MissingSecrets)
			}
			if got := dst.f.called("systemctl restart nodespot-node"); got != tc.wantRestart {
				t.Errorf("restarts %d", got)
			}
			if tc.dryRun {
				if len(res.Steps) != 0 || dst.f.called("systemctl") != 0 {
					t.Errorf("dry run applied: %+v", res.Steps)
				}
				if _, err := os.Stat(filepath.Join(dst.dir, "node.json")); err == nil {
					t.Error("dry run wrote node.json")
				}
				return
			}
			c, err := node.LoadConfig(filepath.Join(dst.dir, "node.json"))
			if err != nil {
				t.Fatalf("restored node.json does not load: %v", err)
			}
			pass := "-"
			if s, ok := c.Modes[mode.EchoLink]; ok {
				pass = s.Values["password"]
			}
			if pass != tc.wantPass {
				t.Errorf("echolink password %q, want %q", pass, tc.wantPass)
			}
			if dst.get("sa818.json") != bkSA818 || dst.get("nodespot.conf") != conf {
				t.Error("sa818.json or nodespot.conf not restored")
			}
			var names []string
			for _, s := range res.Steps {
				names = append(names, s.Name)
			}
			if strings.Join(names, ",") != "Restore node settings,Restore SA818 settings,Restore nodespot.conf,Restart the node" {
				t.Errorf("steps %v", names)
			}
		})
	}
}

func TestBackupRestoreRestartFails(t *testing.T) {
	src := newBkNode(t, bkNodeJSON)
	hdr, files := src.create(BackupRequest{Secrets: true})
	dst := newBkNode(t, "")
	dst.f.rules["systemctl restart"] = resp{out: "Job failed\n", code: 1}
	res, err := dst.o.BackupRestore(context.Background(), &RestoreRequest{}, bytes.NewReader(archive(t, hdr, files, "", 0)))
	if err != nil || res.OK || !strings.Contains(res.Error, "Restart the node") {
		t.Errorf("%+v %v", res, err)
	}
}

func TestKeepListen(t *testing.T) {
	cases := []struct {
		name, backup, current string
		want                  string
		kept, was             string
		changed               bool
	}{
		{"port differs, comments kept",
			"; my node\n[server]\n# web port\nlisten = :8080\ndatadir = /var/lib/nodespot\n\n[node]\nsocket = /run/nodespot-node/control.sock\n",
			"[server]\nlisten = :8181\n",
			"; my node\n[server]\n# web port\nlisten = :8181\ndatadir = /var/lib/nodespot\n\n[node]\nsocket = /run/nodespot-node/control.sock\n",
			":8181", ":8080", true},
		{"backup without listen",
			"[server]\ndatadir = /var/lib/nodespot\n; end\n",
			"[server]\nlisten = :8181\n",
			"[server]\ndatadir = /var/lib/nodespot\nlisten = :8181\n; end\n",
			":8181", "", true},
		{"backup without server section",
			"[node]\nsocket = /run/x.sock\n",
			"[server]\nlisten = :8181\n",
			"[node]\nsocket = /run/x.sock\n\n[server]\nlisten = :8181\n",
			":8181", "", true},
		{"current without listen",
			"[server]\nlisten = :8080\n", "[server]\ndatadir = /x\n",
			"[server]\nlisten = :8080\n", "", "", false},
		{"identical",
			"[server]\nlisten = :8181 ; same\n", "[server]\nlisten=:8181\n",
			"[server]\nlisten = :8181 ; same\n", ":8181", ":8181", false},
		{"inline comment",
			"[server]\nlisten = :8080\n",
			"[server]\nlisten = 192.0.2.10:8181 ; LAN only\n",
			"[server]\nlisten = 192.0.2.10:8181\n",
			"192.0.2.10:8181", ":8080", true},
		{"backup inline comment kept",
			"[server]\nlisten = :8080 ; web\n", "[server]\nlisten = :8181\n",
			"[server]\nlisten = :8181 ; web\n", ":8181", ":8080", true},
		{"no trailing newline",
			"[server]\nlisten = :8080", "[server]\nlisten = :8181\n",
			"[server]\nlisten = :8181", ":8181", ":8080", true},
		{"empty current", "[server]\nlisten = :8080\n", "", "[server]\nlisten = :8080\n", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, kept, was, changed := keepListen([]byte(c.backup), []byte(c.current))
			if string(out) != c.want || kept != c.kept || was != c.was || changed != c.changed {
				t.Errorf("got %q kept=%q was=%q changed=%v\nwant %q kept=%q was=%q changed=%v",
					out, kept, was, changed, c.want, c.kept, c.was, c.changed)
			}
		})
	}
}

func TestBackupRestoreKeepsListen(t *testing.T) {
	src := newBkNode(t, bkNodeJSON)
	hdr, files := src.create(BackupRequest{Secrets: true})
	const backupConf = "# old node\n[server]\nlisten = :8080\n"
	dst := newBkNode(t, "")
	dst.put("nodespot.conf", "[server]\nlisten = :8181\n")
	res, err := dst.o.BackupRestore(context.Background(), &RestoreRequest{}, bytes.NewReader(archive(t, hdr, files, backupConf, 0)))
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if got := dst.get("nodespot.conf"); got != "# old node\n[server]\nlisten = :8181\n" {
		t.Errorf("conf %q", got)
	}
	found := false
	for _, s := range res.Steps {
		if s.Name == "Restore nodespot.conf" {
			found = true
			if !s.OK || !strings.Contains(s.Detail, "listen = :8181") || !strings.Contains(s.Detail, ":8080") {
				t.Errorf("step %+v", s)
			}
		}
	}
	if !found {
		t.Errorf("no step: %+v", res.Steps)
	}
}

func TestBackupRestoreBadParts(t *testing.T) {
	src := newBkNode(t, bkNodeJSON)
	hdr, files := src.create(BackupRequest{Secrets: true})
	for _, tc := range []struct {
		name, path, body, want string
	}{
		{"node config not JSON", backup.PathNodeConfig, "{", "not valid"},
		{"node config newer", backup.PathNodeConfig, `{"version": 99, "station": {}}`, "newer"},
		{"sa818 unknown version", backup.PathSA818, `{"version": 7}`, "version 7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := map[string][]byte{}
			for k, v := range files {
				f[k] = v
			}
			f[tc.path] = []byte(tc.body)
			h := hdr
			h.Parts.SA818 = f[backup.PathSA818] != nil
			dst := newBkNode(t, "")
			_, err := dst.o.BackupRestore(context.Background(), &RestoreRequest{}, bytes.NewReader(archive(t, h, f, "", 0)))
			var be *backup.Error
			if !errors.As(err, &be) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// The mmdvm settings come back with Allow transmit off.
func TestBackupRestoreMMDVMTransmitOff(t *testing.T) {
	const nj = `{"version":1,"station":{"callsign":"N0CALL","radio":{"preset":"mmdvm","mmdvm":{"device":"/dev/serial0","baud":115200,"duplex":false,"rxFreqHz":441500000,"txFreqHz":441500000,"colorCode":3,"allowTx":true,"resetGpio":20}}}}`
	src := newBkNode(t, nj)
	hdr, files := src.create(BackupRequest{Secrets: true})
	if !strings.Contains(string(files[backup.PathNodeConfig]), `"allowTx": true`) {
		t.Fatalf("backup node.json %s", files[backup.PathNodeConfig])
	}
	dst := newBkNode(t, "")
	res, err := dst.o.BackupRestore(context.Background(), &RestoreRequest{}, bytes.NewReader(archive(t, hdr, files, "", 0)))
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	c, err := node.LoadConfig(filepath.Join(dst.dir, "node.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Station.Radio.MMDVM
	if c.Station.Radio.Preset != "mmdvm" || m == nil || m.AllowTX || m.ColorCode == nil || *m.ColorCode != 3 ||
		m.Duplex == nil || *m.Duplex || m.ResetGPIO == nil || *m.ResetGPIO != 20 || m.RXFreqHz != 441500000 {
		t.Errorf("restored mmdvm %+v", m)
	}
}
