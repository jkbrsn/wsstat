package wsstat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	subscriptionIDPrefix = "subscription-"
)

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

// subscriptionDecoder converts an incoming frame into an arbitrary representation.
type subscriptionDecoder func(messageType int, data []byte) (any, error)

// subscriptionMatcher returns true when the provided frame should be delivered to the
// subscription. The decoded value is only non-nil if a Decoder is configured.
type subscriptionMatcher func(messageType int, data []byte, decoded any) bool

// SubscriptionOptions configures how WSStat establishes and demultiplexes a subscription.
type SubscriptionOptions struct {
	// ID can be provided to preassign a human-readable identifier. If empty, WSStat
	// allocates an incremental identifier.
	ID string

	// MessageType and Payload describe the initial frame sent to initiate the subscription.
	MessageType int
	Payload     []byte

	// decoder transforms inbound frames into a convenient representation before dispatching
	// to subscription consumers. If nil, frames are passed through as raw bytes.
	decoder subscriptionDecoder

	// matcher determines whether a given frame belongs to this subscription. When nil,
	// the dispatcher falls back to internal matching heuristics (such as explicit IDs).
	matcher subscriptionMatcher

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

// subscriptionState tracks the state of a subscription.
type subscriptionState struct {
	id string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	closed bool

	matcher subscriptionMatcher
	decoder subscriptionDecoder
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

// Subscribe registers a long-lived listener using the supplied options and context.
// The returned Subscription can be used to consume streamed frames until cancellation.
func (ws *WSStat) Subscribe(ctx context.Context, opts SubscriptionOptions) (*Subscription, error) {
	if ws.conn.Load() == nil {
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
		matcher: opts.matcher,
		decoder: opts.decoder,
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

// newSubscriptionID generates a new subscription ID.
func (ws *WSStat) newSubscriptionID() string {
	val := atomic.AddUint64(&ws.nextSubscriptionID, 1)
	return fmt.Sprintf("%s%d", subscriptionIDPrefix, val)
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

	now := message.Received
	dropped := false
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
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
	if state.buffer != nil {
		select {
		case state.buffer <- message:
		default:
			dropped = true
		}
	}
	state.mu.Unlock()

	if state.messages != nil {
		atomic.AddUint64(state.messages, 1)
	}
	if state.bytes != nil {
		atomic.AddUint64(state.bytes, uint64(message.Size))
	}

	ws.trackSubscriptionEvent(now)

	if dropped {
		ws.log.Warn().Str("subscription", state.id).
			Msg("subscription buffer full; dropping message")
	}
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

// snapshotSubscriptionStats snapshots the subscription stats.
func (ws *WSStat) snapshotSubscriptionStats() (
	stats map[string]SubscriptionStats,
	firstEvent time.Time,
	lastEvent time.Time,
) {
	ws.subscriptionMu.RLock()
	defer ws.subscriptionMu.RUnlock()

	size := len(ws.subscriptions) + len(ws.subscriptionArchive)
	if size == 0 {
		return nil, time.Time{}, time.Time{}
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
	for id, archived := range ws.subscriptionArchive {
		snap[id] = archived
	}

	return snap, ws.subscriptionFirstEvent, ws.subscriptionLastEvent
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
