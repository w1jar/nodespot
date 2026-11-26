package helper

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestNodeRestart(t *testing.T) {
	const cmd = "systemctl restart nodespot-node.service"
	for _, tc := range []struct {
		name    string
		r       resp
		wantErr string
	}{
		{"ok", resp{}, ""},
		{"fails", resp{code: 1, out: "Job for nodespot-node.service failed."}, "nodespot-node.service failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, f := osOps(t, map[string]resp{cmd: tc.r})
			res, err := o.NodeRestart(context.Background())
			if f.called(cmd) != 1 {
				t.Fatalf("calls %v", f.calls)
			}
			if tc.wantErr == "" {
				if err != nil || !res.OK {
					t.Fatalf("%+v %v", res, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestNodeRestartOverSocket(t *testing.T) {
	o, f := osOps(t, map[string]resp{"systemctl restart nodespot-node.service": {}})
	c, _ := startServer(t, o, PeerUID, []uint32{0, uint32(os.Getuid())})
	if err := c.NodeRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.called("systemctl restart nodespot-node.service") != 1 {
		t.Fatalf("calls %v", f.calls)
	}
}
