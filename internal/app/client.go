// Package app measures utilizes the wsstat package to measure to construct a
// client that measures the latency of a WebSocket connection.
package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkbrsn/wsstat"
)

var (
	// Templates for printing singular results
	printValueTemp         = "%s: %s\n"
	printIndentedValueTemp = "  %s: %s\n"

	// Templates for printing tiered results
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

// Client measures the latency of a WebSocket connection, applying different methods
// based on the settings passed to the struct.
type Client struct {
	// Input
	Count       int      // Number of interactions to perform; 0 means unlimited in subscription mode
	Headers     []string // HTTP headers for connection establishment ("Key: Value")
	RPCMethod   string   // JSON-RPC method (no params)
	TextMessage string   // Text message

	// Output
	Format string // Output formatting mode: "auto" or "raw"

	// Verbosity
	Quiet          bool
	VerbosityLevel int

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

// measureText runs the text-message latency measurement flow.
func (c *Client) measureText(target *url.URL, header http.Header) error {
	msgs := buildRepeatedStrings(c.TextMessage, c.Count)
	var err error
	c.Result, c.Response, err = wsstat.MeasureLatencyBurst(target, msgs, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return c.postProcessTextResponse()
}

// postProcessTextResponse keeps behavior identical to the original implementation while allowing
// plain-text echoes:
// - pick the first element if response is []string and non-empty
// - if Format is not raw and response is a JSON-RPC payload, decode into map[string]any
func (c *Client) postProcessTextResponse() error {
	if responseArray, ok := c.Response.([]string); ok && len(responseArray) > 0 {
		c.Response = responseArray[0]
	}
	if c.Format != "raw" {
		responseStr, ok := c.Response.(string)
		if !ok {
			return nil
		}

		trimmed := strings.TrimSpace(responseStr)
		if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
			return nil
		}

		decodedMessage := make(map[string]any)
		if err := json.Unmarshal([]byte(responseStr), &decodedMessage); err != nil {
			return nil
		}

		if _, isJSONRPC := decodedMessage["jsonrpc"]; !isJSONRPC {
			return nil
		}
		c.Response = decodedMessage
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
		Method:     c.RPCMethod,
		ID:         "1",
		RPCVersion: "2.0",
	}
	msgs := buildRepeatedAny(msg, c.Count)
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
	c.Result, err = wsstat.MeasureLatencyPingBurst(target, c.Count, header)
	if err != nil {
		return handleConnectionError(err, target.String())
	}
	return nil
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

// StreamSubscription establishes a WebSocket connection and streams subscription events
// until the provided context is canceled or the server closes the connection.
func (c *Client) StreamSubscription(ctx context.Context, target *url.URL) error {
	wsClient, subscription, err := c.openSubscription(ctx, target)
	if err != nil {
		return err
	}
	defer wsClient.Close()

	if !c.Quiet {
		if err := c.PrintRequestDetails(); err != nil {
			subscription.Cancel()
			<-subscription.Done()
			return err
		}
		fmt.Println()
		fmt.Println(colorWSOrange("Streaming subscription events"))
	}

	return c.runSubscriptionLoop(ctx, wsClient, subscription, target)
}

// StreamSubscriptionOnce establishes a subscription and exits after the first message.
func (c *Client) StreamSubscriptionOnce(ctx context.Context, target *url.URL) error {
	originalCount := c.Count
	c.Count = 1
	defer func() { c.Count = originalCount }()

	wsClient, subscription, err := c.openSubscription(ctx, target)
	if err != nil {
		return err
	}
	defer wsClient.Close()

	if !c.Quiet {
		if err := c.PrintRequestDetails(); err != nil {
			subscription.Cancel()
			<-subscription.Done()
			return err
		}
	}

	fmt.Println()
	return c.runSubscriptionLoop(ctx, wsClient, subscription, target)
}

func (c *Client) openSubscription(
	ctx context.Context,
	target *url.URL,
) (*wsstat.WSStat, *wsstat.Subscription, error) {
	header, err := parseHeaders(c.Headers)
	if err != nil {
		return nil, nil, err
	}

	wsClient := wsstat.New()
	if err := wsClient.Dial(target, header); err != nil {
		wsClient.Close()
		return nil, nil, handleConnectionError(err, target.String())
	}

	messageType, payload, err := c.subscriptionPayload()
	if err != nil {
		wsClient.Close()
		return nil, nil, err
	}

	opts := wsstat.SubscriptionOptions{
		MessageType: messageType,
		Payload:     payload,
	}
	if c.Buffer > 0 {
		opts.Buffer = c.Buffer
	}

	subscription, err := wsClient.Subscribe(ctx, opts)
	if err != nil {
		wsClient.Close()
		return nil, nil, err
	}

	c.Result = wsClient.ExtractResult()
	return wsClient, subscription, nil
}

// subscriptionPayload returns the payload to be sent to the server.
func (c *Client) subscriptionPayload() (int, []byte, error) {
	if c.TextMessage != "" {
		return websocket.TextMessage, []byte(c.TextMessage), nil
	}
	if c.RPCMethod != "" {
		msg := struct {
			Method     string `json:"method"`
			ID         string `json:"id"`
			RPCVersion string `json:"jsonrpc"`
		}{
			Method:     c.RPCMethod,
			ID:         "1",
			RPCVersion: "2.0",
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to marshal subscription payload: %w", err)
		}
		return websocket.TextMessage, payload, nil
	}
	return websocket.TextMessage, nil, nil
}

// runSubscriptionLoop runs the subscription loop.
func (c *Client) runSubscriptionLoop(
	ctx context.Context,
	wsClient *wsstat.WSStat,
	subscription *wsstat.Subscription,
	target *url.URL,
) error {
	var ticker *time.Ticker
	if c.SummaryInterval > 0 {
		ticker = time.NewTicker(c.SummaryInterval)
		defer ticker.Stop()
	}

	messageIndex := 0
	limit := c.Count

	for {
		select {
		case <-ctx.Done():
			subscription.Cancel()
			<-subscription.Done()
			c.handleSubscriptionTick(wsClient, target)
			return nil
		case <-subscription.Done():
			c.handleSubscriptionTick(wsClient, target)
			return nil
		case msg, ok := <-subscription.Updates():
			if !ok {
				continue
			}
			if msg.Err != nil {
				fmt.Fprintf(os.Stderr, "subscription error: %v\n", msg.Err)
				continue
			}
			messageIndex++
			c.Result = wsClient.ExtractResult()
			if err := c.printSubscriptionMessage(messageIndex, msg); err != nil {
				return err
			}
			if limit > 0 && messageIndex >= limit {
				subscription.Cancel()
				<-subscription.Done()
				c.handleSubscriptionTick(wsClient, target)
				return nil
			}
		case <-tickerC(ticker):
			c.handleSubscriptionTick(wsClient, target)
			fmt.Println()
		}
	}
}

// handleSubscriptionTick handles a subscription tick.
func (c *Client) handleSubscriptionTick(wsClient *wsstat.WSStat, target *url.URL) {
	if c.Result == nil {
		return
	}
	c.Result = wsClient.ExtractResult()
	if !c.Quiet {
		c.printSubscriptionSummary(target)
	}
}

// tickerC returns a channel that emits ticks from the provided ticker.
func tickerC(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// printSubscriptionMessage prints a subscription message.
func (c *Client) printSubscriptionMessage(index int, msg wsstat.SubscriptionMessage) error {
	payload := string(msg.Data)
	if c.Quiet || c.Format == "raw" {
		fmt.Println(payload)
		return nil
	}

	timestamp := msg.Received.Format(time.RFC3339Nano)
	if c.VerbosityLevel == 0 {
		fmt.Printf("[%04d @ %s] Message received\n", index, timestamp)
		return nil
	}

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

	fmt.Println()
	fmt.Println(colorWSOrange("Subscription summary"))
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

// formatJSONIfPossible formats the JSON data if possible.
func formatJSONIfPossible(data []byte) string {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ""
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return ""
	}
	var anyJSON any
	if err := json.Unmarshal([]byte(trimmed), &anyJSON); err != nil {
		return ""
	}
	pretty, err := json.MarshalIndent(anyJSON, "", "  ")
	if err != nil {
		return ""
	}
	return string(pretty)
}

// formatDuration formats the duration.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", d/time.Millisecond)
}

// PrintRequestDetails prints the results of the Client, with verbosity based on the flags
// passed to the program. If no results have been produced yet, the function errors.
func (c *Client) PrintRequestDetails() error {
	if c.Result == nil {
		return errors.New("no results have been produced")
	}

	// If quiet, -q, don't print anything.
	if c.Quiet {
		return nil
	}

	fmt.Println()

	// Verbosity level 0
	if c.VerbosityLevel == 0 {
		fmt.Printf(printValueTemp, colorTeaGreen("URL"), c.Result.URL.Hostname())
		if len(c.Result.IPs) > 0 {
			fmt.Printf("%s:  %s\n", colorTeaGreen("IP"), c.Result.IPs[0])
		}
		return nil
	}

	// Verbosity level 1, -v
	if c.VerbosityLevel == 1 {
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

	// Verbosity level 2, -vv
	fmt.Println(colorWSOrange("Target"))
	fmt.Printf("  %s:  %s\n", colorTeaGreen("URL"), c.Result.URL.Hostname())
	for _, ip := range c.Result.IPs {
		fmt.Printf(printIndentedValueTemp, colorTeaGreen("IP"), ip)
	}
	fmt.Printf("  %s: %d\n", colorTeaGreen("Messages sent"), c.Result.MessageCount)
	fmt.Println()
	if c.Result.TLSState != nil {
		fmt.Println(colorWSOrange("TLS"))
		fmt.Printf(printIndentedValueTemp,
			colorTeaGreen("Version"), tls.VersionName(c.Result.TLSState.Version))
		fmt.Printf(printIndentedValueTemp,
			colorTeaGreen("Cipher Suite"), tls.CipherSuiteName(c.Result.TLSState.CipherSuite))

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

// PrintTimingResults prints the WebSocket statistics to the terminal.
func (c *Client) PrintTimingResults(u *url.URL) error {
	if c.Result == nil {
		return errors.New("no results have been produced")
	}

	if c.Quiet {
		return nil
	}

	if c.VerbosityLevel == 0 {
		printTimingResultsBasic(c.Result, c.Count)
		return nil
	}

	rttLabel := "Message RTT"
	if c.Count > 1 || c.Result.MessageCount > 1 {
		rttLabel = "Mean Message RTT"
	}
	printTimingResultsTiered(c.Result, u, rttLabel)

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
	}

	if c.Format == "raw" {
		// If raw output is requested, print the raw data before trying to assert any types
		fmt.Printf("%s%v\n", baseMessage, c.Response)
	} else if responseMap, ok := c.Response.(map[string]any); ok {
		// If JSON in request, print response as JSON
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
		c.Format = "auto"
	}
	if c.Format != "auto" && c.Format != "raw" {
		return errors.New("format must be \"auto\" or \"raw\"")
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

// parseHeaders parses repeated header arguments into an HTTP header.
func parseHeaders(pairs []string) (http.Header, error) {
	header := http.Header{}
	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid header format: %s", pair)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("invalid header format: %s", pair)
		}
		header.Add(key, value)
	}
	return header, nil
}

// printTimingResultsBasic formats and prints only the most basic WebSocket statistics.
func printTimingResultsBasic(result *wsstat.Result, count int) {
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
func printTimingResultsTiered(result *wsstat.Result, u *url.URL, label string) {
	switch u.Scheme {
	case "wss":
		fmt.Fprintf(os.Stdout, wssPrintTemplate,
			label,
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
			label,
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
	default:
		fmt.Fprintf(os.Stdout, wsPrintTemplate,
			label,
			colorTeaGreen(formatPadLeft(result.DNSLookup)),
			colorTeaGreen(formatPadLeft(result.TCPConnection)),
			colorTeaGreen(formatPadLeft(result.WSHandshake)),
			colorTeaGreen(formatPadLeft(result.MessageRTT)),
			colorTeaGreen(formatPadRight(result.DNSLookupDone)),
			colorTeaGreen(formatPadRight(result.TCPConnected)),
			colorTeaGreen(formatPadRight(result.WSHandshakeDone)),
			colorWSOrange(formatPadRight(result.TotalTime)),
		)
	}
	fmt.Println()
}
