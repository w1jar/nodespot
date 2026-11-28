//go:build amd64 || arm64

package alsa

import "unsafe"

// Supported is true where the kernel structure layouts in types.go match the
// 64-bit ABIs, the only ones the analog audio code is written and tested for.
const Supported = true

// Compile-time layout checks for the 64-bit ABIs. A wrong size makes the
// array length negative and the build fails.
var (
	_ [608 - unsafe.Sizeof(hwParams{})]byte
	_ [unsafe.Sizeof(hwParams{}) - 608]byte
	_ [136 - unsafe.Sizeof(swParams{})]byte
	_ [unsafe.Sizeof(swParams{}) - 136]byte
	_ [24 - unsafe.Sizeof(xferi{})]byte
	_ [unsafe.Sizeof(xferi{}) - 24]byte
	_ [64 - unsafe.Sizeof(ctlElemID{})]byte
	_ [unsafe.Sizeof(ctlElemID{}) - 64]byte
	_ [272 - unsafe.Sizeof(ctlElemInfo{})]byte
	_ [unsafe.Sizeof(ctlElemInfo{}) - 272]byte
	_ [1224 - unsafe.Sizeof(ctlElemValue{})]byte
	_ [unsafe.Sizeof(ctlElemValue{}) - 1224]byte
)

// ErrUnsupported is what the device openers return where Supported is false
// (never on this architecture).
var ErrUnsupported error
