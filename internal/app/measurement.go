package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jkbrsn/wsstat"
)

// measureText runs the text-message latency measurement flow.
func (c *Client) measureText(
	ctx context.Context,
	target *url.URL,
	header http.Header,
) (*MeasurementResult, error) {
	msgs := buildRepeatedStrings(c.textMessage, c.count)

	result, rawResponses, err := wsstat.MeasureLatencyBurstWithContext(ctx, target, msgs, header)
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
	msg := struct {
		Method     string `json:"method"`
		ID         string `json:"id"`
		RPCVersion string `json:"jsonrpc"`
	}{
		Method:     c.rpcMethod,
		ID:         "1",
		RPCVersion: "2.0",
	}
	msgs := buildRepeatedAny(msg, c.count)

	result, responses, err := wsstat.MeasureLatencyJSONBurstWithContext(ctx, target, msgs, header)
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
	result, err := wsstat.MeasureLatencyPingBurstWithContext(ctx, target, c.count, header)
	if err != nil {
		return nil, handleConnectionError(err, target.String())
	}

	return &MeasurementResult{
		Result:   result,
		Response: nil,
	}, nil
}

// processTextResponse transforms a raw text response based on format settings
func processTextResponse(response any, format string) (any, error) {
	processedResponse := response
	if responseArray, ok := response.([]string); ok && len(responseArray) > 0 {
		processedResponse = responseArray[0]
	}

	if format == formatRaw {
		return processedResponse, nil
	}

	responseStr, ok := processedResponse.(string)
	if !ok {
		return processedResponse, nil
	}

	jsonResp, err := tryDecodeJSONRPC(responseStr)
	if err != nil {
		return processedResponse, nil
	}

	return jsonResp, nil
}

// tryDecodeJSONRPC attempts to parse a string as JSON-RPC
func tryDecodeJSONRPC(s string) (map[string]any, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, errors.New("not JSON")
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, err
	}

	if _, isJSONRPC := decoded["jsonrpc"]; !isJSONRPC {
		return nil, errors.New("not JSON-RPC")
	}

	return decoded, nil
}
