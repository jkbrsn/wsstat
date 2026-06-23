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
//	    app.WithMode(app.ModeStream),
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
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat/v3"
	"github.com/rs/zerolog"
)

// Client measures the latency of a WebSocket connection and manages subscription streams.
// Use NewClient with functional options to create and configure a Client.
//
// Fields are private; use accessor methods (Count(), Output(), etc.) to read configuration,
// or use MeasureLatency's return value to access results.
type Client struct {
	// Input
	count        int               // Nr of interactions; 0 means unlimited in subscription mode
	headers      []string          // HTTP headers for connection establishment ("Key: Value")
	resolves     map[string]string // DNS resolution overrides: "host:port" → "address"
	rpcMethod    string            // JSON-RPC method (no params)
	rpcVersion   string            // JSON-RPC version to speak: "2.0" (default) or "1.0"
	textMessage  string            // Text message
	subprotocols []string          // WebSocket subprotocols to negotiate

	// Output
	output      Output // whole-stdout contract: text, json, or raw
	body        Body   // body rendering (text output): auto or compact
	clip        bool   // clip each rendered line to terminal width (text output)
	colorMode   string // Color behavior: "auto", "always", or "never"
	showSecrets bool   // render sensitive header values instead of masking them (-vv)

	// Verbosity
	quiet          bool // suppress request/timing output
	verbosityLevel int  // 0 = summary, 1 = extended, >=2 = full detail

	// Mode
	mode            Mode // measure or stream
	once            bool // stream: exit after the first event
	buffer          int
	summaryInterval time.Duration

	// TLS configuration
	insecure bool // skip TLS certificate verification

	// Timeouts
	timeout    time.Duration // read/dial timeout; 0 means use library default
	closeGrace time.Duration // close-handshake echo wait; 0 means use library default

	// Limits
	readLimit int64 // max inbound message size; 0 uses library default, -1 disables

	// Standards
	validateUTF8 bool // validate UTF-8 on inbound text frames

	// Diagnostics
	debug  bool      // emit core debug logs, independent of -v/-vv output verbosity
	debugW io.Writer // destination for debug logs; nil uses os.Stderr
}

// Option configures a Client.
type Option func(*Client)

// NewClient creates a new Client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		count:     1,
		output:    OutputText,
		body:      BodyAuto,
		mode:      ModeMeasure,
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

// WithRPCVersion sets the JSON-RPC version to speak: "2.0" (default) or "1.0". 1.0 emits a
// version-less request with a positional params array and relaxes response decoding to accept
// 1.0 / version-less replies.
func WithRPCVersion(version string) Option {
	return func(c *Client) { c.rpcVersion = version }
}

// WithTextMessage configures a text message to send.
func WithTextMessage(msg string) Option {
	return func(c *Client) { c.textMessage = msg }
}

// WithOutput sets the whole-stdout contract (text, json, or raw).
func WithOutput(output Output) Option {
	return func(c *Client) { c.output = output }
}

// WithBodyRender sets the body rendering for text output (auto or compact).
func WithBodyRender(body Body) Option {
	return func(c *Client) { c.body = body }
}

// WithClip enables clipping each rendered line to terminal width (text output).
func WithClip(clip bool) Option {
	return func(c *Client) { c.clip = clip }
}

// WithShowSecrets renders sensitive header values (Authorization, Cookie, etc.) in -vv
// output instead of masking them. Off by default.
func WithShowSecrets(show bool) Option {
	return func(c *Client) { c.showSecrets = show }
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

// WithMode sets the operation mode (measure or stream).
func WithMode(mode Mode) Option {
	return func(c *Client) { c.mode = mode }
}

// WithStreamOnce makes stream mode exit after the first event.
func WithStreamOnce(once bool) Option {
	return func(c *Client) { c.once = once }
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

// WithTimeout sets the read/dial timeout. Zero uses the library default (5s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithCloseGrace sets the close-handshake echo wait. Zero uses the library default (3s).
// Values of 5s or more have no effect; the transport caps its close handshake at 5s.
func WithCloseGrace(d time.Duration) Option {
	return func(c *Client) { c.closeGrace = d }
}

// WithReadLimit sets the max inbound message size in bytes. Zero uses the library default
// (16 MiB); a negative value disables the limit.
func WithReadLimit(n int64) Option {
	return func(c *Client) { c.readLimit = n }
}

// WithSubprotocols sets the WebSocket subprotocols to offer during the handshake.
func WithSubprotocols(subprotocols []string) Option {
	return func(c *Client) { c.subprotocols = subprotocols }
}

// WithValidateUTF8 enables UTF-8 validation of inbound text frames. Invalid frames are counted
// and surfaced as a warning rather than failing the connection.
func WithValidateUTF8(enabled bool) Option {
	return func(c *Client) { c.validateUTF8 = enabled }
}

// WithDebug enables the core's zerolog debug logs, written to stderr (or the WithDebugWriter
// destination). It is independent of the -v/-vv output verbosity: it never touches the stdout
// output contract, so it is safe to combine with any -o mode or -q.
func WithDebug(enabled bool) Option {
	return func(c *Client) { c.debug = enabled }
}

// WithDebugWriter sets the destination for debug logs (default os.Stderr). Mainly for tests.
func WithDebugWriter(w io.Writer) Option {
	return func(c *Client) { c.debugW = w }
}

// Count returns the configured interaction count.
func (c *Client) Count() int { return c.count }

// Output returns the configured output contract.
func (c *Client) Output() Output { return c.output }

// Body returns the configured body rendering.
func (c *Client) Body() Body { return c.body }

// Once reports whether stream mode exits after the first event.
func (c *Client) Once() bool { return c.once }

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

	if c.timeout > 0 {
		opts = append(opts, wsstat.WithTimeout(c.timeout))
	}

	if c.closeGrace > 0 {
		opts = append(opts, wsstat.WithCloseGrace(c.closeGrace))
	}

	if c.readLimit != 0 {
		opts = append(opts, wsstat.WithReadLimit(c.readLimit))
	}

	if len(c.subprotocols) > 0 {
		opts = append(opts, wsstat.WithSubprotocols(c.subprotocols))
	}

	if c.validateUTF8 {
		opts = append(opts, wsstat.WithValidateUTF8(true))
	}

	if c.debug {
		opts = append(opts, wsstat.WithLogger(debugLogger(c.debugW)))
	}

	return opts
}

// debugLogger builds the core debug logger. A nil writer defaults to os.Stderr so debug
// output never collides with the stdout output contract.
func debugLogger(w io.Writer) zerolog.Logger {
	dst := w
	if dst == nil {
		dst = os.Stderr
	}
	return zerolog.New(dst).Level(zerolog.DebugLevel).With().Timestamp().Logger()
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
//   - Normalizes the output ("text"/"json"/"raw") and body ("auto"/"compact") enums
//   - Validates colorMode is "auto", "always", or "never"
//   - Ensures buffer and summaryInterval are non-negative
//
// Mode-specific count bounds are validated by the caller (cmd) before construction;
// this method only applies the measure-mode default (count=1 when unset).
func (c *Client) Validate() error {
	if c.count < 0 {
		return errors.New("count must be zero or greater")
	}

	if c.textMessage != "" && c.rpcMethod != "" {
		return errors.New("mutually exclusive messaging flags")
	}

	output, err := ParseOutput(string(c.output))
	if err != nil {
		return err
	}
	c.output = output

	body, err := ParseBody(string(c.body))
	if err != nil {
		return err
	}
	c.body = body

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

	if c.mode == ModeStream {
		if c.once && c.count > 1 {
			return errors.New("count must be 0 or 1 when --once is set")
		}
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
