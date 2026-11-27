package alsa

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Control element types (SNDRV_CTL_ELEM_TYPE_*) and the mixer interface.
const (
	elemTypeBoolean = 1
	elemTypeInteger = 2
	elemIfaceMixer  = 2
)

// ErrNoControl is returned when a mixer control does not exist.
var ErrNoControl = errors.New("alsa: no such mixer control")

// Common control names on the supported USB sound chips, tried in order.
var (
	CaptureVolumeNames  = []string{"Mic Capture Volume", "Capture Volume"}
	PlaybackVolumeNames = []string{"Speaker Playback Volume", "PCM Playback Volume", "Master Playback Volume"}
	CaptureSwitchNames  = []string{"Mic Capture Switch", "Capture Switch"}
	PlaybackSwitchNames = []string{"Speaker Playback Switch", "PCM Playback Switch", "Master Playback Switch"}
	// BoostNames are the microphone boost controls (a switch, or a volume
	// with a few steps) some chips have. "Auto Gain Control" is not one: it
	// is a different feature and is left alone.
	BoostNames = []string{"Mic Boost", "Mic Boost Capture Switch", "Mic Boost Volume"}
)

// ControlPath returns the control node of a card.
func ControlPath(card int) string { return fmt.Sprintf("/dev/snd/controlC%d", card) }

// Mixer reads and writes the controls of one sound card.
type Mixer struct {
	mu sync.Mutex
	fd int
}

// OpenMixer opens the control device of a card.
func OpenMixer(card int) (*Mixer, error) {
	return OpenMixerPath(ControlPath(card))
}

// OpenMixerPath opens a control device node.
func OpenMixerPath(path string) (*Mixer, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("alsa: open %s: %w", path, err)
	}
	return &Mixer{fd: fd}, nil
}

// Close releases the device.
func (m *Mixer) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fd < 0 {
		return nil
	}
	err := unix.Close(m.fd)
	m.fd = -1
	return err
}

// ControlInfo describes an integer or boolean control.
type ControlInfo struct {
	Name     string
	Boolean  bool
	Count    int
	Min, Max int64
	Step     int64
}

func nameID(name string) (ctlElemID, error) {
	var id ctlElemID
	if len(name) >= len(id.Name) {
		return id, fmt.Errorf("alsa: control name %q too long", name)
	}
	id.Iface = elemIfaceMixer
	copy(id.Name[:], name)
	return id, nil
}

// Info looks a control up by name.
func (m *Mixer) Info(name string) (ControlInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.info(name)
	if err != nil {
		return ControlInfo{}, err
	}
	return decodeInfo(name, info)
}

func (m *Mixer) info(name string) (*ctlElemInfo, error) {
	id, err := nameID(name)
	if err != nil {
		return nil, err
	}
	if m.fd < 0 {
		return nil, ErrClosed
	}
	info := &ctlElemInfo{ID: id}
	if err := ioctl(m.fd, ioctlElemInfo, unsafe.Pointer(info)); err != nil {
		if err == unix.ENOENT {
			return nil, fmt.Errorf("%w: %q", ErrNoControl, name)
		}
		return nil, fmt.Errorf("alsa: ELEM_INFO %q: %w", name, err)
	}
	return info, nil
}

func decodeInfo(name string, info *ctlElemInfo) (ControlInfo, error) {
	ci := ControlInfo{Name: name, Count: int(info.Count)}
	switch info.Type {
	case elemTypeBoolean:
		ci.Boolean, ci.Min, ci.Max, ci.Step = true, 0, 1, 1
	case elemTypeInteger:
		ci.Min = int64(binary.LittleEndian.Uint64(info.Value[0:]))
		ci.Max = int64(binary.LittleEndian.Uint64(info.Value[8:]))
		ci.Step = int64(binary.LittleEndian.Uint64(info.Value[16:]))
	default:
		return ci, fmt.Errorf("alsa: control %q has unsupported type %d", name, info.Type)
	}
	if ci.Count < 1 || ci.Count > 128 {
		return ci, fmt.Errorf("alsa: control %q has %d values", name, ci.Count)
	}
	return ci, nil
}

// Get returns the current values of a control.
func (m *Mixer) Get(name string) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.info(name)
	if err != nil {
		return nil, err
	}
	ci, err := decodeInfo(name, info)
	if err != nil {
		return nil, err
	}
	val := &ctlElemValue{ID: info.ID}
	if err := ioctl(m.fd, ioctlElemRead, unsafe.Pointer(val)); err != nil {
		return nil, fmt.Errorf("alsa: ELEM_READ %q: %w", name, err)
	}
	out := make([]int64, ci.Count)
	for i := range out {
		out[i] = int64(binary.LittleEndian.Uint64(val.Value[i*8:]))
	}
	return out, nil
}

// Set writes the same value to every channel of a control, clamped to its
// range.
func (m *Mixer) Set(name string, v int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.info(name)
	if err != nil {
		return err
	}
	ci, err := decodeInfo(name, info)
	if err != nil {
		return err
	}
	v = min(max(v, ci.Min), ci.Max)
	val := &ctlElemValue{ID: info.ID}
	for i := 0; i < ci.Count; i++ {
		binary.LittleEndian.PutUint64(val.Value[i*8:], uint64(v))
	}
	if err := ioctl(m.fd, ioctlElemWrite, unsafe.Pointer(val)); err != nil {
		return fmt.Errorf("alsa: ELEM_WRITE %q: %w", name, err)
	}
	return nil
}

// SetFraction sets a volume control to a position between 0 (minimum) and 1
// (maximum) of its raw range.
func (m *Mixer) SetFraction(name string, f float64) error {
	ci, err := m.Info(name)
	if err != nil {
		return err
	}
	return m.Set(name, fractionToRaw(ci, f))
}

// Fraction returns a volume control's position between 0 and 1, averaged over
// its channels.
func (m *Mixer) Fraction(name string) (float64, error) {
	ci, err := m.Info(name)
	if err != nil {
		return 0, err
	}
	vals, err := m.Get(name)
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, v := range vals {
		sum += rawToFraction(ci, v)
	}
	return sum / float64(len(vals)), nil
}

// First applies fn to the first control of names that exists.
func (m *Mixer) First(names []string, fn func(name string) error) error {
	for _, n := range names {
		err := fn(n)
		if errors.Is(err, ErrNoControl) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w: none of %v", ErrNoControl, names)
}

// SetCaptureVolume sets the microphone capture level, 0..1.
func (m *Mixer) SetCaptureVolume(f float64) error {
	return m.First(CaptureVolumeNames, func(n string) error { return m.SetFraction(n, f) })
}

// SetPlaybackVolume sets the speaker playback level, 0..1.
func (m *Mixer) SetPlaybackVolume(f float64) error {
	return m.First(PlaybackVolumeNames, func(n string) error { return m.SetFraction(n, f) })
}

// CaptureVolume returns the microphone capture level, 0..1.
func (m *Mixer) CaptureVolume() (float64, error) {
	var f float64
	err := m.First(CaptureVolumeNames, func(n string) (err error) { f, err = m.Fraction(n); return })
	return f, err
}

// PlaybackVolume returns the speaker playback level, 0..1.
func (m *Mixer) PlaybackVolume() (float64, error) {
	var f float64
	err := m.First(PlaybackVolumeNames, func(n string) (err error) { f, err = m.Fraction(n); return })
	return f, err
}

// BoostAvailable reports whether the chip has a microphone boost control.
func (m *Mixer) BoostAvailable() bool {
	return m.First(BoostNames, func(n string) error { _, err := m.Info(n); return err }) == nil
}

// SetBoost switches the microphone boost on (the control's maximum) or off
// (its minimum).
func (m *Mixer) SetBoost(on bool) error {
	f := 0.0
	if on {
		f = 1
	}
	return m.First(BoostNames, func(n string) error { return m.SetFraction(n, f) })
}

// Boost reports whether the microphone boost is on (more than half way up).
func (m *Mixer) Boost() (bool, error) {
	var f float64
	err := m.First(BoostNames, func(n string) (err error) { f, err = m.Fraction(n); return })
	return f > 0.5, err
}

// Unmute switches the capture and playback paths on. A missing switch is not
// an error: some chips have none.
func (m *Mixer) Unmute() error {
	for _, names := range [][]string{CaptureSwitchNames, PlaybackSwitchNames} {
		err := m.First(names, func(n string) error { return m.Set(n, 1) })
		if err != nil && !errors.Is(err, ErrNoControl) {
			return err
		}
	}
	return nil
}

// stepSize is the raw distance between two hardware positions.
func stepSize(ci ControlInfo) int64 { return max(ci.Step, 1) }

// stepCount is the number of steps above the lowest position: the control
// has stepCount+1 positions.
func stepCount(ci ControlInfo) int {
	if ci.Max <= ci.Min {
		return 0
	}
	return int((ci.Max - ci.Min) / stepSize(ci))
}

// rawToStep is the position (0..stepCount) nearest to a raw value.
func rawToStep(ci ControlInfo, v int64) int {
	n := stepCount(ci)
	k := int(math.Round(float64(v-ci.Min) / float64(stepSize(ci))))
	return min(max(k, 0), n)
}

// stepToRaw is the raw value of position k.
func stepToRaw(ci ControlInfo, k int) int64 {
	return ci.Min + int64(min(max(k, 0), stepCount(ci)))*stepSize(ci)
}

// fractionToRaw maps 0..1 to the nearest of the control's hardware steps: a
// fraction k/stepCount lands exactly on step k.
func fractionToRaw(ci ControlInfo, f float64) int64 {
	if math.IsNaN(f) {
		f = 0
	}
	f = math.Min(math.Max(f, 0), 1)
	return stepToRaw(ci, int(math.Round(f*float64(stepCount(ci)))))
}

// rawToFraction maps a raw value to 0..1 along the hardware steps, so that
// it is the inverse of fractionToRaw for every step.
func rawToFraction(ci ControlInfo, v int64) float64 {
	n := stepCount(ci)
	if n == 0 {
		return 0
	}
	return float64(rawToStep(ci, v)) / float64(n)
}

// tlvRead reads the dB information (TLV) of a control: its type, length and
// data, or nil when the control has none.
func (m *Mixer) tlvRead(info *ctlElemInfo) ([]byte, error) {
	if info.Access&accessTLVRead == 0 {
		return nil, nil
	}
	buf := make([]byte, tlvHeaderSize+tlvMaxBytes)
	binary.LittleEndian.PutUint32(buf[0:], info.ID.Numid)
	binary.LittleEndian.PutUint32(buf[4:], tlvMaxBytes)
	if err := ioctl(m.fd, ioctlTLVRead, unsafe.Pointer(&buf[0])); err != nil {
		return nil, fmt.Errorf("alsa: TLV_READ: %w", err)
	}
	b := buf[tlvHeaderSize:]
	if len(b) < 8 {
		return nil, nil
	}
	n := 8 + int(binary.LittleEndian.Uint32(b[4:]))
	if n > len(b) {
		return nil, nil
	}
	return b[:n], nil
}

// StepOf returns a volume control's hardware steps, position and, when the
// control has dB information, the gain at that position.
func (m *Mixer) StepOf(name string) (VolumeStep, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.info(name)
	if err != nil {
		return VolumeStep{}, err
	}
	ci, err := decodeInfo(name, info)
	if err != nil {
		return VolumeStep{}, err
	}
	val := &ctlElemValue{ID: info.ID}
	if err := ioctl(m.fd, ioctlElemRead, unsafe.Pointer(val)); err != nil {
		return VolumeStep{}, fmt.Errorf("alsa: ELEM_READ %q: %w", name, err)
	}
	var sum float64
	for i := 0; i < ci.Count; i++ {
		sum += float64(rawToStep(ci, int64(binary.LittleEndian.Uint64(val.Value[i*8:]))))
	}
	vs := VolumeStep{Steps: stepCount(ci), Step: int(math.Round(sum / float64(ci.Count)))}
	if tlv, err := m.tlvRead(info); err == nil && tlv != nil {
		vs.Dbs = make([]float64, vs.Steps+1)
		known := false
		for k := range vs.Dbs {
			db, ok := tlvDb(tlv, ci.Min, ci.Max, stepToRaw(ci, k))
			if !ok {
				db = math.NaN()
			}
			vs.Dbs[k], known = db, known || ok
		}
		if !known {
			vs.Dbs = nil
		} else if db := vs.Dbs[vs.Step]; !math.IsNaN(db) {
			vs.Db = &db
		}
	}
	return vs, nil
}

// CaptureStep returns the microphone capture control's step and dB.
func (m *Mixer) CaptureStep() (vs VolumeStep, err error) {
	err = m.First(CaptureVolumeNames, func(n string) (err error) { vs, err = m.StepOf(n); return })
	return
}

// PlaybackStep returns the speaker playback control's step and dB.
func (m *Mixer) PlaybackStep() (vs VolumeStep, err error) {
	err = m.First(PlaybackVolumeNames, func(n string) (err error) { vs, err = m.StepOf(n); return })
	return
}
