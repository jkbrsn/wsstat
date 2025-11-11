package wsstat

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	serverAddr       = "localhost:8080"
	echoServerAddrWs *url.URL
	// TODO: support wss in tests
)

func init() {
	var err error
	echoServerAddrWs, err = url.Parse("ws://" + serverAddr + "/echo")
	if err != nil {
		log.Fatalf("Failed to parse URL: %v", err)
	}
}

// TestMain sets up the test server and runs the tests in this file.
func TestMain(m *testing.M) {
	// Set up test server
	go func() {
		startEchoServer(serverAddr)
	}()

	// Ensure the echo server starts before any tests run
	time.Sleep(250 * time.Millisecond)

	// Run the tests in this file
	m.Run()
}

func TestNew(t *testing.T) {
	ws := New()
	defer ws.Close()

	assert.NotNil(t, ws)
	assert.NotNil(t, ws.dialer)
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
		assert.Equal(t, 16, cap(ws.pongChan))
	})

	t.Run("default buffer size", func(t *testing.T) {
		ws := New()
		defer ws.Close()

		assert.Equal(t, defaultChanBufferSize, cap(ws.readChan))
		assert.Equal(t, defaultChanBufferSize, cap(ws.writeChan))
		assert.Equal(t, defaultChanBufferSize, cap(ws.pongChan))
	})

	t.Run("zero buffer size creates unbuffered channels", func(t *testing.T) {
		ws := New(WithBufferSize(0))
		defer ws.Close()

		assert.Equal(t, 0, cap(ws.readChan))
		assert.Equal(t, 0, cap(ws.writeChan))
		assert.Equal(t, 0, cap(ws.pongChan))
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
		assert.Equal(t, 32, cap(ws.pongChan))
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

	err := ws.Dial(echoServerAddrWs, http.Header{})
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

	err := ws.Dial(echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

	message := []byte("Hello, world!")
	ws.WriteMessage(websocket.TextMessage, message)
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

		err := ws.Dial(echoServerAddrWs, http.Header{})
		assert.NoError(t, err)
		validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

		message := []byte("Hello, world!")
		ws.WriteMessage(websocket.TextMessage, message)
		ws.WriteMessage(websocket.TextMessage, message)
		ws.WriteMessage(websocket.TextMessage, message)
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

		err := ws.Dial(echoServerAddrWs, http.Header{})
		assert.NoError(t, err)
		validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

		message := []byte("Hello, world!")
		messageCount := 75
		for range make([]struct{}, messageCount) {
			ws.WriteMessage(websocket.TextMessage, message)
		}
		time.Sleep(25 * time.Millisecond) // Wait for messages to be sent

		result := ws.ExtractResult()
		assert.NotNil(t, result)
		assert.Greater(t, result.TotalTime, time.Duration(0),
			"Exepcted valid TotalTime despite no reads")
		assert.Equal(t, time.Duration(0), result.MessageRTT, "Expected 0 MessageRTT with no reads")
		assert.Zero(t, result.MessageCount, "Expected 0 MessageCount with no reads")

		for i := 0; i < messageCount; i++ {
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

func TestOneHitMessage(t *testing.T) {
	testStart := time.Now()
	ws := New()
	defer func() {
		ws.Close()
		validateCloseResult(ws, getFunctionName(), t)
	}()

	err := ws.Dial(echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

	message := []byte("Hello, world!")
	response, err := ws.OneHitMessage(websocket.TextMessage, message)
	assert.NoError(t, err)
	assert.Equal(t, message, response, "Received message does not match sent message")

	// Call close before defer, since Close calls calculateResult
	ws.Close()

	validateOneHitResult(ws, getFunctionName(), t)
}

func TestOneHitMessageJSON(t *testing.T) {
	testStart := time.Now()
	ws := New()
	defer func() {
		ws.Close()
		validateCloseResult(ws, getFunctionName(), t)
	}()

	err := ws.Dial(echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	validateDialResult(testStart, ws, echoServerAddrWs, getFunctionName(), t)

	message := struct {
		Text string `json:"text"`
	}{
		Text: "Hello, world!",
	}
	response, err := ws.OneHitMessageJSON(message)
	assert.NoError(t, err)
	responseMap, ok := response.(map[string]any)
	require.True(t, ok, "Response is not a map")
	assert.Equal(t, message.Text, responseMap["text"])

	// Call close before defer, since Close calls calculateResult
	ws.Close()

	validateOneHitResult(ws, getFunctionName(), t)
}

func TestSubscribeReceivesMessage(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: websocket.TextMessage,
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

func TestSubscribeOnceReturnsFirstMessage(t *testing.T) {
	ws := New()
	defer ws.Close()
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))

	msg, err := ws.SubscribeOnce(context.Background(), SubscriptionOptions{
		MessageType: websocket.TextMessage,
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
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: websocket.TextMessage,
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

	ws.WriteMessage(websocket.TextMessage, []byte("ping"))
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
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))

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

	err := ws.Dial(echoServerAddrWs, http.Header{})
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")

	_, err = ws.ReadMessageJSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")

	err = ws.ReadPong()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestResultFormat(t *testing.T) {
	ws := New()
	defer ws.Close()

	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))
	_, err := ws.OneHitMessage(websocket.TextMessage, []byte("test"))
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
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))
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
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: websocket.TextMessage,
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
	require.NoError(t, ws.Dial(echoServerAddrWs, http.Header{}))

	sub, err := ws.Subscribe(context.Background(), SubscriptionOptions{
		MessageType: websocket.TextMessage,
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

func TestMeasureLatencyBurstWithContext(t *testing.T) {
	t.Run("completes normally without cancellation", func(t *testing.T) {
		ctx := context.Background()
		result, _, err := MeasureLatencyBurstWithContext(
			ctx,
			echoServerAddrWs,
			[]string{"test1", "test2", "test3"},
			http.Header{},
		)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 3, result.MessageCount)
		assert.Greater(t, result.MessageRTT, time.Duration(0))
	})

	t.Run("cancels during execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		// Cancel after a very short delay
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		// Create large batch to ensure cancellation happens mid-execution
		msgs := make([]string, 1000)
		for i := range msgs {
			msgs[i] = "test"
		}

		_, _, err := MeasureLatencyBurstWithContext(
			ctx,
			echoServerAddrWs,
			msgs,
			http.Header{},
		)
		// Either context canceled or succeeded before cancel
		if err != nil {
			assert.Contains(t, err.Error(), "context canceled")
		}
	})

	t.Run("context already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, _, err := MeasureLatencyBurstWithContext(
			ctx,
			echoServerAddrWs,
			[]string{"test1", "test2"},
			http.Header{},
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "context canceled")
	})
}

func TestMeasureLatencyJSONBurstWithContext(t *testing.T) {
	t.Run("completes normally without cancellation", func(t *testing.T) {
		ctx := context.Background()
		messages := []any{
			map[string]string{"test": "value1"},
			map[string]string{"test": "value2"},
			map[string]string{"test": "value3"},
		}
		result, _, err := MeasureLatencyJSONBurstWithContext(
			ctx,
			echoServerAddrWs,
			messages,
			http.Header{},
		)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 3, result.MessageCount)
		assert.Greater(t, result.MessageRTT, time.Duration(0))
	})

	t.Run("cancels during execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		messages := make([]any, 1000)
		for i := range messages {
			messages[i] = map[string]string{"test": "value"}
		}

		_, _, err := MeasureLatencyJSONBurstWithContext(
			ctx,
			echoServerAddrWs,
			messages,
			http.Header{},
		)
		// Either context canceled or succeeded before cancel
		if err != nil {
			assert.Contains(t, err.Error(), "context canceled")
		}
	})
}

func TestMeasureLatencyPingBurstWithContext(t *testing.T) {
	t.Run("completes normally without cancellation", func(t *testing.T) {
		ctx := context.Background()
		result, err := MeasureLatencyPingBurstWithContext(
			ctx,
			echoServerAddrWs,
			3,
			http.Header{},
		)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 3, result.MessageCount)
		assert.Greater(t, result.MessageRTT, time.Duration(0))
	})

	t.Run("cancels during execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		_, err := MeasureLatencyPingBurstWithContext(
			ctx,
			echoServerAddrWs,
			1000,
			http.Header{},
		)
		// Either context canceled or succeeded before cancel
		if err != nil {
			assert.Contains(t, err.Error(), "context canceled")
		}
	})

	t.Run("context deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		_, err := MeasureLatencyPingBurstWithContext(
			ctx,
			echoServerAddrWs,
			1000,
			http.Header{},
		)
		// Either timeout or succeeded before timeout
		if err != nil {
			assert.True(t,
				strings.Contains(err.Error(), "context") ||
					strings.Contains(err.Error(), "deadline"),
			)
		}
	})
}

// Helpers

// getFunctionName returns the name of the calling function.
func getFunctionName() string {
	pc, _, _, _ := runtime.Caller(1)
	return strings.TrimPrefix(runtime.FuncForPC(pc).Name(), "main.")
}

// startEchoServer starts a WebSocket server that echoes back any received messages.
func startEchoServer(addr string) {
	var upgrader = websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool {
			return true // Allow all origins
		},
	}

	http.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Print("upgrade:", err)
			return
		}
		defer func() {
			_ = conn.Close()
		}()
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				// Only print error if it's not a normal closure
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					log.Println("read:", err)
				}
				break
			}
			err = conn.WriteMessage(mt, message)
			if err != nil {
				log.Println("write:", err)
				break
			}
		}
	})

	log.Printf("Echo server started on %s\n", addr)
	log.Println(http.ListenAndServe(addr, nil))
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
	for i := 0; i < 50; i++ {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))

			err := ws.Dial(echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			// Send multiple messages concurrently to fill the write buffer
			done := make(chan bool)
			go func() {
				for j := 0; j < 10; j++ {
					msg := []byte(fmt.Sprintf("test message %d", j))
					ws.WriteMessage(websocket.TextMessage, msg)
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
	for i := 0; i < 20; i++ {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))

			err := ws.Dial(echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			// Create a subscription
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sub, err := ws.Subscribe(ctx, SubscriptionOptions{
				MessageType: websocket.TextMessage,
				Buffer:      10,
			})
			require.NoError(t, err)

			// Send messages rapidly
			go func() {
				for j := 0; j < 5; j++ {
					ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("sub message %d", j)))
					time.Sleep(time.Microsecond * 10)
				}
			}()

			// Try to read from subscription
			go func() {
				for j := 0; j < 3; j++ {
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
	for i := 0; i < 30; i++ {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			ws := New(WithTimeout(1 * time.Second))

			err := ws.Dial(echoServerAddrWs, http.Header{})
			require.NoError(t, err)

			// Multiple writers
			for j := 0; j < 3; j++ {
				go func(id int) {
					for k := 0; k < 5; k++ {
						msg := []byte(fmt.Sprintf("writer %d msg %d", id, k))
						ws.WriteMessage(websocket.TextMessage, msg)
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
