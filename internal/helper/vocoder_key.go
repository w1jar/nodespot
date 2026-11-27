package helper

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // the OpenPGP v4 fingerprint is defined as SHA-1
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The the software AMBE vocoder package repository keyring is checked with the Go standard
// library only (no gpg dependency): the file is dearmored if needed and parsed
// as OpenPGP packets, and the SHA-1 fingerprint of every primary key must match
// the pinned one exactly.

// KeyringFingerprints returns the upper-case v4 fingerprints of the primary
// public keys of a keyring (binary or ASCII armored), sorted. Partial-length
// and indeterminate-length packets, secret keys and non-v4 keys are refused.
func KeyringFingerprints(data []byte) ([]string, error) {
	bin, err := dearmor(data)
	if err != nil {
		return nil, err
	}
	return primaryFingerprints(bin)
}

// VerifyVocoderKeyring checks that the keyring holds exactly the pinned key and
// returns it in binary form (what apt's signed-by wants). Anything else is
// ErrVocoderKeyMismatch.
func VerifyVocoderKeyring(data []byte) ([]byte, error) {
	bin, err := dearmor(data)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrVocoderKeyMismatch, err)
	}
	fps, err := primaryFingerprints(bin)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrVocoderKeyMismatch, err)
	}
	if len(fps) != 1 || fps[0] != vocoderPinnedFP {
		return nil, fmt.Errorf("%w (got %s)", ErrVocoderKeyMismatch, strings.Join(fps, ","))
	}
	return bin, nil
}

const armorHeader = "-----BEGIN PGP PUBLIC KEY BLOCK-----"

// dearmor returns binary keyring data; binary input is returned as is.
func dearmor(data []byte) ([]byte, error) {
	trim := bytes.TrimSpace(data)
	if !bytes.HasPrefix(trim, []byte("-----BEGIN PGP")) {
		return data, nil
	}
	if !bytes.HasPrefix(trim, []byte(armorHeader)) {
		return nil, errors.New("not a public key block")
	}
	lines := strings.Split(strings.ReplaceAll(string(trim), "\r\n", "\n"), "\n")
	i := 1
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" { // armor headers
		i++
	}
	var body strings.Builder
	crc := ""
	end := false
	for _, l := range lines[i+1:] {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "-----END PGP PUBLIC KEY BLOCK-----"):
			end = true
		case strings.HasPrefix(l, "="):
			crc = l[1:]
		case end:
			return nil, errors.New("data after the end of the armor")
		default:
			body.WriteString(l)
		}
		if end {
			break
		}
	}
	if !end {
		return nil, errors.New("armor is not terminated")
	}
	bin, err := base64.StdEncoding.DecodeString(body.String())
	if err != nil {
		return nil, fmt.Errorf("armor: %w", err)
	}
	if crc != "" {
		want, err := base64.StdEncoding.DecodeString(crc)
		if err != nil || len(want) != 3 {
			return nil, errors.New("armor: bad checksum field")
		}
		got := crc24(bin)
		if want[0] != byte(got>>16) || want[1] != byte(got>>8) || want[2] != byte(got) {
			return nil, errors.New("armor: checksum mismatch")
		}
	}
	return bin, nil
}

func crc24(b []byte) uint32 {
	crc := uint32(0xB704CE)
	for _, c := range b {
		crc ^= uint32(c) << 16
		for i := 0; i < 8; i++ {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	return crc & 0xFFFFFF
}

func primaryFingerprints(bin []byte) ([]string, error) {
	var fps []string
	for len(bin) > 0 {
		tag, body, rest, err := nextPacket(bin)
		if err != nil {
			return nil, err
		}
		bin = rest
		switch tag {
		case 5:
			return nil, errors.New("secret key in the keyring")
		case 6:
			if len(body) < 6 || body[0] != 4 {
				return nil, errors.New("not a version 4 key")
			}
			h := sha1.New() //nolint:gosec
			h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
			h.Write(body)
			fps = append(fps, strings.ToUpper(hex.EncodeToString(h.Sum(nil))))
		}
	}
	if len(fps) == 0 {
		return nil, errors.New("no key in the keyring")
	}
	sort.Strings(fps)
	return fps, nil
}

// nextPacket parses one OpenPGP packet header (RFC 4880 4.2).
func nextPacket(b []byte) (tag int, body, rest []byte, err error) {
	if len(b) < 2 || b[0]&0x80 == 0 {
		return 0, nil, nil, errors.New("bad packet header")
	}
	var n, hdr int
	if b[0]&0x40 != 0 { // new format
		tag = int(b[0] & 0x3f)
		l := int(b[1])
		switch {
		case l < 192:
			n, hdr = l, 2
		case l < 224:
			if len(b) < 3 {
				return 0, nil, nil, errors.New("truncated packet")
			}
			n, hdr = (l-192)<<8+int(b[2])+192, 3
		case l == 255:
			if len(b) < 6 {
				return 0, nil, nil, errors.New("truncated packet")
			}
			n, hdr = int(b[2])<<24|int(b[3])<<16|int(b[4])<<8|int(b[5]), 6
		default:
			return 0, nil, nil, errors.New("partial-length packet")
		}
	} else { // old format
		tag = int(b[0]>>2) & 0xf
		switch b[0] & 3 {
		case 0:
			n, hdr = int(b[1]), 2
		case 1:
			if len(b) < 3 {
				return 0, nil, nil, errors.New("truncated packet")
			}
			n, hdr = int(b[1])<<8|int(b[2]), 3
		case 2:
			if len(b) < 5 {
				return 0, nil, nil, errors.New("truncated packet")
			}
			n, hdr = int(b[1])<<24|int(b[2])<<16|int(b[3])<<8|int(b[4]), 5
		default:
			return 0, nil, nil, errors.New("indeterminate-length packet")
		}
	}
	if n < 0 || hdr+n > len(b) {
		return 0, nil, nil, errors.New("truncated packet")
	}
	return tag, b[hdr : hdr+n], b[hdr+n:], nil
}

// vocoderPinnedFP is VocoderKeyFingerprint; a variable only so tests can pin a
// synthetic key.
var vocoderPinnedFP = VocoderKeyFingerprint
