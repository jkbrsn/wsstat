package app

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jkbrsn/wsstat/v3"
	"github.com/jkbrsn/wsstat/v3/internal/app/color"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// buildTimingSummaryFromResult builds a timing summary from a result.
func (c *Client) buildTimingSummaryFromResult(u *url.URL, result *wsstat.Result) timingSummaryJSON {
	mode := "single"
	if c.count > 1 || (result != nil && result.MessageCount > 1) {
		mode = "mean"
	}
	summary := timingSummaryJSON{
		Schema:    JSONSchemaVersion,
		Type:      "timing",
		Mode:      mode,
		Counts:    timingCountsJSON{Requested: c.count},
		Durations: timingDurationsJSON{},
	}
	if result != nil {
		summary.Counts.Messages = result.MessageCount
		summary.Target = buildTimingTarget(result, u)
		summary.Durations.DNSLookup = msPtr(result.DNSLookup)
		summary.Durations.TCPConnection = msPtr(result.TCPConnection)
		summary.Durations.TLSHandshake = msPtr(result.TLSHandshake)
		summary.Durations.WSHandshake = msPtr(result.WSHandshake)
		summary.Durations.MessageRTT = msPtr(result.MessageRTT)
		summary.Durations.Total = msPtr(result.TotalTime)
		if timeline := buildTimingTimeline(result); timeline != nil {
			summary.Timeline = timeline
		}
		summary.Warnings = resultWarnings(result)
	}
	return summary
}

// resultWarnings collects non-fatal standards warnings surfaced from a measurement Result, for
// display in both text and JSON output. Returns nil when there is nothing to warn about.
func resultWarnings(result *wsstat.Result) []string {
	if result == nil || result.InvalidUTF8Frames == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"%d inbound text frame(s) failed UTF-8 validation", result.InvalidUTF8Frames)}
}

// colorEnabled returns true if color output is enabled, based on both color mode and terminal
// detection.
func (c *Client) colorEnabled() bool {
	switch c.colorMode {
	case "always":
		return true
	case "auto", "":
	default:
		// never
		return false
	}

	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}

	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// colorizeOrange returns the text with orange color applied if color output is enabled.
func (c *Client) colorizeOrange(text string) string {
	if !c.colorEnabled() {
		return text
	}
	return color.WSOrange.Sprint(text)
}

// colorizeGreen returns the text with green color applied if color output is enabled.
func (c *Client) colorizeGreen(text string) string {
	if !c.colorEnabled() {
		return text
	}
	return color.TeaGreen.Sprint(text)
}

// printJSONLine prints a JSON line.
func (*Client) printJSONLine(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON output: %w", err)
	}
	if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	return nil
}

// printSubscriptionMessage prints a subscription message.
func (c *Client) printSubscriptionMessage(index int, msg wsstat.SubscriptionMessage) error {
	switch c.output {
	case OutputJSON:
		return c.printJSONLine(c.subscriptionMessageJSON(index, msg))
	case OutputRaw:
		return writeRaw(msg.Data)
	}

	payload := string(msg.Data)
	if c.quiet {
		// Quiet prints the payload only (no index/timestamp/size chrome), but still
		// honors --body and --clip so rendering matches measure mode's PrintResponse.
		body := payload
		if formatted, err := renderJSON(msg.Data, c.body == BodyCompact); err == nil {
			body = formatted
		}
		fmt.Println(c.clipBody(body))
		return nil
	}

	singleLine := c.body == BodyCompact
	timestamp := msg.Received.Format(time.RFC3339Nano)
	if c.verbosityLevel >= 1 {
		if singleLine {
			body := payload
			if formatted, err := renderJSON(msg.Data, true); err == nil {
				body = formatted
			}
			line := fmt.Sprintf("[%04d @ %s] %d bytes %s", index, timestamp, msg.Size, body)
			fmt.Println(c.clipBody(line))
			return nil
		}
		fmt.Printf("[%04d @ %s] %d bytes\n", index, timestamp, msg.Size)
		if formatted, err := renderJSON(msg.Data, false); err == nil {
			fmt.Println(c.clipBody(formatted))
		} else {
			fmt.Println(c.clipBody(payload))
		}
		fmt.Println()
		return nil
	}

	body := payload
	if formatted, err := renderJSON(msg.Data, singleLine); err == nil {
		body = formatted
	}
	line := fmt.Sprintf("[%04d @ %s] %s", index, timestamp, body)
	fmt.Println(c.clipBody(line))
	return nil
}

// truncMarker is appended to lines clipped to terminal width.
const truncMarker = "..."

// writeRaw writes payload bytes to stdout verbatim: no label, color, or trailing
// newline. Used by the raw output contract in both measure and stream modes.
//
// Raw adds nothing, ever. Payloads may be binary frames, so any injected delimiter
// (e.g. a per-frame newline) would corrupt them and is ambiguous for text payloads
// that contain newlines. Stream frames are therefore concatenated undelimited; the
// JSON output contract (one NDJSON envelope per frame) is the parseable path.
func writeRaw(b []byte) error {
	if _, err := os.Stdout.Write(b); err != nil {
		return fmt.Errorf("failed to write raw output: %w", err)
	}
	return nil
}

// clipBody clips each line of a (possibly multi-line) rendered body to the
// terminal width when --clip is set and stdout is a terminal. When clipping is
// disabled or the width cannot be determined (e.g. piped/redirected output), s
// is returned unchanged.
func (c *Client) clipBody(s string) string {
	return c.clipBodyWithPrefix(s, 0)
}

// clipBodyWithPrefix behaves like clipBody but reserves prefixWidth columns on
// the first line for a label printed before the body (e.g. a colored "Response: "
// that cannot itself be clipped without corrupting its ANSI codes). Continuation
// lines of a multi-line body get the full width.
func (c *Client) clipBodyWithPrefix(s string, prefixWidth int) string {
	if !c.clip {
		return s
	}
	width := terminalWidth()
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		w := width
		if i == 0 {
			if w -= prefixWidth; w < 0 {
				w = 0
			}
		}
		lines[i] = clipToWidth(line, w)
	}
	return strings.Join(lines, "\n")
}

// terminalWidth returns the column count of stdout, or 0 when stdout is not a
// terminal or its size cannot be determined.
func terminalWidth() int {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil {
		return 0
	}
	return w
}

// clipToWidth shortens s to at most width display columns (counted as runes),
// replacing the tail with truncMarker when truncation occurs.
func clipToWidth(s string, width int) string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	markerLen := utf8.RuneCountInString(truncMarker)
	if width <= markerLen {
		return string([]rune(truncMarker)[:width])
	}
	keep := width - markerLen
	runes := 0
	for i := range s {
		if runes == keep {
			return s[:i] + truncMarker
		}
		runes++
	}
	return s
}

// printSubscriptionSummary prints a subscription summary.
func (c *Client) printSubscriptionSummary(target *url.URL, result *wsstat.Result) {
	if c.output == OutputRaw {
		// Raw emits payload bytes only; no summary chrome.
		return
	}
	if c.output == OutputJSON {
		_ = c.printJSONLine(c.subscriptionSummaryJSON(target, result))
		if c.verbosityLevel >= 1 {
			_ = c.PrintTimingResults(target, &MeasurementResult{Result: result})
		}
		return
	}

	fmt.Println()
	fmt.Println(c.colorizeOrange("Subscription summary"))
	if result.SubscriptionFirstEvent > 0 {
		fmt.Printf("  First event latency: %s\n", formatDuration(result.SubscriptionFirstEvent))
	}
	if result.SubscriptionLastEvent > 0 {
		fmt.Printf("  Last event latency: %s\n", formatDuration(result.SubscriptionLastEvent))
	}
	fmt.Printf("  Total messages: %d\n", result.MessageCount)

	if len(result.Subscriptions) > 0 {
		ids := make([]string, 0, len(result.Subscriptions))
		for id := range result.Subscriptions {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			stats := result.Subscriptions[id]
			fmt.Printf("  %s: %d msgs, %d bytes", id, stats.MessageCount, stats.ByteCount)
			if stats.MeanInterArrival > 0 {
				fmt.Printf(", mean gap %s", formatDuration(stats.MeanInterArrival))
			}
			if stats.Error != nil {
				fmt.Printf(" (error: %v)", stats.Error)
			}
			fmt.Println()
		}
	}

	if c.verbosityLevel >= 1 {
		_ = c.PrintTimingResults(target, &MeasurementResult{Result: result})
	}
}

// printTimingResultsBasic prints a concise timing summary used for verbosity level 0.
func (c *Client) printTimingResultsBasic(result *wsstat.Result, count int) {
	const printValueTemp = "%s: %s\n"

	fmt.Println()
	rttString := "Round-trip time"
	if count > 1 {
		rttString = "Mean round-trip time"
	}
	msgCountString := "message"
	if result.MessageCount > 1 {
		msgCountString = "messages"
	}
	fmt.Printf(
		"%s: %s (%d %s)\n",
		rttString,
		c.colorizeOrange(msString(result.MessageRTT)+"ms"),
		result.MessageCount,
		msgCountString)
	fmt.Printf(
		printValueTemp,
		"Total time",
		c.colorizeOrange(msString(result.TotalTime)+"ms"))
	fmt.Println()
}

// printTimingResultsTiered formats and prints the WebSocket statistics to the terminal
// in a tiered fashion.
func (c *Client) printTimingResultsTiered(result *wsstat.Result, u *url.URL, label string) {
	const wssPrintTemplate = `
  DNS Lookup    TCP Connection    TLS Handshake    WS Handshake    %-17s
|%s  |      %s  |     %s  |    %s  |   %s  |
|           |                 |                |               |              |
|  DNS lookup:%s        |                |               |              |
|                 TCP connected:%s       |               |              |
|                                       TLS done:%s      |              |
|                                                        WS done:%s     |
-                                                                         Total:%s
`
	const wsPrintTemplate = `
  DNS Lookup    TCP Connection    WS Handshake    %-17s
|%s  |      %s  |    %s  |  %s   |
|           |                 |               |              |
|  DNS lookup:%s        |               |              |
|                 TCP connected:%s      |              |
|                                       WS done:%s     |
-                                                        Total:%s
`

	switch u.Scheme {
	case "wss":
		fmt.Fprintf(os.Stdout, wssPrintTemplate,
			label,
			c.colorizeGreen(formatPadLeft(result.DNSLookup)),
			c.colorizeGreen(formatPadLeft(result.TCPConnection)),
			c.colorizeGreen(formatPadLeft(result.TLSHandshake)),
			c.colorizeGreen(formatPadLeft(result.WSHandshake)),
			c.colorizeGreen(formatPadLeft(result.MessageRTT)),
			c.colorizeGreen(formatPadRight(result.DNSLookupDone)),
			c.colorizeGreen(formatPadRight(result.TCPConnected)),
			c.colorizeGreen(formatPadRight(result.TLSHandshakeDone)),
			c.colorizeGreen(formatPadRight(result.WSHandshakeDone)),
			c.colorizeOrange(formatPadRight(result.TotalTime)),
		)
	default:
		// ws as default
		fmt.Fprintf(os.Stdout, wsPrintTemplate,
			label,
			c.colorizeGreen(formatPadLeft(result.DNSLookup)),
			c.colorizeGreen(formatPadLeft(result.TCPConnection)),
			c.colorizeGreen(formatPadLeft(result.WSHandshake)),
			c.colorizeGreen(formatPadLeft(result.MessageRTT)),
			c.colorizeGreen(formatPadRight(result.DNSLookupDone)),
			c.colorizeGreen(formatPadRight(result.TCPConnected)),
			c.colorizeGreen(formatPadRight(result.WSHandshakeDone)),
			c.colorizeOrange(formatPadRight(result.TotalTime)),
		)
	}
	fmt.Println()
}

// printVerbose prints the request details with verbosity level 1.
func (c *Client) printVerbose(result *wsstat.Result) {
	const printValueTemp = "%s: %s\n"

	fmt.Printf(printValueTemp, c.colorizeOrange("Target"), result.URL.Hostname())
	for _, values := range result.IPs {
		fmt.Printf(printValueTemp, c.colorizeOrange("IP"), values)
	}
	fmt.Printf("%s: %d\n", c.colorizeOrange("Messages sent"), result.MessageCount)
	for key, values := range result.RequestHeaders {
		if key == "Sec-WebSocket-Version" {
			fmt.Printf(printValueTemp,
				c.colorizeOrange("WS version"), strings.Join(values, ", "))
		}
	}
	if result.TLSState != nil {
		fmt.Printf(printValueTemp,
			c.colorizeOrange("TLS version"), tls.VersionName(result.TLSState.Version))
	}
}

// printVVerbose prints the request details with verbosity level 2.
func (c *Client) printVVerbose(result *wsstat.Result) {
	const printIndentedValueTemp = "  %s: %s\n"

	fmt.Println(c.colorizeOrange("Target"))
	fmt.Printf("  %s:  %s\n", c.colorizeGreen("URL"), result.URL.Hostname())
	for _, ip := range result.IPs {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen("IP"), ip)
	}
	fmt.Printf("  %s: %d\n", c.colorizeGreen("Messages sent"), result.MessageCount)
	fmt.Println()
	if result.TLSState != nil {
		fmt.Println(c.colorizeOrange("TLS"))
		fmt.Printf(printIndentedValueTemp,
			c.colorizeGreen("Version"), tls.VersionName(result.TLSState.Version))
		fmt.Printf(printIndentedValueTemp,
			c.colorizeGreen("Cipher Suite"), tls.CipherSuiteName(result.TLSState.CipherSuite))
		for i, cert := range result.TLSState.PeerCertificates {
			fmt.Printf("  %s: %d\n", c.colorizeGreen("Certificate"), i+1)
			fmt.Printf("    Subject: %s\n", cert.Subject)
			fmt.Printf("    Issuer: %s\n", cert.Issuer)
			fmt.Printf("    Not Before: %s\n", cert.NotBefore)
			fmt.Printf("    Not After: %s\n", cert.NotAfter)
		}
		fmt.Println()
	}
	fmt.Println(c.colorizeOrange("Request headers"))
	for key, values := range result.RequestHeaders {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen(key), c.headerValue(key, values))
	}
	fmt.Println(c.colorizeOrange("Response headers"))
	for key, values := range result.ResponseHeaders {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen(key), c.headerValue(key, values))
	}
}

// sensitiveHeaders are the standard header names whose values are masked in -vv output
// unless --show-secrets is set. Compared case-insensitively against the canonical form.
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Set-Cookie":          true,
}

// sensitiveHeaderSubstrings flag non-standard credential headers (e.g. X-Api-Key,
// X-Auth-Token, X-Amz-Security-Token) that the standard set above misses; many WS/RPC
// endpoints authenticate via custom headers. Matched case-insensitively as substrings of
// the header name, so masking errs toward over-redacting (revealed by --show-secrets).
var sensitiveHeaderSubstrings = []string{
	"auth", "cookie", "token", "secret", "api-key", "apikey", "password",
}

// isSensitiveHeader reports whether a header's value should be masked in -vv output.
func isSensitiveHeader(key string) bool {
	canonical := http.CanonicalHeaderKey(key)
	if sensitiveHeaders[canonical] {
		return true
	}
	lower := strings.ToLower(canonical)
	for _, s := range sensitiveHeaderSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// headerValue renders a header's values for -vv output, masking sensitive ones unless
// --show-secrets is set.
func (c *Client) headerValue(key string, values []string) string {
	if !c.showSecrets && isSensitiveHeader(key) {
		return "[redacted]"
	}
	return strings.Join(values, ", ")
}

// PrintRequestDetails prints connection and request information to stdout.
// Output verbosity is controlled by client configuration:
//   - verbosity 0: URL and IP only
//   - verbosity 1: adds target summary, message count, TLS version
//   - verbosity 2+: adds full TLS details, certificates, and all headers
//
// In JSON format mode, this outputs nothing (details are in timing JSON).
// Returns an error if result is nil or contains no result data.
func (c *Client) PrintRequestDetails(result *MeasurementResult) error {
	if result == nil || result.Result == nil {
		return errors.New("no results available")
	}
	if c.quiet {
		return nil
	}
	// JSON puts request details in the timing envelope; raw emits payload bytes
	// only. Both suppress the human request-detail block.
	if c.output == OutputJSON || c.output == OutputRaw {
		return nil
	}

	fmt.Println()

	switch {
	case c.verbosityLevel >= 2:
		c.printVVerbose(result.Result)
	case c.verbosityLevel >= 1:
		c.printVerbose(result.Result)
	default:
		const printValueTemp = "%s: %s\n"
		fmt.Printf(printValueTemp, c.colorizeGreen("URL"), result.Result.URL.Hostname())
		if len(result.Result.IPs) > 0 {
			fmt.Printf("%s:  %s\n", c.colorizeGreen("IP"), result.Result.IPs[0])
		}
	}

	return nil
}

// PrintResponse prints the WebSocket server response to stdout, returning any
// write error so a failed or closed stdout propagates as a non-zero exit.
// Output depends on the configured output contract:
//   - json: structured JSON envelope with schema_version
//   - raw: payload bytes verbatim, no label/color/newline (structured JSON-RPC
//     responses are re-marshaled compactly, since the original frame bytes are
//     decoded before reaching this layer)
//   - text: "Response:" label; any JSON body (JSON-RPC or plain) rendered per
//     --body (auto pretty, compact one-line), non-JSON printed as-is
//
// Does nothing if result is nil or result.Response is nil.
func (c *Client) PrintResponse(result *MeasurementResult) error {
	if result == nil || result.Response == nil {
		return nil
	}

	response := result.Response

	switch c.output {
	case OutputJSON:
		return c.printJSONLine(responseOutputJSON{
			Schema:    JSONSchemaVersion,
			Type:      "response",
			RPCMethod: c.rpcMethod,
			Payload:   normalizeResponseForJSON(response),
		})
	case OutputRaw:
		return writeRaw(rawResponseBytes(response))
	}

	const labelText = "Response: "
	baseMessage := c.colorizeOrange("Response") + ": "
	prefixWidth := utf8.RuneCountInString(labelText)
	if c.quiet {
		baseMessage = ""
		prefixWidth = 0
	}
	clip := func(s string) string { return c.clipBodyWithPrefix(s, prefixWidth) }

	compact := c.body == BodyCompact
	// renderBody applies --body (auto pretty / compact one-line) to any JSON
	// payload, falling back to the plain text when it is not JSON. This matches
	// how stream messages are rendered, so a measured response that happens to be
	// JSON (not only JSON-RPC) is shaped by --body too.
	renderBody := func(data []byte, fallback string) string {
		if formatted, ferr := renderJSON(data, compact); ferr == nil {
			return formatted
		}
		return fallback
	}
	var err error
	emit := func(body string) { _, err = fmt.Printf("%s%s\n", baseMessage, clip(body)) }
	switch v := response.(type) {
	case map[string]any:
		if _, isJSON := v["jsonrpc"]; isJSON || c.rpcMethod != "" {
			responseJSON, merr := json.Marshal(v)
			if merr != nil {
				return fmt.Errorf("failed to marshal response: %w", merr)
			}
			emit(renderBody(responseJSON, string(responseJSON)))
		} else {
			emit(fmt.Sprintf("%v", v))
		}
	case []any:
		emit(fmt.Sprintf("%v", v))
	case []byte:
		emit(renderBody(v, string(v)))
	case string:
		emit(renderBody([]byte(v), v))
	default:
		emit(fmt.Sprintf("%v", v))
	}
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}
	return nil
}

// rawResponseBytes renders a measured response as verbatim bytes for the raw
// output contract. Structured (JSON-RPC) responses are marshaled compactly.
func rawResponseBytes(v any) []byte {
	switch r := v.(type) {
	case []byte:
		return r
	case string:
		return []byte(r)
	case json.RawMessage:
		return []byte(r)
	default:
		if b, err := json.Marshal(r); err == nil {
			return b
		}
		return fmt.Appendf(nil, "%v", r)
	}
}

// PrintTimingResults prints connection timing statistics to stdout.
// Output format depends on client configuration:
//   - JSON mode: outputs structured timing JSON with schema_version
//   - verbosity 0: outputs simple round-trip time and total
//   - verbosity 1+: outputs detailed timing diagram showing all phases
//
// Returns an error if result is nil or contains no result data.
func (c *Client) PrintTimingResults(u *url.URL, result *MeasurementResult) error {
	if result == nil || result.Result == nil {
		return errors.New("no results available")
	}

	if c.quiet {
		return nil
	}

	if c.output == OutputJSON {
		return c.printJSONLine(c.buildTimingSummaryFromResult(u, result.Result))
	}
	// Raw emits payload bytes only; no timing report.
	if c.output == OutputRaw {
		return nil
	}

	for _, warn := range resultWarnings(result.Result) {
		fmt.Printf("%s %s\n", c.colorizeOrange("warning:"), warn)
	}

	switch {
	case c.verbosityLevel <= 0:
		c.printTimingResultsBasic(result.Result, c.count)
	default:
		rttLabel := "Message RTT"
		if c.count > 1 || result.Result.MessageCount > 1 {
			rttLabel = "Mean Message RTT"
		}
		c.printTimingResultsTiered(result.Result, u, rttLabel)
	}

	return nil
}
