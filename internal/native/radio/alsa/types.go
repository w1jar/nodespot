package alsa

import (
	"unsafe"

	"github.com/w1jar/nodespot/internal/native/audio"
)

// Audio contract shared with the other native packages.
const (
	// SampleRate is the node audio rate in Hz.
	SampleRate = audio.SampleRate
	// FrameSamples is the number of samples in one 20 ms frame.
	FrameSamples = audio.FrameSamples
)

// Frame is 20 ms of signed 16-bit mono PCM at 8000 Hz.
type Frame = audio.Frame

// Hardware parameter indexes (SNDRV_PCM_HW_PARAM_*).
const (
	paramAccess     = 0
	paramFormat     = 1
	paramSubformat  = 2
	paramSampleBits = 8
	paramFrameBits  = 9
	paramChannels   = 10
	paramRate       = 11
	paramPeriodTime = 12
	paramPeriodSize = 13
	paramPeriodByte = 14
	paramPeriods    = 15
	paramBufferTime = 16
	paramBufferSize = 17
	paramBufferByte = 18
	paramTickTime   = 19

	firstMask     = paramAccess
	lastMask      = paramSubformat
	firstInterval = paramSampleBits
	lastInterval  = paramTickTime
)

// Access, format and subformat values.
const (
	accessRWInterleaved = 3 // SNDRV_PCM_ACCESS_RW_INTERLEAVED
	formatS16LE         = 2 // SNDRV_PCM_FORMAT_S16_LE
	subformatStd        = 0 // SNDRV_PCM_SUBFORMAT_STD
)

// Interval flag bits (the C bitfield, little endian).
const (
	ivOpenMin = 1 << 0
	ivOpenMax = 1 << 1
	ivInteger = 1 << 2
	ivEmpty   = 1 << 3
)

// mask is struct snd_mask: 256 bits.
type mask struct {
	Bits [8]uint32
}

// interval is struct snd_interval.
type interval struct {
	Min, Max uint32
	Flags    uint32
}

// hwParams is struct snd_pcm_hw_params.
type hwParams struct {
	Flags     uint32
	Masks     [lastMask - firstMask + 1]mask
	Mres      [5]mask
	Intervals [lastInterval - firstInterval + 1]interval
	Ires      [9]interval
	Rmask     uint32
	Cmask     uint32
	Info      uint32
	Msbits    uint32
	RateNum   uint32
	RateDen   uint32
	FifoSize  uint64
	Reserved  [64]byte
}

// swParams is struct snd_pcm_sw_params.
type swParams struct {
	TstampMode       int32
	PeriodStep       uint32
	SleepMin         uint32
	_                uint32
	AvailMin         uint64
	XferAlign        uint64
	StartThreshold   uint64
	StopThreshold    uint64
	SilenceThreshold uint64
	SilenceSize      uint64
	Boundary         uint64
	Proto            uint32
	TstampType       uint32
	Reserved         [56]byte
}

// xferi is struct snd_xferi, the argument of READI/WRITEI_FRAMES.
type xferi struct {
	Result int64
	Buf    unsafe.Pointer
	Frames uint64
}

// ctlElemID is struct snd_ctl_elem_id.
type ctlElemID struct {
	Numid     uint32
	Iface     int32
	Device    uint32
	Subdevice uint32
	Name      [44]byte
	Index     uint32
}

// ctlElemInfo is struct snd_ctl_elem_info. Value is the union of
// integer/integer64/enumerated/reserved (128 bytes).
type ctlElemInfo struct {
	ID     ctlElemID
	Type   int32
	Access uint32
	Count  uint32
	Owner  int32
	Value  [128]byte
	Dimen  [64]byte
}

// ctlElemValue is struct snd_ctl_elem_value. Value holds the union of long
// value[128], integer64 value[64] and so on (1024 bytes).
type ctlElemValue struct {
	ID       ctlElemID
	Indirect uint32
	_        uint32
	Value    [1024]byte
	Reserved [128]byte
}

// ioctl direction bits and encoder (asm-generic, used by amd64 and arm64).
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2

	iocNRBits   = 8
	iocTypeBits = 8
	iocSizeBits = 14

	iocNRShift   = 0
	iocTypeShift = iocNRShift + iocNRBits
	iocSizeShift = iocTypeShift + iocTypeBits
	iocDirShift  = iocSizeShift + iocSizeBits
)

func ioc(dir, typ, nr, size uintptr) uintptr {
	return dir<<iocDirShift | typ<<iocTypeShift | nr<<iocNRShift | size<<iocSizeShift
}

// PCM ioctls ('A').
var (
	ioctlPVersion = ioc(iocRead, 'A', 0x00, 4)
	ioctlHWRefine = ioc(iocRead|iocWrite, 'A', 0x10, unsafe.Sizeof(hwParams{}))
	ioctlHWParams = ioc(iocRead|iocWrite, 'A', 0x11, unsafe.Sizeof(hwParams{}))
	ioctlHWFree   = ioc(iocNone, 'A', 0x12, 0)
	ioctlSWParams = ioc(iocRead|iocWrite, 'A', 0x13, unsafe.Sizeof(swParams{}))
	ioctlPrepare  = ioc(iocNone, 'A', 0x40, 0)
	ioctlStart    = ioc(iocNone, 'A', 0x42, 0)
	ioctlDrop     = ioc(iocNone, 'A', 0x43, 0)
	ioctlWriteI   = ioc(iocWrite, 'A', 0x50, unsafe.Sizeof(xferi{}))
	ioctlReadI    = ioc(iocRead, 'A', 0x51, unsafe.Sizeof(xferi{}))
)

// Control ioctls ('U').
var (
	ioctlElemInfo  = ioc(iocRead|iocWrite, 'U', 0x11, unsafe.Sizeof(ctlElemInfo{}))
	ioctlElemRead  = ioc(iocRead|iocWrite, 'U', 0x12, unsafe.Sizeof(ctlElemValue{}))
	ioctlElemWrite = ioc(iocRead|iocWrite, 'U', 0x13, unsafe.Sizeof(ctlElemValue{}))
	// ioctlTLVRead is SNDRV_CTL_IOCTL_TLV_READ: struct snd_ctl_tlv, a header
	// of numid and length followed by the data.
	ioctlTLVRead = ioc(iocRead|iocWrite, 'U', 0x1a, tlvHeaderSize)
)

// anyParams returns parameters that accept everything, the equivalent of
// snd_pcm_hw_params_any: every mask full, every interval open.
func anyParams() *hwParams {
	p := &hwParams{Rmask: ^uint32(0), Info: ^uint32(0)}
	for i := range p.Masks {
		for j := range p.Masks[i].Bits {
			p.Masks[i].Bits[j] = ^uint32(0)
		}
	}
	for i := range p.Intervals {
		p.Intervals[i] = interval{Min: 0, Max: ^uint32(0)}
	}
	return p
}

// setMask restricts a mask parameter to a single value.
func (p *hwParams) setMask(param int, bit uint) {
	m := &p.Masks[param-firstMask]
	*m = mask{}
	m.Bits[bit>>5] = 1 << (bit & 31)
}

// maskHas reports whether a mask parameter allows the bit.
func (p *hwParams) maskHas(param int, bit uint) bool {
	return p.Masks[param-firstMask].Bits[bit>>5]&(1<<(bit&31)) != 0
}

// setRange restricts an interval parameter to [lo, hi].
func (p *hwParams) setRange(param int, lo, hi uint32) {
	iv := &p.Intervals[param-firstInterval]
	iv.Min, iv.Max = lo, hi
	iv.Flags = ivInteger
}

// setExact fixes an interval parameter to one value.
func (p *hwParams) setExact(param int, v uint32) { p.setRange(param, v, v) }

// get returns an interval parameter's bounds.
func (p *hwParams) get(param int) (lo, hi uint32) {
	iv := p.Intervals[param-firstInterval]
	return iv.Min, iv.Max
}

// Control access bit and TLV buffer sizes.
const (
	accessTLVRead = 1 << 28 // SNDRV_CTL_ELEM_ACCESS_TLV_READ
	tlvHeaderSize = 8
	tlvMaxBytes   = 512
)
