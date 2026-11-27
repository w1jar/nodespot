// Package system gathers host metrics and the node daemon's status for the
// System strip. Paths are injectable for tests.
package system

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Node is the node daemon (nodespot-node) part of System.
type Node struct {
	// Running: the control socket answered.
	Running bool `json:"running"`
	// Mode is the active mode ("" when idle).
	Mode string `json:"mode"`
	// Uptime is how long the daemon has run, in seconds (nil when not
	// running).
	Uptime *int64  `json:"uptime"`
	Error  *string `json:"error"`
}

// NodeInfo is what the collector learns from the node: when it started and
// its active mode.
type NodeInfo struct {
	Started time.Time
	Mode    string
}

// System is the API object of GET /api/system.
type System struct {
	Hostname     string     `json:"hostname"`
	CPUTemp      *float64   `json:"cpuTemp"`
	Load         [3]float64 `json:"load"`
	MemTotal     uint64     `json:"memTotal"`
	MemAvailable uint64     `json:"memAvailable"`
	Uptime       int64      `json:"uptime"`
	Node         Node       `json:"node"`
	Version      string     `json:"version"`
}

// Collector reads system information.
type Collector struct {
	Version  string
	ThermalP string // default /sys/class/thermal/thermal_zone0/temp
	LoadP    string // default /proc/loadavg
	MemP     string // default /proc/meminfo
	UptimeP  string // default /proc/uptime
	Hostname func() (string, error)
	// Node asks the node daemon (nil: no node).
	Node func(ctx context.Context) (NodeInfo, error)
	Now  func() time.Time
}

// New returns a Collector with the real /proc and /sys paths.
func New(version string, node func(ctx context.Context) (NodeInfo, error)) *Collector {
	return &Collector{
		Version:  version,
		ThermalP: "/sys/class/thermal/thermal_zone0/temp",
		LoadP:    "/proc/loadavg",
		MemP:     "/proc/meminfo",
		UptimeP:  "/proc/uptime",
		Hostname: os.Hostname,
		Node:     node,
		Now:      time.Now,
	}
}

// Collect gathers a full System snapshot. Missing files leave zero/null values.
func (c *Collector) Collect(ctx context.Context) System {
	s := System{Version: c.Version}
	if c.Hostname != nil {
		s.Hostname, _ = c.Hostname()
	}
	s.CPUTemp = ReadCPUTemp(c.ThermalP)
	s.Load, _ = ReadLoad(c.LoadP)
	s.MemTotal, s.MemAvailable, _ = ReadMem(c.MemP)
	s.Uptime, _ = ReadUptime(c.UptimeP)
	s.Node = c.node(ctx)
	return s
}

// ReadCPUTemp parses millidegrees C; nil if unavailable.
func ReadCPUTemp(path string) *float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return nil
	}
	v := n / 1000
	if v < -50 || v > 200 {
		return nil
	}
	v = float64(int(v*10+0.5)) / 10
	return &v
}

// ReadLoad parses /proc/loadavg.
func ReadLoad(path string) ([3]float64, error) {
	var out [3]float64
	b, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return out, errors.New("system: short loadavg")
	}
	for i := 0; i < 3; i++ {
		out[i], err = strconv.ParseFloat(f[i], 64)
		if err != nil {
			return [3]float64{}, err
		}
	}
	return out, nil
}

// ReadMem parses MemTotal and MemAvailable (kB in the file) into bytes.
func ReadMem(path string) (total, avail uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var gotT, gotA bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) == 0 {
			continue
		}
		n, perr := strconv.ParseUint(fs[0], 10, 64)
		if perr != nil {
			continue
		}
		switch k {
		case "MemTotal":
			total, gotT = n*1024, true
		case "MemAvailable":
			avail, gotA = n*1024, true
		}
	}
	if !gotT || !gotA {
		return total, avail, errors.New("system: meminfo incomplete")
	}
	return total, avail, sc.Err()
}

// ReadUptime parses whole seconds from /proc/uptime.
func ReadUptime(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, errors.New("system: empty uptime")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return int64(v), err
}

func (c *Collector) node(ctx context.Context) Node {
	if c.Node == nil {
		e := "the node is not configured"
		return Node{Error: &e}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	in, err := c.Node(cctx)
	if err != nil {
		e := err.Error()
		return Node{Error: &e}
	}
	n := Node{Running: true, Mode: in.Mode}
	if !in.Started.IsZero() {
		now := time.Now
		if c.Now != nil {
			now = c.Now
		}
		v := int64(now().Sub(in.Started) / time.Second)
		if v < 0 {
			v = 0
		}
		n.Uptime = &v
	}
	return n
}
