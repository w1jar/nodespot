// SPDX-License-Identifier: GPL-3.0-or-later

// Command c2check compares the pure-Go Codec2 3200 port with libcodec2.
// It is the Go half of .github/workflows/codec2-check.yml and is not part of
// normal CI. It never runs the reference binaries itself; the workflow does.
//
// Usage:
//
//	c2check gen    DIR NAME...   write NAME.go.bin (our encode of NAME.raw) and
//	                             NAME.godec.raw (our decode of NAME.ref.bin)
//	c2check report DIR NAME...   compare against NAME.ref.bin, NAME.refdec.raw
//	                             and NAME.xdec.raw (c2dec of NAME.go.bin); print
//	                             a markdown table; exit 1 on failure
//
// Audio is 8 kHz, 16-bit signed little-endian, mono, raw.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"

	"github.com/w1jar/nodespot/internal/dv/codec2"
)

// Pass/fail thresholds. Tune them here.
const (
	// MinDecodeSNR is the minimum SNR in dB of our decoder against libcodec2
	// when both decode the same (reference) bits.
	MinDecodeSNR = 30.0
	// MinExactFrames is the minimum fraction of encoded frames that must be
	// bit-exact with libcodec2.
	MinExactFrames = 0.90
)

const (
	fb = codec2.BytesPerFrame   // bytes per frame
	fs = codec2.SamplesPerFrame // samples per frame
)

// Bit fields of the 64-bit 3200 frame, MSB first.
var fields = []struct {
	name   string
	lo, hi int // bit range [lo, hi)
}{
	{"voicing", 0, 2}, {"Wo", 2, 9}, {"energy", 9, 14}, {"LSPs", 14, 64},
}

func main() {
	if len(os.Args) < 4 || (os.Args[1] != "gen" && os.Args[1] != "report") {
		fmt.Fprintln(os.Stderr, "usage: c2check gen|report DIR NAME...")
		os.Exit(2)
	}
	dir, names := os.Args[2], os.Args[3:]
	var err error
	code := 0
	if os.Args[1] == "gen" {
		for _, n := range names {
			if err = gen(dir, n); err != nil {
				break
			}
		}
	} else {
		code, err = report(os.Stdout, dir, names)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "c2check:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

func readPCM(path string) ([]int16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return s, nil
}

func writePCM(path string, s []int16) error {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return os.WriteFile(path, b, 0o644)
}

func encodeAll(pcm []int16) []byte {
	e := codec2.NewEncoder()
	n := len(pcm) / fs
	out := make([]byte, 0, n*fb)
	var in [fs]int16
	var f [fb]byte
	for i := 0; i < n; i++ {
		copy(in[:], pcm[i*fs:])
		e.Encode(&f, &in)
		out = append(out, f[:]...)
	}
	return out
}

func decodeAll(c2 []byte) []int16 {
	d := codec2.NewDecoder()
	n := len(c2) / fb
	out := make([]int16, 0, n*fs)
	var f [fb]byte
	var pcm [fs]int16
	for i := 0; i < n; i++ {
		copy(f[:], c2[i*fb:])
		d.Decode(&pcm, &f)
		out = append(out, pcm[:]...)
	}
	return out
}

func gen(dir, name string) error {
	p := func(s string) string { return filepath.Join(dir, name+s) }
	pcm, err := readPCM(p(".raw"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(p(".go.bin"), encodeAll(pcm), 0o644); err != nil {
		return err
	}
	ref, err := os.ReadFile(p(".ref.bin"))
	if err != nil {
		return err
	}
	return writePCM(p(".godec.raw"), decodeAll(ref))
}

// bitMagic is the start of a libcodec2 file header. Bit files must be
// headerless, so finding it means a headered file was passed by mistake.
var bitMagic = []byte{0xC0, 0xDE, 0xC2}

// checkBits rejects a headerless bit file that is not a whole number of
// frames or that starts with the codec2 header magic.
func checkBits(path string, b []byte) error {
	if len(b)%fb != 0 {
		return fmt.Errorf("%s: %d bytes is not a whole number of %d-byte frames (headered file?)", path, len(b), fb)
	}
	if bytes.HasPrefix(b, bitMagic) {
		return fmt.Errorf("%s: starts with codec2 header magic 0xC0 0xDE 0xC2; expected headerless bits", path)
	}
	return nil
}

// bitRange returns the bits [lo,hi) of an 8-byte frame as an integer.
func bitRange(f []byte, lo, hi int) uint64 {
	v := binary.BigEndian.Uint64(f)
	return (v << uint(lo)) >> uint(64-(hi-lo))
}

// snr returns 10*log10(sum ref^2 / sum (test-ref)^2) over the common length,
// +Inf when the signals are identical.
func snr(ref, test []int16) float64 {
	n := min(len(ref), len(test))
	var sig, noise float64
	for i := 0; i < n; i++ {
		r, d := float64(ref[i]), float64(test[i])-float64(ref[i])
		sig += r * r
		noise += d * d
	}
	if noise == 0 {
		return math.Inf(1)
	}
	if sig == 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(sig/noise)
}

func fmtSNR(v float64) string {
	if math.IsInf(v, 1) {
		return "inf (exact)"
	}
	return fmt.Sprintf("%.1f dB", v)
}

func report(w io.Writer, dir string, names []string) (int, error) {
	fail := false
	var warns []string
	fmt.Fprintf(w, "### Codec2 3200: Go port vs libcodec2\n\n")
	fmt.Fprintf(w, "Thresholds: decode SNR >= %.0f dB, bit-exact frames >= %.0f%%.\n\n", MinDecodeSNR, MinExactFrames*100)
	fmt.Fprintln(w, "| sample | frames | exact frames | differing bits | decode exact | max abs diff | decode SNR (same bits, ours vs ref) | cross SNR (our bits via c2dec vs ref path) | result |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
	type fieldRow struct {
		name string
		diff []int
	}
	var frows []fieldRow
	var diag []string
	for _, name := range names {
		p := func(s string) string { return filepath.Join(dir, name+s) }
		ref, err := os.ReadFile(p(".ref.bin"))
		if err != nil {
			return 0, err
		}
		ours, err := os.ReadFile(p(".go.bin"))
		if err != nil {
			return 0, err
		}
		if err := checkBits(p(".ref.bin"), ref); err != nil {
			return 0, err
		}
		if err := checkBits(p(".go.bin"), ours); err != nil {
			return 0, err
		}
		refdec, err := readPCM(p(".refdec.raw"))
		if err != nil {
			return 0, err
		}
		godec, err := readPCM(p(".godec.raw"))
		if err != nil {
			return 0, err
		}
		xdec, err := readPCM(p(".xdec.raw"))
		if err != nil {
			return 0, err
		}
		frames := min(len(ref), len(ours)) / fb
		if len(ref)/fb != len(ours)/fb {
			warns = append(warns, fmt.Sprintf("%s: frame count differs (ref %d, ours %d)", name, len(ref)/fb, len(ours)/fb))
		}
		exact, diffBits := 0, 0
		fdiff := make([]int, len(fields))
		for i := 0; i < frames; i++ {
			a, b := ref[i*fb:(i+1)*fb], ours[i*fb:(i+1)*fb]
			x := binary.BigEndian.Uint64(a) ^ binary.BigEndian.Uint64(b)
			if x == 0 {
				exact++
			}
			diffBits += bits.OnesCount64(x)
			for k, f := range fields {
				if bitRange(a, f.lo, f.hi) != bitRange(b, f.lo, f.hi) {
					fdiff[k]++
				}
			}
		}
		n := min(len(refdec), len(godec))
		same, maxd := 0, 0
		for i := 0; i < n; i++ {
			d := int(refdec[i]) - int(godec[i])
			if d < 0 {
				d = -d
			}
			if d == 0 {
				same++
			}
			maxd = max(maxd, d)
		}
		dsnr := snr(refdec, godec)
		xsnr := snr(refdec, xdec)
		pexact := pct(exact, frames)
		res := "pass"
		if frames == 0 || n == 0 || dsnr < MinDecodeSNR || float64(exact) < MinExactFrames*float64(frames) {
			res, fail = "FAIL", true
		} else if exact != frames || dsnr < math.Inf(1) {
			warns = append(warns, fmt.Sprintf("%s: not identical to libcodec2: %d/%d frames exact (%.2f%%), %.3f%% bits differ, decode SNR %s, max abs diff %d",
				name, exact, frames, pexact, pct(diffBits, frames*64), fmtSNR(dsnr), maxd))
		}
		fmt.Fprintf(w, "| %s | %d | %d (%.2f%%) | %.3f%% | %.2f%% | %d | %s | %s | %s |\n",
			name, frames, exact, pexact, pct(diffBits, frames*64), pct(same, n), maxd,
			fmtSNR(dsnr), fmtSNR(xsnr), res)
		frows = append(frows, fieldRow{name, fdiff})
		diag = append(diag, diagnose(name, refdec, godec))
	}
	fmt.Fprintln(w, "\nFrames whose field differs from libcodec2 (percent of frames):")
	fmt.Fprintln(w)
	hdr, sep := "| sample |", "|---|"
	for _, f := range fields {
		hdr += " " + f.name + " |"
		sep += "---|"
	}
	fmt.Fprintln(w, hdr)
	fmt.Fprintln(w, sep)
	for i, r := range frows {
		line := "| " + r.name + " |"
		total := framesOf(dir, names[i])
		for _, c := range r.diff {
			line += fmt.Sprintf(" %d (%.2f%%) |", c, pct(c, total))
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
	for _, d := range diag {
		fmt.Fprint(w, d)
	}
	for _, m := range warns {
		fmt.Fprintln(w, "WARN: "+m+"  ")
	}
	if fail {
		fmt.Fprintln(w, "\n**RESULT: FAIL**")
		return 1, nil
	}
	fmt.Fprintln(w, "\n**RESULT: PASS**")
	return 0, nil
}

func framesOf(dir, name string) int {
	st, err := os.Stat(filepath.Join(dir, name+".ref.bin"))
	if err != nil {
		return 0
	}
	return int(st.Size()) / fb
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}
