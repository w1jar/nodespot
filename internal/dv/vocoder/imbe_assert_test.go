package vocoder_test

import (
	"github.com/w1jar/nodespot/internal/dv/imbe"
	"github.com/w1jar/nodespot/internal/dv/vocoder"
)

var _ vocoder.IMBE = (*imbe.Codec)(nil)
