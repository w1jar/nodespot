package nodetest

import (
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/native/radio"
)

// DemoModem is a stand-in MMDVM modem for tests and the demo: an idle
// MMDVM_HS dual hat that answers, with the status the real one reports
// before it is configured.
type DemoModem struct {
	mu      sync.Mutex
	reopens int
}

// Status is the hat's status.
func (m *DemoModem) Status() radio.ModemStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return radio.ModemStatus{
		Open: true, Protocol: 1,
		Description: "MMDVM_HS-v1.6.1 20231115_WPSD 14.7456MHz dual ADF7021 FW by AUTHORS. GitID #7e16099",
		Board:       "MMDVM_HS", Firmware: "1.6.1", BuildDate: "2023-11-15", DuplexCapable: true, Duplex: true,
		Modes: []string{"dstar", "dmr", "ysf", "p25", "nxdn", "pocsag", "m17"},
		State: "idle", Space: [2]int{10, 25}, LastStatus: time.Now(), Reopens: m.reopens,
	}
}

// Reopen counts a reopen.
func (m *DemoModem) Reopen() {
	m.mu.Lock()
	m.reopens++
	m.mu.Unlock()
}

// Reopens is how often Reopen was called.
func (m *DemoModem) Reopens() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reopens
}
