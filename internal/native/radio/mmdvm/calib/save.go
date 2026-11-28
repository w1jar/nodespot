package calib

import (
	"errors"
	"fmt"

	"github.com/w1jar/nodespot/internal/native/node"
	"github.com/w1jar/nodespot/internal/native/radio"
)

// SaveOffset writes rxOffsetHz into the node config at path and returns the
// value it replaced. Every other setting is kept as the node's own loader
// reads it; the file is checked with the node's validation, replaced
// atomically and keeps its permission bits and owner (so the node, which
// runs as another user, can still read it).
func SaveOffset(path string, off int) (old int, err error) {
	c, err := node.LoadConfig(path)
	if err != nil {
		return 0, err
	}
	if radio.Preset(c.Station.Radio.Preset) != radio.PresetMMDVM {
		return 0, errors.New("the radio preset is not mmdvm")
	}
	if c.Station.Radio.MMDVM == nil {
		c.Station.Radio.MMDVM = &node.MMDVMConfig{}
	}
	m := c.Station.Radio.MMDVM
	old = m.RXOffsetHz
	m.RXOffsetHz = off
	if err := node.SaveConfigKeep(path, c); err != nil {
		return old, fmt.Errorf("not saved: %w", err)
	}
	return old, nil
}
