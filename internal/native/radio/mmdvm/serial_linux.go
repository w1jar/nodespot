package mmdvm

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var bauds = map[int]uint32{
	9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600,
	115200: unix.B115200, 230400: unix.B230400, 460800: unix.B460800, 500000: unix.B500000,
	576000: unix.B576000, 921600: unix.B921600,
}

// OpenSerial opens the modem's UART at baud (0 means DefaultBaud), 8N1, raw,
// no flow control, and drops anything already buffered. The port is
// non-blocking under the Go runtime poller, so Close from another goroutine
// ends a Read in progress.
func OpenSerial(path string, baud int) (io.ReadWriteCloser, error) {
	if baud == 0 {
		baud = DefaultBaud
	}
	speed, ok := bauds[baud]
	if !ok {
		return nil, fmt.Errorf("mmdvm: unsupported baud rate %d", baud)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("mmdvm: open %s: %w", path, err)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("mmdvm: %s is not a serial port: %w", path, err)
	}
	applyRaw(t, speed)
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("mmdvm: configure %s: %w", path, err)
	}
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("mmdvm: open %s: bad descriptor", path)
	}
	return f, nil
}

// applyRaw sets 8 data bits, no parity, 1 stop bit, no flow control, raw
// mode at speed. With a non-blocking descriptor VMIN and VTIME do not
// matter; they are set for a blocking reader all the same.
func applyRaw(t *unix.Termios, speed uint32) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | speed
	t.Ispeed, t.Ospeed = speed, speed
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
}
