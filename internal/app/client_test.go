package app

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseHeaders verifies that parseHeaders correctly handles valid and invalid input.
func TestParseHeaders(t *testing.T) {
	t.Run("valid headers", func(t *testing.T) {
		h, err := parseHeaders([]string{
			"Content-Type: application/json",
			"Authorization: Bearer token",
		})
		require.NoError(t, err)
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Equal(t, "Bearer token", h.Get("Authorization"))
	})

	t.Run("invalid header", func(t *testing.T) {
		_, err := parseHeaders([]string{"Content-Type"})
		assert.Error(t, err)
	})

	t.Run("empty string", func(t *testing.T) {
		h, err := parseHeaders(nil)
		require.NoError(t, err)
		assert.Len(t, h, 0)
	})
}

// TestFormatPadding ensures the padding helpers behave as expected.
func TestFormatPadding(t *testing.T) {
	d := 5 * time.Millisecond
	assert.Equal(t, "      5ms", formatPadLeft(d))
	assert.Equal(t, "5ms     ", formatPadRight(d))
}

// TestHandleConnectionError checks error message classification.
func TestHandleConnectionError(t *testing.T) {
	tlsErr := errors.New("tls: first record does not look like a TLS handshake")
	msg := handleConnectionError(tlsErr, "wss://example.com").Error()
	assert.Contains(t, msg, "secure WebSocket connection")

	genErr := errors.New("something went wrong")
	msg2 := handleConnectionError(genErr, "ws://example.com").Error()
	assert.NotContains(t, msg2, "secure WebSocket connection")
}
