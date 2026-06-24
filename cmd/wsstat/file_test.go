package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jkbrsn/wsstat/v3/internal/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileFlagRoundTrips(t *testing.T) {
	t.Parallel()

	t.Run("unset by default", func(t *testing.T) {
		client, _, err := buildMeasure([]string{"example.com"})
		require.NoError(t, err)
		assert.Empty(t, client.ResponseFilePath())
	})

	t.Run("measure carries the path", func(t *testing.T) {
		client, _, err := buildMeasure([]string{"--file", "/tmp/out.jsonl", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, "/tmp/out.jsonl", client.ResponseFilePath())
	})

	t.Run("stream carries the path", func(t *testing.T) {
		client, _, err := buildStream([]string{"--file", "/tmp/out.jsonl", "example.com"})
		require.NoError(t, err)
		assert.Equal(t, "/tmp/out.jsonl", client.ResponseFilePath())
	})
}

func TestOpenResponseSink(t *testing.T) {
	t.Parallel()

	t.Run("no path is a no-op", func(t *testing.T) {
		client := app.NewClient()
		closeSink, err := openResponseSink(client, app.OutputText)
		require.NoError(t, err)
		require.NotNil(t, closeSink)
		closeSink()
	})

	t.Run("creates a new file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.jsonl")
		client := app.NewClient(app.WithResponseFile(path))
		closeSink, err := openResponseSink(client, app.OutputText)
		require.NoError(t, err)
		defer closeSink()
		assert.FileExists(t, path)
	})

	t.Run("refuses to overwrite an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.jsonl")
		require.NoError(t, os.WriteFile(path, []byte("existing"), 0o644))

		client := app.NewClient(app.WithResponseFile(path))
		_, err := openResponseSink(client, app.OutputText)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "opening response file")

		// The existing content must be left untouched.
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, "existing", string(data))
	})
}
