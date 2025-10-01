package app

import (
	"crypto/tls"
	"errors"
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
	// Check for specific TLS errors first
	var tlsErr *tls.RecordHeaderError
	if errors.As(err, &tlsErr) {
		return fmt.Errorf("TLS handshake failed connecting to '%s': %w", address, err)
	}

	// Fallback to string checking for specific messages
	errMsg := err.Error()
	if strings.Contains(errMsg, "tls:") || strings.Contains(errMsg, "TLS") {
		return fmt.Errorf("secure WebSocket connection failed to '%s': %w", address, err)
	}

	return fmt.Errorf("WebSocket connection failed to '%s': %w", address, err)
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

func repeat[T any](value T, count int) []T {
	result := make([]T, count)
	for i := range result {
		result[i] = value
	}
	return result
}
