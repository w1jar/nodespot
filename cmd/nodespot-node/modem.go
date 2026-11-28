package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/rf"
)

// modemSettings are the driver settings for the mmdvm preset. This release
// only detects the modem: DetectOnly sends GET_VERSION and status polls,
// never SET_FREQ or SET_CONFIG, and transmit stays inhibited in the driver
// whatever Allow transmit says.
func modemSettings(m node.MMDVMSettings, log *slog.Logger) mmdvm.Settings {
	rx := int64(m.RXFreqHz)
	tx := int64(m.TXFreqHz)
	if rx != 0 {
		rx += int64(m.RXOffsetHz)
	}
	if tx != 0 {
		tx += int64(m.TXOffsetHz)
	}
	return mmdvm.Settings{
		Port: m.Device, Baud: m.Baud,
		Modem:      mmdvm.Config{Duplex: m.Duplex, ColorCode: uint8(m.ColorCode)},
		Freq:       mmdvm.Freq{RX: uint32(rx), TX: uint32(tx), Power: float64(m.RFPower)},
		AllowTX:    false,
		DetectOnly: true,
		Logger:     log,
	}
}

// modemRunner is the MMDVM driver and its RF endpoint, run for the mmdvm
// preset in place of the sound-chip radio.
type modemRunner struct {
	d  *mmdvm.Driver
	ep *rf.Endpoint
	wg sync.WaitGroup
}

func startModem(ctx context.Context, cfg *node.Config, m node.MMDVMSettings, log *slog.Logger) *modemRunner {
	log = log.With("radio", "mmdvm")
	d := mmdvm.New(modemSettings(m, log))
	adv := cfg.Station.Advanced
	ep := rf.New(d, rf.Config{Hang: adv.Hang(), TXTimeout: adv.TxTimeout(), Logger: log})
	r := &modemRunner{d: d, ep: ep}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := ep.Run(ctx); err != nil {
			log.Error("mmdvm", "err", err)
		}
	}()
	log.Info("mmdvm modem (detect only)", "device", m.Device, "baud", m.Baud)
	return r
}

// stop closes the modem and waits for the endpoint to return.
func (r *modemRunner) stop() {
	_ = r.d.Close()
	r.wg.Wait()
}
