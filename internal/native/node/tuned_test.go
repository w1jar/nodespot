package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/w1jar/nodespot/internal/native/mode"
	"github.com/w1jar/nodespot/internal/native/radio"
)

func f64(v float64) *float64 { return &v }

// tuneRig starts an engine on a fake chip with capture steps, on the radio
// interface usb, with its config saved at path.
func tuneRig(t *testing.T, cfg *Config, path, usb string) (*Engine, *radio.Fake) {
	t.Helper()
	r := radio.NewFake()
	r.SetCaptureSteps(gainTable(35))
	e, err := New(cfg, Options{
		Radio: r, ConfigPath: path,
		RadioInfo: radio.Info{Present: true, Detected: true, USBID: usb, Interface: radio.Interface{Preset: "shari"}},
		Now:       func() time.Time { return t0 },
		Build:     func(n string, _ mode.Env, _ mode.Settings) (mode.Mode, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, r
}

func freshConfig() *Config {
	return &Config{Version: ConfigVersion, Station: Station{Callsign: "N0CALL"}, Modes: map[string]mode.Settings{}}
}

func TestTuningFreshIsUntuned(t *testing.T) {
	e, _ := tuneRig(t, freshConfig(), "", "0d8c:0012")
	if !e.Status().Radio.NeedsTuning {
		t.Fatal("a fresh config must need tuning")
	}
	if e.Config().Station.AudioTuned != nil {
		t.Errorf("a fresh config has no record: %+v", e.Config().Station.AudioTuned)
	}
}

func TestTuningNoControlNoRadio(t *testing.T) {
	r := radio.NewFake()
	e, err := New(freshConfig(), Options{Radio: r, RadioInfo: radio.Info{Present: true, Interface: radio.Interface{Preset: "shari"}}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status().Radio.NeedsTuning {
		t.Error("no capture control: no banner")
	}
	e2, _ := New(freshConfig(), Options{Radio: r, RadioAbsent: true})
	if e2.Status().Radio.NeedsTuning {
		t.Error("no radio: no banner")
	}
}

func TestTuningLegacyMigration(t *testing.T) {
	for name, mod := range map[string]func(*Station){
		"receive level":  func(s *Station) { s.Radio.RxVolume = f64(0.5) },
		"transmit level": func(s *Station) { s.Radio.TxVolume = f64(0.5) },
		"rx gain":        func(s *Station) { s.Advanced.RxGainDB = 3 },
		"agc target":     func(s *Station) { s.Advanced.RxAGCTarget = -20 },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node.json")
			c := freshConfig()
			mod(&c.Station)
			e, _ := tuneRig(t, c, path, "0d8c:0012")
			if e.Status().Radio.NeedsTuning {
				t.Fatal("a configured node must not need tuning")
			}
			rec := e.Config().Station.AudioTuned
			if rec == nil || rec.Dismissed || rec.Interface != "shari:0d8c:0012" || !rec.At.Equal(t0) {
				t.Fatalf("record %+v", rec)
			}
			saved, err := LoadConfig(path)
			if err != nil || saved.Station.AudioTuned == nil {
				t.Fatalf("not persisted: %v %+v", err, saved)
			}
		})
	}
}

func TestTuningHardwareChangeResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	e, _ := tuneRig(t, freshConfig(), path, "0d8c:0012")
	if err := e.DismissTuning(); err != nil {
		t.Fatal(err)
	}
	if e.Status().Radio.NeedsTuning {
		t.Fatal("dismissed")
	}
	saved, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := tuneRig(t, saved, "", "0d8c:013c")
	if !e2.Status().Radio.NeedsTuning {
		t.Error("another chip must need tuning again, a dismissal included")
	}
	e3, _ := tuneRig(t, saved, "", "0d8c:0012")
	if e3.Status().Radio.NeedsTuning {
		t.Error("the same chip stays tuned")
	}
}

func TestTuningManualLevelSetsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	e, _ := tuneRig(t, freshConfig(), path, "0d8c:0012")
	if err := e.Tune(nil, f64(0.4), nil); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Radio.NeedsTuning {
		t.Fatal("a transmit level is not receive tuning")
	}
	if err := e.Tune(f64(0.5), nil, nil); err != nil {
		t.Fatal(err)
	}
	if e.Status().Radio.NeedsTuning {
		t.Fatal("receive level set")
	}
	saved, _ := LoadConfig(path)
	if rec := saved.Station.AudioTuned; rec == nil || rec.Dismissed || rec.Interface != "shari:0d8c:0012" {
		t.Errorf("saved %+v", rec)
	}
}

func TestTuningStationSaveKeepsAndSets(t *testing.T) {
	e, _ := tuneRig(t, freshConfig(), "", "0d8c:0012")
	st := e.Config().Station
	if err := e.SaveStation(st); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Radio.NeedsTuning {
		t.Fatal("saving other station settings must not tune")
	}
	st.Radio.RxVolume = f64(0.6)
	if err := e.SaveStation(st); err != nil {
		t.Fatal(err)
	}
	if e.Status().Radio.NeedsTuning {
		t.Fatal("a changed receive level tunes")
	}
	// A page holding an older station (no record) cannot erase it.
	st.AudioTuned = nil
	if err := e.SaveStation(st); err != nil {
		t.Fatal(err)
	}
	if e.Status().Radio.NeedsTuning || e.Config().Station.AudioTuned == nil {
		t.Fatal("the record was lost")
	}
}

func TestTuningDismiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	e, _ := tuneRig(t, freshConfig(), path, "0d8c:0012")
	if err := e.DismissTuning(); err != nil {
		t.Fatal(err)
	}
	if e.Status().Radio.NeedsTuning {
		t.Fatal("still needs tuning")
	}
	saved, _ := LoadConfig(path)
	if rec := saved.Station.AudioTuned; rec == nil || !rec.Dismissed || rec.At.IsZero() {
		t.Errorf("saved %+v", rec)
	}
	if saved.Station.Radio.RxVolume != nil {
		t.Error("dismiss must not set a level")
	}
}

func TestAutosetSuccessMarksTuned(t *testing.T) {
	g := autoRig(t, 35)
	if !g.e.Status().Radio.NeedsTuning {
		t.Fatal("untuned to start")
	}
	if _, err := g.e.AutosetRx(); err != nil {
		t.Fatal(err)
	}
	g.talkFor(6*time.Second, 328)
	if st := g.autosetWait(); st.State != AutosetDone {
		t.Fatalf("auto-set %+v", st)
	}
	if g.e.Status().Radio.NeedsTuning || g.e.Config().Station.AudioTuned == nil {
		t.Error("auto-set success must record the tuning")
	}
}

func TestAutosetFailureLeavesUntuned(t *testing.T) {
	g := autoRig(t, 35)
	g.e.AutosetRx()
	g.run(16 * time.Second)
	if !g.e.Status().Radio.NeedsTuning {
		t.Error("a failed auto-set must not tune")
	}
}
