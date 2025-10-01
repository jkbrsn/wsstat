package app

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat"
	"github.com/jkbrsn/wsstat/internal/app/color"
	"github.com/mattn/go-isatty"
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
	}
	return summary
}

// colorEnabled returns true if color output is enabled, based on both color mode and terminal
// detection.
func (c *Client) colorEnabled() bool {
	switch c.colorMode {
	case "always":
		return true
	case "never":
		return false
	case "auto", "":
	default:
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
	if c.format == formatJSON {
		return c.printJSONLine(c.subscriptionMessageJSON(index, msg))
	}

	payload := string(msg.Data)
	if c.quiet || c.format == formatRaw {
		fmt.Println(payload)
		return nil
	}

	timestamp := msg.Received.Format(time.RFC3339Nano)
	if c.verbosityLevel >= 1 {
		fmt.Printf("[%04d @ %s] %d bytes\n", index, timestamp, msg.Size)
		if formatted, err := formatJSONIfPossible(msg.Data); err == nil {
			fmt.Println(formatted)
		} else {
			fmt.Println(payload)
		}
		fmt.Println()
		return nil
	}

	line := payload
	if formatted, err := formatJSONIfPossible(msg.Data); err == nil {
		line = formatted
	}
	fmt.Printf("[%04d @ %s] %s\n", index, timestamp, line)
	return nil
}

// printSubscriptionSummary prints a subscription summary.
func (c *Client) printSubscriptionSummary(target *url.URL, result *wsstat.Result) {
	if c.format == formatJSON {
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
		sort.Strings(ids)
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
		c.colorizeOrange(strconv.FormatInt(result.MessageRTT.Milliseconds(), 10)+"ms"),
		result.MessageCount,
		msgCountString)
	fmt.Printf(
		printValueTemp,
		"Total time",
		c.colorizeOrange(strconv.FormatInt(result.TotalTime.Milliseconds(), 10)+"ms"))
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
	case "ws":
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
	default:
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
	fmt.Printf("%s: %d\n", c.colorizeOrange("Messages sent:"), result.MessageCount)
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
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen(key), strings.Join(values, ", "))
	}
	fmt.Println(c.colorizeOrange("Response headers"))
	for key, values := range result.ResponseHeaders {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen(key), strings.Join(values, ", "))
	}
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
	if c.format == formatJSON {
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

// PrintResponse prints the WebSocket server response to stdout.
// Output format depends on client configuration:
//   - JSON mode: outputs structured JSON with schema_version
//   - raw mode: outputs response as-is without labels
//   - auto mode: outputs with "Response:" label, formats JSON-RPC prettily
//
// Does nothing if result is nil or result.Response is nil.
func (c *Client) PrintResponse(result *MeasurementResult) {
	if result == nil || result.Response == nil {
		return
	}

	response := result.Response

	if c.format == formatJSON {
		_ = c.printJSONLine(responseOutputJSON{
			Schema:    JSONSchemaVersion,
			Type:      "response",
			RPCMethod: c.rpcMethod,
			Payload:   normalizeResponseForJSON(response),
		})
		return
	}

	label := c.colorizeOrange("Response")
	baseMessage := label + ": "

	if c.quiet {
		baseMessage = ""
	}

	if c.format == formatRaw {
		fmt.Printf("%s%v\n", baseMessage, response)
	} else if responseMap, ok := response.(map[string]any); ok {
		if _, isJSON := responseMap["jsonrpc"]; isJSON || c.rpcMethod != "" {
			responseJSON, err := json.Marshal(responseMap)
			if err != nil {
				fmt.Printf("could not marshal response '%v' to JSON: %v", responseMap, err)
			}
			fmt.Printf("%s%s\n", baseMessage, responseJSON)
		} else {
			fmt.Printf("%s%v\n", baseMessage, responseMap)
		}
	} else if responseArray, ok := response.([]any); ok {
		fmt.Printf("%s%v\n", baseMessage, responseArray)
	} else if responseBytes, ok := response.([]byte); ok {
		fmt.Printf("%s%s\n", baseMessage, string(responseBytes))
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

	if c.format == formatJSON {
		return c.printJSONLine(c.buildTimingSummaryFromResult(u, result.Result))
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
