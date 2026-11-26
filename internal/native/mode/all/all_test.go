package all

import (
	"testing"

	"github.com/w1jar/nodespot/internal/native/mode"
)

func TestAllModesRegistered(t *testing.T) {
	want := []string{mode.AllStar, mode.EchoLink, mode.BM, mode.TGIF, mode.DMR, mode.YSF, mode.P25, mode.NXDN, mode.M17, mode.EchoTest}
	specs := mode.Specs()
	if len(specs) != len(want) {
		t.Fatalf("%d modes, want %d", len(specs), len(want))
	}
	for i, sp := range specs {
		if sp.Name != want[i] {
			t.Errorf("mode %d is %s, want %s", i, sp.Name, want[i])
		}
		for _, f := range sp.Fields {
			if f.Kind == mode.FieldPassword && !f.Secret {
				t.Errorf("%s: password field %s is not secret", sp.Name, f.Key)
			}
		}
	}
}
