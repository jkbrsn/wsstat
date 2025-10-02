package main

import (
	"errors"
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
