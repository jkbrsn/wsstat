package wsstat

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
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
