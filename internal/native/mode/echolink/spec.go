// Package echolink is the EchoLink mode: a directory login while EchoLink
// is the active mode, and one outgoing connection to a station,
// conference or node number.
package echolink

import (
	"errors"
	"strings"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// Setting keys.
const (
	KeyCallsign = "callsign"
	KeyPassword = "password"
	KeyName     = "name"
	KeyLocation = "location"
	KeyInfo     = "info"
	KeyServers  = "servers"
)

func init() { mode.Register(Spec()) }

// Spec describes the EchoLink mode.
func Spec() mode.Spec {
	return mode.Spec{
		Name:        mode.EchoLink,
		Title:       "EchoLink",
		TargetLabel: "Station",
		TargetHelp:  "A callsign, conference (*NAME*) or EchoLink node number.",
		Fields: []mode.Field{
			{Key: KeyCallsign, Label: "EchoLink callsign", Kind: mode.FieldText, Required: true,
				StationDefault: mode.StationCallsign, StationSuffix: "-L",
				Help: "Your validated EchoLink callsign, usually with -L for a simplex link. Leave empty to use the station callsign with -L."},
			{Key: KeyPassword, Label: "EchoLink password", Kind: mode.FieldPassword, Required: true, Secret: true},
			{Key: KeyName, Label: "Name", Kind: mode.FieldText, Help: "Shown to the stations you connect to."},
			{Key: KeyLocation, Label: "Location", Kind: mode.FieldText, Help: "Shown in the EchoLink directory."},
			{Key: KeyInfo, Label: "Station info", Kind: mode.FieldText, Advanced: true},
			{Key: KeyServers, Label: "Directory servers", Kind: mode.FieldText, Advanced: true,
				Help: "Comma-separated host or host:port list. Default: the public EchoLink servers."},
		},
		Validate: func(s mode.Settings) error {
			if c := s.Get(KeyCallsign, ""); c != "" && strings.ContainsAny(c, " \t,") {
				return errors.New("EchoLink callsign must not contain spaces or commas")
			}
			return nil
		},
		New: func(env mode.Env, s mode.Settings) (mode.Mode, error) {
			return New(env, s, Options{})
		},
	}
}
