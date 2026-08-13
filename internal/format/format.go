// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

// Package format renders inventory values and filesystem names as text that is
// safe to write to a terminal.
package format

import (
	"fmt"
	"strconv"
)

// Bytes renders a byte count in binary units.
func Bytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	divisor := uint64(unit)
	exponent := 0
	for quotient := value / unit; quotient >= unit && exponent < 5; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f%ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

// Count renders an object count in decimal units.
func Count(value uint64) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

// Display escapes a filesystem name for output. Linux names may contain any
// byte except NUL and '/', so an unescaped name can carry newlines that forge
// output lines or escape sequences that reprogram the terminal.
func Display(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	return quoted[1 : len(quoted)-1]
}
