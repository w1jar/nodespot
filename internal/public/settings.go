// Package public implements the public, read-only status view: unauthenticated
// JSON and Server-Sent Events endpoints on the main server (enabled by an admin
// setting, 404 otherwise). It shows the node: its active mode and target,
// who is talking and the last heard calls. The data they expose is built from dedicated structs
// that can never carry IP addresses or system information, and abuse limits
// (per-IP rate limit, SSE connection caps) apply.
package public

import (
	"errors"
	"fmt"
	"unicode"

	"github.com/w1jar/nodespot/internal/store"
)

// Setting keys in the settings table.
const (
	keyEnabled   = "public.enabled"
	keyTitle     = "public.title"
	keyCallsigns = "public.callsigns"
	keyActivity  = "public.activity"
	keyEmbed     = "public.embed"
	keyTrustProx = "public.trust_proxy"
)

// MaxTitle is the maximum length of the page title in runes.
const MaxTitle = 80

// Settings configure the public page. The zero value is not useful; use
// DefaultSettings.
type Settings struct {
	Enabled bool
	// Title is the page title; "" selects the automatic title.
	Title string
	// ShowCallsigns shows who is talking (callsign, name and place of the
	// caller); off, the page shows only that someone is.
	ShowCallsigns bool
	// ShowHeard adds the last heard calls.
	ShowHeard      bool
	AllowEmbedding bool
	// TrustProxy: take the client address from X-Forwarded-For (only for use
	// behind a reverse proxy or tunnel that sets it).
	TrustProxy bool
}

// DefaultSettings returns the defaults: off, everything visible
// except embedding.
func DefaultSettings() Settings {
	return Settings{ShowCallsigns: true, ShowHeard: true}
}

// SettingsStore is the part of the database the settings need.
type SettingsStore interface {
	GetSetting(k string) (string, error)
	SetSetting(k, v string) error
}

// LoadSettings reads the settings; missing or unreadable values fall back to
// the defaults (defaultEnabled applies to "enabled" when it was never set).
func LoadSettings(st SettingsStore, defaultEnabled bool) Settings {
	s := DefaultSettings()
	s.Enabled = defaultEnabled
	if st == nil {
		return s
	}
	get := func(k string) (string, bool) {
		v, err := st.GetSetting(k)
		return v, err == nil
	}
	boolv := func(k string, dst *bool) {
		if v, ok := get(k); ok {
			*dst = v == "true"
		}
	}
	boolv(keyEnabled, &s.Enabled)
	boolv(keyCallsigns, &s.ShowCallsigns)
	boolv(keyActivity, &s.ShowHeard)
	boolv(keyEmbed, &s.AllowEmbedding)
	boolv(keyTrustProx, &s.TrustProxy)
	if v, ok := get(keyTitle); ok {
		s.Title = v
	}
	return s
}

// SaveSettings stores all settings.
func SaveSettings(st SettingsStore, s Settings) error {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	for _, kv := range [][2]string{
		{keyEnabled, b(s.Enabled)}, {keyTitle, s.Title},
		{keyCallsigns, b(s.ShowCallsigns)}, {keyActivity, b(s.ShowHeard)},
		{keyEmbed, b(s.AllowEmbedding)}, {keyTrustProx, b(s.TrustProxy)},
	} {
		if err := st.SetSetting(kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the settings.
func (s Settings) Validate() error {
	if n := len([]rune(s.Title)); n > MaxTitle {
		return fmt.Errorf("title is too long (%d characters, at most %d)", n, MaxTitle)
	}
	for _, r := range s.Title {
		if unicode.IsControl(r) {
			return errors.New("title contains control characters")
		}
	}
	return nil
}

var _ SettingsStore = (*store.Store)(nil)
