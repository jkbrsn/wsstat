package app

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func formatPadLeft(d time.Duration) string {
	return fmt.Sprintf("%7dms", int(d/time.Millisecond))
}

func formatPadRight(d time.Duration) string {
	return fmt.Sprintf("%-8s", strconv.Itoa(int(d/time.Millisecond))+"ms")
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", d/time.Millisecond)
}

func handleConnectionError(err error, address string) error {
	if strings.Contains(err.Error(), "tls: first record does not look like a TLS handshake") {
		return fmt.Errorf("error establishing secure WS connection to '%s': %v", address, err)
	}
	return fmt.Errorf("error establishing WS connection to '%s': %v", address, err)
}

func msPtr(d time.Duration) *int64 {
	if d <= 0 {
		return nil
	}
	ms := d.Milliseconds()
	return &ms
}

func parseHeaders(pairs []string) (http.Header, error) {
	header := http.Header{}
	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid header format: %s", pair)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("invalid header format: %s", pair)
		}
		header.Add(key, value)
	}
	return header, nil
}

func tickerC(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func buildRepeatedStrings(s string, n int) []string {
	msgs := make([]string, n)
	for i := range msgs {
		msgs[i] = s
	}
	return msgs
}

func buildRepeatedAny(v any, n int) []any {
	msgs := make([]any, n)
	for i := range msgs {
		msgs[i] = v
	}
	return msgs
}
