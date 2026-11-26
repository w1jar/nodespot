package node

import (
	"context"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// stepParams are the station settings the 20 ms step uses, derived from
// the config whenever it changes.
type stepParams struct {
	hang      time.Duration
	txTimeout time.Duration
	courtesy  bool
	agc       audio.AGCParams
	modeAGC   bool
	radioAGC  bool
	id        idParams
	callsign  string
	cw        audio.CWOptions
	voice     string

	rxGainDb, txGainDb float64
	preEmph, deEmph    bool
	plFilter           bool
	rxOnDelay          time.Duration
	hangTail           time.Duration // extra time added to hang
}

// paramsFor derives the step settings; sa818 says the radio interface has an
// SA818 (which decides the PL filter default).
func paramsFor(c *Config, sa818 bool) stepParams {
	st := c.Station
	p := stepParams{
		hang:      st.Advanced.Hang(),
		txTimeout: st.Advanced.TxTimeout(),
		courtesy:  st.Advanced.Courtesy(),
		callsign:  st.Callsign,
		cw:        st.ID.CW(),
		voice:     st.ID.VoiceFile,

		rxGainDb: st.Advanced.RxGainDB, txGainDb: st.Advanced.TxGainDB,
		preEmph: st.Advanced.PreEmphasis, deEmph: st.Advanced.DeEmphasis,
		plFilter:  st.Advanced.PL(sa818),
		rxOnDelay: st.Advanced.RxOnDelay(), hangTail: st.Advanced.TxOffDelay(),
		id: idParams{
			on:       st.ID.IDOn() && st.Callsign != "",
			interval: st.ID.Interval(),
			tail:     st.ID.TailWindow(),
			endQuiet: st.ID.EndQuiet(),
		},
	}
	p.agc, p.modeAGC, p.radioAGC = st.Advanced.AGC()
	return p
}

// simplex is the radio-side state the step keeps. Guarded by Engine.mu.
type simplex struct {
	ptt       bool
	hangUntil time.Time
	// pttStuck: unkeying failed, so the transmitter may still be keyed
	// although ptt is false. Every step tries to unkey again.
	pttStuck bool

	cos      bool      // last carrier state seen
	rfStart  time.Time // a local transmission into the receiver began
	rfSentTo mode.Mode // the mode that received its audio
	rfLocked bool      // timed out: ignore the carrier until it drops

	netStart time.Time // the network started keying the transmitter
	txLocked bool      // timed out: ignore the network until it stops

	// Playout prebuffer: at the start of each talk spurt the network's
	// audio waits until prebufFrames are queued (or prebufWait passes)
	// so that network jitter does not cause gaps on the air.
	netPlaying bool
	prebufAt   time.Time // when the spurt's first frame was seen waiting

	tone audio.Source // courtesy tone playing
	id   audio.Source // ID playing
	idT  idTimer

	modeAGC, radioAGC *audio.AGC
	// Station audio conditioning: PL high-pass and de-emphasis on the way
	// from the radio, pre-emphasis on the way to it.
	rxPL     *audio.HighPass
	rxDeemph *audio.Emphasis
	txPre    *audio.Emphasis

	cosSince time.Time // the carrier has been present since (zero: absent)

	// rx is the receive level over the last 100 ms, taken from every frame
	// the sound chip delivers (before the AGC); rxAt is when the last one
	// arrived.
	rx   audio.Rolling
	rxAt time.Time
}

// Playout prebuffer at the start of a talk spurt: 2 frames, or what has
// arrived after 60 ms. The step itself paces playout at 20 ms.
const (
	prebufFrames = 2
	prebufWait   = 60 * time.Millisecond
)

// maxCatchUp bounds how many captured frames one step forwards when the
// sound card clock runs ahead of the step clock.
const maxCatchUp = 3

// Run steps the engine on clock until ctx ends. It drops PTT before it
// returns.
func (e *Engine) Run(ctx context.Context, clock audio.Clock) {
	defer e.dropPTT()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-clock.C():
			e.Step(t)
		}
	}
}

// Step runs one 20 ms frame of the simplex logic at now. It never blocks.
func (e *Engine) Step(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := &e.sx
	p := e.params
	var m mode.Mode
	if e.active != nil {
		m = e.active.m
	}
	if s.pttStuck && !s.ptt {
		e.retryUnkey(now)
	}

	// Radio side: a local station transmitting into our receiver. While
	// we transmit the receiver hears nothing of use (simplex).
	carrier := e.o.Radio.Carrier()
	if carrier != s.cos {
		s.cos = carrier
		e.publishOn(EventCOS, carrier, now)
	}
	// The carrier counts as a received signal after it has been present for
	// the receive-on delay. While we transmit it is not trusted, and the
	// delay starts again from the moment we unkey.
	switch {
	case !carrier:
		s.cosSince = time.Time{}
	case s.ptt, s.cosSince.IsZero():
		s.cosSince = now
	}
	rfActive := carrier && !s.ptt && now.Sub(s.cosSince) >= p.rxOnDelay
	if !carrier {
		s.rfLocked = false
	}
	e.stepRF(now, m, rfActive)

	// Network side: the active mode's audio for the radio.
	var nf audio.Frame
	haveNet, netTalking := false, false
	if m != nil {
		if rfActive {
			drain(m.Recv()) // the receiver has priority
			s.netPlaying, s.prebufAt = false, time.Time{}
		} else {
			e.prebuffer(now, m)
			if s.netPlaying {
				select {
				case nf = <-m.Recv():
					haveNet = true
				default:
				}
			}
		}
		netTalking = haveNet || m.Talking() || len(m.Recv()) > 0
	}
	if !netTalking {
		s.netStart, s.txLocked = time.Time{}, false
		s.netPlaying, s.prebufAt = false, time.Time{}
		s.modeAGC.Reset()
		s.txPre.Reset()
	}
	wantNet := netTalking && !rfActive && !s.txLocked
	if wantNet {
		if s.netStart.IsZero() {
			s.netStart = now
		}
		if now.Sub(s.netStart) >= p.txTimeout {
			s.txLocked, wantNet = true, false
			e.logf(now, "transmit timeout: the network has keyed the radio for %s", p.txTimeout)
		}
	}

	// Mix what goes on the air.
	var out audio.Frame
	if wantNet && haveNet {
		out = nf
		audio.ApplyGain(&out, p.txGainDb)
		if p.preEmph {
			s.txPre.Process(&out)
		}
		if p.modeAGC {
			s.modeAGC.Process(&out, p.agc)
		}
	}
	if rfActive {
		s.tone = nil
	}
	tonePlaying := false
	if s.tone != nil {
		var tf audio.Frame
		if s.tone.Next(&tf) {
			mix(&out, &tf)
			tonePlaying = true
		} else {
			s.tone = nil
		}
	}
	if wantNet || tonePlaying {
		s.idT.transmitted(now, p.id)
	}

	// The station ID: RF only.
	if s.id == nil {
		inTail := s.ptt && !wantNet && !tonePlaying
		if why := s.idT.due(now, p.id, s.ptt, inTail, rfActive); why != idNone {
			if src := e.idSource(now); src != nil {
				if !s.ptt {
					src = &silenceThen{n: leadIn, s: src}
				}
				s.id = src
				s.idT.sent(now, why)
				e.hub.publish(Event{Kind: EventID, At: now, Text: string(why)})
			}
		}
	}
	idPlaying := false
	if s.id != nil {
		var f audio.Frame
		if s.id.Next(&f) {
			mix(&out, &f)
			idPlaying = true
		} else {
			s.id = nil
		}
	}

	// PTT.
	content := wantNet || tonePlaying || idPlaying
	if content {
		s.hangUntil = now.Add(p.hang + p.hangTail)
	}
	want := !rfActive && (content || (s.ptt && now.Before(s.hangUntil)))
	if want != s.ptt {
		e.setPTT(want, now)
	}
	if s.ptt {
		e.o.Radio.Play(out)
	}
}

// prebuffer decides whether the network's audio may start playing.
func (e *Engine) prebuffer(now time.Time, m mode.Mode) {
	s := &e.sx
	if s.netPlaying {
		return
	}
	q := len(m.Recv())
	if q == 0 {
		return
	}
	if s.prebufAt.IsZero() {
		s.prebufAt = now
	}
	if q >= prebufFrames || now.Sub(s.prebufAt) >= prebufWait || !m.Talking() {
		s.netPlaying = true
	}
}

// stepRF forwards the receiver's audio to the active mode while a local
// station transmits, and ends the transmission when it unkeys.
func (e *Engine) stepRF(now time.Time, m mode.Mode, rfActive bool) {
	s := &e.sx
	p := e.params
	capt := e.o.Radio.Capture()
	if !rfActive || s.rfLocked {
		e.drainRX(capt, now)
		if !rfActive && !s.rfStart.IsZero() {
			e.endRF(now, m)
		}
		return
	}
	if s.rfStart.IsZero() {
		s.rfStart = now
		s.radioAGC.Reset()
		s.rxPL.Reset()
		s.rxDeemph.Reset()
	}
	if now.Sub(s.rfStart) >= p.txTimeout {
		s.rfLocked = true
		e.logf(now, "receive timeout: the local station has transmitted for %s", p.txTimeout)
		e.endRF(now, m)
		s.rfStart = now // keep the carrier marked busy until it drops
		e.drainRX(capt, now)
		return
	}
	if s.rfSentTo != nil && s.rfSentTo != m {
		s.rfSentTo.EndTx()
		s.rfSentTo = nil
	}
	for i := 0; i < maxCatchUp; i++ {
		var f audio.Frame
		select {
		case f = <-capt:
		default:
			return
		}
		s.rx.Add(&f)
		s.rxAt = now
		if m == nil {
			continue
		}
		if p.plFilter {
			s.rxPL.Process(&f)
		}
		if p.deEmph {
			s.rxDeemph.Process(&f)
		}
		audio.ApplyGain(&f, p.rxGainDb)
		if p.radioAGC {
			s.radioAGC.Process(&f, p.agc)
		}
		m.Send(f)
		s.rfSentTo = m
	}
}

// drainRX drops the receiver's queued audio after measuring it.
func (e *Engine) drainRX(c <-chan audio.Frame, now time.Time) {
	for {
		select {
		case f := <-c:
			e.sx.rx.Add(&f)
			e.sx.rxAt = now
		default:
			return
		}
	}
}

// endRF finishes a local transmission: the mode that got its audio hears
// the end, and the courtesy tone follows on the radio.
func (e *Engine) endRF(now time.Time, m mode.Mode) {
	s := &e.sx
	sent := s.rfSentTo
	locked := s.rfLocked
	s.rfStart, s.rfSentTo = time.Time{}, nil
	if sent == nil {
		return
	}
	sent.EndTx()
	if e.params.courtesy && sent == m && !locked {
		s.tone = audio.CourtesyTone()
	}
}

// idSource builds the ID audio, or nil with a logged reason.
func (e *Engine) idSource(now time.Time) audio.Source {
	p := e.params
	if p.voice != "" {
		w, err := audio.LoadWAV(p.voice)
		if err == nil {
			return w
		}
		e.logf(now, "ID voice file: %v; sending CW", err)
	}
	cw, err := audio.NewCW(p.callsign, p.cw)
	if err != nil {
		e.logf(now, "ID: %v", err)
		return nil
	}
	return cw
}

func (e *Engine) setPTT(on bool, now time.Time) {
	if err := e.o.Radio.SetPTT(on); err != nil {
		e.logf(now, "PTT: %v", err)
		if on {
			return
		}
		// The transmitter may still be keyed: keep trying.
		e.sx.pttStuck = true
	} else {
		e.sx.pttStuck = false
	}
	e.sx.ptt = on
	if !on {
		e.sx.netStart = time.Time{}
	}
	e.publishOn(EventPTT, on, now)
}

// retryUnkey tries again to unkey a transmitter that failed to unkey.
// Called with mu.
func (e *Engine) retryUnkey(now time.Time) {
	if err := e.o.Radio.SetPTT(false); err != nil {
		return // logged when it first failed; Status shows it
	}
	e.sx.pttStuck = false
	e.logf(now, "PTT: unkeyed after an earlier failure")
}

// dropPTT unkeys the transmitter, whatever the state.
func (e *Engine) dropPTT() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.o.Radio.SetPTT(false); err != nil {
		e.sx.pttStuck = true
		e.log.Warn("PTT: unkey failed", "err", err)
	} else {
		e.sx.pttStuck = false
	}
	if e.sx.ptt {
		e.sx.ptt = false
		e.publishOn(EventPTT, false, e.now())
	}
	e.sx.tone, e.sx.id = nil, nil
}

func (e *Engine) publishOn(k EventKind, on bool, now time.Time) {
	v := on
	e.hub.publish(Event{Kind: k, At: now, On: &v})
}

func (e *Engine) logf(now time.Time, format string, args ...any) {
	ev := Event{Kind: EventLog, At: now, Text: sprintf(format, args...)}
	e.log.Warn(ev.Text)
	e.hub.publish(ev)
}

func drain(c <-chan audio.Frame) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}

// mix adds b into a with saturation.
func mix(a, b *audio.Frame) {
	for i := range a {
		v := int32(a[i]) + int32(b[i])
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		a[i] = int16(v)
	}
}
