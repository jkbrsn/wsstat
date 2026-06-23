package app

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRPCRequest(t *testing.T) {
	t.Run("2.0 default", func(t *testing.T) {
		b, err := json.Marshal(buildRPCRequest("eth_blockNumber", "2.0"))
		require.NoError(t, err)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"1","method":"eth_blockNumber"}`, string(b))
	})

	t.Run("unknown version falls back to 2.0", func(t *testing.T) {
		b, err := json.Marshal(buildRPCRequest("m", ""))
		require.NoError(t, err)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"1","method":"m"}`, string(b))
	})

	t.Run("1.0 omits version, integer id, empty params array", func(t *testing.T) {
		b, err := json.Marshal(buildRPCRequest("getInfo", "1.0"))
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":1,"method":"getInfo","params":[]}`, string(b))
	})
}

func TestDecodeJSONRPCResponse2(t *testing.T) {
	t.Run("result response", func(t *testing.T) {
		in := []byte(`{"jsonrpc":"2.0","id":"1","result":"0x1"}`)
		got, err := decodeJSONRPCResponse(in, "2.0")
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"jsonrpc": "2.0", "id": "1", "result": "0x1"}, got)
	})

	t.Run("error response", func(t *testing.T) {
		got, err := decodeJSONRPCResponse(
			[]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"x"}}`), "2.0")
		require.NoError(t, err)
		assert.Equal(t, "2.0", got["jsonrpc"])
		assert.Equal(t, float64(1), got["id"])
		assert.IsType(t, map[string]any{}, got["error"])
		_, hasResult := got["result"]
		assert.False(t, hasResult)
	})

	t.Run("explicit null error treated as absent", func(t *testing.T) {
		in := []byte(`{"jsonrpc":"2.0","id":"1","result":"ok","error":null}`)
		got, err := decodeJSONRPCResponse(in, "2.0")
		require.NoError(t, err)
		assert.Equal(t, "ok", got["result"])
		_, hasErr := got["error"]
		assert.False(t, hasErr)
	})

	cases := []struct {
		name string
		in   string
	}{
		{"invalid json", `{`},
		{"wrong version", `{"jsonrpc":"1.0","result":"ok"}`},
		{"missing version", `{"result":"ok"}`},
		{"both result and error", `{"jsonrpc":"2.0","result":"ok","error":{"code":1}}`},
		{"neither result nor error", `{"jsonrpc":"2.0","id":"1"}`},
		{"object id rejected", `{"jsonrpc":"2.0","id":{"x":1},"result":"ok"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeJSONRPCResponse([]byte(tc.in), "2.0")
			assert.Error(t, err)
		})
	}
}

func TestDecodeJSONRPCResponse1(t *testing.T) {
	t.Run("version-less result response", func(t *testing.T) {
		got, err := decodeJSONRPCResponse([]byte(`{"id":1,"result":"ok","error":null}`), "1.0")
		require.NoError(t, err)
		assert.Equal(t, "ok", got["result"])
		_, hasVersion := got["jsonrpc"]
		assert.False(t, hasVersion, "implicit 1.0 carries no version field")
		_, hasErr := got["error"]
		assert.False(t, hasErr)
	})

	t.Run("1.0 error response with null result", func(t *testing.T) {
		got, err := decodeJSONRPCResponse([]byte(`{"id":1,"result":null,"error":"boom"}`), "1.0")
		require.NoError(t, err)
		assert.Equal(t, "boom", got["error"])
		_, hasResult := got["result"]
		assert.False(t, hasResult)
	})

	t.Run("explicit 1.0 version accepted", func(t *testing.T) {
		in := []byte(`{"jsonrpc":"1.0","id":1,"result":"ok","error":null}`)
		got, err := decodeJSONRPCResponse(in, "1.0")
		require.NoError(t, err)
		assert.Equal(t, "1.0", got["jsonrpc"])
		assert.Equal(t, "ok", got["result"])
	})

	t.Run("version-less rejected when not allowed", func(t *testing.T) {
		_, err := decodeJSONRPCResponse([]byte(`{"id":1,"result":"ok","error":null}`), "2.0")
		assert.Error(t, err)
	})
}
