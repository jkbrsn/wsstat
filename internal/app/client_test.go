package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

func captureStdoutFrom(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = r.Close() }()

	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()

	err = fn()
	require.NoError(t, err)
	require.NoError(t, w.Close())

	output, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(output)
}

func sampleResult(t *testing.T) *wsstat.Result {
	t.Helper()
	u, err := url.Parse("wss://example.test/ws")
	require.NoError(t, err)
	return &wsstat.Result{
		URL:            u,
		IPs:            []string{"192.0.2.1", "198.51.100.2"},
		MessageCount:   2,
		RequestHeaders: http.Header{"Sec-WebSocket-Version": {"13"}},
		ResponseHeaders: http.Header{
			"Server": {"demo"},
		},
		TLSState: &tls.ConnectionState{
			Version:          tls.VersionTLS13,
			CipherSuite:      tls.TLS_AES_128_GCM_SHA256,
			PeerCertificates: []*x509.Certificate{{}},
		},
	}
}

func sampleTimingResult(t *testing.T) *wsstat.Result {
	t.Helper()
	u, err := url.Parse("wss://example.test/ws")
	require.NoError(t, err)
	return &wsstat.Result{
		URL:              u,
		MessageCount:     1,
		DNSLookup:        10 * time.Millisecond,
		TCPConnection:    20 * time.Millisecond,
		TLSHandshake:     30 * time.Millisecond,
		WSHandshake:      40 * time.Millisecond,
		MessageRTT:       50 * time.Millisecond,
		DNSLookupDone:    10 * time.Millisecond,
		TCPConnected:     30 * time.Millisecond,
		TLSHandshakeDone: 60 * time.Millisecond,
		WSHandshakeDone:  80 * time.Millisecond,
		TotalTime:        120 * time.Millisecond,
	}
}

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
		h, err := parseHeaders([]string{
			"Content-Type: application/json",
			"Authorization: Bearer token",
		})
		require.NoError(t, err)
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Equal(t, "Bearer token", h.Get("Authorization"))
	})

	t.Run("invalid header", func(t *testing.T) {
		_, err := parseHeaders([]string{"Content-Type"})
		assert.Error(t, err)
	})

	t.Run("empty string", func(t *testing.T) {
		h, err := parseHeaders(nil)
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

func TestPrintRequestDetailsVerbosityLevels(t *testing.T) {
	res := sampleResult(t)

	t.Run("level0", func(t *testing.T) {
		output := captureStdoutFrom(t, func() error {
			client := &Client{Result: res}
			return client.PrintRequestDetails()
		})
		assert.Contains(t, output, "URL")
		assert.NotContains(t, output, "Target")
		assert.NotContains(t, output, "Request headers")
	})

	t.Run("level1", func(t *testing.T) {
		output := captureStdoutFrom(t, func() error {
			client := &Client{Result: res, VerbosityLevel: 1}
			return client.PrintRequestDetails()
		})
		assert.Contains(t, output, "Target")
		assert.Contains(t, output, "Messages sent")
		assert.NotContains(t, output, "Request headers")
	})

	t.Run("level2", func(t *testing.T) {
		output := captureStdoutFrom(t, func() error {
			client := &Client{Result: res, VerbosityLevel: 2}
			return client.PrintRequestDetails()
		})
		assert.Contains(t, output, "Request headers")
		assert.Contains(t, output, "Response headers")
		assert.Contains(t, output, "Certificate")
	})
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
		c := &Client{Count: 1, TextMessage: "hi", RPCMethod: "foo"}
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

	t.Run("invalid format", func(t *testing.T) {
		c := &Client{Format: "xml"}
		assert.Error(t, c.Validate())
	})

	t.Run("negative buffer", func(t *testing.T) {
		c := &Client{Buffer: -1}
		assert.Error(t, c.Validate())
	})

	t.Run("negative summary interval", func(t *testing.T) {
		c := &Client{SummaryInterval: -1}
		assert.Error(t, c.Validate())
	})
}

func TestPrintTimingResultsVerbosityLevels(t *testing.T) {
	base := sampleTimingResult(t)
	ctxURL, err := url.Parse("wss://example.test/ws")
	require.NoError(t, err)

	t.Run("level0", func(t *testing.T) {
		client := &Client{Result: base, Count: 1}
		output := captureStdoutFrom(t, func() error {
			return client.PrintTimingResults(ctxURL)
		})
		assert.Contains(t, output, "Round-trip time")
		assert.NotContains(t, output, "DNS Lookup    TCP Connection")
	})

	t.Run("level1", func(t *testing.T) {
		client := &Client{Result: base, Count: 1, VerbosityLevel: 1}
		output := captureStdoutFrom(t, func() error {
			return client.PrintTimingResults(ctxURL)
		})
		assert.Contains(t, output, "DNS Lookup    TCP Connection")
	})

	t.Run("level2", func(t *testing.T) {
		client := &Client{Result: base, Count: 1, VerbosityLevel: 2}
		output := captureStdoutFrom(t, func() error {
			return client.PrintTimingResults(ctxURL)
		})
		assert.Contains(t, output, "DNS Lookup    TCP Connection")
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
		c := &Client{VerbosityLevel: 1}
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
	c := &Client{Format: "raw"}

	output := captureStdoutFrom(t, func() error {
		return c.printSubscriptionMessage(1, msg)
	})
	assert.Equal(t, "raw\n", output)
}
