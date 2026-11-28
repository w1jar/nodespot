package alsa

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Direction selects capture or playback.
type Direction int

// Stream directions.
const (
	DirCapture Direction = iota
	DirPlayback
)

// ErrClosed is returned by operations on a closed device.
var ErrClosed = errors.New("alsa: device closed")

// PCMPath returns the device node for a card, device and direction.
func PCMPath(card, device int, dir Direction) string {
	c := 'c'
	if dir == DirPlayback {
		c = 'p'
	}
	return fmt.Sprintf("/dev/snd/pcmC%dD%d%c", card, device, c)
}

// Params selects the hardware format. The zero value means 48000 Hz with a
// 10 ms period and an 80 ms buffer, mono if the device takes it and stereo
// otherwise (see OpenPCM).
type Params struct {
	Rate         int
	Channels     int
	PeriodFrames int
	Periods      int
}

func (p Params) withDefaults() Params {
	if p.Rate == 0 {
		p.Rate = hwRate
	}
	if p.PeriodFrames == 0 {
		p.PeriodFrames = 480
	}
	if p.Periods == 0 {
		p.Periods = 8
	}
	return p
}

// PCM is an open ALSA PCM stream, S16_LE interleaved.
type PCM struct {
	fd    int
	dir   Direction
	par   Params
	mu    sync.RWMutex // held shared by I/O, exclusively by Close
	shut  atomic.Bool
	xruns atomic.Uint64
	// period and buffer size the kernel granted, in frames.
	period, buffer int
}

// OpenPCM opens path and configures it. The stream is left prepared; capture
// starts on the first Read and playback on the first Write. When par.Channels
// is 0 it tries mono first and then stereo (CM108-family playback is stereo
// only); Channels reports what was granted.
func OpenPCM(path string, dir Direction, par Params) (*PCM, error) {
	if !Supported {
		return nil, ErrUnsupported
	}
	par = par.withDefaults()
	tries := []int{par.Channels}
	if par.Channels == 0 {
		tries = []int{1, 2}
	}
	var lastErr error
	for _, ch := range tries {
		par.Channels = ch
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("alsa: open %s: %w", path, err)
		}
		p := &PCM{fd: fd, dir: dir, par: par}
		if lastErr = p.configure(); lastErr == nil {
			return p, nil
		}
		unix.Close(fd)
	}
	return nil, fmt.Errorf("alsa: configure %s: %w", path, lastErr)
}

// Channels reports the channel count the stream was configured with.
func (p *PCM) Channels() int { return p.par.Channels }

func (p *PCM) configure() error {
	var ver int32
	if err := ioctl(p.fd, ioctlPVersion, unsafe.Pointer(&ver)); err != nil {
		return fmt.Errorf("PVERSION: %w", err)
	}
	// First ask for the exact period and buffer, then settle for whatever
	// ranges the driver accepts.
	attempts := []func(*hwParams){
		func(h *hwParams) {
			h.setExact(paramPeriodSize, uint32(p.par.PeriodFrames))
			h.setExact(paramPeriods, uint32(p.par.Periods))
		},
		func(h *hwParams) {
			h.setRange(paramPeriodSize, 160, 4800)
			h.setRange(paramPeriods, 2, 64)
		},
	}
	var hw *hwParams
	var err error
	for _, shape := range attempts {
		hw = anyParams()
		hw.setMask(paramAccess, accessRWInterleaved)
		hw.setMask(paramFormat, formatS16LE)
		hw.setMask(paramSubformat, subformatStd)
		hw.setExact(paramSampleBits, 16)
		hw.setExact(paramFrameBits, uint32(16*p.par.Channels))
		hw.setExact(paramChannels, uint32(p.par.Channels))
		hw.setExact(paramRate, uint32(p.par.Rate))
		shape(hw)
		if err = ioctl(p.fd, ioctlHWParams, unsafe.Pointer(hw)); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("HW_PARAMS: %w", err)
	}
	period, _ := hw.get(paramPeriodSize)
	buffer, _ := hw.get(paramBufferSize)
	p.period, p.buffer = int(period), int(buffer)

	sw := &swParams{
		PeriodStep:     1,
		AvailMin:       uint64(p.period),
		XferAlign:      1,
		StartThreshold: 1,
		StopThreshold:  uint64(p.buffer),
		Proto:          uint32(ver),
	}
	if err := ioctl(p.fd, ioctlSWParams, unsafe.Pointer(sw)); err != nil {
		return fmt.Errorf("SW_PARAMS: %w", err)
	}
	if err := ioctl(p.fd, ioctlPrepare, nil); err != nil {
		return fmt.Errorf("PREPARE: %w", err)
	}
	return nil
}

// PeriodFrames and BufferFrames report what the driver granted.
func (p *PCM) PeriodFrames() int { return p.period }

// BufferFrames reports the buffer size the driver granted, in frames.
func (p *PCM) BufferFrames() int { return p.buffer }

// Xruns counts overruns and underruns recovered so far.
func (p *PCM) Xruns() uint64 { return p.xruns.Load() }

// recover re-prepares the stream after an xrun or suspend.
func (p *PCM) recover(err error) error {
	switch err {
	case unix.EPIPE:
		p.xruns.Add(1)
	case unix.ESTRPIPE:
		// Suspended: nothing to resume on a USB stream that went away
		// cleanly; fall through to prepare.
	default:
		return err
	}
	if err := ioctl(p.fd, ioctlPrepare, nil); err != nil {
		return fmt.Errorf("alsa: recover: %w", err)
	}
	if p.dir == DirCapture {
		if err := ioctl(p.fd, ioctlStart, nil); err != nil {
			return fmt.Errorf("alsa: restart: %w", err)
		}
	}
	return nil
}

// wait polls for room or data, returning ErrClosed when the device is shut.
func (p *PCM) wait() error {
	ev := int16(unix.POLLIN)
	if p.dir == DirPlayback {
		ev = unix.POLLOUT
	}
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: ev}}
	for {
		if p.shut.Load() {
			return ErrClosed
		}
		_, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("alsa: poll: %w", err)
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			// POLLERR also signals an xrun; let the next ioctl report it.
			return nil
		}
		if fds[0].Revents&ev != 0 {
			return nil
		}
	}
}

// Read fills buf with captured samples (mono frames), blocking until it is
// full. Overruns are recovered and counted; the samples lost are not
// replaced.
func (p *PCM) Read(buf []int16) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.shut.Load() {
		return ErrClosed
	}
	ch := p.par.Channels
	if len(buf)%ch != 0 {
		return fmt.Errorf("alsa: read of %d samples is not a whole number of frames", len(buf))
	}
	done := 0
	started := false
	for done < len(buf) {
		x := xferi{Buf: unsafe.Pointer(&buf[done]), Frames: uint64((len(buf) - done) / ch)}
		err := ioctl(p.fd, ioctlReadI, unsafe.Pointer(&x))
		if x.Result > 0 {
			done += int(x.Result) * ch
		}
		switch {
		case err == nil:
			continue
		case err == unix.EAGAIN:
			if !started {
				// An idle prepared stream does not start by itself under
				// O_NONBLOCK on every driver.
				started = true
				_ = ioctl(p.fd, ioctlStart, nil)
			}
			if err := p.wait(); err != nil {
				return err
			}
		default:
			if err := p.recover(err); err != nil {
				return fmt.Errorf("alsa: read: %w", err)
			}
		}
	}
	return nil
}

// Write plays samples, blocking until all are queued. Underruns are
// recovered and counted.
func (p *PCM) Write(buf []int16) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.shut.Load() {
		return ErrClosed
	}
	ch := p.par.Channels
	if len(buf)%ch != 0 {
		return fmt.Errorf("alsa: write of %d samples is not a whole number of frames", len(buf))
	}
	done := 0
	for done < len(buf) {
		x := xferi{Buf: unsafe.Pointer(&buf[done]), Frames: uint64((len(buf) - done) / ch)}
		err := ioctl(p.fd, ioctlWriteI, unsafe.Pointer(&x))
		if x.Result > 0 {
			done += int(x.Result) * ch
		}
		switch {
		case err == nil:
			continue
		case err == unix.EAGAIN:
			if err := p.wait(); err != nil {
				return err
			}
		default:
			if err := p.recover(err); err != nil {
				return fmt.Errorf("alsa: write: %w", err)
			}
		}
	}
	return nil
}

// Drop discards queued audio and stops the stream.
func (p *PCM) Drop() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.shut.Load() {
		return ErrClosed
	}
	if err := ioctl(p.fd, ioctlDrop, nil); err != nil {
		return err
	}
	return ioctl(p.fd, ioctlPrepare, nil)
}

// Close stops the stream and releases the device. It waits for a blocked
// Read or Write to notice, at most about 100 ms.
func (p *PCM) Close() error {
	if !p.shut.CompareAndSwap(false, true) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = ioctl(p.fd, ioctlDrop, nil)
	return unix.Close(p.fd)
}
