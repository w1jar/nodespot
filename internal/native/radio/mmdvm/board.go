package mmdvm

import (
	"regexp"
	"strconv"
	"strings"
)

// Board is what a GET_VERSION description says about the hardware and
// firmware. Fields the text does not carry are empty. The UDID is not part
// of it.
type Board struct {
	// Name is the board or firmware family, such as "MMDVM_HS".
	Name string
	// Version is the firmware version, such as "1.6.1".
	Version string
	// BuildDate is the build date as YYYY-MM-DD; BuildTag is any text
	// joined to the date ("20231115_XYZ" gives "XYZ").
	BuildDate, BuildTag string
	// TCXO is the reference oscillator, such as "14.7456MHz".
	TCXO string
	// RFChip is the radio chip, such as "ADF7021"; Dual is true for a
	// board with two of them (one receives while the other transmits).
	RFChip string
	Dual   bool
	// GitID is the firmware commit, without the '#'.
	GitID string
	// DuplexCapable is true when the board can run both DMR slots: a
	// dual-chip hotspot, or a full-size modem (no hotspot RF chip named).
	DuplexCapable bool
}

var (
	reNameVersion = regexp.MustCompile(`^([A-Za-z0-9_.]+)-v(\d+(?:\.\d+)+)`)
	reDate        = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})(?:[_-](\S+))?$`)
	reTCXO        = regexp.MustCompile(`^(\d+(?:\.\d+)?)MHz$`)
	reGitID       = regexp.MustCompile(`GitID\s*#?([0-9A-Fa-f]{4,40})`)
)

// ParseBoard reads a GET_VERSION description, such as
//
//	MMDVM_HS-v1.6.1 20231115 14.7456MHz dual ADF7021 FW by AUTHORS. GitID #7e16099
//
// Unknown text is ignored, so any description gives a Board.
func ParseBoard(desc string) Board {
	var b Board
	f := strings.Fields(desc)
	if len(f) > 0 {
		if m := reNameVersion.FindStringSubmatch(f[0]); m != nil {
			b.Name, b.Version = m[1], m[2]
		} else {
			b.Name = f[0]
		}
	}
	for i, w := range f {
		if i > 0 && b.BuildDate == "" {
			if m := reDate.FindStringSubmatch(w); m != nil && validDate(m[1], m[2], m[3]) {
				b.BuildDate, b.BuildTag = m[1]+"-"+m[2]+"-"+m[3], m[4]
				continue
			}
		}
		if b.TCXO == "" && reTCXO.MatchString(w) {
			b.TCXO = w
			continue
		}
		up := strings.ToUpper(strings.Trim(w, ".,()"))
		if b.RFChip == "" && (strings.HasPrefix(up, "ADF70") || strings.HasPrefix(up, "CC1200")) {
			b.RFChip = strings.Trim(w, ".,()")
			b.Dual = i > 0 && strings.EqualFold(f[i-1], "dual")
		}
	}
	if m := reGitID.FindStringSubmatch(desc); m != nil {
		b.GitID = m[1]
	}
	b.DuplexCapable = b.Dual || (b.RFChip == "" && b.Name != "" && !isHotspotName(b.Name))
	return b
}

// isHotspotName reports names of single-board hotspots, simplex unless the
// description says "dual".
func isHotspotName(n string) bool {
	u := strings.ToUpper(n)
	for _, p := range []string{"MMDVM_HS", "ZUMSPOT", "D2RG_MMDVM_HS", "NANO_", "SKYBRIDGE", "LONESTAR"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

func validDate(y, m, d string) bool {
	return y >= "2000" && y <= "2099" && m >= "01" && m <= "12" && d >= "01" && d <= "31"
}

// Protocol 1 enabled-modes bits (GET_STATUS byte 0).
const (
	EnDStar  = 0x01
	EnDMR    = 0x02
	EnYSF    = 0x04
	EnP25    = 0x08
	EnNXDN   = 0x10
	EnPOCSAG = 0x20
	EnM17    = 0x80
)

var enabledNames = []struct {
	bit  byte
	name string
}{
	{EnDStar, "dstar"}, {EnDMR, "dmr"}, {EnYSF, "ysf"}, {EnP25, "p25"},
	{EnNXDN, "nxdn"}, {EnPOCSAG, "pocsag"}, {EnM17, "m17"},
}

// ModeNames names the bits of a protocol 1 enabled-modes byte, in bit
// order. Bit 0x40 is not named.
func ModeNames(b byte) []string {
	out := []string{}
	for _, e := range enabledNames {
		if b&e.bit != 0 {
			out = append(out, e.name)
		}
	}
	return out
}

// ModeName names a modem mode (SET_MODE and GET_STATUS).
func ModeName(m byte) string {
	switch m {
	case ModeIdle:
		return "idle"
	case ModeDStar:
		return "dstar"
	case ModeDMR:
		return "dmr"
	case ModeYSF:
		return "ysf"
	case ModeP25:
		return "p25"
	case ModeNXDN:
		return "nxdn"
	case ModePOCSAG:
		return "pocsag"
	case ModeFM:
		return "fm"
	case 98:
		return "cw"
	case 99:
		return "lockout"
	case 100:
		return "error"
	}
	return "mode " + strconv.Itoa(int(m))
}
