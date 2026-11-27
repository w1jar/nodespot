package echotest

import (
	"errors"
	"strings"

	"github.com/w1jar/nodespot/internal/native/mode"
)

func init() { mode.Register(Spec()) }

// Spec is the echo test's mode spec.
func Spec() mode.Spec {
	return mode.Spec{
		Name:  mode.EchoTest,
		Title: "Parrot",
		TargetHelp: "Talk, release, and your transmission plays back about a second later. " +
			"The Radio tune page shows the levels of the last recording.",
		Fields: []mode.Field{
			{Key: KeyCodec, Label: "Codec", Kind: mode.FieldSelect, Options: Codecs, Default: CodecPlain,
				Help: "What the playback goes through, so it sounds like that network: plain (no codec, like ASL), " +
					"ambe (DMR, YSF, NXDN; needs the vocoder), imbe (P25), codec2 (M17), gsm (EchoLink), ulaw (ASL)."},
			{Key: KeyDelay, Label: "Playback delay (ms)", Kind: mode.FieldNumber, Advanced: true,
				Default: "1000", Help: "Pause between the end of your transmission and the playback."},
		},
		// Only the AMBE codec needs the software AMBE vocoder.
		AMBEFor: func(s mode.Settings) bool {
			return strings.EqualFold(s.Get(KeyCodec, CodecPlain), CodecAMBE)
		},
		Validate: func(s mode.Settings) error {
			n, err := s.Int(KeyDelay, 0)
			if err == nil && (n < 0 || n > 30000) {
				err = errors.New("playback delay must be 0 to 30000 ms")
			}
			return err
		},
		New: func(env mode.Env, s mode.Settings) (mode.Mode, error) {
			return New(env, s, Options{})
		},
	}
}
