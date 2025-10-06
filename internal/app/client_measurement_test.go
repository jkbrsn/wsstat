package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureText(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	t.Run("single text message", func(t *testing.T) {
		client := NewClient(WithTextMessage("hello"), WithCount(1))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.measureText(ctx, wsURL, nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotNil(t, result.Result)
		assert.Equal(t, "hello", result.Response)
		assert.Equal(t, 1, result.Result.MessageCount)
	})

	t.Run("multiple text messages", func(t *testing.T) {
		client := NewClient(WithTextMessage("test"), WithCount(3))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.measureText(ctx, wsURL, nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 3, result.Result.MessageCount)
	})

	t.Run("context cancellation", func(t *testing.T) {
		client := NewClient(WithTextMessage("test"), WithCount(1))
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := client.measureText(ctx, wsURL, nil)
		assert.Error(t, err)
	})
}

func TestMeasureJSON(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			response := map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"result":  "success",
			}
			if err := conn.WriteJSON(response); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	t.Run("single JSON-RPC message", func(t *testing.T) {
		client := NewClient(WithRPCMethod("test_method"), WithCount(1))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.measureJSON(ctx, wsURL, nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotNil(t, result.Result)
		assert.Equal(t, 1, result.Result.MessageCount)

		responseMap, ok := result.Response.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "2.0", responseMap["jsonrpc"])
		assert.Equal(t, "success", responseMap["result"])
	})

	t.Run("multiple JSON-RPC messages", func(t *testing.T) {
		client := NewClient(WithRPCMethod("test_method"), WithCount(5))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.measureJSON(ctx, wsURL, nil)
		require.NoError(t, err)
		assert.Equal(t, 5, result.Result.MessageCount)
	})
}

func TestMeasurePing(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		conn.SetPingHandler(func(appData string) error {
			deadline := time.Now().Add(time.Second)
			return conn.WriteControl(websocket.PongMessage, []byte(appData), deadline)
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	t.Run("single ping", func(t *testing.T) {
		client := NewClient(WithCount(1))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.measurePing(ctx, wsURL, nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotNil(t, result.Result)
		assert.Nil(t, result.Response)
		assert.Equal(t, 1, result.Result.MessageCount)
	})

	t.Run("multiple pings", func(t *testing.T) {
		client := NewClient(WithCount(3))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.measurePing(ctx, wsURL, nil)
		require.NoError(t, err)
		assert.Equal(t, 3, result.Result.MessageCount)
	})
}

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
		assert.True(t, ok, "expected JSON-RPC response to decode into a map, got %T", result)
		assert.Equal(t, "ok", asMap["result"])
	})

	t.Run("extracts first element from array", func(t *testing.T) {
		input := []string{"first", "second", "third"}
		result, err := processTextResponse(input, formatAuto)
		require.NoError(t, err)
		assert.Equal(t, "first", result)
	})

	t.Run("respects raw format", func(t *testing.T) {
		payload := `{"jsonrpc":"2.0","result":"ok"}`
		result, err := processTextResponse(payload, formatRaw)
		require.NoError(t, err)
		assert.Equal(t, payload, result)
	})
}

func TestDecodeAsJSONRPC(t *testing.T) {
	t.Run("valid JSON-RPC", func(t *testing.T) {
		input := `{"jsonrpc":"2.0","result":"ok","id":"1"}`
		result, err := decodeAsJSONRPC(input)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, "ok", result["result"])
	})

	t.Run("plain JSON without jsonrpc field", func(t *testing.T) {
		input := `{"result":"ok"}`
		_, err := decodeAsJSONRPC(input)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid JSON-RPC version")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		input := `{invalid json`
		_, err := decodeAsJSONRPC(input)
		assert.Error(t, err)
	})

	t.Run("empty string", func(t *testing.T) {
		_, err := decodeAsJSONRPC("")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not JSON")
	})

	t.Run("plain text", func(t *testing.T) {
		input := "not json at all"
		_, err := decodeAsJSONRPC(input)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not JSON")
	})

	t.Run("whitespace trimming", func(t *testing.T) {
		input := `  {"jsonrpc":"2.0","result":"ok"}  `
		result, err := decodeAsJSONRPC(input)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
	})
}
