package wsstat

import (
	"context"
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

func TestDial(t *testing.T) {
	testStart := time.Now()
	ws := New()
	defer ws.Close()

	err := ws.Dial(echoServerAddrWs, http.Header{})
	assert.NoError(t, err)
	assert.NotNil(t, ws.conn)
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
	assert.Contains(t, err.Error(), "read channel closed")

	_, err = ws.ReadMessageJSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read channel closed")

	err = ws.ReadPong()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pong channel closed")
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
