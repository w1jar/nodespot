package dmr

// RX parses the bursts of received calls on one slot. Call Reset at the
// start of every new stream (the networks see it as a new stream ID). Not
// safe for concurrent use.
type RX struct {
	lc     LC
	haveLC bool
	frags  [4][4]byte
	next   int // next expected embedded fragment, -1 = waiting for LCSS First
}

// NewRX returns a parser.
func NewRX() *RX { return &RX{next: -1} }

// Reset forgets the current call.
func (r *RX) Reset() { *r = RX{next: -1} }

// LC returns the LC of the current call, if one has been decoded.
func (r *RX) LC() (LC, bool) { return r.lc, r.haveLC }

// Push parses one burst with the frame type and voice sequence / data type
// the network sent with it.
//
// Voice LC headers and terminators give the LC directly; voice bursts give
// three AMBE frames each, and the embedded LC of bursts B-E gives the LC to
// a receiver that missed the header (late entry). Only group and private
// voice LCs (FLCO 0 and 3) are reported; other embedded LCs (talker alias,
// GPS) are ignored. A terminator sets End and resets the parser.
func (r *RX) Push(ft FrameType, dtypeVSeq uint8, b *Burst) RXResult {
	var res RXResult
	switch ft {
	case FrameDataSync:
		dt := DataType(dtypeVSeq & 0xF)
		switch dt {
		case DataVoiceLCHeader, DataTerminatorWithLC:
			if lc, err := DecodeLCBurst(b, dt); err == nil && lc.IsVoice() {
				r.setLC(lc, &res)
				res.LCFromHeader = true
			}
			if dt == DataTerminatorWithLC {
				res.End = true
				r.Reset()
			}
		}
	case FrameVoiceSync, FrameVoice:
		res.Voice, res.FECErrors = b.Voice()
		res.HasVoice = true
		if ft == FrameVoiceSync || dtypeVSeq == 0 {
			r.next = -1
			break
		}
		emb, ok := b.EMB()
		if !ok {
			r.next = -1
			break
		}
		frag := b.Embedded()
		switch {
		case emb.LCSS == LCSSFirst:
			r.frags[0] = frag
			r.next = 1
		case emb.LCSS == LCSSContinue && (r.next == 1 || r.next == 2):
			r.frags[r.next] = frag
			r.next++
		case emb.LCSS == LCSSLast && r.next == 3:
			r.frags[3] = frag
			r.next = -1
			if lc, ok := DecodeEmbeddedLC(r.frags); ok && lc.IsVoice() {
				r.setLC(lc, &res)
			}
		default:
			r.next = -1
		}
	}
	return res
}

func (r *RX) setLC(lc LC, res *RXResult) {
	res.NewCall = !r.haveLC
	res.LC, res.LCValid = lc, true
	r.lc, r.haveLC = lc, true
}
