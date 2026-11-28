// Package fake is a software MMDVM modem for tests. It answers the host
// protocol of package mmdvm over an in-memory link (or any
// io.ReadWriteCloser, such as a pty master): GET_VERSION and GET_STATUS in
// protocol 1 or 2, ACK or NAK with the firmware's reasons, DMR transmit
// with a per-slot FIFO, received-burst injection, optional loopback of
// transmitted bursts, and a mode where it stops answering.
package fake

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
)

// Options configure a fake modem.
type Options struct {
	// Protocol is 1 or 2 (default 1, as MMDVM_HS hats speak).
	Protocol int
	// Description is the version text (default [DefaultDescription]).
	Description string
	// Modes is the protocol 1 enabled-modes byte reported before
	// SET_CONFIG (default 0xBF, as the hat in testdata reports).
	Modes byte
	// SimplexOnly refuses a duplex SET_CONFIG (NAK 6), like a
	// single-transceiver hat.
	SimplexOnly bool
	// Space is the transmit FIFO of each slot, in bursts (default 10).
	Space int
	// AutoDrain empties the FIFOs before each GET_STATUS reply, as if the
	// bursts had gone on the air. Without it tests call Drain.
	AutoDrain bool
	// AnyFrequency turns off the SET_FREQ checks below. By default the
	// fake NAKs (reason 4) like an MMDVM_HS dual ADF7021 duplex board:
	// rx or tx must be in a band, pocsag must be in a band, none of the
	// three may be in a banned range, and rx and tx must be UHF.
	AnyFrequency bool
	// DualBand makes the fake a dual-band board, which accepts rx or tx
	// below 420 MHz (the duplex board default does not).
	DualBand bool
	// Loopback sends every accepted transmit burst back as a received one.
	Loopback bool
}

// DefaultDescription is a real MMDVM_HS dual-hat GET_VERSION text, with the
// firmware author credits replaced.
const DefaultDescription = "MMDVM_HS-v1.6.1 20231115_WPSD 14.7456MHz dual ADF7021 FW by AUTHORS. GitID #7e16099"

// UDID1 is the placeholder unique ID the fake sends after the protocol 1
// description: 24 hex digits, all zero.
const UDID1 = "000000000000000000000000"

// ErrNotConnected is returned when no host link is open.
var ErrNotConnected = errors.New("fake: no host connected")

// Modem is a fake MMDVM modem.
type Modem struct {
	o Options

	mu       sync.Mutex
	changed  chan struct{}
	port     io.ReadWriteCloser
	opens    int
	silent   bool
	loopback bool
	cmds     []mmdvm.Frame

	configured bool
	duplex     bool
	modes      byte
	cc         uint8
	mode       byte
	tx         bool // duplex downlink started
	fifo       [2]int
	sent       [2][]dmr.Burst
	aborts     [2]int
	shortLC    [][]byte
	freq       []byte
	vseq       [2]uint8
}

// New returns a fake modem.
func New(o Options) *Modem {
	if o.Protocol == 0 {
		o.Protocol = 1
	}
	if o.Description == "" {
		o.Description = DefaultDescription
	}
	if o.Modes == 0 {
		o.Modes = 0xBF
	}
	if o.Space <= 0 {
		o.Space = 10
	}
	return &Modem{o: o, changed: make(chan struct{}), loopback: o.Loopback}
}

// Open is an mmdvm.Settings.Open function: each call makes a new link,
// like opening the serial port again, and resets the modem as a reboot
// would. An earlier link is closed.
func (m *Modem) Open() (io.ReadWriteCloser, error) {
	host, dev := link()
	m.mu.Lock()
	if m.port != nil {
		_ = m.port.Close()
	}
	m.opens++
	m.port = dev
	m.mu.Unlock()
	go m.Serve(dev)
	return host, nil
}

// Serve answers the host on rw until it fails or closes.
func (m *Modem) Serve(rw io.ReadWriteCloser) {
	m.mu.Lock()
	m.port = rw
	m.configured, m.duplex, m.mode, m.tx = false, false, mmdvm.ModeIdle, false
	m.fifo = [2]int{}
	m.vseq = [2]uint8{}
	m.notifyLocked()
	m.mu.Unlock()

	var dec mmdvm.Decoder
	buf := make([]byte, 512)
	for {
		n, err := rw.Read(buf)
		dec.Feed(buf[:n], func(f mmdvm.Frame) { m.handle(rw, f) })
		if err != nil {
			break
		}
	}
	m.mu.Lock()
	if m.port == rw {
		m.port = nil
	}
	m.notifyLocked()
	m.mu.Unlock()
	_ = rw.Close()
}

func (m *Modem) notifyLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// Wait blocks until cond (called with no lock held) is true, rechecking it
// whenever the modem's state changes.
func (m *Modem) Wait(ctx context.Context, cond func() bool) error {
	for {
		m.mu.Lock()
		ch := m.changed
		m.mu.Unlock()
		if cond() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (m *Modem) handle(w io.Writer, f mmdvm.Frame) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyLocked()
	m.cmds = append(m.cmds, mmdvm.Frame{Cmd: f.Cmd, Payload: append([]byte(nil), f.Payload...)})
	if m.silent {
		return
	}
	reply := func(b []byte) { _, _ = w.Write(b) }
	nak := func(r mmdvm.Reason) { reply(mmdvm.Nak(f.Cmd, r)) }
	p := f.Payload
	switch f.Cmd {
	case mmdvm.CmdGetVersion:
		v := mmdvm.Version{Protocol: m.o.Protocol, Description: m.o.Description,
			Cap1: 0x02, CPU: mmdvm.CPUST, UDID: make([]byte, mmdvm.UDIDLen)}
		if m.o.Protocol == 1 {
			v.UDID = []byte(UDID1)
		}
		b, err := mmdvm.EncodeVersion(v)
		if err == nil {
			reply(b)
		}
	case mmdvm.CmdGetStatus:
		if m.o.AutoDrain {
			m.fifo = [2]int{}
		}
		modes := m.modes
		if !m.configured {
			modes = m.o.Modes
		}
		s := mmdvm.Status{Protocol: m.o.Protocol, EnabledModes: modes, Mode: m.mode,
			DMRSpace1: m.o.Space - m.fifo[0], DMRSpace2: m.o.Space - m.fifo[1]}
		if m.tx || m.fifo[0]+m.fifo[1] > 0 {
			s.Flags |= mmdvm.FlagTX
		}
		if !m.duplex {
			s.DMRSpace1 = 10 // what simplex firmware reports for slot 1
		}
		b, _ := mmdvm.EncodeStatus(s)
		reply(b)
	case mmdvm.CmdSetConfig:
		r := m.setConfig(p)
		if r != 0 {
			nak(r)
			return
		}
		reply(mmdvm.Ack(f.Cmd))
	case mmdvm.CmdSetFreq:
		if len(p) < 9 || (!m.o.AnyFrequency && !m.freqOK(p)) {
			nak(mmdvm.ReasonBadData)
			return
		}
		m.freq = append([]byte(nil), p...)
		reply(mmdvm.Ack(f.Cmd))
	case mmdvm.CmdSetMode:
		if len(p) != 1 || !m.modeEnabled(p[0]) {
			nak(mmdvm.ReasonBadData)
			return
		}
		m.mode = p[0]
		if m.mode != mmdvm.ModeDMR {
			m.tx = false
			m.fifo = [2]int{}
		}
		reply(mmdvm.Ack(f.Cmd))
	case mmdvm.CmdDMRData1, mmdvm.CmdDMRData2:
		i := 1
		if f.Cmd == mmdvm.CmdDMRData1 {
			i = 0
		}
		switch {
		case !m.configured || m.modes&0x02 == 0 || (m.mode != mmdvm.ModeIdle && m.mode != mmdvm.ModeDMR):
			nak(mmdvm.ReasonWrongState)
		case i == 0 && !m.duplex:
			nak(mmdvm.ReasonWrongState)
		case len(p) != 1+dmr.BurstLen:
			nak(mmdvm.ReasonBadData)
		case m.fifo[i] >= m.o.Space:
			nak(mmdvm.ReasonNoSpace)
		default:
			m.mode = mmdvm.ModeDMR
			m.fifo[i]++
			var b dmr.Burst
			copy(b[:], p[1:])
			m.sent[i] = append(m.sent[i], b)
			if m.loopback {
				reply(mmdvm.DMRData(i+1, m.control(i, &b), &b))
			}
		}
	case mmdvm.CmdDMRStart:
		switch {
		case !m.duplex:
			nak(mmdvm.ReasonWrongState)
		case len(p) != 1 || p[0] > 1 || m.mode != mmdvm.ModeDMR:
			nak(mmdvm.ReasonBadData)
		default:
			m.tx = p[0] == 1
		}
	case mmdvm.CmdDMRAbort:
		switch {
		case !m.duplex:
			nak(mmdvm.ReasonWrongState)
		case len(p) != 1 || (p[0] != 1 && p[0] != 2):
			nak(mmdvm.ReasonBadData)
		default:
			m.fifo[p[0]-1] = 0
			m.aborts[p[0]-1]++
		}
	case mmdvm.CmdDMRShortLC:
		if !m.duplex || len(p) != mmdvm.ShortLCLen {
			nak(mmdvm.ReasonBadData)
			return
		}
		m.shortLC = append(m.shortLC, append([]byte(nil), p...))
	default:
		nak(mmdvm.ReasonInvalidCommand)
	}
}

func (m *Modem) modeEnabled(mode byte) bool {
	switch mode {
	case mmdvm.ModeIdle:
		return true
	case mmdvm.ModeDMR:
		return m.modes&0x02 != 0
	}
	return false
}

// setConfig checks and applies a SET_CONFIG payload; nonzero is a NAK.
func (m *Modem) setConfig(p []byte) mmdvm.Reason {
	var delay, initial, cc byte
	switch m.o.Protocol {
	case 1:
		if len(p) != 23 {
			return mmdvm.ReasonBadData
		}
		delay, initial, cc = p[2], p[3], p[6]
	default:
		if len(p) != 37 {
			return mmdvm.ReasonBadData
		}
		delay, initial, cc = p[3], p[4], p[26]
	}
	duplex := p[0]&0x80 == 0
	switch {
	case delay > 50, cc > 15:
		return mmdvm.ReasonBadData
	case duplex && m.o.SimplexOnly:
		return mmdvm.ReasonNoDuplex
	}
	m.modes = p[1]
	if !m.modeEnabled(initial) {
		return mmdvm.ReasonBadData
	}
	m.configured, m.duplex, m.cc, m.mode = true, duplex, cc, initial
	return 0
}

// control is the control byte the modem would report for a burst it
// received: from the sync, the slot type, or the position after sync.
func (m *Modem) control(i int, b *dmr.Burst) byte {
	s, _ := dmr.MatchSync(b, dmr.SyncMaxBitError)
	switch {
	case s.IsVoice():
		m.vseq[i] = 0
		return mmdvm.ControlVoiceSync
	case s.IsData():
		st, _ := b.SlotType()
		return mmdvm.Control(dmr.FrameDataSync, uint8(st.DataType))
	}
	m.vseq[i]++
	if m.vseq[i] > 5 {
		m.vseq[i] = 5
	}
	return m.vseq[i]
}

// ---- test controls ----

func (m *Modem) send(b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return ErrNotConnected
	}
	if m.silent {
		return nil
	}
	_, err := m.port.Write(b)
	return err
}

// Inject sends a raw frame to the host.
func (m *Modem) Inject(b []byte) error { return m.send(b) }

// InjectDMR sends one received burst on slot 1 or 2 with the given control
// byte (see mmdvm.ParseControl).
func (m *Modem) InjectDMR(slot int, control byte, b dmr.Burst) error {
	return m.send(mmdvm.DMRData(slot, control, &b))
}

// InjectFrames sends the bursts of a call as received on slot.
func (m *Modem) InjectFrames(slot int, fs []dmr.TXFrame) error {
	for _, f := range fs {
		if err := m.InjectDMR(slot, mmdvm.Control(f.FrameType, f.DTypeVSeq), f.Burst); err != nil {
			return err
		}
	}
	return nil
}

// InjectLost sends DMR_LOST for slot.
func (m *Modem) InjectLost(slot int) error { return m.send(mmdvm.DMRLost(slot)) }

// SetSilent makes the modem read and log commands but answer nothing.
func (m *Modem) SetSilent(on bool) {
	m.mu.Lock()
	m.silent = on
	m.notifyLocked()
	m.mu.Unlock()
}

// SetLoopback turns loopback of transmitted bursts on or off.
func (m *Modem) SetLoopback(on bool) {
	m.mu.Lock()
	m.loopback = on
	m.mu.Unlock()
}

// Drain removes up to n bursts from the FIFO of slot, as if sent.
func (m *Modem) Drain(slot, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slot - 1
	m.fifo[i] = max(0, m.fifo[i]-n)
	m.notifyLocked()
}

// Sent returns a copy of the bursts accepted for slot 1 or 2.
func (m *Modem) Sent(slot int) []dmr.Burst {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]dmr.Burst(nil), m.sent[slot-1]...)
}

// Commands returns a copy of every frame the host sent.
func (m *Modem) Commands() []mmdvm.Frame {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mmdvm.Frame(nil), m.cmds...)
}

// Count returns how many frames of cmd the host sent.
func (m *Modem) Count(cmd byte) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, f := range m.cmds {
		if f.Cmd == cmd {
			n++
		}
	}
	return n
}

// State is a snapshot of the modem.
type State struct {
	Connected  bool
	Opens      int
	Configured bool
	Duplex     bool
	Mode       byte
	TX         bool // duplex downlink started
	FIFO       [2]int
	Aborts     [2]int
	ColorCode  uint8
	Freq       []byte
	ShortLCs   int
}

// State returns a snapshot of the modem.
func (m *Modem) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return State{Connected: m.port != nil, Opens: m.opens, Configured: m.configured,
		Duplex: m.duplex, Mode: m.mode, TX: m.tx, FIFO: m.fifo, Aborts: m.aborts,
		ColorCode: m.cc, Freq: append([]byte(nil), m.freq...), ShortLCs: len(m.shortLC)}
}

// freqOK mirrors CIO::setFreq and checkZUMspot in MMDVM_HS (IO.cpp). The
// payload is a pad byte, RX and TX (u32 LE), power, and optionally the
// POCSAG frequency; the firmware uses TX when that field is omitted.
func (m *Modem) freqOK(p []byte) bool {
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(p[i:]) }
	rx, tx := u(1), u(5)
	pocsag := tx
	if len(p) >= 14 {
		pocsag = u(10)
	}
	inBand := func(f uint32) bool {
		return (f >= 144_000_000 && f < 148_000_000) || (f >= 219_000_000 && f < 225_000_000) ||
			(f >= 420_000_000 && f < 475_000_000) || (f >= 842_000_000 && f < 950_000_000)
	}
	banned := func(f uint32) bool {
		return (f >= 145_800_000 && f <= 146_000_000) || (f >= 435_000_000 && f <= 438_000_000)
	}
	if !inBand(rx) && !inBand(tx) || !inBand(pocsag) {
		return false
	}
	if banned(rx) || banned(tx) || banned(pocsag) {
		return false
	}
	if !m.o.DualBand && (rx < 420_000_000 || tx < 420_000_000) {
		return false
	}
	return true
}
