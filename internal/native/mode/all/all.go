// Package all registers every node mode. Import it for its side effect.
package all

import (
	// Each mode registers itself with internal/native/mode.
	_ "github.com/w1jar/nodespot/internal/native/mode/allstar"
	_ "github.com/w1jar/nodespot/internal/native/mode/dv"
	_ "github.com/w1jar/nodespot/internal/native/mode/echolink"
	_ "github.com/w1jar/nodespot/internal/native/mode/echotest"
)
