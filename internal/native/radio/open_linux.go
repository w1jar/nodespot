package radio

import (
	"errors"
	"fmt"

	"github.com/w1jar/nodespot/internal/native/radio/alsa"
	"github.com/w1jar/nodespot/internal/native/radio/cm108"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

// Detect looks for a supported interface chip and says whether it looks like
// a SHARI. It never opens anything.
func Detect(cfg Config) (Info, error) {
	var info Info
	found, err := cm108.Discover(cfg.SysRoot, cfg.DevRoot, cfg.BusPath)
	if err != nil {
		if errors.Is(err, cm108.ErrNotFound) {
			return info, fmt.Errorf("%w: %v", ErrNoRadio, err)
		}
		return info, fmt.Errorf("radio: detect: %w", err)
	}
	info.Detected = true
	info.Chip, info.USBID, info.Product, info.BusPath = found.Chip, found.ID.String(), found.Product, found.BusPath
	_, serialErr := sa818.FindPort(cfg.SysRoot, cfg.DevRoot)
	info.SHARI = found.Chip == "CM108B" || serialErr == nil
	return info, nil
}

// Open detects the interface (USB sound chip, hidraw node, ALSA card), picks
// the settings of the preset and opens it. For the none preset (or no preset
// and no SHARI) it returns a [Fake] and Info.Present false. When the preset
// needs a sound chip and none is found, the error wraps [ErrNoRadio]; the Info
// is filled in either way. It does not program the SA818; see
// [ProgramSA818].
func Open(cfg Config) (Radio, Info, error) {
	info, derr := Detect(cfg)
	if derr != nil && !errors.Is(derr, ErrNoRadio) && cfg.Preset != PresetAuto && cfg.Preset != PresetNone && cfg.Preset != PresetMMDVM {
		return nil, info, derr
	}
	iface, err := Resolve(cfg.Preset, cfg.Overrides, info.SHARI)
	if err != nil {
		return nil, info, fmt.Errorf("radio: %w", err)
	}
	info.Interface = iface
	if !iface.Audio {
		return NewFake(), info, nil
	}
	if !info.Detected {
		if derr == nil {
			derr = ErrNoRadio
		}
		return nil, info, derr
	}
	keyer, err := cm108.Open(cm108.Config{
		SysRoot: cfg.SysRoot, DevRoot: cfg.DevRoot, BusPath: info.BusPath,
		PTTGPIO: iface.PTTPin, PTTActiveLow: iface.PTTActiveLow,
		COS: iface.COS, COSActiveLow: iface.COSActiveLow,
		CTCSS: iface.CTCSS, CTCSSActiveLow: iface.CTCSSActiveLow,
	})
	if err != nil {
		return nil, info, fmt.Errorf("radio: open HID: %w", err)
	}
	found, err := cm108.Discover(cfg.SysRoot, cfg.DevRoot, info.BusPath)
	if err != nil {
		keyer.Close()
		return nil, info, fmt.Errorf("radio: detect: %w", err)
	}
	mix, err := alsa.OpenMixer(found.Card)
	if err != nil {
		keyer.Close()
		return nil, info, fmt.Errorf("radio: %w", err)
	}
	// A missing switch is fine; a failure to unmute is not worth failing for.
	_ = mix.Unmute()
	if v := cfg.InitialVolumes; v != nil {
		if err := mix.SetCaptureVolume(v.Capture); err != nil {
			keyer.Close()
			mix.Close()
			return nil, info, fmt.Errorf("radio: capture volume: %w", err)
		}
		if err := mix.SetPlaybackVolume(v.Playback); err != nil {
			keyer.Close()
			mix.Close()
			return nil, info, fmt.Errorf("radio: playback volume: %w", err)
		}
	}
	capt, err := alsa.OpenCapture(found.Card, 0, alsa.Params{})
	if err != nil {
		keyer.Close()
		mix.Close()
		return nil, info, fmt.Errorf("radio: %w", err)
	}
	play, err := alsa.OpenPlayback(found.Card, 0, alsa.Params{})
	if err != nil {
		capt.Close()
		keyer.Close()
		mix.Close()
		return nil, info, fmt.Errorf("radio: %w", err)
	}
	info.Present = true
	return New(Parts{Capture: capt, Playback: play, PTT: keyer, Mixer: mix}), info, nil
}

// ProgramSA818 finds the module's serial port, programs it with s and saves
// the applied settings to cfg.SA818StatePath (default /etc/nodespot/sa818.json).
// It opens and closes the port itself, so it can run while the radio is open.
func ProgramSA818(cfg Config, s sa818.Settings) error {
	path := cfg.SA818Port
	if path == "" {
		var err error
		if path, err = sa818.FindPort(cfg.SysRoot, cfg.DevRoot); err != nil {
			return err
		}
	}
	port, err := sa818.OpenSerial(path)
	if err != nil {
		return err
	}
	defer port.Close()
	return sa818.NewProgrammer(port, cfg.statePath()).Apply(s)
}

// SavedSA818 returns the settings last programmed, from the state file.
func SavedSA818(cfg Config) (sa818.State, error) {
	return sa818.LoadState(cfg.statePath())
}
