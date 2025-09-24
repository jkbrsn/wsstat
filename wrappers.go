// Package wsstat measures the latency of WebSocket connections.
// It wraps the gorilla/websocket package and includes latency measurements in the Result struct.
package wsstat

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// TODO: replace zerolog.Nop() with optional function parameter for a logger

// MeasureLatency is a wrapper around a one-hit usage of the WSStat instance. It establishes a
// WebSocket connection, sends a message, reads the response, and closes the connection.
// Note: sets all times in the Result object.
func MeasureLatency(
	targetURL *url.URL,
	msg string,
	customHeaders http.Header,
) (*Result, []byte, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(targetURL, customHeaders); err != nil {
		return nil, nil, fmt.Errorf("failed to establish WebSocket connection: %v", err)
	}
	ws.WriteMessage(websocket.TextMessage, []byte(msg))
	_, p, err := ws.ReadMessage()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read message: %v", err)
	}
	ws.Close()

	return ws.result, p, nil
}

// MeasureLatencyBurst is a convenience wrapper around the WSStat instance, used to measure the
// latency of a WebSocket connection with multiple messages sent in quick succession. It connects
// to the server, sends all messages, reads the responses, and closes the connection.
// Note: sets all times in the Result object, where the MessageRTT will be the mean round trip time
// of all messages sent.
func MeasureLatencyBurst(
	targetURL *url.URL,
	msgs []string,
	customHeaders http.Header,
) (*Result, []string, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(targetURL, customHeaders); err != nil {
		return nil, nil, err
	}

	for _, msg := range msgs {
		ws.WriteMessage(websocket.TextMessage, []byte(msg))
	}

	var responses []string
	for range len(msgs) {
		_, p, err := ws.ReadMessage()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read message: %v", err)
		}
		responses = append(responses, string(p))
	}
	ws.Close()

	return ws.result, responses, nil
}

// MeasureLatencyJSON is a wrapper around a one-hit usage of the WSStat instance. It establishes a
// WebSocket connection, sends a JSON message, reads the response, and closes the connection.
// Note: sets all times in the Result object.
func MeasureLatencyJSON(
	targetURL *url.URL,
	v any,
	customHeaders http.Header,
) (*Result, any, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(targetURL, customHeaders); err != nil {
		return nil, nil, err
	}
	p, err := ws.OneHitMessageJSON(v)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to send message: %v", err)
	}
	ws.Close()

	return ws.result, p, nil
}

// MeasureLatencyJSONBurst is a convenience wrapper around the WSStat instance, used to measure the
// latency of a WebSocket connection with multiple messages sent in quick succession. It connects
// to the server, sends all JSON messages, reads the responses, and closes the connection.
// Note: sets all times in the Result object, where the MessageRTT will be the mean round trip time
// of all messages sent.
func MeasureLatencyJSONBurst(
	targetURL *url.URL,
	v []any,
	customHeaders http.Header,
) (*Result, []any, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(targetURL, customHeaders); err != nil {
		return nil, nil, err
	}

	for _, msg := range v {
		ws.WriteMessageJSON(msg)
	}

	var responses []any
	for range len(v) {
		resp, err := ws.ReadMessageJSON()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read message: %v", err)
		}
		responses = append(responses, resp)
	}
	ws.Close()

	return ws.result, responses, nil
}

// MeasureLatencyPing is a convenience wrapper around a one-hit usage of the WSStat instance. It
// establishes a WebSocket connection, sends a ping message, awaits the pong response, and closes
// the connection.
// Note: sets all times in the Result object.
func MeasureLatencyPing(
	targetURL *url.URL,
	customHeaders http.Header,
) (*Result, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(targetURL, customHeaders); err != nil {
		return nil, err
	}
	ws.PingPong()
	ws.Close()

	return ws.result, nil
}

// MeasureLatencyPingBurst is a convenience wrapper around a one-hit usage of the WSStat instance.
// It establishes a WebSocket connection, sends ping messages according to pingCount, awaits the
// pong responses, and closes the connection.
// Note: sets all times in the Result object.
func MeasureLatencyPingBurst(
	targetURL *url.URL,
	pingCount int,
	customHeaders http.Header,
) (*Result, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(targetURL, customHeaders); err != nil {
		return nil, err
	}
	for range pingCount {
		ws.WriteMessage(websocket.PingMessage, nil)
	}
	for range pingCount {
		ws.ReadPong()
	}
	ws.Close()

	return ws.result, nil
}
