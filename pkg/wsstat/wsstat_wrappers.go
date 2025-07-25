package wsstat

import (
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// MeasureLatency is a wrapper around a one-hit usage of the WSStat instance. It establishes a
// WebSocket connection, sends a message, reads the response, and closes the connection.
// Note: sets all times in the Result object.
func MeasureLatency(url *url.URL, msg string, customHeaders http.Header) (*Result, []byte, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(url, customHeaders); err != nil {
		logger.Debug().Err(err).Msg("Failed to establish WebSocket connection")
		return nil, nil, err
	}
	ws.WriteMessage(websocket.TextMessage, []byte(msg))
	_, p, err := ws.ReadMessage()
	if err != nil {
		logger.Debug().Err(err).Msg("Failed to read message")
		return nil, nil, err
	}
	ws.Close()

	return ws.result, p, nil
}

// MeasureLatencyBurst is a convenience wrapper around the WSStat instance, used to measure the
// latency of a WebSocket connection with multiple messages sent in quick succession. It connects
// to the server, sends all messages, reads the responses, and closes the connection.
// Note: sets all times in the Result object, where the MessageRTT will be the mean round trip time
// of all messages sent.
func MeasureLatencyBurst(url *url.URL, msgs []string, customHeaders http.Header) (*Result, []string, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(url, customHeaders); err != nil {
		logger.Debug().Err(err).Msg("Failed to establish WebSocket connection")
		return nil, nil, err
	}

	for _, msg := range msgs {
		ws.WriteMessage(websocket.TextMessage, []byte(msg))
	}

	var responses []string
	for range len(msgs) {
		_, p, err := ws.ReadMessage()
		if err != nil {
			logger.Debug().Err(err).Msg("Failed to read message")
			return nil, nil, err
		}
		responses = append(responses, string(p))
	}
	ws.Close()

	return ws.result, responses, nil
}

// MeasureLatencyJSON is a wrapper around a one-hit usage of the WSStat instance. It establishes a
// WebSocket connection, sends a JSON message, reads the response, and closes the connection.
// Note: sets all times in the Result object.
func MeasureLatencyJSON(url *url.URL, v interface{}, customHeaders http.Header) (*Result, interface{}, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(url, customHeaders); err != nil {
		logger.Debug().Err(err).Msg("Failed to establish WebSocket connection")
		return nil, nil, err
	}
	p, err := ws.OneHitMessageJSON(v)
	if err != nil {
		logger.Debug().Err(err).Msg("Failed to send message")
		return nil, nil, err
	}
	ws.Close()

	return ws.result, p, nil
}

// MeasureLatencyJSONBurst is a convenience wrapper around the WSStat instance, used to measure the
// latency of a WebSocket connection with multiple messages sent in quick succession. It connects
// to the server, sends all JSON messages, reads the responses, and closes the connection.
// Note: sets all times in the Result object, where the MessageRTT will be the mean round trip time
// of all messages sent.
func MeasureLatencyJSONBurst(url *url.URL, v []interface{}, customHeaders http.Header) (*Result, []interface{}, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(url, customHeaders); err != nil {
		logger.Debug().Err(err).Msg("Failed to establish WebSocket connection")
		return nil, nil, err
	}

	for _, msg := range v {
		ws.WriteMessageJSON(msg)
	}

	var responses []interface{}
	for range len(v) {
		resp, err := ws.ReadMessageJSON()
		if err != nil {
			logger.Debug().Err(err).Msg("Failed to read message")
			return nil, nil, err
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
func MeasureLatencyPing(url *url.URL, customHeaders http.Header) (*Result, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(url, customHeaders); err != nil {
		logger.Debug().Err(err).Msg("Failed to establish WebSocket connection")
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
func MeasureLatencyPingBurst(url *url.URL, pingCount int, customHeaders http.Header) (*Result, error) {
	ws := New()
	defer ws.Close()

	if err := ws.Dial(url, customHeaders); err != nil {
		logger.Debug().Err(err).Msg("Failed to establish WebSocket connection")
		return nil, err
	}
	for i := 0; i < pingCount; i++ {
		ws.WriteMessage(websocket.PingMessage, nil)
	}
	for i := 0; i < pingCount; i++ {
		ws.ReadPong()
	}
	ws.Close()

	return ws.result, nil
}
