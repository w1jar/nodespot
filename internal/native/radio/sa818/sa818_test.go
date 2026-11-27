package sa818

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClock advances on Sleep so timeouts elapse without waiting.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

// fakeSerial is a module simulator: each line written is answered by reply.
type fakeSerial struct {
	sent   []string
	reply  func(cmd string) string // "" means stay silent
	rd     bytes.Buffer
	eofAt  bool // return io.EOF when nothing to read
	chunk  int  // max bytes per Read; 0 = unlimited
	failWr bool
}

func (f *fakeSerial) Write(b []byte) (int, error) {
	if f.failWr {
		return 0, errors.New("port gone")
	}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n") {
		f.sent = append(f.sent, line)
		if r := f.reply(line); r != "" {
			f.rd.WriteString(r)
		}
	}
	return len(b), nil
}

func (f *fakeSerial) Read(b []byte) (int, error) {
	if f.rd.Len() == 0 {
		if f.eofAt {
			return 0, io.EOF
		}
		return 0, nil
	}
	if f.chunk > 0 && len(b) > f.chunk {
		b = b[:f.chunk]
	}
	return f.rd.Read(b)
}

func okModule(cmd string) string {
	switch {
	case cmd == "AT+DMOCONNECT":
		return "+DMOCONNECT:0\r\n"
	case strings.HasPrefix(cmd, "AT+DMOSETGROUP"):
		return "+DMOSETGROUP:0\r\n"
	case strings.HasPrefix(cmd, "AT+DMOSETVOLUME"):
		return "+DMOSETVOLUME:0\r\n"
	case strings.HasPrefix(cmd, "AT+SETFILTER"):
		return "+DMOSETFILTER:0\r\n"
	}
	return ""
}

func newClient(f *fakeSerial) *Client {
	c := NewClient(f)
	c.Clock = &fakeClock{now: time.Unix(0, 0)}
	return c
}

func TestToneWire(t *testing.T) {
	tests := []struct {
		name    string
		tone    Tone
		want    string
		wantErr bool
	}{
		{"none", Tone{}, "0000", false},
		{"ctcss 67.0", Tone{Kind: ToneCTCSS, CTCSSHz: 67.0}, "0001", false},
		{"ctcss 100.0", Tone{Kind: ToneCTCSS, CTCSSHz: 100.0}, "0012", false},
		{"ctcss 250.3", Tone{Kind: ToneCTCSS, CTCSSHz: 250.3}, "0038", false},
		{"ctcss bad", Tone{Kind: ToneCTCSS, CTCSSHz: 100.5}, "", true},
		{"dcs 023 normal", Tone{Kind: ToneDCS, DCSCode: 23}, "023N", false},
		{"dcs 754 inverted", Tone{Kind: ToneDCS, DCSCode: 754, DCSInverted: true}, "754I", false},
		{"dcs not octal", Tone{Kind: ToneDCS, DCSCode: 189}, "", true},
		{"dcs zero", Tone{Kind: ToneDCS}, "", true},
		{"unknown kind", Tone{Kind: "x"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.tone.wire()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v; want %q (err %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
	if len(CTCSSTones()) != 38 {
		t.Errorf("tone table has %d entries", len(CTCSSTones()))
	}
}

func TestCommands(t *testing.T) {
	base := DefaultSettings()
	base.RxMHz, base.TxMHz = 146.52, 146.52
	tests := []struct {
		name string
		mod  func(*Settings)
		want []string
		err  bool
	}{
		{"simplex wide", func(*Settings) {}, []string{
			"AT+DMOSETGROUP=1,146.5200,146.5200,0000,4,0000",
			"AT+DMOSETVOLUME=8",
			"AT+SETFILTER=1,0,0",
		}, false},
		{"narrow repeater ctcss", func(s *Settings) {
			s.Bandwidth = Narrow
			s.RxMHz, s.TxMHz = 442.1, 447.1
			s.TxTone = Tone{Kind: ToneCTCSS, CTCSSHz: 88.5}
			s.RxTone = Tone{Kind: ToneCTCSS, CTCSSHz: 88.5}
			s.Squelch, s.Volume = 2, 5
			s.Filters = Filters{}
		}, []string{
			"AT+DMOSETGROUP=0,447.1000,442.1000,0008,2,0008",
			"AT+DMOSETVOLUME=5",
			"AT+SETFILTER=1,1,1", // all bypassed
		}, false},
		{"all filters on", func(s *Settings) { s.Filters = Filters{Emphasis: true, HighPass: true, LowPass: true} }, []string{
			"AT+DMOSETGROUP=1,146.5200,146.5200,0000,4,0000",
			"AT+DMOSETVOLUME=8",
			"AT+SETFILTER=0,0,0", // the manual's example: all three enabled
		}, false},
		{"dcs", func(s *Settings) {
			s.RxTone = Tone{Kind: ToneDCS, DCSCode: 23}
			s.TxTone = Tone{Kind: ToneDCS, DCSCode: 25, DCSInverted: true}
		}, []string{
			"AT+DMOSETGROUP=1,146.5200,146.5200,025I,4,023N",
			"AT+DMOSETVOLUME=8",
			"AT+SETFILTER=1,0,0",
		}, false},
		{"frequency out of band", func(s *Settings) { s.RxMHz = 200 }, nil, true},
		{"squelch too high", func(s *Settings) { s.Squelch = 9 }, nil, true},
		{"volume zero", func(s *Settings) { s.Volume = 0 }, nil, true},
		{"bad tone", func(s *Settings) { s.TxTone = Tone{Kind: ToneCTCSS, CTCSSHz: 1} }, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mod(&s)
			got, err := s.Commands()
			if (err != nil) != tc.err {
				t.Fatalf("err = %v", err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestParseReply(t *testing.T) {
	tests := []struct {
		in      string
		want    Reply
		ok      bool
		wantErr bool
	}{
		{"+DMOCONNECT:0", Reply{"DMOCONNECT", "0"}, true, false},
		{"+DMOSETGROUP:0\r\n", Reply{"DMOSETGROUP", "0"}, true, false},
		{"+DMOSETGROUP:1", Reply{"DMOSETGROUP", "1"}, false, false},
		{"+DMOSETVOLUME: 0 ", Reply{"DMOSETVOLUME", "0"}, true, false},
		{"garbage", Reply{}, false, true},
		{"+NOCOLON", Reply{}, false, true},
		{"+:0", Reply{}, false, true},
		{"", Reply{}, false, true},
	}
	for _, tc := range tests {
		got, err := ParseReply(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want || (err == nil && got.OK() != tc.ok) {
			t.Errorf("ParseReply(%q) = %+v, %v", tc.in, got, err)
		}
	}
}

func TestApplyOverFakeSerial(t *testing.T) {
	f := &fakeSerial{reply: okModule, chunk: 5} // short reads exercise line assembly
	c := newClient(f)
	s := DefaultSettings()
	s.RxMHz, s.TxMHz = 146.52, 146.52
	if err := c.Apply(s); err != nil {
		t.Fatal(err)
	}
	want := []string{"AT+DMOCONNECT", "AT+DMOSETGROUP=1,146.5200,146.5200,0000,4,0000", "AT+DMOSETVOLUME=8", "AT+SETFILTER=1,0,0"}
	if strings.Join(f.sent, "|") != strings.Join(want, "|") {
		t.Errorf("sent %q", f.sent)
	}
}

func TestApplyFailures(t *testing.T) {
	good := DefaultSettings()
	good.RxMHz, good.TxMHz = 146.52, 146.52
	tests := []struct {
		name   string
		reply  func(string) string
		eof    bool
		failWr bool
		s      Settings
		want   string
	}{
		{"silent module", func(string) string { return "" }, false, false, good, "did not answer connect"},
		{"closed port", func(string) string { return "" }, true, false, good, "did not answer connect"},
		{"write error", okModule, false, true, good, "port gone"},
		{"group refused", func(c string) string {
			if strings.HasPrefix(c, "AT+DMOSETGROUP") {
				return "+DMOSETGROUP:1\r\n"
			}
			return okModule(c)
		}, false, false, good, "DMOSETGROUP refused"},
		{"invalid settings never touch the port", okModule, false, false, Settings{}, "frequency"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSerial{reply: tc.reply, eofAt: tc.eof, failWr: tc.failWr}
			err := newClient(f).Apply(tc.s)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestConnectRetries(t *testing.T) {
	n := 0
	f := &fakeSerial{reply: func(cmd string) string {
		n++
		if n < 3 {
			return "" // silent for the first two attempts
		}
		return "+DMOCONNECT:0\r\n"
	}}
	if err := newClient(f).Connect(); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("attempts = %d", n)
	}
}

func TestProgrammerSavesOnlyOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sa818.json")
	s := DefaultSettings()
	s.RxMHz, s.TxMHz = 146.52, 146.52
	s.RxTone = Tone{Kind: ToneCTCSS, CTCSSHz: 100.0}

	fail := &fakeSerial{reply: func(string) string { return "" }}
	p := &Programmer{Client: newClient(fail), StatePath: path}
	if err := p.Apply(s); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := LoadState(path); !errors.Is(err, ErrNoState) {
		t.Fatalf("state saved after failure: %v", err)
	}

	p = &Programmer{Client: newClient(&fakeSerial{reply: okModule}), StatePath: path,
		Now: func() time.Time { return time.Date(2026, 11, 26, 3, 4, 5, 0, time.UTC) }}
	if err := p.Apply(s); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Settings != s || st.Version != 2 || !st.AppliedAt.Equal(time.Date(2026, 11, 26, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("state = %+v", st)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", fi.Mode())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func TestLoadStateErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{"missing", filepath.Join(dir, "nope"), "no saved settings"},
		{"bad json", write("a", "{"), "parse state"},
		{"wrong version", write("b", `{"version":9}`), "version 9"},
	}
	for _, tc := range tests {
		_, err := LoadState(tc.path)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
}

func TestFindPort(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "bus", "usb", "devices", "1-1.3")
	os.MkdirAll(filepath.Join(dev, "1-1.3:1.0", "ttyUSB1"), 0o755)
	os.WriteFile(filepath.Join(dev, "idVendor"), []byte("1a86\n"), 0o644)
	os.WriteFile(filepath.Join(dev, "idProduct"), []byte("7523\n"), 0o644)
	got, err := FindPort(root, "/dev")
	if err != nil || got != "/dev/ttyUSB1" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := FindPort(t.TempDir(), "/dev"); err == nil {
		t.Error("expected error for empty tree")
	}
	empty := t.TempDir()
	os.MkdirAll(filepath.Join(empty, "bus", "usb", "devices"), 0o755)
	if _, err := FindPort(empty, "/dev"); !errors.Is(err, ErrNoPort) {
		t.Errorf("err = %v", err)
	}
}

// A state file of an earlier build (version 1, filter flags sent inverted)
// does not count as programmed.
func TestStateVersion1IsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sa818.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"applied_at":"2026-01-01T00:00:00Z","settings":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Fatal("a version 1 state loaded")
	}
}
