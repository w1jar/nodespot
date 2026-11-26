package mode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/audio"
)

func testSpec() Spec {
	return Spec{
		Name: "test", Title: "Test", TargetLabel: "Thing",
		Fields: []Field{
			{Key: "callsign", Label: "Callsign", Kind: FieldText, Required: true},
			{Key: "password", Label: "Password", Kind: FieldPassword, Secret: true},
			{Key: "port", Label: "Port", Kind: FieldNumber, Default: "4569"},
			{Key: "on", Label: "On", Kind: FieldBool},
			{Key: "pick", Label: "Pick", Kind: FieldSelect, Options: []string{"a", "b"}},
		},
		Validate: func(s Settings) error {
			if s.Get("callsign", "") == "BAD" {
				return errors.New("callsign is bad")
			}
			return nil
		},
		New: func(Env, Settings) (Mode, error) { return nil, errors.New("no") },
	}
}

func TestCheck(t *testing.T) {
	sp := testSpec()
	tests := []struct {
		name string
		vals map[string]string
		want []string // substrings; nil = no error
	}{
		{"ok", map[string]string{"callsign": "N0CALL"}, nil},
		{"missing", map[string]string{}, []string{"Callsign is required"}},
		{"number", map[string]string{"callsign": "N0CALL", "port": "x"}, []string{"Port must be a whole number"}},
		{"bool", map[string]string{"callsign": "N0CALL", "on": "maybe"}, []string{"On must be true or false"}},
		{"select", map[string]string{"callsign": "N0CALL", "pick": "c"}, []string{"Pick must be one of a, b"}},
		{"unknown", map[string]string{"callsign": "N0CALL", "zap": "1"}, []string{`unknown setting "zap"`}},
		{"all", map[string]string{"port": "x", "on": "?"}, []string{"Callsign is required", "Port", "On"}},
		{"validate", map[string]string{"callsign": "BAD"}, []string{"Test: callsign is bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sp.Check(Settings{Values: tt.vals})
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Check: no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestRedactMerge(t *testing.T) {
	sp := testSpec()
	old := Settings{Values: map[string]string{"callsign": "N0CALL", "password": "hunter2"},
		Favorites: []Target{{ID: "1999"}}}
	red, set := sp.Redact(old)
	if _, ok := red.Values["password"]; ok || !set["password"] {
		t.Fatalf("Redact: %+v %v", red.Values, set)
	}
	if old.Values["password"] != "hunter2" {
		t.Fatal("Redact changed its input")
	}
	tests := []struct {
		name string
		upd  Settings
		pass string
		favs int
	}{
		{"blank keeps", Settings{Values: map[string]string{"callsign": "N1CALL", "password": ""}}, "hunter2", 1},
		{"absent keeps", Settings{Values: map[string]string{"callsign": "N1CALL"}}, "hunter2", 1},
		{"new", Settings{Values: map[string]string{"password": "new"}, Favorites: []Target{}}, "new", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sp.Merge(old, tt.upd)
			if got.Values["password"] != tt.pass || len(got.Favorites) != tt.favs {
				t.Fatalf("Merge: %+v", got)
			}
		})
	}
	if s := sp.Secrets(old); len(s) != 1 || s[0] != "hunter2" {
		t.Fatalf("Secrets: %v", s)
	}
}

func TestSettingsHelpers(t *testing.T) {
	s := Settings{Values: map[string]string{"n": " 12 ", "b": "true", "x": "zz"},
		Reflectors: []Target{{ID: "Alpha", Host: "192.0.2.1", Port: 42000}}}
	if n, err := s.Int("n", 0); err != nil || n != 12 {
		t.Fatalf("Int: %d %v", n, err)
	}
	if n, _ := s.Int("none", 7); n != 7 {
		t.Fatalf("Int default: %d", n)
	}
	if _, err := s.Int("x", 0); err == nil {
		t.Fatal("Int: no error")
	}
	if b, _ := s.Bool("b", false); !b {
		t.Fatal("Bool")
	}
	if r, ok := s.Reflector("alpha"); !ok || r.Port != 42000 {
		t.Fatalf("Reflector: %+v %v", r, ok)
	}
}

var regRuns int

func TestRegistry(t *testing.T) {
	sp := testSpec()
	regRuns++
	sp.Name = fmt.Sprintf("zz-registry-test-%d", regRuns)
	Register(sp)
	if _, ok := Lookup(sp.Name); !ok {
		t.Fatal("Lookup")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("duplicate Register did not panic")
			}
		}()
		Register(sp)
	}()
	if _, err := Build(sp.Name, Env{}, Settings{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("Build: %v", err)
	}
	if _, err := Build(sp.Name, Env{}, Settings{Values: map[string]string{"callsign": "N0CALL"}}); err == nil || !strings.Contains(err.Error(), "Test: no") {
		t.Fatalf("Build: %v", err)
	}
	if _, err := Build("missing-mode", Env{}, Settings{}); err == nil {
		t.Fatal("Build missing: no error")
	}
	specs := Specs()
	if specs[len(specs)-1].Name != sp.Name {
		t.Fatalf("Specs order: %v", specs)
	}
}

type nopMode struct{ Base }

func (*nopMode) Start(context.Context) error           { return nil }
func (*nopMode) Connect(context.Context, Target) error { return nil }
func (*nopMode) Disconnect(context.Context) error      { return nil }
func (*nopMode) Stop() error                           { return nil }
func (*nopMode) Send(audio.Frame)                      {}
func (*nopMode) EndTx()                                {}

var _ Mode = (*nopMode)(nil)

func TestBase(t *testing.T) {
	now := time.Unix(1000, 0)
	var b Base
	b.Init("test", "Test", func() time.Time { return now })
	b.SetTarget(&Target{ID: "1999"})
	b.SetState(StateConnecting, "linking", "")
	b.SetState(StateError, "", "refused")
	b.TalkStart(Talker{Callsign: "N0CALL"})
	b.TalkStart(Talker{Callsign: "N0CALL", Name: "Pat"}) // same talker: no event
	b.UpdateTalker("N0CALL", "Pat", "Anytown")
	b.TalkStart(Talker{Callsign: "N1CALL"}) // ends N0CALL
	b.TalkEnd()
	var kinds []string
	for len(b.Events()) > 0 {
		ev := <-b.Events()
		if ev.Mode != "test" || !ev.At.Equal(now) {
			t.Fatalf("event %+v", ev)
		}
		s := string(ev.Kind)
		if ev.Talker != nil {
			s += ":" + ev.Talker.Callsign
		}
		kinds = append(kinds, s)
	}
	want := "state,state,error,talk_start:N0CALL,talk_start:N0CALL,talk_end:N0CALL,talk_start:N1CALL,talk_end:N1CALL"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	st := b.Status()
	if st.State != StateError || st.Error != "refused" || st.Target.ID != "1999" || st.Talker != nil {
		t.Fatalf("status %+v", st)
	}
	for i := 0; i < RecvBuffer+5; i++ {
		var f audio.Frame
		f[0] = int16(i)
		b.Deliver(f)
	}
	if len(b.Recv()) != RecvBuffer {
		t.Fatalf("recv len %d", len(b.Recv()))
	}
	if f := <-b.Recv(); f[0] != 5 {
		t.Fatalf("oldest frame %d, want 5 (dropped oldest)", f[0])
	}
	b.Flush()
	if len(b.Recv()) != 0 {
		t.Fatal("Flush")
	}
}
