package homebrew

import (
	"errors"
	"strconv"
	"time"

	"github.com/w1jar/nodespot/internal/dv/ctl"
	"github.com/w1jar/nodespot/internal/dv/dmr"
	"github.com/w1jar/nodespot/internal/dv/network"
)

// handleDMRD turns a received DMRD packet into Sink callbacks.
func (n *Net) handleDMRD(p []byte) {
	d, ok := parseDMRD(p)
	if !ok {
		return
	}
	n.mu.Lock()
	sink := n.sink
	if sink == nil || (d.Slot != n.slot && !n.rxAnySlot) {
		n.mu.Unlock()
		return
	}
	if n.dropUntunedLocked(d) {
		// a group call of another talkgroup (the master keeps sending the
		// previous one for a while): not heard, and it must not take the
		// per-call slot lock either
		n.mu.Unlock()
		return
	}
	if n.rxCall != nil && n.rxCall.slot != d.Slot {
		// a call is in progress on the other slot: a stray packet must not
		// end or replace it
		n.mu.Unlock()
		return
	}
	var hdr *network.CallHeader
	if n.rxCall == nil || n.rxCall.stream != d.Stream {
		old := n.rxCall
		n.nextCall++
		n.rxCall = &rxState{stream: d.Stream, slot: d.Slot, id: n.nextCall, rx: dmr.NewRX()}
		if old != nil {
			n.mu.Unlock()
			sink.End(old.id)
			n.mu.Lock()
			if n.rxCall == nil || n.rxCall.stream != d.Stream {
				n.mu.Unlock()
				return
			}
		}
		if d.FrameTyp == ftDataSync && d.DType == dtTerminator {
			// stray terminator of a call we never heard
			n.rxCall = nil
			n.mu.Unlock()
			return
		}
		hdr = &network.CallHeader{Call: n.rxCall.id, Src: strconv.FormatUint(uint64(d.Src), 10),
			SrcID: d.Src, TG: d.Dst, Slot: d.Slot, Private: d.Private}
	}
	rc := n.rxCall
	end := d.FrameTyp == ftDataSync && d.DType == dtTerminator
	burst := dmr.Burst(d.Payload)
	res := rc.rx.Push(dmr.FrameType(d.FrameTyp), uint8(d.DType), &burst)
	isVoice := res.HasVoice
	seq := rc.seq
	if isVoice {
		rc.seq += 3
	}
	if end || res.End {
		end = true
		n.rxCall = nil
		if n.rxTimer != nil {
			n.rxTimer.Stop()
		}
	} else {
		n.armRXLocked(rc)
	}
	n.mu.Unlock()
	if hdr != nil {
		sink.Header(*hdr)
	}
	for i := 0; isVoice && i < 3; i++ {
		sink.Voice(network.VoiceFrame{Call: rc.id, Seq: seq + uint32(i), Data: append([]byte(nil), res.Voice[i][:]...)})
	}
	if end {
		sink.End(rc.id)
	}
}

// dropUntunedLocked reports whether d is a group call to drop: DMR - Other
// tuned to a talkgroup hears only that talkgroup. Private calls and any
// other mode or an untuned network pass.
func (n *Net) dropUntunedLocked(d dmrd) bool {
	t := n.target
	if n.mode != ctl.ModeDMR || t == nil || t.Private || t.TG == 0 {
		return false
	}
	return !d.Private && d.Dst != t.TG
}

// armRXLocked (re)starts the lost-end timer for rc.
func (n *Net) armRXLocked(rc *rxState) {
	if n.rxTimer != nil {
		n.rxTimer.Stop()
	}
	stream := rc.stream
	n.rxTimer = time.AfterFunc(n.rxTO, func() {
		n.mu.Lock()
		if n.rxCall == nil || n.rxCall.stream != stream {
			n.mu.Unlock()
			return
		}
		n.mu.Unlock()
		n.endRX(stream)
	})
}

// endRX ends the current received call (any if stream==0, else only that
// stream) and tells the sink.
func (n *Net) endRX(stream uint32) {
	n.mu.Lock()
	rc := n.rxCall
	sink := n.sink
	if rc == nil || (stream != 0 && rc.stream != stream) {
		n.mu.Unlock()
		return
	}
	n.rxCall = nil
	if n.rxTimer != nil {
		n.rxTimer.Stop()
	}
	n.mu.Unlock()
	if sink != nil {
		sink.End(rc.id)
	}
}

// ---- TX ----

func (n *Net) sendFrames(tx txState, slot int, fs []dmr.TXFrame) error {
	for _, f := range fs {
		d := dmrd{Src: tx.src, Dst: tx.dst, Slot: slot, Private: tx.private, FrameTyp: int(f.FrameType),
			DType: int(f.DTypeVSeq), Stream: tx.stream, Payload: f.Burst}
		if err := n.sendDMRD(d); err != nil {
			return err
		}
	}
	return nil
}

func (n *Net) newTXLocked(call uint64, src, dst uint32, private bool) *txState {
	return &txState{call: call, stream: n.newStream(), src: src, dst: dst, private: private,
		tx: dmr.NewTX(dmr.TXConfig{Src: src, Dst: dst, Private: private, CC: uint8(n.cc & 15)})}
}

// StartTX sends the voice LC header(s) and opens a new stream.
func (n *Net) StartTX(h network.CallHeader) error {
	n.mu.Lock()
	if err := n.failIfNotReady(); err != nil {
		n.mu.Unlock()
		return err
	}
	src := h.SrcID
	if src == 0 {
		src = n.srcDefault
	}
	if src == 0 {
		src = n.rid
	}
	dst := h.TG
	private := h.Private
	if dst == 0 && n.target != nil {
		dst, private = n.target.TG, n.target.Private
	}
	tx := n.newTXLocked(h.Call, src, dst, private)
	n.txCall = tx
	slot := n.slot
	fs := tx.tx.Start()
	n.mu.Unlock()
	return n.sendFrames(*tx, slot, fs)
}

// WriteVoice feeds the superframe builder; a burst goes out per three frames.
func (n *Net) WriteVoice(f network.VoiceFrame) error {
	if len(f.Data) != 7 {
		return errors.New("homebrew: need 7-byte AMBE frame")
	}
	n.mu.Lock()
	tx := n.txCall
	if tx == nil || tx.call != f.Call {
		n.mu.Unlock()
		return errors.New("homebrew: no tx call")
	}
	var a dmr.AMBE
	copy(a[:], f.Data)
	fs := tx.tx.Write(a)
	slot := n.slot
	n.mu.Unlock()
	return n.sendFrames(*tx, slot, fs)
}

// EndTX flushes a partial burst (the builder pads with silence) and sends
// the terminator.
func (n *Net) EndTX(call uint64) error {
	n.mu.Lock()
	tx := n.txCall
	if tx == nil || tx.call != call {
		n.mu.Unlock()
		return nil
	}
	n.txCall = nil
	fs := tx.tx.End()
	slot := n.slot
	n.mu.Unlock()
	return n.sendFrames(*tx, slot, fs)
}
