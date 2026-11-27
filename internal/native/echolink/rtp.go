package echolink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"

	"github.com/w1jar/nodespot/internal/native/gsm"
)

// Audio-port packets. EchoLink audio is RTP with version bits 3 (first byte
// 0xC0) and payload type 3 (GSM), a zero SSRC and four 33-byte GSM frames.
// Text packets share the port and start with "oNDATA": "oNDATA\r" followed
// by station info lines separated by "\r", or "oNDATA" followed by a chat
// line "CALLSIGN>text".

const (
	rtpHeaderLen  = 12
	rtpByte0      = 0xC0 // version bits 3, no padding, extension or CSRCs
	payloadGSM    = 3
	audioPayload  = FramesPerPacket * gsm.FrameBytes
	ndataPrefix   = "oNDATA"
	maxTextPacket = 1400
)

var errNotAudio = errors.New("echolink: not a GSM audio packet")

// buildAudio builds one RTP packet carrying the given packed GSM frames.
func buildAudio(seq uint16, ts uint32, frames []byte) []byte {
	p := make([]byte, rtpHeaderLen+len(frames))
	p[0] = rtpByte0
	p[1] = payloadGSM
	binary.BigEndian.PutUint16(p[2:], seq)
	binary.BigEndian.PutUint32(p[4:], ts)
	// SSRC stays zero, as EchoLink clients send it.
	copy(p[rtpHeaderLen:], frames)
	return p
}

// parseAudio checks an RTP packet and returns its sequence number and GSM
// payload. Any version bits are accepted since peers differ; the payload
// must be one or more whole GSM frames.
func parseAudio(p []byte) (seq uint16, payload []byte, err error) {
	if len(p) < rtpHeaderLen {
		return 0, nil, errNotAudio
	}
	if p[1]&0x7f != payloadGSM {
		return 0, nil, errNotAudio
	}
	hl := rtpHeaderLen + 4*int(p[0]&0x0f)
	if p[0]&0x10 != 0 { // header extension
		if len(p) < hl+4 {
			return 0, nil, errNotAudio
		}
		hl += 4 + 4*int(binary.BigEndian.Uint16(p[hl+2:]))
	}
	if len(p) < hl {
		return 0, nil, errNotAudio
	}
	payload = p[hl:]
	if p[0]&0x20 != 0 && len(payload) > 0 { // padding
		pad := int(payload[len(payload)-1])
		if pad > len(payload) {
			return 0, nil, errNotAudio
		}
		payload = payload[:len(payload)-pad]
	}
	if len(payload) == 0 || len(payload)%gsm.FrameBytes != 0 {
		return 0, nil, errNotAudio
	}
	return binary.BigEndian.Uint16(p[2:]), payload, nil
}

// isText reports whether p is an oNDATA text packet.
func isText(p []byte) bool { return bytes.HasPrefix(p, []byte(ndataPrefix)) }

// textPacket is a parsed oNDATA packet.
type textPacket struct {
	info     bool   // station info rather than chat
	text     string // info text with "\n" line breaks, or the chat message
	callsign string // chat sender, if the line had one
}

// parseText parses an oNDATA packet.
func parseText(p []byte) textPacket {
	body := p[len(ndataPrefix):]
	if i := bytes.IndexByte(body, 0); i >= 0 {
		body = body[:i]
	}
	if len(body) > 0 && body[0] == '\r' {
		s := strings.ReplaceAll(string(body[1:]), "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
		return textPacket{info: true, text: strings.TrimRight(s, "\n")}
	}
	s := strings.TrimRight(string(body), "\r\n")
	if i := strings.IndexByte(s, '>'); i > 0 && i <= 20 && !strings.ContainsAny(s[:i], " \t") {
		return textPacket{callsign: strings.ToUpper(s[:i]), text: s[i+1:]}
	}
	return textPacket{text: s}
}

// buildInfo builds a station info packet from text with "\n" line breaks.
func buildInfo(text string) []byte {
	s := strings.ReplaceAll(text, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\r")
	p := append([]byte(ndataPrefix+"\r"), s...)
	if len(p) > maxTextPacket-1 {
		p = p[:maxTextPacket-1]
	}
	return append(p, 0)
}

// buildChat builds a chat packet from callsign and a one-line message.
func buildChat(callsign, msg string) []byte {
	msg = strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
	p := []byte(ndataPrefix + callsign + ">" + msg)
	if len(p) > maxTextPacket-3 {
		p = p[:maxTextPacket-3]
	}
	return append(p, '\r', '\n', 0)
}
