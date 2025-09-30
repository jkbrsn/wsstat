// Package app measures utilizes the wsstat package to measure the latency of a WebSocket
// connection and print the results.
package app

import (
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
	Count       int      // Nr of interactions to perform; 0 means unlimited in subscription mode
	Headers     []string // HTTP headers for connection establishment ("Key: Value")
	RPCMethod   string   // JSON-RPC method (no params)
	TextMessage string   // Text message

	// Output
	Format    string // Output formatting mode: "auto", "json", or "raw"
	ColorMode string // Color behavior: "auto", "always", or "never"

	// Verbosity
	Quiet          bool // suppress request/timing output
	VerbosityLevel int  // 0 = summary, 1 = extended, >=2 = full detail

	// Subscription mode
	Subscribe       bool
	SubscribeOnce   bool
	Buffer          int
	SummaryInterval time.Duration

	// The response of a MeasureLatency call. Is overwritten if the function is called again.
	Response any

	// The result of a MeasureLatency call. Is overwritten if the function is called again.
	Result *wsstat.Result
}

// MeasureLatency measures the latency of the WebSocket connection, applying different methods
// based on the flags passed to the program.
func (c *Client) MeasureLatency(target *url.URL) error {
	header, err := parseHeaders(c.Headers)
	if err != nil {
		return err
	}

	switch {
	case c.TextMessage != "":
		return c.measureText(target, header)
	case c.RPCMethod != "":
		return c.measureJSON(target, header)
	default:
		return c.measurePing(target, header)
	}
}

// Validate validates the Client is ready for measurement; it checks that the client settings are
// set to valid values.
func (c *Client) Validate() error {
	if c.Count < 0 {
		return errors.New("count must be zero or greater")
	}

	if c.TextMessage != "" && c.RPCMethod != "" {
		return errors.New("mutually exclusive messaging flags")
	}

	c.Format = strings.TrimSpace(strings.ToLower(c.Format))
	if c.Format == "" {
		c.Format = formatAuto
	}
	switch c.Format {
	case formatAuto, formatRaw, formatJSON:
		// valid
	default:
		return errors.New("format must be \"auto\", \"json\", or \"raw\"")
	}

	c.ColorMode = strings.TrimSpace(strings.ToLower(c.ColorMode))
	if c.ColorMode == "" {
		c.ColorMode = "auto"
	}
	if c.ColorMode != "auto" && c.ColorMode != "always" && c.ColorMode != "never" {
		return errors.New("color must be \"auto\", \"always\", or \"never\"")
	}

	if c.Buffer < 0 {
		return errors.New("buffer must be zero or greater")
	}

	if c.SummaryInterval < 0 {
		return errors.New("summary-interval must be zero or greater")
	}

	if c.SubscribeOnce {
		c.Subscribe = true
		if c.Count == 0 {
			c.Count = 1
		}
		if c.Count != 1 {
			return errors.New("count must equal 1 when subscribe-once is enabled")
		}
	}

	if c.Subscribe {
		return nil
	}

	if c.Count == 0 {
		c.Count = 1
	}
	if c.Count < 1 {
		return errors.New("count must be greater than 0")
	}
	return nil
}
