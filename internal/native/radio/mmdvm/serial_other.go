//go:build !linux

package mmdvm

import (
	"errors"
	"io"
)

// OpenSerial is only available on Linux.
func OpenSerial(path string, baud int) (io.ReadWriteCloser, error) {
	return nil, errors.New("mmdvm: serial ports are only supported on Linux")
}
