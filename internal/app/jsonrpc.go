package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// rpcVersion2 is the default JSON-RPC version wsstat speaks.
	rpcVersion2 = "2.0"
	// rpcVersion1 is the legacy JSON-RPC version, opt-in via --rpc-version 1.0.
	rpcVersion1 = "1.0"
)

// rpcRequest2 is the JSON-RPC 2.0 request wsstat emits: a method with a fixed string id and
// no params. Marshals to {"jsonrpc":"2.0","id":"1","method":...}.
type rpcRequest2 struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
}

// rpcRequest1 is the JSON-RPC 1.0 request wsstat emits: no version field, an integer id, and a
// (required) positional params array, empty since wsstat sends no arguments. Marshals to
// {"id":1,"method":...,"params":[]}.
type rpcRequest1 struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
	Params []any  `json:"params"`
}

// buildRPCRequest returns the request value to send for the given method and JSON-RPC version.
// An unrecognized version falls back to 2.0.
func buildRPCRequest(method, version string) any {
	if version == rpcVersion1 {
		return rpcRequest1{ID: 1, Method: method, Params: []any{}}
	}
	return rpcRequest2{JSONRPC: rpcVersion2, ID: "1", Method: method}
}

// versionAllowedOnDecode reports whether a decoded jsonrpc version string is acceptable. Strict
// mode accepts only "2.0"; with allowV1 it also accepts "" (absent) and "1.0", matching the
// jkbrsn/jsonrpc WithAllowJSONRPC1 decode contract.
func versionAllowedOnDecode(v string, allowV1 bool) bool {
	if v == rpcVersion2 {
		return true
	}
	return allowV1 && (v == "" || v == rpcVersion1)
}

// decodeJSONRPCResponse parses data as a JSON-RPC response and returns it as a map of the
// recognized fields (jsonrpc when present, id, and exactly one of result/error). When rpcVersion
// is "1.0" it also accepts version-less / 1.0 payloads: an "error":null is treated as absent, and
// a "result":null alongside a real error is treated as absent so a 1.0 error response decodes.
func decodeJSONRPCResponse(data []byte, rpcVersion string) (map[string]any, error) {
	allowV1 := rpcVersion == rpcVersion1
	var raw struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	if !versionAllowedOnDecode(raw.JSONRPC, allowV1) {
		return nil, fmt.Errorf("invalid JSON-RPC version: %q", raw.JSONRPC)
	}

	isNull := func(m json.RawMessage) bool {
		return bytes.Equal(bytes.TrimSpace(m), []byte("null"))
	}

	// A null error is always treated as absent; 1.0 carries "error":null beside a result, and
	// strict 2.0 servers occasionally emit it too.
	resultExists := len(raw.Result) > 0
	errorExists := len(raw.Error) > 0 && !isNull(raw.Error)

	// 1.0 error responses carry "result":null beside the error; drop it so the error wins.
	// Strict 2.0 keeps "result":null as a valid result value.
	if allowV1 && resultExists && errorExists && isNull(raw.Result) {
		resultExists = false
	}

	if !resultExists && !errorExists {
		return nil, errors.New("response must contain either result or error")
	}
	if resultExists && errorExists {
		return nil, errors.New("response must not contain both result and error")
	}

	out := map[string]any{}
	if raw.JSONRPC != "" {
		out["jsonrpc"] = raw.JSONRPC
	}

	if len(raw.ID) > 0 {
		var id any
		if err := json.Unmarshal(raw.ID, &id); err != nil {
			return nil, fmt.Errorf("invalid id field: %w", err)
		}
		switch id.(type) {
		case nil, string, float64:
		default:
			return nil, errors.New("id field must be a string or a number")
		}
		out["id"] = id
	}

	field, rawField := "result", raw.Result
	if !resultExists {
		field, rawField = "error", raw.Error
	}
	var val any
	if err := json.Unmarshal(rawField, &val); err != nil {
		return nil, fmt.Errorf("invalid %s field: %w", field, err)
	}
	out[field] = val

	return out, nil
}
