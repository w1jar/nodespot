# Regenerating the Codec2 test vectors

`gen.c` links against the unmodified Codec2-mod C sources and produces the
files in `..` (`enc.frames`, `dec.pcm.gz`, `rnd.frames`, `rnddec.pcm.gz`).
It is test tooling: it is not part of any Go build and never runs in
`go test`.

Reference: Codec2-mod, https://github.com/M17-Project/Codec2-mod, commit
`7c334283794fa6304dc014f12df70afc8393143a` (LGPL-2.1). The reference source is
not included here. The vectors were made with gcc 13.3, x86-64, glibc 2.39.

Treat the downloaded sources as untrusted: compile them yourself in a scratch
directory; do not run their build scripts.

```sh
git clone https://github.com/M17-Project/Codec2-mod c2src
git -C c2src checkout 7c334283794fa6304dc014f12df70afc8393143a
mkdir build && cd build
# -ffp-contract=off keeps gcc from fusing multiply-adds, as the Go port does
gcc -O2 -ffp-contract=off -std=gnu11 -I../c2src/inc ../c2src/src/*.c \
    /path/to/nodespot/internal/dv/codec2/testdata/gen/gen.c -lm -o gen

./gen sig in.pcm            # 512 frames, s16le; sha256 7fe7a8bb...d47f69
./gen enc in.pcm enc.bits   # -> testdata/enc.frames
./gen dec enc.bits dec.pcm  # -> gzip -9n -> testdata/dec.pcm.gz
./gen rnd 300 rnd.bits      # 300 pseudo-random frames -> testdata/rnd.frames
./gen dec rnd.bits rnddec.pcm   # -> gzip -9n -> testdata/rnddec.pcm.gz
./gen libm libm.txt         # the glibc cosf/sinf tables of libmtables.go
```

The test signal is integer-only (no libm), so `sigGen` in `codec2_test.go`
reproduces it bit-exactly; keep it in step with `gen_signal()` in `gen.c`. The
test checks its SHA-256. Do not use real recordings.

`gen libm` writes the cosf/sinf values the reference computes at start-up
(KISS FFT twiddles, analysis window, pitch estimator window) as float32 hex;
`libmtables.go` is that output. Regenerate and compare if the reference or the
libm changes: glibc's cosf/sinf are not correctly rounded and the decoder is
sensitive to the last bit of the twiddles.
