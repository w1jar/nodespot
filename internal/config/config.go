// Package config loads /etc/nodespot/nodespot.conf (an INI file: [section],
// key = value, comments after ";" or "#").
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default paths.
const (
	DefaultConfigPath = "/etc/nodespot/nodespot.conf"
	// DefaultNodeSocket is the node daemon's control socket.
	DefaultNodeSocket = "/run/nodespot-node/control.sock"
	// DefaultNodeConfig is the node daemon's config file (backed up with
	// the panel's data).
	DefaultNodeConfig = "/etc/nodespot/node.json"
)

// Config is the effective server configuration.
type Config struct {
	Listen  string
	DataDir string

	// NodeSocket is the node daemon's control socket ([node] socket);
	// NodeConfig its config file ([node] config).
	NodeSocket string
	NodeConfig string

	NodelistURL     string
	NodelistRefresh time.Duration

	// HelperSocket is the nodespot-helper unix socket ([helper] socket).
	HelperSocket string
	// UpdatesURL is the base URL of the public apt repository, which also
	// serves latest.json with release notes ([updates] url). Empty disables
	// release notes.
	UpdatesURL string

	// FavStatsURL is the base URL of the stats API used by the favorites
	// activity scan ([favstats] url); FavStatsRate its requests per minute
	// ([favstats] rate, 1..25); FavStatsLog logs one line per stats request
	// ([favstats] log, default false).
	FavStatsURL  string
	FavStatsRate int
	FavStatsLog  bool
}

// Defaults returns the built-in defaults.
func Defaults() Config {
	return Config{
		Listen:          ":8080",
		DataDir:         "/var/lib/nodespot",
		NodeSocket:      DefaultNodeSocket,
		NodeConfig:      DefaultNodeConfig,
		NodelistURL:     "https://allmondb.allstarlink.org/",
		NodelistRefresh: 24 * time.Hour,
		HelperSocket:    "/run/nodespot/helper.sock",
		UpdatesURL:      "https://w1jar.github.io/nodespot",
		FavStatsURL:     "https://stats.allstarlink.org",
		FavStatsRate:    20,
	}
}

// ListenPort returns the TCP port of a listen address (":8080" or
// "addr:port"), validated to 1..65535.
func ListenPort(listen string) (int, error) {
	_, ps, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("config: invalid listen address %q", listen)
	}
	p, err := strconv.Atoi(ps)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("config: invalid listen port %q (1..65535)", ps)
	}
	return p, nil
}

// Load reads path (a missing file yields the defaults). Sections and keys
// it does not know (such as the [ami] section of a 2.x install) are
// ignored.
func Load(path string) (Config, error) {
	c := Defaults()
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	f, err := parseINI(data)
	if err != nil {
		return c, fmt.Errorf("config: %s: %w", path, err)
	}
	get := f.get
	if v, ok := get("server", "listen"); ok && v != "" {
		c.Listen = v
	}
	if v, ok := get("server", "datadir"); ok && v != "" {
		c.DataDir = v
	}
	if v, ok := get("node", "socket"); ok && v != "" {
		c.NodeSocket = v
	}
	if v, ok := get("node", "config"); ok && v != "" {
		c.NodeConfig = v
	}
	if v, ok := get("nodelist", "url"); ok {
		c.NodelistURL = v
	}
	if v, ok := get("nodelist", "refresh"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			return c, fmt.Errorf("config: invalid nodelist refresh %q", v)
		}
		c.NodelistRefresh = d
	}
	if v, ok := get("helper", "socket"); ok && v != "" {
		c.HelperSocket = v
	}
	if v, ok := get("updates", "url"); ok {
		c.UpdatesURL = strings.TrimRight(v, "/")
	}
	if v, ok := get("favstats", "url"); ok && v != "" {
		c.FavStatsURL = strings.TrimRight(v, "/")
	}
	if v, ok := get("favstats", "rate"); ok && v != "" {
		r, err := strconv.Atoi(v)
		if err != nil || r < 1 || r > 25 {
			return c, fmt.Errorf("config: invalid favstats rate %q (1..25)", v)
		}
		c.FavStatsRate = r
	}
	if v, ok := get("favstats", "log"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("config: invalid favstats log %q (true or false)", v)
		}
		c.FavStatsLog = b
	}
	return c, nil
}
