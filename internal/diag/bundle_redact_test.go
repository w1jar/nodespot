package diag

import (
	"net"
	"reflect"
	"strings"
	"testing"
)

// Placeholder data only: callsign N0CALL, node 1999, addresses from the
// documentation ranges, password example-pass.

func TestRedactConfLine(t *testing.T) {
	cases := []struct {
		name, in, want string
		identity       bool
		c              Counts
	}{
		{"secret", "secret=example-pass", "secret=<redacted>", false, Counts{Secrets: 1}},
		{"secret spaced", "secret = example-pass", "secret = <redacted>", false, Counts{Secrets: 1}},
		{"secret with trailing comment", "secret=example-pass ; the AMI one", "secret=<redacted>", false, Counts{Secrets: 1}},
		{"value with spaces", "password = example pass phrase", "password = <redacted>", false, Counts{Secrets: 1}},
		{"upper case key", "Password=example-pass", "Password=<redacted>", false, Counts{Secrets: 1}},
		{"md5secret", "md5secret=0123456789abcdef", "md5secret=<redacted>", false, Counts{Secrets: 1}},
		{"pwd (echolink)", "pwd=example-pass", "pwd=<redacted>", false, Counts{Secrets: 1}},
		{"pass (old stack)", "Pass=example-pass", "Pass=<redacted>", false, Counts{Secrets: 1}},
		{"psk", "psk=example-pass", "psk=<redacted>", false, Counts{Secrets: 1}},
		{"wpa-psk", "wpa-psk example-pass", "wpa-psk example-pass", false, Counts{}}, // no "=": handled by the text rules
		{"wep key", "wep-key0=abcdef1234", "wep-key0=<redacted>", false, Counts{Secrets: 1}},
		{"token", "api_token = example-pass", "api_token = <redacted>", false, Counts{Secrets: 1}},
		{"commented out secret", ";secret=example-pass", ";secret=<redacted>", false, Counts{Secrets: 1}},
		{"hash comment", "# password = example-pass", "# password = <redacted>", false, Counts{Secrets: 1}},
		{"auto kept", "secret = auto", "secret = auto", false, Counts{}},
		{"already redacted", "secret=<redacted>", "secret=<redacted>", false, Counts{}},
		{"empty value", "secret=", "secret=", false, Counts{}},
		{"plain key", "host = 192.0.2.10", "host = 192.0.2.10", false, Counts{}},
		{"section", "[1999]", "[1999]", false, Counts{}},
		{"prose comment", "; the secret is kept elsewhere", "; the secret is kept elsewhere", false, Counts{}},
		{"keyed word", "bypass=yes", "bypass=yes", false, Counts{}},
		{"register", "register => 1999:example-pass@register.example.net", "register => 1999:<redacted>@register.example.net", false, Counts{Secrets: 1}},
		{"register no space", "register=>1999:example-pass@192.0.2.5:4569", "register=>1999:<redacted>@192.0.2.5:4569", false, Counts{Secrets: 1}},
		{"register authuser", "register => 1999:example-pass:authuser@host.example.net/1999", "register => 1999:<redacted>:authuser@host.example.net/1999", false, Counts{Secrets: 1}},
		{"register password with at", "register => 1999:example@pass@host.example.net", "register => 1999:<redacted>@host.example.net", false, Counts{Secrets: 1}},
		{"register without password", "register => 1999@host.example.net", "register => 1999@host.example.net", false, Counts{}},
		{"register redacted", "register => 1999:<redacted>@host.example.net", "register => 1999:<redacted>@host.example.net", false, Counts{}},
		{"crlf kept by text func", "secret=example-pass", "secret=<redacted>", false, Counts{Secrets: 1}},
		// identity keys: only with the checkbox
		{"callsign off", "callsign=N0CALL", "callsign=N0CALL", false, Counts{}},
		{"callsign on", "callsign=N0CALL", "callsign=<masked>", true, Counts{Identity: 1}},
		{"echolink call on", "call = N0CALL-L", "call = <masked>", true, Counts{Identity: 1}},
		{"latitude on", "Latitude=00.000000", "Latitude=<masked>", true, Counts{Identity: 1}},
		{"email on", "email=someone@example.net", "email=<masked>", true, Counts{Identity: 1}},
		{"secret still with identity", "secret=example-pass", "secret=<redacted>", true, Counts{Secrets: 1}},
	}
	for _, c := range cases {
		got, n := RedactConfLine(c.in, c.identity)
		if got != c.want || n != c.c {
			t.Errorf("%s:\n in:   %q\n got:  %q %+v\n want: %q %+v", c.name, c.in, got, n, c.want, c.c)
		}
	}
}

func TestRedactConfText(t *testing.T) {
	in := "[general]\r\nsecret=example-pass\r\n[1999]\nsecret = example-pass\n;old=1\n" +
		"tlsprivatekey=/etc/asterisk/keys/x.pem\n-----BEGIN RSA PRIVATE KEY-----\nMIIexample\n-----END RSA PRIVATE KEY-----\nend\n"
	got, c := RedactConfText(in, false)
	if strings.Contains(got, "example-pass") || strings.Contains(got, "MIIexample") || strings.Contains(got, "RSA PRIVATE") {
		t.Fatalf("leak: %q", got)
	}
	for _, keep := range []string{"[general]\r\n", "[1999]", ";old=1", "tlsprivatekey=/etc/asterisk/keys/x.pem", "\nend\n"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q in %q", keep, got)
		}
	}
	if c.Secrets != 3 {
		t.Errorf("counts %+v", c)
	}
	if again, c2 := RedactConfText(got, false); again != got || c2.Secrets != 0 {
		t.Errorf("not idempotent: %q %+v", again, c2)
	}
}

func TestRedactSecretsText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"kv", "login secret=example-pass ok", "login secret=<redacted> ok"},
		{"json", `{"password":"example-pass","user":"x"}`, `{"password":<redacted>,"user":"x"}`},
		{"manager action", "Action: Login\r\nUsername: admin\r\nSecret: example-pass\r\n", "Action: Login\r\nUsername: admin\r\nSecret: <redacted>\r\n"},
		{"psk", "802-11-wireless-security.psk: example-pass", "802-11-wireless-security.psk: <redacted>"},
		{"wpa_passphrase", `wpa_passphrase="example-pass"`, `wpa_passphrase=<redacted>`},
		{"cookie", "Cookie: nodespot_session=abcdef", "Cookie: <redacted>"},
		{"session kv", "session=abcdef", "session=<redacted>"},
		{"bearer", "Authorization: Bearer abcdefghijkl", "Authorization: <redacted>"},
		{"url creds", "GET https://user:example-pass@host.example.net/x", "GET https://<redacted>@host.example.net/x"},
		{"setup code", "http://192.0.2.10:8080/#/setup?code=ABCD1234EFGH", "http://192.0.2.10:8080/#/setup?code=<redacted>"},
		{"register in log", "Sending register => 1999:example-pass@host.example.net now", "Sending register => 1999:<redacted>@host.example.net now"},
		{"long token", "id 0123456789abcdef0123456789abcdef0123 end", "id <redacted> end"},
		{"ordinary line kept", "Nov 20 12:00:00 node2001 asterisk[1]: Registered 1999 to 192.0.2.5", "Nov 20 12:00:00 node2001 asterisk[1]: Registered 1999 to 192.0.2.5"},
		{"callsign kept", "link N0CALL to 1999", "link N0CALL to 1999"},
		{"public ip kept", "peer 198.51.100.7:4569", "peer 198.51.100.7:4569"},
		{"keyed word kept", "bypass: yes", "bypass: yes"},
		{"pem", "a\n-----BEGIN PRIVATE KEY-----\nQUJD\n-----END PRIVATE KEY-----\nb", "a\n<redacted private key>\nb"},
		{"pem truncated", "a\n-----BEGIN EC PRIVATE KEY-----\nQUJD", "a\n<redacted private key>"},
	}
	for _, c := range cases {
		got, n := RedactSecretsText(c.in)
		if got != c.want {
			t.Errorf("%s:\n in:   %q\n got:  %q\n want: %q", c.name, c.in, got, c.want)
		}
		if (got != c.in) != (n.Secrets > 0) || n.Identity != 0 {
			t.Errorf("%s: counts %+v for change=%v", c.name, n, got != c.in)
		}
	}
}

func TestReplaceLiterals(t *testing.T) {
	cases := []struct {
		name, in string
		lits     []string
		want     string
		n        int
	}{
		{"everywhere", "a example-pass b example-pass", []string{"example-pass"}, "a <redacted> b <redacted>", 2},
		{"short ignored", "radio radio", []string{"radio"}, "radio radio", 0},
		{"auto ignored", "auto", []string{"auto"}, "auto", 0},
		{"longest first", "example-pass-two", []string{"example-pass", "example-pass-two"}, "<redacted>", 1},
		{"duplicates", "example-pass", []string{"example-pass", "example-pass"}, "<redacted>", 1},
		{"none", "nothing here", nil, "nothing here", 0},
	}
	for _, c := range cases {
		got, n := ReplaceLiterals(c.in, c.lits)
		if got != c.want || n != c.n {
			t.Errorf("%s: %q %d want %q %d", c.name, got, n, c.want, c.n)
		}
	}
}

func TestExtractSecrets(t *testing.T) {
	conf := "[general]\nsecret=example-pass\n[x]\nsecret = auto\npassword=short\nregister => 1999:example-reg@host.example.net\n" +
		";secret=example-old\nusername=admin\npwd=example-echo ; comment\n"
	got := ExtractSecrets(conf)
	want := []string{"example-pass", "example-echo", "example-reg", "example-old"}
	sortByLenThenName := func(s []string) { // order is by length, ties undefined
		for i := range s {
			for j := i + 1; j < len(s); j++ {
				if len(s[j]) > len(s[i]) || len(s[j]) == len(s[i]) && s[j] < s[i] {
					s[i], s[j] = s[j], s[i]
				}
			}
		}
	}
	sortByLenThenName(got)
	sortByLenThenName(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := map[string]bool{
		"192.0.2.10": true, "198.51.100.7": true, "203.0.113.9": true, "8.8.8.8": true, "2001:db8::10": true,
		"10.1.2.3": false, "172.16.0.5": false, "172.32.0.5": true, "192.168.1.20": false, "127.0.0.1": false, "0.0.0.0": false,
		"169.254.1.1": false, "100.64.0.1": false, "100.127.255.255": false, "100.128.0.1": true,
		"::1": false, "fe80::1": false, "fd00::1": false, "ff02::1": false, "224.0.0.1": false,
	}
	for s, want := range cases {
		if got := IsPublicIP(net.ParseIP(s)); got != want {
			t.Errorf("%s: %v want %v", s, got, want)
		}
	}
	if IsPublicIP(nil) {
		t.Error("nil")
	}
}

func TestExtractIdentity(t *testing.T) {
	conf := "[1999]\ncallsign=N0CALL\nidrecording=|iN0CALL/R\nidtalkover=|iN0CALL,|cx\n[general]\n" +
		"externip=198.51.100.7\nexternhost = node.example.net\nexternaddr=192.0.2.9:5060\nexternip=10.0.0.5\n" +
		";callsign=N0OLD\ncall=N0CALL-L\ncallsign=not a call\n"
	got := ExtractIdentity(conf)
	if !reflect.DeepEqual(got.Callsigns, []string{"N0CALL"}) {
		t.Errorf("callsigns %v", got.Callsigns)
	}
	if !reflect.DeepEqual(got.IPs, []string{"198.51.100.7", "192.0.2.9"}) {
		t.Errorf("ips %v", got.IPs)
	}
	if !reflect.DeepEqual(got.Hosts, []string{"node.example.net"}) {
		t.Errorf("hosts %v", got.Hosts)
	}
}

func TestMaskIdentity(t *testing.T) {
	id := Identity{Callsigns: []string{"N0CALL"}, IPs: []string{"192.0.2.10", "2001:db8::10"}, Hosts: []string{"node.example.net"}}
	cases := []struct {
		name, in, want string
		n              int
	}{
		{"callsign", "node of N0CALL linked", "node of <callsign> linked", 1},
		{"callsign lower and suffix", "n0call and N0CALL-R", "<callsign> and <callsign>-R", 2},
		{"other callsign kept", "W1AW and N0CALLER", "W1AW and N0CALLER", 0},
		{"ip", "from 192.0.2.10:4569", "from <own-ip>:4569", 1},
		{"ip prefix kept", "from 192.0.2.1 and 192.0.2.100", "from 192.0.2.1 and 192.0.2.100", 0},
		{"other public ip kept", "peer 198.51.100.7", "peer 198.51.100.7", 0},
		{"private ip kept", "lan 192.168.1.20", "lan 192.168.1.20", 0},
		{"ipv6", "addr 2001:db8::10/64 up", "addr <own-ip>/64 up", 1},
		{"ipv6 long form", "addr 2001:0db8:0000:0000:0000:0000:0000:0010.", "addr <own-ip>.", 1},
		{"ipv6 bracket", "[2001:db8::10]:4569", "[<own-ip>]:4569", 1},
		{"other ipv6 kept", "addr 2001:db8::11", "addr 2001:db8::11", 0},
		{"host", "via Node.Example.Net now", "via <own-host> now", 1},
		{"host superstring kept", "via mynode.example.net.au", "via mynode.example.net.au", 0},
		{"all", "N0CALL 192.0.2.10 node.example.net", "<callsign> <own-ip> <own-host>", 3},
	}
	for _, c := range cases {
		got, n := MaskIdentity(c.in, id)
		if got != c.want || n != c.n {
			t.Errorf("%s:\n in:   %q\n got:  %q %d\n want: %q %d", c.name, c.in, got, n, c.want, c.n)
		}
	}
	if got, n := MaskIdentity("N0CALL 192.0.2.10", Identity{}); got != "N0CALL 192.0.2.10" || n != 0 {
		t.Errorf("empty identity changed text: %q", got)
	}
}

func TestRedactBundleText(t *testing.T) {
	id := Identity{Callsigns: []string{"N0CALL"}, IPs: []string{"192.0.2.10"}}
	conf := "[1999]\ncallsign=N0CALL\nsecret=example-pass\nhost=192.0.2.10\n"
	log := "asterisk: N0CALL at 192.0.2.10 rejected example-pass for 1999\n"
	off := BundleOptions{Secrets: ExtractSecrets(conf), Identity: id}
	on := off
	on.MaskIdentity = true

	got, c := RedactBundleText(conf, KindConf, off)
	if got != "[1999]\ncallsign=N0CALL\nsecret=<redacted>\nhost=192.0.2.10\n" || c != (Counts{Secrets: 1}) {
		t.Errorf("conf off: %q %+v", got, c)
	}
	got, c = RedactBundleText(conf, KindConf, on)
	if got != "[1999]\ncallsign=<masked>\nsecret=<redacted>\nhost=<own-ip>\n" || c != (Counts{Secrets: 1, Identity: 2}) {
		t.Errorf("conf on: %q %+v", got, c)
	}
	got, c = RedactBundleText(log, KindText, off)
	if got != "asterisk: N0CALL at 192.0.2.10 rejected <redacted> for 1999\n" || c != (Counts{Secrets: 1}) {
		t.Errorf("log off: %q %+v", got, c)
	}
	got, c = RedactBundleText(log, KindText, on)
	if got != "asterisk: <callsign> at <own-ip> rejected <redacted> for 1999\n" || c != (Counts{Secrets: 1, Identity: 2}) {
		t.Errorf("log on: %q %+v", got, c)
	}
	// idempotent
	again, _ := RedactBundleText(got, KindText, on)
	if again != got {
		t.Errorf("not idempotent: %q", again)
	}
}

func TestExcludedPath(t *testing.T) {
	excluded := []string{
		"/etc/shadow", "/etc/gshadow", "/etc/shadow-", "/etc/ssh/ssh_host_ed25519_key", "/root/.ssh/id_ed25519", "/home/x/id_rsa",
		"/etc/asterisk/keys/node.key", "/etc/ssl/private/x.pem", "/etc/asterisk/cert.pem", "/etc/asterisk/x.key",
		"/var/lib/nodespot/nodespot.db", "/var/lib/nodespot/nodespot.db-wal", "/var/lib/nodespot/data.sqlite3", "/var/lib/nodespot/setup-code",
		"/var/lib/nodespot/sessions/abc", "/etc/NetworkManager/system-connections/ExampleNet.nmconnection",
		"/etc/wpa_supplicant/wpa_supplicant.conf", "/etc/allmon3/users", "/etc/asterisk/iax.conf.bak", "/etc/asterisk/iax.conf~",
		"/etc/asterisk/private-notes.txt", "/var/lib/nodespot/session-token", "/etc/apache2/.htpasswd",
	}
	for _, p := range excluded {
		if !ExcludedPath(p) {
			t.Errorf("%s not excluded", p)
		}
	}
	kept := []string{"/etc/asterisk/rpt.conf", "/etc/asterisk/iax.conf", "/etc/asterisk/manager.conf", "/etc/nodespot/nodespot.conf",
		"/etc/allmon3/allmon3.ini", "/opt/MMDVM_Bridge/MMDVM_Bridge.ini", "/opt/Analog_Bridge/Analog_Bridge.ini", "/etc/asterisk/users.conf"}
	for _, p := range kept {
		if ExcludedPath(p) {
			t.Errorf("%s excluded", p)
		}
	}
}
