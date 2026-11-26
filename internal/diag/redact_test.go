package diag

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	o := Options{KeepCalls: []string{"W1ABC"}, Secrets: []string{"hunter2xyz"}}
	cases := []struct{ name, in, want string }{
		{"ipv4", "link from 192.168.1.20 and 8.8.8.8:5038", "link from <ipv4> and <ipv4>:5038"},
		{"loopback kept", "listening 127.0.0.1:8080 and 127.1.2.3", "listening 127.0.0.1:8080 and 127.1.2.3"},
		{"unspecified kept", "listen 0.0.0.0:8080", "listen 0.0.0.0:8080"},
		{"not an ip", "version 999.1.1.1 ok", "version 999.1.1.1 ok"},
		{"ipv6", "peer 2001:db8::1 up", "peer <ipv6> up"},
		{"ipv6 full", "peer 2001:0db8:85a3:0000:0000:8a2e:0370:7334.", "peer <ipv6>."},
		{"ipv6 bracket", "dial [2001:db8::5]:4569", "dial [<ipv6>]:4569"},
		{"ipv6 loopback", "bind ::1 and [::1]:80 and [::]:80", "bind ::1 and [::1]:80 and [::]:80"},
		{"ipv6 mapped", "from ::ffff:10.1.2.3 x", "from <ipv6> x"},
		{"ipv6 link-local", "fe80::1%eth0 ", "<ipv6> "},
		{"time kept", "at 12:34:56 done", "at 12:34:56 done"},
		{"mac", "dev aa:bb:cc:dd:ee:ff", "dev <mac>"},
		{"email", "mail bob.smith+x@example.co.uk now", "mail <email> now"},
		{"callsign other", "link W2XYZ and K1AB, VE3ABC", "link <call> and <call>, <call>"},
		{"callsign own kept", "node of W1ABC linked to N0CALL", "node of W1ABC linked to <call>"},
		{"callsign suffix", "W1XYZ-R/P", "<call>-R/P"},
		{"node numbers kept", "node 1999 -> 2000 ASL3 DEB12", "node 1999 -> 2000 ASL3 DEB12"},
		{"secret kv", "secret = abc123", "secret = <redacted>"},
		{"secret colon", "Secret: abc123", "Secret: <redacted>"},
		{"password quoted", `password="my pass"`, `password=<redacted>`},
		{"json", `{"token":"abcdef","user":"x"}`, `{"token":<redacted>,"user":"x"}`},
		{"api key", "api_key=zzz and ami_secret=qqq and AMISecret=ppp", "api_key=<redacted> and ami_secret=<redacted> and AMISecret=<redacted>"},
		{"auto kept", "secret = auto", "secret = auto"},
		{"key", "key: deadbeef", "key: <redacted>"},
		{"keyed untouched", "keyed: true", "keyed: true"},
		{"authorization", "Authorization: Bearer abcdefghijkl", "Authorization: <redacted>"},
		{"bearer", "got Bearer abcdefghijklmnop here", "got Bearer <redacted> here"},
		{"cookie", "Cookie: nodespot_session=abc; x=y", "Cookie: <redacted>"},
		{"url creds", "fetch https://user:pw@host.example/x", "fetch https://<redacted>@host.example/x"},
		{"setup link", "open http://10.0.0.5:8080/#/setup?code=ABCD1234EFGH", "open http://<ipv4>:8080/#/setup?code=<redacted>"},
		{"long token", "id 0123456789abcdef0123456789abcdef0123 end", "id <redacted> end"},
		{"long word kept", "supercalifragilisticexpialidocious-and-more-words", "supercalifragilisticexpialidocious-and-more-words"},
		{"literal secret", "ami: login failed with hunter2xyz!", "ami: login failed with <redacted>!"},
		{"path kept", "/usr/libexec/nodespot/nodespot-helper", "/usr/libexec/nodespot/nodespot-helper"},
	}
	for _, c := range cases {
		if got := Redact(c.in, o); got != c.want {
			t.Errorf("%s:\n in:   %q\n got:  %q\n want: %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRedactIdempotent(t *testing.T) {
	in := "10.1.2.3 secret=abc W2XYZ a@b.com 2001:db8::1"
	a := Redact(in, Options{})
	if b := Redact(a, Options{}); a != b {
		t.Errorf("not idempotent: %q vs %q", a, b)
	}
}

func TestAMISecretNeverPresent(t *testing.T) {
	const sec = "S3cr3tAMIvalue"
	log := strings.Join([]string{
		"ami: login as admin secret=" + sec,
		"Action: Login\r\nUsername: admin\r\nSecret: " + sec,
		"unexpected response containing " + sec + " in prose",
		`{"secret":"` + sec + `"}`,
	}, "\n")
	out := Redact(log, Options{Secrets: []string{sec}})
	if strings.Contains(out, sec) {
		t.Fatalf("secret leaked: %s", out)
	}
	// Even without the literal, the key forms are removed.
	out = Redact(log, Options{})
	if strings.Count(out, sec) != 1 { // only the bare-prose mention
		t.Fatalf("unexpected: %s", out)
	}
}

func TestRedactConfigLine(t *testing.T) {
	cases := map[string]string{
		"secret = hunter2":                "secret = <redacted>",
		"  password=abc def":              "  password = <redacted>",
		"host = 127.0.0.1":                "host = 127.0.0.1",
		"; secret = x":                    "; secret = x",
		"[ami]":                           "[ami]",
		"username = auto":                 "username = auto",
		"api_token = zzz":                 "api_token = <redacted>",
		"url = https://u:p@example.org/x": "url = https://u:p@example.org/x", // masked later by Redact
	}
	for in, want := range cases {
		if got := RedactConfigLine(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if got := Redact("url = https://u:p@example.org/x", Options{}); strings.Contains(got, "u:p") {
		t.Errorf("url creds: %q", got)
	}
}

func TestRing(t *testing.T) {
	r := NewRing(3)
	_, _ = r.Write([]byte("a\nb\nc\n"))
	_, _ = r.Write([]byte("d\ne\n"))
	if got := strings.Join(r.Lines(0), ","); got != "c,d,e" {
		t.Errorf("got %q", got)
	}
	if got := strings.Join(r.Lines(2), ","); got != "d,e" {
		t.Errorf("got %q", got)
	}
}

func TestReportRender(t *testing.T) {
	r := Report{
		Version: "0.1.1",
		Node: NodeState{Socket: "/run/nodespot-node/control.sock", Running: true, Callsign: "N0CALL", Active: "allstar", State: "connected", Target: "2000",
			ModeErr: "dial 203.0.113.9:4569: refused secret=topsecret", Modes: []string{"allstar", "bm"}},
		Config:  []string{"[node]", RedactConfigLine("secret = topsecret"), "host = 192.0.2.5"},
		Log:     []string{"2026/11/15 12:00:00 link to N1CALL at 198.51.100.7 from N0CALL mail a@example.net", "login hunter2xyz bad"},
		Secrets: []string{"hunter2xyz"},
	}
	out := r.Render()
	for _, bad := range []string{"topsecret", "203.0.113.9", "192.0.2.5", "198.51.100.7", "N1CALL", "a@example.net", "hunter2xyz"} {
		if strings.Contains(out, bad) {
			t.Errorf("report contains %q:\n%s", bad, out)
		}
	}
	for _, good := range []string{"N0CALL", "control.sock", "Active mode: allstar", "target=2000", "allstar bm"} {
		if !strings.Contains(out, good) {
			t.Errorf("report lacks %q", good)
		}
	}
}
