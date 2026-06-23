package wsstat

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	echoServer       *httptest.Server // shared echo server on a random port
	echoServerAddrWs *url.URL         // ws:// URL to the echo server's /echo path
	echoServerPort   string           // the echo server's port, for resolve-override tests
)

// TestMain starts a single shared echo server on a random port and runs the tests.
// httptest.NewServer is listening before it returns, so no readiness poll is needed.
func TestMain(m *testing.M) {
	echoServer = httptest.NewServer(http.HandlerFunc(echoHandler))
	defer echoServer.Close()

	host := strings.TrimPrefix(echoServer.URL, "http://")
	_, echoServerPort, _ = net.SplitHostPort(host)
	echoServerAddrWs = &url.URL{Scheme: "ws", Host: host, Path: "/echo"}

	m.Run()
}

// echoHostPort builds a "host:port" key on the shared echo server's port, used to key
// WithResolves overrides at a known port without hardcoding one.
func echoHostPort(host string) string { return net.JoinHostPort(host, echoServerPort) }

// echoHostURL builds a ws:// echo URL for host on the shared echo server's port.
func echoHostURL(host string) string { return "ws://" + echoHostPort(host) + "/echo" }

func TestNew(t *testing.T) {
	ws := New()
	defer ws.Close()

	assert.NotNil(t, ws)
	assert.NotNil(t, ws.httpClient)
	assert.NotNil(t, ws.result)
	assert.NotNil(t, ws.timings)
	assert.NotNil(t, ws.readChan)
	assert.NotNil(t, ws.writeChan)
	assert.NotNil(t, ws.ctx)
	assert.NotNil(t, ws.cancel)
}

func TestWithTLSConfig(t *testing.T) {
	t.Run("insecure skip verify is applied", func(t *testing.T) {
		tlsConf := &tls.Config{
			InsecureSkipVerify: true,
		}
		ws := New(WithTLSConfig(tlsConf))
		defer ws.Close()

		require.NotNil(t, ws.tlsConf)
		assert.True(t, ws.tlsConf.InsecureSkipVerify)
	})

	t.Run("nil config uses defaults", func(t *testing.T) {
		ws := New()
		defer ws.Close()

		assert.Nil(t, ws.tlsConf)
	})

	t.Run("custom server name", func(t *testing.T) {
		tlsConf := &tls.Config{
			ServerName: "custom.example.com",
		}
		ws := New(WithTLSConfig(tlsConf))
		defer ws.Close()

		require.NotNil(t, ws.tlsConf)
		assert.Equal(t, "custom.example.com", ws.tlsConf.ServerName)
	})
}

func TestWithTimeout(t *testing.T) {
	t.Run("custom timeout is applied", func(t *testing.T) {
		ws := New(WithTimeout(100 * time.Millisecond))
		defer ws.Close()

		assert.Equal(t, 100*time.Millisecond, ws.timeout)
	})

	t.Run("default timeout when not specified", func(t *testing.T) {
		ws := New()
		defer ws.Close()

		assert.Equal(t, defaultTimeout, ws.timeout)
	})

	t.Run("zero timeout is allowed", func(t *testing.T) {
		ws := New(WithTimeout(0))
		defer ws.Close()

		assert.Equal(t, time.Duration(0), ws.timeout)
	})
}

func TestWithBufferSize(t *testing.T) {
	t.Run("custom buffer size is applied", func(t *testing.T) {
		ws := New(WithBufferSize(16))
		defer ws.Close()

		assert.Equal(t, 16, cap(ws.readChan))
		assert.Equal(t, 16, cap(ws.writeChan))
	})

	t.Run("default buffer size", func(t *testing.T) {
		ws := New()
		defer ws.Close()

		assert.Equal(t, defaultChanBufferSize, cap(ws.readChan))
		assert.Equal(t, defaultChanBufferSize, cap(ws.writeChan))
	})

	t.Run("zero buffer size creates unbuffered channels", func(t *testing.T) {
		ws := New(WithBufferSize(0))
		defer ws.Close()

		assert.Equal(t, 0, cap(ws.readChan))
		assert.Equal(t, 0, cap(ws.writeChan))
	})
}

func TestMultipleOptions(t *testing.T) {
	t.Run("all options are applied together", func(t *testing.T) {
		tlsConf := &tls.Config{InsecureSkipVerify: true}
		ws := New(
			WithTimeout(200*time.Millisecond),
			WithTLSConfig(tlsConf),
			WithBufferSize(32),
		)
		defer ws.Close()

		assert.Equal(t, 200*time.Millisecond, ws.timeout)
		require.NotNil(t, ws.tlsConf)
		assert.True(t, ws.tlsConf.InsecureSkipVerify)
		assert.Equal(t, 32, cap(ws.readChan))
		assert.Equal(t, 32, cap(ws.writeChan))
	})

	t.Run("options order does not matter", func(t *testing.T) {
		tlsConf := &tls.Config{ServerName: "test.example.com"}

		ws1 := New(
			WithBufferSize(10),
			WithTimeout(50*time.Millisecond),
			WithTLSConfig(tlsConf),
		)
		defer ws1.Close()

		ws2 := New(
			WithTLSConfig(tlsConf),
			WithTimeout(50*time.Millisecond),
			WithBufferSize(10),
		)
		defer ws2.Close()

		assert.Equal(t, ws1.timeout, ws2.timeout)
		assert.Equal(t, ws1.tlsConf.ServerName, ws2.tlsConf.ServerName)
		assert.Equal(t, cap(ws1.readChan), cap(ws2.readChan))
	})
}

func TestDial(t *testing.T) {
	testStart := time.Now()
	ws := New()
	defer ws.Close()

	err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	assert.NotNil(t, ws.conn.Load())
	validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)
}

func TestWriteReadClose(t *testing.T) {
	testStart := time.Now()
	ws := New()
	defer func() {
		ws.Close()
		validateCloseResult(ws, getFunctionName(), t)
	}()

	err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

	message := []byte("Hello, world!")
	ws.WriteMessage(TextMessage, message)
	_, receivedMessage, err := ws.ReadMessage()
	assert.NoError(t, err)
	assert.Equal(t, message, receivedMessage, "Received message does not match sent message")

	// Call close before defer, since Close calls calculateResult
	ws.Close()

	validateOneHitResult(ws, getFunctionName(), t)
}

func TestBufferedReadWrite(t *testing.T) {
	testStart := time.Now()

	t.Run("No reads", func(t *testing.T) {
		ws := New()
		defer func() {
			ws.Close()
			validateCloseResult(ws, getFunctionName(), t)
		}()

		err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
		assert.NoError(t, err)
		validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

		message := []byte("Hello, world!")
		ws.WriteMessage(TextMessage, message)
		ws.WriteMessage(TextMessage, message)
		ws.WriteMessage(TextMessage, message)
		time.Sleep(10 * time.Millisecond) // Wait for messages to be sent

		result := ws.ExtractResult()
		assert.NotNil(t, result)
		assert.Greater(t, result.TotalTime, time.Duration(0),
			"Exepcted valid TotalTime despite no reads")
		assert.Equal(t, time.Duration(0), result.MessageRTT, "Expected 0 MessageRTT with no reads")
	})

	t.Run("Writes and reads", func(t *testing.T) {
		ws := New()
		defer func() {
			ws.Close()
			validateCloseResult(ws, getFunctionName(), t)
		}()

		err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
		assert.NoError(t, err)
		validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

		message := []byte("Hello, world!")
		messageCount := 75
		for range make([]struct{}, messageCount) {
			ws.WriteMessage(TextMessage, message)
		}
		time.Sleep(25 * time.Millisecond) // Wait for messages to be sent

		result := ws.ExtractResult()
		assert.NotNil(t, result)
		assert.Greater(t, result.TotalTime, time.Duration(0),
			"Exepcted valid TotalTime despite no reads")
		assert.Equal(t, time.Duration(0), result.MessageRTT, "Expected 0 MessageRTT with no reads")
		assert.Zero(t, result.MessageCount, "Expected 0 MessageCount with no reads")

		for range messageCount {
			_, receivedMessage, err := ws.ReadMessage()
			assert.NoError(t, err)
			assert.Equal(t, message, receivedMessage,
				"Received message does not match sent message")
		}

		result = ws.ExtractResult()
		assert.NotNil(t, result)
		assert.Greater(t, result.TotalTime, time.Duration(0), "Exepcted valid TotalTime")
		assert.Greater(t, result.MessageRTT, time.Duration(0), "Exepcted valid MessageRTT")
		assert.Equal(t, messageCount, result.MessageCount, "Expected correct MessageCount")
	})
}

func TestSubscribeReceivesMessage(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("hello-sub"),
	})
	require.NoError(t, err)

	select {
	case msg := <-sub.Updates():
		assert.Equal(t, "hello-sub", string(msg.Data))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscription message")
	}

	require.Eventually(t, func() bool {
		return sub.MessageCount() >= 1
	}, time.Second, 10*time.Millisecond)

	sub.Cancel()
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("subscription did not close after cancel")
	}

	ws.Close()

	result := ws.ExtractResult()
	require.NotNil(t, result.Subscriptions)
	stats, ok := result.Subscriptions[sub.ID]
	require.True(t, ok, "expected subscription stats to be archived")
	assert.EqualValues(t, 1, stats.MessageCount)
	assert.Greater(t, stats.FirstEvent, time.Duration(0))
	assert.GreaterOrEqual(t, stats.LastEvent, stats.FirstEvent)
}

func TestSubscriptionSurvivesIdleBeyondTimeout(t *testing.T) {
	// Regression: the per-read dial/read timeout must not tear down a long-lived
	// subscription that is merely idle. A 5s (here 100ms) silence used to finalize
	// every active subscription with a deadline error and close the connection.
	ws := New(WithTimeout(100 * time.Millisecond))
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("first"),
	})
	require.NoError(t, err)

	select {
	case msg := <-sub.Updates():
		assert.Equal(t, "first", string(msg.Data))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial subscription message")
	}

	// Stay idle well beyond the read timeout; the subscription must not be finalized.
	select {
	case <-sub.Done():
		t.Fatal("subscription torn down while idle past the read timeout")
	case <-time.After(500 * time.Millisecond):
	}

	// The stream is still live: a later message is still delivered.
	ws.WriteMessage(TextMessage, []byte("later"))
	select {
	case msg := <-sub.Updates():
		assert.Equal(t, "later", string(msg.Data))
	case <-time.After(2 * time.Second):
		t.Fatal("subscription stopped receiving after the idle period")
	}

	sub.Cancel()
	<-sub.Done()
}

func TestSubscriptionDecodeErrorDoesNotLeak(t *testing.T) {
	// A decode error on a frame a subscription never matched must not be delivered to it;
	// only the claiming subscription sees a frame (and its decode error).
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	// subA never matches and its decoder always errors.
	subA, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("frame-a"),
		decoder:     func(int, []byte) (any, error) { return nil, errors.New("boom") },
		matcher:     func(int, []byte, any) bool { return false },
	})
	require.NoError(t, err)

	// subB claims only its own echo.
	subB, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("frame-b"),
		matcher:     func(_ int, data []byte, _ any) bool { return string(data) == "frame-b" },
	})
	require.NoError(t, err)

	select {
	case msg := <-subB.Updates():
		require.NoError(t, msg.Err)
		assert.Equal(t, "frame-b", string(msg.Data))
	case <-time.After(2 * time.Second):
		t.Fatal("subB did not receive its frame")
	}

	// subA must not have received the unrelated decode error.
	select {
	case msg := <-subA.Updates():
		t.Fatalf("subA received an unmatched frame: data=%q err=%v", string(msg.Data), msg.Err)
	case <-time.After(200 * time.Millisecond):
	}
	assert.EqualValues(t, 0, subA.MessageCount())

	subA.Cancel()
	subB.Cancel()
	<-subA.Done()
	<-subB.Done()
}

func TestDialContextNilContext(t *testing.T) {
	ws := New()
	defer ws.Close()
	//nolint:staticcheck // passing nil to verify the guard returns an error, not a panic
	err := ws.DialContext(nil, echoServerAddrWs, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil context")
}

func TestReadMessageDoesNotMaskCloseError(t *testing.T) {
	// Regression: readPump buffers an inbound error then closes, so a read issued after the
	// closed flag flips must drain the buffered close error rather than report plain ErrClosed.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusInternalError, "boom")
	}))
	defer server.Close()
	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), wsURL, http.Header{}))

	// Wait until the read pump has observed the close and finalized; by then the close error
	// is already buffered in readChan and the closed flag is set.
	require.Eventually(t, ws.closed.Load, 2*time.Second, 5*time.Millisecond)

	_, _, err = ws.ReadMessage()
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrClosed), "real close error masked as ErrClosed: %v", err)
	assert.Contains(t, err.Error(), "unexpected close error")
}

func TestSubscribeOnceReturnsFirstMessage(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	msg, err := ws.SubscribeOnce(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("hello-once"),
	})
	require.NoError(t, err)
	assert.Equal(t, "hello-once", string(msg.Data))

	result := ws.ExtractResult()
	require.NotNil(t, result.Subscriptions)
	var found bool
	for _, stats := range result.Subscriptions {
		if stats.MessageCount == 1 {
			found = true
		}
	}
	assert.True(t, found, "expected archived subscription stats with one message")
}

func TestSubscriptionMatcherFallThrough(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("init"),
		matcher: func(int, []byte, any) bool {
			return false
		},
	})
	require.NoError(t, err)
	defer func() {
		sub.Cancel()
		<-sub.Done()
		ws.Close()
	}()

	// Initial echo should remain on the legacy read path.
	_, initial, err := ws.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "init", string(initial))

	ws.WriteMessage(TextMessage, []byte("ping"))
	_, data, err := ws.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "ping", string(data))

	select {
	case msg := <-sub.Updates():
		t.Fatalf("subscription unexpectedly claimed message %q", string(msg.Data))
	case <-time.After(100 * time.Millisecond):
		// Expected: no subscription delivery
	}

	assert.EqualValues(t, 0, sub.MessageCount())
}

func TestSubscriptionCancelWithoutTraffic(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{})
	require.NoError(t, err)

	sub.Cancel()
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("subscription did not close on cancel")
	}

	ws.Close()
	result := ws.ExtractResult()
	if result.Subscriptions != nil {
		stats, ok := result.Subscriptions[sub.ID]
		if ok {
			assert.EqualValues(t, 0, stats.MessageCount)
		}
	}
}

func TestPingPong(t *testing.T) {
	testStart := time.Now()
	ws := New()
	defer func() {
		ws.Close()
		validateCloseResult(ws, getFunctionName(), t)
	}()

	err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

	err = ws.PingPong()
	assert.NoError(t, err)

	result := ws.ExtractResult()
	assert.NotNil(t, result)
	assert.Greater(t, result.MessageRTT, time.Duration(0), "Expected valid MessageRTT")
	assert.Equal(t, 1, result.MessageCount, "Expected 1 MessageCount")

	// Call close before defer, since Close calls calculateResult
	ws.Close()

	validateOneHitResult(ws, getFunctionName(), t)
}

func TestReadAfterClose(t *testing.T) {
	ws := New()
	ws.Close()

	_, _, err := ws.ReadMessage()
	require.ErrorIs(t, err, ErrClosed)

	_, err = ws.ReadMessageJSON()
	require.ErrorIs(t, err, ErrClosed)

	err = ws.PingPong()
	require.ErrorIs(t, err, ErrClosed)
}

// TestOperationsBeforeDial verifies the not-established sentinel on a fresh, undialed instance.
func TestOperationsBeforeDial(t *testing.T) {
	ws := New()
	defer ws.Close()

	_, _, err := ws.ReadMessage()
	require.ErrorIs(t, err, ErrConnectionNotEstablished)

	_, err = ws.ReadMessageJSON()
	require.ErrorIs(t, err, ErrConnectionNotEstablished)

	err = ws.PingPong()
	require.ErrorIs(t, err, ErrConnectionNotEstablished)
}

// TestCloseHandshakeStatus verifies that Close performs the RFC 6455 two-way closing
// handshake: the server must observe a clean StatusNormalClosure (1000), not an abrupt
// 1006. Regression test for the ungraceful-close defect fixed by the coder migration.
func TestCloseHandshakeStatus(t *testing.T) {
	closeStatus := make(chan websocket.StatusCode, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(-1)
		ctx := r.Context()
		for {
			mt, msg, err := conn.Read(ctx)
			if err != nil {
				closeStatus <- websocket.CloseStatus(err)
				return
			}
			if err := conn.Write(ctx, mt, msg); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	ws := New()
	require.NoError(t, ws.DialContext(context.Background(), wsURL, http.Header{}))
	ws.WriteMessage(TextMessage, []byte("hello"))
	_, _, err = ws.ReadMessage()
	require.NoError(t, err)
	ws.Close()

	select {
	case status := <-closeStatus:
		assert.Equal(t, websocket.StatusNormalClosure, status,
			"server should observe a clean 1000 close, not 1006")
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe a close status")
	}
}

// TestCloseGraceBound verifies that Close does not stall on a write-only / non-echoing
// peer. coder's Conn.Close waits up to a hard-coded 5s for the peer's Close echo; wsstat
// bounds that wait to closeGrace and forces the socket shut, so teardown stays prompt.
func TestCloseGraceBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		// Write-only / non-echoing: pump frames, never read. The client's Close frame is
		// never processed and never echoed, so the close handshake cannot complete. Exits
		// when a write fails (client gone).
		ctx := context.Background()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if err := conn.Write(ctx, websocket.MessageText, []byte("tick")); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	grace := 500 * time.Millisecond
	ws := New(WithCloseGrace(grace))
	require.NoError(t, ws.DialContext(context.Background(), wsURL, http.Header{}))

	start := time.Now()
	done := make(chan struct{})
	go func() {
		ws.Close()
		close(done)
	}()

	select {
	case <-done:
		assert.Less(t, time.Since(start), 3*time.Second,
			"Close should bound the handshake to ~closeGrace, not coder's 5s")
	case <-time.After(5 * time.Second):
		t.Fatal("Close stalled past the grace bound")
	}
}

func TestResultFormat(t *testing.T) {
	ws := New()
	defer ws.Close()

	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
	ws.WriteMessage(TextMessage, []byte("test"))
	_, _, err := ws.ReadMessage()
	require.NoError(t, err)
	ws.Close()

	result := ws.ExtractResult()

	t.Run("compact format with %s", func(t *testing.T) {
		output := fmt.Sprintf("%s", result)
		assert.Contains(t, output, "DNSLookup:")
		assert.Contains(t, output, "TCPConnection:")
		assert.Contains(t, output, "WSHandshake:")
		assert.Contains(t, output, "MessageRTT:")
		assert.Contains(t, output, "TotalTime:")
		assert.Contains(t, output, "ms")
		// Should be comma-separated
		assert.Contains(t, output, ", ")
	})

	t.Run("compact format with %v", func(t *testing.T) {
		output := fmt.Sprintf("%v", result)
		assert.Contains(t, output, "DNSLookup:")
		assert.Contains(t, output, "ms")
	})

	t.Run("verbose format with %+v", func(t *testing.T) {
		output := fmt.Sprintf("%+v", result)
		// URL section
		assert.Contains(t, output, "URL")
		assert.Contains(t, output, "Scheme:")
		assert.Contains(t, output, "Host:")
		assert.Contains(t, output, "Port:")
		// IP section
		assert.Contains(t, output, "IP")
		// Headers section
		assert.Contains(t, output, "Request headers")
		assert.Contains(t, output, "Response headers")
		// Durations section
		assert.Contains(t, output, "DNS lookup:")
		assert.Contains(t, output, "TCP connection:")
		assert.Contains(t, output, "WS handshake:")
		assert.Contains(t, output, "Total:")
		// Should be multi-line
		lines := strings.Split(output, "\n")
		assert.Greater(t, len(lines), 10, "verbose format should have multiple lines")
	})

	t.Run("hostPort helper", func(t *testing.T) {
		wsURL, _ := url.Parse("ws://example.com/path")
		host, port := hostPort(wsURL)
		assert.Equal(t, "example.com", host)
		assert.Equal(t, "80", port)

		wssURL, _ := url.Parse("wss://example.com:9000/path")
		host, port = hostPort(wssURL)
		assert.Equal(t, "example.com", host)
		assert.Equal(t, "9000", port)
	})
}

func TestResultFormatWithZeroValues(t *testing.T) {
	ws := New()
	defer ws.Close()

	// Dial but close immediately
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
	ws.Close()
	result := ws.ExtractResult()

	// After close, TotalTime should be set
	t.Run("has valid TotalTime after close", func(t *testing.T) {
		output := fmt.Sprintf("%s", result)
		assert.Greater(t, result.TotalTime, time.Duration(0))
		assert.NotContains(t, output, "TotalTime: - ms")
	})
}

func TestSubscriptionByteCount(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("byte-count-test"),
	})
	require.NoError(t, err)

	select {
	case msg := <-sub.Updates():
		assert.Equal(t, "byte-count-test", string(msg.Data))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscription message")
	}

	require.Eventually(t, func() bool {
		return sub.ByteCount() >= uint64(len("byte-count-test"))
	}, time.Second, 10*time.Millisecond)

	assert.EqualValues(t, len("byte-count-test"), sub.ByteCount())

	sub.Cancel()
	<-sub.Done()
}

func TestSubscriptionUnsubscribe(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: TextMessage,
		Payload:     []byte("unsubscribe-test"),
	})
	require.NoError(t, err)

	// Consume the echo
	select {
	case <-sub.Updates():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	// Test Unsubscribe (alias for Cancel)
	sub.Unsubscribe()

	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("subscription did not close after unsubscribe")
	}
}

// Helpers

// getFunctionName returns the name of the calling function.
func getFunctionName() string {
	pc, _, _, _ := runtime.Caller(1)
	return strings.TrimPrefix(runtime.FuncForPC(pc).Name(), "main.")
}

// echoHandler echoes back any received message. It backs the shared httptest echo server
// and accepts a WebSocket upgrade on any path (tests dial /echo by convention).
func echoHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		log.Print("accept:", err)
		return
	}
	defer func() {
		_ = conn.CloseNow()
	}()
	conn.SetReadLimit(-1)
	ctx := r.Context()
	for {
		mt, message, err := conn.Read(ctx)
		if err != nil {
			// Only print error if it's not a normal/expected closure
			status := websocket.CloseStatus(err)
			if status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway {
				log.Println("read:", err)
			}
			break
		}
		if err := conn.Write(ctx, mt, message); err != nil {
			log.Println("write:", err)
			break
		}
	}
}

// Validation of WSStat results after Dial has been called
func validateDialResult(testStart time.Time, ws *WSStat, u *url.URL, msg string, t *testing.T) {
	assert.Greater(t,
		ws.timings.dnsLookupDone.Sub(testStart),
		time.Duration(0),
		"Invalid DNSLookupDone time in %s", msg)
	assert.Greater(t,
		ws.timings.tcpConnected.Sub(ws.timings.dnsLookupDone),
		time.Duration(0),
		"Invalid TCPConnected time in %s", msg)

	if strings.Contains(u.String(), "wss://") {
		assert.Greater(t,
			ws.timings.tlsHandshakeDone.Sub(ws.timings.dnsLookupDone),
			time.Duration(0),
			"Invalid TLSHandshakeDone time in %s", msg)
	}

	assert.Greater(t,
		ws.timings.wsHandshakeDone.Sub(ws.timings.tcpConnected),
		time.Duration(0),
		"Invalid WSHandshakeDone time in %s", msg)
}

// validateOneHitResult validates Result after both write and read have been called
func validateOneHitResult(ws *WSStat, msg string, t *testing.T) {
	assert.Greater(t, ws.result.MessageRTT, time.Duration(0), "Invalid MessageRTT time in %s", msg)
	assert.Greater(t, ws.result.FirstMessageResponse, time.Duration(0),
		"Invalid FirstMessageResponse time in %s", msg)
}

// validateCloseResult validates Results after Close has been called
func validateCloseResult(ws *WSStat, msg string, t *testing.T) {
	assert.Greater(t, ws.result.TotalTime, time.Duration(0), "Invalid TotalTime time in %s", msg)
}

// TestRaceConditionOnClose tests that closing while messages are being sent/received
// doesn't cause a race condition or panic when accessing ws.conn
func TestRaceConditionOnClose(t *testing.T) {
	// Run this test multiple times to increase chances of hitting the race
	for i := range 50 {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))

			err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			// Send multiple messages concurrently to fill the write buffer
			done := make(chan bool)
			go func() {
				for j := range 10 {
					msg := fmt.Appendf(nil, "test message %d", j)
					ws.WriteMessage(TextMessage, msg)
					// Small delay to allow some messages to be sent
					time.Sleep(time.Microsecond)
				}
				close(done)
			}()

			// Close immediately while messages are still being sent
			// This should trigger the race condition where writePump tries to access
			// ws.conn after it's been set to nil
			time.Sleep(time.Microsecond * 100)
			ws.Close()

			// Wait for the sender goroutine to finish
			<-done

			// If we get here without a panic, the fix is working
		})
	}
}

// TestRaceConditionWithSubscription tests race condition with subscriptions active
func TestRaceConditionWithSubscription(t *testing.T) {
	for i := range 20 {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))

			err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			// Create a subscription
			ctx := t.Context()

			sub, err := ws.Subscribe(ctx, SubscriptionOptions{
				MessageType: TextMessage,
				Buffer:      10,
			})
			require.NoError(t, err)

			// Send messages rapidly
			go func() {
				for j := range 5 {
					ws.WriteMessage(TextMessage, fmt.Appendf(nil, "sub message %d", j))
					time.Sleep(time.Microsecond * 10)
				}
			}()

			// Try to read from subscription
			go func() {
				for range 3 {
					select {
					case <-sub.Updates():
						// Message received
					case <-time.After(time.Millisecond * 10):
						return
					}
				}
			}()

			// Close while subscription is active
			time.Sleep(time.Microsecond * 50)
			ws.Close()
		})
	}
}

// TestConcurrentWritesAndClose tests multiple goroutines writing while closing
func TestConcurrentWritesAndClose(t *testing.T) {
	for i := range 30 {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))

			err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			// Multiple writers
			for j := range 3 {
				go func(id int) {
					for k := range 5 {
						msg := fmt.Appendf(nil, "writer %d msg %d", id, k)
						ws.WriteMessage(TextMessage, msg)
						time.Sleep(time.Microsecond * 5)
					}
				}(j)
			}

			// Close after a very short delay
			time.Sleep(time.Microsecond * 20)
			ws.Close()
		})
	}
}

// hammerExtractResult calls ExtractResult in a tight loop until stop is closed.
func hammerExtractResult(ws *WSStat, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
			_ = ws.ExtractResult().TotalTime
		}
	}
}

// TestRaceExtractResultDuringClose exercises the ws.result race: ExtractResult calculates
// and copies Result while Close finalizes it concurrently. The pre-existing suite never
// raced these two paths, which is why the data race on ws.result stayed latent. Must pass
// under -race.
func TestRaceExtractResultDuringClose(t *testing.T) {
	for i := range 30 {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))
			err := ws.DialContext(context.Background(), echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			var wg sync.WaitGroup

			// Writer to populate timings concurrently with the extractors.
			wg.Go(func() {
				for j := range 10 {
					ws.WriteMessage(TextMessage, fmt.Appendf(nil, "msg %d", j))
					time.Sleep(time.Microsecond * 5)
				}
			})

			// Extractors hammer ExtractResult so a calculate/copy overlaps Close's finalize.
			stop := make(chan struct{})
			for range 3 {
				wg.Go(func() { hammerExtractResult(ws, stop) })
			}

			time.Sleep(time.Microsecond * 50)
			ws.Close()
			close(stop)
			wg.Wait()

			final := ws.ExtractResult()
			assert.Greater(t, final.TotalTime, time.Duration(0))
		})
	}
}

// TestWithSubprotocols verifies that an offered subprotocol is negotiated and surfaced in
// Result.Subprotocol.
func TestWithSubprotocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			Subprotocols:       []string{"chat.v1"},
		})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		for {
			mt, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, mt, msg); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	ws := New(WithSubprotocols([]string{"chat.v1"}))
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), wsURL, http.Header{}))

	result := ws.ExtractResult()
	assert.Equal(t, "chat.v1", result.Subprotocol)
}

// TestWithCompression verifies that enabling compression negotiates permessage-deflate and that
// the negotiated extension is surfaced in Result.Compression.
func TestWithCompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionContextTakeover,
		})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		for {
			mt, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, mt, msg); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)

	ws := New(WithCompression(true))
	defer ws.Close()
	require.NoError(t, ws.DialContext(context.Background(), wsURL, http.Header{}))

	result := ws.ExtractResult()
	assert.Contains(t, result.Compression, "permessage-deflate")
}

func TestWithReadLimit(t *testing.T) {
	message := []byte("this message comfortably exceeds the tiny read limit")

	t.Run("message above limit errors", func(t *testing.T) {
		ws := New(WithReadLimit(8))
		defer ws.Close()
		require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
		ws.WriteMessage(TextMessage, message)
		_, _, err := ws.ReadMessage()
		require.Error(t, err)
	})

	t.Run("negative limit disables the cap", func(t *testing.T) {
		ws := New(WithReadLimit(-1))
		defer ws.Close()
		require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
		ws.WriteMessage(TextMessage, message)
		_, got, err := ws.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, message, got)
	})
}

func TestWithResolves(t *testing.T) {
	t.Run("basic resolve override", func(t *testing.T) {
		// Create WSStat with resolve override pointing to localhost
		resolves := map[string]string{
			echoHostPort("example.com"): "127.0.0.1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		// Dial using the overridden hostname
		targetURL, err := url.Parse(echoHostURL("example.com"))
		require.NoError(t, err)

		err = ws.DialContext(context.Background(), targetURL, nil)
		require.NoError(t, err)

		// Verify connection succeeded (proves override worked)
		err = ws.PingPong()
		assert.NoError(t, err)

		result := ws.ExtractResult()
		assert.Equal(t, targetURL, result.URL)
		// result.IPs holds the actually-connected address, which is the override.
		assert.Equal(t, []string{"127.0.0.1"}, result.IPs)
	})

	t.Run("multiple hosts", func(t *testing.T) {
		// Multiple overrides for different hosts
		resolves := map[string]string{
			echoHostPort("example.com"): "127.0.0.1",
			echoHostPort("other.com"):   "127.0.0.1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		// Use the first override
		targetURL, err := url.Parse(echoHostURL("example.com"))
		require.NoError(t, err)

		err = ws.DialContext(context.Background(), targetURL, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		assert.NoError(t, err)
	})

	t.Run("multiple ports same host", func(t *testing.T) {
		// Override for specific port
		resolves := map[string]string{
			echoHostPort("example.com"): "127.0.0.1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		// Connect using the override host (should use override)
		targetURL, err := url.Parse(echoHostURL("example.com"))
		require.NoError(t, err)

		err = ws.DialContext(context.Background(), targetURL, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		assert.NoError(t, err)
	})

	t.Run("fallback to DNS when no override", func(t *testing.T) {
		// Override for different host
		resolves := map[string]string{
			echoHostPort("other.com"): "192.168.1.1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		// Connect to localhost (no override, should use DNS)
		err := ws.DialContext(context.Background(), echoServerAddrWs, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		assert.NoError(t, err)
	})

	t.Run("case insensitive hostname", func(t *testing.T) {
		// Override with lowercase
		resolves := map[string]string{
			echoHostPort("example.com"): "127.0.0.1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		// Dial with uppercase (should match due to case-insensitive comparison)
		targetURL, err := url.Parse(echoHostURL("EXAMPLE.COM"))
		require.NoError(t, err)

		err = ws.DialContext(context.Background(), targetURL, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		assert.NoError(t, err)
	})

	t.Run("IPv6 address override", func(t *testing.T) {
		// Override with IPv6 loopback
		resolves := map[string]string{
			echoHostPort("example.com"): "::1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		// Note: This test might fail if IPv6 is not available
		// but that's acceptable for testing the override mechanism
		targetURL, err := url.Parse(echoHostURL("example.com"))
		require.NoError(t, err)

		err = ws.DialContext(context.Background(), targetURL, nil)
		// We don't require.NoError here because IPv6 might not be available
		// The important thing is that the override was attempted
		if err == nil {
			ws.Close()
		}
	})

	t.Run("timing measurements with override", func(t *testing.T) {
		resolves := map[string]string{
			echoHostPort("example.com"): "127.0.0.1",
		}
		ws := New(WithResolves(resolves))
		defer ws.Close()

		targetURL, err := url.Parse(echoHostURL("example.com"))
		require.NoError(t, err)

		err = ws.DialContext(context.Background(), targetURL, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		require.NoError(t, err)

		result := ws.ExtractResult()

		// Verify timing measurements are still accurate
		assert.Greater(t, result.DNSLookup, time.Duration(0))
		assert.Greater(t, result.TCPConnection, time.Duration(0))
		assert.Greater(t, result.WSHandshake, time.Duration(0))
		assert.Greater(t, result.MessageRTT, time.Duration(0))
	})

	t.Run("nil resolves map", func(t *testing.T) {
		// Should work normally with nil map
		ws := New(WithResolves(nil))
		defer ws.Close()

		err := ws.DialContext(context.Background(), echoServerAddrWs, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		assert.NoError(t, err)
	})

	t.Run("empty resolves map", func(t *testing.T) {
		// Should work normally with empty map
		ws := New(WithResolves(map[string]string{}))
		defer ws.Close()

		err := ws.DialContext(context.Background(), echoServerAddrWs, nil)
		require.NoError(t, err)

		err = ws.PingPong()
		assert.NoError(t, err)
	})
}

// selfSignedCert generates an ECDSA P-256 self-signed certificate with known fields,
// so TLS tests can assert exact CertificateDetails values.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wsstat-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// newTLSEchoServer starts an HTTPS echo server using the in-test self-signed cert.
// Because TLS.Certificates is non-empty, StartTLS keeps our cert instead of the
// httptest default, so the test can assert known certificate fields.
func newTLSEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(echoHandler))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t)}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestDialTLS exercises the wss:// dial path: the TLS handshake instrumentation,
// CertificateDetails parsing, and the text TLS Format section.
func TestDialTLS(t *testing.T) {
	srv := newTLSEchoServer(t)
	u := &url.URL{
		Scheme: "wss",
		Host:   strings.TrimPrefix(srv.URL, "https://"),
		Path:   "/echo",
	}

	ws := New(WithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	require.NoError(t, ws.DialContext(context.Background(), u, http.Header{}))
	defer ws.Close()

	res := ws.ExtractResult()

	// Handshake state populated and phase timings ordered.
	require.NotNil(t, res.TLSState)
	assert.True(t, res.TLSState.HandshakeComplete)
	assert.Greater(t, res.TLSHandshake, time.Duration(0))
	// TLSHandshakeDone is measured from dialStart, so it spans the handshake duration.
	assert.GreaterOrEqual(t, res.TLSHandshakeDone, res.TLSHandshake)

	// CertificateDetails parses the presented leaf certificate.
	certs := res.CertificateDetails()
	require.Len(t, certs, 1)
	assert.Equal(t, "wsstat-test", certs[0].CommonName)
	assert.Contains(t, certs[0].DNSNames, "localhost")
	assert.True(t, certs[0].NotAfter.After(time.Now()))

	// Verbose text output renders the TLS section.
	out := fmt.Sprintf("%+v", res)
	assert.Contains(t, out, "TLS handshake details")
	assert.Contains(t, out, "wsstat-test")
}

// TestDocumentedDefaultHeadersDrift guards documentedDefaultHeaders against drift in the
// underlying library: it captures the headers coder/websocket actually sends on the handshake
// and fails if the assumed constant values (notably Sec-WebSocket-Version) no longer match, so
// the -vv / %+v "library defaults" map cannot silently go stale across a transport upgrade.
func TestDocumentedDefaultHeadersDrift(t *testing.T) {
	gotHeaders := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders <- r.Header.Clone()
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		_ = conn.CloseNow()
	}))
	defer srv.Close()

	target := &url.URL{Scheme: "ws", Host: srv.Listener.Addr().String(), Path: "/echo"}
	ws := New()
	require.NoError(t, ws.DialContext(context.Background(), target, http.Header{}))
	defer ws.Close()

	var sent http.Header
	select {
	case sent = <-gotHeaders:
	case <-time.After(2 * time.Second):
		t.Fatal("handshake headers were not captured")
	}

	for key, want := range documentedDefaultHeaders {
		if key == "Sec-WebSocket-Key" {
			// Nonce: dynamically generated per request, so only assert presence.
			assert.NotEmpty(t, sent.Get(key), "handshake missing %s", key)
			continue
		}
		assert.Equalf(t, want, sent.Values(key),
			"documentedDefaultHeaders[%q] no longer matches what coder/websocket sends; "+
				"update the map (and any -vv/%%+v docs that rely on it)", key)
	}
}

// TestCloseWith verifies that CloseWith sends the chosen close code and reason in the closing
// handshake, and that it validates its arguments.
func TestCloseWith(t *testing.T) {
	t.Run("sends chosen code and reason", func(t *testing.T) {
		gotErr := make(chan error, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			for {
				if _, _, err := conn.Read(r.Context()); err != nil {
					gotErr <- err
					return
				}
			}
		}))
		defer srv.Close()

		target := &url.URL{Scheme: "ws", Host: srv.Listener.Addr().String(), Path: "/echo"}
		ws := New()
		require.NoError(t, ws.DialContext(context.Background(), target, http.Header{}))
		require.NoError(t, ws.CloseWith(int(websocket.StatusGoingAway), "bye"))

		select {
		case err := <-gotErr:
			assert.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err))
			var ce websocket.CloseError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, "bye", ce.Reason)
		case <-time.After(2 * time.Second):
			t.Fatal("server never observed the close frame")
		}
	})

	t.Run("rejects invalid code", func(t *testing.T) {
		ws := New()
		require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
		defer ws.Close()
		assert.Error(t, ws.CloseWith(999, ""))
		assert.Error(t, ws.CloseWith(int(websocket.StatusAbnormalClosure), "")) // 1006, local-only
	})

	t.Run("rejects over-long reason", func(t *testing.T) {
		ws := New()
		require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
		defer ws.Close()
		assert.Error(t, ws.CloseWith(int(websocket.StatusNormalClosure), strings.Repeat("x", 124)))
	})

	t.Run("returns ErrClosed after close", func(t *testing.T) {
		ws := New()
		require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
		ws.Close()
		assert.ErrorIs(t, ws.CloseWith(int(websocket.StatusNormalClosure), ""), ErrClosed)
	})
}

// TestWithValidateUTF8 verifies that opt-in UTF-8 validation counts invalid inbound text frames
// in Result.InvalidUTF8Frames, and that the default leaves it at zero.
func TestWithValidateUTF8(t *testing.T) {
	invalid := []byte{0xff, 0xfe, 0xfd}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if err := conn.Write(r.Context(), websocket.MessageText, invalid); err != nil {
			return
		}
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	target := &url.URL{Scheme: "ws", Host: srv.Listener.Addr().String(), Path: "/echo"}

	t.Run("counts invalid frames when enabled", func(t *testing.T) {
		ws := New(WithValidateUTF8(true))
		require.NoError(t, ws.DialContext(context.Background(), target, http.Header{}))
		mt, p, err := ws.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, TextMessage, mt)
		assert.Equal(t, invalid, p)
		ws.Close()
		assert.Equal(t, 1, ws.ExtractResult().InvalidUTF8Frames)
	})

	t.Run("ignores invalid frames when disabled", func(t *testing.T) {
		ws := New()
		require.NoError(t, ws.DialContext(context.Background(), target, http.Header{}))
		_, _, err := ws.ReadMessage()
		require.NoError(t, err)
		ws.Close()
		assert.Equal(t, 0, ws.ExtractResult().InvalidUTF8Frames)
	})
}
