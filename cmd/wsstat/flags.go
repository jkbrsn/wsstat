package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"
)

// headerList is a flag.Value implementation that accumulates repeated -H / -header entries.
type headerList []string

// Set appends the header value to the list.
func (h *headerList) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("header must not be empty")
	}
	if !strings.Contains(trimmed, ":") {
		return errors.New("header must be in 'Key: Value' format")
	}
	*h = append(*h, trimmed)
	return nil
}

// String returns the string representation of the flag value.
func (h *headerList) String() string {
	return strings.Join(*h, ", ")
}

// Values returns the list of headers.
func (h *headerList) Values() []string {
	return append([]string(nil), *h...)
}

// trackedIntFlag is a flag.Value implementation that tracks whether a flag was set.
type trackedIntFlag struct {
	value int
	set   bool
}

// Set converts the string value to an integer and stores it.
func (f *trackedIntFlag) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	f.value = v
	f.set = true
	return nil
}

// String returns the string representation of the flag value.
func (f *trackedIntFlag) String() string {
	return strconv.Itoa(f.value)
}

// Value returns the integer value of the flag.
func (f *trackedIntFlag) Value() int {
	return f.value
}

// WasSet returns true if the flag was set.
func (f *trackedIntFlag) WasSet() bool {
	return f.set
}

// newTrackedIntFlag creates a new trackedIntFlag with the given default value.
func newTrackedIntFlag(defaultValue int) trackedIntFlag {
	return trackedIntFlag{value: defaultValue}
}

// resolveList is a flag.Value implementation that accumulates DNS resolution overrides.
// Each entry has the format "host:port:address" (e.g., "example.com:443:192.168.1.1").
// The flag can be repeated to override multiple host:port combinations.
type resolveList struct {
	overrides map[string]string // key: "host:port", value: "address"
}

// Set parses and adds a DNS resolution override in the format "host:port:address".
func (r *resolveList) Set(value string) error {
	parts := strings.Split(value, ":")

	// Handle IPv6 addresses which contain colons
	// Valid formats:
	//   - host:port:ipv4 (3 parts)
	//   - host:port:ipv6 (4+ parts, last parts form IPv6)
	const minResolveFormatParts = 3
	if len(parts) < minResolveFormatParts {
		return errors.New("resolve must be in 'host:port:address' format")
	}

	// Extract host
	host := strings.TrimSpace(parts[0])
	if host == "" {
		return errors.New("host must not be empty")
	}

	// Extract port
	portStr := strings.TrimSpace(parts[1])
	if portStr == "" {
		return errors.New("port must not be empty")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}
	const maxPortNumber = 65535 // the maximum valid TCP/UDP port number
	if port < 1 || port > maxPortNumber {
		return fmt.Errorf("port must be between 1 and %d, got %d", maxPortNumber, port)
	}

	// Extract address by joining remaining parts (for IPv6)
	address := strings.TrimSpace(strings.Join(parts[2:], ":"))
	if address == "" {
		return errors.New("address must not be empty")
	}

	// Strip brackets from IPv6 addresses if present
	address = strings.TrimPrefix(address, "[")
	address = strings.TrimSuffix(address, "]")

	// Validate IP address format
	if net.ParseIP(address) == nil {
		return fmt.Errorf("invalid IP address: %s", address)
	}

	// Initialize map on first use
	if r.overrides == nil {
		r.overrides = make(map[string]string)
	}

	// Normalize host to lowercase
	key := net.JoinHostPort(strings.ToLower(host), portStr)
	r.overrides[key] = address

	return nil
}

// String returns the string representation of the flag value.
func (r *resolveList) String() string {
	if len(r.overrides) == 0 {
		return ""
	}
	var entries []string
	for key, addr := range r.overrides {
		entries = append(entries, fmt.Sprintf("%s→%s", key, addr))
	}
	return strings.Join(entries, ", ")
}

// Values returns a copy of the DNS resolution overrides map.
func (r *resolveList) Values() map[string]string {
	if r.overrides == nil {
		return nil
	}
	clone := make(map[string]string, len(r.overrides))
	maps.Copy(clone, r.overrides)
	return clone
}
