// Package wsstat measures the latency of WebSocket connections.
// It wraps the gorilla/websocket package and includes latency measurements in the Result struct.
package wsstat

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

const (
	// defaultTimeout is the default read/dial timeout for WSStat instances.
	defaultTimeout = 5 * time.Second
	// defaultChanBufferSize is the default size of read/write/pong channels.
	defaultChanBufferSize = 8
	// defaultSubscriptionBufferSize is the default queue length for subscription deliveries.
	defaultSubscriptionBufferSize = 32
	subscriptionIDPrefix          = "subscription-"
)

// CertificateDetails holds details regarding a certificate.
type CertificateDetails struct {
	CommonName         string
	Issuer             string
	NotBefore          time.Time
	NotAfter           time.Time
	PublicKeyAlgorithm x509.PublicKeyAlgorithm
	SignatureAlgorithm x509.SignatureAlgorithm
	DNSNames           []string
	IPAddresses        []net.IP
	URIs               []*url.URL
}

// Result holds durations of each phase of a WebSocket connection, cumulative durations over
// the connection timeline, and other relevant connection details.
type Result struct {
	IPs             []string             // IP addresses of the WebSocket connection
	URL             *url.URL             // URL of the WebSocket connection
	RequestHeaders  http.Header          // Headers of the initial request
	ResponseHeaders http.Header          // Headers of the response
	TLSState        *tls.ConnectionState // State of the TLS connection
	MessageCount    int                  // Number of messages sent and received

	// Subscription statistics captured when long-lived streams are active.
	Subscriptions          map[string]SubscriptionStats // Metrics by subscription ID
	SubscriptionFirstEvent time.Duration                // Time until first subscription event
	SubscriptionLastEvent  time.Duration                // Time until last subscription event

	// Duration of each phase of the connection
	DNSLookup     time.Duration // Time to resolve DNS
	TCPConnection time.Duration // TCP connection establishment time
	TLSHandshake  time.Duration // Time to perform TLS handshake
	WSHandshake   time.Duration // Time to perform WebSocket handshake
	MessageRTT    time.Duration // Time to send message and receive response

	// Cumulative durations over the connection timeline
	DNSLookupDone        time.Duration // Time to resolve DNS (might be redundant with DNSLookup)
	TCPConnected         time.Duration // Time until the TCP connection is established
	TLSHandshakeDone     time.Duration // Time until the TLS handshake is completed
	WSHandshakeDone      time.Duration // Time until the WS handshake is completed
	FirstMessageResponse time.Duration // Time until the first message is received
	TotalTime            time.Duration // Total time from opening to closing the connection
}

// Subscription captures a long-lived stream registered through Subscribe.
// Counters are updated atomically by the WSStat instance.
type Subscription struct {
	ID string

	cancel   context.CancelFunc
	done     <-chan struct{}
	messages *uint64
	bytes    *uint64
	updates  <-chan SubscriptionMessage
}

// SubscriptionOptions configures how WSStat establishes and demultiplexes a subscription.
type SubscriptionOptions struct {
	// ID can be provided to preassign a human-readable identifier. If empty, WSStat
	// allocates an incremental identifier.
	ID string

	// MessageType and Payload describe the initial frame sent to initiate the subscription.
	MessageType int
	Payload     []byte

	// Decoder transforms inbound frames into a convenient representation before dispatching
	// to subscription consumers. If nil, frames are passed through as raw bytes.
	Decoder SubscriptionDecoder

	// Matcher determines whether a given frame belongs to this subscription. When nil,
	// the dispatcher falls back to internal matching heuristics (such as explicit IDs).
	Matcher SubscriptionMatcher

	// Buffer controls the per-subscription delivery queue length. Zero implies the default.
	Buffer int
}

// SubscriptionStats snapshots per-subscription metrics for reporting through Result.
type SubscriptionStats struct {
	FirstEvent       time.Duration
	LastEvent        time.Duration
	MessageCount     uint64
	ByteCount        uint64
	MeanInterArrival time.Duration
	Error            error
}

// SubscriptionMessage represents a single frame delivered to a subscription consumer.
type SubscriptionMessage struct {
	MessageType int
	Data        []byte
	Decoded     any
	Received    time.Time
	Err         error
	Size        int
}

// Cancel stops the subscription and prevents further deliveries.
func (s *Subscription) Cancel() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
}

// Unsubscribe is an alias for Cancel and preserves semantic clarity for callers.
func (s *Subscription) Unsubscribe() {
	s.Cancel()
}

// Done returns a channel that closes once the subscription is fully torn down.
func (s *Subscription) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Updates exposes the buffered stream of subscription messages.
func (s *Subscription) Updates() <-chan SubscriptionMessage {
	if s == nil {
		return nil
	}
	return s.updates
}

// MessageCount reports the total number of messages delivered to the subscription.
func (s *Subscription) MessageCount() uint64 {
	if s == nil || s.messages == nil {
		return 0
	}
	return atomic.LoadUint64(s.messages)
}

// ByteCount reports the aggregate payload size delivered to the subscription.
func (s *Subscription) ByteCount() uint64 {
	if s == nil || s.bytes == nil {
		return 0
	}
	return atomic.LoadUint64(s.bytes)
}

// SubscriptionDecoder converts an incoming frame into an arbitrary representation.
type SubscriptionDecoder func(messageType int, data []byte) (any, error)

// SubscriptionMatcher returns true when the provided frame should be delivered to the
// subscription. The decoded value is only non-nil if a Decoder is configured.
type SubscriptionMatcher func(messageType int, data []byte, decoded any) bool

// subscriptionState tracks the state of a subscription.
type subscriptionState struct {
	id string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	closed bool

	matcher SubscriptionMatcher
	decoder SubscriptionDecoder
	options SubscriptionOptions

	buffer chan SubscriptionMessage
	stats  subscriptionMetrics

	messages *uint64
	bytes    *uint64
}

// subscriptionMetrics tracks the metrics of a subscription.
type subscriptionMetrics struct {
	firstEvent        time.Time
	lastEvent         time.Time
	messageCount      uint64
	byteCount         uint64
	totalInterArrival time.Duration
	lastArrival       time.Time
	finalErr          error
}

// activeSubscriptions returns a snapshot of the active subscriptions.
func (ws *WSStat) activeSubscriptions() []*subscriptionState {
	ws.subscriptionMu.RLock()
	defer ws.subscriptionMu.RUnlock()

	if len(ws.subscriptions) == 0 {
		return nil
	}

	states := make([]*subscriptionState, 0, len(ws.subscriptions))
	for _, state := range ws.subscriptions {
		states = append(states, state)
	}
	return states
}

// trackSubscriptionEvent tracks the first and last events of a subscription.
func (ws *WSStat) trackSubscriptionEvent(ts time.Time) {
	if ts.IsZero() {
		return
	}
	ws.subscriptionMu.Lock()
	defer ws.subscriptionMu.Unlock()
	if ws.subscriptionFirstEvent.IsZero() || ts.Before(ws.subscriptionFirstEvent) {
		ws.subscriptionFirstEvent = ts
	}
	if ts.After(ws.subscriptionLastEvent) {
		ws.subscriptionLastEvent = ts
	}
}

// deliverSubscriptionMessage delivers a message to a subscription.
func (ws *WSStat) deliverSubscriptionMessage(
	state *subscriptionState,
	message SubscriptionMessage,
) {
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	now := message.Received
	if message.Err == nil {
		if state.stats.firstEvent.IsZero() {
			state.stats.firstEvent = now
		}
		if !state.stats.lastArrival.IsZero() {
			state.stats.totalInterArrival += now.Sub(state.stats.lastArrival)
		}
		state.stats.lastArrival = now
		state.stats.lastEvent = now
		state.stats.messageCount++
		state.stats.byteCount += uint64(message.Size)
	} else {
		if state.stats.firstEvent.IsZero() {
			state.stats.firstEvent = now
		}
		state.stats.lastEvent = now
	}
	state.mu.Unlock()

	if state.messages != nil {
		atomic.AddUint64(state.messages, 1)
	}
	if state.bytes != nil {
		atomic.AddUint64(state.bytes, uint64(message.Size))
	}

	ws.trackSubscriptionEvent(now)

	select {
	case state.buffer <- message:
	default:
		ws.log.Warn().Str("subscription", state.id).
			Msg("subscription buffer full; dropping message")
	}
}

// finalizeSubscription finalizes a subscription upon its completion or cancellation.
func (ws *WSStat) finalizeSubscription(state *subscriptionState, finalErr error) {
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	state.closed = true
	if finalErr != nil {
		state.stats.finalErr = finalErr
	}
	metricsCopy := state.stats
	var msgCount, byteCount uint64
	if state.messages != nil {
		msgCount = atomic.LoadUint64(state.messages)
	}
	if state.bytes != nil {
		byteCount = atomic.LoadUint64(state.bytes)
	}
	buffer := state.buffer
	done := state.done
	state.mu.Unlock()

	if buffer != nil {
		close(buffer)
	}
	if done != nil {
		close(done)
	}
	if state.cancel != nil {
		state.cancel()
	}
	subStats := SubscriptionStats{
		FirstEvent:       ws.durationSinceDial(metricsCopy.firstEvent),
		LastEvent:        ws.durationSinceDial(metricsCopy.lastEvent),
		MessageCount:     msgCount,
		ByteCount:        byteCount,
		MeanInterArrival: 0,
		Error:            metricsCopy.finalErr,
	}
	if metricsCopy.messageCount > 1 {
		subStats.MeanInterArrival =
			metricsCopy.totalInterArrival / time.Duration(metricsCopy.messageCount-1)
	}
	ws.removeSubscription(state.id, &subStats)
}

// dispatchIncoming dispatches an incoming frame to the appropriate subscription.
func (ws *WSStat) dispatchIncoming(read *wsRead) bool {
	states := ws.activeSubscriptions()
	if len(states) == 0 {
		return false
	}

	receivedAt := time.Now()

	if read.err != nil {
		for _, state := range states {
			envelope := SubscriptionMessage{
				MessageType: read.messageType,
				Received:    receivedAt,
				Err:         read.err,
			}
			ws.deliverSubscriptionMessage(state, envelope)
			ws.finalizeSubscription(state, read.err)
		}
		return true
	}

	deliverAll := len(states) == 1
	claimed := false
	for _, state := range states {
		if state == nil {
			continue
		}
		if err := state.ctx.Err(); err != nil {
			ws.finalizeSubscription(state, err)
			continue
		}

		decoded := any(nil)
		var decodeErr error
		if state.decoder != nil {
			decoded, decodeErr = state.decoder(read.messageType, read.data)
		}

		match := false
		if state.matcher != nil {
			match = state.matcher(read.messageType, read.data, decoded)
		} else if deliverAll {
			match = true
		}

		if !match && decodeErr == nil {
			continue
		}

		payload := append([]byte(nil), read.data...)
		envelope := SubscriptionMessage{
			MessageType: read.messageType,
			Data:        payload,
			Decoded:     decoded,
			Received:    receivedAt,
			Err:         decodeErr,
			Size:        len(payload),
		}
		ws.deliverSubscriptionMessage(state, envelope)
		claimed = true
	}

	return claimed
}

// watchSubscription watches a subscription for its completion or cancellation.
func (ws *WSStat) watchSubscription(state *subscriptionState) {
	if state == nil {
		return
	}
	<-state.ctx.Done()
	err := state.ctx.Err()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	ws.finalizeSubscription(state, err)
}

// newSubscriptionID generates a new subscription ID.
func (ws *WSStat) newSubscriptionID() string {
	val := atomic.AddUint64(&ws.nextSubscriptionID, 1)
	return fmt.Sprintf("%s%d", subscriptionIDPrefix, val)
}

// registerSubscription registers a subscription.
func (ws *WSStat) registerSubscription(state *subscriptionState) *Subscription {
	ws.subscriptionMu.Lock()
	defer ws.subscriptionMu.Unlock()

	if ws.subscriptions == nil {
		ws.subscriptions = make(map[string]*subscriptionState)
	}
	ws.subscriptions[state.id] = state
	if ws.subscriptionArchive == nil {
		ws.subscriptionArchive = make(map[string]SubscriptionStats)
	}
	delete(ws.subscriptionArchive, state.id)

	if state.messages == nil {
		state.messages = new(uint64)
	}
	if state.bytes == nil {
		state.bytes = new(uint64)
	}

	sub := &Subscription{
		ID:       state.id,
		cancel:   state.cancel,
		done:     state.done,
		messages: state.messages,
		bytes:    state.bytes,
		updates:  state.buffer,
	}

	return sub
}

// removeSubscription removes a subscription by ID.
func (ws *WSStat) removeSubscription(id string, stats *SubscriptionStats) {
	ws.subscriptionMu.Lock()
	defer ws.subscriptionMu.Unlock()

	if ws.subscriptions != nil {
		delete(ws.subscriptions, id)
	}
	if stats != nil {
		if ws.subscriptionArchive == nil {
			ws.subscriptionArchive = make(map[string]SubscriptionStats)
		}
		ws.subscriptionArchive[id] = *stats
	}
}

// snapshotSubscriptionStats snapshots the subscription stats.
func (ws *WSStat) snapshotSubscriptionStats() map[string]SubscriptionStats {
	ws.subscriptionMu.RLock()
	defer ws.subscriptionMu.RUnlock()

	size := len(ws.subscriptions) + len(ws.subscriptionArchive)
	if size == 0 {
		return nil
	}

	snap := make(map[string]SubscriptionStats, size)
	for id, state := range ws.subscriptions {
		state.mu.Lock()
		s := state.stats
		var msgCount, byteCount uint64
		if state.messages != nil {
			msgCount = atomic.LoadUint64(state.messages)
		}
		if state.bytes != nil {
			byteCount = atomic.LoadUint64(state.bytes)
		}
		stats := SubscriptionStats{
			FirstEvent:       ws.durationSinceDial(s.firstEvent),
			LastEvent:        ws.durationSinceDial(s.lastEvent),
			MessageCount:     msgCount,
			ByteCount:        byteCount,
			MeanInterArrival: 0,
			Error:            s.finalErr,
		}
		if s.messageCount > 1 {
			stats.MeanInterArrival = s.totalInterArrival / time.Duration(s.messageCount-1)
		}
		state.mu.Unlock()
		snap[id] = stats
	}
	for id, stats := range ws.subscriptionArchive {
		snap[id] = stats
	}

	return snap
}

// WSStat wraps the gorilla/websocket package with latency measuring capabilities.
type WSStat struct {
	log zerolog.Logger

	conn    *websocket.Conn
	dialer  *websocket.Dialer
	timings *wsTimings
	result  *Result

	readChan  chan *wsRead
	pongChan  chan bool
	writeChan chan *wsWrite

	subscriptionMu            sync.RWMutex
	subscriptions             map[string]*subscriptionState
	subscriptionArchive       map[string]SubscriptionStats
	nextSubscriptionID        uint64
	defaultSubscriptionBuffer int
	subscriptionFirstEvent    time.Time
	subscriptionLastEvent     time.Time

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	wgPumps   sync.WaitGroup

	// instance configuration
	timeout time.Duration
	tlsConf *tls.Config
}

// wsRead holds the data read from the WebSocket connection.
type wsRead struct {
	data        []byte
	err         error
	messageType int
}

// wsWrite holds the data to be written to the WebSocket connection.
type wsWrite struct {
	data        []byte
	messageType int
}

// wsTimings holds the timings of each event in the WebSocket connection timeline.
type wsTimings struct {
	dialStart        time.Time   // Time when the dialing process started
	dnsLookupDone    time.Time   // Time when the DNS lookup is done
	tcpConnected     time.Time   // Time when the TCP connection is established
	tlsHandshakeDone time.Time   // Time when the TLS handshake is completed
	wsHandshakeDone  time.Time   // Time when the WS handshake is completed
	messageWrites    []time.Time // Times when messages are sent
	messageReads     []time.Time // Times when messages are received
	closeDone        time.Time   // Time when the connection was closed
}

// calculateResult calculates the durations of each phase of the WebSocket connection based
// on the current state of the WSStat timings.
// Note: if there haven't been as many message reads as writes, MessageRTT will be 0.
func (ws *WSStat) calculateResult() {
	// Calculate durations per phase
	ws.result.DNSLookup = ws.timings.dnsLookupDone.Sub(ws.timings.dialStart)
	ws.result.TCPConnection = ws.timings.tcpConnected.Sub(ws.timings.dnsLookupDone)
	if ws.timings.tlsHandshakeDone.IsZero() {
		ws.result.TLSHandshake = 0
		ws.result.WSHandshake = ws.timings.wsHandshakeDone.Sub(ws.timings.tcpConnected)
	} else {
		ws.result.TLSHandshake = ws.timings.tlsHandshakeDone.Sub(ws.timings.tcpConnected)
		ws.result.WSHandshake = ws.timings.wsHandshakeDone.Sub(ws.timings.tlsHandshakeDone)
	}

	// Note on MessageRTT calculations:
	// Since there is no guarantee that the time of a read corresponds to the time of the write
	// with the same index, we calculate only the mean round-trip time for all messages. As the
	// mean is calculated over all of the measurements, the result will be the same even if the
	// reads and writes are not in the same order as addition is commutative and associative.
	if len(ws.timings.messageReads) < 1 && len(ws.timings.messageWrites) < 1 ||
		len(ws.timings.messageReads) != len(ws.timings.messageWrites) {
		ws.result.MessageRTT = 0
		ws.result.MessageCount = 0
	} else {
		var meanMessageRTT time.Duration
		for i, readTime := range ws.timings.messageReads {
			writeTime := ws.timings.messageWrites[i]
			meanMessageRTT += readTime.Sub(writeTime)
		}
		ws.result.MessageRTT = meanMessageRTT / time.Duration(len(ws.timings.messageReads))
		ws.result.MessageCount = len(ws.timings.messageReads)
	}

	// Calculate cumulative durations
	ws.result.DNSLookupDone = ws.timings.dnsLookupDone.Sub(ws.timings.dialStart)
	ws.result.TCPConnected = ws.timings.tcpConnected.Sub(ws.timings.dialStart)
	if ws.timings.tlsHandshakeDone.IsZero() {
		ws.result.TLSHandshakeDone = 0
	} else {
		ws.result.TLSHandshakeDone = ws.timings.tlsHandshakeDone.Sub(ws.timings.dialStart)
	}
	ws.result.WSHandshakeDone = ws.timings.wsHandshakeDone.Sub(ws.timings.dialStart)
	if len(ws.timings.messageReads) < 1 {
		ws.result.FirstMessageResponse = 0
	} else {
		ws.result.FirstMessageResponse = ws.timings.messageReads[0].Sub(ws.timings.dialStart)
	}

	subscriptionStats := ws.snapshotSubscriptionStats()
	if subscriptionStats == nil {
		ws.result.Subscriptions = nil
		ws.result.SubscriptionFirstEvent = 0
		ws.result.SubscriptionLastEvent = 0
	} else {
		ws.result.Subscriptions = subscriptionStats
		ws.result.SubscriptionFirstEvent = ws.durationSinceDial(ws.subscriptionFirstEvent)
		ws.result.SubscriptionLastEvent = ws.durationSinceDial(ws.subscriptionLastEvent)
		var subMessages int
		for _, stats := range subscriptionStats {
			subMessages += int(stats.MessageCount)
		}
		ws.result.MessageCount += subMessages
	}

	// If the WSStat is not yet closed, set the total time to the current time
	if ws.timings.closeDone.IsZero() {
		ws.result.TotalTime = time.Since(ws.timings.dialStart)
	} else {
		ws.result.TotalTime = ws.timings.closeDone.Sub(ws.timings.dialStart)
	}
}

// readPump reads messages from the WebSocket connection and sends them to the read channel.
func (ws *WSStat) readPump() {
	defer func() {
		ws.wgPumps.Done()
		ws.Close()
	}()

	for {
		select {
		case <-ws.ctx.Done():
			return
		default:
			if err := ws.conn.SetReadDeadline(time.Now().Add(ws.timeout)); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to set read deadline")
			}
			if messageType, p, err := ws.conn.ReadMessage(); err != nil {
				ws.dispatchIncoming(&wsRead{err: err, messageType: messageType})
				ws.readChan <- &wsRead{err: err, messageType: messageType}
				return
			} else {
				if !ws.dispatchIncoming(&wsRead{data: p, messageType: messageType}) {
					ws.readChan <- &wsRead{data: p, messageType: messageType}
				}
			}
		}
	}
}

// writePump writes messages to the WebSocket connection.
func (ws *WSStat) writePump() {
	defer func() {
		ws.wgPumps.Done()
		ws.Close()
	}()

	for {
		select {
		case write, ok := <-ws.writeChan:
			if !ok {
				// Channel closed, exit write pump
				return
			}

			if err := ws.conn.SetWriteDeadline(time.Now().Add(ws.timeout)); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to set write deadline")
			}
			if err := ws.conn.WriteMessage(write.messageType, write.data); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to write message")
				return
			}
		case <-ws.ctx.Done():
			return
		}
	}
}

// Close closes the WebSocket connection and cleans up the WSStat instance.
// Sets result times: CloseDone
func (ws *WSStat) Close() {
	ws.closeOnce.Do(func() {
		// Cancel the context
		ws.cancel()

		for _, state := range ws.activeSubscriptions() {
			ws.finalizeSubscription(state, context.Canceled)
		}

		// If the connection is not already closed, close it gracefully
		if ws.conn != nil {
			// Set read deadline to stop reading messages
			if err := ws.conn.SetReadDeadline(time.Now()); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to set read deadline")
			}

			// Send close frame
			formattedCloseMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			deadline := time.Now().Add(time.Second)
			if err := ws.conn.WriteControl(
				websocket.CloseMessage,
				formattedCloseMessage,
				deadline,
			); err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				ws.log.Debug().Err(err).Msg("Failed to write close message")
			}

			if err := ws.conn.Close(); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to close connection")
			}
			ws.conn = nil
		}

		// Calculate timings and set result
		ws.timings.closeDone = time.Now()
		ws.calculateResult()

		// Wait for pumps to finish
		pumpsTimeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			ws.wgPumps.Wait()
			close(done)
		}()

		select {
		case <-done:
			// All goroutines finished
		case <-pumpsTimeoutCtx.Done():
			ws.log.Warn().Msg("Timeout closing WSStat pumps")
		}

		// Close the pump channels
		close(ws.readChan)
		close(ws.pongChan)
		close(ws.writeChan)
	})
}

// Dial establishes a new WebSocket connection using the custom dialer defined in this package.
// If required, specify custom headers to merge with the default headers.
// Sets times: dialStart, wsHandshakeDone
func (ws *WSStat) Dial(targetURL *url.URL, customHeaders http.Header) error {
	ws.result.URL = targetURL
	headers := http.Header{}
	// Preserve multi-value headers by copying each value individually
	for name, values := range customHeaders {
		for _, v := range values {
			headers.Add(name, v)
		}
	}
	ws.timings.dialStart = time.Now()
	conn, resp, err := ws.dialer.Dial(targetURL.String(), headers)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			defer func() {
				_ = resp.Body.Close()
			}()
			return fmt.Errorf("failed dial response '%s': %v", string(body), err)
		}
		return fmt.Errorf("failed to establish WebSocket connection: %v", err)
	}
	ws.timings.wsHandshakeDone = time.Now()
	ws.conn = conn
	ws.conn.SetPongHandler(func(_ string) error {
		select {
		case <-ws.ctx.Done():
			return nil
		default:
			ws.pongChan <- true
			return nil
		}
	})

	// Start the read and write pumps
	ws.wgPumps.Add(2)
	go ws.readPump()
	go ws.writePump()

	// Lookup IP
	ips, err := net.LookupIP(targetURL.Hostname())
	if err != nil {
		return fmt.Errorf("failed IP lookup: %v", err)
	}
	ws.result.IPs = make([]string, len(ips))
	for i, ip := range ips {
		ws.result.IPs[i] = ip.String()
	}

	// Capture request and response headers
	// documentedDefaultHeaders lists the known headers that Gorilla WebSocket sets by default.
	var documentedDefaultHeaders = map[string][]string{
		"Upgrade":               {"websocket"}, // Constant value
		"Connection":            {"Upgrade"},   // Constant value
		"Sec-WebSocket-Version": {"13"},        // Constant value

		// A nonce value; dynamically generated for each request
		"Sec-WebSocket-Key": {"<hidden>"},

		// Set by gorilla/websocket, but only if subprotocols are specified
		// "Sec-WebSocket-Protocol",
	}
	// Merge documented defaults without overwriting any user-provided values
	for k, vals := range documentedDefaultHeaders {
		if _, exists := headers[k]; !exists {
			headers[k] = append([]string(nil), vals...)
		}
	}
	ws.result.RequestHeaders = headers
	ws.result.ResponseHeaders = resp.Header

	return nil
}

// ExtractResult calculate the current results and returns a copy of the Result object.
func (ws *WSStat) ExtractResult() *Result {
	ws.calculateResult()

	resultCopy := *ws.result
	if ws.result.Subscriptions != nil {
		clone := make(map[string]SubscriptionStats, len(ws.result.Subscriptions))
		for id, stats := range ws.result.Subscriptions {
			clone[id] = stats
		}
		resultCopy.Subscriptions = clone
	}
	return &resultCopy
}

// OneHitMessage sends a single message through the WebSocket connection, and waits for
// the response. Note: this function assumes that the response received is the response to the
// sent message, make sure to only run this function sequentially to avoid unexpected behavior.
// Sets result times: MessageReads, MessageWrites
func (ws *WSStat) OneHitMessage(messageType int, data []byte) ([]byte, error) {
	ws.WriteMessage(messageType, data)

	// Assuming immediate response
	_, p, err := ws.ReadMessage()
	if err != nil {
		return nil, err
	}

	return p, nil
}

// OneHitMessageJSON sends a single JSON message through the WebSocket connection, and waits for
// the response. Note: this function assumes that the response received is the response to the
// sent message, make sure to only run this function sequentially to avoid unexpected behavior.
// Sets result times: MessageReads, MessageWrites
func (ws *WSStat) OneHitMessageJSON(v any) (any, error) {
	ws.WriteMessageJSON(v)

	// Assuming immediate response
	resp, err := ws.ReadMessageJSON()
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// PingPong sends a ping message through the WebSocket connection and awaits the pong.
// Note: this function assumes that the pong received is the response to the sent message,
// make sure to only run this function sequentially to avoid unexpected behavior.
// Sets result times: MessageReads, MessageWrites
func (ws *WSStat) PingPong() error {
	ws.WriteMessage(websocket.PingMessage, nil)
	return ws.ReadPong()
}

// Subscribe registers a long-lived listener using the supplied options and context.
// The returned Subscription can be used to consume streamed frames until cancellation.
func (ws *WSStat) Subscribe(ctx context.Context, opts SubscriptionOptions) (*Subscription, error) {
	if ws.conn == nil {
		return nil, errors.New("websocket connection is not established")
	}

	if opts.MessageType == 0 {
		opts.MessageType = websocket.TextMessage
	}

	bufferSize := opts.Buffer
	if bufferSize <= 0 {
		bufferSize = ws.defaultSubscriptionBuffer
	}

	parentCtx := ws.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	combinedCtx, cancel := context.WithCancel(parentCtx)
	done := make(chan struct{})

	state := &subscriptionState{
		options: opts,
		matcher: opts.Matcher,
		decoder: opts.Decoder,
		buffer:  make(chan SubscriptionMessage, bufferSize),
		done:    done,
		cancel:  cancel,
		ctx:     combinedCtx,
	}

	if opts.ID != "" {
		state.id = opts.ID
	} else {
		state.id = ws.newSubscriptionID()
	}

	// Ensure caller-provided context cancels the subscription if terminated early.
	if ctx != nil {
		go func(c context.Context, cancel func()) {
			select {
			case <-c.Done():
				cancel()
			case <-done:
			}
		}(ctx, cancel)
	}

	sub := ws.registerSubscription(state)
	go ws.watchSubscription(state)

	// Send initial subscription request if payload is provided.
	if len(opts.Payload) > 0 {
		ws.WriteMessage(opts.MessageType, opts.Payload)
	}

	return sub, nil
}

// SubscribeOnce registers a subscription and waits for the first delivered message before
// canceling the subscription. The returned message is a snapshot of the first delivery.
func (ws *WSStat) SubscribeOnce(
	ctx context.Context,
	opts SubscriptionOptions,
) (SubscriptionMessage, error) {
	sub, err := ws.Subscribe(ctx, opts)
	if err != nil {
		return SubscriptionMessage{}, err
	}

	updates := sub.Updates()
	done := sub.Done()
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}

	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				select {
				case <-done:
				default:
				}
				return SubscriptionMessage{},
					errors.New("subscription closed before delivering a message")
			}
			if msg.Err != nil {
				sub.Cancel()
				<-done
				return SubscriptionMessage{}, msg.Err
			}
			sub.Cancel()
			<-done
			return msg, nil
		case <-done:
			return SubscriptionMessage{},
				errors.New("subscription closed before delivering a message")
		case <-ctxDone:
			sub.Cancel()
			<-done
			if ctx.Err() != nil {
				return SubscriptionMessage{}, ctx.Err()
			}
			return SubscriptionMessage{}, context.Canceled
		}
	}
}

// ReadMessage reads a message from the WebSocket connection and measures the round-trip time.
// If an error occurs, it will be returned.
// Sets time: MessageReads
func (ws *WSStat) ReadMessage() (int, []byte, error) {
	msg, ok := <-ws.readChan
	if !ok {
		return 0, nil, errors.New("read channel closed")
	}

	if msg.err != nil {
		if websocket.IsUnexpectedCloseError(
			msg.err,
			websocket.CloseGoingAway,
			websocket.CloseNormalClosure,
		) {
			return msg.messageType, nil, fmt.Errorf("unexpected close error: %v", msg.err)
		}
		return msg.messageType, nil, msg.err
	}

	ws.timings.messageReads = append(ws.timings.messageReads, time.Now())

	return msg.messageType, msg.data, nil
}

// ReadMessageJSON reads a message from the WebSocket connection and measures the round-trip time.
// Sets time: MessageReads
func (ws *WSStat) ReadMessageJSON() (any, error) {
	msg, ok := <-ws.readChan
	if !ok {
		return nil, errors.New("read channel closed")
	}

	if msg.err != nil {
		return nil, msg.err
	}

	ws.timings.messageReads = append(ws.timings.messageReads, time.Now())

	var resp any
	err := json.Unmarshal(msg.data, &resp)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// ReadPong reads a pong message from the WebSocket connection and measures the round-trip time.
// Sets time: MessageReads
func (ws *WSStat) ReadPong() error {
	_, ok := <-ws.pongChan
	if !ok {
		return errors.New("pong channel closed")
	}

	ws.timings.messageReads = append(ws.timings.messageReads, time.Now())
	return nil
}

// WriteMessage sends a message through the WebSocket connection.
// Sets time: MessageWrites
func (ws *WSStat) WriteMessage(messageType int, data []byte) {
	ws.timings.messageWrites = append(ws.timings.messageWrites, time.Now())
	ws.writeChan <- &wsWrite{data: data, messageType: messageType}
}

// WriteMessageJSON sends a message through the WebSocket connection.
// Sets time: MessageWrites
func (ws *WSStat) WriteMessageJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		ws.log.Debug().Err(err).Msg("Failed to encode JSON")
		return
	}
	ws.timings.messageWrites = append(ws.timings.messageWrites, time.Now())
	ws.writeChan <- &wsWrite{data: b, messageType: websocket.TextMessage}
}

// durations returns a map of the time.Duration members of Result.
func (r *Result) durations() map[string]time.Duration {
	return map[string]time.Duration{
		"DNSLookup":     r.DNSLookup,
		"TCPConnection": r.TCPConnection,
		"TLSHandshake":  r.TLSHandshake,
		"WSHandshake":   r.WSHandshake,
		"MessageRTT":    r.MessageRTT,

		"DNSLookupDone":        r.DNSLookupDone,
		"TCPConnected":         r.TCPConnected,
		"TLSHandshakeDone":     r.TLSHandshakeDone,
		"WSHandshakeDone":      r.WSHandshakeDone,
		"FirstMessageResponse": r.FirstMessageResponse,
		"TotalTime":            r.TotalTime,
	}
}

// formatCompact prints the single-line comma-separated view used by %s, %q, and %v without '+'.
func (r *Result) formatCompact(s fmt.State) {
	d := r.durations()
	// Stable, readable order for single-line output
	order := []string{
		"DNSLookup", "TCPConnection", "TLSHandshake", "WSHandshake", "MessageRTT",
		"DNSLookupDone", "TCPConnected", "TLSHandshakeDone", "WSHandshakeDone",
		"FirstMessageResponse", "TotalTime",
	}
	list := make([]string, 0, len(order))
	for _, k := range order {
		v := d[k]
		if k == "TotalTime" && r.TotalTime == 0 {
			list = append(list, fmt.Sprintf("%s: - ms", k))
			continue
		}
		list = append(list, fmt.Sprintf("%s: %d ms", k, v/time.Millisecond))
	}
	io.WriteString(s, strings.Join(list, ", "))
}

// formatVerbosePlus prints the verbose multi-line view used by %#v.
func (r *Result) formatVerbosePlus(s fmt.State) {
	r.printURLAndIPSection(s)
	r.printTLSSectionIfPresent(s)
	r.printHeadersSection(s)
	r.printDurationsSection(s)
}

// printURLAndIPSection prints the URL details and IPs, followed by a blank line.
func (r *Result) printURLAndIPSection(s fmt.State) {
	fmt.Fprintln(s, "URL")
	fmt.Fprintf(s, "  Scheme: %s\n", r.URL.Scheme)
	host, port := hostPort(r.URL)
	fmt.Fprintf(s, "  Host: %s\n", host)
	fmt.Fprintf(s, "  Port: %s\n", port)
	if r.URL.Path != "" {
		fmt.Fprintf(s, "  Path: %s\n", r.URL.Path)
	}
	if r.URL.RawQuery != "" {
		fmt.Fprintf(s, "  Query: %s\n", r.URL.RawQuery)
	}
	fmt.Fprintln(s, "IP")
	fmt.Fprintf(s, "  %v\n", r.IPs)
	fmt.Fprintln(s)
}

// printTLSSectionIfPresent prints TLS handshake details and certificate information
// if TLSState is set. It ends with a blank line only when TLS details are printed,
// matching previous behavior.
func (r *Result) printTLSSectionIfPresent(s fmt.State) {
	if r.TLSState == nil {
		return
	}
	fmt.Fprint(s, "TLS handshake details\n")
	fmt.Fprintf(s, "  Version: %s\n", tls.VersionName(r.TLSState.Version))
	fmt.Fprintf(s, "  Cipher Suite: %s\n", tls.CipherSuiteName(r.TLSState.CipherSuite))
	fmt.Fprintf(s, "  Server Name: %s\n", r.TLSState.ServerName)
	fmt.Fprintf(s, "  Handshake Complete: %t\n", r.TLSState.HandshakeComplete)

	for i, cert := range r.CertificateDetails() {
		fmt.Fprintf(s, "Certificate %d\n", i+1)
		fmt.Fprintf(s, "  Common Name: %s\n", cert.CommonName)
		fmt.Fprintf(s, "  Issuer: %s\n", cert.Issuer)
		fmt.Fprintf(s, "  Not Before: %s\n", cert.NotBefore)
		fmt.Fprintf(s, "  Not After: %s\n", cert.NotAfter)
		fmt.Fprintf(s, "  Public Key Algorithm: %s\n", cert.PublicKeyAlgorithm.String())
		fmt.Fprintf(s, "  Signature Algorithm: %s\n", cert.SignatureAlgorithm.String())
		fmt.Fprintf(s, "  DNS Names: %v\n", cert.DNSNames)
		fmt.Fprintf(s, "  IP Addresses: %v\n", cert.IPAddresses)
		fmt.Fprintf(s, "  URIs: %v\n", cert.URIs)
	}
	fmt.Fprintln(s)
}

// printHeadersSection prints request and response headers (if present) and then a blank line.
func (r *Result) printHeadersSection(s fmt.State) {
	if r.RequestHeaders != nil {
		fmt.Fprint(s, "Request headers\n")
		for k, v := range r.RequestHeaders {
			fmt.Fprintf(s, "  %s: %s\n", k, strings.Join(v, ", "))
		}
	}
	if r.ResponseHeaders != nil {
		fmt.Fprint(s, "Response headers\n")
		for k, v := range r.ResponseHeaders {
			fmt.Fprintf(s, "  %s: %s\n", k, strings.Join(v, ", "))
		}
	}
	fmt.Fprintln(s)
}

// printDurationsSection prints the durations table exactly as before.
func (r *Result) printDurationsSection(s fmt.State) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "DNS lookup:     %4d ms\n", int(r.DNSLookup/time.Millisecond))
	fmt.Fprintf(&buf, "TCP connection: %4d ms\n", int(r.TCPConnection/time.Millisecond))
	fmt.Fprintf(&buf, "TLS handshake:  %4d ms\n", int(r.TLSHandshake/time.Millisecond))
	fmt.Fprintf(&buf, "WS handshake:   %4d ms\n", int(r.WSHandshake/time.Millisecond))
	fmt.Fprintf(&buf, "Msg round trip: %4d ms\n\n", int(r.MessageRTT/time.Millisecond))

	fmt.Fprintf(&buf, "Name lookup done:   %4d ms\n", int(r.DNSLookupDone/time.Millisecond))
	fmt.Fprintf(&buf, "TCP connected:      %4d ms\n", int(r.TCPConnected/time.Millisecond))
	fmt.Fprintf(&buf, "TLS handshake done: %4d ms\n", int(r.TLSHandshakeDone/time.Millisecond))
	fmt.Fprintf(&buf, "WS handshake done:  %4d ms\n", int(r.WSHandshakeDone/time.Millisecond))
	fmt.Fprintf(&buf, "First msg response: %4d ms\n", int(r.FirstMessageResponse/time.Millisecond))

	if r.TotalTime > 0 {
		fmt.Fprintf(&buf, "Total:              %4d ms\n", int(r.TotalTime/time.Millisecond))
	} else {
		fmt.Fprintf(&buf, "Total:          %4s ms\n", "-")
	}
	io.WriteString(s, buf.String())
}

// CertificateDetails returns a slice of CertificateDetails for each certificate in the
// TLS connection.
func (r *Result) CertificateDetails() []CertificateDetails {
	if r.TLSState == nil {
		return nil
	}

	var details []CertificateDetails
	for _, cert := range r.TLSState.PeerCertificates {
		details = append(details, CertificateDetails{
			CommonName:         cert.Subject.CommonName,
			Issuer:             cert.Issuer.CommonName,
			NotBefore:          cert.NotBefore,
			NotAfter:           cert.NotAfter,
			PublicKeyAlgorithm: cert.PublicKeyAlgorithm,
			SignatureAlgorithm: cert.SignatureAlgorithm,
			DNSNames:           cert.DNSNames,
			IPAddresses:        cert.IPAddresses,
			URIs:               cert.URIs,
		})
	}

	return details
}

// Format formats the time.Duration members of Result.
func (r *Result) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		if s.Flag('+') {
			r.formatVerbosePlus(s)
			return
		}
		fallthrough
	case 's', 'q':
		r.formatCompact(s)
	default:
		r.formatCompact(s)
	}
}

// hostPort returns the host and port from a URL.
func hostPort(u *url.URL) (host, port string) {
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		// Return the default port based on the scheme
		switch u.Scheme {
		case "ws":
			port = "80"
		case "wss":
			port = "443"
		default:
			port = ""
		}
	}
	return host, port
}

// newDialer initializes and returns a websocket.Dialer with customized dial functions
// to measure the connection phases.
// Sets timings: dnsLookupDone, tcpConnected, tlsHandshakeDone.
func newDialer(
	result *Result,
	timings *wsTimings,
	tlsConf *tls.Config,
	timeout time.Duration,
) *websocket.Dialer {
	return &websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target, err := resolveDialTargets(ctx, addr, timings)
			if err != nil {
				return nil, err
			}

			return dialWithAddresses(ctx, network, target, timeout, timings, nil)
		},

		NetDialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target, err := resolveDialTargets(ctx, addr, timings)
			if err != nil {
				return nil, err
			}

			wrap := func(netConn net.Conn) (net.Conn, error) {
				tlsConfig := tlsConf
				if tlsConfig == nil {
					tlsConfig = &tls.Config{}
				}
				if tlsConfig.ServerName == "" {
					tlsConfig = tlsConfig.Clone()
					tlsConfig.ServerName = target.host
				}

				tlsConn := tls.Client(netConn, tlsConfig)
				if err := tlsConn.Handshake(); err != nil {
					return nil, errors.Join(err, tlsConn.Close())
				}

				timings.tlsHandshakeDone = time.Now()
				state := tlsConn.ConnectionState()
				result.TLSState = &state

				return tlsConn, nil
			}

			return dialWithAddresses(ctx, network, target, timeout, timings, wrap)
		},
	}
}

type dialTarget struct {
	host  string
	port  string
	addrs []string
}

func resolveDialTargets(
	ctx context.Context,
	addr string,
	timings *wsTimings,
) (dialTarget, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return dialTarget{}, err
	}

	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return dialTarget{}, err
	}

	timings.dnsLookupDone = time.Now()
	if len(addrs) == 0 {
		return dialTarget{}, fmt.Errorf("no addresses found for %s", host)
	}

	return dialTarget{host: host, port: port, addrs: addrs}, nil
}

func dialWithAddresses(
	ctx context.Context,
	network string,
	target dialTarget,
	timeout time.Duration,
	timings *wsTimings,
	wrap func(net.Conn) (net.Conn, error),
) (net.Conn, error) {
	var dialErr error
	for _, ip := range target.addrs {
		dialer := &net.Dialer{Timeout: timeout}
		netConn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, target.port))
		if err != nil {
			dialErr = err
			continue
		}

		timings.tcpConnected = time.Now()

		if wrap == nil {
			return netConn, nil
		}

		wrappedConn, err := wrap(netConn)
		if err != nil {
			dialErr = err
			_ = netConn.Close()
			continue
		}

		return wrappedConn, nil
	}

	if dialErr != nil {
		return nil, dialErr
	}

	return nil, fmt.Errorf("no addresses found for %s", target.host)
}

// durationSinceDial returns the duration since the dial started.
func (ws *WSStat) durationSinceDial(ts time.Time) time.Duration {
	if ws.timings == nil || ts.IsZero() {
		return 0
	}
	return ts.Sub(ws.timings.dialStart)
}

// Option configures a WSStat instance.
type Option func(*options)

type options struct {
	tlsConfig  *tls.Config
	timeout    time.Duration
	bufferSize int
	logger     zerolog.Logger
}

// WithBufferSize sets the buffer size for read/write/pong channels.
func WithBufferSize(n int) Option { return func(o *options) { o.bufferSize = n } }

// WithLogger sets the logger for the WSStat instance.
func WithLogger(logger zerolog.Logger) Option { return func(o *options) { o.logger = logger } }

// WithTimeout sets the timeout used for dialing and read deadlines.
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// WithTLSConfig sets the TLS configuration for the connection.
func WithTLSConfig(cfg *tls.Config) Option { return func(o *options) { o.tlsConfig = cfg } }

// New creates and returns a new WSStat instance. To adjust channel buffer size or timeouts,
// use options. If not provided, package defaults are used for compatibility.
func New(opts ...Option) *WSStat {
	// Start with package defaults for back-compat
	cfg := options{
		bufferSize: defaultChanBufferSize,
		timeout:    defaultTimeout,
		tlsConfig:  nil,
		logger:     zerolog.Nop(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	result := &Result{}
	timings := &wsTimings{}
	dialer := newDialer(result, timings, cfg.tlsConfig, cfg.timeout)

	ctx, cancel := context.WithCancel(context.Background())
	ws := &WSStat{
		log:                       cfg.logger.With().Str("pkg", "wsstat").Logger(),
		dialer:                    dialer,
		timings:                   timings,
		result:                    result,
		ctx:                       ctx,
		cancel:                    cancel,
		readChan:                  make(chan *wsRead, cfg.bufferSize),
		pongChan:                  make(chan bool, cfg.bufferSize),
		writeChan:                 make(chan *wsWrite, cfg.bufferSize),
		timeout:                   cfg.timeout,
		tlsConf:                   cfg.tlsConfig,
		subscriptions:             make(map[string]*subscriptionState),
		subscriptionArchive:       make(map[string]SubscriptionStats),
		defaultSubscriptionBuffer: defaultSubscriptionBufferSize,
	}

	return ws
}
