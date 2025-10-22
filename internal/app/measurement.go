package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jkbrsn/jsonrpc"
	"github.com/jkbrsn/wsstat"
)

// measureText runs the text-message latency measurement flow.
func (c *Client) measureText(
	ctx context.Context,
	target *url.URL,
	header http.Header,
) (*MeasurementResult, error) {
	msgs := repeat(c.textMessage, c.count)

	result, rawResponses, err := wsstat.MeasureLatencyBurstWithContext(
		ctx, target, msgs, header, c.wsstatOptions()...)
	if err != nil {
		return nil, handleConnectionError(err, target.String())
	}

	var rawResponse any = rawResponses
	if len(rawResponses) > 0 {
		rawResponse = rawResponses[0]
	}

	processedResponse, err := processTextResponse(rawResponse, c.format)
	if err != nil {
		return nil, err
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
	req := jsonrpc.NewRequestWithID(c.rpcMethod, nil, "1")
	msgs := repeat[any](req, c.count)

	result, responses, err := wsstat.MeasureLatencyJSONBurstWithContext(
		ctx, target, msgs, header, c.wsstatOptions()...)
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
	result, err := wsstat.MeasureLatencyPingBurstWithContext(
		ctx, target, c.count, header, c.wsstatOptions()...)
	if err != nil {
		return nil, handleConnectionError(err, target.String())
	}

	return &MeasurementResult{
		Result:   result,
		Response: nil,
	}, nil
}

// processTextResponse transforms a raw text response based on the format setting.
// Format rules:
//   - formatRaw: returns the raw response string without any parsing
//   - formatAuto: attempts to parse as JSON-RPC; falls back to raw if not valid JSON-RPC
//   - formatJSON: same as formatAuto (the format option controls output rendering,
//     not response parsing; both auto and json modes parse JSON-RPC when possible)
//
// The response may be a []string from burst measurements; if so, only the first
// element is processed.
func processTextResponse(response any, format string) (any, error) {
	// Extract first response from array if present
	responseStr := extractFirstString(response)

	switch format {
	case formatRaw:
		// Raw format: return response without any parsing
		return responseStr, nil

	default:
		// Default to auto/fromatJSON: attempt parsing, fall back to raw
		if str, ok := responseStr.(string); ok {
			if decoded, err := decodeAsJSONRPC(str); err == nil {
				return decoded, nil
			}
		}
		return responseStr, nil
	}
}

// extractFirstString normalizes response arrays to a single value.
// If response is []string with elements, returns the first element.
// Otherwise returns the response unchanged.
func extractFirstString(response any) any {
	if arr, ok := response.([]string); ok && len(arr) > 0 {
		return arr[0]
	}
	return response
}

// decodeAsJSONRPC parses a string as a JSON-RPC response.
// Returns the decoded response as a map, or an error if:
//   - the string is not valid JSON
//   - the JSON is not a valid JSON-RPC 2.0 response
func decodeAsJSONRPC(s string) (map[string]any, error) {
	trimmed := strings.TrimSpace(s)

	// Verify starts with JSON structure
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, errors.New("not JSON")
	}

	resp, err := jsonrpc.DecodeResponse([]byte(trimmed))
	if err != nil {
		return nil, fmt.Errorf("decode failed: %w", err)
	}

	var decoded map[string]any
	if err := resp.Unmarshal(&decoded); err != nil {
		return nil, fmt.Errorf("unmarshal failed: %w", err)
	}

	return decoded, nil
}
