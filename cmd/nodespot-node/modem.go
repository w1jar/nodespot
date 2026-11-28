package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/rf"
)

// modemSettings are the driver settings for the mmdvm preset. With a
// receive frequency the driver is set up to receive DMR in simplex and never
// transmits (RXOnly: SET_FREQ with RF power 0, simplex SET_CONFIG, SET_MODE
// DMR, and a write filter that drops every transmit frame). Without one it
// only detects the modem: GET_VERSION and status polls. Transmit stays
// inhibited whatever Allow transmit says; it comes in a later release.
func modemSettings(m node.MMDVMSettings, log *slog.Logger) mmdvm.Settings {
	rx, tx, receive := m.ModemFreq()
	return mmdvm.Settings{
		Port: m.Device, Baud: m.Baud,
		Modem:      mmdvm.Config{Duplex: false, ColorCode: uint8(m.ColorCode)},
		Freq:       mmdvm.Freq{RX: rx, TX: tx, Power: 0},
		AllowTX:    false,
		RXOnly:     receive,
		DetectOnly: !receive,
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
	ep := rf.New(d, rf.Config{Hang: adv.Hang(), TXTimeout: adv.TxTimeout(), ReceiveOnly: true, Logger: log})
	r := &modemRunner{d: d, ep: ep}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := ep.Run(ctx); err != nil {
			log.Error("mmdvm", "err", err)
		}
	}()
	if _, _, receive := m.ModemFreq(); receive {
		log.Info("mmdvm modem (receive only, simplex)", "device", m.Device, "baud", m.Baud, "duplexConfigured", m.Duplex)
	} else {
		log.Info("mmdvm modem (detect only: no receive frequency set)", "device", m.Device, "baud", m.Baud)
	}
	return r
}

// stop closes the modem and waits for the endpoint to return.
func (r *modemRunner) stop() {
	_ = r.d.Close()
	r.wg.Wait()
}
