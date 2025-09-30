package app

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkbrsn/wsstat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionJSONOutput(t *testing.T) {
	t.Run("message metadata", func(t *testing.T) {
		client := &Client{format: formatJSON}
		msg := wsstat.SubscriptionMessage{
			MessageType: websocket.TextMessage,
			Data:        []byte(`{"foo":"bar"}`),
			Received:    time.Unix(0, 0).UTC(),
			Size:        len(`{"foo":"bar"}`),
		}
		output := captureStdoutFrom(t, func() error {
			return client.printSubscriptionMessage(5, msg)
		})
		payload := decodeJSONLine(t, output)
		assert.Equal(t, "subscription_message", payload["type"])
		assert.EqualValues(t, 5, payload["index"])
		assert.Equal(t, msg.Received.Format(time.RFC3339Nano), payload["timestamp"])
		assert.EqualValues(t, msg.Size, payload["size"])
		assert.Equal(t, "text", payload["message_type"])
		inner := asMap(t, payload["payload"])
		assert.Equal(t, "bar", inner["foo"])
	})

	t.Run("quiet omits metadata", func(t *testing.T) {
		client := &Client{format: formatJSON, quiet: true}
		msg := wsstat.SubscriptionMessage{Data: []byte("plain"), Received: time.Unix(0, 0).UTC()}
		output := captureStdoutFrom(t, func() error {
			return client.printSubscriptionMessage(1, msg)
		})
		payload := decodeJSONLine(t, output)
		assert.Equal(t, "subscription_message", payload["type"])
		assert.NotContains(t, payload, "index")
		assert.Equal(t, "plain", payload["payload"])
	})

	t.Run("summary", func(t *testing.T) {
		res := sampleResult(t)
		res.MessageCount = 3
		res.SubscriptionFirstEvent = 50 * time.Millisecond
		res.SubscriptionLastEvent = 150 * time.Millisecond
		res.Subscriptions = map[string]wsstat.SubscriptionStats{
			"alpha": {
				FirstEvent:       40 * time.Millisecond,
				LastEvent:        140 * time.Millisecond,
				MessageCount:     2,
				ByteCount:        64,
				MeanInterArrival: 20 * time.Millisecond,
			},
		}
		client := &Client{format: formatJSON, result: res}
		output := captureStdoutFrom(t, func() error {
			client.printSubscriptionSummary(res.URL)
			return nil
		})
		payload := decodeJSONLine(t, output)
		assert.Equal(t, "subscription_summary", payload["type"])
		assert.EqualValues(t, res.MessageCount, payload["total_messages"])
		assert.EqualValues(t, res.SubscriptionFirstEvent.Milliseconds(), payload["first_event_ms"])
		subs := asSlice(t, payload["subscriptions"])
		require.Len(t, subs, 1)
		entry := asMap(t, subs[0])
		assert.Equal(t, "alpha", entry["id"])
		assert.EqualValues(t, res.Subscriptions["alpha"].MessageCount, entry["messages"])
		assert.EqualValues(t, res.Subscriptions["alpha"].ByteCount, entry["bytes"])
		assert.EqualValues(t, res.Subscriptions["alpha"].MeanInterArrival.Milliseconds(),
			entry["mean_inter_arrival_ms"])
	})
}

func TestStreamSubscriptionRespectsCount(t *testing.T) {
	t.Parallel()

	server := newSubscriptionTestServer(t)
	defer server.cleanup()

	c := &Client{
		count:       2,
		subscribe:   true,
		textMessage: "start",
		quiet:       true,
	}
	require.NoError(t, c.Validate())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.StreamSubscription(ctx, server.wsURL)
	}()

	<-server.ready
	server.events <- "event-1"
	server.events <- "event-2"

	require.NoError(t, <-errCh)
	require.NotNil(t, c.result)
	assert.Equal(t, 2, c.result.MessageCount)
}

func TestStreamSubscriptionUnlimitedRequiresCancel(t *testing.T) {
	t.Parallel()

	server := newSubscriptionTestServer(t)
	defer server.cleanup()

	c := &Client{
		subscribe:   true,
		textMessage: "start",
		quiet:       true,
	}
	require.NoError(t, c.Validate())
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.StreamSubscription(ctx, server.wsURL)
	}()

	<-server.ready
	server.events <- "event-1"
	server.events <- "event-2"

	select {
	case err := <-errCh:
		t.Fatalf("subscription returned before cancellation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("subscription did not exit after cancellation")
	}
	require.NotNil(t, c.result)
	assert.GreaterOrEqual(t, c.result.MessageCount, 2)
}

func TestPrintSubscriptionMessageLevels(t *testing.T) {
	msg := wsstat.SubscriptionMessage{
		Data:     []byte("{\"foo\":\"bar\"}"),
		Received: time.Date(2024, 1, 2, 3, 4, 5, 6, time.UTC),
		Size:     17,
	}

	t.Run("default level", func(t *testing.T) {
		c := &Client{}
		output := captureStdoutFrom(t, func() error {
			return c.printSubscriptionMessage(3, msg)
		})
		assert.Contains(t, output, "[0003 @ 2024-01-02T03:04:05.000000006Z]")
		assert.NotContains(t, output, "bytes")
		assert.Contains(t, output, "\"foo\": \"bar\"")
	})

	t.Run("verbose level", func(t *testing.T) {
		c := &Client{verbosityLevel: 1}
		output := captureStdoutFrom(t, func() error {
			return c.printSubscriptionMessage(3, msg)
		})
		assert.Contains(t, output, "[0003 @ 2024-01-02T03:04:05.000000006Z]")
		assert.Contains(t, output, "17 bytes")
		assert.Contains(t, output, "\"foo\": \"bar\"")
	})
}

func TestPrintSubscriptionMessageRaw(t *testing.T) {
	msg := wsstat.SubscriptionMessage{Data: []byte("raw"), Received: time.Now()}
	c := &Client{format: "raw"}

	output := captureStdoutFrom(t, func() error {
		return c.printSubscriptionMessage(1, msg)
	})
	assert.Equal(t, "raw\n", output)
}
