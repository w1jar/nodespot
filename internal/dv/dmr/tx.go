package dmr

// TX builds the bursts of one transmitted call: Start, then Write for every
// 20 ms AMBE frame, then End. Not safe for concurrent use.
type TX struct {
	cfg   TXConfig
	lc    LC
	frags [4][4]byte
	voice Sync
	data  Sync
	seq   int // next voice burst, 0 (A) to 5 (F)
	buf   [3]AMBE
	n     int // frames in buf
	ended bool
}

// NewTX returns a builder for one call.
func NewTX(cfg TXConfig) *TX {
	lc := LC{FLCO: FLCOGroup, FID: cfg.FID, ServiceOptions: cfg.ServiceOptions,
		Dst: cfg.Dst & 0xFFFFFF, Src: cfg.Src & 0xFFFFFF}
	if cfg.Private {
		lc.FLCO = FLCOPrivate
	}
	t := &TX{cfg: cfg, lc: lc, frags: EmbeddedLC(lc), voice: SyncBSVoice, data: SyncBSData}
	if cfg.MSSync {
		t.voice, t.data = SyncMSVoice, SyncMSData
	}
	return t
}

// LC returns the link control of the call.
func (t *TX) LC() LC { return t.lc }

// Start returns the voice LC headers (TXConfig.Headers, default 3).
func (t *TX) Start() []TXFrame {
	n := t.cfg.Headers
	if n <= 0 {
		n = 3
	}
	h := TXFrame{FrameType: FrameDataSync, DTypeVSeq: uint8(DataVoiceLCHeader),
		Burst: lcBurst(t.lc, DataVoiceLCHeader, t.cfg.CC, t.data)}
	out := make([]TXFrame, n)
	for i := range out {
		out[i] = h
	}
	return out
}

// Write adds one AMBE frame; every third frame completes a voice burst,
// which is returned.
func (t *TX) Write(f AMBE) []TXFrame {
	if t.ended {
		return nil
	}
	t.buf[t.n] = f
	t.n++
	if t.n < 3 {
		return nil
	}
	return []TXFrame{t.flush()}
}

// End pads a partial burst with silence (and, with PadSuperframe, the rest of
// the superframe) and returns the remaining voice bursts and the terminator.
func (t *TX) End() []TXFrame {
	if t.ended {
		return nil
	}
	var out []TXFrame
	if t.n > 0 {
		for t.n < 3 {
			t.buf[t.n] = SilenceAMBE
			t.n++
		}
		out = append(out, t.flush())
	}
	if t.cfg.PadSuperframe {
		for t.seq != 0 {
			t.buf = [3]AMBE{SilenceAMBE, SilenceAMBE, SilenceAMBE}
			out = append(out, t.flush())
		}
	}
	t.ended = true
	return append(out, TXFrame{FrameType: FrameDataSync, DTypeVSeq: uint8(DataTerminatorWithLC),
		Burst: lcBurst(t.lc, DataTerminatorWithLC, t.cfg.CC, t.data)})
}

// flush emits the buffered three frames as voice burst t.seq.
func (t *TX) flush() TXFrame {
	var b Burst
	b.PutVoice(t.buf)
	fr := TXFrame{FrameType: FrameVoice, DTypeVSeq: uint8(t.seq)}
	switch t.seq {
	case 0:
		b.PutSync(t.voice)
		fr.FrameType = FrameVoiceSync
	case 5: // F: null embedded signalling
		b.PutEMB(EMB{CC: t.cfg.CC, LCSS: LCSSSingle})
		b.PutEmbedded([4]byte{})
	default: // B..E: embedded LC
		lcss := LCSSContinue
		switch t.seq {
		case 1:
			lcss = LCSSFirst
		case 4:
			lcss = LCSSLast
		}
		b.PutEMB(EMB{CC: t.cfg.CC, LCSS: lcss})
		b.PutEmbedded(t.frags[t.seq-1])
	}
	fr.Burst = b
	t.n = 0
	t.seq = (t.seq + 1) % 6
	return fr
}
