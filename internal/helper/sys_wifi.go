package helper

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// WiFi manager of the System tab (docs/PLAN-1.3.0.md section 9), through
// NetworkManager's nmcli. Every command is an argv vector. The WPA key never
// appears in one: a network is written as a NetworkManager keyfile (root,
// mode 0600) and loaded with "nmcli connection load".
const (
	OpWifiStatus   = "wifi-status"      // read-only
	OpWifiScan     = "wifi-scan"        // read-only (asks the radio for a fresh scan)
	OpWifiList     = "wifi-list"        // read-only: saved networks
	OpWifiConnect  = "wifi-connect"     // SysRequest.Wifi: SSID, Password, Hidden
	OpWifiForget   = "wifi-forget"      // SysRequest.Wifi: ID
	OpWifiPriority = "wifi-priority"    // SysRequest.Wifi: Order
	OpWifiHotspot  = "wifi-hotspot-set" // SysRequest.Wifi: Enabled, DelaySeconds
)

// WifiRequest is the typed argument of the wifi-* ops (SysRequest.Wifi).
// Every op reads only the fields it documents.
type WifiRequest struct {
	SSID         string   `json:"ssid,omitempty"`
	Password     string   `json:"password,omitempty"` // write-only, never returned or logged
	Hidden       bool     `json:"hidden,omitempty"`
	ID           string   `json:"id,omitempty"`    // connection UUID, from wifi-list
	Order        []string `json:"order,omitempty"` // connection UUIDs, first = highest priority
	Enabled      *bool    `json:"enabled,omitempty"`
	DelaySeconds int      `json:"delaySeconds,omitempty"`
}

func init() {
	registerSysOp(OpWifiStatus, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) { return o.WifiStatus(ctx), nil })
	registerSysOp(OpWifiScan, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) { return o.WifiScan(ctx) })
	registerSysOp(OpWifiList, func(o *Ops, ctx context.Context, _ *SysRequest) (any, error) { return o.WifiList(ctx) })
	registerSysOp(OpWifiConnect, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		w := wifiArg(r)
		return o.WifiConnect(ctx, w.SSID, w.Password, w.Hidden)
	})
	registerSysOp(OpWifiForget, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		return o.WifiForget(ctx, wifiArg(r).ID)
	})
	registerSysOp(OpWifiPriority, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		return o.WifiPriority(ctx, wifiArg(r).Order)
	})
	registerSysOp(OpWifiHotspot, func(o *Ops, ctx context.Context, r *SysRequest) (any, error) {
		w := wifiArg(r)
		if w.Enabled == nil {
			return nil, errors.New("invalid hotspot setting")
		}
		return o.WifiHotspotSet(ctx, *w.Enabled, w.DelaySeconds)
	})
}

func wifiArg(r *SysRequest) WifiRequest {
	if r.Wifi == nil {
		return WifiRequest{}
	}
	return *r.Wifi
}

// WifiPaths are the files of the WiFi manager; the zero value uses the real
// locations (tests inject temporary directories).
type WifiPaths struct {
	NMDir    string // NetworkManager keyfiles (default /etc/NetworkManager/system-connections)
	StateDir string // hotspot.json, root only (default /var/lib/nodespot-wifi)
	RunDir   string // watchdog state and lock (default /run/nodespot)
	Uptime   string // default /proc/uptime
	Hostname func() string
}

// Defaults of WifiPaths.
const (
	DefaultNMDir        = "/etc/NetworkManager/system-connections"
	DefaultWifiStateDir = "/var/lib/nodespot-wifi"
	DefaultWifiRunDir   = "/run/nodespot"
)

func (o *Ops) nmDir() string        { return orDefault(o.Wifi.NMDir, DefaultNMDir) }
func (o *Ops) wifiStateDir() string { return orDefault(o.Wifi.StateDir, DefaultWifiStateDir) }
func (o *Ops) wifiRunDir() string   { return orDefault(o.Wifi.RunDir, DefaultWifiRunDir) }

// ---------------------------------------------------------------- validation

// ValidSSID checks an SSID: 1 to 32 bytes of UTF-8 without control
// characters.
func ValidSSID(s string) error {
	if len(s) < 1 || len(s) > 32 {
		return errors.New("invalid SSID: 1 to 32 bytes")
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid SSID: not valid UTF-8")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("invalid SSID: control characters are not allowed")
		}
	}
	return nil
}

var hex64RE = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)

// ValidWifiPassword checks a WPA passphrase (8 to 63 printable ASCII
// characters) or a raw key (64 hex digits). An empty password is valid and
// means an open network. The error never contains the value.
func ValidWifiPassword(s string) error {
	if s == "" {
		return nil
	}
	if len(s) == 64 {
		if hex64RE.MatchString(s) {
			return nil
		}
		return errors.New("invalid password: 64 characters must be hexadecimal digits")
	}
	if len(s) < 8 || len(s) > 63 {
		return errors.New("invalid password: 8 to 63 characters (or 64 hexadecimal digits)")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return errors.New("invalid password: printable ASCII characters only")
		}
	}
	return nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidConnID reports whether s has the shape of a connection UUID. It is
// only the syntax check: the op still requires the id to be one of the WiFi
// connections it lists itself.
func ValidConnID(s string) bool { return uuidRE.MatchString(s) }

// ------------------------------------------------------------- nmcli parsing

// splitTerse splits one line of "nmcli -t" output at its unescaped colons
// and removes the backslash escapes ("\:" and "\\").
func splitTerse(line string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == ':':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

// terseRows splits the output into rows of exactly n fields; other lines are
// dropped.
func terseRows(text string, n int) [][]string {
	var rows [][]string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		if f := splitTerse(l); len(f) == n {
			rows = append(rows, f)
		}
	}
	return rows
}

// parseTerseProps parses the "property:value" lines of
// "nmcli -t -f <props> connection show <id>".
func parseTerseProps(text string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		i := strings.IndexByte(l, ':')
		if i <= 0 {
			continue
		}
		v := splitTerse(l[i+1:])
		m[l[:i]] = strings.Join(v, ":")
	}
	return m
}

// cleanText drops control characters from text that came from the network
// (SSIDs of other people's access points, connection names).
func wifiText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

// nmDev is one row of "nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status".
type nmDev struct{ Name, Type, State, Conn string }

func parseNMDevices(text string) []nmDev {
	var out []nmDev
	for _, f := range terseRows(text, 4) {
		out = append(out, nmDev{f[0], f[1], f[2], wifiText(f[3])})
	}
	return out
}

// wifiConn is a saved WiFi client connection (the hotspot excluded).
type wifiConn struct {
	ID       string // UUID
	Name     string // connection id (the name nmcli shows)
	SSID     string
	Priority int
	Auto     bool
	Active   bool
	Hidden   bool
	KeyMgmt  string
}

// connRow is one row of "nmcli -t -f NAME,UUID,TYPE,AUTOCONNECT,AUTOCONNECT-PRIORITY,ACTIVE connection show".
type connRow struct {
	Name, UUID, Type string
	Auto             bool
	Priority         int
	Active           bool
}

func parseConnRows(text string) []connRow {
	var out []connRow
	for _, f := range terseRows(text, 6) {
		p, _ := strconv.Atoi(strings.TrimSpace(f[4]))
		out = append(out, connRow{Name: wifiText(f[0]), UUID: f[1], Type: f[2], Auto: f[3] == "yes", Priority: p, Active: f[5] == "yes"})
	}
	return out
}

func isWifiType(t string) bool { return t == "802-11-wireless" || t == "wifi" }

// WifiAP is one access point of a scan.
type WifiAP struct {
	SSID      string `json:"ssid"`
	Signal    int    `json:"signal"` // 0..100
	Channel   int    `json:"channel"`
	Security  string `json:"security"` // nmcli's text, "" for open
	Secured   bool   `json:"secured"`
	Supported bool   `json:"supported"` // false: enterprise (802.1X), not handled here
	InUse     bool   `json:"inUse"`
	Saved     bool   `json:"saved"`
}

// parseWifiScan reads "nmcli -t -f IN-USE,SSID,SIGNAL,CHAN,SECURITY device wifi
// list". Hidden networks (empty SSID) are skipped; an SSID seen several times
// (one per access point) keeps its strongest entry, and in-use wins.
func parseWifiScan(text string) []WifiAP {
	best := map[string]WifiAP{}
	var order []string
	for _, f := range terseRows(text, 5) {
		ssid := wifiText(f[1])
		if ssid == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[2])
		ch, _ := strconv.Atoi(f[3])
		sec := strings.TrimSpace(f[4])
		if sec == "--" {
			sec = ""
		}
		ap := WifiAP{SSID: ssid, Signal: sig, Channel: ch, Security: sec, Secured: sec != "", Supported: !strings.Contains(sec, "802.1X"), InUse: f[0] == "*"}
		cur, seen := best[ssid]
		if !seen {
			order = append(order, ssid)
			best[ssid] = ap
			continue
		}
		if ap.InUse || (!cur.InUse && ap.Signal > cur.Signal) {
			ap.InUse = ap.InUse || cur.InUse
			best[ssid] = ap
		} else if ap.InUse != cur.InUse {
			cur.InUse = true
			best[ssid] = cur
		}
	}
	out := make([]WifiAP, 0, len(order))
	for _, s := range order {
		out = append(out, best[s])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Signal > out[j].Signal })
	return out
}

// keyMgmtFor picks the keyfile key-mgmt for a scanned security string:
// "sae" only for WPA3-only networks, "wpa-psk" otherwise (WPA2, mixed mode).
func keyMgmtFor(security string) string {
	if strings.Contains(security, "WPA3") && !strings.Contains(security, "WPA2") && !strings.Contains(security, "WPA1") {
		return "sae"
	}
	return "wpa-psk"
}

// countNeighbors counts the clients in the output of "ip neigh show dev X"
// (entries that have a link-layer address and are not failed).
func countNeighbors(text string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, " lladdr ") && !strings.HasSuffix(strings.TrimSpace(l), "FAILED") {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------------ keyfile

// keyfileEscape is the escaping of NetworkManager keyfile string values:
// backslash, and a leading or trailing space (as "\s"). Control characters
// cannot occur (validated).
func keyfileEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	if strings.HasPrefix(s, " ") {
		s = `\s` + s[1:]
	}
	if strings.HasSuffix(s, " ") {
		s = s[:len(s)-1] + `\s`
	}
	return s
}

// ssidList writes an SSID as a keyfile byte list, which carries any byte
// without escaping questions.
func ssidList(ssid string) string {
	var b strings.Builder
	for i := 0; i < len(ssid); i++ {
		b.WriteString(strconv.Itoa(int(ssid[i])))
		b.WriteByte(';')
	}
	return b.String()
}

// buildWifiKeyfile renders the NetworkManager keyfile of a client network.
// psk empty: an open network.
func buildWifiKeyfile(name, uuid, ssid, psk, keyMgmt string, hidden bool, priority int) string {
	var b strings.Builder
	b.WriteString("[connection]\n")
	b.WriteString("id=" + keyfileEscape(name) + "\n")
	b.WriteString("uuid=" + uuid + "\n")
	b.WriteString("type=wifi\n")
	b.WriteString("autoconnect=true\n")
	b.WriteString("autoconnect-priority=" + strconv.Itoa(priority) + "\n\n")
	b.WriteString("[wifi]\nmode=infrastructure\n")
	b.WriteString("ssid=" + ssidList(ssid) + "\n")
	if hidden {
		b.WriteString("hidden=true\n")
	}
	if psk != "" {
		b.WriteString("\n[wifi-security]\nkey-mgmt=" + keyMgmt + "\n")
		b.WriteString("psk=" + keyfileEscape(psk) + "\n")
	}
	b.WriteString("\n[ipv4]\nmethod=auto\n\n[ipv6]\naddr-gen-mode=default\nmethod=auto\n")
	return b.String()
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// ---------------------------------------------------------------------- ops

// WifiDevice is a network device of the WiFi status.
type WifiDevice struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"` // wifi, ethernet
	State      string   `json:"state"`
	Connection string   `json:"connection"`
	Addrs      []string `json:"addrs"` // IPv4 addresses of a WiFi device
}

// WifiStatus is the response of wifi-status. It never fails: a missing or
// stopped NetworkManager is Available == false with a Reason.
type WifiStatus struct {
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	HasWifi   bool         `json:"hasWifi"`
	Radio     string       `json:"radio"` // enabled, disabled or ""
	Devices   []WifiDevice `json:"devices"`
	Active    string       `json:"active"`   // connection of the WiFi device ("" when none)
	Ethernet  bool         `json:"ethernet"` // an Ethernet device is connected
	Hotspot   WifiHotspot  `json:"hotspot"`
}

// WifiNetwork is a saved network of wifi-list.
type WifiNetwork struct {
	ID          string `json:"id"` // UUID: the handle for forget and priority
	Name        string `json:"name"`
	SSID        string `json:"ssid"`
	Priority    int    `json:"priority"`
	Autoconnect bool   `json:"autoconnect"`
	Active      bool   `json:"active"`
	Hidden      bool   `json:"hidden"`
	Secured     bool   `json:"secured"`
}

// WifiList is the response of wifi-list and wifi-scan.
type WifiList struct {
	Networks []WifiNetwork `json:"networks"`
}

// WifiScanResult is the response of wifi-scan.
type WifiScanResult struct {
	Networks []WifiAP `json:"networks"`
	Note     string   `json:"note,omitempty"`
}

// WifiConnectResult is the response of wifi-connect.
type WifiConnectResult struct {
	ID      string `json:"id"`
	SSID    string `json:"ssid"`
	Updated bool   `json:"updated"` // an existing saved network was replaced
}

var errNoNM = errors.New("NetworkManager is not available on this node")

// nmRunning says whether nmcli exists and NetworkManager answers.
func (o *Ops) nmRunning(ctx context.Context) (bool, string) {
	out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", "RUNNING", "general")
	if err != nil {
		return false, "nmcli is not installed"
	}
	if code != 0 || strings.TrimSpace(out) != "running" {
		return false, "NetworkManager is not running"
	}
	return true, ""
}

var ifnameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// WifiStatus reads the NetworkManager state.
func (o *Ops) WifiStatus(ctx context.Context) WifiStatus {
	st := WifiStatus{Devices: []WifiDevice{}, Hotspot: o.hotspotInfo()}
	ok, why := o.nmRunning(ctx)
	if !ok {
		st.Reason = why
		return st
	}
	st.Available = true
	if out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status"); err == nil && code == 0 {
		for _, d := range parseNMDevices(out) {
			switch d.Type {
			case "wifi":
				st.HasWifi = true
				wd := WifiDevice{Name: d.Name, Type: "wifi", State: d.State, Connection: d.Conn, Addrs: []string{}}
				if ifnameRE.MatchString(d.Name) {
					if out, code, err := o.Run.Run(ctx, "ip", "-j", "addr", "show", "dev", d.Name); err == nil && code == 0 {
						if ifs, err := parseIPAddr(out); err == nil {
							for _, i := range ifs {
								for _, a := range i.Addrs {
									if a.Family == "inet" {
										wd.Addrs = append(wd.Addrs, a.Addr)
									}
								}
							}
						}
					}
				}
				st.Devices = append(st.Devices, wd)
				if d.State == "connected" {
					if d.Conn == hotspotConnName {
						st.Hotspot.Active = true
					} else {
						st.Active = d.Conn
					}
				}
			case "ethernet":
				if d.State == "connected" {
					st.Ethernet = true
				}
				st.Devices = append(st.Devices, WifiDevice{Name: d.Name, Type: "ethernet", State: d.State, Connection: d.Conn, Addrs: []string{}})
			}
		}
	}
	if !st.HasWifi {
		st.Reason = "no WiFi device found"
		return st
	}
	if out, code, err := o.Run.Run(ctx, "nmcli", "radio", "wifi"); err == nil && code == 0 {
		st.Radio = strings.TrimSpace(out)
	}
	return st
}

// wifiGate returns an error unless NetworkManager runs and a WiFi device
// exists. It returns the first WiFi device name.
func (o *Ops) wifiGate(ctx context.Context) (string, error) {
	if ok, _ := o.nmRunning(ctx); !ok {
		return "", errNoNM
	}
	out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
	if err != nil || code != 0 {
		return "", errNoNM
	}
	for _, d := range parseNMDevices(out) {
		if d.Type == "wifi" && ifnameRE.MatchString(d.Name) {
			return d.Name, nil
		}
	}
	return "", errors.New("no WiFi device found")
}

const maxWifiConns = 50

// wifiConns lists the saved WiFi client connections with their properties.
// Access point profiles (ours and others') are left out.
func (o *Ops) wifiConns(ctx context.Context) ([]wifiConn, error) {
	out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", "NAME,UUID,TYPE,AUTOCONNECT,AUTOCONNECT-PRIORITY,ACTIVE", "connection", "show")
	if err != nil || code != 0 {
		return nil, errNoNM
	}
	var res []wifiConn
	for _, r := range parseConnRows(out) {
		if !isWifiType(r.Type) || r.Name == hotspotConnName || !ValidConnID(r.UUID) {
			continue
		}
		if len(res) >= maxWifiConns {
			break
		}
		c := wifiConn{ID: r.UUID, Name: r.Name, Priority: r.Priority, Auto: r.Auto, Active: r.Active}
		pout, pcode, perr := o.Run.Run(ctx, "nmcli", "-t", "-f",
			"802-11-wireless.ssid,802-11-wireless.hidden,802-11-wireless.mode,802-11-wireless-security.key-mgmt",
			"connection", "show", "uuid", r.UUID)
		if perr == nil && pcode == 0 {
			p := parseTerseProps(pout)
			if p["802-11-wireless.mode"] == "ap" {
				continue
			}
			c.SSID = wifiText(p["802-11-wireless.ssid"])
			c.Hidden = p["802-11-wireless.hidden"] == "yes"
			c.KeyMgmt = p["802-11-wireless-security.key-mgmt"]
		}
		if c.SSID == "" {
			c.SSID = c.Name
		}
		res = append(res, c)
	}
	return res, nil
}

func (c wifiConn) secured() bool {
	switch c.KeyMgmt {
	case "", "--", "none":
		return false
	}
	return true
}

// WifiList returns the saved networks, highest priority first.
func (o *Ops) WifiList(ctx context.Context) (WifiList, error) {
	if ok, _ := o.nmRunning(ctx); !ok {
		return WifiList{Networks: []WifiNetwork{}}, errNoNM
	}
	cs, err := o.wifiConns(ctx)
	if err != nil {
		return WifiList{Networks: []WifiNetwork{}}, err
	}
	res := WifiList{Networks: []WifiNetwork{}}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Priority > cs[j].Priority })
	for _, c := range cs {
		res.Networks = append(res.Networks, WifiNetwork{ID: c.ID, Name: c.Name, SSID: c.SSID, Priority: c.Priority,
			Autoconnect: c.Auto, Active: c.Active, Hidden: c.Hidden, Secured: c.secured()})
	}
	return res, nil
}

const wifiScanFields = "IN-USE,SSID,SIGNAL,CHAN,SECURITY"

// WifiScan asks for a fresh scan and lists the access points in range.
func (o *Ops) WifiScan(ctx context.Context) (WifiScanResult, error) {
	res := WifiScanResult{Networks: []WifiAP{}}
	if _, err := o.wifiGate(ctx); err != nil {
		return res, err
	}
	out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", wifiScanFields, "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return res, errNoNM
	}
	if code != 0 {
		// A rescan too soon after the last one is refused: show the cached list.
		out, code, err = o.Run.Run(ctx, "nmcli", "-t", "-f", wifiScanFields, "device", "wifi", "list", "--rescan", "no")
		if err != nil || code != 0 {
			return res, errors.New("scan failed: " + LastLines(out, 2))
		}
		res.Note = "showing the previous scan"
	}
	res.Networks = parseWifiScan(out)
	if saved, err := o.wifiConns(ctx); err == nil {
		have := map[string]bool{}
		for _, c := range saved {
			have[c.SSID] = true
		}
		for i := range res.Networks {
			res.Networks[i].Saved = have[res.Networks[i].SSID]
		}
	}
	if o.hotspotInfo().Enabled && len(res.Networks) == 0 && o.WifiStatus(ctx).Hotspot.Active {
		res.Note = "the setup hotspot is on, so the radio cannot scan"
	}
	return res, nil
}

// redact removes secret from text before it is shown or logged.
func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "***")
}

// connectError turns an nmcli activation failure into a message for the
// operator. The key never reaches it.
func connectError(out, psk string) error {
	out = redact(out, psk)
	l := strings.ToLower(out)
	switch {
	case strings.Contains(l, "secrets were required"), strings.Contains(l, "4-way"), strings.Contains(l, "(7)"):
		return errors.New("could not connect: the password may be wrong")
	case strings.Contains(l, "no network with ssid"), strings.Contains(l, "(53)"):
		return errors.New("could not connect: the network was not found")
	}
	return errors.New("could not connect: " + wifiText(LastLines(out, 2)))
}

// WifiConnect saves a network (replacing a saved network with the same SSID,
// keeping its priority) and connects to it now. When the connection fails the
// new profile is removed again and the old one stays.
func (o *Ops) WifiConnect(ctx context.Context, ssid, psk string, hidden bool) (WifiConnectResult, error) {
	if err := ValidSSID(ssid); err != nil {
		return WifiConnectResult{}, err
	}
	if err := ValidWifiPassword(psk); err != nil {
		return WifiConnectResult{}, err
	}
	unlock, err := o.wifiLock(ctx, true)
	if err != nil {
		return WifiConnectResult{}, err
	}
	defer unlock()
	if _, err := o.wifiGate(ctx); err != nil {
		return WifiConnectResult{}, err
	}
	conns, err := o.wifiConns(ctx)
	if err != nil {
		return WifiConnectResult{}, err
	}
	var same []wifiConn
	prio, lowest := 100, 1<<30
	for _, c := range conns {
		if c.SSID == ssid {
			same = append(same, c)
		}
		if c.Priority < lowest {
			lowest = c.Priority
		}
	}
	switch {
	case len(same) > 0:
		prio = same[0].Priority
	case len(conns) > 0:
		prio = lowest - 10
		if prio < -999 {
			prio = -999
		}
	}
	km := "wpa-psk"
	if psk != "" {
		// A WPA3-only network needs "sae"; the cached scan says which it is.
		if out, code, err := o.Run.Run(ctx, "nmcli", "-t", "-f", wifiScanFields, "device", "wifi", "list", "--rescan", "no"); err == nil && code == 0 {
			for _, ap := range parseWifiScan(out) {
				if ap.SSID == ssid && ap.Secured {
					km = keyMgmtFor(ap.Security)
					break
				}
			}
		}
		if km == "sae" && len(psk) == 64 {
			return WifiConnectResult{}, errors.New("a WPA3 network needs its passphrase, not a 64-digit key")
		}
	}
	uuid, err := newUUID()
	if err != nil {
		return WifiConnectResult{}, err
	}
	dir, err := filepath.EvalSymlinks(o.nmDir())
	if err != nil {
		return WifiConnectResult{}, errors.New("NetworkManager's connection directory is not available")
	}
	path := filepath.Join(dir, "nodespot-"+uuid+".nmconnection")
	data := buildWifiKeyfile(ssid, uuid, ssid, psk, km, hidden, prio)
	if err := atomicWrite(path, dir, []byte(data), 0, 0, 0o600); err != nil {
		return WifiConnectResult{}, errors.New("could not write the connection file")
	}
	remove := func() {
		_, _, _ = o.Run.Run(context.WithoutCancel(ctx), "nmcli", "connection", "delete", "uuid", uuid)
		_ = os.Remove(path)
	}
	if out, code, err := o.Run.Run(ctx, "nmcli", "connection", "load", path); err != nil || code != 0 {
		_ = os.Remove(path)
		return WifiConnectResult{}, errors.New("NetworkManager could not load the connection: " + redact(wifiText(LastLines(out, 2)), psk))
	}
	if out, code, err := o.Run.Run(ctx, "nmcli", "--wait", "30", "connection", "up", "uuid", uuid); err != nil || code != 0 {
		remove()
		if err != nil {
			return WifiConnectResult{}, errNoNM
		}
		return WifiConnectResult{}, connectError(out, psk)
	}
	for _, c := range same {
		_, _, _ = o.Run.Run(ctx, "nmcli", "connection", "delete", "uuid", c.ID)
	}
	return WifiConnectResult{ID: uuid, SSID: ssid, Updated: len(same) > 0}, nil
}

// WifiForget deletes a saved WiFi network. id must be one of the networks
// wifi-list shows.
func (o *Ops) WifiForget(ctx context.Context, id string) (OKResult, error) {
	if !ValidConnID(id) {
		return OKResult{}, errors.New("invalid connection id")
	}
	unlock, err := o.wifiLock(ctx, true)
	if err != nil {
		return OKResult{}, err
	}
	defer unlock()
	conns, err := o.wifiConns(ctx)
	if err != nil {
		return OKResult{}, err
	}
	for _, c := range conns {
		if c.ID == id {
			if out, code, err := o.Run.Run(ctx, "nmcli", "connection", "delete", "uuid", id); err != nil || code != 0 {
				return OKResult{}, errors.New("could not remove the network: " + wifiText(LastLines(out, 2)))
			}
			return OKResult{OK: true}, nil
		}
	}
	return OKResult{}, errors.New("unknown network")
}

// WifiPriority writes descending autoconnect priorities (100, 90, 80 ...) in
// the given order. order must name every saved WiFi network exactly once.
func (o *Ops) WifiPriority(ctx context.Context, order []string) (WifiList, error) {
	if len(order) == 0 || len(order) > maxWifiConns {
		return WifiList{}, errors.New("invalid order")
	}
	seen := map[string]bool{}
	for _, id := range order {
		if !ValidConnID(id) || seen[id] {
			return WifiList{}, errors.New("invalid order")
		}
		seen[id] = true
	}
	unlock, err := o.wifiLock(ctx, true)
	if err != nil {
		return WifiList{}, err
	}
	defer unlock()
	conns, err := o.wifiConns(ctx)
	if err != nil {
		return WifiList{}, err
	}
	if len(conns) != len(order) {
		return WifiList{}, errors.New("the list of networks changed: reload and try again")
	}
	for _, c := range conns {
		if !seen[c.ID] {
			return WifiList{}, errors.New("the list of networks changed: reload and try again")
		}
	}
	for i, id := range order {
		if out, code, err := o.Run.Run(ctx, "nmcli", "connection", "modify", "uuid", id, "connection.autoconnect-priority", strconv.Itoa(100-10*i)); err != nil || code != 0 {
			return WifiList{}, errors.New("could not set the priority: " + wifiText(LastLines(out, 2)))
		}
	}
	return o.WifiList(ctx)
}
