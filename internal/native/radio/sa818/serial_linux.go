package sa818

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/w1jar/nodespot/internal/native/radio/usbsys"
)

// SerialID is the USB id of the CH340 bridge on the SHARI.
var SerialID = usbsys.ID{Vendor: 0x1a86, Product: 0x7523}

// ErrNoPort is returned when no serial bridge is found.
var ErrNoPort = fmt.Errorf("sa818: no CH340 serial port found")

// FindPort returns the device node of the first CH340 serial port under
// sysRoot (default /sys), with nodes under devRoot (default /dev).
func FindPort(sysRoot, devRoot string) (string, error) {
	if sysRoot == "" {
		sysRoot = usbsys.DefaultRoot
	}
	if devRoot == "" {
		devRoot = "/dev"
	}
	devs, err := usbsys.Find(sysRoot, []usbsys.ID{SerialID}, "")
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		if ttys := d.TTYs(); len(ttys) > 0 {
			return filepath.Join(devRoot, ttys[0]), nil
		}
	}
	return "", ErrNoPort
}

// Port is a serial port set to 9600 8N1, raw, with a 100 ms read timeout.
type Port struct{ fd int }

// OpenSerial opens path and configures it for the module.
func OpenSerial(path string) (*Port, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("sa818: open %s: %w", path, err)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("sa818: %s is not a serial port: %w", path, err)
	}
	applyRaw9600(t)
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("sa818: configure %s: %w", path, err)
	}
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)
	return &Port{fd: fd}, nil
}

// applyRaw9600 sets 9600 baud, 8 data bits, no parity, 1 stop bit, no flow
// control, raw mode, read returning after 100 ms with whatever arrived.
func applyRaw9600(t *unix.Termios) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | unix.B9600
	t.Ispeed, t.Ospeed = unix.B9600, unix.B9600
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 1
}

// Read returns what arrived within the read timeout; a timeout with no data
// is reported so [Client] keeps waiting up to its own deadline.
func (p *Port) Read(b []byte) (int, error) {
	n, err := unix.Read(p.fd, b)
	if err != nil {
		if err == unix.EINTR {
			return 0, errTimeout
		}
		return 0, err
	}
	if n == 0 {
		return 0, errTimeout
	}
	return n, nil
}

// Write sends b.
func (p *Port) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := unix.Write(p.fd, b[total:])
		if err != nil {
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			return total, err
		}
		total += n
	}
	return total, nil
}

// Close releases the port.
func (p *Port) Close() error { return unix.Close(p.fd) }
