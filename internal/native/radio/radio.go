package radio

import (
	"errors"

	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

// Frame is one 20 ms frame of 8 kHz mono PCM, the shared audio contract.
type Frame = audio.Frame

// ErrNoRadio is returned by Open when no supported interface is plugged in.
// A caller that wants a radioless node falls back to [NewFake].
var ErrNoRadio = errors.New("radio: no supported radio interface found")

// Levels is a snapshot of the audio levels and device health.
type Levels struct {
	// CaptureVolume and PlaybackVolume are the sound chip's mixer positions,
	// 0 to 1. They are -1 when the chip has no such control.
	CaptureVolume  float64
	PlaybackVolume float64
	// Rx and Tx meter the audio from and to the radio.
	Rx, Tx audio.Reading
	// DroppedRx counts captured frames the consumer was too slow to take;
	// DroppedTx counts frames Play discarded because the device queue was full.
	DroppedRx, DroppedTx uint64
	// Xruns counts device overruns and underruns that were recovered.
	Xruns uint64
}

// Radio is the interface to a radio, real or fake.
type Radio interface {
	// Capture returns the received audio, one frame per 20 ms. The channel is
	// closed by Close. A consumer that falls behind loses the newest frames.
	Capture() <-chan Frame
	// Play queues a frame for transmit audio. It never blocks; frames are
	// dropped (and counted) if the device cannot keep up.
	Play(Frame)
	// SetPTT keys or unkeys the transmitter. Unkeying also discards queued
	// audio.
	SetPTT(on bool) error
	// PTT reports the last state set.
	PTT() bool
	// COS delivers every debounced change of the carrier-operated squelch:
	// true when a signal opens the squelch. The channel is closed by Close.
	COS() <-chan bool
	// Carrier reports the current debounced COS state.
	Carrier() bool
	// Levels returns the current levels.
	Levels() Levels
	// SetVolumes sets the sound chip's capture and playback mixer, 0 to 1.
	SetVolumes(capture, playback float64) error
	// Close unkeys, stops the audio and releases the hardware.
	Close() error
}

// Config tells Open what to use. The zero value detects everything: a SHARI
// is used with the SHARI preset, and without one the node has no radio.
type Config struct {
	// SysRoot and DevRoot default to /sys and /dev; tests point them at a
	// fake tree.
	SysRoot, DevRoot string
	// BusPath selects one USB sound chip when several are plugged in.
	BusPath string

	// Preset picks the interface settings; the empty preset means the SHARI
	// when one is detected, otherwise none. Overrides change single
	// settings of the preset.
	Preset    Preset
	Overrides Overrides

	// InitialVolumes, when not nil, sets the sound chip mixer at Open.
	InitialVolumes *Volumes

	// SA818Port overrides the serial port search; SA818StatePath is the file
	// the applied settings are saved to (default sa818.DefaultStatePath).
	SA818Port      string
	SA818StatePath string
}

// Info says what Open found and how it set the interface up.
type Info struct {
	// Present is true when a real radio interface was opened. It is false
	// for the none preset, which runs the [Fake].
	Present bool `json:"present"`
	// Interface is the settings in use (the resolved preset and overrides).
	Interface Interface `json:"interface"`
	// Detected is true when a supported USB sound chip is plugged in,
	// whether or not the preset uses it. Chip, USBID, Product and BusPath
	// describe it.
	Detected bool   `json:"detected"`
	Chip     string `json:"chip,omitempty"`
	USBID    string `json:"usbId,omitempty"`
	Product  string `json:"product,omitempty"`
	BusPath  string `json:"busPath,omitempty"`
	// SHARI is true when the detected hardware looks like a SHARI: a CM108B
	// chip, or any supported chip beside the SA818's serial bridge.
	SHARI bool `json:"shari"`
}

// Volumes is a pair of mixer positions, 0 to 1.
type Volumes struct{ Capture, Playback float64 }

func (c Config) statePath() string {
	if c.SA818StatePath != "" {
		return c.SA818StatePath
	}
	return sa818.DefaultStatePath
}
