// Package allstar is the AllStar mode: an outbound-only IAX2 link to one
// AllStar node, with the node kept registered with AllStarLink in the
// background whenever AllStar is configured.
package allstar

import (
	"errors"
	"fmt"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/register"
)

// Setting keys.
const (
	KeyNode         = "node"
	KeyPassword     = "password"
	KeyRegister     = "register"
	KeyRegisterURL  = "register_url"
	KeyIntervalS    = "register_interval_s"
	KeyLinkWaitS    = "link_wait_s"
	KeyReconnect    = "reconnect"
	KeyIAXPort      = "iax_port"
	KeyNodeDomain   = "node_domain"
	defaultLinkWait = 90
)

func init() { mode.Register(Spec()) }

// Spec describes the AllStar mode.
func Spec() mode.Spec {
	return mode.Spec{
		Name:        mode.AllStar,
		Title:       "AllStar",
		TargetLabel: "Node",
		TargetHelp:  "The AllStar node number to link to.",
		Fields: []mode.Field{
			{Key: KeyNode, Label: "Node number", Kind: mode.FieldText, Required: true,
				Help: "Your AllStarLink node number."},
			{Key: KeyPassword, Label: "Node password", Kind: mode.FieldPassword, Secret: true,
				Help: "The node password from the AllStarLink portal. Needed to register."},
			{Key: KeyRegister, Label: "Register with AllStarLink", Kind: mode.FieldBool, Default: "true", Advanced: true},
			{Key: KeyRegisterURL, Label: "Registration URL", Kind: mode.FieldText, Default: register.DefaultURL, Advanced: true},
			{Key: KeyIntervalS, Label: "Registration interval (s)", Kind: mode.FieldNumber, Default: "60", Advanced: true},
			{Key: KeyLinkWaitS, Label: "Wait for registration (s)", Kind: mode.FieldNumber, Default: "90", Advanced: true,
				Help: "How long a link waits for the registration and DNS record before dialing anyway."},
			{Key: KeyReconnect, Label: "Reconnect dropped links", Kind: mode.FieldBool, Default: "true", Advanced: true},
			{Key: KeyIAXPort, Label: "Local IAX2 port", Kind: mode.FieldNumber, Default: "0", Advanced: true,
				Help: "0 picks a free port. The node never listens for incoming calls."},
			{Key: KeyNodeDomain, Label: "Node DNS zone", Kind: mode.FieldText, Advanced: true},
		},
		Validate: validate,
		New: func(env mode.Env, s mode.Settings) (mode.Mode, error) {
			return New(env, s, Options{})
		},
	}
}

// ValidNode reports whether s is an AllStar node number (4 to 7 digits).
func ValidNode(s string) bool {
	if len(s) < 4 || len(s) > 7 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validate(s mode.Settings) error {
	var errs []error
	if n := s.Get(KeyNode, ""); !ValidNode(n) {
		errs = append(errs, fmt.Errorf("node number %q must be 4 to 7 digits", n))
	}
	if iv, err := s.Int(KeyIntervalS, 60); err == nil && iv < 60 {
		errs = append(errs, errors.New("registration interval must be at least 60 seconds"))
	}
	if w, err := s.Int(KeyLinkWaitS, defaultLinkWait); err == nil && (w < 0 || w > 600) {
		errs = append(errs, errors.New("registration wait must be 0 to 600 seconds"))
	}
	if p, err := s.Int(KeyIAXPort, 0); err == nil && (p < 0 || p > 65535) {
		errs = append(errs, errors.New("local IAX2 port must be 0 to 65535"))
	}
	return errors.Join(errs...)
}
