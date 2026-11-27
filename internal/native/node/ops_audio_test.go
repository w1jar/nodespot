package node

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/mode/modetest"
	"github.com/w1jar/nodespot/internal/native/radio"
	"github.com/w1jar/nodespot/internal/native/radio/sa818"
)

// Changing a module value (filters, volume, tail) programs the module when
// the config is saved; other audio settings send nothing.
func TestSA818ModuleValuesProgram(t *testing.T) {
	var sent []sa818.Settings
	state := filepath.Join(t.TempDir(), "sa818.json")
	cfg := quietConfig()
	cfg.Modes = nil
	cfg.Station.Radio = RadioConfig{Preset: "shari", FrequencyMHz: 446.1}
	e, err := New(cfg, Options{
		Radio: radio.NewFake(), RadioInfo: openInfo(radio.PresetSHARI, false), Now: time.Now,
		RadioConfig: radio.Config{SA818StatePath: state},
		Program:     func(_ radio.Config, s sa818.Settings) error { sent = append(sent, s); return nil },
		Build:       func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) { return modetest.New(n), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if d := e.RadioInfo(); d.SA818Filters != nil {
		t.Fatalf("filters shown with no state file: %+v", d.SA818Filters)
	}
	st := e.Config().Station
	st.Radio.SA818Filter = &SA818Filter{Emphasis: bp(false), HighPass: bp(false)}
	st.Advanced.RxGainDB, st.Advanced.PLFilter = 4, bp(true)
	if err := e.SaveStation(st); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("saving a filter change sent %d programs to the SA818", len(sent))
	}
	// Audio settings that are not module values send nothing.
	st = e.Config().Station
	st.Advanced.RxGainDB = 5
	if err := e.SaveStation(st); err != nil || len(sent) != 1 {
		t.Fatalf("save: %v, %d sent", err, len(sent))
	}
	st = e.Config().Station
	v := 3
	st.Radio.SA818Volume, st.Radio.SA818Tail = &v, bp(true)
	if err := e.SaveStation(st); err != nil || len(sent) != 2 {
		t.Fatalf("save: %v, %d sent", err, len(sent))
	}
	if sent[1].Volume != 3 || sent[1].Tail == nil || !*sent[1].Tail {
		t.Fatalf("programmed %+v", sent[1])
	}
	if err := e.ProgramRadio(); err != nil || len(sent) != 3 {
		t.Fatalf("program: %v, %d sent", err, len(sent))
	}
	sent[0] = sent[2]
	if f := sent[0].Filters; f.Emphasis || f.HighPass || !f.LowPass {
		t.Fatalf("programmed filters %+v", f)
	}
	// The state file records what was programmed, and the info shows it.
	if err := sa818.SaveState(state, sa818.State{AppliedAt: time.Unix(1e9, 0), Settings: sent[0]}); err != nil {
		t.Fatal(err)
	}
	d := e.RadioInfo()
	if p := d.SA818Programmed; p == nil || p.Volume != 3 || p.Squelch != 4 || p.Tail == nil || !*p.Tail || p.Filters.LowPass != true {
		t.Fatalf("programmed %+v", p)
	}
	// At boot nothing is programmed when the state matches, and one change
	// in any module value programs again.
	n := len(sent)
	if err := e.ProgramIfChanged(); err != nil || len(sent) != n {
		t.Fatalf("boot with matching state: %v, %d sent", err, len(sent)-n)
	}
	for _, tc := range []struct {
		name string
		mod  func(*sa818.Settings)
	}{
		{"volume", func(s *sa818.Settings) { s.Volume = 2 }},
		{"tail", func(s *sa818.Settings) { s.Tail = nil }},
		{"filters", func(s *sa818.Settings) { s.Filters.Emphasis = !s.Filters.Emphasis }},
	} {
		old := sent[0]
		tc.mod(&old)
		if err := sa818.SaveState(state, sa818.State{AppliedAt: time.Unix(1e9, 0), Settings: old}); err != nil {
			t.Fatal(err)
		}
		n = len(sent)
		if err := e.ProgramIfChanged(); err != nil || len(sent) != n+1 {
			t.Fatalf("boot after %s change: %v, %d sent", tc.name, err, len(sent)-n)
		}
	}

	if d.SA818Filters == nil || d.SA818Filters.Emphasis || d.SA818Filters.HighPass || !d.SA818Filters.LowPass {
		t.Fatalf("shown filters %+v", d.SA818Filters)
	}
}

func TestRxBoost(t *testing.T) {
	fake := radio.NewFake()
	cfg := quietConfig()
	cfg.Modes = nil
	e, err := New(cfg, Options{
		Radio: fake, RadioInfo: openInfo(radio.PresetURI, false), Now: time.Now,
		Build: func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) { return modetest.New(n), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	on := true
	// A chip with no boost control: saved, nothing to apply, no error.
	if err := e.Tune(nil, nil, &on); err != nil {
		t.Fatal(err)
	}
	if l := e.Levels(); l.BoostAvailable || l.Boost {
		t.Fatalf("levels %+v", l)
	}
	if b := e.Config().Station.Radio.RxBoost; b == nil || !*b {
		t.Fatal("boost not saved")
	}
	// A chip with one: switched, and reported.
	fake.SetBoostAvailable(true)
	if err := e.Tune(nil, nil, &on); err != nil {
		t.Fatal(err)
	}
	if l := e.Levels(); !l.BoostAvailable || !l.Boost {
		t.Fatalf("levels %+v", l)
	}
	off := false
	if err := e.Tune(nil, nil, &off); err != nil {
		t.Fatal(err)
	}
	if l := e.Levels(); l.Boost {
		t.Fatalf("levels %+v", l)
	}
}
