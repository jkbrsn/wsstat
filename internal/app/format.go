package app

import (
	"fmt"
	"strings"
)

// Output is the whole-stdout contract. It controls what stdout *is*, not how a
// single payload body is rendered.
type Output string

const (
	// OutputText is human output, governed by Body/Clip/verbosity.
	OutputText Output = "text"
	// OutputJSON is schema-stable, newline-delimited JSON envelopes.
	OutputJSON Output = "json"
	// OutputRaw is verbatim payload bytes with nothing added.
	OutputRaw Output = "raw"
)

// Body is the human rendering of a payload body. It applies only to text output
// and governs both streamed messages and the measured response.
type Body string

const (
	// BodyAuto renders payloads pretty / multi-line.
	BodyAuto Body = "auto"
	// BodyCompact renders one line per message.
	BodyCompact Body = "compact"
)

// Mode is the operation the client performs.
type Mode int

const (
	// ModeMeasure measures connection latency.
	ModeMeasure Mode = iota
	// ModeStream streams subscription events.
	ModeStream
)

// ParseOutput normalizes and validates an output contract string. An empty
// string defaults to OutputText.
func ParseOutput(s string) (Output, error) {
	switch o := Output(strings.ToLower(strings.TrimSpace(s))); o {
	case "":
		return OutputText, nil
	case OutputText, OutputJSON, OutputRaw:
		return o, nil
	default:
		return "", fmt.Errorf("output must be %q, %q, or %q",
			OutputText, OutputJSON, OutputRaw)
	}
}

// ParseBody normalizes and validates a body-rendering string. An empty string
// defaults to BodyAuto.
func ParseBody(s string) (Body, error) {
	switch b := Body(strings.ToLower(strings.TrimSpace(s))); b {
	case "":
		return BodyAuto, nil
	case BodyAuto, BodyCompact:
		return b, nil
	default:
		return "", fmt.Errorf("body must be %q or %q", BodyAuto, BodyCompact)
	}
}
