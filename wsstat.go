// Package wsstat measures the latency of WebSocket connections.
// It wraps the coder/websocket package and includes latency measurements in the Result struct.
package wsstat

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

const (
	// defaultTimeout is the default read/dial timeout for WSStat instances.
	defaultTimeout = 5 * time.Second
	// defaultCloseGrace bounds how long Close waits for the peer's closing-handshake
	// echo before forcing the socket shut. Above realistic worldwide RTT (clean 1000
	// closes stay clean) yet below coder's hard-coded 5s, so a non-echoing peer cannot
	// stall teardown for the full 5s.
	defaultCloseGrace = 3 * time.Second
	// defaultChanBufferSize is the default size of read/write channels.
	defaultChanBufferSize = 8
	// defaultSubscriptionBufferSize is the default queue length for subscription deliveries.
	defaultSubscriptionBufferSize = 32
	// defaultReadLimit bounds a single inbound message (16 MiB). Covers realistic payloads
	// while keeping coder's OOM guard; raise or disable via WithReadLimit.
	defaultReadLimit = 16 << 20
	// maxErrBodyBytes caps how much of a failed-handshake response body is read into the
	// returned error, so a hostile server cannot reflect an unbounded body into it.
	maxErrBodyBytes = 4 << 10

	// maxCloseReasonBytes is the largest close-handshake reason CloseWith accepts: the RFC 6455
	// control-frame payload limit (125 bytes) minus the 2-byte close code.
	maxCloseReasonBytes = 123

	// TextMessage denotes a UTF-8 encoded text message (e.g. JSON). Numerically identical to
	// websocket.MessageText so the public int-based API stays stable across the transport swap.
	TextMessage = 1
	// BinaryMessage denotes a binary data message. Numerically identical to
	// websocket.MessageBinary.
	BinaryMessage = 2
)

// toCoderType converts the public int message type to coder's websocket.MessageType.
func toCoderType(mt int) websocket.MessageType {
	return websocket.MessageType(mt)
}

// fromCoderType converts coder's websocket.MessageType back to the public int message type.
func fromCoderType(mt websocket.MessageType) int {
	return int(mt)
}

// Exported error sentinels for the failure classes library consumers branch on. Returned
// (via errors.Is) by the connection methods; the one-shot Measure* functions propagate them.
var (
	// ErrConnectionNotEstablished is returned when a read or ping is attempted before a
	// successful dial.
	ErrConnectionNotEstablished = errors.New("wsstat: connection not established")
	// ErrClosed is returned when an operation is attempted on a closed connection.
	ErrClosed = errors.New("wsstat: connection closed")
)

// documentedDefaultHeaders lists the known headers the WebSocket library sets by default.
var documentedDefaultHeaders = map[string][]string{
	"Upgrade":               {"websocket"}, // Constant value
	"Connection":            {"Upgrade"},   // Constant value
	"Sec-WebSocket-Version": {"13"},        // Constant value

	// A nonce value; dynamically generated for each request
	"Sec-WebSocket-Key": {"<hidden>"},

	// Set only if subprotocols are specified
	// "Sec-WebSocket-Protocol",
}

// WSStat wraps the coder/websocket package with latency measuring capabilities.
//
// Concurrency: DialContext must complete (single-threaded) before any other method is
// called. After a successful dial, ExtractResult and Close are safe to call concurrently
// with each other and with the read/write/subscription methods; ExtractResult takes a
// consistent snapshot even while Close is finalizing the Result. Close is idempotent and
// may be called from multiple goroutines. WriteMessage/WriteMessageJSON and
// ReadMessage/ReadMessageJSON/PingPong are not internally serialized against each other:
// concurrent writers (or concurrent readers) interleave on the shared channels, so callers
// that need ordering must coordinate it. Subscribe/SubscribeOnce are safe to call
// concurrently.
type WSStat struct {
	log zerolog.Logger

	conn       atomic.Pointer[websocket.Conn]
	netConn    atomic.Pointer[net.Conn] // raw transport conn, for forced teardown on close
	httpClient *http.Client
	timings    *wsTimings
	result     *Result
	resultMu   sync.Mutex // guards calculateResultLocked, the ExtractResult copy, and closeDone

	readChan  chan *wsRead
	writeChan chan *wsWrite

	subscriptionMu            sync.RWMutex
	subscriptions             map[string]*subscriptionState
	subscriptionArchive       map[string]SubscriptionStats
	nextSubscriptionID        atomic.Uint64
	defaultSubscriptionBuffer int
	subscriptionFirstEvent    time.Time
	subscriptionLastEvent     time.Time

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	closed    atomic.Bool // set once Close begins; distinguishes closed from never-dialed
	dialed    atomic.Bool // set once DialContext starts the pumps; the instance is single-use
	wgPumps   sync.WaitGroup

	// instance configuration
	timeout        time.Duration
	closeGrace     time.Duration
	tlsConf        *tls.Config
	resolves       map[string]string // DNS resolution overrides: "host:port" → "address"
	readLimit      int64             // max inbound message size; -1 disables the limit
	subprotocols   []string          // WebSocket subprotocols to negotiate
	headers        http.Header       // headers merged into every handshake
	compress       bool              // negotiate permessage-deflate
	validateUTF8   bool              // validate UTF-8 on inbound text frames
	unboundedReads bool              // drop the read pump's per-read timeout (long-lived sessions)
	discardReads   bool              // drop unclaimed inbound data frames instead of queueing them
	invalidUTF8    atomic.Int64      // count of text frames that failed UTF-8 validation

	// Close-handshake frame, settable via CloseWith before teardown. A single pointer so
	// code and reason are read and written together: nil means the default
	// StatusNormalClosure with an empty reason, and the first CloseWith to swap it in wins
	// both fields atomically.
	closeFrame atomic.Pointer[closeFrame]

	// recvCloseStatus records the peer's close status observed on a read error in the read
	// pump, so the code survives even when the buffered error read loses the delivery race
	// with teardown. -1 until a close frame arrives (see ReceivedCloseStatus).
	recvCloseStatus atomic.Int64
	// closeEchoLost marks that the bounded read's timeout tore the connection down: coder
	// kills the conn when the read context ends, and its Close masks the resulting
	// net.ErrClosed as nil, so a nil close error no longer implies the peer echoed;
	// recordCloseEcho must not fabricate an echo from it.
	closeEchoLost atomic.Bool
}

// New creates and returns a new WSStat instance. To adjust channel buffer size or timeouts,
// use options. If not provided, package defaults are used for compatibility.
func New(opts ...Option) *WSStat {
	// Start with package defaults for back-compat
	cfg := options{
		bufferSize: defaultChanBufferSize,
		timeout:    defaultTimeout,
		closeGrace: defaultCloseGrace,
		tlsConfig:  nil,
		logger:     zerolog.Nop(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	// Resolve the read limit: 0 (unset) uses the default; negative disables the limit.
	if cfg.readLimit == 0 {
		cfg.readLimit = defaultReadLimit
	}

	result := &Result{}
	timings := &wsTimings{}

	ctx, cancel := context.WithCancel(context.Background())
	ws := &WSStat{
		log:                       cfg.logger.With().Str("pkg", "wsstat").Logger(),
		timings:                   timings,
		result:                    result,
		ctx:                       ctx,
		cancel:                    cancel,
		readChan:                  make(chan *wsRead, cfg.bufferSize),
		writeChan:                 make(chan *wsWrite, cfg.bufferSize),
		timeout:                   cfg.timeout,
		closeGrace:                cfg.closeGrace,
		tlsConf:                   cfg.tlsConfig,
		resolves:                  cfg.resolves,
		readLimit:                 cfg.readLimit,
		subprotocols:              cfg.subprotocols,
		headers:                   cfg.headers,
		compress:                  cfg.compress,
		validateUTF8:              cfg.validateUTF8,
		unboundedReads:            cfg.unboundedReads,
		discardReads:              cfg.discardReads,
		subscriptions:             make(map[string]*subscriptionState),
		subscriptionArchive:       make(map[string]SubscriptionStats),
		defaultSubscriptionBuffer: defaultSubscriptionBufferSize,
	}
	ws.recvCloseStatus.Store(-1)
	// Built after ws so the transport can hand the raw conn back via captureNetConn.
	ws.httpClient = newHTTPClient(
		result, timings, cfg.tlsConfig, cfg.timeout, cfg.resolves, ws.captureNetConn,
	)

	return ws
}

// captureNetConn stores the raw transport connection so Close can force the socket
// shut if the peer never echoes the closing handshake.
func (ws *WSStat) captureNetConn(c net.Conn) {
	ws.netConn.Store(&c)
}

// wsRead holds the data read from the WebSocket connection.
type wsRead struct {
	data        []byte
	err         error
	messageType int
	at          time.Time // frame arrival, stamped in the read pump
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

	mu sync.Mutex // Protects messageWrites and messageReads
}

// calculateResultLocked calculates the durations of each phase of the WebSocket connection
// based on the current state of the WSStat timings. The caller must hold ws.resultMu.
// Note: if there haven't been as many message reads as writes, MessageRTT will be 0.
func (ws *WSStat) calculateResultLocked() {
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
	ws.timings.mu.Lock()
	numReads := len(ws.timings.messageReads)
	numWrites := len(ws.timings.messageWrites)
	if numReads < 1 && numWrites < 1 || numReads != numWrites {
		ws.result.MessageRTT = 0
		ws.result.MessageCount = 0
	} else {
		var meanMessageRTT time.Duration
		for i, readTime := range ws.timings.messageReads {
			writeTime := ws.timings.messageWrites[i]
			meanMessageRTT += readTime.Sub(writeTime)
		}
		ws.result.MessageRTT = meanMessageRTT / time.Duration(numReads)
		ws.result.MessageCount = numReads
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
	if numReads < 1 {
		ws.result.FirstMessageResponse = 0
	} else {
		ws.result.FirstMessageResponse = ws.timings.messageReads[0].Sub(ws.timings.dialStart)
	}
	ws.timings.mu.Unlock()

	subscriptionStats, firstEvent, lastEvent := ws.snapshotSubscriptionStats()
	if subscriptionStats == nil {
		ws.result.Subscriptions = nil
		ws.result.SubscriptionFirstEvent = 0
		ws.result.SubscriptionLastEvent = 0
	} else {
		ws.result.Subscriptions = subscriptionStats
		ws.result.SubscriptionFirstEvent = ws.durationSinceDial(firstEvent)
		ws.result.SubscriptionLastEvent = ws.durationSinceDial(lastEvent)
		var subMessages int
		for _, stats := range subscriptionStats {
			subMessages += int(stats.MessageCount)
		}
		ws.result.MessageCount += subMessages
	}

	ws.result.InvalidUTF8Frames = int(ws.invalidUTF8.Load())

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
		}

		conn := ws.conn.Load()
		if conn == nil {
			ws.log.Debug().Msg("Connection already closed, exiting read pump")
			return
		}

		read, deadlineHit := ws.readFrame(conn)
		if read.err != nil {
			ws.handleReadError(read, deadlineHit)
			return
		}

		// coder/websocket performs no UTF-8 validation on text frames (RFC 6455 §5.6);
		// when opted in, flag invalid payloads via the logger and the Result counter rather
		// than failing the connection, since this is a measurement tool.
		if ws.validateUTF8 && read.messageType == TextMessage && !utf8.Valid(read.data) {
			ws.invalidUTF8.Add(1)
			ws.log.Warn().Int("bytes", len(read.data)).Msg("received text frame with invalid UTF-8")
		}

		if !ws.deliverRead(read) {
			return
		}
	}
}

// handleReadError records what a failed read reveals and delivers the error read. The pump
// always exits afterwards: a bounded read that timed out has already lost the connection
// (coder tears the conn down when the read context ends), so retrying would only replace the
// timeout with the net.ErrClosed the next read is guaranteed to return.
//
//revive:disable-next-line:flag-parameter deadlineHit is read-outcome data, not a caller toggle
func (ws *WSStat) handleReadError(read *wsRead, deadlineHit bool) {
	// Capture the peer's close status here, before the buffered error read can lose
	// its delivery race with teardown, so ReceivedCloseStatus stays reliable.
	ws.recordCloseStatus(read.err)
	if deadlineHit {
		// The bound's cancel killed the connection (coder tears the conn down when the
		// read context ends), whether or not a close was in flight. Any later Close runs
		// its handshake on the dead conn and gets net.ErrClosed masked as nil, so a nil
		// close error no longer implies an echo. Flag it before the close goroutine
		// can record.
		ws.closeEchoLost.Store(true)
	}
	ws.dispatchIncoming(read)
	select {
	case ws.readChan <- read:
	case <-ws.ctx.Done():
		ws.log.Debug().Msg("Context done, dropping error read")
	}
}

// deliverRead routes a successfully-read frame: subscription dispatch first; in discard mode
// unclaimed frames are dropped so the pump keeps reading (no consumer drains readChan, and a
// blocked pump would also starve pong control-frame handling — coder/websocket processes
// control frames only inside Read); otherwise the frame is queued for the read methods.
// Reports false when the pump should exit.
func (ws *WSStat) deliverRead(read *wsRead) bool {
	if ws.dispatchIncoming(read) {
		return true
	}
	if ws.discardReads {
		return true
	}
	select {
	case ws.readChan <- read:
		return true
	case <-ws.ctx.Done():
		ws.log.Debug().Msg("Context done, dropping read message")
		return false
	}
}

// recordCloseStatus stores the peer's close status from a read error, if the error carries
// one, so ReceivedCloseStatus can report it after teardown. First write wins, atomically:
// the read pump and the close goroutine (via recordCloseEcho) race to record, and only one
// close frame ever arrives, so the first observation is the truth.
func (ws *WSStat) recordCloseStatus(err error) {
	if s := websocket.CloseStatus(err); s != -1 {
		ws.recvCloseStatus.CompareAndSwap(-1, int64(s))
	}
}

// recordCloseEcho records the peer's close-handshake echo when coder's internal close
// handshake consumed it instead of the read pump: the two race for coder's read mutex,
// and the loser never sees the close frame. coder's Close returns nil when the peer
// echoed the status we sent and a close error carrying the status when it echoed a
// different one, so a nil error implies an observed echo — unless the connection was
// torn down under the handshake (closeEchoLost), where coder masks net.ErrClosed as nil
// and no echo was ever read. First write wins: a status the read pump already recorded
// is kept.
func (ws *WSStat) recordCloseEcho(sent websocket.StatusCode, err error) {
	if err != nil {
		ws.recordCloseStatus(err)
		return
	}
	if ws.closeEchoLost.Load() {
		return
	}
	ws.recvCloseStatus.CompareAndSwap(-1, int64(sent))
}

// readFrame reads one frame, bounding the read with the dial/read timeout only when no
// subscription is active, WithUnboundedReads was not set, and no close is in progress.
// Subscriptions (and unbounded-read sessions such as a ping/pong monitor) are long-lived and
// idle by nature, so a per-read deadline would tear them down after a quiet interval; in those
// modes the read blocks until ws.ctx is canceled (Close). deadlineHit reports that the bound
// fired (a one-shot timeout) rather than a real transport error or context cancel.
//
// The bound is a manual timer rather than a context deadline: coder kills the whole connection
// when the read context ends, and once Close has begun, the closing handshake owns the
// connection — a bound firing then would tear the conn down mid-handshake and coder's Close
// would mask the resulting net.ErrClosed as nil, fabricating a close echo that never arrived.
// The timer re-checks both conditions when it fires rather than trusting the snapshot taken at
// read entry: the read pump reaches its first read before the dialer's caller can Subscribe, so
// a bound armed then would otherwise kill the connection out from under a subscription that
// registered while the read was already blocked. It stands down once ws.closed is set, leaving
// closeGrace as the close-phase bound.
func (ws *WSStat) readFrame(conn *websocket.Conn) (*wsRead, bool) {
	readCtx := ws.ctx
	var timedOut atomic.Bool
	if !ws.unboundedReads && !ws.hasActiveSubscriptions() && !ws.closed.Load() {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithCancel(ws.ctx)
		defer cancel()
		timer := time.AfterFunc(ws.timeout, func() {
			if ws.closed.Load() || ws.hasActiveSubscriptions() {
				return
			}
			timedOut.Store(true)
			cancel()
		})
		defer timer.Stop()
	}
	coderType, p, err := conn.Read(readCtx)
	deadlineHit := timedOut.Load() && ws.ctx.Err() == nil
	if deadlineHit && errors.Is(err, context.Canceled) {
		// Restore the deadline identity the manual timer's cancel replaced (coder surfaces
		// the read context's Err), so timeout errors keep their pre-timer shape for callers.
		err = context.DeadlineExceeded
	}
	return &wsRead{
		data: p, err: err, messageType: fromCoderType(coderType), at: time.Now(),
	}, deadlineHit
}

// writePump writes messages to the WebSocket connection.
func (ws *WSStat) writePump() {
	defer func() {
		ws.wgPumps.Done()
		ws.Close()
	}()

	for {
		select {
		case <-ws.ctx.Done():
			return
		case write, ok := <-ws.writeChan:
			if !ok {
				// Channel closed, exit write pump
				return
			}

			// Check context again to avoid processing writes after cancellation
			select {
			case <-ws.ctx.Done():
				return
			default:
			}

			// Load conn once and check for nil to avoid race with Close()
			conn := ws.conn.Load()
			if conn == nil {
				ws.log.Debug().Msg("Connection already closed, skipping write")
				return
			}

			writeCtx, cancel := context.WithTimeout(ws.ctx, ws.timeout)
			err := conn.Write(writeCtx, toCoderType(write.messageType), write.data)
			cancel()
			if err != nil {
				ws.log.Debug().Err(err).Msg("Failed to write message")
				return
			}
		}
	}
}

// DialContext establishes a new WebSocket connection bound to ctx. Canceling ctx (or calling
// Close) tears down the connection and unblocks in-flight reads and writes. If required, specify
// custom headers to merge with the default headers.
//
// A WSStat instance is single-use: after a successful dial (or a Close), DialContext returns
// an error; create a new instance to reconnect. A failed dial leaves the instance reusable.
// Sets times: dialStart, wsHandshakeDone
func (ws *WSStat) DialContext(
	ctx context.Context, targetURL *url.URL, customHeaders http.Header,
) error {
	// A nil ctx would panic in context.WithCancel below; the Measure* contract promises a clean
	// abort instead, and direct callers get the same guarantee.
	if ctx == nil {
		return errors.New("nil context")
	}
	// Close is permanent (closeOnce is consumed), so a redial would start pumps that
	// nothing can ever cancel; a second dial on a live instance would orphan the first
	// connection and its pumps. Guard both.
	if ws.closed.Load() {
		return ErrClosed
	}
	if !ws.dialed.CompareAndSwap(false, true) {
		return errors.New("wsstat: instance already dialed; create a new WSStat to reconnect")
	}
	// Install the connection context from the caller, replacing the placeholder created in New so
	// the pumps and read/write paths honor caller cancellation and deadlines.
	ws.cancel()
	ws.ctx, ws.cancel = context.WithCancel(ctx)

	ws.result.URL = targetURL
	// Option headers form the base; headers passed to this call override them per key.
	headers := cloneHeaders(ws.headers)
	for name, values := range customHeaders {
		headers[name] = append([]string(nil), values...)
	}
	// net/http drops a Host key from the header map when writing the request; the override
	// must travel via DialOptions.Host to reach the wire.
	hostOverride := headers.Get("Host")
	headers.Del("Host")
	compression := websocket.CompressionDisabled
	if ws.compress {
		compression = websocket.CompressionContextTakeover
	}
	ws.timings.dialStart = time.Now()
	conn, resp, err := websocket.Dial(ws.ctx, targetURL.String(), &websocket.DialOptions{
		HTTPClient:      ws.httpClient,
		HTTPHeader:      headers,
		Host:            hostOverride,
		Subprotocols:    ws.subprotocols,
		CompressionMode: compression,
	})
	if err != nil {
		// The pumps never started; allow the caller to retry on the same instance.
		ws.dialed.Store(false)
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBodyBytes))
			defer func() {
				_ = resp.Body.Close()
			}()
			return fmt.Errorf("failed dial response '%s': %w", string(body), err)
		}
		return fmt.Errorf("failed to establish WebSocket connection: %w", err)
	}
	ws.timings.wsHandshakeDone = time.Now()
	conn.SetReadLimit(ws.readLimit) // bound a single message; negative disables the limit
	ws.conn.Store(conn)
	// Result.IPs was set to the connected address by the transport during the handshake.

	// Start the read and write pumps after successful setup
	ws.wgPumps.Add(2)
	go ws.readPump()
	go ws.writePump()

	// Capture request and response headers
	if hostOverride != "" {
		headers.Set("Host", hostOverride)
	}
	ws.result.RequestHeaders = applyDefaultHeaders(headers)
	ws.result.ResponseHeaders = resp.Header

	// Capture the negotiated subprotocol and compression extension.
	ws.result.Subprotocol = conn.Subprotocol()
	ws.result.Compression = resp.Header.Get("Sec-WebSocket-Extensions")

	return nil
}

// cloneHeaders preserves multi-value headers by copying each value individually.
func cloneHeaders(src http.Header) http.Header {
	if src == nil {
		return http.Header{}
	}
	dst := make(http.Header, len(src))
	for name, values := range src {
		copied := make([]string, len(values))
		copy(copied, values)
		dst[name] = copied
	}
	return dst
}

// applyDefaultHeaders merges documented defaults without overwriting user-provided values.
func applyDefaultHeaders(headers http.Header) http.Header {
	dst := headers
	if dst == nil {
		dst = http.Header{}
	}
	for k, vals := range documentedDefaultHeaders {
		if _, exists := dst[k]; exists {
			continue
		}
		copied := make([]string, len(vals))
		copy(copied, vals)
		dst[k] = copied
	}
	return dst
}

// enqueueWrite queues a frame for the write pump without recording a write timing.
// It reports whether the frame was queued; false means the connection is closing
// and the frame was dropped.
func (ws *WSStat) enqueueWrite(messageType int, data []byte) bool {
	// Check if connection is closing before attempting to write
	select {
	case <-ws.ctx.Done():
		ws.log.Debug().Msg("Dropping write message, connection closing")
		return false
	default:
	}

	select {
	case ws.writeChan <- &wsWrite{data: data, messageType: messageType}:
		return true
	case <-ws.ctx.Done():
		// Connection is closing, drop the message
		ws.log.Debug().Msg("Dropping write message, connection closing")
		return false
	}
}

// WriteMessage sends a message through the WebSocket connection.
// Sets time: MessageWrites. A message dropped because the connection is closing
// records no timing, keeping the write/read ledgers pairable.
func (ws *WSStat) WriteMessage(messageType int, data []byte) {
	t := time.Now()
	if !ws.enqueueWrite(messageType, data) {
		return
	}
	ws.timings.mu.Lock()
	ws.timings.messageWrites = append(ws.timings.messageWrites, t)
	ws.timings.mu.Unlock()
}

// WriteMessageJSON sends a message through the WebSocket connection.
// Sets time: MessageWrites. A message dropped because the connection is closing
// (or one that fails to marshal) records no timing, keeping the write/read
// ledgers pairable.
func (ws *WSStat) WriteMessageJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		ws.log.Debug().Err(err).Msg("Failed to encode JSON")
		return
	}

	t := time.Now()
	if !ws.enqueueWrite(TextMessage, b) {
		return
	}
	ws.timings.mu.Lock()
	ws.timings.messageWrites = append(ws.timings.messageWrites, t)
	ws.timings.mu.Unlock()
}

// WriteMessageFragmented sends data as a single text or binary message split across
// len(fragments)+1 WebSocket frames: coder's streaming Writer emits one non-final frame per
// Write call for uncompressed messages, then a trailing empty continuation frame carrying the
// FIN bit from its Close. Each fragment is written synchronously, bypassing the write pump;
// run it on a connection dialed without compression, since permessage-deflate does not
// preserve fragment boundaries. fragments must be non-empty. Returns ErrClosed or
// ErrConnectionNotEstablished when the connection is unusable, and any transport error
// otherwise. Records no message timing.
func (ws *WSStat) WriteMessageFragmented(messageType int, fragments [][]byte) error {
	if len(fragments) == 0 {
		return errors.New("wsstat: no fragments to write")
	}
	if ws.closed.Load() {
		return ErrClosed
	}
	conn := ws.conn.Load()
	if conn == nil {
		return ErrConnectionNotEstablished
	}

	writeCtx, cancel := context.WithTimeout(ws.ctx, ws.timeout)
	defer cancel()

	w, err := conn.Writer(writeCtx, toCoderType(messageType))
	if err != nil {
		return fmt.Errorf("open fragmented writer: %w", err)
	}
	for _, fragment := range fragments {
		if _, err := w.Write(fragment); err != nil {
			_ = w.Close()
			return fmt.Errorf("write fragment: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close fragmented writer: %w", err)
	}
	return nil
}

// PingPong sends a ping through the WebSocket connection and blocks until the matching pong
// is received. coder's Ping is a synchronous round-trip, so both the write and read timings
// are recorded around the single call.
// Sets result times: MessageReads, MessageWrites
func (ws *WSStat) PingPong() error {
	return ws.PingPongContext(context.Background())
}

// PingPongContext is PingPong with the pong wait additionally bounded by ctx: the round-trip
// ends at the earliest of ctx's cancellation, the connection's context, and the read timeout.
// It lets a caller-side deadline or interrupt cut short a ping blocked on an unresponsive peer.
func (ws *WSStat) PingPongContext(ctx context.Context) error {
	if ws.closed.Load() {
		return ErrClosed
	}
	conn := ws.conn.Load()
	if conn == nil {
		return ErrConnectionNotEstablished
	}

	pingCtx, cancel := context.WithTimeout(ctx, ws.timeout)
	defer cancel()
	stop := context.AfterFunc(ws.ctx, cancel)
	defer stop()

	// Record both ends only once the round-trip completed. A ping whose pong never arrives
	// records no timing at all, keeping the write/read ledgers pairable — an unbalanced
	// ledger makes calculateResultLocked zero MessageRTT and MessageCount for the rest of
	// the connection's life. Popping the write on error is not an option: PingPong is not
	// serialized against WriteMessage, so it could remove another goroutine's entry.
	start := time.Now()
	if err := conn.Ping(pingCtx); err != nil {
		return err
	}

	ws.timings.mu.Lock()
	ws.timings.messageWrites = append(ws.timings.messageWrites, start)
	ws.timings.messageReads = append(ws.timings.messageReads, time.Now())
	ws.timings.mu.Unlock()
	return nil
}

// classifyReadErr applies the close-status contract shared by the read methods: a
// normal or going-away close passes through as-is, any other close status is wrapped
// as an unexpected close error, and non-close errors pass through unchanged.
func classifyReadErr(err error) error {
	status := websocket.CloseStatus(err)
	if status != -1 &&
		status != websocket.StatusNormalClosure &&
		status != websocket.StatusGoingAway {
		return fmt.Errorf("unexpected close error: %w", err)
	}
	return err
}

// CloseStatus returns the RFC 6455 close status code carried by an error returned from
// ReadMessage or ReadMessageJSON, or -1 if err is nil or carries no close status. It wraps
// coder/websocket's CloseStatus so callers need not import the transport package; because
// classifyReadErr preserves the error chain, a wrapped "unexpected close error" still yields
// its code.
func CloseStatus(err error) int {
	return int(websocket.CloseStatus(err))
}

// ReceivedCloseStatus returns the RFC 6455 close status the peer sent in its close frame,
// as observed by the read pump, or -1 if no close frame has been read. It is the reliable
// companion to CloseStatus for the closing handshake: when the connection is closed with
// CloseWith, the peer's echoed status is captured here even though the buffered read error
// carrying it can lose the delivery race with teardown.
func (ws *WSStat) ReceivedCloseStatus() int {
	return int(ws.recvCloseStatus.Load())
}

// handleRead processes a value received from readChan, recording read timing on success.
// The timing is the frame's arrival in the read pump, not the moment the consumer drained
// the channel, so time spent buffered does not inflate MessageRTT.
func (ws *WSStat) handleRead(msg *wsRead) (int, []byte, error) {
	if msg == nil {
		return 0, nil, ErrClosed
	}
	if msg.err != nil {
		return msg.messageType, nil, classifyReadErr(msg.err)
	}
	ws.timings.mu.Lock()
	ws.timings.messageReads = append(ws.timings.messageReads, msg.at)
	ws.timings.mu.Unlock()
	return msg.messageType, msg.data, nil
}

// ReadMessage reads a message from the WebSocket connection and measures the round-trip time.
// If an error occurs, it will be returned.
// Sets time: MessageReads
func (ws *WSStat) ReadMessage() (int, []byte, error) {
	// Drain a buffered read/error first: readPump enqueues an inbound error and then closes,
	// so checking closed before draining could mask a real close/read-limit error as ErrClosed.
	select {
	case msg := <-ws.readChan:
		return ws.handleRead(msg)
	default:
	}
	if ws.closed.Load() {
		return 0, nil, ErrClosed
	}
	if ws.conn.Load() == nil {
		return 0, nil, ErrConnectionNotEstablished
	}
	select {
	case <-ws.ctx.Done():
		return 0, nil, ws.ctx.Err()
	case msg := <-ws.readChan:
		return ws.handleRead(msg)
	}
}

// decodeRead unmarshals a successful read result as JSON, propagating any read error.
func decodeRead(_ int, data []byte, readErr error) (any, error) {
	if readErr != nil {
		return nil, readErr
	}
	var resp any
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ReadMessageJSON reads a message from the WebSocket connection and measures the round-trip time.
// Sets time: MessageReads
func (ws *WSStat) ReadMessageJSON() (any, error) {
	// Drain a buffered read/error first; see ReadMessage for why the closed check comes after.
	select {
	case msg := <-ws.readChan:
		return decodeRead(ws.handleRead(msg))
	default:
	}
	if ws.closed.Load() {
		return nil, ErrClosed
	}
	if ws.conn.Load() == nil {
		return nil, ErrConnectionNotEstablished
	}
	select {
	case <-ws.ctx.Done():
		return nil, ws.ctx.Err()
	case msg := <-ws.readChan:
		return decodeRead(ws.handleRead(msg))
	}
}

// ExtractResult calculate the current results and returns a copy of the Result object.
// Safe to call concurrently with the read/write/subscription methods and with Close.
func (ws *WSStat) ExtractResult() *Result {
	ws.resultMu.Lock()
	defer ws.resultMu.Unlock()
	ws.calculateResultLocked()

	resultCopy := *ws.result
	if ws.result.Subscriptions != nil {
		clone := make(map[string]SubscriptionStats, len(ws.result.Subscriptions))
		maps.Copy(clone, ws.result.Subscriptions)
		resultCopy.Subscriptions = clone
	}
	return &resultCopy
}

// gracefulClose performs coder's two-way RFC 6455 closing handshake (write Close frame,
// wait for the peer's echo), bounded by closeGrace. coder's Close blocks on a hard-coded
// 5s wait for that echo (waitCloseHandshake); a write-only / non-echoing peer never echoes,
// so on timeout the raw socket is forced shut, which unblocks coder's read and lets the
// close goroutine return instead of stalling the full 5s.
func (ws *WSStat) gracefulClose(conn *websocket.Conn) {
	status, reason := websocket.StatusNormalClosure, ""
	if f := ws.closeFrame.Load(); f != nil {
		status, reason = f.code, f.reason
	}
	closed := make(chan struct{})
	var closeErr error
	go func() {
		defer close(closed)
		closeErr = conn.Close(status, reason)
		if closeErr != nil {
			ws.log.Debug().Err(closeErr).Msg("close handshake")
		}
	}()

	timer := time.NewTimer(ws.closeGrace)
	select {
	case <-closed:
		timer.Stop()
		// The handshake completed within grace: surface the peer's echo in case coder's
		// internal close handshake consumed it before the read pump could observe it.
		ws.recordCloseEcho(status, closeErr)
	case <-timer.C:
		ws.log.Debug().Dur("grace", ws.closeGrace).
			Msg("close handshake timed out, forcing teardown")
		if nc := ws.netConn.Load(); nc != nil {
			_ = (*nc).Close()
		}
		<-closed
	}
}

// Close-code bounds for CloseWith validation (RFC 6455 §7.4). closeCodeReserved (1004) is
// reserved; the 3000-4999 range is for registered/private application use. The 1005/1006/1015
// codes are local-only sentinels (via coder's StatusNoStatusRcvd/AbnormalClosure/TLSHandshake)
// that must never be sent on the wire.
const (
	closeCodeReserved websocket.StatusCode = 1004
	closeCodeAppMin   websocket.StatusCode = 3000
	closeCodeAppMax   websocket.StatusCode = 4999
)

// validCloseCode reports whether code is a close status that may be sent on the wire. Mirrors
// coder/websocket's internal validation, leaving 1000-1011 (minus the local-only codes) and
// the 3000-4999 application range.
func validCloseCode(code int) bool {
	c := websocket.StatusCode(code)
	switch c {
	case closeCodeReserved, websocket.StatusNoStatusRcvd,
		websocket.StatusAbnormalClosure, websocket.StatusTLSHandshake:
		return false
	}
	if c >= websocket.StatusNormalClosure && c <= websocket.StatusBadGateway {
		return true
	}
	return c >= closeCodeAppMin && c <= closeCodeAppMax
}

// closeFrame is the close-handshake code/reason pair CloseWith installs before teardown.
type closeFrame struct {
	code   websocket.StatusCode
	reason string
}

// CloseWith closes the connection sending a chosen close code and reason in the RFC 6455
// closing handshake, instead of Close's default StatusNormalClosure (1000) with an empty
// reason. code must be a sendable close status (1000-1003, 1007-1011, or 3000-4999) and
// reason valid UTF-8 (RFC 6455 §5.5.1) of at most 123 bytes (the control-frame payload limit
// minus the 2-byte code); an invalid code or reason returns an error without closing. Returns
// ErrClosed if the connection is already closing. Otherwise teardown proceeds exactly as
// Close, which it calls; like Close it is idempotent, but the code/reason only take effect
// for the first CloseWith to install them before a close is initiated.
func (ws *WSStat) CloseWith(code int, reason string) error {
	if ws.closed.Load() {
		return ErrClosed
	}
	if !validCloseCode(code) {
		return fmt.Errorf("wsstat: invalid close code %d", code)
	}
	if len(reason) > maxCloseReasonBytes {
		return fmt.Errorf("wsstat: close reason exceeds %d bytes", maxCloseReasonBytes)
	}
	if !utf8.ValidString(reason) {
		return errors.New("wsstat: close reason is not valid UTF-8")
	}
	ws.closeFrame.CompareAndSwap(nil, &closeFrame{code: websocket.StatusCode(code), reason: reason})
	ws.Close()
	return nil
}

// Close closes the WebSocket connection and cleans up the WSStat instance.
// Sets result times: CloseDone
func (ws *WSStat) Close() {
	ws.closeOnce.Do(func() {
		ws.closed.Store(true)
		// Graceful close FIRST, while the read pump is still alive so the server's Close
		// echo is read off the socket before TCP teardown. Ordering is load-bearing:
		// canceling the context first would kill the read pump before the echo arrives
		// and force an ungraceful 1006 teardown.
		if conn := ws.conn.Load(); conn != nil {
			ws.gracefulClose(conn)
		}

		// Record closeDone after the handshake completes for accurate timing.
		// Guarded by resultMu because calculateResultLocked reads it and a concurrent
		// ExtractResult may be calculating at the same time.
		ws.resultMu.Lock()
		ws.timings.closeDone = time.Now()
		ws.resultMu.Unlock()

		// Now stop the pumps and finalize subscriptions.
		ws.cancel()

		for _, state := range ws.activeSubscriptions() {
			ws.finalizeSubscription(state, context.Canceled)
		}

		ws.resultMu.Lock()
		ws.calculateResultLocked()
		ws.resultMu.Unlock()

		// Wait for pumps to finish
		pumpsTimeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			ws.wgPumps.Wait()
			close(done)
		}()

		pumpsFinished := false
		select {
		case <-done:
			pumpsFinished = true
			// All goroutines finished
		case <-pumpsTimeoutCtx.Done():
			ws.log.Warn().Msg("Timeout closing WSStat pumps")
		}

		if pumpsFinished {
			ws.conn.Store(nil)
		}

		// Note: Channels are intentionally NOT closed here.
		// The pumps have exited due to context cancellation, and the channels
		// will be garbage collected when the WSStat instance is no longer referenced.
		// This avoids race conditions with external goroutines calling WriteMessage()
		// or ReadMessage() after Close(). Those methods check ws.ctx.Done() and
		// return early if the connection is closed.
	})
}

// dialTarget represents a target address for dialing a WebSocket connection.
type dialTarget struct {
	host  string
	port  string
	addrs []string
}

// newHTTPClient builds the instrumented *http.Client that coder's websocket.Dial uses to
// run the handshake. The transport's DialContext/DialTLSContext carry the per-phase timing
// instrumentation for the DNS, TCP, and TLS phases.
// Sets timings: dnsLookupDone, tcpConnected, tlsHandshakeDone.
func newHTTPClient(
	result *Result,
	timings *wsTimings,
	tlsConf *tls.Config,
	timeout time.Duration,
	resolves map[string]string,
	capture func(net.Conn),
) *http.Client {
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DisableKeepAlives: true,  // one connection per WSStat dial
		ForceAttemptHTTP2: false, // WebSocket upgrade requires HTTP/1.1

		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target, err := resolveDialTargets(ctx, addr, timings, resolves)
			if err != nil {
				return nil, err
			}

			return dialWithAddresses(ctx, network, target, timeout, timings, result, nil, capture)
		},

		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target, err := resolveDialTargets(ctx, addr, timings, resolves)
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

				// Bound the handshake explicitly: net/http detaches the dial context
				// (context.WithoutCancel) and abandons the dial goroutine when the request
				// deadline passes, so a peer that completes the TCP connect but never speaks
				// TLS would otherwise park this goroutine and its socket forever.
				tlsConn := tls.Client(netConn, tlsConfig)
				hsCtx, hsCancel := context.WithTimeout(ctx, timeout)
				defer hsCancel()
				if err := tlsConn.HandshakeContext(hsCtx); err != nil {
					return nil, errors.Join(err, tlsConn.Close())
				}

				timings.tlsHandshakeDone = time.Now()
				state := tlsConn.ConnectionState()
				result.TLSState = &state

				return tlsConn, nil
			}

			return dialWithAddresses(ctx, network, target, timeout, timings, result, wrap, capture)
		},
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout, // overall handshake timeout
	}
}

// resolveDialTargets resolves the target address for dialing a WebSocket connection.
// If a DNS override exists for the host:port combination, it is used instead of DNS lookup.
func resolveDialTargets(
	ctx context.Context,
	addr string,
	timings *wsTimings,
	resolves map[string]string,
) (dialTarget, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return dialTarget{}, err
	}

	// Check for DNS override first
	key := net.JoinHostPort(strings.ToLower(host), port)
	if overrideIP, ok := resolves[key]; ok {
		timings.dnsLookupDone = time.Now()
		return dialTarget{
			host:  host,
			port:  port,
			addrs: []string{overrideIP},
		}, nil
	}

	// Fall back to DNS lookup
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

// dialWithAddresses dials a WebSocket connection using the specified network and target address.
func dialWithAddresses(
	ctx context.Context,
	network string,
	target dialTarget,
	timeout time.Duration,
	timings *wsTimings,
	result *Result,
	wrap func(net.Conn) (net.Conn, error),
	capture func(net.Conn),
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
		// Record the actually-connected address. Overwritten per attempt so the value
		// reflects the IP whose connection is ultimately returned.
		result.IPs = []string{ip}

		// Capture the raw conn (not the TLS wrapper) so Close can force the underlying
		// socket shut; closing it unblocks coder's read regardless of the TLS layer.
		if capture != nil {
			capture(netConn)
		}

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

// options stores the configuration for a WSStat instance.
type options struct {
	tlsConfig    *tls.Config
	timeout      time.Duration
	closeGrace   time.Duration
	bufferSize   int
	logger       zerolog.Logger
	resolves     map[string]string // DNS resolution overrides: "host:port" → "address"
	readLimit    int64             // max inbound message size; 0 uses the default, -1 disables
	subprotocols []string          // WebSocket subprotocols to negotiate
	headers      http.Header       // headers merged into every handshake
	compress     bool              // negotiate permessage-deflate
	validateUTF8 bool              // validate UTF-8 on inbound text frames
	// unboundedReads drops the per-read timeout on the read pump (like an active
	// subscription), for long-lived sessions where control-frame traffic (ping/pong)
	// carries the connection but never surfaces as a read.
	unboundedReads bool
	// discardReads drops inbound data frames not claimed by a subscription instead of
	// queueing them on the read channel, for sessions that never call ReadMessage.
	discardReads bool
}

// WithBufferSize sets the buffer size for read/write/pong channels.
func WithBufferSize(n int) Option { return func(o *options) { o.bufferSize = n } }

// WithLogger sets the logger for the WSStat instance.
func WithLogger(logger zerolog.Logger) Option { return func(o *options) { o.logger = logger } }

// WithTimeout sets the timeout used for dialing and read deadlines.
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// WithUnboundedReads drops the read pump's per-read timeout so an idle connection is not torn
// down after the read/dial timeout elapses with no inbound data frame. It matches how reads
// behave while a subscription is active. Use it for long-lived sessions driven by control
// frames the read pump never sees as reads: a ping/pong monitor sends ping frames and receives
// pongs (both handled below Read), so without this the connection would be closed one timeout
// after the last data frame. PingPong keeps its own per-ping timeout, so a lost pong is still
// detected; it just no longer kills the connection.
func WithUnboundedReads() Option { return func(o *options) { o.unboundedReads = true } }

// WithDiscardReads drops inbound data frames not claimed by a subscription instead of
// queueing them on the read channel. Without it, a session that never calls ReadMessage
// (such as a ping/pong monitor) fills the read channel after bufferSize unsolicited frames
// from a chatty peer; the blocked read pump then stops calling Read, which also starves
// pong control-frame processing. Error reads are still delivered to the read channel.
func WithDiscardReads() Option { return func(o *options) { o.discardReads = true } }

// WithCloseGrace bounds how long Close waits for the peer's closing-handshake echo
// before forcing the connection shut. Zero or negative fires the teardown immediately
// rather than granting a grace window; a peer that echoes in that instant may still
// complete the handshake cleanly. Defaults to 3s.
//
// Only values below 5s take effect: the underlying coder/websocket library caps its
// own close handshake at a hard-coded 5s, so Close returns by then regardless and a
// larger grace cannot extend the wait. The useful range is (0, 5s).
func WithCloseGrace(d time.Duration) Option { return func(o *options) { o.closeGrace = d } }

// WithTLSConfig sets the TLS configuration for the connection.
func WithTLSConfig(cfg *tls.Config) Option { return func(o *options) { o.tlsConfig = cfg } }

// WithResolves sets DNS resolution overrides for specific host:port combinations.
// Map key format: "host:port", value: "ip_address".
func WithResolves(resolves map[string]string) Option {
	return func(o *options) { o.resolves = resolves }
}

// WithReadLimit bounds the size in bytes of a single inbound message. A value of 0 keeps the
// default (16 MiB); a negative value disables the limit (use with care: an unbounded message
// can exhaust memory). Defaults to 16 MiB.
func WithReadLimit(n int64) Option { return func(o *options) { o.readLimit = n } }

// WithSubprotocols sets the WebSocket subprotocols to offer during the handshake, in preference
// order. The negotiated value is reported in Result.Subprotocol.
func WithSubprotocols(subprotocols []string) Option {
	return func(o *options) { o.subprotocols = subprotocols }
}

// WithHeaders sets HTTP headers merged into every handshake request. Headers passed directly to
// Dial/DialContext take precedence over these on a per-key basis.
func WithHeaders(headers http.Header) Option {
	return func(o *options) { o.headers = headers }
}

// WithCompression enables negotiation of the permessage-deflate extension. Disabled by default.
// The negotiated extension is reported in Result.Compression.
func WithCompression(enabled bool) Option {
	return func(o *options) { o.compress = enabled }
}

// WithValidateUTF8 enables UTF-8 validation of inbound text frames. coder/websocket performs
// none (RFC 6455 §5.6 requires text payloads to be valid UTF-8); when enabled, an invalid text
// frame is logged at warn level and counted in Result.InvalidUTF8Frames rather than failing the
// connection. Disabled by default.
func WithValidateUTF8(enabled bool) Option {
	return func(o *options) { o.validateUTF8 = enabled }
}
