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
		hdrStr := "Content-Type: application/json, Authorization: Bearer token"
		h, err := parseHeaders(hdrStr)
		require.NoError(t, err)
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Equal(t, "Bearer token", h.Get("Authorization"))
	})

	t.Run("invalid header", func(t *testing.T) {
		_, err := parseHeaders("Content-Type")
		assert.Error(t, err)
	})

	t.Run("empty string", func(t *testing.T) {
		h, err := parseHeaders("")
		require.NoError(t, err)
		assert.Len(t, h, 0)
	})
}

// TestFormatPadding ensures the padding helpers behave as expected.
func TestFormatPadding(t *testing.T) {
	d := 5 * time.Millisecond
	assert.Equal(t, "      5ms", formatPadLeft(d)) // 6 leading spaces + "5ms"
	assert.Equal(t, "5ms     ", formatPadRight(d)) // "5ms" + 5 trailing spaces
}

func TestPostProcessTextResponse(t *testing.T) {
	t.Run("keeps plain text", func(t *testing.T) {
		c := &Client{Response: "not json"}
		require.NoError(t, c.postProcessTextResponse())
		assert.Equal(t, "not json", c.Response)
	})

	t.Run("decodes json rpc", func(t *testing.T) {
		payload := `{"jsonrpc":"2.0","result":"ok"}`
		c := &Client{Response: payload}
		require.NoError(t, c.postProcessTextResponse())
		asMap, ok := c.Response.(map[string]any)
		require.True(t, ok, "expected JSON-RPC response to decode into a map")
		assert.Equal(t, "ok", asMap["result"])
	})
}

// TestColorHelpers performs basic sanity checks on ANSI wrapped strings.
func TestColorHelpers(t *testing.T) {
	base := "txt"
	assert.Contains(t, customColor(1, 2, 3, base), "[38;2;1;2;3m")
	assert.Contains(t, colorWSOrange(base), "255;102;0m")
	assert.Contains(t, colorTeaGreen(base), "211;249;181m")
}

// TestHandleConnectionError checks error message classification.
func TestHandleConnectionError(t *testing.T) {
	tlsErr := errors.New("tls: first record does not look like a TLS handshake")
	msg := handleConnectionError(tlsErr, "wss://example.com").Error()
	assert.Contains(t, msg, "secure WS connection")

	genErr := errors.New("something went wrong")
	msg2 := handleConnectionError(genErr, "ws://example.com").Error()
	assert.NotContains(t, msg2, "secure WS connection")
}

// TestClientValidate exercises the validation logic for burst and mutually-exclusive flags.
func TestClientValidate(t *testing.T) {
	t.Run("invalid burst", func(t *testing.T) {
		c := &Client{Burst: 0}
		assert.Error(t, c.Validate())
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		c := &Client{Burst: 1, TextMessage: "hi", JSONMethod: "foo"}
		assert.Error(t, c.Validate())
	})

	t.Run("valid", func(t *testing.T) {
		c := &Client{Burst: 2, TextMessage: "hi"}
		require.NoError(t, c.Validate())
	})

	t.Run("subscribe requires single burst", func(t *testing.T) {
		c := &Client{Burst: 2, Subscribe: true}
		assert.Error(t, c.Validate())
	})

	t.Run("subscribe with burst one ok", func(t *testing.T) {
		c := &Client{Burst: 1, Subscribe: true}
		require.NoError(t, c.Validate())
	})
}
