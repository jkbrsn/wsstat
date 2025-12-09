// Package app provides a high-level client for measuring WebSocket latency and streaming
// subscription events. It builds on the wsstat package with configuration management,
// output formatting (terminal and JSON), and subscription handling.
//
// # Basic Usage
//
// Create a client with functional options and measure latency:
//
//	client := app.NewClient(
//	    app.WithTextMessage("ping"),
//	    app.WithCount(5),
//	)
//	if err := client.Validate(); err != nil {
//	    log.Fatal(err)
//	}
//	ctx := context.Background()
//	result, err := client.MeasureLatency(ctx, targetURL)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	client.PrintTimingResults(targetURL, result)
//
// # Subscription Mode
//
// Stream events from a WebSocket server:
//
//	client := app.NewClient(
//	    app.WithSubscription(true),
//	    app.WithCount(10),
//	    app.WithTextMessage("subscribe"),
//	)
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	if err := client.StreamSubscription(ctx, targetURL); err != nil {
//	    log.Fatal(err)
//	}
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat"
)

// Client measures the latency of a WebSocket connection and manages subscription streams.
// Use NewClient with functional options to create and configure a Client.
//
// Fields are private; use accessor methods (Count(), Format(), etc.) to read configuration,
// or use MeasureLatency's return value to access results.
type Client struct {
	// Input
	count       int               // Nr of interactions to perform; 0 means unlimited in subscription mode
	headers     []string          // HTTP headers for connection establishment ("Key: Value")
	resolves    map[string]string // DNS resolution overrides: "host:port" → "address"
	rpcMethod   string            // JSON-RPC method (no params)
	textMessage string            // Text message

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

	// TLS configuration
	insecure bool // skip TLS certificate verification
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

// WithResolves sets DNS resolution overrides for specific host:port combinations.
// Map key format: "host:port", value: "ip_address".
func WithResolves(resolves map[string]string) Option {
	return func(c *Client) { c.resolves = resolves }
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

// WithInsecure configures whether to skip TLS certificate verification.
func WithInsecure(insecure bool) Option {
	return func(c *Client) { c.insecure = insecure }
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

// wsstatOptions builds wsstat options based on client configuration.
func (c *Client) wsstatOptions() []wsstat.Option {
	var opts []wsstat.Option

	if c.insecure {
		opts = append(opts, wsstat.WithTLSConfig(&tls.Config{
			InsecureSkipVerify: true,
		}))
	}

	if c.resolves != nil {
		opts = append(opts, wsstat.WithResolves(c.resolves))
	}

	return opts
}

// MeasureLatency measures WebSocket connection latency using ping, text, or JSON-RPC messages
// based on client configuration. Returns timing results and the server response.
//
// The measurement method is determined by client settings:
//   - If textMessage is set: sends text messages and measures echo latency
//   - If rpcMethod is set: sends JSON-RPC requests and measures response latency
//   - Otherwise: sends WebSocket ping frames and measures pong latency
//
// The context can be used to cancel the measurement operation.
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

	return result, nil
}

// Validate checks client configuration for validity and applies defaults.
// It must be called after construction and before MeasureLatency or subscription methods.
//
// Validation includes:
//   - Ensures count is non-negative
//   - Verifies text and rpc-method are not both set (mutually exclusive)
//   - Validates format is "auto", "json", or "raw"
//   - Validates colorMode is "auto", "always", or "never"
//   - Ensures buffer and summaryInterval are non-negative
//   - Enforces subscribe-once requires count == 1
//
// This method also normalizes configuration (e.g., sets count=1 default in non-subscribe mode).
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
