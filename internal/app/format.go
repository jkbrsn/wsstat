package app

import (
	"fmt"
	"strings"
)

// Format is the output rendering mode for responses and subscription events.
type Format string

const (
	formatAuto    Format = "auto"    // human-readable, pretty-printed JSON (multi-line)
	formatCompact Format = "compact" // human-readable, one line per message
	formatJSON    Format = "json"    // newline-delimited JSON envelopes
	formatRaw     Format = "raw"     // verbatim payload bytes
)

// ParseFormat normalizes and validates a format string. An empty string defaults
// to formatAuto.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case "":
		return formatAuto, nil
	case formatAuto, formatCompact, formatJSON, formatRaw:
		return f, nil
	default:
		return "", fmt.Errorf("format must be %q, %q, %q, or %q",
			formatAuto, formatCompact, formatJSON, formatRaw)
	}
}
