package wsstat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureText(t *testing.T) {
	result, responses, err := MeasureText(
		context.Background(), echoServerAddrWs, []string{"hello", "world"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"hello", "world"}, responses)
	assert.Positive(t, result.TotalTime)
	assert.Equal(t, 2, result.MessageCount)
}

func TestMeasureJSON(t *testing.T) {
	payload := map[string]any{"foo": "bar"}
	result, responses, err := MeasureJSON(
		context.Background(), echoServerAddrWs, []any{payload})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, responses, 1)
	assert.Equal(t, payload, responses[0])
}

func TestMeasurePing(t *testing.T) {
	result, err := MeasurePing(context.Background(), echoServerAddrWs, 3)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Positive(t, result.TotalTime)
}

func TestMeasurePingNegativeCount(t *testing.T) {
	result, err := MeasurePing(context.Background(), echoServerAddrWs, -1)
	require.Error(t, err)
	assert.Nil(t, result)
}

func TestMeasureTextCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := MeasureText(ctx, echoServerAddrWs, []string{"hello"})
	require.Error(t, err)
}

// nonUpgradingURL returns a ws:// URL backed by a plain-HTTP server that never upgrades the
// connection, so the WebSocket handshake fails. handler shapes the failure (immediate 200 vs
// a slow response for timeout coverage). The server is cleaned up via t.Cleanup.
func nonUpgradingURL(t *testing.T, handler http.HandlerFunc) *url.URL {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse("ws" + strings.TrimPrefix(srv.URL, "http") + "/echo")
	require.NoError(t, err)
	return u
}

// TestMeasureDialErrors covers the three failure shapes for the one-shot measure funcs: a
// refused port, a plain-HTTP endpoint that fails the handshake, and a dial timeout against a
// slow server. Each must return a non-nil wrapped error and a nil Result.
func TestMeasureDialErrors(t *testing.T) {
	refused, err := url.Parse("ws://127.0.0.1:1/echo")
	require.NoError(t, err)

	noUpgrade := nonUpgradingURL(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // 200, never a 101 switch
	})

	slow := nonUpgradingURL(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never responds; the dial times out and the handler unblocks
	})

	cases := []struct {
		name   string
		target *url.URL
		opts   []Option
	}{
		{"connection refused", refused, []Option{WithTimeout(time.Second)}},
		{"plain HTTP handshake failure", noUpgrade, nil},
		{"dial timeout", slow, []Option{WithTimeout(100 * time.Millisecond)}},
	}

	for _, tc := range cases {
		t.Run("MeasureText/"+tc.name, func(t *testing.T) {
			result, responses, err := MeasureText(
				context.Background(), tc.target, []string{"hi"}, tc.opts...)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Nil(t, responses)
			assert.ErrorContains(t, err, "dial")
		})

		t.Run("MeasureJSON/"+tc.name, func(t *testing.T) {
			result, responses, err := MeasureJSON(
				context.Background(), tc.target, []any{map[string]any{"a": 1}}, tc.opts...)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Nil(t, responses)
		})

		t.Run("MeasurePing/"+tc.name, func(t *testing.T) {
			result, err := MeasurePing(context.Background(), tc.target, 1, tc.opts...)
			require.Error(t, err)
			assert.Nil(t, result)
		})
	}
}
