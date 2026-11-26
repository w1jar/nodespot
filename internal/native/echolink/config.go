package echolink

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"
)

// Defaults.
const (
	// DirectoryPort is the directory servers' TCP port.
	DirectoryPort = 5200
	// AudioPort is the UDP port for RTP audio and text packets.
	AudioPort = 5198
	// ControlPort is the UDP port for RTCP.
	ControlPort = 5199

	DefaultRefresh        = 5 * time.Minute
	DefaultRetry          = 30 * time.Second
	DefaultListMaxAge     = 5 * time.Minute
	DefaultRequestTimeout = 15 * time.Second
	DefaultKeepalive      = 10 * time.Second
	DefaultIdleTimeout    = 50 * time.Second
	DefaultConnectTimeout = 30 * time.Second
	DefaultTalkHang       = 400 * time.Millisecond
	defaultTick           = 100 * time.Millisecond
	defaultRecvBuffer     = 250 // frames, five seconds
	defaultEventBuffer    = 64
)

// DefaultServers are the public EchoLink directory servers, tried in order.
var DefaultServers = []string{
	"servers.echolink.org",
	"backup.echolink.org",
	"naeast.echolink.org",
	"nawest.echolink.org",
}

// Config configures the EchoLink client.
type Config struct {
	Callsign string // EchoLink callsign, with or without -L or -R
	Password string // EchoLink password; never logged or put in errors
	Name     string // operator name shown to peers in SDES; optional
	Location string // directory description, e.g. a city
	// Info is the station info text sent to a station after connecting,
	// with "\n" line breaks; optional.
	Info string
	// Servers lists directory servers as host or host:port; default
	// DefaultServers on port DirectoryPort.
	Servers []string

	// Refresh is how often the logon is renewed; default DefaultRefresh.
	Refresh time.Duration
	// Retry is the delay after a failed renewal; default DefaultRetry.
	Retry time.Duration
	// ListMaxAge is how long a fetched station list is reused; default
	// DefaultListMaxAge.
	ListMaxAge time.Duration
	// RequestTimeout bounds each directory request; default
	// DefaultRequestTimeout.
	RequestTimeout time.Duration

	// Keepalive is the SDES interval on a connection; default
	// DefaultKeepalive.
	Keepalive time.Duration
	// IdleTimeout drops a connection after this long without any packet
	// from the peer; default DefaultIdleTimeout.
	IdleTimeout time.Duration
	// ConnectTimeout bounds waiting for the peer's first reply; default
	// DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// TalkHang is how long after the last audio packet the peer counts as
	// still talking; default DefaultTalkHang.
	TalkHang time.Duration

	// LocalAudioAddr and LocalControlAddr are the local UDP addresses bound
	// while connected; default ":5198" and ":5199". EchoLink peers send to
	// those ports, so only tests change them.
	LocalAudioAddr   string
	LocalControlAddr string
	// RemoteAudioPort and RemoteControlPort are the peer's UDP ports;
	// default AudioPort and ControlPort.
	RemoteAudioPort   int
	RemoteControlPort int

	// Dial opens directory connections; default an IPv4-only net.Dialer, so
	// the directory sees the same address our UDP traffic comes from.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Clock drives timers; default the system clock.
	Clock Clock
	// Logger gets the info lines about transmissions sent to a station
	// and, at debug level, packet summaries per talk spurt (never audio).
	// Default: discard.
	Logger *slog.Logger

	tick time.Duration // timer resolution of a connection; tests shorten it
}

// withDefaults returns a copy of c with defaults filled in.
func (c Config) withDefaults() (Config, error) {
	c.Callsign = strings.ToUpper(strings.TrimSpace(c.Callsign))
	if c.Callsign == "" {
		return c, errors.New("echolink: callsign is required")
	}
	if strings.ContainsAny(c.Callsign, " \t\r\n>") {
		return c, errors.New("echolink: callsign contains invalid characters")
	}
	if c.Password == "" {
		return c, errors.New("echolink: password is required")
	}
	if len(c.Servers) == 0 {
		c.Servers = DefaultServers
	}
	servers := make([]string, 0, len(c.Servers))
	for _, s := range c.Servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, "5200")
		}
		servers = append(servers, s)
	}
	if len(servers) == 0 {
		return c, errors.New("echolink: no directory servers")
	}
	c.Servers = servers
	setDur := func(d *time.Duration, def time.Duration) {
		if *d <= 0 {
			*d = def
		}
	}
	setDur(&c.Refresh, DefaultRefresh)
	setDur(&c.Retry, DefaultRetry)
	setDur(&c.ListMaxAge, DefaultListMaxAge)
	setDur(&c.RequestTimeout, DefaultRequestTimeout)
	setDur(&c.Keepalive, DefaultKeepalive)
	setDur(&c.IdleTimeout, DefaultIdleTimeout)
	setDur(&c.ConnectTimeout, DefaultConnectTimeout)
	setDur(&c.TalkHang, DefaultTalkHang)
	setDur(&c.tick, defaultTick)
	if c.LocalAudioAddr == "" {
		c.LocalAudioAddr = ":5198"
	}
	if c.LocalControlAddr == "" {
		c.LocalControlAddr = ":5199"
	}
	if c.RemoteAudioPort <= 0 {
		c.RemoteAudioPort = AudioPort
	}
	if c.RemoteControlPort <= 0 {
		c.RemoteControlPort = ControlPort
	}
	if c.Dial == nil {
		d := &net.Dialer{}
		c.Dial = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		}
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return c, nil
}
