# YSF test vectors

`vectors.txt` holds small synthetic vectors (no recorded traffic, no real
callsigns). They were generated once by a throwaway C++ harness linked
against unmodified g4klx MMDVMHost sources (YSFFICH, YSFPayload,
YSFConvolution, Golay24128, CRC, P25Audio/AMBEFEC) plus the V/D mode 2
DCH writer and AMBE-to-VCH packing of MMDVM_CM (YSF2DMR ModeConv /
YSFPayload::writeVDMode2Data). The harness also decoded every encoded
block with MMDVMHost and checked the round trip.

Lines:

- `golay24128 <data12> <codeword24>`: CGolay24128::encode24128
- `crc <bytes> <crc16>`: CCRC::addCCITT162
- `fich <4 info bytes> <25 coded bytes>`: CYSFFICH::encode
- `dch <10-char word> <25 coded bytes>`: the five 5-byte DCH slices
- `csd <90-byte payload>`: CYSFPayload::writeHeader, CSD1 `ALL       N0CALL    `, CSD2 blanks
- `vch <7-byte AMBE> <13-byte interleaved VCH>`
- `imbeNN` / `vwblockNN`: synthetic 11-byte IMBE frames and the 18-byte
  interleaved Voice FR codewords CP25Audio::encode produced for them

The harness and the reference clones are not part of the repository.
