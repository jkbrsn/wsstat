package app

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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
		URL:          u,
		MessageCount: 1,
		//revive:disable:add-constant test fixture values
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
		//revive:enable:add-constant
	}
}

// subscriptionTestServer is a test server for testing subscription mode.
type subscriptionTestServer struct {
	wsURL   *url.URL
	events  chan<- string
	ready   <-chan struct{}
	cleanup func()
}

// newSubscriptionTestServer creates a new subscription test server.
func newSubscriptionTestServer(t *testing.T) subscriptionTestServer {
	t.Helper()

	events := make(chan string, 4) //revive:disable-line:add-constant test buffer size
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

func decodeJSONLine(t *testing.T, output string) map[string]any {
	t.Helper()
	trimmed := strings.TrimSpace(output)
	require.NotEmpty(t, trimmed)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(trimmed), &payload))
	return payload
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	require.Truef(t, ok, "expected map[string]any, got %T", value)
	return result
}

func asSlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	require.Truef(t, ok, "expected []any, got %T", value)
	return result
}
