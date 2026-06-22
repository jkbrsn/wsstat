package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// msRoundScale rounds milliseconds to 3 decimals (microsecond resolution).
	msRoundScale = 1000
	// float64BitSize is the bit size passed to strconv float conversions.
	float64BitSize = 64
)

// msFloat converts a duration to milliseconds rounded to 3 decimals (microsecond resolution),
// so sub-millisecond phases render non-zero instead of truncating to whole ms.
func msFloat(d time.Duration) float64 {
	ms := float64(d) / float64(time.Millisecond)
	return math.Round(ms*msRoundScale) / msRoundScale
}

// msString renders a duration as milliseconds with up to 3 decimals, trailing zeros trimmed.
func msString(d time.Duration) string {
	return strconv.FormatFloat(msFloat(d), 'f', -1, float64BitSize)
}

func formatPadLeft(d time.Duration) string {
	return fmt.Sprintf("%7sms", msString(d))
}

func formatPadRight(d time.Duration) string {
	return fmt.Sprintf("%-8s", msString(d)+"ms")
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return msString(d) + "ms"
}

func handleConnectionError(err error, address string) error {
	// A malformed TLS record during the handshake is reported distinctly.
	var recordErr *tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return fmt.Errorf("TLS handshake failed connecting to '%s': %w", address, err)
	}
	if isTLSCertError(err) {
		return fmt.Errorf("secure WebSocket connection failed to '%s': %w", address, err)
	}
	return fmt.Errorf("WebSocket connection failed to '%s': %w", address, err)
}

// isTLSCertError reports whether err is a TLS certificate-verification failure. It prefers typed
// matching (now that dial errors are %w-wrapped, the x509/tls types survive the boundary) and
// falls back to a string check for errors that don't surface a typed value.
func isTLSCertError(err error) bool {
	var (
		certVerif   *tls.CertificateVerificationError
		unknownAuth x509.UnknownAuthorityError
		hostname    x509.HostnameError
		invalid     x509.CertificateInvalidError
	)
	if errors.As(err, &certVerif) || errors.As(err, &unknownAuth) ||
		errors.As(err, &hostname) || errors.As(err, &invalid) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "tls:") || strings.Contains(msg, "TLS")
}

func msPtr(d time.Duration) *float64 {
	if d <= 0 {
		return nil
	}
	ms := msFloat(d)
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
