package alsa

import (
	"testing"
	"unsafe"
)

// The kernel ABI on amd64 and arm64 is identical (LP64, asm-generic ioctl
// numbers); the compile-time assertions in types.go enforce the sizes on
// both. These tests pin the field offsets and the request numbers.

func TestStructLayout(t *testing.T) {
	if !Supported {
		t.Skip("64-bit layouts only")
	}
	var hw hwParams
	var sw swParams
	var xf xferi
	var info ctlElemInfo
	var val ctlElemValue
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"hwParams size", unsafe.Sizeof(hw), 608},
		{"hwParams.Masks", unsafe.Offsetof(hw.Masks), 4},
		{"hwParams.Mres", unsafe.Offsetof(hw.Mres), 100},
		{"hwParams.Intervals", unsafe.Offsetof(hw.Intervals), 260},
		{"hwParams.Ires", unsafe.Offsetof(hw.Ires), 404},
		{"hwParams.Rmask", unsafe.Offsetof(hw.Rmask), 512},
		{"hwParams.FifoSize", unsafe.Offsetof(hw.FifoSize), 536},
		{"hwParams.Reserved", unsafe.Offsetof(hw.Reserved), 544},
		{"swParams size", unsafe.Sizeof(sw), 136},
		{"swParams.AvailMin", unsafe.Offsetof(sw.AvailMin), 16},
		{"swParams.Boundary", unsafe.Offsetof(sw.Boundary), 64},
		{"swParams.Proto", unsafe.Offsetof(sw.Proto), 72},
		{"swParams.Reserved", unsafe.Offsetof(sw.Reserved), 80},
		{"xferi size", unsafe.Sizeof(xf), 24},
		{"xferi.Buf", unsafe.Offsetof(xf.Buf), 8},
		{"xferi.Frames", unsafe.Offsetof(xf.Frames), 16},
		{"mask size", unsafe.Sizeof(mask{}), 32},
		{"interval size", unsafe.Sizeof(interval{}), 12},
		{"ctlElemID size", unsafe.Sizeof(ctlElemID{}), 64},
		{"ctlElemInfo size", unsafe.Sizeof(info), 272},
		{"ctlElemInfo.Type", unsafe.Offsetof(info.Type), 64},
		{"ctlElemInfo.Value", unsafe.Offsetof(info.Value), 80},
		{"ctlElemInfo.Dimen", unsafe.Offsetof(info.Dimen), 208},
		{"ctlElemValue size", unsafe.Sizeof(val), 1224},
		{"ctlElemValue.Value", unsafe.Offsetof(val.Value), 72},
		{"ctlElemValue.Reserved", unsafe.Offsetof(val.Reserved), 1096},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestIoctlNumbers(t *testing.T) {
	if !Supported {
		t.Skip("64-bit layouts only")
	}
	// Values as seen in <sound/asound.h> on 64-bit Linux.
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"PVERSION", ioctlPVersion, 0x80044100},
		{"HW_REFINE", ioctlHWRefine, 0xc2604110},
		{"HW_PARAMS", ioctlHWParams, 0xc2604111},
		{"SW_PARAMS", ioctlSWParams, 0xc0884113},
		{"PREPARE", ioctlPrepare, 0x4140},
		{"START", ioctlStart, 0x4142},
		{"DROP", ioctlDrop, 0x4143},
		{"WRITEI_FRAMES", ioctlWriteI, 0x40184150},
		{"READI_FRAMES", ioctlReadI, 0x80184151},
		{"ELEM_INFO", ioctlElemInfo, 0xc1105511},
		{"ELEM_READ", ioctlElemRead, 0xc4c85512},
		{"ELEM_WRITE", ioctlElemWrite, 0xc4c85513},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %#x, want %#x", tc.name, tc.got, tc.want)
		}
	}
}

func TestHWParamsHelpers(t *testing.T) {
	p := anyParams()
	if p.Rmask != ^uint32(0) {
		t.Fatalf("rmask = %#x", p.Rmask)
	}
	p.setMask(paramFormat, formatS16LE)
	p.setMask(paramAccess, accessRWInterleaved)
	p.setExact(paramRate, 48000)
	p.setRange(paramPeriods, 2, 64)

	tests := []struct {
		name string
		ok   bool
	}{
		{"S16_LE allowed", p.maskHas(paramFormat, formatS16LE)},
		{"S8 refused", !p.maskHas(paramFormat, 0)},
		{"S16_BE refused", !p.maskHas(paramFormat, 3)},
		{"interleaved allowed", p.maskHas(paramAccess, accessRWInterleaved)},
		{"mmap refused", !p.maskHas(paramAccess, 0)},
	}
	for _, tc := range tests {
		if !tc.ok {
			t.Errorf("%s", tc.name)
		}
	}
	if lo, hi := p.get(paramRate); lo != 48000 || hi != 48000 {
		t.Errorf("rate = %d..%d", lo, hi)
	}
	if lo, hi := p.get(paramPeriods); lo != 2 || hi != 64 {
		t.Errorf("periods = %d..%d", lo, hi)
	}
	if iv := p.Intervals[paramRate-firstInterval]; iv.Flags != ivInteger {
		t.Errorf("flags = %#x", iv.Flags)
	}
	// A mask bit above bit 31 lands in the right word.
	p.setMask(paramFormat, 40)
	if p.Masks[paramFormat].Bits[1] != 1<<8 || p.Masks[paramFormat].Bits[0] != 0 {
		t.Errorf("mask words = %v", p.Masks[paramFormat].Bits)
	}
}

func TestFractionMapping(t *testing.T) {
	ci := ControlInfo{Min: 0, Max: 39, Step: 1, Count: 1}
	tests := []struct {
		f    float64
		want int64
	}{{0, 0}, {1, 39}, {0.5, 20}, {-1, 0}, {2, 39}}
	for _, tc := range tests {
		if got := fractionToRaw(ci, tc.f); got != tc.want {
			t.Errorf("fractionToRaw(%v) = %d, want %d", tc.f, got, tc.want)
		}
	}
	if got := fractionToRaw(ControlInfo{Min: 10, Max: 20, Step: 5}, 0.5); got != 15 {
		t.Errorf("stepped = %d", got)
	}
	if got := rawToFraction(ci, 39); got != 1 {
		t.Errorf("rawToFraction = %v", got)
	}
	if got := rawToFraction(ControlInfo{}, 5); got != 0 {
		t.Errorf("degenerate = %v", got)
	}
}

func TestDecodeInfo(t *testing.T) {
	var info ctlElemInfo
	info.Type = elemTypeInteger
	info.Count = 2
	info.Value[0] = 0xF0 // min = -16 (low byte only, rest below)
	for i := 1; i < 8; i++ {
		info.Value[i] = 0xFF
	}
	info.Value[8] = 39
	info.Value[16] = 1
	ci, err := decodeInfo("x", &info)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Min != -16 || ci.Max != 39 || ci.Step != 1 || ci.Count != 2 {
		t.Errorf("got %+v", ci)
	}
	info.Type = 3 // enumerated
	if _, err := decodeInfo("x", &info); err == nil {
		t.Error("enumerated control accepted")
	}
}

func TestNameID(t *testing.T) {
	id, err := nameID("Mic Capture Volume")
	if err != nil {
		t.Fatal(err)
	}
	if id.Iface != elemIfaceMixer || string(id.Name[:18]) != "Mic Capture Volume" || id.Name[18] != 0 {
		t.Errorf("id = %+v", id)
	}
	long := make([]byte, 50)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := nameID(string(long)); err == nil {
		t.Error("oversized name accepted")
	}
}
