package mode

import "github.com/w1jar/nodespot/internal/dv/network"

// FrameKind says what a FrameEvent carries.
type FrameKind int

// Frame event kinds.
const (
	FrameHeader FrameKind = iota + 1 // a network call starts (Header)
	FrameVoice                       // one voice frame of it (Frame)
	FrameEnd                         // it ended (Call)
)

// FrameEvent is one step of a network call delivered as coded frames.
// Call is the mode's own id of the call: Header.Call and Frame.Call carry
// the same value.
type FrameEvent struct {
	Kind   FrameKind
	Call   uint64
	Header network.CallHeader
	Frame  network.VoiceFrame
}

// FrameMode is a Mode that can also carry a network's coded voice frames
// as they are, with no vocoder in the path: a digital radio (an MMDVM
// modem) hands DMR AMBE straight to a DMR network and back. The audio
// methods of Mode keep working; a caller uses one path or the other for a
// call, not both.
type FrameMode interface {
	Mode
	// FrameCodec is the codec of the network's frames, and false when the
	// mode is not started.
	FrameCodec() (network.Codec, bool)
	// SetFrames switches received calls between decoded audio (Recv, the
	// default) and coded frames (RecvFrames). Display events (talk start
	// and end) are sent either way.
	SetFrames(on bool)
	// RecvFrames carries received calls while SetFrames(true). The channel
	// stays the same for the life of the Mode; events are dropped when it
	// is full.
	RecvFrames() <-chan FrameEvent
	// SendHeader starts a transmission of coded frames, ending one already
	// under way. The mode fills in what the network needs from its own
	// link (slot, room); h.Src, h.SrcID, h.TG and h.Private are used when
	// set. h.Call and h.Slot are ignored.
	SendHeader(h network.CallHeader) error
	// SendFrame is one coded frame of the transmission. f.Codec must be
	// FrameCodec; f.Call and f.Seq are ignored (the mode numbers frames
	// itself). It never blocks.
	SendFrame(f network.VoiceFrame) error
	// EndFrames ends the transmission behind the frames already queued.
	EndFrames() error
}
