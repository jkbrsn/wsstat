package main

import (
	"testing"

	"github.com/jkbrsn/wsstat/v3/internal/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revive:disable:line-length-limit table-driven test rows

func TestParseWSURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{name: "full wss URL", input: "wss://example.com/path", expected: "wss://example.com/path"},
		{name: "full ws URL", input: "ws://example.com/path", expected: "ws://example.com/path"},
		{name: "no scheme defaults to wss", input: "example.com/path", expected: "wss://example.com/path"},
		{name: "localhost without scheme", input: "localhost:8080", expected: "wss://localhost:8080"},
		{name: "invalid URL", input: "ht!tp://invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseWSURI(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result.String())
		})
	}
}

func TestMeasureFlags(t *testing.T) {
	t.Parallel()

	t.Run("bare url defaults", func(t *testing.T) {
		client, target, err := buildMeasure([]string{"example.com"})
		require.NoError(t, err)
		assert.Equal(t, "wss://example.com", target.String())
		assert.Equal(t, 1, client.Count())
		assert.Equal(t, app.OutputText, client.Output())
		assert.Equal(t, app.BodyAuto, client.Body())
	})

	t.Run("count", func(t *testing.T) {
		client, _, err := buildMeasure([]string{"-c", "5", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, 5, client.Count())
	})

	t.Run("count below one rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-c", "0", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "count must be greater than 0")
	})

	t.Run("text and rpc-method conflict", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-t", "hi", "--rpc-method", "m", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("text-only flag under json rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-o", "json", "-q", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only applies to text output")
	})

	t.Run("body under json rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-o", "json", "--body", "compact", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only applies to text output")
	})

	t.Run("raw measure without message rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-o", "raw", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires --text or --rpc-method")
	})

	t.Run("raw measure with text ok", func(t *testing.T) {
		client, _, err := buildMeasure([]string{"-o", "raw", "-t", "hi", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, app.OutputRaw, client.Output())
	})

	t.Run("invalid color rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"--color", "purple", "example.com"})
		require.Error(t, err)
	})

	t.Run("negative timeout rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"--timeout", "-1s", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--timeout must be zero or greater")
	})

	t.Run("negative close-timeout rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"--close-timeout", "-2s", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--close-timeout must be zero or greater")
	})

	t.Run("quiet and verbose conflict", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-q", "-v", "example.com"})
		require.Error(t, err)
	})

	t.Run("missing url rejected", func(t *testing.T) {
		_, _, err := buildMeasure([]string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one URL")
	})

	t.Run("stray positional rejected (global flag before subcommand)", func(t *testing.T) {
		// `wsstat -v stream url` dispatches to measure with two positionals.
		_, _, err := buildMeasure([]string{"-v", "stream", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one URL")
	})

	t.Run("headers accumulate", func(t *testing.T) {
		client, _, err := buildMeasure([]string{
			"-H", "Auth: Bearer token", "-H", "Origin: https://foo.com", "example.com",
		})
		require.NoError(t, err)
		assert.NotNil(t, client)
	})
}

func TestStreamFlags(t *testing.T) {
	t.Parallel()

	t.Run("bare url is unlimited", func(t *testing.T) {
		client, target, err := buildStream([]string{"example.com"})
		require.NoError(t, err)
		assert.Equal(t, "wss://example.com", target.String())
		assert.Equal(t, 0, client.Count())
		assert.False(t, client.Once())
	})

	t.Run("count", func(t *testing.T) {
		client, _, err := buildStream([]string{"-c", "3", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, 3, client.Count())
	})

	t.Run("once", func(t *testing.T) {
		client, _, err := buildStream([]string{"--once", "example.com"})
		require.NoError(t, err)
		assert.True(t, client.Once())
	})

	t.Run("once with count greater than one rejected", func(t *testing.T) {
		_, _, err := buildStream([]string{"--once", "-c", "5", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--once")
	})

	t.Run("json output allowed without message", func(t *testing.T) {
		client, _, err := buildStream([]string{"-o", "json", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, app.OutputJSON, client.Output())
	})

	t.Run("raw output allowed without message", func(t *testing.T) {
		client, _, err := buildStream([]string{"-o", "raw", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, app.OutputRaw, client.Output())
	})

	t.Run("verbose under json rejected", func(t *testing.T) {
		_, _, err := buildStream([]string{"-o", "json", "-v", "example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only applies to text output")
	})

	t.Run("buffer", func(t *testing.T) {
		_, _, err := buildStream([]string{"-b", "10", "example.com"})
		require.NoError(t, err)
	})
}
