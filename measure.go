package wsstat

import (
	"context"
	"fmt"
	"net/url"
)

// MeasureText establishes a WebSocket connection to target, sends each message in msgs as a text
// frame, reads one response per message, closes the connection, and returns the measured Result
// together with the responses in order. A single message is a one-element slice. Custom headers
// and other settings are supplied via opts (e.g. WithHeaders, WithTimeout). The whole operation
// honors ctx; a nil or already-canceled ctx aborts before dialing.
func MeasureText(
	ctx context.Context, target *url.URL, msgs []string, opts ...Option,
) (*Result, []string, error) {
	ws := New(opts...)
	defer ws.Close()

	if err := ws.DialContext(ctx, target, nil); err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}

	responses := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		ws.WriteMessage(TextMessage, []byte(msg))
		_, p, err := ws.ReadMessage()
		if err != nil {
			return nil, nil, fmt.Errorf("read message: %w", err)
		}
		responses = append(responses, string(p))
	}

	ws.Close()
	return ws.ExtractResult(), responses, nil
}

// MeasureJSON behaves like MeasureText but encodes each element of msgs as JSON and decodes each
// response as JSON. Settings are supplied via opts.
func MeasureJSON(
	ctx context.Context, target *url.URL, msgs []any, opts ...Option,
) (*Result, []any, error) {
	ws := New(opts...)
	defer ws.Close()

	if err := ws.DialContext(ctx, target, nil); err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}

	responses := make([]any, 0, len(msgs))
	for _, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		ws.WriteMessageJSON(msg)
		resp, err := ws.ReadMessageJSON()
		if err != nil {
			return nil, nil, fmt.Errorf("read message: %w", err)
		}
		responses = append(responses, resp)
	}

	ws.Close()
	return ws.ExtractResult(), responses, nil
}

// MeasurePing establishes a connection to target, performs count ping/pong round-trips, closes the
// connection, and returns the measured Result. Settings are supplied via opts.
func MeasurePing(
	ctx context.Context, target *url.URL, count int, opts ...Option,
) (*Result, error) {
	ws := New(opts...)
	defer ws.Close()

	if err := ws.DialContext(ctx, target, nil); err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	for range count {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := ws.PingPong(); err != nil {
			return nil, fmt.Errorf("ping: %w", err)
		}
	}

	ws.Close()
	return ws.ExtractResult(), nil
}
