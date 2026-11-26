package mode

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FieldKind says how the UI should edit a Field.
type FieldKind string

// Field kinds.
const (
	FieldText     FieldKind = "text"
	FieldNumber   FieldKind = "number"
	FieldPassword FieldKind = "password"
	FieldBool     FieldKind = "bool"
	FieldSelect   FieldKind = "select"
)

// Field is one setting of a mode.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Kind     FieldKind `json:"kind"`
	Required bool      `json:"required,omitempty"`
	// Secret fields are never returned by the control API. Saving an
	// empty value keeps the stored one.
	Secret   bool     `json:"secret,omitempty"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"` // FieldSelect choices
	Help     string   `json:"help,omitempty"`
	Advanced bool     `json:"advanced,omitempty"`
	// StationDefault names the station value this field falls back to when
	// left empty: StationCallsign, StationDMRID or StationNXDNID. A Required
	// field with a StationDefault is required only when the station has no
	// such value.
	StationDefault string `json:"stationDefault,omitempty"`
	// StationSuffix is added to the station value when the mode uses it
	// (EchoLink adds "-L" to the station callsign). The UI shows it in the
	// "from station" hint.
	StationSuffix string `json:"stationSuffix,omitempty"`
	// HostList names a host list (GET /api/hostlists?mode=<HostList>) the
	// UI offers as a picker for this text field: HostListBM or
	// HostListDMROther. A pick fills the field with the list entry's host
	// (host:port when the list gives a port); the UI also keeps an
	// "Other..." choice to type a value. The saved value is always plain
	// text, so the field works the same without the list.
	HostList string `json:"hostList,omitempty"`
}

// Host lists a Field can be picked from (Field.HostList).
const (
	HostListBM       = "bm"        // BrandMeister masters of the DMR list
	HostListDMROther = "dmr-other" // the DMR list without BrandMeister and TGIF
)

// Station values a Field can fall back to (Field.StationDefault).
const (
	StationCallsign = "callsign"
	StationDMRID    = "dmrId"
	StationNXDNID   = "nxdnId"
)

// Settings is what the operator saved for one mode.
type Settings struct {
	// Values holds the Field values by Key.
	Values map[string]string `json:"values,omitempty"`
	// Favorites are targets the operator saved for quick selection.
	Favorites []Target `json:"favorites,omitempty"`
	// Reflectors maps target IDs to addresses for modes that connect to a
	// host (YSF, P25, NXDN, M17), so a target can be picked by ID alone.
	Reflectors []Target `json:"reflectors,omitempty"`
	// Clear names Secret fields to delete from the saved settings. It is
	// only read by a save request (Spec.CheckClear, Spec.Merge); Merge
	// drops it, so it is never stored.
	Clear []string `json:"clear,omitempty"`
}

// Get returns the value of key, or def when it is unset or blank.
func (s Settings) Get(key, def string) string {
	if v, ok := s.Values[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// Int returns key as an integer, or def when it is unset.
func (s Settings) Int(key string, def int) (int, error) {
	v := s.Get(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a whole number", key, v)
	}
	return n, nil
}

// Bool returns key as a boolean, or def when it is unset.
func (s Settings) Bool(key string, def bool) (bool, error) {
	v := s.Get(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not true or false", key, v)
	}
	return b, nil
}

// Clone returns a deep copy.
func (s Settings) Clone() Settings {
	out := Settings{
		Favorites:  append([]Target(nil), s.Favorites...),
		Reflectors: append([]Target(nil), s.Reflectors...),
	}
	if s.Values != nil {
		out.Values = make(map[string]string, len(s.Values))
		for k, v := range s.Values {
			out.Values[k] = v
		}
	}
	return out
}

// Reflector looks id up in Reflectors (case-insensitive).
func (s Settings) Reflector(id string) (Target, bool) {
	for _, r := range s.Reflectors {
		if strings.EqualFold(strings.TrimSpace(r.ID), strings.TrimSpace(id)) {
			return r, true
		}
	}
	return Target{}, false
}

// Env is what the node gives every mode.
type Env struct {
	// Callsign is the station callsign.
	Callsign string
	// DMRID and NXDNID are the station's optional digital IDs (0 = unset).
	// A mode uses them when its own ID setting is empty.
	DMRID  uint32
	NXDNID uint32
	// Log is where the mode logs. Never log a password.
	Log *slog.Logger
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// MD380 is the address of the md380-emu AMBE vocoder (DMR and YSF).
	MD380 string
	// MD380Timeout bounds one vocoder request (0 = the vocoder default).
	MD380Timeout time.Duration
}

// Logger returns e.Log, or a logger that discards.
func (e Env) Logger() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Clock returns e.Now, or time.Now.
func (e Env) Clock() func() time.Time {
	if e.Now != nil {
		return e.Now
	}
	return time.Now
}

// Spec describes a mode: its settings and how to build it.
type Spec struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// TargetLabel and TargetHelp tell the UI what a Target ID is for
	// this mode ("Node", "Talkgroup", ...).
	TargetLabel string  `json:"targetLabel"`
	TargetHelp  string  `json:"targetHelp,omitempty"`
	Fields      []Field `json:"fields"`
	// NeedsHost: targets need an address (from Reflectors or Target.Host).
	NeedsHost bool `json:"needsHost,omitempty"`
	// NeedsAMBE: the mode's voice is AMBE, which the md380-emu vocoder
	// encodes and decodes (the UI offers to install it).
	NeedsAMBE bool `json:"needsAmbe,omitempty"`
	// AMBEFor, when set, decides NeedsAMBE from the saved settings (the
	// echo test needs the vocoder only when its codec is AMBE). See
	// Spec.AMBENeeded.
	AMBEFor func(Settings) bool `json:"-"`
	// Legacy lists setting keys that older configs may still hold. They are
	// accepted and ignored, and dropped when the settings are next saved.
	Legacy []string `json:"-"`
	// Validate checks settings beyond Required fields (optional).
	Validate func(Settings) error `json:"-"`
	// New builds the mode from validated settings.
	New func(Env, Settings) (Mode, error) `json:"-"`
}

// AMBENeeded reports whether the mode needs the md380-emu vocoder with the
// settings s: AMBEFor when the spec has one, else NeedsAMBE.
func (sp Spec) AMBENeeded(s Settings) bool {
	if sp.AMBEFor != nil {
		return sp.AMBEFor(s)
	}
	return sp.NeedsAMBE
}

// Field returns the field with key.
func (sp Spec) Field(key string) (Field, bool) {
	for _, f := range sp.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// StationValue returns the station value named by a Field.StationDefault
// as text ("" when the station has none).
func (e Env) StationValue(name string) string {
	switch name {
	case StationCallsign:
		return strings.ToUpper(strings.TrimSpace(e.Callsign))
	case StationDMRID, StationNXDNID:
		if n := e.StationID(name); n != 0 {
			return strconv.FormatUint(uint64(n), 10)
		}
	}
	return ""
}

// StationID returns the station ID named by a Field.StationDefault, or 0.
func (e Env) StationID(name string) uint32 {
	switch name {
	case StationDMRID:
		return e.DMRID
	case StationNXDNID:
		return e.NXDNID
	}
	return 0
}

// Missing returns the Required fields that have no value in s and no
// station fallback in env. A mode with none missing is ready to use.
func (sp Spec) Missing(env Env, s Settings) []Field {
	var out []Field
	for _, f := range sp.Fields {
		if f.Required && s.Get(f.Key, f.Default) == "" && env.StationValue(f.StationDefault) == "" {
			out = append(out, f)
		}
	}
	return out
}

// IsLegacy reports whether key is a Legacy setting.
func (sp Spec) IsLegacy(key string) bool {
	for _, k := range sp.Legacy {
		if k == key {
			return true
		}
	}
	return false
}

// Check validates s against the spec with no station values: a field with a
// StationDefault must be set. See CheckEnv.
func (sp Spec) Check(s Settings) error { return sp.CheckEnv(Env{}, s) }

// CheckEnv validates s against the spec: required fields, numbers,
// booleans, select choices, then Validate. A required field with a
// StationDefault may be empty when env has that station ID. All problems
// are reported together.
func (sp Spec) CheckEnv(env Env, s Settings) error {
	var errs []error
	for _, f := range sp.Fields {
		v := s.Get(f.Key, f.Default)
		if v == "" {
			if f.Required && env.StationValue(f.StationDefault) == "" {
				errs = append(errs, fmt.Errorf("%s: %s is required", sp.Title, f.Label))
			}
			continue
		}
		switch f.Kind {
		case FieldNumber:
			if _, err := strconv.Atoi(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s must be a whole number", sp.Title, f.Label))
			}
		case FieldBool:
			if _, err := strconv.ParseBool(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s must be true or false", sp.Title, f.Label))
			}
		case FieldSelect:
			ok := false
			for _, o := range f.Options {
				if strings.EqualFold(o, v) {
					ok = true
				}
			}
			if !ok {
				errs = append(errs, fmt.Errorf("%s: %s must be one of %s", sp.Title, f.Label, strings.Join(f.Options, ", ")))
			}
		}
	}
	for k := range s.Values {
		if _, ok := sp.Field(k); !ok && !sp.IsLegacy(k) {
			errs = append(errs, fmt.Errorf("%s: unknown setting %q", sp.Title, k))
		}
	}
	if len(errs) == 0 && sp.Validate != nil {
		if err := sp.Validate(s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sp.Title, err))
		}
	}
	return errors.Join(errs...)
}

// Redact returns s with every Secret field blanked, for the control API.
// A blank secret that is set reads as "set" in the second return value.
func (sp Spec) Redact(s Settings) (Settings, map[string]bool) {
	out := s.Clone()
	set := map[string]bool{}
	for _, f := range sp.Fields {
		if !f.Secret {
			continue
		}
		if out.Values[f.Key] != "" {
			set[f.Key] = true
		}
		delete(out.Values, f.Key)
	}
	return out, set
}

// CheckClear validates upd.Clear: every key must be a Secret field of the
// spec, and upd must not set a new value for it at the same time.
func (sp Spec) CheckClear(upd Settings) error {
	for _, k := range upd.Clear {
		if f, ok := sp.Field(k); !ok || !f.Secret {
			return fmt.Errorf("%s: %q is not a saved password", sp.Title, k)
		}
		if strings.TrimSpace(upd.Values[k]) != "" {
			return fmt.Errorf("%s: cannot set and clear %q in one save", sp.Title, k)
		}
	}
	return nil
}

// Merge applies an update from the control API onto old: secrets left
// blank in upd keep their old values, except those named in upd.Clear
// (checked by CheckClear; others are ignored here), which are removed. An
// update with no values and a Clear list changes nothing else.
// Favorites and Reflectors are taken
// from upd when it has them (nil keeps the old list).
func (sp Spec) Merge(old, upd Settings) Settings {
	out := upd.Clone()
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	for _, f := range sp.Fields {
		if f.Secret && strings.TrimSpace(out.Values[f.Key]) == "" && old.Values[f.Key] != "" && !slices.Contains(upd.Clear, f.Key) {
			out.Values[f.Key] = old.Values[f.Key]
		}
	}
	out.Clear = nil
	if len(upd.Values) == 0 && len(upd.Clear) > 0 {
		// A request that only clears passwords leaves every other value as it is.
		for k, v := range old.Values {
			if !slices.Contains(upd.Clear, k) {
				out.Values[k] = v
			}
		}
	}
	for k, v := range out.Values {
		if strings.TrimSpace(v) == "" || sp.IsLegacy(k) {
			delete(out.Values, k)
		}
	}
	if upd.Favorites == nil {
		out.Favorites = append([]Target(nil), old.Favorites...)
	}
	if upd.Reflectors == nil {
		out.Reflectors = append([]Target(nil), old.Reflectors...)
	}
	return out
}

// Secrets returns every non-empty secret value in s, so a logger can scrub
// them.
func (sp Spec) Secrets(s Settings) []string {
	var out []string
	for _, f := range sp.Fields {
		if f.Secret && s.Values[f.Key] != "" {
			out = append(out, s.Values[f.Key])
		}
	}
	return out
}

var (
	regMu sync.RWMutex
	reg   = map[string]Spec{}
)

// Register adds a mode. Mode packages call it from init. It panics on a
// duplicate or incomplete spec.
func Register(sp Spec) {
	if sp.Name == "" || sp.New == nil {
		panic("mode: Register needs a Name and New")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg[sp.Name]; dup {
		panic("mode: duplicate " + sp.Name)
	}
	reg[sp.Name] = sp
}

// Lookup returns the spec of a registered mode.
func Lookup(name string) (Spec, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	sp, ok := reg[name]
	return sp, ok
}

// order is the display order; unknown names sort after it by name.
var order = []string{AllStar, EchoLink, BM, TGIF, DMR, YSF, P25, NXDN, M17, EchoTest}

// Specs returns every registered mode in display order.
func Specs() []Spec {
	regMu.RLock()
	defer regMu.RUnlock()
	rank := func(n string) int {
		for i, o := range order {
			if o == n {
				return i
			}
		}
		return len(order)
	}
	out := make([]Spec, 0, len(reg))
	for _, sp := range reg {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank(out[i].Name), rank(out[j].Name)
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Build checks s and builds the named mode.
func Build(name string, env Env, s Settings) (Mode, error) {
	sp, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("mode %q is not available", name)
	}
	if err := sp.CheckEnv(env, s); err != nil {
		return nil, err
	}
	m, err := sp.New(env, s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", sp.Title, err)
	}
	return m, nil
}
