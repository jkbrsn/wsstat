package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jkbrsn/wsstat/v3"
)

// measureText runs the text-message latency measurement flow.
func (c *Client) measureText(
	ctx context.Context,
	target *url.URL,
	header http.Header,
) (*MeasurementResult, error) {
	msgs := repeat(c.textMessage, c.count)

	opts := append(c.wsstatOptions(), wsstat.WithHeaders(header))
	result, rawResponses, err := wsstat.MeasureText(ctx, target, msgs, opts...)
	if err != nil {
		return nil, handleConnectionError(err, target.String())
	}

	// No response (empty msgs) leaves Response nil so the output layer skips it, rather than
	// rendering the empty slice literal as the body. Mirrors measureJSON/measurePing.
	var processedResponse any
	if len(rawResponses) > 0 {
		processedResponse, err = processTextResponse(rawResponses[0], c.output, c.rpcVersion)
		if err != nil {
			return nil, err
		}
	}

	return &MeasurementResult{
		Result:   result,
		Response: processedResponse,
	}, nil
}

// measureJSON runs the JSON-RPC latency measurement flow.
func (c *Client) measureJSON(
	ctx context.Context,
	target *url.URL,
	header http.Header,
) (*MeasurementResult, error) {
	req := buildRPCRequest(c.rpcMethod, c.rpcVersion)
	msgs := repeat[any](req, c.count)

	opts := append(c.wsstatOptions(), wsstat.WithHeaders(header))
	result, responses, err := wsstat.MeasureJSON(ctx, target, msgs, opts...)
	if err != nil {
		return nil, handleConnectionError(err, target.String())
	}

	var response any
	if len(responses) > 0 {
		response = responses[0]
	}

	return &MeasurementResult{
		Result:   result,
		Response: response,
	}, nil
}

// measurePing runs the ping latency measurement flow.
func (c *Client) measurePing(
	ctx context.Context,
	target *url.URL,
	header http.Header,
) (*MeasurementResult, error) {
	opts := append(c.wsstatOptions(), wsstat.WithHeaders(header))
	result, err := wsstat.MeasurePing(ctx, target, c.count, opts...)
	if err != nil {
		return nil, handleConnectionError(err, target.String())
	}

	return &MeasurementResult{
		Result:   result,
		Response: nil,
	}, nil
}

// processTextResponse transforms a raw text response based on the output contract.
// Rules:
//   - OutputRaw: returns the raw response string without any parsing
//   - OutputText/OutputJSON: attempt to parse as JSON-RPC and fall back to raw if
//     not valid JSON-RPC. The contract controls rendering, not response parsing.
func processTextResponse(response string, output Output, rpcVersion string) (any, error) {
	if output == OutputRaw {
		// Raw: return the response without any parsing.
		return response, nil
	}
	// Text/JSON: attempt JSON-RPC parsing, fall back to the raw string.
	if decoded, err := decodeAsJSONRPC(response, rpcVersion); err == nil {
		return decoded, nil
	}
	return response, nil
}

// decodeAsJSONRPC parses a string as a JSON-RPC response.
// Returns the decoded response as a map, or an error if:
//   - the string is not valid JSON
//   - the JSON is not a valid JSON-RPC 2.0 response
func decodeAsJSONRPC(s string, rpcVersion string) (map[string]any, error) {
	trimmed := strings.TrimSpace(s)

	// Verify starts with JSON structure
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, errors.New("not JSON")
	}

	decoded, err := decodeJSONRPCResponse([]byte(trimmed), rpcVersion)
	if err != nil {
		return nil, fmt.Errorf("decode failed: %w", err)
	}

	return decoded, nil
}
