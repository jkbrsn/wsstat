// Package app measures utilizes the wsstat package to measure to construct a
// client that measures the latency of a WebSocket connection.
package app

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat"
)

var (
	// Templates for printing singular results
	printValueTemp         = `%s: %s\n`
	printIndentedValueTemp = `  %s: %s\n`

	// Templates for printing tiered results
	wssPrintTemplate = `` +
		`  DNS Lookup    TCP Connection    TLS Handshake    WS Handshake    Message RTT` + "\n" +
		`|%s  |      %s  |     %s  |    %s  |   %s  |` + "\n" +
		`|           |                 |                |               |              |` + "\n" +
		`|  DNS lookup:%s        |                |               |              |` + "\n" +
		`|                 TCP connected:%s       |               |              |` + "\n" +
		`|                                       TLS done:%s      |              |` + "\n" +
		`|                                                        WS done:%s     |` + "\n" +
		`-                                                                         Total:%s` + "\n"
	wsPrintTemplate = `` +
		`  DNS Lookup    TCP Connection    WS Handshake    Message RTT` + "\n" +
		`|%s  |      %s  |    %s  |  %s   |` + "\n" +
		`|           |                 |               |              |` + "\n" +
		`|  DNS lookup:%s        |               |              |` + "\n" +
		`|                 TCP connected:%s      |              |` + "\n" +
		`|                                       WS done:%s     |` + "\n" +
		`-                                                        Total:%s` + "\n"
)

// Client measures the latency of a WebSocket connection, applying different methods
// based on the settings passed to the struct.
type Client struct {
	// Input
	Burst        int    // Number of messages to send in a burst (if > 1)
	InputHeaders string // Comma-separated headers for connection establishment
	JSONMethod   string // A single JSON RPC method (no params)
	TextMessage  string // A text message
	// Output
	RawOutput   bool
	ShowVersion bool
	Version     string
	// Protocol
	Insecure bool
	// Verbosity
	Basic   bool
	Quiet   bool
	Verbose bool

	// The response of a MeasureLatency call. Is overwritten if the function is called again.
	Response any

	// The result of a MeasureLatency call. Is overwritten if the function is called again.
	Result *wsstat.Result
}

// measureText runs the text-message latency measurement flow.
func (c *Client) measureText(target *url.URL, header http.Header) error {
	msgs := buildRepeatedStrings(c.TextMessage, c.Burst)
	var err error
	c.Result, c.Response, err = wsstat.MeasureLatencyBurst(target, msgs, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return c.postProcessTextResponse()
}

// postProcessTextResponse keeps behavior identical to the original implementation:
// - pick the first element if response is []string and non-empty
// - if RawOutput is false and response is a string, attempt to JSON-decode into map[string]any
func (c *Client) postProcessTextResponse() error {
	if responseArray, ok := c.Response.([]string); ok && len(responseArray) > 0 {
		c.Response = responseArray[0]
	}
	if !c.RawOutput {
		// Automatically decode JSON messages
		decodedMessage := make(map[string]any)
		responseStr, ok := c.Response.(string)
		if ok {
			if err := json.Unmarshal([]byte(responseStr), &decodedMessage); err != nil {
				return fmt.Errorf("error unmarshalling JSON message: %v", err)
			}
			c.Response = decodedMessage
		}
	}
	return nil
}

// measureJSON runs the JSON-RPC latency measurement flow.
func (c *Client) measureJSON(target *url.URL, header http.Header) error {
	msg := struct {
		Method     string `json:"method"`
		ID         string `json:"id"`
		RPCVersion string `json:"jsonrpc"`
	}{
		Method:     c.JSONMethod,
		ID:         "1",
		RPCVersion: "2.0",
	}
	msgs := buildRepeatedAny(msg, c.Burst)
	var err error
	c.Result, c.Response, err = wsstat.MeasureLatencyJSONBurst(target, msgs, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return nil
}

// measurePing runs the ping latency measurement flow.
func (c *Client) measurePing(target *url.URL, header http.Header) error {
	var err error
	c.Result, err = wsstat.MeasureLatencyPingBurst(target, c.Burst, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return nil
}

// MeasureLatency measures the latency of the WebSocket connection, applying different methods
// based on the flags passed to the program.
func (c *Client) MeasureLatency(target *url.URL) error {
	header, err := parseHeaders(c.InputHeaders)
	if err != nil {
		return err
	}

	switch {
	case c.TextMessage != "":
		return c.measureText(target, header)
	case c.JSONMethod != "":
		return c.measureJSON(target, header)
	default:
		return c.measurePing(target, header)
	}
}

// PrintRequestDetails prints the results of the Client, with verbosity based on the flags
// passed to the program. If no results have been produced yet, the function errors.
func (c *Client) PrintRequestDetails() error {
	if c.Result == nil {
		return errors.New("no results have been produced")
	}
	fmt.Println()

	// Print basic output
	if c.Basic {
		fmt.Printf(printValueTemp, colorTeaGreen("URL"), c.Result.URL.Hostname())
		if len(c.Result.IPs) > 0 {
			fmt.Printf("%s:  %s\n", colorTeaGreen("IP"), c.Result.IPs[0])
		}
		return nil
	}

	// Print verbose output
	if c.Verbose {
		fmt.Println(colorWSOrange("Target"))
		fmt.Printf("  %s:  %s\n", colorTeaGreen("URL"), c.Result.URL.Hostname())
		// Loop in case there are multiple IPs with the target
		for _, ip := range c.Result.IPs {
			fmt.Printf(printIndentedValueTemp, colorTeaGreen("IP"), ip)
		}
		fmt.Printf("  %s: %d\n", colorTeaGreen("Messages sent:"), c.Result.MessageCount)
		fmt.Println()
		if c.Result.TLSState != nil {
			fmt.Println(colorWSOrange("TLS"))
			fmt.Printf(printIndentedValueTemp,
				colorTeaGreen("Version"), tls.VersionName(c.Result.TLSState.Version))
			fmt.Printf(printIndentedValueTemp,
				colorTeaGreen("Cipher Suite"), tls.CipherSuiteName(c.Result.TLSState.CipherSuite))

			// Print the certificate details
			for i, cert := range c.Result.TLSState.PeerCertificates {
				fmt.Printf("  %s: %d\n", colorTeaGreen("Certificate"), i+1)
				fmt.Printf("    Subject: %s\n", cert.Subject)
				fmt.Printf("    Issuer: %s\n", cert.Issuer)
				fmt.Printf("    Not Before: %s\n", cert.NotBefore)
				fmt.Printf("    Not After: %s\n", cert.NotAfter)
			}
			fmt.Println()
		}
		fmt.Println(colorWSOrange("Request headers"))
		for key, values := range c.Result.RequestHeaders {
			fmt.Printf(printIndentedValueTemp, colorTeaGreen(key), strings.Join(values, ", "))
		}
		fmt.Println(colorWSOrange("Response headers"))
		for key, values := range c.Result.ResponseHeaders {
			fmt.Printf(printIndentedValueTemp, colorTeaGreen(key), strings.Join(values, ", "))
		}
		return nil
	}

	// Print standard output
	fmt.Printf(printValueTemp, colorWSOrange("Target"), c.Result.URL.Hostname())
	for _, values := range c.Result.IPs {
		fmt.Printf(printValueTemp, colorWSOrange("IP"), values)
	}
	fmt.Printf("%s: %d\n", colorWSOrange("Messages sent:"), c.Result.MessageCount)
	for key, values := range c.Result.RequestHeaders {
		if key == "Sec-WebSocket-Version" {
			fmt.Printf(printValueTemp, colorWSOrange("WS version"), strings.Join(values, ", "))
		}
	}
	if c.Result.TLSState != nil {
		fmt.Printf(printValueTemp,
			colorWSOrange("TLS version"), tls.VersionName(c.Result.TLSState.Version))
	}

	return nil
}

// PrintTimingResults prints the WebSocket statistics to the terminal.
func (c *Client) PrintTimingResults(u *url.URL) error {
	if c.Result == nil {
		return errors.New("no results have been produced")
	}

	if c.Basic {
		printTimingResultsBasic(c.Result, c.Burst)
	} else {
		printTimingResultsTiered(c.Result, u)
	}

	return nil
}

// PrintResponse prints the response to the terminal if there is one, otherwise does nothing.
func (c *Client) PrintResponse() {
	if c.Response == nil {
		return
	}

	baseMessage := colorWSOrange("Response") + ": "

	if c.Quiet {
		baseMessage = ""
	} else {
		fmt.Println()
	}

	if c.RawOutput {
		// If raw output is requested, print the raw data before trying to assert any types
		fmt.Printf("%s%v\n", baseMessage, c.Response)
	} else if responseMap, ok := c.Response.(map[string]any); ok {
		// If JSON in request, print response as JSON
		if _, isJSON := responseMap["jsonrpc"]; isJSON || c.JSONMethod != "" {
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
		fmt.Printf("%s%v\n", baseMessage, responseBytes)
	}

	if !c.Quiet {
		fmt.Println()
	}
}

// Validate validates the Client is ready for measurement; it checks that the client settings are
// set to valid values.
func (c *Client) Validate() error {
	if c.Burst < 1 {
		return errors.New("burst must be greater than 0")
	}

	// Fix grammar if burst is greater than 1
	if c.Burst > 1 {
		wssPrintTemplate = strings.Replace(
			wssPrintTemplate, "Message RTT", "Mean Message RTT", 1)
		wsPrintTemplate = strings.Replace(
			wsPrintTemplate, "Message RTT", "Mean Message RTT", 1)
	}

	if c.TextMessage != "" && c.JSONMethod != "" {
		return errors.New("mutually exclusive messaging flags")
	}
	return nil
}

// buildRepeatedStrings returns a slice of length n where every element equals s.
func buildRepeatedStrings(s string, n int) []string {
	msgs := make([]string, n)
	for i := range msgs {
		msgs[i] = s
	}
	return msgs
}

// buildRepeatedAny returns a slice of length n where every element equals v.
func buildRepeatedAny(v any, n int) []any {
	msgs := make([]any, n)
	for i := range msgs {
		msgs[i] = v
	}
	return msgs
}

// colorWSOrange returns the text with a custom orange color.
// The color is from the WS logo, #ff6600 is its hex code.
func colorWSOrange(text string) string {
	return customColor(255, 102, 0, text) //revive:disable:add-constant should allow
}

// colorTeaGreen returns the text with a custom tea green color.
// The color has hex code #d3f9b5.
func colorTeaGreen(text string) string {
	return customColor(211, 249, 181, text) //revive:disable:add-constant should allow
}

// customColor returns the text with a custom RGB color.
func customColor(r, g, b int, text string) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", r, g, b, text)
}

// formatPadLeft formats the duration to a string with padding on the left.
func formatPadLeft(d time.Duration) string {
	return fmt.Sprintf("%7dms", int(d/time.Millisecond))
}

// formatPadRight formats the duration to a string with padding on the right.
func formatPadRight(d time.Duration) string {
	return fmt.Sprintf("%-8s", strconv.Itoa(int(d/time.Millisecond))+"ms")
}

// handleConnectionError prints the error message and exits the program.
func handleConnectionError(err error, address string) error {
	if strings.Contains(err.Error(), "tls: first record does not look like a TLS handshake") {
		return fmt.Errorf("error establishing secure WS connection to '%s': %v", address, err)
	}
	return fmt.Errorf("error establishing WS connection to '%s': %v", address, err)
}

// parseHeaders parses comma separated headers into an HTTP header.
func parseHeaders(headers string) (http.Header, error) {
	header := http.Header{}
	if headers != "" {
		headerParts := strings.SplitSeq(headers, ",")
		for part := range headerParts {
			parts := strings.SplitN(part, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid header format: %s", part)
			}
			header.Add(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return header, nil
}

// printTimingResultsBasic formats and prints only the most basic WebSocket statistics.
func printTimingResultsBasic(result *wsstat.Result, burst int) {
	fmt.Println()
	rttString := "Round-trip time"
	if burst > 1 {
		rttString = "Mean round-trip time"
	}
	msgCountString := "message"
	if result.MessageCount > 1 {
		msgCountString = "messages"
	}
	fmt.Printf(
		"%s: %s (%d %s)\n",
		rttString,
		colorWSOrange(strconv.FormatInt(result.MessageRTT.Milliseconds(), 10)+"ms"),
		result.MessageCount,
		msgCountString)
	fmt.Printf(
		printValueTemp,
		"Total time",
		colorWSOrange(strconv.FormatInt(result.TotalTime.Milliseconds(), 10)+"ms"))
	fmt.Println()
}

// printTimingResultsTiered formats and prints the WebSocket statistics to the terminal
// in a tiered fashion.
func printTimingResultsTiered(result *wsstat.Result, u *url.URL) {
	fmt.Println()
	switch u.Scheme {
	case "wss":
		fmt.Fprintf(os.Stdout, wssPrintTemplate,
			colorTeaGreen(formatPadLeft(result.DNSLookup)),
			colorTeaGreen(formatPadLeft(result.TCPConnection)),
			colorTeaGreen(formatPadLeft(result.TLSHandshake)),
			colorTeaGreen(formatPadLeft(result.WSHandshake)),
			colorTeaGreen(formatPadLeft(result.MessageRTT)),
			colorTeaGreen(formatPadRight(result.DNSLookupDone)),
			colorTeaGreen(formatPadRight(result.TCPConnected)),
			colorTeaGreen(formatPadRight(result.TLSHandshakeDone)),
			colorTeaGreen(formatPadRight(result.WSHandshakeDone)),
			// formatPadRight(result.FirstMessageResponse), // Skipping due to ConnectionClose skip
			colorWSOrange(formatPadRight(result.TotalTime)),
		)
	case "ws":
		fmt.Fprintf(os.Stdout, wsPrintTemplate,
			colorTeaGreen(formatPadLeft(result.DNSLookup)),
			colorTeaGreen(formatPadLeft(result.TCPConnection)),
			colorTeaGreen(formatPadLeft(result.WSHandshake)),
			colorTeaGreen(formatPadLeft(result.MessageRTT)),
			colorTeaGreen(formatPadRight(result.DNSLookupDone)),
			colorTeaGreen(formatPadRight(result.TCPConnected)),
			colorTeaGreen(formatPadRight(result.WSHandshakeDone)),
			// formatPadRight(result.FirstMessageResponse), // Skipping due to ConnectionClose skip
			colorWSOrange(formatPadRight(result.TotalTime)),
		)
	}
	fmt.Println()
}
