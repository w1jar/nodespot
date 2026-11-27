package echolink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/gsm"
)

func TestLogonCommand(t *testing.T) {
	now := time.Date(2026, 1, 2, 9, 7, 0, 0, time.UTC)
	tests := []struct {
		name                    string
		call, pass, status, loc string
		want                    string
	}{
		{"online", "n0call-l", "secret", statusOnline, "Example City",
			"lN0CALL-L\xac\xacsecret\rONLINE" + clientVersion + "(09:07)\rExample City\r"},
		{"busy", "N0CALL", "pw", statusBusy, "",
			"lN0CALL\xac\xacpw\rBUSY" + clientVersion + "(09:07)\r\r"},
		{"separators stripped", "N0CALL", "p\rw", statusOffline, "a\rb\nc",
			"lN0CALL\xac\xacpw\rOFF-V" + clientVersion + "(09:07)\rabc\r"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(logonCommand(tc.call, tc.pass, tc.status, tc.loc, now))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// chunkReader returns its input a few bytes per Read, then blocks forever
// unless eof is set.
type chunkReader struct {
	data []byte
	eof  bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		panic("read past the reply without EOF")
	}
	n := min(2, len(p), len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestReadLogonReply(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		eof   bool
		want  error
	}{
		{"ok without close", "OK2.6\n", false, nil},
		{"ok", "OK", true, nil},
		{"bad password", "BAD PASSWORD\n", true, ErrBadLogon},
		{"empty", "", true, ErrBadReply},
		{"garbage", "HELLO", true, ErrBadReply},
		{"short garbage", "X", true, ErrBadReply},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &chunkReader{data: []byte(tc.reply), eof: tc.eof}
			err := readLogonReply(r)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseStationList(t *testing.T) {
	good := stationList(
		[4]string{"N0CALL-L", "Example Town [ON 12:34]", "1997", "192.0.2.10"},
		[4]string{"*EXAMPLE*", "Example conference [BUSY 01:02]", "1998", "203.0.113.5"},
		[4]string{"N1CALL", "No status", "1999", "192.0.2.11"},
	)
	tests := []struct {
		name    string
		in      string
		want    []Station
		wantErr error
	}{
		{"three", good, []Station{
			{Callsign: "N0CALL-L", Location: "Example Town", Status: "ON", Time: "12:34", ID: 1997, Addr: netip.MustParseAddr("192.0.2.10")},
			{Callsign: "*EXAMPLE*", Location: "Example conference", Status: "BUSY", Time: "01:02", ID: 1998, Addr: netip.MustParseAddr("203.0.113.5")},
			{Callsign: "N1CALL", Location: "No status", ID: 1999, Addr: netip.MustParseAddr("192.0.2.11")},
		}, nil},
		{"crlf", strings.ReplaceAll(good, "\n", "\r\n"), nil, nil},
		{"bad records skipped", stationList(
			[4]string{"N0CALL", "x", "notanumber", "192.0.2.1"},
			[4]string{"N1CALL", "x", "2000", "not-an-ip"},
			[4]string{"N1CALL-R", "y [ON 00:00]", "2001", "192.0.2.2"},
		), []Station{{Callsign: "N1CALL-R", Location: "y", Status: "ON", Time: "00:00", ID: 2001, Addr: netip.MustParseAddr("192.0.2.2")}}, nil},
		{"no header", "N0CALL\n", nil, ErrBadReply},
		{"empty", "", nil, ErrBadReply},
		{"bad count", "@@@\nmany\n", nil, ErrBadReply},
		{"no trailer", "@@@\n1\nN0CALL\nx\n2002\n192.0.2.3\n", []Station{{Callsign: "N0CALL", Location: "x", ID: 2002, Addr: netip.MustParseAddr("192.0.2.3")}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStationList(strings.NewReader(tc.in))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				if len(got) != 3 {
					t.Fatalf("got %d stations, want 3", len(got))
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("station %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFindStation(t *testing.T) {
	list := []Station{
		{Callsign: "N0CALL-L", ID: 1997},
		{Callsign: "*EXAMPLE*", ID: 1998},
		{Callsign: "N1CALL", ID: 1999},
	}
	tests := []struct {
		target string
		want   int // ID, 0 for not found
	}{
		{"N0CALL-L", 1997},
		{" n0call-l ", 1997},
		{"1999", 1999},
		{"*EXAMPLE*", 1998},
		{"example", 1998},
		{"N0CALL", 0},
		{"2002", 0},
		{"", 0},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			st, err := findStation(list, tc.target)
			if tc.want == 0 {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil || st.ID != tc.want {
				t.Fatalf("got %+v, %v; want ID %d", st, err, tc.want)
			}
		})
	}
}

func TestStationKind(t *testing.T) {
	tests := []struct {
		call string
		want Kind
	}{
		{"N0CALL", KindUser}, {"N0CALL-L", KindLink}, {"N0CALL-R", KindRepeater}, {"*EXAMPLE*", KindConference},
	}
	for _, tc := range tests {
		if got := (Station{Callsign: tc.call}).Kind(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.call, got, tc.want)
		}
	}
}

func TestAudioPacket(t *testing.T) {
	frames := bytes.Repeat([]byte{0xd0}, audioPayload)
	p := buildAudio(0x1234, 640, frames)
	if len(p) != 144 || p[0] != 0xC0 || p[1] != 3 {
		t.Fatalf("header %x, len %d", p[:12], len(p))
	}
	seq, payload, err := parseAudio(p)
	if err != nil || seq != 0x1234 || !bytes.Equal(payload, frames) {
		t.Fatalf("parse: seq %x err %v", seq, err)
	}

	ext := append([]byte{0x90, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 9, 9, 9, 9}, frames[:33]...)
	padded := append([]byte{0xA0, 3, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}, frames[:33]...)
	padded = append(padded, 0, 0, 3)
	tests := []struct {
		name   string
		p      []byte
		frames int // -1 for an error
	}{
		{"four frames", p, 4},
		{"one frame, version 2", append([]byte{0x80, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, frames[:33]...), 1},
		{"extension", ext, 1},
		{"padding", padded, 1},
		{"short", p[:11], -1},
		{"no payload", p[:12], -1},
		{"partial frame", p[:12+40], -1},
		{"other payload type", append([]byte{0xC0, 0x96}, p[2:]...), -1},
		{"bad padding", append(append([]byte{0xA0, 3}, p[2:12]...), 200), -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, payload, err := parseAudio(tc.p)
			if tc.frames < 0 {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil || len(payload) != tc.frames*gsm.FrameBytes {
				t.Fatalf("payload %d bytes, err %v", len(payload), err)
			}
		})
	}
}

func TestTextPackets(t *testing.T) {
	tests := []struct {
		name string
		p    []byte
		want textPacket
	}{
		{"info", []byte("oNDATA\rN1CALL\rExample Town\r\x00"), textPacket{info: true, text: "N1CALL\nExample Town"}},
		{"chat", []byte("oNDATAN1CALL>hello there\r\n\x00"), textPacket{callsign: "N1CALL", text: "hello there"}},
		{"chat without sender", []byte("oNDATAjust text\r\n"), textPacket{text: "just text"}},
		{"built info", buildInfo("line one\nline two"), textPacket{info: true, text: "line one\nline two"}},
		{"built chat", buildChat("N0CALL", "hi\nall"), textPacket{callsign: "N0CALL", text: "hi all"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !isText(tc.p) {
				t.Fatal("not recognized as text")
			}
			if got := parseText(tc.p); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRTCPPackets(t *testing.T) {
	sdes := buildSDES("N0CALL-L", "Test Op", "Example Town")
	bye := buildBYE()
	for name, p := range map[string][]byte{"sdes": sdes, "bye": bye} {
		if len(p)%4 != 0 {
			t.Errorf("%s: length %d not a multiple of 4", name, len(p))
		}
		// Every packet's length field must cover it exactly.
		for q := p; len(q) > 0; {
			n := (int(binary.BigEndian.Uint16(q[2:])) + 1) * 4
			if n > len(q) || q[0]>>6 != 3 {
				t.Fatalf("%s: bad packet header %x", name, q[:4])
			}
			q = q[n:]
		}
	}

	tests := []struct {
		name     string
		p        []byte
		sdes     bool
		bye      bool
		callsign string
		opName   string
		loc      string
	}{
		{"sdes", sdes, true, false, "N0CALL-L", "Test Op", "Example Town"},
		{"bye", bye, false, true, "", "", ""},
		{"rr only", emptyRR, false, false, "", "", ""},
		{"sdes without name", buildSDES("N1CALL", "", ""), true, false, "N1CALL", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseRTCP(tc.p)
			if err != nil {
				t.Fatal(err)
			}
			if info.sdes != tc.sdes || info.bye != tc.bye {
				t.Fatalf("sdes %v bye %v", info.sdes, info.bye)
			}
			if info.callsign() != tc.callsign || info.name() != tc.opName || info.items[sdesLOC] != tc.loc {
				t.Fatalf("callsign %q name %q loc %q", info.callsign(), info.name(), info.items[sdesLOC])
			}
			if tc.sdes && info.items[sdesCNAME] != "CALLSIGN" {
				t.Fatalf("CNAME %q", info.items[sdesCNAME])
			}
		})
	}
}

// TestParsersArbitraryInput feeds random bytes to every parser.
func TestParsersArbitraryInput(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	seeds := [][]byte{buildSDES("N0CALL", "x", "y"), buildBYE(), buildAudio(1, 2, make([]byte, 132)), buildInfo("a")}
	for i := 0; i < 5000; i++ {
		var p []byte
		if i%2 == 0 {
			p = make([]byte, r.Intn(200))
			r.Read(p)
		} else {
			p = append([]byte(nil), seeds[i%len(seeds)]...)
			if len(p) > 0 {
				p = p[:r.Intn(len(p)+1)]
			}
			for j := 0; j < 3 && len(p) > 0; j++ {
				p[r.Intn(len(p))] = byte(r.Intn(256))
			}
		}
		_, _ = parseRTCP(p)
		_, _, _ = parseAudio(p)
		if isText(p) {
			parseText(p)
		}
		_, _ = parseStationList(bytes.NewReader(p))
		splitDescription(string(p))
	}
}
