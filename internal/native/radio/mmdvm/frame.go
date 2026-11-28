package mmdvm

import (
	"errors"
	"fmt"
)

// FrameStart is the first byte of every frame.
const FrameStart = 0xE0

// Frame size limits. A short frame's length byte counts the whole frame; a
// long frame (E0 00 L cmd ...) is L+255 bytes long with a 4-byte header.
const (
	MaxShortFrame = 255
	MaxLongFrame  = 255 + 255
)

// ErrFrameTooLong is returned by Encode for a payload that does not fit even
// a long frame.
var ErrFrameTooLong = errors.New("mmdvm: frame too long")

// Frame is one decoded frame: the command byte and its payload.
type Frame struct {
	Cmd     byte
	Payload []byte
}

func (f Frame) String() string {
	return fmt.Sprintf("%s % x", CommandName(f.Cmd), f.Payload)
}

// Encode builds the frame for cmd and payload: the short form when the
// whole frame is up to 255 bytes, the long form otherwise. MMDVM_HS only
// reads short frames; no DMR frame needs the long form.
func Encode(cmd byte, payload []byte) ([]byte, error) {
	n := 3 + len(payload)
	if n <= MaxShortFrame {
		b := make([]byte, 0, n)
		b = append(b, FrameStart, byte(n), cmd)
		return append(b, payload...), nil
	}
	n++
	if n > MaxLongFrame {
		return nil, ErrFrameTooLong
	}
	b := make([]byte, 0, n)
	b = append(b, FrameStart, 0, byte(n-255), cmd)
	return append(b, payload...), nil
}

// encode is Encode for frames the package knows are short.
func encode(cmd byte, payload ...byte) []byte {
	b, err := Encode(cmd, payload)
	if err != nil {
		panic(err)
	}
	return b
}

// Decoder splits a byte stream into frames. Bytes before a frame start, and
// frames with an impossible length, are skipped (counted in Skipped). The
// zero value is ready to use; not safe for concurrent use.
type Decoder struct {
	buf     []byte
	need    int // total length of the frame in buf, 0 = not known yet
	hdr     int // header length, 3 or 4
	Skipped uint64
}

// Feed adds p to the stream and calls emit for every complete frame, in
// order. The payload passed to emit is a fresh slice.
func (d *Decoder) Feed(p []byte, emit func(Frame)) {
	for _, c := range p {
		switch {
		case len(d.buf) == 0:
			if c != FrameStart {
				d.Skipped++
				continue
			}
			d.buf = append(d.buf, c)
			d.need, d.hdr = 0, 3
		case len(d.buf) == 1:
			switch {
			case c == 0:
				d.hdr = 4 // long frame: the length follows
			case c < 3:
				d.reset(c)
				continue
			default:
				d.need = int(c)
			}
			d.buf = append(d.buf, c)
		case len(d.buf) == 2 && d.hdr == 4:
			if c == 0 {
				d.reset(c)
				continue
			}
			d.need = int(c) + 255
			d.buf = append(d.buf, c)
		default:
			d.buf = append(d.buf, c)
		}
		if d.need > 0 && len(d.buf) == d.need {
			f := Frame{Cmd: d.buf[d.hdr-1], Payload: append([]byte(nil), d.buf[d.hdr:]...)}
			d.buf = d.buf[:0]
			d.need = 0
			emit(f)
		}
	}
}

// reset drops a bad frame start; c, the byte that made it bad, may start
// the next frame.
func (d *Decoder) reset(c byte) {
	d.Skipped += uint64(len(d.buf))
	d.buf = d.buf[:0]
	d.need = 0
	if c == FrameStart {
		d.buf = append(d.buf, c)
		d.hdr = 3
	} else {
		d.Skipped++
	}
}
