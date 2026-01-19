package app

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/jkbrsn/wsstat/v2"
)

const (
	formatAuto = "auto"
	formatRaw  = "raw"
	formatJSON = "json"

	// JSONSchemaVersion is the schema version for JSON output
	JSONSchemaVersion = "1.0"
)

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
	DNSLookup     *int64 `json:"dns_lookup,omitempty"`
	TCPConnection *int64 `json:"tcp_connection,omitempty"`
	TLSHandshake  *int64 `json:"tls_handshake,omitempty"`
	WSHandshake   *int64 `json:"ws_handshake,omitempty"`
	MessageRTT    *int64 `json:"message_rtt,omitempty"`
	Total         *int64 `json:"total,omitempty"`
}

type timingTimelineJSON struct {
	DNSLookupDone        *int64 `json:"dns_lookup_done,omitempty"`
	TCPConnected         *int64 `json:"tcp_connected,omitempty"`
	TLSHandshakeDone     *int64 `json:"tls_handshake_done,omitempty"`
	WSHandshakeDone      *int64 `json:"ws_handshake_done,omitempty"`
	FirstMessageResponse *int64 `json:"first_message_response,omitempty"`
	Total                *int64 `json:"total,omitempty"`
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
	FirstEventMs  *int64                  `json:"first_event_ms,omitempty"`
	LastEventMs   *int64                  `json:"last_event_ms,omitempty"`
	TotalMessages int                     `json:"total_messages"`
	Subscriptions []subscriptionEntryJSON `json:"subscriptions,omitempty"`
}

type subscriptionEntryJSON struct {
	ID                 string `json:"id"`
	Messages           uint64 `json:"messages"`
	Bytes              uint64 `json:"bytes"`
	FirstEventMs       *int64 `json:"first_event_ms,omitempty"`
	LastEventMs        *int64 `json:"last_event_ms,omitempty"`
	MeanInterArrivalMs *int64 `json:"mean_inter_arrival_ms,omitempty"`
	Error              string `json:"error,omitempty"`
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

func formatJSONIfPossible(data []byte) (string, error) {
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
	pretty, err := json.MarshalIndent(anyJSON, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format JSON: %w", err)
	}
	return string(pretty), nil
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
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return ""
	}
}
