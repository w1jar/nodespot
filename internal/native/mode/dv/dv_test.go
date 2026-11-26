package dv

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/network"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
	"github.com/w1jar/nodespot/internal/native/audio"
	"github.com/w1jar/nodespot/internal/native/mode"
)

// fakeNet is a loopback stand-in for a DV network client.
type fakeNet struct {
	codec network.Codec
	login bool

	mu     sync.Mutex
	sink   network.Sink
	tunes  []ctl.Target
	unl    int
	starts []network.CallHeader
	voice  []network.VoiceFrame
	ends   []uint64
	ran    chan struct{}
}

func (n *fakeNet) Mode() string          { return "FAKE" }
func (n *fakeNet) Codec() network.Codec  { return n.codec }
func (n *fakeNet) Status() ctl.NetStatus { return ctl.NetStatus{Detail: "fake detail"} }
func (n *fakeNet) Run(ctx context.Context, s network.Sink) error {
	n.mu.Lock()
	n.sink = s
	n.mu.Unlock()
	close(n.ran)
	if n.login {
		s.Login(ctl.LoginPending, "")
	}
	<-ctx.Done()
	return nil
}
func (n *fakeNet) Tune(_ context.Context, t ctl.Target) error {
	n.mu.Lock()
	n.tunes = append(n.tunes, t)
	n.mu.Unlock()
	return nil
}
func (n *fakeNet) Unlink(context.Context) error { n.mu.Lock(); n.unl++; n.mu.Unlock(); return nil }
func (n *fakeNet) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	n.starts = append(n.starts, h)
	n.mu.Unlock()
	return nil
}
func (n *fakeNet) WriteVoice(f network.VoiceFrame) error {
	n.mu.Lock()
	n.voice = append(n.voice, f)
	n.mu.Unlock()
	return nil
}
func (n *fakeNet) EndTX(c uint64) error {
	n.mu.Lock()
	n.ends = append(n.ends, c)
	n.mu.Unlock()
	return nil
}

func (n *fakeNet) snapshot() (tunes []ctl.Target, starts []network.CallHeader, voice []network.VoiceFrame, ends []uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append(tunes, n.tunes...), append(starts, n.starts...), append(voice, n.voice...), append(ends, n.ends...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// fakeAMBE is an in-process AMBE vocoder: a frame carries the first sample.
type fakeAMBE struct{}

func (fakeAMBE) Encode(_ context.Context, p *[vocoder.PCMSamples]int16) ([vocoder.AMBEBytes]byte, error) {
	return [vocoder.AMBEBytes]byte{byte(p[0])}, nil
}
func (fakeAMBE) Decode(_ context.Context, f [vocoder.AMBEBytes]byte) ([vocoder.PCMSamples]int16, error) {
	var p [vocoder.PCMSamples]int16
	p[0] = int16(f[0])
	return p, nil
}

var base = map[string]map[string]string{
	mode.BM:   {KeyDMRID: "1234567", KeyPassword: "pw-secret", KeyAddress: "master.example.net:62031", "keepalive_ms": "5000"},
	mode.TGIF: {KeyDMRID: "1234567", KeyPassword: "pw-secret"},
	mode.DMR:  {KeyDMRID: "1234567", KeyAddress: "192.0.2.4", KeyOptions: "options=TS2=91"},
	mode.YSF:  {},
	mode.P25:  {},
	mode.NXDN: {"nxdn_id": "12345"},
	mode.M17:  {"suffix": "H"},
}

var targets = map[string]struct {
	t      mode.Target
	want   ctl.Target
	codec  network.Codec
	frameN int
}{
	mode.BM:   {mode.Target{ID: "91"}, ctl.Target{TG: 91}, network.CodecAMBE, 7},
	mode.TGIF: {mode.Target{ID: "777", Slot: 1}, ctl.Target{TG: 777, Slot: 1}, network.CodecAMBE, 7},
	mode.DMR:  {mode.Target{ID: "9", Private: true}, ctl.Target{TG: 9, Private: true}, network.CodecAMBE, 7},
	mode.YSF:  {mode.Target{ID: "Alpha"}, ctl.Target{Room: "Alpha", Name: "Alpha room", Host: "192.0.2.7", Port: 42000}, network.CodecAMBE, 7},
	mode.P25:  {mode.Target{ID: "10200"}, ctl.Target{TG: 10200, Host: "p25.example.net", Port: 41000}, network.CodecIMBE, 11},
	mode.NXDN: {mode.Target{ID: "65000"}, ctl.Target{TG: 65000, Host: "203.0.113.9", Port: 41400}, network.CodecAMBE, 7},
	mode.M17:  {mode.Target{ID: "m17-xxx c"}, ctl.Target{Room: "M17-XXX C", Host: "192.0.2.17", Port: 17000}, network.CodecC2, 8},
}

var reflectors = []mode.Target{
	{ID: "alpha", Name: "Alpha room", Host: "192.0.2.7", Port: 42000},
	{ID: "10200", Host: "p25.example.net", Port: 41000},
	{ID: "65000", Host: "203.0.113.9", Port: 41400},
	{ID: "M17-XXX", Host: "192.0.2.17", Port: 17000},
}

func TestEveryModeWithFakeNetwork(t *testing.T) {
	for _, d := range defs {
		t.Run(d.name, func(t *testing.T) {
			tc := targets[d.name]
			s := mode.Settings{Values: base[d.name], Reflectors: reflectors}
			if err := d.spec().CheckEnv(mode.Env{Callsign: "n0call"}, s); err != nil {
				t.Fatalf("Check: %v", err)
			}
			fn := &fakeNet{codec: tc.codec, login: d.login, ran: make(chan struct{})}
			var gotCfg ctl.NetConfig
			var gotID ctl.Identity
			m, err := New(d.name, mode.Env{Callsign: "n0call"}, s, Options{
				NewNetwork: func(cfg ctl.NetConfig, id ctl.Identity) (network.Network, error) {
					gotCfg, gotID = cfg, id
					return fn, nil
				},
				AMBE: fakeAMBE{}, RxSilence: 80 * time.Millisecond, TxTimeout: time.Hour,
				Tick: 5 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			<-fn.ran
			if gotCfg.Mode != d.ctlMode || gotID.Callsign != "N0CALL" {
				t.Fatalf("cfg %+v id %+v", gotCfg, gotID)
			}
			checkConfig(t, d.name, gotCfg, gotID)

			if err := m.Connect(context.Background(), tc.t); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if d.login {
				if st := m.Status(); st.State != mode.StateConnecting || st.Detail != "waiting for login" {
					t.Fatalf("before login: %+v", st)
				}
				tunes, _, _, _ := fn.snapshot()
				if len(tunes) != 0 {
					t.Fatal("tuned before login")
				}
				m.Send(audio.Frame{}) // not sent while waiting for the login
				fn.sink.Login(ctl.LoginOK, "")
			}
			waitFor(t, "tune", func() bool { tunes, _, _, _ := fn.snapshot(); return len(tunes) == 1 })
			tunes, _, _, _ := fn.snapshot()
			if tunes[0] != tc.want {
				t.Fatalf("tune\n got %+v\nwant %+v", tunes[0], tc.want)
			}
			if !d.login {
				fn.sink.Login(ctl.LoginOK, "linked")
			}
			waitFor(t, "connected", func() bool { return m.Status().State == mode.StateConnected })

			// Receive one call.
			fn.sink.Header(network.CallHeader{Call: 50, Src: "N1CALL", SrcID: 1234567, TG: tc.want.TG, Room: tc.want.Room})
			waitFor(t, "talking", m.Talking)
			fn.sink.Voice(network.VoiceFrame{Call: 50, Codec: tc.codec, Data: make([]byte, tc.frameN)})
			fn.sink.Voice(network.VoiceFrame{Call: 50, Codec: tc.codec, Data: make([]byte, tc.frameN+1)}) // bad length: dropped
			fn.sink.Voice(network.VoiceFrame{Call: 51, Codec: tc.codec, Data: make([]byte, tc.frameN)})   // other call: dropped
			select {
			case <-m.Recv():
			case <-time.After(2 * time.Second):
				t.Fatal("no decoded audio")
			}
			if tk := m.Status().Talker; tk == nil || tk.Callsign != "N1CALL" || tk.ID != 1234567 {
				t.Fatalf("talker %+v", tk)
			}
			time.Sleep(30 * time.Millisecond)
			if len(m.Recv()) != 0 {
				t.Fatal("a bad frame was decoded")
			}
			// The call ends by silence.
			waitFor(t, "rx silence", func() bool { return !m.Talking() })

			// A new call is cut by local transmission.
			fn.sink.Header(network.CallHeader{Call: 52, Src: "N1CALL"})
			waitFor(t, "talking 2", m.Talking)
			var f audio.Frame
			f[0] = 9
			m.Send(f)
			if m.Talking() {
				t.Fatal("local transmission did not cut the received call")
			}
			m.Send(f)
			m.EndTx()
			waitFor(t, "end tx", func() bool { _, _, _, e := fn.snapshot(); return len(e) == 1 })
			_, starts, voice, ends := fn.snapshot()
			if len(starts) != 1 || len(voice) != 2 || ends[0] != starts[0].Call {
				t.Fatalf("starts %+v voice %d ends %v", starts, len(voice), ends)
			}
			h := starts[0]
			if h.Src != "N0CALL" || h.TG != tc.want.TG || h.Room != tc.want.Room || h.Slot != tc.want.Slot || h.Private != tc.want.Private {
				t.Fatalf("tx header %+v", h)
			}
			for _, v := range voice {
				if v.Codec != tc.codec || len(v.Data) != tc.frameN || v.Call != h.Call {
					t.Fatalf("voice frame %+v", v)
				}
			}
			if tc.codec == network.CodecAMBE && voice[0].Data[0] != 9 {
				t.Fatal("AMBE encoder not used")
			}

			_ = m.Disconnect(context.Background())
			if m.Status().Target != nil || m.Status().State != mode.StateReady {
				t.Fatalf("after disconnect %+v", m.Status())
			}
			m.Send(f) // nothing to send to
			_, starts, _, _ = fn.snapshot()
			if len(starts) != 1 {
				t.Fatal("sent with no target")
			}
			_ = m.Stop()
			if m.Status().State != mode.StateIdle {
				t.Fatal("not idle")
			}
			if strings.Contains(m.Status().Detail+m.Status().Error, "pw-secret") {
				t.Fatal("password in status")
			}
		})
	}
}

func checkConfig(t *testing.T, name string, c ctl.NetConfig, id ctl.Identity) {
	t.Helper()
	switch name {
	case mode.BM:
		if c.Address != "master.example.net" || c.Port != 62031 || c.Password != "pw-secret" || c.Options["keepalive_ms"] != "5000" || id.DMRID != 1234567 {
			t.Fatalf("bm cfg %+v %+v", c, id)
		}
	case mode.TGIF:
		if c.Address != "" || c.Options["slot"] != "2" {
			t.Fatalf("tgif cfg %+v", c)
		}
	case mode.DMR:
		if c.Address != "192.0.2.4" || c.Port != 0 || c.Options["options"] != "TS2=91" || c.Options["colour_code"] != "1" {
			t.Fatalf("dmr cfg %+v", c)
		}
	case mode.NXDN:
		if c.Options["nxdn_id"] != "12345" {
			t.Fatalf("nxdn cfg %+v", c)
		}
	case mode.M17:
		if c.Options["suffix"] != "H" {
			t.Fatalf("m17 cfg %+v", c)
		}
	}
}

func TestTargets(t *testing.T) {
	s := mode.Settings{Reflectors: reflectors}
	tests := []struct {
		mode string
		id   string
		want string // error substring, "" = ok
	}{
		{mode.BM, "x", "not a talkgroup"},
		{mode.BM, "0", "not a talkgroup"},
		{mode.BM, "16777216", "not a talkgroup"},
		{mode.P25, "10100", "no reflector address"},
		{mode.YSF, "Unknown", "not in the reflector list"},
		{mode.YSF, "192.0.2.8:42000", ""},
		{mode.M17, "M17-XXX", "module"},
		{mode.M17, "M17-YYY 1", "module"},
		{mode.M17, "M17-YYY A", ""}, // the default address is the network's job
	}
	for _, tt := range tests {
		d, _ := lookupDef(tt.mode)
		_, err := d.netTarget(s, ctl.NetConfig{}, mode.Target{ID: tt.id})
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s %q: %v, want %q", tt.mode, tt.id, err, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		mode string
		vals map[string]string
		want string
	}{
		{mode.BM, map[string]string{KeyDMRID: "1234567", KeyAddress: "master.example.net"}, "Hotspot password is required"},
		{mode.BM, map[string]string{KeyDMRID: "x", KeyPassword: "p", KeyAddress: "m.example.net"}, "DMR ID must be a whole number"},
		{mode.BM, map[string]string{KeyDMRID: "99999999", KeyPassword: "p", KeyAddress: "m.example.net"}, "not valid"},
		{mode.DMR, map[string]string{KeyDMRID: "1234567", KeyAddress: "m.example.net:99999"}, "bad port"},
		{mode.YSF, map[string]string{KeyOptions: "novalue"}, "not key=value"},
		{mode.NXDN, map[string]string{}, "NXDN ID is required"},
		{mode.TGIF, map[string]string{KeyDMRID: "1234567"}, "Hotspot password is required"},
		{mode.TGIF, map[string]string{KeyDMRID: "1234567", KeyPassword: "p"}, ""},
	}
	for _, tt := range tests {
		sp, _ := mode.Lookup(tt.mode)
		err := sp.CheckEnv(mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: tt.vals})
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s %v: %v, want %q", tt.mode, tt.vals, err, tt.want)
		}
	}
}

// TestStationIDFallback checks that a mode with no ID of its own takes the
// station's, and that its own value wins.
func TestStationIDFallback(t *testing.T) {
	sp, _ := mode.Lookup(mode.BM)
	vals := map[string]string{KeyPassword: "p", KeyAddress: "m.example.net"}
	s := mode.Settings{Values: vals}
	if err := sp.Check(s); err == nil || !strings.Contains(err.Error(), "DMR ID is required") {
		t.Fatalf("Check without any ID: %v", err)
	}
	env := mode.Env{Callsign: "N0CALL", DMRID: 1234567, NXDNID: 4321}
	if err := sp.CheckEnv(env, s); err != nil {
		t.Fatalf("CheckEnv with the station ID: %v", err)
	}
	d, _ := lookupDef(mode.BM)
	_, id, err := d.netConfig(env, s)
	if err != nil || id.DMRID != 1234567 {
		t.Fatalf("identity %+v, %v", id, err)
	}
	vals[KeyDMRID] = "7654321"
	if _, id, _ = d.netConfig(env, s); id.DMRID != 7654321 {
		t.Fatalf("the mode's own ID should win: %+v", id)
	}
	nd, _ := lookupDef(mode.NXDN)
	cfg, _, err := nd.netConfig(env, mode.Settings{})
	if err != nil || cfg.Options[KeyNXDNID] != "4321" {
		t.Fatalf("nxdn options %v, %v", cfg.Options, err)
	}
	cfg, _, _ = nd.netConfig(env, mode.Settings{Values: map[string]string{KeyNXDNID: "99"}})
	if cfg.Options[KeyNXDNID] != "99" {
		t.Fatalf("nxdn own ID: %v", cfg.Options)
	}
	if _, err := mode.Build(mode.BM, mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: map[string]string{KeyPassword: "p", KeyAddress: "m.example.net"}}); err == nil {
		t.Fatal("Build with no ID anywhere should fail")
	}
}

func TestLoginFailure(t *testing.T) {
	fn := &fakeNet{codec: network.CodecAMBE, login: true, ran: make(chan struct{})}
	m, err := New(mode.BM, mode.Env{Callsign: "N0CALL"}, mode.Settings{Values: base[mode.BM]}, Options{
		NewNetwork: func(ctl.NetConfig, ctl.Identity) (network.Network, error) { return fn, nil },
		AMBE:       fakeAMBE{}, LoginWait: 20 * time.Millisecond, Tick: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Start(context.Background())
	<-fn.ran
	_ = m.Connect(context.Background(), mode.Target{ID: "91"})
	waitFor(t, "login wait error", func() bool { return strings.Contains(m.Status().Error, "not logged in yet") })
	fn.sink.Login(ctl.LoginFailed, "bad password")
	waitFor(t, "login failed", func() bool { return m.Status().State == mode.StateError })
	if e := m.Status().Error; e != "login refused: bad password" {
		t.Fatalf("error %q", e)
	}
	_ = m.Stop()
}

// TestStationCallsignFallback checks that the modes that need a callsign
// take the station's when their own is empty, and that their own wins.
func TestStationCallsignFallback(t *testing.T) {
	for _, n := range []string{mode.YSF, mode.P25, mode.NXDN, mode.M17} {
		sp, _ := mode.Lookup(n)
		s := mode.Settings{Values: map[string]string{KeyNXDNID: "4321"}}
		if n != mode.NXDN {
			s = mode.Settings{}
		}
		if err := sp.CheckEnv(mode.Env{NXDNID: 4321}, s); err == nil || !strings.Contains(err.Error(), "Callsign is required") {
			t.Errorf("%s with no callsign anywhere: %v", n, err)
		}
		if err := sp.CheckEnv(mode.Env{Callsign: "n0call"}, s); err != nil {
			t.Errorf("%s with the station callsign: %v", n, err)
		}
		d, _ := lookupDef(n)
		_, id, err := d.netConfig(mode.Env{Callsign: "n0call"}, s)
		if err != nil || id.Callsign != "N0CALL" {
			t.Errorf("%s identity %+v, %v", n, id, err)
		}
		s.Values = map[string]string{KeyCallsign: "n1call"}
		cfg, id, err := d.netConfig(mode.Env{Callsign: "n0call"}, s)
		if err != nil || id.Callsign != "N1CALL" {
			t.Errorf("%s own callsign: %+v, %v", n, id, err)
		}
		if _, ok := cfg.Options[KeyCallsign]; ok {
			t.Errorf("%s passed the callsign on as a network option", n)
		}
	}
	// The DMR family never needs it from the mode.
	sp, _ := mode.Lookup(mode.TGIF)
	if err := sp.CheckEnv(mode.Env{}, mode.Settings{Values: map[string]string{KeyDMRID: "1234567", KeyPassword: "p"}}); err != nil {
		t.Errorf("tgif: %v", err)
	}
}

// TestLegacyDefaultReflector checks that the removed default reflector
// setting is still accepted in old configs, ignored, and dropped on save.
func TestLegacyDefaultReflector(t *testing.T) {
	env := mode.Env{Callsign: "N0CALL", NXDNID: 4321}
	for _, n := range []string{mode.P25, mode.NXDN, mode.M17} {
		sp, _ := mode.Lookup(n)
		if _, ok := sp.Field(KeyAddress); ok {
			t.Errorf("%s still has an address field", n)
		}
		old := mode.Settings{Values: map[string]string{KeyAddress: "not a valid:address:x"}}
		if err := sp.CheckEnv(env, old); err != nil {
			t.Errorf("%s: old config rejected: %v", n, err)
		}
		if got := sp.Merge(old, old); got.Values[KeyAddress] != "" {
			t.Errorf("%s: legacy key kept on save: %v", n, got.Values)
		}
	}
	// BM's master address is not a default reflector.
	if sp, _ := mode.Lookup(mode.BM); sp.IsLegacy(KeyAddress) {
		t.Error("bm address is legacy")
	}
	if _, err := mode.Build(mode.P25, env, mode.Settings{Values: map[string]string{KeyAddress: "p25.example.net:41000"}}); err != nil {
		t.Errorf("build with the old key: %v", err)
	}
}

// TestTGIFNeedsPassword checks that TGIF is not ready without a hotspot
// password and is ready with a stored one (which satisfies Required even
// though the control API never returns it).
func TestTGIFNeedsPassword(t *testing.T) {
	sp, _ := mode.Lookup(mode.TGIF)
	env := mode.Env{Callsign: "N0CALL"}
	without := mode.Settings{Values: map[string]string{KeyDMRID: "1234567"}}
	if m := sp.Missing(env, without); len(m) != 1 || m[0].Key != KeyPassword {
		t.Fatalf("Missing without password: %+v", m)
	}
	with := mode.Settings{Values: map[string]string{KeyDMRID: "1234567", KeyPassword: "pw-secret"}}
	if m := sp.Missing(env, with); len(m) != 0 {
		t.Fatalf("Missing with password: %+v", m)
	}
	// The API shows the stored password as set, and a save that leaves the
	// password blank keeps it, so other fields can change.
	red, set := sp.Redact(with)
	if red.Values[KeyPassword] != "" || !set[KeyPassword] {
		t.Fatalf("Redact: %+v %v", red, set)
	}
	upd := mode.Settings{Values: map[string]string{KeyDMRID: "7654321", KeyPassword: ""}}
	if err := sp.CheckEnv(env, sp.Merge(with, upd)); err != nil {
		t.Fatalf("save with a stored password: %v", err)
	}
	if err := sp.CheckEnv(env, sp.Merge(without, upd)); err == nil {
		t.Fatal("save with no password was accepted")
	}
}
