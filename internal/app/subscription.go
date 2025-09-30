package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkbrsn/wsstat"
)

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
