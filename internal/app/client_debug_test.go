package app

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWithDebug confirms --debug wiring appends a core logger option only when enabled, and
// that the logger writes to the configured destination independently of the output contract.
func TestWithDebug(t *testing.T) {
	t.Run("off appends no logger option", func(t *testing.T) {
		base := len(NewClient().wsstatOptions())
		assert.Equal(t, base, len(NewClient(WithDebug(false)).wsstatOptions()))
	})

	t.Run("on appends one logger option", func(t *testing.T) {
		base := len(NewClient().wsstatOptions())
		assert.Equal(t, base+1, len(NewClient(WithDebug(true)).wsstatOptions()))
	})

	t.Run("logger writes to the configured writer", func(t *testing.T) {
		var buf bytes.Buffer
		c := NewClient(WithDebug(true), WithDebugWriter(&buf))
		// wsstatOptions builds a zerolog logger over c.debugW; exercise it to confirm
		// the writer (not os.Stderr) receives core debug output.
		logger := debugLogger(c.debugW)
		logger.Debug().Str("probe", "x").Msg("debug probe")
		assert.Contains(t, buf.String(), `"level":"debug"`)
		assert.Contains(t, buf.String(), `"probe":"x"`)
	})
}
