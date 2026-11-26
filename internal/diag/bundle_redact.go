package diag

import (
	"net"
	"regexp"
	"sort"
	"strings"
)

// Redaction rules of the diagnostics bundle (docs/PLAN-1.3.0.md section 12).
//
// Everything here is a pure function of its arguments, so the rules are
// covered by table tests that use placeholder data only. The bundle differs
// from the text report (Redact above): the report masks every address and
// callsign, while the bundle keeps addresses and callsigns unless the
// operator asks for them to be masked, and masks only the node's own.
//
// Two classes of rule:
//
//   - secrets: always applied. Values of secret-like keys (secret, password,
//     pwd, psk, token, ...) in config files and free text, registration
//     strings, private key blocks, bearer tokens, user:password@ in URLs,
//     session cookies, long random tokens, and every literal secret found in
//     the collected configs or passed in (so a password echoed in a journal
//     line is removed too).
//   - identity: applied when BundleOptions.MaskIdentity is set. The node's own
//     callsigns, its own public IP addresses and host names, and location
//     fields.

// Placeholders of the identity rules.
const (
	phCallsign = "<callsign>"
	phOwnIP    = "<own-ip>"
	phOwnHost  = "<own-host>"
	phMasked   = "<masked>"
	phPEM      = "<redacted private key>"
)

// MinLiteralSecret is the shortest secret that is also removed as a literal
// from text where it has no key in front of it. Shorter values (a default
// like "radio") would shred ordinary words; they are still removed wherever
// a secret-like key introduces them.
const MinLiteralSecret = 6

// Counts says how many values a function replaced.
type Counts struct{ Secrets, Identity int }

// Add returns the sum of c and d.
func (c Counts) Add(d Counts) Counts {
	return Counts{c.Secrets + d.Secrets, c.Identity + d.Identity}
}

// BundleOptions tunes RedactBundleText.
type BundleOptions struct {
	// MaskIdentity turns the identity rules on.
	MaskIdentity bool
	// Secrets are literal secrets to remove wherever they occur.
	Secrets []string
	// Identity is the node's own identity (used when MaskIdentity is set).
	Identity Identity
}

// Identity is what MaskIdentity removes from text.
type Identity struct {
	Callsigns []string // case-insensitive, matched as whole tokens ("N0CALL" also matches in "N0CALL-R")
	IPs       []string // the node's own public addresses
	Hosts     []string // the node's own public host names
}

// Kind selects the rules of RedactBundleText.
type Kind int

const (
	// KindText is free text: journals, reports, command output.
	KindText Kind = iota
	// KindConf is an Asterisk-style or INI config file, handled line by line.
	KindConf
)

const bundleSecretName = secretName + `|pass|pwd|[\w.\-]*pwd[\w.\-]*|[\w.\-]*psk|md5secret|wep[_\-]?key\d*|auth[_\-]?key`

var (
	reBundleKV = regexp.MustCompile(`(?i)\b(` + bundleSecretName + `)(["']?\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;]+)`)
	// Config line: optional comment prefix, key, "=" or "=>", value to the end of the line.
	reConfLine   = regexp.MustCompile(`^(\s*(?:[;#]+\s*)?)([A-Za-z0-9_.\-]+)(\s*=>?\s*)(.*)$`)
	reConfSecret = regexp.MustCompile(`(?i)^(?:` + bundleSecretName + `)$`)
	rePEM        = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	rePEMOpen    = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*`)
	// "register => user:secret[:authuser]@host[:port][/ext]" inside text.
	reRegisterText = regexp.MustCompile(`(?i)(\bregister\s*=>?\s*[^:@\s;]+:)([^@\s;]*)(@)`)
	reCallsignTok  = regexp.MustCompile(`^[A-Z0-9]{1,3}[0-9][A-Z0-9]{1,4}$`)
	reIDRecording  = regexp.MustCompile(`\|i([A-Za-z0-9/\-]{3,16})`)
)

// identityKeys are config keys whose value is masked when MaskIdentity is on.
var identityKeys = map[string]bool{
	"callsign": true, "call": true, "latitude": true, "longitude": true, "lat": true, "lon": true,
	"location": true, "qth": true, "email": true,
}

// externalKeys carry the node's public address or name in Asterisk configs.
var externalKeys = map[string]bool{
	"externip": true, "externaddr": true, "externhost": true,
	"external_media_address": true, "external_signaling_address": true,
}

func plainValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto", "none", "yes", "no", "true", "false", Redacted:
		return true
	}
	return strings.Contains(v, Redacted)
}

// splitRegister splits the value of a "register" line at its last "@" and
// returns (user, password, rest-from-@). ok is false when it has no password.
func splitRegister(v string) (head, pass, tail string, ok bool) {
	at := strings.LastIndexByte(v, '@')
	if at < 0 {
		return "", "", "", false
	}
	cred, tail := v[:at], v[at:]
	parts := strings.SplitN(cred, ":", 3)
	if len(parts) < 2 || parts[1] == "" {
		return "", "", "", false
	}
	head = parts[0] + ":"
	if len(parts) == 3 {
		tail = ":" + parts[2] + tail
	}
	return head, parts[1], tail, true
}

// RedactConfLine masks one config line: the value of a secret-like key
// (also on commented-out lines, where a retired password often survives),
// the password of a registration string, and with identity set the location
// and callsign keys. Other lines are returned unchanged.
func RedactConfLine(line string, identity bool) (string, Counts) {
	m := reConfLine.FindStringSubmatch(line)
	if m == nil {
		return line, Counts{}
	}
	prefix, key, op, val := m[1], m[2], m[3], m[4]
	lk := strings.ToLower(key)
	switch {
	case lk == "register":
		if head, _, tail, ok := splitRegister(strings.TrimSpace(val)); ok && !plainValue(strings.TrimSpace(val)) {
			if strings.Contains(strings.TrimSpace(val), ":"+Redacted+"@") || strings.Contains(strings.TrimSpace(val), ":"+Redacted+":") {
				return line, Counts{}
			}
			return prefix + key + op + head + Redacted + tail, Counts{Secrets: 1}
		}
	case reConfSecret.MatchString(key):
		if !plainValue(val) {
			return prefix + key + op + Redacted, Counts{Secrets: 1}
		}
	case identity && identityKeys[lk]:
		if !plainValue(val) && !strings.Contains(val, phMasked) {
			return prefix + key + op + phMasked, Counts{Identity: 1}
		}
	}
	return line, Counts{}
}

// RedactConfText applies RedactConfLine to every line and removes private key blocks.
func RedactConfText(s string, identity bool) (string, Counts) {
	var c Counts
	s, c.Secrets = redactPEM(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		cr := strings.HasSuffix(l, "\r")
		out, n := RedactConfLine(strings.TrimSuffix(l, "\r"), identity)
		if cr {
			out += "\r"
		}
		lines[i] = out
		c = c.Add(n)
	}
	return strings.Join(lines, "\n"), c
}

func redactPEM(s string) (string, int) {
	n := len(rePEM.FindAllStringIndex(s, -1))
	s = rePEM.ReplaceAllString(s, phPEM)
	if rePEMOpen.MatchString(s) { // truncated block: drop to the end
		s = rePEMOpen.ReplaceAllString(s, phPEM)
		n++
	}
	return s, n
}

// RedactSecretsText removes secrets from free text (the key rules of
// Redact without the address and callsign rules).
func RedactSecretsText(s string) (string, Counts) {
	n := 0
	count := func(re *regexp.Regexp, repl func(string) string) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			out := repl(m)
			if out != m {
				n++
			}
			return out
		})
	}
	var k int
	s, k = redactPEM(s)
	n += k
	count(reHeaderLine, func(m string) string {
		sub := reHeaderLine.FindStringSubmatch(m)
		if strings.Contains(m, Redacted) {
			return m
		}
		return sub[1] + sub[2] + Redacted
	})
	count(reRegisterText, func(m string) string {
		sub := reRegisterText.FindStringSubmatch(m)
		if sub[2] == "" || strings.Contains(sub[2], Redacted) {
			return m
		}
		return sub[1] + Redacted + sub[3]
	})
	count(reBundleKV, func(m string) string {
		sub := reBundleKV.FindStringSubmatch(m)
		if plainValue(sub[3]) {
			return m
		}
		return sub[1] + sub[2] + Redacted
	})
	count(reBearer, func(m string) string {
		sub := reBearer.FindStringSubmatch(m)
		if strings.Contains(m, Redacted) {
			return m
		}
		return sub[1] + " " + Redacted
	})
	count(reURLCreds, func(m string) string {
		if strings.Contains(m, Redacted) {
			return m
		}
		return reURLCreds.ReplaceAllString(m, "${1}"+Redacted+"@")
	})
	count(reSetup, func(m string) string {
		if strings.HasSuffix(m, Redacted) {
			return m
		}
		return reSetup.ReplaceAllString(m, "${1}"+Redacted)
	})
	count(reLongTok, func(m string) string {
		if looksRandom(m) {
			return Redacted
		}
		return m
	})
	return s, Counts{Secrets: n}
}

// looksRandom: a token with both a digit and a letter (plain long words and
// dashed names are left alone).
func looksRandom(m string) bool {
	var dig, let bool
	for _, c := range m {
		switch {
		case c >= '0' && c <= '9':
			dig = true
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
			let = true
		}
	}
	return dig && let
}

// ReplaceLiterals removes every occurrence of each literal (at least
// MinLiteralSecret bytes long).
func ReplaceLiterals(s string, lits []string) (string, int) {
	n := 0
	seen := map[string]bool{}
	sorted := make([]string, 0, len(lits))
	for _, l := range lits {
		if len(l) >= MinLiteralSecret && !seen[l] && !plainValue(l) {
			seen[l] = true
			sorted = append(sorted, l)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) }) // longest first
	for _, l := range sorted {
		n += strings.Count(s, l)
		s = strings.ReplaceAll(s, l, Redacted)
	}
	return s, n
}

// ExtractSecrets returns the secret values found in config text: values of
// secret-like keys and registration passwords (not "auto", not already
// redacted), longest first. They feed ReplaceLiterals.
func ExtractSecrets(conf string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		m := reConfLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		key, val := m[2], stripComment(m[4])
		var secret string
		switch {
		case strings.EqualFold(key, "register"):
			if _, p, _, ok := splitRegister(val); ok {
				secret = p
			}
		case reConfSecret.MatchString(key):
			secret = val
		}
		if len(secret) >= MinLiteralSecret && !plainValue(secret) && !seen[secret] {
			seen[secret] = true
			out = append(out, secret)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// IsPublicIP reports whether ip is a routable address that identifies a
// node: not loopback, private (RFC 1918, unique local), link local,
// shared address space (100.64.0.0/10), multicast or unspecified. The
// documentation ranges (192.0.2.0/24 and so on) count as public so that
// placeholder data behaves like a real address.
func IsPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 {
		return false
	}
	return true
}

// ExtractIdentity returns the callsigns, public addresses and host names a
// config text reveals about its node: callsign and call keys, the
// "|iCALL" of idrecording and idtalkover, and the extern* and external_*
// address keys.
func ExtractIdentity(conf string) Identity {
	var id Identity
	seen := map[string]bool{}
	add := func(dst *[]string, v string) {
		k := strings.ToLower(v)
		if v != "" && !seen[k] {
			seen[k] = true
			*dst = append(*dst, v)
		}
	}
	for _, line := range strings.Split(conf, "\n") {
		m := reConfLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil || strings.ContainsAny(strings.TrimSpace(m[1]), ";#") {
			continue // commented-out lines are not the node's identity
		}
		key, val := strings.ToLower(m[2]), stripComment(m[4])
		switch {
		case key == "callsign" || key == "call":
			if c := baseCallsign(val); c != "" {
				add(&id.Callsigns, c)
			}
		case key == "idrecording" || key == "idtalkover":
			for _, mm := range reIDRecording.FindAllStringSubmatch(val, -1) {
				if c := baseCallsign(mm[1]); c != "" {
					add(&id.Callsigns, c)
				}
			}
		case externalKeys[key]:
			host := val
			if h, _, err := net.SplitHostPort(val); err == nil {
				host = h
			}
			host = strings.Trim(host, "[]")
			if ip := net.ParseIP(host); ip != nil {
				if IsPublicIP(ip) {
					add(&id.IPs, ip.String())
				}
			} else if strings.Contains(host, ".") && !strings.ContainsAny(host, " \t") {
				add(&id.Hosts, host)
			}
		}
	}
	return id
}

// stripComment trims a value and a trailing "; comment" from it.
func stripComment(v string) string {
	if i := strings.IndexByte(v, ';'); i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// baseCallsign returns the callsign part of v ("N0CALL-L", "N0CALL/R" give
// "N0CALL") when it looks like one, else "".
func baseCallsign(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	if i := strings.IndexAny(v, "-/"); i >= 0 {
		v = v[:i]
	}
	if reCallsignTok.MatchString(v) {
		return v
	}
	return ""
}

// MaskIdentity replaces the node's own callsigns, public addresses and host
// names in text.
func MaskIdentity(s string, id Identity) (string, int) {
	n := 0
	if toks := nonEmpty(id.Callsigns); len(toks) > 0 {
		// "|iN0CALL" in voice-ID strings: the "i" is a word character, so \b does not match there.
		pre := regexp.MustCompile(`(?i)(\|i)(?:` + quoteAlt(toks) + `)\b`)
		n += len(pre.FindAllStringIndex(s, -1))
		s = pre.ReplaceAllString(s, "${1}"+phCallsign)
		re := regexp.MustCompile(`(?i)\b(?:` + quoteAlt(toks) + `)\b`)
		n += len(re.FindAllStringIndex(s, -1))
		s = re.ReplaceAllString(s, phCallsign)
	}
	if toks := nonEmpty(id.Hosts); len(toks) > 0 {
		re := regexp.MustCompile(`(?i)\b(?:` + quoteAlt(toks) + `)\b`)
		n += len(re.FindAllStringIndex(s, -1))
		s = re.ReplaceAllString(s, phOwnHost)
	}
	if len(id.IPs) > 0 {
		own := []net.IP{}
		for _, v := range id.IPs {
			if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
				own = append(own, ip)
			}
		}
		isOwn := func(tok string) bool {
			ip := net.ParseIP(tok)
			if ip == nil {
				return false
			}
			for _, o := range own {
				if o.Equal(ip) {
					return true
				}
			}
			return false
		}
		s = reV4.ReplaceAllStringFunc(s, func(m string) string {
			if isOwn(m) {
				n++
				return phOwnIP
			}
			return m
		})
		s = reV6Run.ReplaceAllStringFunc(s, func(run string) string {
			core, zone := run, ""
			if i := strings.IndexByte(core, '%'); i >= 0 {
				core, zone = core[:i], core[i:]
			}
			trimmed := strings.TrimRight(core, ":.")
			if !strings.Contains(trimmed, ":") || !isOwn(trimmed) {
				return run
			}
			n++
			return phOwnIP + core[len(trimmed):] + zone
		})
	}
	return s, n
}

func nonEmpty(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func quoteAlt(toks []string) string {
	q := make([]string, len(toks))
	for i, t := range toks {
		q[i] = regexp.QuoteMeta(t)
	}
	sort.Slice(q, func(i, j int) bool { return len(q[i]) > len(q[j]) })
	return strings.Join(q, "|")
}

// RedactBundleText runs the whole pipeline for one file: private key blocks
// and secret keys (line rules for config files, key rules for text), then
// the literal secrets, then the identity rules when o.MaskIdentity is set.
func RedactBundleText(s string, kind Kind, o BundleOptions) (string, Counts) {
	var c, d Counts
	if kind == KindConf {
		s, d = RedactConfText(s, o.MaskIdentity)
		c = c.Add(d)
	}
	s, d = RedactSecretsText(s)
	c = c.Add(d)
	var n int
	s, n = ReplaceLiterals(s, o.Secrets)
	c.Secrets += n
	if o.MaskIdentity {
		s, n = MaskIdentity(s, o.Identity)
		c.Identity += n
	}
	return s, c
}

// ExcludedPath reports whether a file must never go into a bundle, whatever
// the caller asks for: password and key material, the nodespot database and
// session data, setup codes, NetworkManager connection files (they hold
// WiFi passwords) and the Allmon3 user list.
func ExcludedPath(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	for _, comp := range strings.Split(p, "/") {
		switch comp {
		case "keys", "private", "ssl", "certs", "system-connections", ".ssh", "sessions", "session":
			return true
		}
	}
	base := p[strings.LastIndexByte(p, '/')+1:]
	switch base {
	case "shadow", "gshadow", "shadow-", "gshadow-", "passwd-", "users", "setup-code", "authorized_keys", ".htpasswd", "htpasswd", "wpa_supplicant.conf":
		return true
	}
	for _, pre := range []string{"id_", "ssh_host_", "shadow", "session", "cookie"} {
		if strings.HasPrefix(base, pre) {
			return true
		}
	}
	for _, suf := range []string{".key", ".pem", ".p12", ".pfx", ".crt", ".db", ".sqlite", ".sqlite3", ".db-wal", ".db-shm", ".db-journal",
		".nmconnection", ".token", ".secret", "_rsa", "_dsa", "_ecdsa", "_ed25519", "~", ".bak", ".orig"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return strings.Contains(base, ".sqlite") || strings.Contains(base, "private")
}
