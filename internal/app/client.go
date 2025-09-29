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
	"github.com/mattn/go-isatty"
)

const (
	formatAuto = "auto"
	formatRaw  = "raw"
	formatJSON = "json"
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
	return colorWSOrange(text)
}

// colorizeGreen returns the text with green color applied if color output is enabled.
func (c *Client) colorizeGreen(text string) string {
	if !c.colorEnabled() {
		return text
	}
	return colorTeaGreen(text)
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

// openSubscription opens a subscription to the target WebSocket server.
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

// postProcessTextResponse keeps behavior identical to the original implementation while allowing
// plain-text echoes:
// - pick the first element if response is []string and non-empty
// - if Format is not raw and response is a JSON-RPC payload, decode into map[string]any
func (c *Client) postProcessTextResponse() error {
	if responseArray, ok := c.Response.([]string); ok && len(responseArray) > 0 {
		c.Response = responseArray[0]
	}
	if c.Format != formatRaw {
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
			// formatPadRight(result.FirstMessageResponse), // Skipping due to ConnectionClose skip
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
			// formatPadRight(result.FirstMessageResponse), // Skipping due to ConnectionClose skip
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
			if c.Format != formatJSON {
				fmt.Println()
			}
		}
	}
}

// subscriptionMessageJSON builds a subscription message.
func (c *Client) subscriptionMessageJSON(
	index int,
	msg wsstat.SubscriptionMessage,
) subscriptionMessageJSON {
	payload, ok := parseJSONPayload(msg.Data)
	if !ok {
		payload = string(msg.Data)
	}
	output := subscriptionMessageJSON{
		Type:    "subscription_message",
		Payload: payload,
	}
	if !c.Quiet {
		output.Index = index
		output.Timestamp = msg.Received.Format(time.RFC3339Nano)
		output.Size = msg.Size
		output.MessageType = messageTypeLabel(msg.MessageType)
	}
	return output
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

// subscriptionSummaryJSON builds a subscription summary.
func (c *Client) subscriptionSummaryJSON(target *url.URL) subscriptionSummaryJSON {
	result := c.Result
	summary := subscriptionSummaryJSON{
		Type:          "subscription_summary",
		Target:        buildTimingTarget(result, target),
		FirstEventMs:  msPtr(result.SubscriptionFirstEvent),
		LastEventMs:   msPtr(result.SubscriptionLastEvent),
		TotalMessages: result.MessageCount,
	}
	if len(result.Subscriptions) > 0 {
		ids := make([]string, 0, len(result.Subscriptions))
		for id := range result.Subscriptions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		entries := make([]subscriptionEntryJSON, 0, len(ids))
		for _, id := range ids {
			stats := result.Subscriptions[id]
			entry := subscriptionEntryJSON{
				ID:                 id,
				Messages:           stats.MessageCount,
				Bytes:              stats.ByteCount,
				FirstEventMs:       msPtr(stats.FirstEvent),
				LastEventMs:        msPtr(stats.LastEvent),
				MeanInterArrivalMs: msPtr(stats.MeanInterArrival),
			}
			if stats.Error != nil {
				entry.Error = stats.Error.Error()
			}
			entries = append(entries, entry)
		}
		summary.Subscriptions = entries
	}
	return summary
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
		if c.Format != formatJSON {
			fmt.Println()
			fmt.Println(c.colorizeOrange("Streaming subscription events"))
		}
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

	if c.Format != formatJSON {
		fmt.Println()
	}
	return c.runSubscriptionLoop(ctx, wsClient, subscription, target)
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

// formatDuration formats the duration.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", d/time.Millisecond)
}

// handleConnectionError prints the error message and exits the program.
func handleConnectionError(err error, address string) error {
	if strings.Contains(err.Error(), "tls: first record does not look like a TLS handshake") {
		return fmt.Errorf("error establishing secure WS connection to '%s': %v", address, err)
	}
	return fmt.Errorf("error establishing WS connection to '%s': %v", address, err)
}

func msPtr(d time.Duration) *int64 {
	if d <= 0 {
		return nil
	}
	ms := d.Milliseconds()
	return &ms
}

func messageTypeLabel(messageType int) string {
	switch messageType {
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return strconv.Itoa(messageType)
	}
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

// tickerC returns a channel that emits ticks from the provided ticker.
func tickerC(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
