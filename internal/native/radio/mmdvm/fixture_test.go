package mmdvm_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm"
	"github.com/w1jar/nodespot/internal/native/radio/mmdvm/fake"
)

const (
	fixtureVersion = "testdata/hs-dual-v1.6.1-version.hex"
	fixtureStatus  = "testdata/hs-dual-v1.6.1-status.hex"
	fixtureDesc    = "MMDVM_HS-v1.6.1 20231115_WPSD 14.7456MHz dual ADF7021 FW by AUTHORS. GitID #7e16099"
)

// readHex reads a testdata hex dump: '#' comments, bytes as hex pairs.
func readHex(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var digits strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		digits.WriteString(strings.Join(strings.Fields(line), ""))
	}
	b, err := hex.DecodeString(digits.String())
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return b
}

// oneFrame decodes b, which must be exactly one frame.
func oneFrame(t *testing.T, b []byte) mmdvm.Frame {
	t.Helper()
	var dec mmdvm.Decoder
	var got []mmdvm.Frame
	dec.Feed(b, func(f mmdvm.Frame) { got = append(got, f) })
	if len(got) != 1 {
		t.Fatalf("decoded %d frames from % X", len(got), b)
	}
	if int(b[1]) != len(b) {
		t.Fatalf("length byte %d, frame is %d bytes", b[1], len(b))
	}
	return got[0]
}

func TestHatVersionFixture(t *testing.T) {
	f := oneFrame(t, readHex(t, fixtureVersion))
	if f.Cmd != mmdvm.CmdGetVersion {
		t.Fatalf("command %s", mmdvm.CommandName(f.Cmd))
	}
	v, err := mmdvm.ParseVersion(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if v.Protocol != 1 || !v.DMR() {
		t.Errorf("protocol %d, DMR %v", v.Protocol, v.DMR())
	}
	if v.Description != fixtureDesc {
		t.Errorf("description %q", v.Description)
	}
	if string(v.UDID) != strings.Repeat("0", 24) {
		t.Errorf("UDID %q, want the 24-digit placeholder", v.UDID)
	}
	b := mmdvm.ParseBoard(v.Description)
	want := mmdvm.Board{Name: "MMDVM_HS", Version: "1.6.1", BuildDate: "2023-11-15", BuildTag: "WPSD",
		TCXO: "14.7456MHz", RFChip: "ADF7021", Dual: true, GitID: "7e16099", DuplexCapable: true}
	if b != want {
		t.Errorf("board\n got %+v\nwant %+v", b, want)
	}
}

func TestHatStatusFixture(t *testing.T) {
	f := oneFrame(t, readHex(t, fixtureStatus))
	if f.Cmd != mmdvm.CmdGetStatus || len(f.Payload)+3 != mmdvm.StatusLen1 {
		t.Fatalf("frame %s", f)
	}
	s, err := mmdvm.ParseStatus(1, f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	want := mmdvm.Status{Protocol: 1, EnabledModes: 0xBF, Mode: mmdvm.ModeIdle, Flags: 0,
		DStarSpace: 76, DMRSpace1: 10, DMRSpace2: 25, YSFSpace: 12, P25Space: 6, NXDNSpace: 31,
		POCSAGSpace: 14, M17Space: 31}
	if s != want {
		t.Errorf("status\n got %+v\nwant %+v", s, want)
	}
	if got := mmdvm.ModeNames(s.EnabledModes); !reflect.DeepEqual(got,
		[]string{"dstar", "dmr", "ysf", "p25", "nxdn", "pocsag", "m17"}) {
		t.Errorf("modes %v", got)
	}
	if mmdvm.ModeName(s.Mode) != "idle" || s.TX() {
		t.Errorf("mode %q, TX %v", mmdvm.ModeName(s.Mode), s.TX())
	}
	// Re-encoding gives the bytes the hat sent.
	b, _ := mmdvm.EncodeStatus(s)
	if !bytes.Equal(b, readHex(t, fixtureStatus)) {
		t.Errorf("re-encoded % X", b)
	}
}

// The fake modem's default version reply is the captured one.
func TestFakeDefaultVersion(t *testing.T) {
	b, err := mmdvm.EncodeVersion(mmdvm.Version{Protocol: 1, Description: fake.DefaultDescription, UDID: []byte(fake.UDID1)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, readHex(t, fixtureVersion)) {
		t.Errorf("fake version % X", b)
	}
}

func TestParseBoard(t *testing.T) {
	cases := []struct {
		desc string
		want mmdvm.Board
	}{
		{"MMDVM_HS_Hat-v1.6.1 20230101 14.7456MHz ADF7021 FW by AUTHORS GitID #abc1234",
			mmdvm.Board{Name: "MMDVM_HS_Hat", Version: "1.6.1", BuildDate: "2023-01-01", TCXO: "14.7456MHz",
				RFChip: "ADF7021", GitID: "abc1234"}},
		{"ZUMspot-v1.4.17 20190101 14.7456MHz ADF7021 FW by AUTHORS",
			mmdvm.Board{Name: "ZUMspot", Version: "1.4.17", BuildDate: "2019-01-01", TCXO: "14.7456MHz", RFChip: "ADF7021"}},
		{"MMDVM 20200101 TCXO (D-Star/DMR/System Fusion/P25/NXDN)",
			mmdvm.Board{Name: "MMDVM", BuildDate: "2020-01-01", DuplexCapable: true}},
		{"", mmdvm.Board{}},
		{"MMDVM_HS-v1.6.1 99999999", mmdvm.Board{Name: "MMDVM_HS", Version: "1.6.1"}},
	}
	for _, c := range cases {
		if got := mmdvm.ParseBoard(c.desc); got != c.want {
			t.Errorf("%q\n got %+v\nwant %+v", c.desc, got, c.want)
		}
	}
	if mmdvm.ModeName(42) != "mode 42" || mmdvm.ModeName(mmdvm.ModeDMR) != "dmr" {
		t.Error("mode names")
	}
}

// replay is a modem that answers GET_VERSION and GET_STATUS with captured
// frames and refuses everything else, recording what the host sent.
type replay struct {
	version, status []byte

	mu   sync.Mutex
	cmds []byte
}

func (r *replay) open() (io.ReadWriteCloser, error) {
	host, dev := net.Pipe()
	go func() {
		defer dev.Close()
		var dec mmdvm.Decoder
		buf := make([]byte, 256)
		for {
			n, err := dev.Read(buf)
			dec.Feed(buf[:n], func(f mmdvm.Frame) {
				r.mu.Lock()
				r.cmds = append(r.cmds, f.Cmd)
				r.mu.Unlock()
				switch f.Cmd {
				case mmdvm.CmdGetVersion:
					_, _ = dev.Write(r.version)
				case mmdvm.CmdGetStatus:
					_, _ = dev.Write(r.status)
				default:
					_, _ = dev.Write(mmdvm.Nak(f.Cmd, mmdvm.ReasonInvalidCommand))
				}
			})
			if err != nil {
				return
			}
		}
	}()
	return host, nil
}

func (r *replay) commands() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.cmds...)
}

func waitFor(t *testing.T, ctx context.Context, what string, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(time.Millisecond):
		}
	}
}

// A detect-only driver on the captured hat reads the board and polls
// status, and sends nothing but GET_VERSION and GET_STATUS.
func TestDetectOnlyReplay(t *testing.T) {
	r := &replay{version: readHex(t, fixtureVersion), status: readHex(t, fixtureStatus)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := mmdvm.Settings{Open: r.open, DetectOnly: true, AllowTX: true,
		Modem:          mmdvm.Config{Duplex: true, ColorCode: 1},
		Freq:           mmdvm.Freq{RX: 123456000, TX: 123456000, Power: 100},
		StatusInterval: 2 * time.Millisecond, Inactivity: time.Second, BootWait: time.Millisecond,
		VersionRetry: 5 * time.Millisecond, ReplyTimeout: 500 * time.Millisecond, Playout: time.Millisecond,
		ReopenDelay: time.Millisecond}
	d := mmdvm.New(st)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.Run(runCtx, newSink()) }()
	waitFor(t, ctx, "status polls", func() bool {
		n := 0
		for _, c := range r.commands() {
			if c == mmdvm.CmdGetStatus {
				n++
			}
		}
		return n >= 3 && d.Status().State != ""
	})
	s := d.Status()
	if !s.Open || s.Protocol != 1 || s.Board != "MMDVM_HS" || s.Firmware != "1.6.1" ||
		s.BuildDate != "2023-11-15" || !s.DuplexCapable || s.State != "idle" || s.Flags != 0 ||
		s.Space != [2]int{10, 25} || s.NAKs != 0 || s.Error != "" {
		t.Errorf("status %+v", s)
	}
	if !reflect.DeepEqual(s.Modes, []string{"dstar", "dmr", "ysf", "p25", "nxdn", "pocsag", "m17"}) {
		t.Errorf("modes %v", s.Modes)
	}
	if err := d.StartTX(network.CallHeader{Call: 1, SrcID: testSrc, TG: testTG, Slot: 1}); !errors.Is(err, mmdvm.ErrTXInhibited) {
		t.Errorf("StartTX: %v", err)
	}
	d.Abort()
	time.Sleep(10 * time.Millisecond)
	stop()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	for i, c := range r.commands() {
		if c != mmdvm.CmdGetVersion && c != mmdvm.CmdGetStatus {
			t.Fatalf("command %d is %s; detect-only sends only GET_VERSION and GET_STATUS", i, mmdvm.CommandName(c))
		}
	}
}

func TestReopen(t *testing.T) {
	h := start(t, fake.Options{}, func(s *mmdvm.Settings) { s.ReopenDelay = time.Hour })
	h.waitOpen()
	h.d.Reopen()
	h.waitModem(func(s fake.State) bool { return s.Opens >= 2 && h.d.Status().Open })
	if st := h.d.Status(); st.Reopens != 1 || st.Error != "" {
		t.Errorf("status %+v", st)
	}
}

func TestOverflowCount(t *testing.T) {
	h := start(t, fake.Options{}, nil)
	h.waitOpen()
	b, _ := mmdvm.EncodeStatus(mmdvm.Status{Protocol: 1, Flags: mmdvm.FlagRXOverflow | mmdvm.FlagTXOverflow})
	if err := h.m.Inject(b); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h.ctx, "overflow counts", func() bool {
		s := h.d.Status()
		return s.RXOverflows >= 1 && s.TXOverflows >= 1
	})
}
