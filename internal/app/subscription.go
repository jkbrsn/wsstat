package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/jkbrsn/wsstat/v3"
)

// handleSubscriptionTick handles a subscription tick.
func (c *Client) handleSubscriptionTick(wsClient *wsstat.WSStat, target *url.URL) {
	result := wsClient.ExtractResult()
	if !c.quiet {
		c.printSubscriptionSummary(target, result)
	}
}

// openSubscription opens a subscription to the target WebSocket server.
func (c *Client) openSubscription(
	ctx context.Context,
	target *url.URL,
) (*wsstat.WSStat, *wsstat.Subscription, error) {
	header, err := parseHeaders(c.headers)
	if err != nil {
		return nil, nil, err
	}

	wsClient := wsstat.New(c.wsstatOptions()...)
	if err := wsClient.DialContext(ctx, target, header); err != nil {
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
	if c.buffer > 0 {
		opts.Buffer = c.buffer
	}

	subscription, err := wsClient.Subscribe(ctx, opts)
	if err != nil {
		wsClient.Close()
		return nil, nil, err
	}

	return wsClient, subscription, nil
}

// runSubscriptionLoop runs the subscription loop.
func (c *Client) runSubscriptionLoop(
	ctx context.Context,
	wsClient *wsstat.WSStat,
	subscription *wsstat.Subscription,
	target *url.URL,
) error {
	var ticker *time.Ticker
	if c.summaryInterval > 0 {
		ticker = time.NewTicker(c.summaryInterval)
		defer ticker.Stop()
	}

	sender := newPendingSender(c.pendingSends(), c.sendDelay)
	defer sender.stop()

	messageIndex := 0
	limit := c.count

	// finish drains whatever is still buffered behind the subscription's close, then reports
	// the summary. The core closes the update buffer before closing Done, so Done and Updates
	// go ready together and this loop can observe Done with frames still queued; without the
	// drain those frames are lost from both stdout and the --file capture.
	finish := func() error {
		var err error
		messageIndex, err = c.drainSubscription(subscription, messageIndex, limit)
		c.handleSubscriptionTick(wsClient, target)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			subscription.Cancel()
			<-subscription.Done()
			return finish()
		case <-subscription.Done():
			return finish()
		case msg, ok := <-subscription.Updates():
			if !ok {
				// Updates is closed but Done has not been observed yet: the subscription is
				// mid-finalize. Wait it out instead of spinning on the closed channel.
				<-subscription.Done()
				return finish()
			}
			if msg.Err != nil {
				fmt.Fprintf(os.Stderr, "subscription error: %v\n", msg.Err)
				continue
			}
			messageIndex++
			if err := c.emitMessage(messageIndex, msg); err != nil {
				return err
			}
			if limit > 0 && messageIndex >= limit {
				subscription.Cancel()
				<-subscription.Done()
				return finish()
			}
		case <-sender.c():
			sender.send(wsClient)
		case <-tickerC(ticker):
			c.handleSubscriptionTick(wsClient, target)
			if c.output == OutputText {
				fmt.Println()
			}
		}
	}
}

// drainSubscription emits the frames still sitting in the update buffer after the subscription
// finalized, and returns the running message index. Surplus frames past --count are consumed
// but not emitted. The range terminates because the core closes the buffer before closing Done,
// so this must only be called once Done has been observed.
func (c *Client) drainSubscription(
	subscription *wsstat.Subscription, from, limit int,
) (int, error) {
	index := from
	for msg := range subscription.Updates() {
		if msg.Err != nil {
			fmt.Fprintf(os.Stderr, "subscription error: %v\n", msg.Err)
			continue
		}
		if limit > 0 && index >= limit {
			continue
		}
		index++
		if err := c.emitMessage(index, msg); err != nil {
			return index, err
		}
	}
	return index, nil
}

// pendingSends returns the text messages to send after the initial subscribe payload.
func (c *Client) pendingSends() []string {
	if len(c.textMessages) < 2 {
		return nil
	}
	return c.textMessages[1:]
}

// pendingSender staggers the post-subscribe text messages. It is driven from the
// subscription loop goroutine, so ordering against the transport's write pump is
// guaranteed without extra coordination. With no pending messages its channel is
// nil and the loop's select never fires.
type pendingSender struct {
	msgs  []string
	delay time.Duration
	timer *time.Timer
}

// newPendingSender creates a sender for msgs, arming the first send after delay.
func newPendingSender(msgs []string, delay time.Duration) *pendingSender {
	s := &pendingSender{msgs: msgs, delay: delay}
	if len(msgs) > 0 {
		s.timer = time.NewTimer(delay)
	}
	return s
}

// c returns the channel that fires when the next message is due.
func (s *pendingSender) c() <-chan time.Time {
	return timerC(s.timer)
}

// send writes the next message and re-arms the timer while messages remain.
func (s *pendingSender) send(ws *wsstat.WSStat) {
	ws.WriteMessage(wsstat.TextMessage, []byte(s.msgs[0]))
	s.msgs = s.msgs[1:]
	if len(s.msgs) > 0 {
		s.timer.Reset(s.delay)
	}
}

// stop releases the timer.
func (s *pendingSender) stop() {
	if s.timer != nil {
		s.timer.Stop()
	}
}

// emitMessage records a received update to the response sink (no-op when --file is unset)
// and prints it to stdout. Recording is independent of the stdout print, so it fires
// regardless of -q/verbosity.
func (c *Client) emitMessage(index int, msg wsstat.SubscriptionMessage) error {
	if err := c.writeResponseLine(msg.Data); err != nil {
		return err
	}
	return c.printSubscriptionMessage(index, msg)
}

// subscriptionPayload returns the initial payload to be sent to the server.
func (c *Client) subscriptionPayload() (int, []byte, error) {
	if len(c.textMessages) > 0 {
		return wsstat.TextMessage, []byte(c.textMessages[0]), nil
	}
	if c.rpcMethod != "" {
		req := buildRPCRequest(c.rpcMethod, c.rpcVersion)
		payload, err := json.Marshal(req)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to marshal subscription payload: %w", err)
		}
		return wsstat.TextMessage, payload, nil
	}
	return wsstat.TextMessage, nil, nil
}

// subscriptionMessageJSON builds a subscription message envelope.
// JSON output is schema-stable: every envelope carries the same fields
// regardless of verbosity (quiet is rejected under -o json at the CLI layer).
func (*Client) subscriptionMessageJSON(
	index int,
	msg wsstat.SubscriptionMessage,
) subscriptionMessageJSON {
	payload, ok := parseJSONPayload(msg.Data)
	if !ok {
		payload = string(msg.Data)
	}
	return subscriptionMessageJSON{
		Schema:      JSONSchemaVersion,
		Type:        "subscription_message",
		Index:       index,
		Timestamp:   msg.Received.Format(time.RFC3339Nano),
		Size:        msg.Size,
		MessageType: messageTypeLabel(msg.MessageType),
		Payload:     payload,
	}
}

// subscriptionSummaryJSON builds a subscription summary.
func (*Client) subscriptionSummaryJSON(
	target *url.URL,
	result *wsstat.Result,
) subscriptionSummaryJSON {
	summary := subscriptionSummaryJSON{
		Schema:        JSONSchemaVersion,
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
		slices.Sort(ids)
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

// StreamSubscription establishes a WebSocket connection and streams events from the server.
// Events are printed as they arrive. The stream continues until:
//   - The configured message count is reached (if count > 0), or
//   - The context is canceled (if count == 0 for unlimited), or
//   - The server closes the connection
//
// If summaryInterval is configured, periodic subscription summaries are printed.
// Use context cancellation for graceful shutdown.
func (c *Client) StreamSubscription(ctx context.Context, target *url.URL) error {
	wsClient, subscription, err := c.openSubscription(ctx, target)
	if err != nil {
		return err
	}
	defer wsClient.Close()

	if !c.quiet {
		result := wsClient.ExtractResult()
		if err := c.PrintRequestDetails(&MeasurementResult{Result: result}); err != nil {
			subscription.Cancel()
			<-subscription.Done()
			return err
		}
		if c.output == OutputText {
			fmt.Println()
			fmt.Println(c.colorizeOrange("Streaming subscription events"))
		}
	}

	return c.runSubscriptionLoop(ctx, wsClient, subscription, target)
}

// StreamSubscriptionOnce establishes a WebSocket connection, receives exactly one event,
// prints it, and exits. This is equivalent to StreamSubscription with count=1, but optimized
// for the single-message case.
//
// Validation ensures count equals 1 when using this mode.
func (c *Client) StreamSubscriptionOnce(ctx context.Context, target *url.URL) error {
	originalCount := c.count
	c.count = 1
	defer func() { c.count = originalCount }()

	wsClient, subscription, err := c.openSubscription(ctx, target)
	if err != nil {
		return err
	}
	defer wsClient.Close()

	if !c.quiet {
		result := wsClient.ExtractResult()
		if err := c.PrintRequestDetails(&MeasurementResult{Result: result}); err != nil {
			subscription.Cancel()
			<-subscription.Done()
			return err
		}
	}

	if c.output == OutputText {
		fmt.Println()
	}
	return c.runSubscriptionLoop(ctx, wsClient, subscription, target)
}
