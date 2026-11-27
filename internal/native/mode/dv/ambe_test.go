package dv

import (
	"testing"

	"github.com/w1jar/nodespot/internal/native/mode"
)

// The modes whose network carries AMBE voice are the ones that need the
// software AMBE vocoder; P25 (IMBE) and M17 (Codec2) are computed in Go.
func TestSpecNeedsAMBE(t *testing.T) {
	want := map[string]bool{mode.BM: true, mode.TGIF: true, mode.DMR: true, mode.YSF: true, mode.NXDN: true}
	n := 0
	for _, d := range defs {
		sp := d.spec()
		if sp.NeedsAMBE != want[d.name] {
			t.Errorf("%s: NeedsAMBE = %v", d.name, sp.NeedsAMBE)
		}
		if sp.NeedsAMBE {
			n++
		}
	}
	if n != len(want) {
		t.Errorf("%d modes need AMBE, want %d", n, len(want))
	}
}
