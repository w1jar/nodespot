# DMR cross-check vectors

`vectors.txt` is the output of `harness.cc` linked against the unmodified
MMDVMHost sources (G4KLX, GPL-2.0-or-later), https://github.com/g4klx/MMDVMHost,
commit `b18e6f9e529177305dc9c648d0660a30574a7afa`. The reference source is not
included here. Inputs are synthetic: an LCG for the AMBE frames and bursts, and
placeholder IDs only (DMR ID 1234567, TGs 1997/1999, parrot 9990, the low 24
bits of repeater ID 123456701).

| line       | content                                                                       |
|------------|-------------------------------------------------------------------------------|
| `ambe`     | AMBE 49-bit frame, its 72-bit DMR frame (built with `DMR_A/B/C_TABLE`, `encode24128`, `encode23127`, `PRNG_TABLE`), the PRNG word |
| `regen`    | 33-byte burst in, `CAMBEFEC::regenerateDMR` burst out, error count (36 bursts of clean frames with 0-5 bit errors per frame, 4 random bursts) |
| `prngfnv`  | FNV-1a 32 of all 4096 `PRNG_TABLE[i] >> 1` (3 bytes each, big endian)         |
| `emb`      | CC, PI, LCSS, burst bytes 13..19 after `CDMREMB::getData` (QR 16,7,6)        |
| `slottype` | CC, data type, burst bytes 12..20 after `CDMRSlotType::getData` (Golay 20,8) |
| `lc`       | 9-byte LC, `CDMRFullLC::encode` voice LC header burst, terminator burst (BPTC 196,96 + RS 12,9 + masks; sync and slot type left zero) |
| `emblc`    | 9-byte LC, then for n = 1..4 `CDMREmbeddedData::getData(n)`: `LCSS:burst bytes 14..18` |

The tests in `vectors_test.go` read every line.

Where the package differs from MMDVMHost on purpose: MMDVMHost's
`decode24128` rejects some correctable C0 words (two data-bit errors plus a
parity-bit error); this package corrects them (Golay(24,12) corrects any 3
errors). Frames MMDVMHost mutes are therefore compared only for the error
count of bursts with no muted frame, and each side mutes with its own frame
(MMDVMHost `a=F00292 b=0E0B20 c=0`, this package `SilenceAMBE`).

## Regenerating

```sh
git clone https://github.com/g4klx/MMDVMHost && cd MMDVMHost
git checkout b18e6f9e529177305dc9c648d0660a30574a7afa && cd ..
mkdir -p stub/nlohmann && echo 'namespace nlohmann { class json {}; }' > stub/nlohmann/json.hpp
cat > Log.cpp <<'X'
#include <cstdio>
#include <cstdarg>
void Log(unsigned int, const char* f, ...) { va_list a; va_start(a, f); vfprintf(stderr, f, a); va_end(a); }
X
M=MMDVMHost
g++ -O1 -std=c++17 -include ctime -Istub -I$M path/to/testdata/harness.cc Log.cpp \
  $M/Golay24128.cpp $M/Hamming.cpp $M/Utils.cpp $M/DMREmbeddedData.cpp $M/DMRFullLC.cpp \
  $M/DMRLC.cpp $M/DMREMB.cpp $M/QR1676.cpp $M/DMRSlotType.cpp $M/Golay2087.cpp $M/CRC.cpp \
  $M/BPTC19696.cpp $M/RS129.cpp -o harness
./harness > vectors.txt
```
