// Package app measures utilizes the wsstat package to measure the latency of a WebSocket
// connection and print the results.
package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat"
)

// Client measures the latency of a WebSocket connection, applying different methods
// based on the settings passed to the struct.
type Client struct {
	// Input
	count       int      // Nr of interactions to perform; 0 means unlimited in subscription mode
	headers     []string // HTTP headers for connection establishment ("Key: Value")
	rpcMethod   string   // JSON-RPC method (no params)
	textMessage string   // Text message

	// Output
	format    string // Output formatting mode: "auto", "json", or "raw"
	colorMode string // Color behavior: "auto", "always", or "never"

	// Verbosity
	quiet          bool // suppress request/timing output
	verbosityLevel int  // 0 = summary, 1 = extended, >=2 = full detail

	// Subscription mode
	subscribe       bool
	subscribeOnce   bool
	buffer          int
	summaryInterval time.Duration

	// Deprecated: Use MeasureLatency return value instead
	// The response of a MeasureLatency call. Is overwritten if the function is called again.
	response any

	// Deprecated: Use MeasureLatency return value instead
	// The result of a MeasureLatency call. Is overwritten if the function is called again.
	result *wsstat.Result
}

// Option configures a Client.
type Option func(*Client)

// NewClient creates a new Client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		count:     1,
		format:    formatAuto,
		colorMode: "auto",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithCount sets the number of measurement interactions.
func WithCount(n int) Option {
	return func(c *Client) { c.count = n }
}

// WithHeaders sets custom HTTP headers for the WebSocket handshake.
func WithHeaders(headers []string) Option {
	return func(c *Client) { c.headers = headers }
}

// WithRPCMethod configures JSON-RPC method to send.
func WithRPCMethod(method string) Option {
	return func(c *Client) { c.rpcMethod = method }
}

// WithTextMessage configures a text message to send.
func WithTextMessage(msg string) Option {
	return func(c *Client) { c.textMessage = msg }
}

// WithFormat sets the output format (auto, json, or raw).
func WithFormat(format string) Option {
	return func(c *Client) { c.format = format }
}

// WithColorMode sets color output behavior (auto, always, or never).
func WithColorMode(mode string) Option {
	return func(c *Client) { c.colorMode = mode }
}

// WithQuiet suppresses non-essential output.
func WithQuiet(quiet bool) Option {
	return func(c *Client) { c.quiet = quiet }
}

// WithVerbosity sets the verbosity level (0 = summary, 1 = extended, 2+ = full).
func WithVerbosity(level int) Option {
	return func(c *Client) { c.verbosityLevel = level }
}

// WithSubscription enables subscription mode.
func WithSubscription(subscribe bool) Option {
	return func(c *Client) { c.subscribe = subscribe }
}

// WithSubscriptionOnce enables one-shot subscription mode.
func WithSubscriptionOnce(once bool) Option {
	return func(c *Client) { c.subscribeOnce = once }
}

// WithBuffer sets the subscription delivery buffer size.
func WithBuffer(size int) Option {
	return func(c *Client) { c.buffer = size }
}

// WithSummaryInterval sets the subscription summary print interval.
func WithSummaryInterval(interval time.Duration) Option {
	return func(c *Client) { c.summaryInterval = interval }
}

// Count returns the configured interaction count.
func (c *Client) Count() int { return c.count }

// Format returns the configured output format.
func (c *Client) Format() string { return c.format }

// ColorMode returns the configured color mode.
func (c *Client) ColorMode() string { return c.colorMode }

// VerbosityLevel returns the configured verbosity level.
func (c *Client) VerbosityLevel() int { return c.verbosityLevel }

// Quiet returns whether quiet mode is enabled.
func (c *Client) Quiet() bool { return c.quiet }

// RPCMethod returns the configured RPC method.
func (c *Client) RPCMethod() string { return c.rpcMethod }

// Result returns the deprecated result field.
// Deprecated: Access result from MeasureLatency return value instead.
func (c *Client) Result() *wsstat.Result { return c.result }

// Response returns the deprecated response field.
// Deprecated: Access response from MeasureLatency return value instead.
func (c *Client) Response() any { return c.response }

// MeasureLatency measures the latency of the WebSocket connection, applying different methods
// based on the flags passed to the program.
func (c *Client) MeasureLatency(
	ctx context.Context,
	target *url.URL,
) (*MeasurementResult, error) {
	header, err := parseHeaders(c.headers)
	if err != nil {
		return nil, err
	}

	var result *MeasurementResult
	switch {
	case c.textMessage != "":
		result, err = c.measureText(ctx, target, header)
	case c.rpcMethod != "":
		result, err = c.measureJSON(ctx, target, header)
	default:
		result, err = c.measurePing(ctx, target, header)
	}

	if err != nil {
		return nil, err
	}

	// Keep deprecated fields populated for backward compatibility
	c.result = result.Result
	c.response = result.Response

	return result, nil
}

// Validate validates the Client is ready for measurement; it checks that the client settings are
// set to valid values.
func (c *Client) Validate() error {
	if c.count < 0 {
		return errors.New("count must be zero or greater")
	}

	if c.textMessage != "" && c.rpcMethod != "" {
		return errors.New("mutually exclusive messaging flags")
	}

	c.format = strings.TrimSpace(strings.ToLower(c.format))
	if c.format == "" {
		c.format = formatAuto
	}
	switch c.format {
	case formatAuto, formatRaw, formatJSON:
		// valid
	default:
		return errors.New("format must be \"auto\", \"json\", or \"raw\"")
	}

	c.colorMode = strings.TrimSpace(strings.ToLower(c.colorMode))
	if c.colorMode == "" {
		c.colorMode = "auto"
	}
	if c.colorMode != "auto" && c.colorMode != "always" && c.colorMode != "never" {
		return errors.New("color must be \"auto\", \"always\", or \"never\"")
	}

	if c.buffer < 0 {
		return errors.New("buffer must be zero or greater")
	}

	if c.summaryInterval < 0 {
		return errors.New("summary-interval must be zero or greater")
	}

	if c.subscribeOnce {
		c.subscribe = true
		if c.count == 0 {
			c.count = 1
		}
		if c.count != 1 {
			return errors.New("count must equal 1 when subscribe-once is enabled")
		}
	}

	if c.subscribe {
		return nil
	}

	if c.count == 0 {
		c.count = 1
	}
	if c.count < 1 {
		return errors.New("count must be greater than 0")
	}
	return nil
}
