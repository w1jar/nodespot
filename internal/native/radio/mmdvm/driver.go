package mmdvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// DefaultBaud is the UART speed of MMDVM_HS hats. Full-size MMDVM boards
// default to 460800.
const DefaultBaud = 115200

// Timing defaults, the same as MMDVMHost's.
const (
	DefaultStatusInterval  = 250 * time.Millisecond
	DefaultInactivity      = 2 * time.Second
	DefaultBootWait        = 2 * time.Second
	DefaultVersionAttempts = 6
	DefaultVersionRetry    = 1500 * time.Millisecond
	DefaultReplyTimeout    = 300 * time.Millisecond // 30 reads of 10 ms
	DefaultPlayout         = 10 * time.Millisecond
	DefaultTXHang          = 4 * time.Second
	DefaultReopenDelay     = 2 * time.Second
	DefaultRXTimeout       = 500 * time.Millisecond
	DefaultQueueLen        = 100 // bursts per slot, 6 s of voice
	DefaultSimplexSlot     = 2
)

// Errors from the transmit methods.
var (
	ErrTXInhibited = errors.New("mmdvm: transmit is not allowed")
	ErrNotOpen     = errors.New("mmdvm: the modem is not open")
	ErrNoCall      = errors.New("mmdvm: no such transmission")
	ErrBadSlot     = errors.New("mmdvm: no such slot")
	ErrBadFrame    = errors.New("mmdvm: need a 7-byte AMBE frame")
	ErrNoAnswer    = errors.New("mmdvm: the modem stopped answering")
	ErrClosed      = errors.New("mmdvm: driver closed")
)

// errReopen ends a session when Reopen is called.
var errReopen = errors.New("mmdvm: reopen asked")

// Settings configure a [Driver]. Zero durations and counts take the
// defaults above.
type Settings struct {
	// Port is the serial device; Baud its speed (default 115200).
	Port string
	Baud int
	// Open opens the modem link instead of the serial port (tests, the
	// fake modem). Close on the returned link must end a Read in progress.
	Open func() (io.ReadWriteCloser, error)

	// Modem is the SET_CONFIG content. DMR is always enabled and the
	// initial mode is always idle; Modem.Duplex picks duplex (both slots)
	// or simplex.
	Modem Config
	// Freq is sent with SET_FREQ; it is skipped when both frequencies are 0.
	Freq Freq
	// AllowTX lets the node transmit. Without it StartTX fails and the
	// duplex downlink is never started.
	AllowTX bool
	// SimplexSlot is the slot number a simplex modem's one slot has
	// (default 2).
	SimplexSlot int
	// DetectOnly only identifies and watches the modem: GET_VERSION, then
	// status polls. SET_FREQ and SET_CONFIG are not sent, nothing is sent
	// at shutdown, and AllowTX is forced off, so the modem never keys up.
	DetectOnly bool

	StatusInterval  time.Duration
	Inactivity      time.Duration // no status reply this long: close and reopen
	BootWait        time.Duration // wait after opening, before GET_VERSION
	VersionAttempts int
	VersionRetry    time.Duration
	ReplyTimeout    time.Duration // wait for a reply or an ACK
	Playout         time.Duration // transmit tick: at most one burst per slot
	TXHang          time.Duration // duplex downlink kept on after the last activity
	ReopenDelay     time.Duration
	RXTimeout       time.Duration // a received call with no burst this long ends
	QueueLen        int

	Logger *slog.Logger
}

func (s *Settings) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&s.StatusInterval, DefaultStatusInterval)
	def(&s.Inactivity, DefaultInactivity)
	def(&s.BootWait, DefaultBootWait)
	def(&s.VersionRetry, DefaultVersionRetry)
	def(&s.ReplyTimeout, DefaultReplyTimeout)
	def(&s.Playout, DefaultPlayout)
	def(&s.TXHang, DefaultTXHang)
	def(&s.ReopenDelay, DefaultReopenDelay)
	def(&s.RXTimeout, DefaultRXTimeout)
	if s.VersionAttempts <= 0 {
		s.VersionAttempts = DefaultVersionAttempts
	}
	if s.QueueLen <= 0 {
		s.QueueLen = DefaultQueueLen
	}
	if s.SimplexSlot != 1 {
		s.SimplexSlot = DefaultSimplexSlot
	}
	if s.Baud == 0 {
		s.Baud = DefaultBaud
	}
	if s.Open == nil {
		port, baud := s.Port, s.Baud
		s.Open = func() (io.ReadWriteCloser, error) { return OpenSerial(port, baud) }
	}
	if s.Logger == nil {
		s.Logger = slog.New(slog.DiscardHandler)
	}
	s.Modem.Modes.DMR = true
	s.Modem.InitialMode = ModeIdle
	if s.DetectOnly {
		s.AllowTX = false
	}
}

// Driver runs an MMDVM modem as a [radio.DigitalRadio] for DMR.
//
// Receive: DMR_DATA bursts are parsed with [dmr.RX] into call headers (from
// the voice LC header or, for late entry, the embedded LC), AMBE frames and
// ends (terminator, DMR_LOST, or RXTimeout of silence).
//
// Transmit: StartTX, WriteVoice and EndTX build bursts with [dmr.TX]
// (BS-sourced sync on a duplex modem, MS-sourced on a simplex one) into a
// queue per slot. The session writes at most one burst per slot every
// Playout tick, and only while the free space the modem last reported is
// above 1, counting it down per burst. A status reply resets the count to
// the reported space less the bursts written after its GET_STATUS went out,
// which the reply cannot include yet.
type Driver struct {
	s      Settings
	log    *slog.Logger
	duplex bool

	closeOnce sync.Once
	closed    chan struct{}
	kick      chan struct{}
	reopen    chan struct{}

	mu      sync.Mutex
	st      radio.ModemStatus
	open    bool
	proto   int
	space   [2]int
	written [2]uint64   // bursts written per slot, this session
	polls   [][2]uint64 // written at each GET_STATUS still unanswered
	tx      [2]txSlot
	started bool // duplex downlink on
	lastAct time.Time
	abort   bool // Abort called: send the abort sequence
	wake    bool // RX on a stopped duplex downlink: start it
	running bool

	rxMu sync.Mutex // RX state and every Sink call
	rx   [2]rxSlot
	sink network.Sink
	ids  uint64
}

type txSlot struct {
	call  uint64
	tx    *dmr.TX
	queue []dmr.Burst
}

type rxSlot struct {
	rx     *dmr.RX
	active bool
	call   uint64
	lc     dmr.LC
	seq    uint32
	last   time.Time
}

var _ radio.DigitalRadio = (*Driver)(nil)

// New returns a driver; Run opens the modem.
func New(s Settings) *Driver {
	s.defaults()
	d := &Driver{s: s, log: s.Logger, duplex: s.Modem.Duplex,
		closed: make(chan struct{}), kick: make(chan struct{}, 1), reopen: make(chan struct{}, 1)}
	d.st.Duplex = d.duplex
	for i := range d.rx {
		d.rx[i].rx = dmr.NewRX()
	}
	return d
}

// Slots is 2 for a duplex modem and 1 for a simplex one.
func (d *Driver) Slots() int {
	if d.duplex {
		return 2
	}
	return 1
}

// slotIndex maps a call's slot to a queue: slot-1 on a duplex modem, the
// one queue (index 1, DMR_DATA2) on a simplex one.
func (d *Driver) slotIndex(slot int) (int, bool) {
	if !d.duplex {
		return 1, true
	}
	if slot != 1 && slot != 2 {
		return 0, false
	}
	return slot - 1, true
}

// slotNumber is the slot a received burst on queue i is reported as.
func (d *Driver) slotNumber(i int) int {
	if !d.duplex {
		return d.s.SimplexSlot
	}
	return i + 1
}

// Status is a snapshot of the modem.
func (d *Driver) Status() radio.ModemStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

// Close stops Run. It does not wait for Run to return.
func (d *Driver) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

// Reopen closes the modem and opens it again at once (a "reset" from the
// UI). It does nothing when Run is not running.
func (d *Driver) Reopen() {
	select {
	case d.reopen <- struct{}{}:
	default:
	}
}

func (d *Driver) poke() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// ---- transmit ----

// StartTX starts a transmission on h.Slot (any slot on a simplex modem).
// A transmission already on the slot is ended first.
func (d *Driver) StartTX(h network.CallHeader) error {
	if !d.s.AllowTX {
		return ErrTXInhibited
	}
	i, ok := d.slotIndex(h.Slot)
	if !ok {
		return ErrBadSlot
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.open {
		return ErrNotOpen
	}
	t := &d.tx[i]
	if t.tx != nil {
		d.queueLocked(i, t.tx.End())
	}
	t.call = h.Call
	t.tx = dmr.NewTX(dmr.TXConfig{Src: h.SrcID, Dst: h.TG, Private: h.Private,
		CC: d.s.Modem.ColorCode, MSSync: !d.duplex})
	d.queueLocked(i, t.tx.Start())
	return nil
}

// WriteVoice adds one AMBE frame to the transmission f.Call.
func (d *Driver) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != dmr.AMBELen {
		return ErrBadFrame
	}
	var a dmr.AMBE
	copy(a[:], f.Data)
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.tx {
		if t := &d.tx[i]; t.tx != nil && t.call == f.Call {
			d.queueLocked(i, t.tx.Write(a))
			return nil
		}
	}
	return ErrNoCall
}

// EndTX ends the transmission on slot: the last burst is padded with
// silence and the terminator follows.
func (d *Driver) EndTX(slot int) error {
	i, ok := d.slotIndex(slot)
	if !ok {
		return ErrBadSlot
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := &d.tx[i]
	if t.tx == nil {
		return ErrNoCall
	}
	d.queueLocked(i, t.tx.End())
	t.tx, t.call = nil, 0
	return nil
}

// Abort drops every queued burst and stops the transmitter: DMR_ABORT for
// both slots and DMR_START 0 on a duplex modem, then SET_MODE idle.
func (d *Driver) Abort() {
	d.mu.Lock()
	for i := range d.tx {
		d.tx[i] = txSlot{}
	}
	d.abort = d.open && !d.s.DetectOnly
	d.mu.Unlock()
	d.poke()
}

func (d *Driver) queueLocked(i int, fs []dmr.TXFrame) {
	for _, f := range fs {
		if len(d.tx[i].queue) >= d.s.QueueLen {
			d.st.Dropped++
			continue
		}
		d.tx[i].queue = append(d.tx[i].queue, f.Burst)
	}
	if len(fs) > 0 {
		d.poke()
	}
}

// ---- run ----

// Run opens the modem, configures it and serves it until ctx ends or Close
// is called. A modem that cannot be opened, refuses its configuration or
// stops answering is closed and opened again after ReopenDelay.
func (d *Driver) Run(ctx context.Context, sink network.Sink) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return errors.New("mmdvm: already running")
	}
	d.running = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-d.closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	d.rxMu.Lock()
	d.sink = sink
	d.rxMu.Unlock()
	select { // a Reopen from before Run has nothing to reopen
	case <-d.reopen:
	default:
	}
	for {
		err := d.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		d.mu.Lock()
		d.st.Reopens++
		if errors.Is(err, errReopen) {
			d.st.Error = ""
		} else {
			d.st.Error = err.Error()
		}
		d.mu.Unlock()
		if errors.Is(err, errReopen) {
			d.log.Info("mmdvm: reopening the modem")
			continue
		}
		d.log.Warn("mmdvm: modem closed", "err", err)
		t := time.NewTimer(d.s.ReopenDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-d.reopen:
		case <-t.C:
		}
		t.Stop()
	}
}

// session is one open of the modem.
func (d *Driver) session(ctx context.Context) error {
	port, err := d.s.Open()
	if err != nil {
		return err
	}
	frames := make(chan Frame, 64)
	rdErr := make(chan error, 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.reader(port, frames, rdErr, done)
	}()
	defer func() {
		d.mu.Lock()
		d.open, d.started, d.abort, d.wake = false, false, false, false
		d.space = [2]int{}
		d.written = [2]uint64{}
		d.polls = nil
		for i := range d.tx {
			d.tx[i] = txSlot{}
		}
		d.st.Open, d.st.TX = false, false
		d.mu.Unlock()
		close(done)
		_ = port.Close()
		wg.Wait()
		d.endAllRX()
	}()

	w := &writer{port: port}
	if err := d.startup(ctx, w, frames, rdErr); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	now := time.Now()
	d.mu.Lock()
	d.open = true
	d.st.Open = true
	d.st.Error = ""
	d.st.LastStatus = now
	d.mu.Unlock()
	d.log.Info("mmdvm: modem ready", "protocol", d.proto, "description", d.st.Description,
		"duplex", d.duplex, "detectOnly", d.s.DetectOnly)

	statusT := time.NewTicker(d.s.StatusInterval)
	defer statusT.Stop()
	playT := time.NewTicker(d.s.Playout)
	defer playT.Stop()
	lastStatus := now
	if err := d.poll(w); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			d.shutdown(w)
			return ctx.Err()
		case <-d.reopen:
			d.shutdown(w)
			return errReopen
		case err := <-rdErr:
			return err
		case f := <-frames:
			if f.Cmd == CmdGetStatus {
				lastStatus = time.Now()
			}
			d.onFrame(f)
		case <-statusT.C:
			if time.Since(lastStatus) > d.s.Inactivity {
				return ErrNoAnswer
			}
			if err := d.poll(w); err != nil {
				return err
			}
		case <-playT.C:
			if err := d.playout(w); err != nil {
				return err
			}
		case <-d.kick:
			if err := d.playout(w); err != nil {
				return err
			}
		}
	}
}

// poll sends GET_STATUS, noting how many bursts had been written by then.
func (d *Driver) poll(w *writer) error {
	d.mu.Lock()
	if len(d.polls) < 16 {
		d.polls = append(d.polls, d.written)
	}
	d.mu.Unlock()
	return w.write(GetStatus())
}

type writer struct{ port io.Writer }

func (w *writer) write(b ...[]byte) error {
	for _, f := range b {
		if _, err := w.port.Write(f); err != nil {
			return fmt.Errorf("mmdvm: write: %w", err)
		}
	}
	return nil
}

// reader decodes the link. Received DMR goes to the sink at once; every
// other frame goes to the session.
func (d *Driver) reader(port io.Reader, frames chan<- Frame, rdErr chan<- error, done <-chan struct{}) {
	var dec Decoder
	buf := make([]byte, 512)
	for {
		n, err := port.Read(buf)
		if n > 0 {
			dec.Feed(buf[:n], func(f Frame) {
				switch f.Cmd {
				case CmdDMRData1, CmdDMRData2, CmdDMRLost1, CmdDMRLost2:
					d.onRX(f)
				case CmdDebug1, CmdDebug2, CmdDebug3, CmdDebug4, CmdDebug5, CmdDebugDump:
					d.log.Debug("mmdvm: modem debug", "text", cleanText(f.Payload))
				default:
					select {
					case frames <- f:
					case <-done:
					}
				}
			})
		}
		if err != nil {
			select {
			case rdErr <- fmt.Errorf("mmdvm: read: %w", err):
			default:
			}
			return
		}
	}
}

// startup runs GET_VERSION, SET_FREQ and SET_CONFIG.
func (d *Driver) startup(ctx context.Context, w *writer, frames <-chan Frame, rdErr <-chan error) error {
	if err := sleepCtx(ctx, d.s.BootWait); err != nil {
		return err
	}
	var v Version
	var verr error
	for attempt := 0; attempt < d.s.VersionAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, d.s.VersionRetry); err != nil {
				return err
			}
		}
		if err := w.write(GetVersion()); err != nil {
			return err
		}
		f, err := d.await(ctx, frames, rdErr, CmdGetVersion)
		if err != nil {
			verr = err
			if errors.Is(err, errTimeout) {
				continue
			}
			return err
		}
		v, verr = ParseVersion(f.Payload)
		break
	}
	if verr != nil {
		if errors.Is(verr, errTimeout) {
			return errors.New("mmdvm: the modem does not answer GET_VERSION")
		}
		return verr
	}
	if !v.DMR() {
		return fmt.Errorf("mmdvm: the modem (%s) has no DMR", v.Description)
	}
	b := ParseBoard(v.Description)
	d.mu.Lock()
	d.proto = v.Protocol
	d.st.Protocol, d.st.Description = v.Protocol, v.Description
	d.st.Board, d.st.Firmware, d.st.BuildDate = b.Name, b.Version, b.BuildDate
	d.st.DuplexCapable = b.DuplexCapable
	d.mu.Unlock()

	if d.s.DetectOnly {
		return nil
	}
	if d.s.Freq.RX != 0 || d.s.Freq.TX != 0 {
		if err := d.command(ctx, w, frames, rdErr, SetFreq(d.s.Freq)); err != nil {
			return err
		}
	}
	cfg, err := SetConfig(v.Protocol, d.s.Modem)
	if err != nil {
		return err
	}
	return d.command(ctx, w, frames, rdErr, cfg)
}

var errTimeout = errors.New("mmdvm: no reply")

// command writes a command that the modem ACKs and waits for the ACK.
func (d *Driver) command(ctx context.Context, w *writer, frames <-chan Frame, rdErr <-chan error, b []byte) error {
	cmd := b[2]
	if err := w.write(b); err != nil {
		return err
	}
	if _, err := d.await(ctx, frames, rdErr, cmd); err != nil {
		if errors.Is(err, errTimeout) {
			return fmt.Errorf("mmdvm: no answer to %s", CommandName(cmd))
		}
		return err
	}
	return nil
}

// await waits ReplyTimeout for the reply to cmd: the frame of that command,
// or an ACK or NAK of it.
func (d *Driver) await(ctx context.Context, frames <-chan Frame, rdErr <-chan error, cmd byte) (Frame, error) {
	t := time.NewTimer(d.s.ReplyTimeout)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return Frame{}, ctx.Err()
		case err := <-rdErr:
			return Frame{}, err
		case <-t.C:
			return Frame{}, errTimeout
		case f := <-frames:
			switch f.Cmd {
			case cmd:
				return f, nil
			case CmdACK, CmdNAK:
				if acked, err := ParseAck(f); acked == cmd {
					if err != nil {
						d.countNAK(err)
					}
					return f, err
				}
			}
			d.onFrame(f)
		}
	}
}

func sleepCtx(ctx context.Context, dur time.Duration) error {
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *Driver) countNAK(err error) {
	d.mu.Lock()
	d.st.NAKs++
	d.st.Error = err.Error()
	d.mu.Unlock()
}

// onFrame handles a frame outside start-up: status replies, ACKs and NAKs.
func (d *Driver) onFrame(f Frame) {
	switch f.Cmd {
	case CmdGetStatus:
		d.mu.Lock()
		defer d.mu.Unlock()
		s, err := ParseStatus(d.proto, f.Payload)
		if err != nil {
			d.log.Debug("mmdvm: bad status", "err", err)
			return
		}
		// The reply counts the bursts written before its GET_STATUS; the
		// ones written since then are not in it yet.
		since := d.written
		if len(d.polls) > 0 {
			since = d.polls[0]
			d.polls = d.polls[1:]
		}
		d.space = [2]int{s.DMRSpace1 - int(d.written[0]-since[0]), s.DMRSpace2 - int(d.written[1]-since[1])}
		d.st.Mode = int(s.Mode)
		d.st.State = ModeName(s.Mode)
		d.st.Flags = int(s.Flags)
		if d.proto < 2 {
			d.st.Modes = ModeNames(s.EnabledModes)
		}
		if s.Flags&FlagRXOverflow != 0 && !d.st.RXOverflow {
			d.st.RXOverflows++
		}
		if s.Flags&FlagTXOverflow != 0 && !d.st.TXOverflow {
			d.st.TXOverflows++
		}
		d.st.TX = s.TX()
		d.st.ADCOverflow = s.Flags&FlagADCOverflow != 0 && d.proto >= 2
		d.st.RXOverflow = s.Flags&FlagRXOverflow != 0
		d.st.TXOverflow = s.Flags&FlagTXOverflow != 0
		d.st.Lockout = s.Flags&FlagLockout != 0 && d.proto >= 2
		d.st.DACOverflow = s.Flags&FlagDACOverflow != 0 && d.proto >= 2
		d.st.CarrierDetect = s.Flags&FlagCD != 0 && d.proto >= 2
		d.st.Space = d.space
		d.st.LastStatus = time.Now()
	case CmdACK:
	case CmdNAK:
		_, err := ParseAck(f)
		if err != nil {
			d.log.Debug("mmdvm: NAK", "err", err)
			d.countNAK(err)
		}
	default:
		d.log.Debug("mmdvm: unexpected frame", "frame", f.String())
	}
}

// playout sends the abort sequence, starts or stops the duplex downlink and
// writes queued bursts as the modem's free space allows.
func (d *Driver) playout(w *writer) error {
	now := time.Now()
	d.checkRXTimeout(now)

	var out [][]byte
	d.mu.Lock()
	if d.abort {
		d.abort = false
		for i := range d.tx {
			d.tx[i] = txSlot{}
		}
		if d.duplex {
			out = append(out, DMRAbort(1), DMRAbort(2), DMRStart(false))
		}
		out = append(out, SetMode(ModeIdle))
		d.started, d.wake = false, false
	}
	queued := false
	for i := range d.tx {
		if len(d.tx[i].queue) > 0 {
			queued = true
		}
	}
	if d.duplex && d.s.AllowTX && !d.started && (queued || d.wake) {
		out = append(out, SetMode(ModeDMR), DMRStart(true))
		d.started = true
		d.lastAct = now
	}
	d.wake = false
	for i := range d.tx {
		t := &d.tx[i]
		if len(t.queue) == 0 || d.space[i] <= 1 {
			continue
		}
		b := t.queue[0]
		t.queue = t.queue[1:]
		d.space[i]--
		d.written[i]++
		out = append(out, DMRData(i+1, 0, &b))
		d.lastAct = now
	}
	if d.duplex && d.started && !queued && d.tx[0].tx == nil && d.tx[1].tx == nil && now.Sub(d.lastAct) > d.s.TXHang {
		out = append(out, DMRStart(false))
		d.started = false
	}
	d.mu.Unlock()
	return w.write(out...)
}

// shutdown stops transmission before the port closes. A detect-only
// driver never configured the modem and sends nothing.
func (d *Driver) shutdown(w *writer) {
	if d.s.DetectOnly {
		return
	}
	var out [][]byte
	if d.duplex {
		out = append(out, DMRAbort(1), DMRAbort(2), DMRStart(false))
	}
	out = append(out, SetMode(ModeIdle))
	_ = w.write(out...)
}

// ---- receive ----

func (d *Driver) onRX(f Frame) {
	var i int
	switch f.Cmd {
	case CmdDMRData1, CmdDMRLost1:
		i = 0
	default:
		i = 1
	}
	if !d.duplex {
		i = 1 // a simplex modem reports everything on DMR_DATA2
	}
	if f.Cmd == CmdDMRLost1 || f.Cmd == CmdDMRLost2 {
		d.rxMu.Lock()
		d.endRXLocked(i)
		d.rxMu.Unlock()
		return
	}
	df, err := ParseDMRData(f)
	if err != nil {
		return
	}
	ft, v, err := ParseControl(df.Control)
	if err != nil {
		return
	}
	now := time.Now()
	d.mu.Lock()
	if d.duplex && d.s.AllowTX && !d.started {
		d.wake = true
	} else if d.started {
		d.lastAct = now
	}
	d.mu.Unlock()
	if d.duplex {
		d.poke()
	}

	d.rxMu.Lock()
	defer d.rxMu.Unlock()
	r := &d.rx[i]
	res := r.rx.Push(ft, v, &df.Burst)
	r.last = now
	if res.LCValid {
		if r.active && (res.LC.Src != r.lc.Src || res.LC.Dst != r.lc.Dst || res.LC.FLCO != r.lc.FLCO) {
			d.endRXLocked(i)
		}
		if !r.active {
			d.ids++
			r.active, r.call, r.lc, r.seq = true, d.ids, res.LC, 0
			d.sinkHeader(network.CallHeader{Call: r.call, Src: strconv.FormatUint(uint64(res.LC.Src), 10),
				SrcID: res.LC.Src, TG: res.LC.Dst, Slot: d.slotNumber(i), Private: res.LC.Private()})
		}
	}
	if res.HasVoice && r.active {
		for k := range res.Voice {
			d.sinkVoice(network.VoiceFrame{Call: r.call, Seq: r.seq, Codec: network.CodecAMBE,
				Data: append([]byte(nil), res.Voice[k][:]...)})
			r.seq++
		}
	}
	if res.End {
		d.endRXLocked(i) // Push has reset the parser
	}
}

func (d *Driver) sinkHeader(h network.CallHeader) {
	if d.sink != nil {
		d.sink.Header(h)
	}
}

func (d *Driver) sinkVoice(f network.VoiceFrame) {
	if d.sink != nil {
		d.sink.Voice(f)
	}
}

// endRXLocked ends the received call on queue i, if any.
func (d *Driver) endRXLocked(i int) {
	r := &d.rx[i]
	r.rx.Reset()
	if !r.active {
		return
	}
	r.active = false
	if d.sink != nil {
		d.sink.End(r.call)
	}
}

func (d *Driver) checkRXTimeout(now time.Time) {
	d.rxMu.Lock()
	defer d.rxMu.Unlock()
	for i := range d.rx {
		if d.rx[i].active && now.Sub(d.rx[i].last) > d.s.RXTimeout {
			d.endRXLocked(i)
		}
	}
}

func (d *Driver) endAllRX() {
	d.rxMu.Lock()
	defer d.rxMu.Unlock()
	for i := range d.rx {
		d.endRXLocked(i)
	}
}
