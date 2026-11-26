package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

// Status is a snapshot of the node for the UI. It never carries a password.
type Status struct {
	Callsign string      `json:"callsign,omitempty"`
	Started  time.Time   `json:"started"`
	Radio    RadioStatus `json:"radio"`
	// Active is the active mode ("" when idle) and Mode its status.
	Active string       `json:"active,omitempty"`
	Since  time.Time    `json:"since,omitempty"`
	Mode   *mode.Status `json:"mode,omitempty"`
	// Registration is the AllStar registration, kept up whenever AllStar
	// is configured, active or not.
	Registration *mode.Registration `json:"registration,omitempty"`
	// EchoTest is the level report of the echo test's last recording, kept
	// whether or not the echo test is the active mode.
	EchoTest *audio.Report `json:"echoTest,omitempty"`
	ID       IDStatus      `json:"id"`
	// Configured is false until the station has a callsign.
	Configured bool `json:"configured"`
	// RestartRequired: the saved radio wiring (preset, pins, polarity,
	// SA818 port) differs from what the radio was opened with.
	// The node opens the radio once, so it restarts itself to apply it
	// (Options.RadioChanged); until then the old wiring is in use.
	RestartRequired bool `json:"restartRequired,omitempty"`
}

// RadioStatus is the radio side.
type RadioStatus struct {
	Present bool `json:"present"`
	// Preset is the interface preset in use ("shari", "uri" or "none"),
	// Chip and USBID the detected sound chip ("CM108B", "0d8c:0012"), Product
	// its USB product string. Detected is true when a supported chip is
	// plugged in, whether or not the preset uses it. SA818 is true when the
	// interface has an SA818 module the node programs; otherwise the
	// frequency and tone are set on the radio.
	Preset   string `json:"preset"`
	Chip     string `json:"chip,omitempty"`
	USBID    string `json:"usbId,omitempty"`
	Product  string `json:"product,omitempty"`
	Detected bool   `json:"detected,omitempty"`
	SA818    bool   `json:"sa818"`
	PTT      bool   `json:"ptt"`
	COS      bool   `json:"cos"`
	// Level is the receive level over the last 100 ms (see InputLevel).
	Level InputLevel `json:"level"`
	// RxActive: a local station is transmitting into the receiver and its
	// audio is being sent to the active mode.
	RxActive bool `json:"rxActive,omitempty"`
	// NetDropped counts frames of the active mode's audio the node threw
	// away instead of playing them: a local station was transmitting into
	// the receiver (it has priority) or the transmit timeout had locked
	// the network out. It restarts when the active mode changes.
	NetDropped uint64 `json:"netDropped,omitempty"`
	RxLocked   bool   `json:"rxLocked,omitempty"` // a local transmission timed out
	TxLocked   bool   `json:"txLocked,omitempty"` // the network timed out the transmitter
	// PTTStuck: unkeying the transmitter failed and is being retried, so
	// it may still be on the air.
	PTTStuck bool `json:"pttStuck,omitempty"`
	// TestTone: the transmit test tone is playing or waiting to key up.
	TestTone bool `json:"testTone,omitempty"`
}

// InputLevel is the radio's receive level as the node hears it from the sound
// chip, before any AGC: the peak and RMS over the last 100 ms in dBFS (-90
// for silence, also when the radio has stopped delivering audio) and
// whether the carrier is on.
type InputLevel struct {
	PeakDb float64 `json:"peakDb"`
	RMSDb  float64 `json:"rmsDb"`
	COS    bool    `json:"cos"`
}

// IDStatus is the station ID.
type IDStatus struct {
	Enabled bool      `json:"enabled"`
	Owed    bool      `json:"owed"`
	Due     time.Time `json:"due,omitempty"`
	Last    time.Time `json:"last,omitempty"`
	Why     string    `json:"why,omitempty"`
}

// Status returns a snapshot of the node.
func (e *Engine) Status() Status {
	e.mu.Lock()
	st := Status{
		Callsign: e.cfg.Station.Callsign,
		Started:  e.started,
		Radio: RadioStatus{
			Present: !e.o.RadioAbsent, PTT: e.sx.ptt, COS: e.sx.cos,
			Level:  e.inputLevelLocked(),
			Preset: string(e.info.Interface.Preset), Chip: e.info.Chip, USBID: e.info.USBID,
			Product: e.info.Product, Detected: e.info.Detected, SA818: e.info.Interface.SA818,
			RxActive: e.sx.rfOn, NetDropped: e.sx.netDropped, RxLocked: e.sx.rfLocked, TxLocked: e.sx.txLocked, PTTStuck: e.sx.pttStuck,
			TestTone: e.sx.tone != nil,
		},
		ID: IDStatus{
			Enabled: e.params.id.on, Owed: e.sx.idT.owed,
			Last: e.sx.idT.lastID, Why: string(e.sx.idT.lastWhy),
		},
		Configured:      e.cfg.Station.Callsign != "",
		RestartRequired: e.wiringChanged(e.cfg.Station.Radio),
	}
	if e.sx.idT.owed {
		st.ID.Due = e.sx.idT.deadline
	}
	in := e.active
	as := e.modes[mode.AllStar]
	et := e.modes[mode.EchoTest]
	if in != nil {
		st.Active, st.Since = in.name, e.since
	}
	e.mu.Unlock()
	if in != nil {
		ms := in.m.Status()
		ms.Active = true
		st.Mode = &ms
	}
	if as != nil {
		st.Registration = as.m.Status().Registration
	}
	if et != nil {
		st.EchoTest = et.m.Status().Levels
	}
	return st
}

// ModeInfo describes one mode for the settings UI.
type ModeInfo struct {
	Name        string       `json:"name"`
	Title       string       `json:"title"`
	TargetLabel string       `json:"targetLabel,omitempty"`
	TargetHelp  string       `json:"targetHelp,omitempty"`
	NeedsHost   bool         `json:"needsHost,omitempty"`
	NeedsAmbe   bool         `json:"needsAmbe,omitempty"` // needs the md380-emu AMBE vocoder
	Fields      []mode.Field `json:"fields"`
	// Settings are the saved settings with every secret blank; SecretsSet
	// says which secrets have a value.
	Settings   mode.Settings   `json:"settings"`
	SecretsSet map[string]bool `json:"secretsSet,omitempty"`
	// Configured says the mode is ready to use: every required value is
	// set, by the mode or by the station (callsign, DMR ID, NXDN ID), and
	// it builds. The echo test needs nothing, so it is always ready.
	Configured bool `json:"configured"`
	// Missing lists the keys of the required fields that have no value and
	// no station fallback.
	Missing []string `json:"missing,omitempty"`
	// Saved says the mode has saved settings.
	Saved  bool `json:"saved"`
	Active bool `json:"active"`
	// Last is the target the mode reconnects to when activated (nil after
	// an explicit disconnect, or when it never connected).
	Last   *mode.Target `json:"last,omitempty"`
	Status *mode.Status `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"` // why it could not be built
}

// Modes lists every mode with its settings. Passwords are never returned.
func (e *Engine) Modes() []ModeInfo {
	e.mu.Lock()
	cfg := e.cfg
	built := make(map[string]*instance, len(e.modes))
	for k, v := range e.modes {
		built[k] = v
	}
	active := e.active
	tunes := make(map[string]Tune, len(e.tunes))
	for k, v := range e.tunes {
		tunes[k] = v
	}
	errs := make(map[string]string, len(e.buildErr))
	for k, v := range e.buildErr {
		errs[k] = v
	}
	env := e.env(cfg)
	e.mu.Unlock()
	var out []ModeInfo
	for _, sp := range mode.Specs() {
		s, ok := cfg.Modes[sp.Name]
		red, set := sp.Redact(s)
		mi := ModeInfo{
			Name: sp.Name, Title: sp.Title, TargetLabel: sp.TargetLabel,
			TargetHelp: sp.TargetHelp, NeedsHost: sp.NeedsHost, NeedsAmbe: sp.AMBENeeded(s), Fields: sp.Fields,
			Settings: red, SecretsSet: set, Saved: ok, Error: errs[sp.Name],
			Last: cloneTarget(tunes[sp.Name].Target),
		}
		for _, f := range sp.Missing(env, s) {
			mi.Missing = append(mi.Missing, f.Key)
		}
		mi.Configured = len(mi.Missing) == 0 && mi.Error == ""
		if in := built[sp.Name]; in != nil {
			ms := in.m.Status()
			mi.Active = in == active
			ms.Active = mi.Active
			mi.Status = &ms
		}
		out = append(out, mi)
	}
	return out
}

// SaveStation replaces the station settings (but not the active mode,
// which Select manages), saves them and applies them. When the radio's
// channel changed and a radio is present, the SA818 is programmed.
func (e *Engine) SaveStation(st Station) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	c := e.cfg.Clone()
	e.mu.Unlock()
	old := c.Station
	st.Active = old.Active
	c.Station = st
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	if err := e.commit(c, true); err != nil {
		return err
	}
	var errs []error
	if e.wiringChanged(c.Station.Radio) && e.o.RadioChanged != nil {
		go e.o.RadioChanged()
	}
	if !reflect.DeepEqual(old.Radio, c.Station.Radio) {
		if err := e.applyVolumes(c.Station.Radio); err != nil {
			errs = append(errs, err)
		}
		if channelChanged(old.Radio, c.Station.Radio) && c.Station.Radio.FrequencyMHz != 0 && e.hasSA818() {
			if err := e.program(c.Station.Radio); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// wiringChanged reports whether r would open the radio differently from how
// it is open now. The preset and overrides are resolved first, so naming the
// preset that was already chosen automatically changes nothing.
func (e *Engine) wiringChanged(r RadioConfig) bool {
	iface, err := r.Interface(e.info.SHARI)
	return err != nil || iface != e.info.Interface || r.SA818Port != e.o.RadioConfig.SA818Port
}

// hasSA818 reports whether the radio in use has an SA818 module to program.
func (e *Engine) hasSA818() bool { return !e.o.RadioAbsent && e.info.Interface.SA818 }

func channelChanged(a, b RadioConfig) bool {
	return a.FrequencyMHz != b.FrequencyMHz || a.Tone != b.Tone ||
		a.SquelchLevel() != b.SquelchLevel() || a.Narrow != b.Narrow || a.SA818Port != b.SA818Port
}

// SaveMode merges upd into mode n's saved settings (a blank secret keeps
// the saved one unless upd.Clear names it), checks, saves and applies them. The active mode is
// restarted on its target when its settings changed.
func (e *Engine) SaveMode(n string, upd mode.Settings) error {
	n = strings.ToLower(strings.TrimSpace(n))
	sp, ok := mode.Lookup(n)
	if !ok {
		return fmt.Errorf("mode %q is not a known mode", n)
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	c := e.cfg.Clone()
	e.mu.Unlock()
	if err := sp.CheckClear(upd); err != nil {
		return err
	}
	s := sp.Merge(c.Modes[n], upd)
	// Clearing a required password is allowed (the mode then reports not
	// ready), so the check sees a placeholder in the cleared fields.
	chk := s.Clone()
	for _, k := range upd.Clear {
		chk.Values[k] = "-"
	}
	if err := sp.CheckEnv(e.env(c), chk); err != nil {
		return err
	}
	c.Modes[n] = s
	return e.commit(c, true)
}

// DeleteMode forgets mode n's settings. An active mode is dropped first.
func (e *Engine) DeleteMode(ctx context.Context, n string) error {
	n = strings.ToLower(strings.TrimSpace(n))
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	act := e.active != nil && e.active.name == n
	_, had := e.cfg.Modes[n]
	e.mu.Unlock()
	if !had {
		return fmt.Errorf("mode %q has no saved settings", n)
	}
	if act {
		if err := e.selectLocked(ctx, "", nil, true); err != nil {
			return err
		}
	}
	e.mu.Lock()
	c := e.cfg.Clone()
	e.mu.Unlock()
	delete(c.Modes, n)
	err := e.forgetTune(n)
	return errors.Join(err, e.commit(c, true))
}

// Reload reads the config file again and applies it. A bad file is
// reported and the running config is kept.
func (e *Engine) Reload(ctx context.Context) error {
	if e.o.ConfigPath == "" {
		return errors.New("no config file")
	}
	c, err := LoadConfig(e.o.ConfigPath)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	same := reflect.DeepEqual(c, e.cfg)
	cur := ""
	if e.active != nil {
		cur = e.active.name
	}
	oldRadio := e.cfg.Station.Radio
	e.cfg = c.Clone()
	e.params = paramsFor(c, e.info.Interface.SA818)
	e.mu.Unlock()
	if same {
		return nil
	}
	e.applyModes(c)
	var errs []error
	if !reflect.DeepEqual(oldRadio, c.Station.Radio) {
		errs = append(errs, e.applyVolumes(c.Station.Radio))
	}
	want := ""
	var t *mode.Target
	if a := c.Station.Active; a != nil {
		want, t = a.Mode, a.Target
	}
	if want != cur {
		errs = append(errs, e.selectLocked(ctx, want, t, false))
	}
	e.hub.publish(Event{Kind: EventConfig, At: e.now(), Text: "reloaded"})
	return errors.Join(errs...)
}

// Tune sets the sound chip's mixer (0 to 1 each; nil keeps a level) and the
// receive boost (nil keeps it) and saves them.
func (e *Engine) Tune(rx, tx *float64, boost *bool) error {
	for _, v := range []*float64{rx, tx} {
		if v != nil && (*v < 0 || *v > 1) {
			return errors.New("levels must be 0 to 1")
		}
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	c := e.cfg.Clone()
	e.mu.Unlock()
	if rx != nil {
		v := *rx
		c.Station.Radio.RxVolume = &v
	}
	if tx != nil {
		v := *tx
		c.Station.Radio.TxVolume = &v
	}
	if boost != nil {
		v := *boost
		c.Station.Radio.RxBoost = &v
	}
	if err := e.applyVolumes(c.Station.Radio); err != nil {
		return err
	}
	return e.commit(c, false)
}

func (e *Engine) applyVolumes(r RadioConfig) error {
	var errs []error
	if r.RxBoost != nil {
		// A chip with no boost control has nothing to apply: not an error.
		if b, ok := e.o.Radio.(radio.Booster); ok && e.o.Radio.Levels().BoostAvailable {
			if err := b.SetBoost(*r.RxBoost); err != nil {
				errs = append(errs, fmt.Errorf("radio receive boost: %w", err))
			}
		}
	}
	if r.RxVolume == nil && r.TxVolume == nil {
		return errors.Join(errs...)
	}
	cur := e.o.Radio.Levels()
	rx, tx := cur.CaptureVolume, cur.PlaybackVolume
	if r.RxVolume != nil {
		rx = *r.RxVolume
	}
	if r.TxVolume != nil {
		tx = *r.TxVolume
	}
	if err := e.o.Radio.SetVolumes(rx, tx); err != nil {
		errs = append(errs, fmt.Errorf("radio levels: %w", err))
	}
	return errors.Join(errs...)
}

// Levels returns the radio's levels.
func (e *Engine) Levels() radio.Levels { return e.o.Radio.Levels() }

// ProgramRadio programs the SA818 with the station's channel.
func (e *Engine) ProgramRadio() error {
	if e.o.RadioAbsent {
		return errors.New("no radio is connected")
	}
	if !e.info.Interface.SA818 {
		return errors.New("this radio interface has no SA818 to program: set the frequency and tone on the radio")
	}
	e.mu.Lock()
	r := e.cfg.Station.Radio
	e.mu.Unlock()
	return e.program(r)
}

// ProgramIfChanged programs the SA818 at boot when the station's channel
// differs from what the module was last programmed with.
func (e *Engine) ProgramIfChanged() error {
	e.mu.Lock()
	r := e.cfg.Station.Radio
	e.mu.Unlock()
	if !e.hasSA818() || r.FrequencyMHz == 0 {
		return nil
	}
	want, err := r.SA818()
	if err != nil {
		return err
	}
	if st, err := radio.SavedSA818(e.o.RadioConfig); err == nil && sameChannel(st.Settings, want) {
		return nil
	}
	return e.program(r)
}

func sameChannel(a, b sa818.Settings) bool {
	return a.RxMHz == b.RxMHz && a.TxMHz == b.TxMHz && a.RxTone == b.RxTone &&
		a.TxTone == b.TxTone && a.Squelch == b.Squelch && a.Bandwidth == b.Bandwidth
}

func (e *Engine) program(r RadioConfig) error {
	s, err := r.SA818()
	if err != nil {
		return fmt.Errorf("radio: %w", err)
	}
	rc := e.o.RadioConfig
	if r.SA818Port != "" {
		rc.SA818Port = r.SA818Port
	}
	if err := e.o.Program(rc, s); err != nil {
		return fmt.Errorf("programming the SA818: %w", err)
	}
	e.log.Info("programmed the SA818", "mhz", s.RxMHz, "squelch", s.Squelch)
	return nil
}

// WatchConfig reloads the config whenever the file's modification time
// changes, checking every interval, until ctx ends.
func (e *Engine) WatchConfig(ctx context.Context, interval time.Duration) {
	if e.o.ConfigPath == "" {
		return
	}
	mtime := func() time.Time {
		fi, err := os.Stat(e.o.ConfigPath)
		if err != nil {
			return time.Time{}
		}
		return fi.ModTime()
	}
	last := mtime()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m := mtime()
			if m.Equal(last) {
				continue
			}
			last = m
			if err := e.Reload(ctx); err != nil {
				e.logf(e.now(), "config reload: %v", err)
			}
		}
	}
}

// RadioDetail is what the radio settings pages need: the hardware found, the
// settings in use, the presets and the configured preset.
type RadioDetail struct {
	radio.Info
	// Preset is the configured preset; empty means automatic.
	Preset  string             `json:"preset"`
	Presets []radio.PresetInfo `json:"presets"`
	// Resolved is what the saved settings would run with after a restart
	// (the settings in use are Info.Interface). Error says why they do not
	// resolve.
	Resolved        *radio.Interface `json:"resolved,omitempty"`
	RestartRequired bool             `json:"restartRequired,omitempty"`
	// SA818Filters is what the SA818's audio filters were last programmed to,
	// from the state file. It is absent when the module has never been
	// programmed by this node (the filters are then unknown).
	SA818Filters *ProgrammedFilters `json:"sa818Filters,omitempty"`
}

// ProgrammedFilters are the SA818 filter flags last sent to the module, true
// meaning the filter is on (wire value 0; 1 bypasses it), and when.
type ProgrammedFilters struct {
	Emphasis bool      `json:"emphasis"`
	HighPass bool      `json:"highPass"`
	LowPass  bool      `json:"lowPass"`
	At       time.Time `json:"at"`
}

// RadioInfo describes the radio interface.
func (e *Engine) RadioInfo() RadioDetail {
	e.mu.Lock()
	r := e.cfg.Station.Radio
	e.mu.Unlock()
	d := RadioDetail{Info: e.info, Preset: r.Preset, Presets: radio.Presets(), RestartRequired: e.wiringChanged(r)}
	d.Info.Present = !e.o.RadioAbsent
	if iface, err := r.Interface(e.info.SHARI); err == nil {
		d.Resolved = &iface
	}
	if e.hasSA818() {
		// Read only: showing the saved state sends nothing to the module.
		if st, err := radio.SavedSA818(e.o.RadioConfig); err == nil {
			f := st.Settings.Filters
			d.SA818Filters = &ProgrammedFilters{Emphasis: f.Emphasis, HighPass: f.HighPass, LowPass: f.LowPass, At: st.AppliedAt}
		}
	}
	return d
}

// inputStale is how long after its last frame the receive level reads as
// silence.
const inputStale = 200 * time.Millisecond

// InputLevel returns the radio's current receive level (cheap: no I/O).
func (e *Engine) InputLevel() InputLevel {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inputLevelLocked()
}

func (e *Engine) inputLevelLocked() InputLevel {
	l := InputLevel{PeakDb: audio.FloorDb, RMSDb: audio.FloorDb, COS: e.sx.cos}
	if !e.sx.rxAt.IsZero() && e.now().Sub(e.sx.rxAt) <= inputStale {
		l.PeakDb, l.RMSDb = e.sx.rx.Levels()
	}
	return l
}
