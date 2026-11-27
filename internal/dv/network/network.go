// Package network defines what a digital voice network looks like to the
// bridge: it hears calls (Sink) and carries the node's transmissions.
package network

import (
	"context"
	"fmt"
	"sync"

	"github.com/w1jar/nodespot/internal/dv/ctl"
)

// Codec is the voice codec a network carries.
type Codec int

const (
	CodecAMBE Codec = iota
	CodecIMBE
	CodecC2 // Codec2 3200, 8 bytes per frame (M17)
)

// VoiceFrame is one 20 ms codec frame.
type VoiceFrame struct {
	Call uint64 // ctl.Talk.ID
	Seq  uint32
	// Codec says what Data holds. The zero value is CodecAMBE, so networks
	// that only carry AMBE need not set it. A network may mix codecs from
	// frame to frame (YSF carries DN as AMBE and Voice FR as IMBE); on TX
	// the bridge sets it to the network's Codec().
	Codec Codec
	Data  []byte // 7 (AMBE), 11 (IMBE) or 8 (Codec2) bytes, as Codec says
}

// CallHeader starts a call.
type CallHeader struct {
	Call    uint64
	Src     string // callsign
	SrcID   uint32
	TG      uint32
	Room    string
	Slot    int
	Private bool
}

// Sink receives what a network hears. Calls come from the network's
// goroutine; implementations (the bridge router) must not block.
type Sink interface {
	Header(h CallHeader)
	Voice(f VoiceFrame)
	End(call uint64)
	Login(ctl.LoginState, string)
}

// Network is one digital voice network.
type Network interface {
	Mode() string // ctl.Mode*
	Codec() Codec
	Run(ctx context.Context, s Sink) error // connect/login/keepalive until ctx ends
	Tune(ctx context.Context, t ctl.Target) error
	Unlink(ctx context.Context) error
	// Local TX from the node.
	StartTX(h CallHeader) error
	WriteVoice(f VoiceFrame) error
	EndTX(call uint64) error
	Status() ctl.NetStatus
}

// Factory builds a network from its config.
type Factory func(cfg ctl.NetConfig, id ctl.Identity) (Network, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a factory for mode. It panics on a duplicate.
func Register(mode string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[mode]; dup {
		panic("network: duplicate registration of " + mode)
	}
	registry[mode] = f
}

// Available reports whether mode has an implementation in this build.
func Available(mode string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[mode]
	return ok
}

// Error is returned by New for an unregistered mode; Code is
// ctl.CodeNotAvailable.
type Error struct{ Code, Mode string }

func (e *Error) Error() string { return fmt.Sprintf("network %s: %s", e.Mode, e.Code) }

// New builds the network for cfg.Mode.
func New(cfg ctl.NetConfig, id ctl.Identity) (Network, error) {
	mu.RLock()
	f := registry[cfg.Mode]
	mu.RUnlock()
	if f == nil {
		return nil, &Error{Code: ctl.CodeNotAvailable, Mode: cfg.Mode}
	}
	return f(cfg, id)
}
