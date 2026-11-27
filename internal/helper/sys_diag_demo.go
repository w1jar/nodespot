package helper

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/w1jar/nodespot/internal/diag"
)

// DiagBundle builds a small bundle from canned data for --demo. It runs the
// real redaction and packing, so the demo shows what a download looks like,
// including a planted placeholder password that comes out as <redacted>.
func (d *DemoSystem) DiagBundle(_ context.Context, req DiagRequest) (io.ReadCloser, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	conf := "{\n  \"version\": 1,\n  \"station\": {\"callsign\": \"N0CALL\"},\n  \"modes\": {\"allstar\": {\"values\": {\"node\": \"2001\"}}}\n}\n"
	id := diag.ExtractIdentity(conf)
	id.Callsigns = append(append(id.Callsigns, "N0CALL"), req.Callsigns...)
	id.IPs = append(id.IPs, "192.0.2.10")
	opts := diag.BundleOptions{MaskIdentity: req.MaskIdentity, Secrets: []string{"example-pass"}, Identity: id}
	b, err := newDiagBuilder([]string{os.TempDir()}, opts, time.Now(), req.Version)
	if err != nil {
		return nil, err
	}
	b.add("report.txt", "nodespot status report (demo)", orDefault(req.Report, "nodespot demo report\nnode 2001 N0CALL at 192.0.2.10\n"), diag.KindText, DiagMaxReport)
	b.add("versions.txt", "Versions (demo)", "nodespot: "+orDefault(req.Version, "demo")+"\nnodespot-node (demo)\n", diag.KindText, diagMaxFile)
	b.add("network.txt", "Network status (demo)", "eth0: UP\n    inet 192.0.2.10/24 scope global\n", diag.KindText, diagMaxFile)
	b.add("journal/nodespot-node.txt", "Journal of nodespot-node (demo)", "2026-11-20T12:00:00+0000 node2001 nodespot-node[412]: allstar: N0CALL registered from 192.0.2.10 with example-pass\n", diag.KindText, diagMaxFile)
	b.add("config/node.json", "Node configuration (demo, mode passwords removed)", conf, diag.KindConf, diagMaxConf)
	b.add("audit.txt", "Recent audit log entries (demo)", orDefault(req.Audit, "(demo)\n"), diag.KindText, DiagMaxAudit)
	bundle, err := b.pack()
	if err != nil {
		b.cleanup()
		return nil, err
	}
	return bundle, nil
}
