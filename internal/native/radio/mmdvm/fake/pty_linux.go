package fake

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenPTY makes a pseudo-terminal pair. Serve the master with the fake
// modem and open the returned path with mmdvm.OpenSerial, to run the real
// serial code path against the fake.
func OpenPTY() (master *os.File, path string, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(fd)
		return nil, "", fmt.Errorf("fake: unlock pty: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		unix.Close(fd)
		return nil, "", fmt.Errorf("fake: pty number: %w", err)
	}
	// Raw mode on the master side too, so nothing is echoed or translated.
	if t, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		t.Oflag &^= unix.OPOST
		t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		t.Cflag &^= unix.CSIZE | unix.PARENB
		t.Cflag |= unix.CS8
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, t)
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), fmt.Sprintf("/dev/pts/%d", n), nil
}
