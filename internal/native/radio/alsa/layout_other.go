//go:build !(amd64 || arm64)

package alsa

import "errors"

// Supported is false on a 32-bit board (armhf): the structure layouts in
// types.go are the 64-bit ones, so the audio device is never opened there.
const Supported = false

// ErrUnsupported is what the device openers return where Supported is false.
var ErrUnsupported = errors.New("alsa: analog audio is not supported on 32-bit boards")
