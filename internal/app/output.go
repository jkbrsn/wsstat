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

var (
	printValueTemp         = "%s: %s\n"
	printIndentedValueTemp = "  %s: %s\n"

	wssPrintTemplate = `
  DNS Lookup    TCP Connection    TLS Handshake    WS Handshake    %-17s
|%s  |      %s  |     %s  |    %s  |   %s  |
|           |                 |                |               |              |
|  DNS lookup:%s        |                |               |              |
|                 TCP connected:%s       |               |              |
|                                       TLS done:%s      |              |
|                                                        WS done:%s     |
-                                                                         Total:%s
`
	wsPrintTemplate = `
  DNS Lookup    TCP Connection    WS Handshake    %-17s
|%s  |      %s  |    %s  |  %s   |
|           |                 |               |              |
|  DNS lookup:%s        |               |              |
|                 TCP connected:%s      |              |
|                                       WS done:%s     |
-                                                        Total:%s
`
)

// buildTimingSummary builds a timing summary.
func (c *Client) buildTimingSummary(u *url.URL) timingSummaryJSON {
	result := c.Result
	mode := "single"
	if c.Count > 1 || (result != nil && result.MessageCount > 1) {
		mode = "mean"
	}
	summary := timingSummaryJSON{
		Type:      "timing",
		Mode:      mode,
		Counts:    timingCountsJSON{Requested: c.Count},
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
	switch c.ColorMode {
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
func (*Client) printJSONLine(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal JSON output: %v\n", err)
		return
	}
	_, _ = os.Stdout.Write(append(data, '\n'))
}

// printSubscriptionMessage prints a subscription message.
func (c *Client) printSubscriptionMessage(index int, msg wsstat.SubscriptionMessage) error {
	if c.Format == formatJSON {
		c.printJSONLine(c.subscriptionMessageJSON(index, msg))
		return nil
	}

	payload := string(msg.Data)
	if c.Quiet || c.Format == formatRaw {
		fmt.Println(payload)
		return nil
	}

	timestamp := msg.Received.Format(time.RFC3339Nano)
	if c.VerbosityLevel >= 1 {
		fmt.Printf("[%04d @ %s] %d bytes\n", index, timestamp, msg.Size)
		if formatted := formatJSONIfPossible(msg.Data); formatted != "" {
			fmt.Println(formatted)
		} else {
			fmt.Println(payload)
		}
		fmt.Println()
		return nil
	}

	line := payload
	if formatted := formatJSONIfPossible(msg.Data); formatted != "" {
		line = formatted
	}
	fmt.Printf("[%04d @ %s] %s\n", index, timestamp, line)
	return nil
}

// printSubscriptionSummary prints a subscription summary.
func (c *Client) printSubscriptionSummary(target *url.URL) {
	if c.Result == nil {
		return
	}

	if c.Format == formatJSON {
		c.printJSONLine(c.subscriptionSummaryJSON(target))
		if c.VerbosityLevel >= 1 {
			_ = c.PrintTimingResults(target)
		}
		return
	}

	fmt.Println()
	fmt.Println(c.colorizeOrange("Subscription summary"))
	if c.Result.SubscriptionFirstEvent > 0 {
		fmt.Printf("  First event latency: %s\n", formatDuration(c.Result.SubscriptionFirstEvent))
	}
	if c.Result.SubscriptionLastEvent > 0 {
		fmt.Printf("  Last event latency: %s\n", formatDuration(c.Result.SubscriptionLastEvent))
	}
	fmt.Printf("  Total messages: %d\n", c.Result.MessageCount)

	if len(c.Result.Subscriptions) > 0 {
		ids := make([]string, 0, len(c.Result.Subscriptions))
		for id := range c.Result.Subscriptions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			stats := c.Result.Subscriptions[id]
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

	if c.VerbosityLevel >= 1 {
		_ = c.PrintTimingResults(target)
	}
}

// printTimingResultsBasic prints a concise timing summary used for verbosity level 0.
func (c *Client) printTimingResultsBasic(result *wsstat.Result, count int) {
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
func (c *Client) printVerbose() {
	fmt.Printf(printValueTemp, c.colorizeOrange("Target"), c.Result.URL.Hostname())
	for _, values := range c.Result.IPs {
		fmt.Printf(printValueTemp, c.colorizeOrange("IP"), values)
	}
	fmt.Printf("%s: %d\n", c.colorizeOrange("Messages sent:"), c.Result.MessageCount)
	for key, values := range c.Result.RequestHeaders {
		if key == "Sec-WebSocket-Version" {
			fmt.Printf(printValueTemp,
				c.colorizeOrange("WS version"), strings.Join(values, ", "))
		}
	}
	if c.Result.TLSState != nil {
		fmt.Printf(printValueTemp,
			c.colorizeOrange("TLS version"), tls.VersionName(c.Result.TLSState.Version))
	}
}

// printVVerbose prints the request details with verbosity level 2.
func (c *Client) printVVerbose() {
	fmt.Println(c.colorizeOrange("Target"))
	fmt.Printf("  %s:  %s\n", c.colorizeGreen("URL"), c.Result.URL.Hostname())
	for _, ip := range c.Result.IPs {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen("IP"), ip)
	}
	fmt.Printf("  %s: %d\n", c.colorizeGreen("Messages sent"), c.Result.MessageCount)
	fmt.Println()
	if c.Result.TLSState != nil {
		fmt.Println(c.colorizeOrange("TLS"))
		fmt.Printf(printIndentedValueTemp,
			c.colorizeGreen("Version"), tls.VersionName(c.Result.TLSState.Version))
		fmt.Printf(printIndentedValueTemp,
			c.colorizeGreen("Cipher Suite"), tls.CipherSuiteName(c.Result.TLSState.CipherSuite))
		for i, cert := range c.Result.TLSState.PeerCertificates {
			fmt.Printf("  %s: %d\n", c.colorizeGreen("Certificate"), i+1)
			fmt.Printf("    Subject: %s\n", cert.Subject)
			fmt.Printf("    Issuer: %s\n", cert.Issuer)
			fmt.Printf("    Not Before: %s\n", cert.NotBefore)
			fmt.Printf("    Not After: %s\n", cert.NotAfter)
		}
		fmt.Println()
	}
	fmt.Println(c.colorizeOrange("Request headers"))
	for key, values := range c.Result.RequestHeaders {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen(key), strings.Join(values, ", "))
	}
	fmt.Println(c.colorizeOrange("Response headers"))
	for key, values := range c.Result.ResponseHeaders {
		fmt.Printf(printIndentedValueTemp, c.colorizeGreen(key), strings.Join(values, ", "))
	}
}

// PrintRequestDetails prints the results of the Client, with verbosity based on the flags
// passed to the program. If no results have been produced yet, the function errors.
func (c *Client) PrintRequestDetails() error {
	if c.Result == nil {
		return errors.New("no results have been produced")
	}
	if c.Quiet {
		return nil
	}
	if c.Format == formatJSON {
		return nil
	}

	fmt.Println()

	switch {
	case c.VerbosityLevel >= 2:
		c.printVVerbose()
	case c.VerbosityLevel >= 1:
		c.printVerbose()
	default:
		fmt.Printf(printValueTemp, c.colorizeGreen("URL"), c.Result.URL.Hostname())
		if len(c.Result.IPs) > 0 {
			fmt.Printf("%s:  %s\n", c.colorizeGreen("IP"), c.Result.IPs[0])
		}
	}

	return nil
}

// PrintResponse prints the response to the terminal if there is one, otherwise does nothing.
func (c *Client) PrintResponse() {
	if c.Response == nil {
		return
	}

	if c.Format == formatJSON {
		c.printJSONLine(responseOutputJSON{
			Type:      "response",
			RPCMethod: c.RPCMethod,
			Payload:   normalizeResponseForJSON(c.Response),
		})
		return
	}

	label := c.colorizeOrange("Response")
	baseMessage := label + ": "

	if c.Quiet {
		baseMessage = ""
	}

	if c.Format == formatRaw {
		fmt.Printf("%s%v\n", baseMessage, c.Response)
	} else if responseMap, ok := c.Response.(map[string]any); ok {
		if _, isJSON := responseMap["jsonrpc"]; isJSON || c.RPCMethod != "" {
			responseJSON, err := json.Marshal(responseMap)
			if err != nil {
				fmt.Printf("could not marshal response '%v' to JSON: %v", responseMap, err)
			}
			fmt.Printf("%s%s\n", baseMessage, responseJSON)
		} else {
			fmt.Printf("%s%v\n", baseMessage, responseMap)
		}
	} else if responseArray, ok := c.Response.([]any); ok {
		fmt.Printf("%s%v\n", baseMessage, responseArray)
	} else if responseBytes, ok := c.Response.([]byte); ok {
		fmt.Printf("%s%s\n", baseMessage, string(responseBytes))
	}
}

// PrintTimingResults prints the WebSocket statistics to the terminal.
func (c *Client) PrintTimingResults(u *url.URL) error {
	if c.Result == nil {
		return errors.New("no results have been produced")
	}

	if c.Quiet {
		return nil
	}

	if c.Format == formatJSON {
		c.printJSONLine(c.buildTimingSummary(u))
		return nil
	}

	switch {
	case c.VerbosityLevel <= 0:
		c.printTimingResultsBasic(c.Result, c.Count)
	default:
		rttLabel := "Message RTT"
		if c.Count > 1 || c.Result.MessageCount > 1 {
			rttLabel = "Mean Message RTT"
		}
		c.printTimingResultsTiered(c.Result, u, rttLabel)
	}

	return nil
}
