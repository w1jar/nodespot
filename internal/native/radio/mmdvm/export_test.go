package mmdvm

import (
	"io"
	"log/slog"
	"sync/atomic"
)

// TXCommand exposes txCommand to the tests.
func TXCommand(cmd byte) bool { return txCommand(cmd) }

// FilterWrite writes frames through a receive-only writer and returns how
// many it dropped.
func FilterWrite(w io.Writer, frames ...[]byte) int {
	var n atomic.Uint64
	wr := &writer{port: w, rxOnly: true, blocked: &n, log: slog.New(slog.DiscardHandler)}
	_ = wr.write(frames...)
	return int(n.Load())
}
