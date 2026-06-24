package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordResponse(t *testing.T) {
	t.Parallel()

	t.Run("compacts JSON to one line", func(t *testing.T) {
		var buf bytes.Buffer
		c := &Client{}
		c.SetResponseSink(&buf)
		require.NoError(t, c.writeResponseLine([]byte("{\n  \"foo\": \"bar\"\n}")))
		assert.Equal(t, "{\"foo\":\"bar\"}\n", buf.String())
	})

	t.Run("writes non-JSON verbatim with newline", func(t *testing.T) {
		var buf bytes.Buffer
		c := &Client{}
		c.SetResponseSink(&buf)
		require.NoError(t, c.writeResponseLine([]byte("plain text")))
		assert.Equal(t, "plain text\n", buf.String())
	})

	t.Run("nil sink is a no-op", func(t *testing.T) {
		c := &Client{}
		require.NoError(t, c.writeResponseLine([]byte("anything")))
	})

	t.Run("appends one line per call", func(t *testing.T) {
		var buf bytes.Buffer
		c := &Client{}
		c.SetResponseSink(&buf)
		require.NoError(t, c.writeResponseLine([]byte(`{"n":1}`)))
		require.NoError(t, c.writeResponseLine([]byte(`{"n":2}`)))
		assert.Equal(t, "{\"n\":1}\n{\"n\":2}\n", buf.String())
	})
}

func TestRecordResponseMeasure(t *testing.T) {
	t.Parallel()

	t.Run("records JSON-RPC response as compact line", func(t *testing.T) {
		var buf bytes.Buffer
		c := &Client{}
		c.SetResponseSink(&buf)
		result := &MeasurementResult{Response: map[string]any{"jsonrpc": "2.0", "result": "0x1"}}
		require.NoError(t, c.RecordResponse(result))

		line := strings.TrimSpace(buf.String())
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &decoded))
		assert.Equal(t, "2.0", decoded["jsonrpc"])
		assert.Equal(t, "0x1", decoded["result"])
	})

	t.Run("no response is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		c := &Client{}
		c.SetResponseSink(&buf)
		require.NoError(t, c.RecordResponse(&MeasurementResult{}))
		assert.Empty(t, buf.String())
	})

	t.Run("nil sink is a no-op", func(t *testing.T) {
		c := &Client{}
		require.NoError(t, c.RecordResponse(&MeasurementResult{Response: "x"}))
	})
}

func TestStreamSubscriptionRecordsToSink(t *testing.T) {
	// The --file sink records each response payload as an NDJSON line, independent of the
	// stdout output contract and verbosity. Runs quiet to prove recording is not gated on
	// the stdout print path.
	server := newSubscriptionTestServer(t)
	defer server.cleanup()

	var buf bytes.Buffer
	c := &Client{
		count:       2,
		mode:        ModeStream,
		textMessage: "start",
		quiet:       true,
	}
	require.NoError(t, c.Validate())
	c.SetResponseSink(&buf)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- c.StreamSubscription(ctx, server.wsURL) }()

	<-server.ready
	server.events <- `{"event":1}`
	server.events <- `{"event":2}`

	require.NoError(t, <-errCh)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	for i, line := range lines {
		var decoded map[string]any
		err := json.Unmarshal([]byte(line), &decoded)
		require.NoErrorf(t, err, "line %d not valid JSON: %q", i, line)
		assert.EqualValues(t, i+1, decoded["event"])
	}
}
