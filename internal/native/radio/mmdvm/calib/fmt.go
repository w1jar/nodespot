package calib

import (
	"fmt"
	"time"
)

func fmtPct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

func clock(t time.Time) string { return t.Format("15:04:05") }

// Hz formats an offset as "+350 Hz".
func hz(off int) string { return fmt.Sprintf("%+d Hz", off) }

func idText(c CallResult) string {
	if !c.HaveID {
		return "unknown"
	}
	return fmt.Sprintf("%d -> %d", c.Src, c.Dst)
}

func berText(c CallResult) string {
	if b, ok := c.BER(); ok {
		return fmtPct(b)
	}
	return "no voice"
}
