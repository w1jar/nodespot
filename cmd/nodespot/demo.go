package main

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/w1jar/nodespot/internal/demo"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/node/nodetest"
	"github.com/w1jar/nodespot/internal/store"
)

// demoNodeConfig is the demo station: every mode set up with placeholder
// values, AllStar active.
func demoNodeConfig() *node.Config {
	off := false
	return &node.Config{
		Version: node.ConfigVersion,
		Station: node.Station{
			Callsign: "N0CALL",
			ID:       node.IDConfig{Enabled: &off},
			Active:   &node.Selection{Mode: mode.AllStar},
		},
		Modes: map[string]mode.Settings{
			mode.AllStar:  {Values: map[string]string{"node": demo.LocalNode, "password": "demo-node-secret"}},
			mode.EchoLink: {Values: map[string]string{"password": "demo-echolink-secret"}},
			mode.BM:       {Values: map[string]string{"dmr_id": "1234567", "password": "demo-bm-secret", "address": "master.example.net"}},
			mode.YSF:      {},
			mode.M17:      {},
		},
	}
}

// startDemoNode runs a fake node (no network, no radio) on a socket in dir.
func startDemoNode(dir string) (*nodetest.Rig, error) {
	return nodetest.Start(demoNodeConfig(), nodetest.Options{Dir: dir, Modem: &nodetest.DemoModem{}})
}

// demoCallers are the made-up stations that talk on the demo node.
var demoCallers = []string{"N1CALL", "N2CALL", "N3CALL", "N0DEMO", "N0TEST"}

// simulateTalk keys the active demo mode now and then, so Now calling and
// Last heard have something to show.
func simulateTalk(ctx context.Context, rig *nodetest.Rig) {
	c := rig.Client()
	for {
		if !sleep(ctx, time.Duration(8+rand.IntN(10))*time.Second) {
			return
		}
		st, err := c.Status(ctx)
		if err != nil || st.Active == "" {
			continue
		}
		f := rig.Fake(st.Active)
		if f == nil {
			continue
		}
		f.Key(demoCallers[rand.IntN(len(demoCallers))])
		ok := sleep(ctx, time.Duration(2+rand.IntN(5))*time.Second)
		f.Unkey()
		if !ok {
			return
		}
	}
}

// seedDemo adds a few favorites and two example schedules to an empty demo
// database.
func seedDemo(st *store.Store, now time.Time) {
	if favs, _ := st.ListFavorites(""); len(favs) == 0 {
		for _, f := range []struct{ mode, target, label string }{
			{mode.AllStar, "1998", "Demo hub"},
			{mode.AllStar, "1999", "Demo test hub"},
			{mode.AllStar, "2000", "Demo reflector"},
			{mode.BM, "91", "Worldwide"},
			{mode.EchoLink, "2001", "Demo conference"},
		} {
			_, _ = st.AddFavorite(f.mode, f.target, f.label, "")
		}
	}
	seedDemoSchedules(st, now)
}
