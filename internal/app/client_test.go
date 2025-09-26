package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkbrsn/wsstat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscriptionTestServer is a test server for testing subscription mode.
type subscriptionTestServer struct {
	wsURL   *url.URL
	events  chan<- string
	ready   <-chan struct{}
	cleanup func()

	// server   *httptest.Server
	// upgrader websocket.Upgrader
}

// newSubscriptionTestServer creates a new subscription test server.
func newSubscriptionTestServer(t *testing.T) subscriptionTestServer {
	t.Helper()

	events := make(chan string, 4)
	done := make(chan struct{})
	ready := make(chan struct{})
	var once sync.Once
	closeReady := func() {
		once.Do(func() {
			close(ready)
		})
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			closeReady()
			return
		}
		defer func() {
			_ = conn.Close()
		}()

		if _, _, err := conn.ReadMessage(); err != nil {
			closeReady()
			return
		}
		closeReady()

		for {
			select {
			case <-done:
				return
			case msg, ok := <-events:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
					return
				}
			}
		}
	}))

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	cleanup := func() {
		closeReady()
		close(done)
		close(events)
		server.Close()
	}

	return subscriptionTestServer{
		wsURL:   wsURL,
		events:  events,
		ready:   ready,
		cleanup: cleanup,
	}
}

// TestParseHeaders verifies that parseHeaders correctly handles valid and invalid input.
func TestParseHeaders(t *testing.T) {
	t.Run("valid headers", func(t *testing.T) {
		hdrStr := "Content-Type: application/json, Authorization: Bearer token"
		h, err := parseHeaders(hdrStr)
		require.NoError(t, err)
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Equal(t, "Bearer token", h.Get("Authorization"))
	})

	t.Run("invalid header", func(t *testing.T) {
		_, err := parseHeaders("Content-Type")
		assert.Error(t, err)
	})

	t.Run("empty string", func(t *testing.T) {
		h, err := parseHeaders("")
		require.NoError(t, err)
		assert.Len(t, h, 0)
	})
}

// TestFormatPadding ensures the padding helpers behave as expected.
func TestFormatPadding(t *testing.T) {
	d := 5 * time.Millisecond
	assert.Equal(t, "      5ms", formatPadLeft(d)) // 6 leading spaces + "5ms"
	assert.Equal(t, "5ms     ", formatPadRight(d)) // "5ms" + 5 trailing spaces
}

func TestPostProcessTextResponse(t *testing.T) {
	t.Run("keeps plain text", func(t *testing.T) {
		c := &Client{Response: "not json"}
		require.NoError(t, c.postProcessTextResponse())
		assert.Equal(t, "not json", c.Response)
	})

	t.Run("decodes json rpc", func(t *testing.T) {
		payload := `{"jsonrpc":"2.0","result":"ok"}`
		c := &Client{Response: payload}
		require.NoError(t, c.postProcessTextResponse())
		asMap, ok := c.Response.(map[string]any)
		require.True(t, ok, "expected JSON-RPC response to decode into a map")
		assert.Equal(t, "ok", asMap["result"])
	})
}

// TestColorHelpers performs basic sanity checks on ANSI wrapped strings.
func TestColorHelpers(t *testing.T) {
	base := "txt"
	assert.Contains(t, customColor(1, 2, 3, base), "[38;2;1;2;3m")
	assert.Contains(t, colorWSOrange(base), "255;102;0m")
	assert.Contains(t, colorTeaGreen(base), "211;249;181m")
}

// TestHandleConnectionError checks error message classification.
func TestHandleConnectionError(t *testing.T) {
	tlsErr := errors.New("tls: first record does not look like a TLS handshake")
	msg := handleConnectionError(tlsErr, "wss://example.com").Error()
	assert.Contains(t, msg, "secure WS connection")

	genErr := errors.New("something went wrong")
	msg2 := handleConnectionError(genErr, "ws://example.com").Error()
	assert.NotContains(t, msg2, "secure WS connection")
}

// TestClientValidate exercises the validation logic for burst and mutually-exclusive flags.
func TestClientValidate(t *testing.T) {
	t.Run("negative count", func(t *testing.T) {
		c := &Client{Count: -1}
		assert.Error(t, c.Validate())
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		c := &Client{Count: 1, TextMessage: "hi", JSONMethod: "foo"}
		assert.Error(t, c.Validate())
	})

	t.Run("defaults to one when unset", func(t *testing.T) {
		c := &Client{}
		require.NoError(t, c.Validate())
		assert.Equal(t, 1, c.Count)
	})

	t.Run("valid", func(t *testing.T) {
		c := &Client{Count: 2, TextMessage: "hi"}
		require.NoError(t, c.Validate())
	})

	t.Run("subscribe unlimited allowed", func(t *testing.T) {
		c := &Client{Subscribe: true}
		require.NoError(t, c.Validate())
		assert.Equal(t, 0, c.Count)
	})

	t.Run("subscribe with explicit count ok", func(t *testing.T) {
		c := &Client{Count: 3, Subscribe: true}
		require.NoError(t, c.Validate())
	})

	t.Run("subscribe once coerces to one", func(t *testing.T) {
		c := &Client{SubscribeOnce: true}
		require.NoError(t, c.Validate())
		assert.True(t, c.Subscribe)
		assert.Equal(t, 1, c.Count)
	})

	t.Run("subscribe once forbids alternative counts", func(t *testing.T) {
		c := &Client{Count: 2, SubscribeOnce: true}
		assert.Error(t, c.Validate())
	})
}

func TestStreamSubscriptionRespectsCount(t *testing.T) {
	t.Parallel()

	server := newSubscriptionTestServer(t)
	defer server.cleanup()

	c := &Client{
		Count:       2,
		Subscribe:   true,
		TextMessage: "start",
		Quiet:       true,
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
	require.NotNil(t, c.Result)
	assert.Equal(t, 2, c.Result.MessageCount)
}

func TestStreamSubscriptionUnlimitedRequiresCancel(t *testing.T) {
	t.Parallel()

	server := newSubscriptionTestServer(t)
	defer server.cleanup()

	c := &Client{
		Subscribe:   true,
		TextMessage: "start",
		Quiet:       true,
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
	require.NotNil(t, c.Result)
	assert.GreaterOrEqual(t, c.Result.MessageCount, 2)
}

func TestPrintSubscriptionMessageBasic(t *testing.T) {
	msg := wsstat.SubscriptionMessage{
		Data:     []byte("{\"foo\":\"bar\"}"),
		Received: time.Date(2024, 1, 2, 3, 4, 5, 6, time.UTC),
		Size:     17,
	}
	c := &Client{Basic: true}

	r, w, err := os.Pipe()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = w

	require.NoError(t, c.printSubscriptionMessage(3, msg))

	require.NoError(t, w.Close())
	os.Stdout = stdout
	output, err := io.ReadAll(r)
	require.NoError(t, err)

	outStr := string(output)
	assert.Contains(t, outStr, "Message received")
	assert.NotContains(t, outStr, "foo")
}
