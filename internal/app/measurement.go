package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/jkbrsn/wsstat"
)

// measureText runs the text-message latency measurement flow.
func (c *Client) measureText(target *url.URL, header http.Header) error {
	msgs := buildRepeatedStrings(c.TextMessage, c.Count)
	var err error
	c.Result, c.Response, err = wsstat.MeasureLatencyBurst(target, msgs, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return c.postProcessTextResponse()
}

// measureJSON runs the JSON-RPC latency measurement flow.
func (c *Client) measureJSON(target *url.URL, header http.Header) error {
	msg := struct {
		Method     string `json:"method"`
		ID         string `json:"id"`
		RPCVersion string `json:"jsonrpc"`
	}{
		Method:     c.RPCMethod,
		ID:         "1",
		RPCVersion: "2.0",
	}
	msgs := buildRepeatedAny(msg, c.Count)
	var err error
	c.Result, c.Response, err = wsstat.MeasureLatencyJSONBurst(target, msgs, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return nil
}

// measurePing runs the ping latency measurement flow.
func (c *Client) measurePing(target *url.URL, header http.Header) error {
	var err error
	c.Result, err = wsstat.MeasureLatencyPingBurst(target, c.Count, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return nil
}

// postProcessTextResponse keeps behavior identical to the original implementation while allowing
// plain-text echoes:
// - pick the first element if response is []string and non-empty
// - if Format is not raw and response is a JSON-RPC payload, decode into map[string]any
func (c *Client) postProcessTextResponse() error {
	if responseArray, ok := c.Response.([]string); ok && len(responseArray) > 0 {
		c.Response = responseArray[0]
	}
	if c.Format != formatRaw {
		responseStr, ok := c.Response.(string)
		if !ok {
			return nil
		}

		trimmed := strings.TrimSpace(responseStr)
		if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
			return nil
		}

		decodedMessage := make(map[string]any)
		if err := json.Unmarshal([]byte(responseStr), &decodedMessage); err != nil {
			return nil
		}

		if _, isJSONRPC := decodedMessage["jsonrpc"]; !isJSONRPC {
			return nil
		}
		c.Response = decodedMessage
	}
	return nil
}
