package app

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/jkbrsn/wsstat/v3"
)

// JSONSchemaVersion is the schema version for JSON output
const JSONSchemaVersion = "1.0"

// MeasurementResult holds the outcome of a WebSocket measurement operation
type MeasurementResult struct {
	Result   *wsstat.Result // Timing and connection details
	Response any            // Response payload (type varies by message type)
}

type timingSummaryJSON struct {
	Schema    string              `json:"schema_version"`
	Type      string              `json:"type"`
	Mode      string              `json:"mode"`
	Target    *timingTargetJSON   `json:"target,omitempty"`
	Counts    timingCountsJSON    `json:"counts"`
	Durations timingDurationsJSON `json:"durations_ms"`
	Timeline  *timingTimelineJSON `json:"timeline_ms,omitempty"`
	Warnings  []string            `json:"warnings,omitempty"`
}

type timingCountsJSON struct {
	Requested int `json:"requested"`
	Messages  int `json:"messages"`
}

type timingTargetJSON struct {
	URL  string         `json:"url,omitempty"`
	Host string         `json:"host,omitempty"`
	IPs  []string       `json:"ips,omitempty"`
	TLS  *timingTLSJSON `json:"tls,omitempty"`
}

type timingTLSJSON struct {
	Version          string `json:"version,omitempty"`
	CipherSuite      string `json:"cipher_suite,omitempty"`
	PeerCertificates int    `json:"peer_certificates,omitempty"`
}

type timingDurationsJSON struct {
	DNSLookup     *float64 `json:"dns_lookup,omitempty"`
	TCPConnection *float64 `json:"tcp_connection,omitempty"`
	TLSHandshake  *float64 `json:"tls_handshake,omitempty"`
	WSHandshake   *float64 `json:"ws_handshake,omitempty"`
	MessageRTT    *float64 `json:"message_rtt,omitempty"`
	Total         *float64 `json:"total,omitempty"`
}

type timingTimelineJSON struct {
	DNSLookupDone        *float64 `json:"dns_lookup_done,omitempty"`
	TCPConnected         *float64 `json:"tcp_connected,omitempty"`
	TLSHandshakeDone     *float64 `json:"tls_handshake_done,omitempty"`
	WSHandshakeDone      *float64 `json:"ws_handshake_done,omitempty"`
	FirstMessageResponse *float64 `json:"first_message_response,omitempty"`
	Total                *float64 `json:"total,omitempty"`
}

type responseOutputJSON struct {
	Schema    string `json:"schema_version"`
	Type      string `json:"type"`
	RPCMethod string `json:"rpc_method,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}

type subscriptionSummaryJSON struct {
	Schema        string                  `json:"schema_version"`
	Type          string                  `json:"type"`
	Target        *timingTargetJSON       `json:"target,omitempty"`
	FirstEventMs  *float64                `json:"first_event_ms,omitempty"`
	LastEventMs   *float64                `json:"last_event_ms,omitempty"`
	TotalMessages int                     `json:"total_messages"`
	Subscriptions []subscriptionEntryJSON `json:"subscriptions,omitempty"`
}

type subscriptionEntryJSON struct {
	ID                 string   `json:"id"`
	Messages           uint64   `json:"messages"`
	Bytes              uint64   `json:"bytes"`
	FirstEventMs       *float64 `json:"first_event_ms,omitempty"`
	LastEventMs        *float64 `json:"last_event_ms,omitempty"`
	MeanInterArrivalMs *float64 `json:"mean_inter_arrival_ms,omitempty"`
	Error              string   `json:"error,omitempty"`
}

// errorOutputJSON is the schema-stable error envelope emitted under -o json when a runtime
// failure occurs, so a `wsstat ... -o json | jq` pipeline always receives a parseable record
// on the failure path instead of falling back to plain stderr text.
type errorOutputJSON struct {
	Schema string `json:"schema_version"`
	Type   string `json:"type"`
	Error  string `json:"error"`
}

// EmitJSONError writes a structured error envelope (type "error") to w, terminated by a
// newline to match the NDJSON data stream. The CLI calls this on the runtime-failure path
// under the JSON output contract; the envelope goes to stdout alongside any data already
// streamed, so consumers parsing the stream see the failure as one final record.
func EmitJSONError(w io.Writer, err error) error {
	env := errorOutputJSON{
		Schema: JSONSchemaVersion,
		Type:   "error",
		Error:  err.Error(),
	}
	data, mErr := json.Marshal(env)
	if mErr != nil {
		return fmt.Errorf("failed to marshal error envelope: %w", mErr)
	}
	if _, wErr := w.Write(append(data, '\n')); wErr != nil {
		return fmt.Errorf("failed to write error envelope: %w", wErr)
	}
	return nil
}

type subscriptionMessageJSON struct {
	Schema      string `json:"schema_version"`
	Type        string `json:"type"`
	Index       int    `json:"index,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
	Size        int    `json:"size,omitempty"`
	MessageType string `json:"message_type,omitempty"`
	Payload     any    `json:"payload,omitempty"`
}

func buildTimingTarget(result *wsstat.Result, fallback *url.URL) *timingTargetJSON {
	if result == nil {
		return nil
	}
	var target timingTargetJSON
	if result.URL != nil {
		target.URL = result.URL.String()
		target.Host = result.URL.Hostname()
	} else if fallback != nil {
		target.URL = fallback.String()
		target.Host = fallback.Hostname()
	}
	if len(result.IPs) > 0 {
		target.IPs = append([]string(nil), result.IPs...)
	}
	if result.TLSState != nil {
		tlsInfo := &timingTLSJSON{
			Version: tls.VersionName(result.TLSState.Version),
		}
		if result.TLSState.CipherSuite != 0 {
			tlsInfo.CipherSuite = tls.CipherSuiteName(result.TLSState.CipherSuite)
		}
		if len(result.TLSState.PeerCertificates) > 0 {
			tlsInfo.PeerCertificates = len(result.TLSState.PeerCertificates)
		}
		target.TLS = tlsInfo
	}
	if target.URL == "" && target.Host == "" && len(target.IPs) == 0 && target.TLS == nil {
		return nil
	}
	return &target
}

func buildTimingTimeline(result *wsstat.Result) *timingTimelineJSON {
	timeline := timingTimelineJSON{
		DNSLookupDone:        msPtr(result.DNSLookupDone),
		TCPConnected:         msPtr(result.TCPConnected),
		TLSHandshakeDone:     msPtr(result.TLSHandshakeDone),
		WSHandshakeDone:      msPtr(result.WSHandshakeDone),
		FirstMessageResponse: msPtr(result.FirstMessageResponse),
		Total:                msPtr(result.TotalTime),
	}
	if timeline.DNSLookupDone == nil && timeline.TCPConnected == nil &&
		timeline.TLSHandshakeDone == nil && timeline.WSHandshakeDone == nil &&
		timeline.FirstMessageResponse == nil && timeline.Total == nil {
		return nil
	}
	return &timeline
}

// renderJSON re-marshals JSON data for display. When compact is true the output
// is a single line; otherwise it is pretty-printed with two-space indentation.
//
// revive:disable:flag-parameter allow
func renderJSON(data []byte, compact bool) (string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", errors.New("empty data")
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return "", errors.New("not JSON")
	}
	var anyJSON any
	if err := json.Unmarshal(trimmed, &anyJSON); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	var out []byte
	var err error
	if compact {
		out, err = json.Marshal(anyJSON)
	} else {
		out, err = json.MarshalIndent(anyJSON, "", "  ")
	}
	if err != nil {
		return "", fmt.Errorf("failed to format JSON: %w", err)
	}
	return string(out), nil
}

func normalizeResponseForJSON(value any) any {
	switch v := value.(type) {
	case []byte:
		if payload, ok := parseJSONPayload(v); ok {
			return payload
		}
		return string(v)
	case string:
		if payload, ok := parseJSONPayload([]byte(v)); ok {
			return payload
		}
		return v
	case json.RawMessage:
		if payload, ok := parseJSONPayload([]byte(v)); ok {
			return payload
		}
		return string(v)
	default:
		return v
	}
}

func parseJSONPayload(data []byte) (any, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, false
	}
	var payload any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func messageTypeLabel(messageType int) string {
	switch messageType {
	case wsstat.TextMessage:
		return "text"
	case wsstat.BinaryMessage:
		return "binary"
	default:
		// coder never surfaces close/ping/pong frames from Read; they are handled
		// inside the library and never reach this label path.
		return ""
	}
}
