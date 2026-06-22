package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveTextPayload covers the @file/@-/@@ expansion of the --text value.
func TestResolveTextPayload(t *testing.T) {
	t.Parallel()

	t.Run("plain value passes through", func(t *testing.T) {
		t.Parallel()
		c := &commonFlags{text: "ping"}
		require.NoError(t, resolveTextPayload(c))
		assert.Equal(t, "ping", c.text)
	})

	t.Run("empty value passes through", func(t *testing.T) {
		t.Parallel()
		c := &commonFlags{text: ""}
		require.NoError(t, resolveTextPayload(c))
		assert.Empty(t, c.text)
	})

	t.Run("escaped leading at", func(t *testing.T) {
		t.Parallel()
		c := &commonFlags{text: "@@literal"}
		require.NoError(t, resolveTextPayload(c))
		assert.Equal(t, "@literal", c.text)
	})

	t.Run("reads from file verbatim", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "payload.json")
		require.NoError(t, os.WriteFile(path, []byte("{\"a\":1}\n"), 0o600))
		c := &commonFlags{text: "@" + path}
		require.NoError(t, resolveTextPayload(c))
		assert.Equal(t, "{\"a\":1}\n", c.text, "trailing newline is preserved")
	})

	t.Run("missing file errors", func(t *testing.T) {
		t.Parallel()
		c := &commonFlags{text: "@" + filepath.Join(t.TempDir(), "nope")}
		err := resolveTextPayload(c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading --text payload")
	})

	// os.Stdin is process-global, so this subtest is not parallel.
	t.Run("reads from stdin via @-", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "stdin")
		require.NoError(t, os.WriteFile(path, []byte("from stdin"), 0o600))
		f, err := os.Open(path)
		require.NoError(t, err)
		defer func() { _ = f.Close() }()

		prev := os.Stdin
		os.Stdin = f
		defer func() { os.Stdin = prev }()

		c := &commonFlags{text: "@-"}
		require.NoError(t, resolveTextPayload(c))
		assert.Equal(t, "from stdin", c.text)
	})
}

// TestPrintHelpFor verifies that `help <subcommand>` dispatches to that subcommand's usage.
func TestPrintHelpFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rest []string
		want string
	}{
		{name: "no arg shows top usage", rest: nil, want: "wsstat measure [options] <url>"},
		{
			name: "measure shows measure usage",
			rest: []string{"measure"},
			want: "measure WebSocket connection latency",
		},
		{
			name: "stream shows stream usage",
			rest: []string{"stream"},
			want: "stream WebSocket subscription events",
		},
		{
			name: "unknown falls back to top usage",
			rest: []string{"bogus"},
			want: "wsstat measure [options] <url>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			printHelpFor(tt.rest, &buf)
			assert.Contains(t, buf.String(), tt.want)
		})
	}
}
