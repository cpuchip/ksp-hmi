package main

import (
	"fmt"
	"math"
)

// fmtDuration renders seconds as a compact human string a CAPCOM can read aloud:
// "2d 03h 04m 05s", "1h 02m 05s", "5m 03s", or "45s". NaN/Inf render as "n/a".
func fmtDuration(sec float64) string {
	if math.IsNaN(sec) || math.IsInf(sec, 0) {
		return "n/a"
	}
	neg := sec < 0
	total := int64(math.Abs(sec) + 0.5)
	d := total / 86400
	total %= 86400
	h := total / 3600
	total %= 3600
	m := total / 60
	s := total % 60
	var out string
	switch {
	case d > 0:
		out = fmt.Sprintf("%dd %02dh %02dm %02ds", d, h, m, s)
	case h > 0:
		out = fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	case m > 0:
		out = fmt.Sprintf("%dm %02ds", m, s)
	default:
		out = fmt.Sprintf("%ds", s)
	}
	if neg {
		out = "-" + out
	}
	return out
}

// round2 rounds to two decimals to keep JSON output free of float noise.
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}
