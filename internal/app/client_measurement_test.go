package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessTextResponse(t *testing.T) {
	t.Run("keeps plain text", func(t *testing.T) {
		result, err := processTextResponse("not json", formatAuto)
		require.NoError(t, err)
		assert.Equal(t, "not json", result)
	})

	t.Run("decodes json rpc", func(t *testing.T) {
		payload := `{"jsonrpc":"2.0","result":"ok"}`
		result, err := processTextResponse(payload, formatAuto)
		require.NoError(t, err)
		asMap, ok := result.(map[string]any)
		require.True(t, ok, "expected JSON-RPC response to decode into a map")
		assert.Equal(t, "ok", asMap["result"])
	})
}
