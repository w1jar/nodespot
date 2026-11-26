package helper

import (
	"context"
	"fmt"
)

// OpNodeRestart restarts nodespot-node.service (Settings, System, the node
// service). Its state is part of services-status.
const OpNodeRestart = "node-restart"

func init() {
	registerSysOp(OpNodeRestart, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) {
		return o.NodeRestart(ctx)
	})
}

// NodeRestart runs "systemctl restart nodespot-node.service" and waits for it,
// so a node that fails to start is reported.
func (o *Ops) NodeRestart(ctx context.Context) (OKResult, error) {
	out, code, err := o.Run.Run(ctx, "systemctl", "restart", nodeService+".service")
	if err != nil {
		return OKResult{}, fmt.Errorf("systemctl: %w", err)
	}
	if code != 0 {
		return OKResult{}, fmt.Errorf("systemctl restart %s failed: %s", nodeService, LastLines(out, 3))
	}
	return OKResult{OK: true}, nil
}

// NodeRestart calls node-restart.
func (c *Client) NodeRestart(ctx context.Context) error {
	var v OKResult
	return c.sys(ctx, OpNodeRestart, nil, &v, 1<<20)
}

// NodeRestart simulates the restart of the node (nothing happens).
func (d *DemoSystem) NodeRestart(context.Context) error { return nil }
