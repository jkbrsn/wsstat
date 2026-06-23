package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmitJSONError(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, EmitJSONError(&buf, errors.New("measuring latency: dial tcp: refused")))

	out := buf.String()
	assert.True(t, strings.HasSuffix(out, "\n"), "envelope must be newline-terminated for NDJSON")

	var env struct {
		Schema string `json:"schema_version"`
		Type   string `json:"type"`
		Error  string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &env))
	assert.Equal(t, JSONSchemaVersion, env.Schema)
	assert.Equal(t, "error", env.Type)
	assert.Equal(t, "measuring latency: dial tcp: refused", env.Error)
}

// failWriter always errors, exercising EmitJSONError's write-failure path.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

func TestEmitJSONErrorWriteFailure(t *testing.T) {
	err := EmitJSONError(failWriter{}, errors.New("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write error envelope")
}
