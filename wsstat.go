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
	wgPumps   sync.WaitGroup

	// instance configuration
	timeout      time.Duration
	closeGrace   time.Duration
	tlsConf      *tls.Config
	resolves     map[string]string // DNS resolution overrides: "host:port" → "address"
	readLimit    int64             // max inbound message size; -1 disables the limit
	subprotocols []string          // WebSocket subprotocols to negotiate
	headers      http.Header       // headers merged into every handshake
	compress     bool              // negotiate permessage-deflate
	validateUTF8 bool              // validate UTF-8 on inbound text frames
	invalidUTF8  atomic.Int64      // count of text frames that failed UTF-8 validation

	// Close-handshake frame, settable via CloseWith before teardown.
	closeStatus atomic.Int64           // handshake close code (default StatusNormalClosure)
	closeReason atomic.Pointer[string] // handshake close reason (nil means empty)
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
		subscriptions:             make(map[string]*subscriptionState),
		subscriptionArchive:       make(map[string]SubscriptionStats),
		defaultSubscriptionBuffer: defaultSubscriptionBufferSize,
	}
	ws.closeStatus.Store(int64(websocket.StatusNormalClosure))
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
			// If a subscription became active while a bounded read was in flight, the
			// stream now owns the connection: drop the timeout and keep reading rather
			// than finalizing the subscription on a spurious read deadline.
			if deadlineHit && ws.hasActiveSubscriptions() {
				continue
			}
			ws.dispatchIncoming(read)
			select {
			case ws.readChan <- read:
			case <-ws.ctx.Done():
				ws.log.Debug().Msg("Context done, dropping error read")
			}
			return
		}

		// coder/websocket performs no UTF-8 validation on text frames (RFC 6455 §5.6);
		// when opted in, flag invalid payloads via the logger and the Result counter rather
		// than failing the connection, since this is a measurement tool.
		if ws.validateUTF8 && read.messageType == TextMessage && !utf8.Valid(read.data) {
			ws.invalidUTF8.Add(1)
			ws.log.Warn().Int("bytes", len(read.data)).Msg("received text frame with invalid UTF-8")
		}

		// Message read successfully, dispatch if not handled by subscription
		if ws.dispatchIncoming(read) {
			continue
		}

		select {
		case ws.readChan <- read:
		case <-ws.ctx.Done():
			ws.log.Debug().Msg("Context done, dropping read message")
			return
		}
	}
}

// readFrame reads one frame, bounding the read with the dial/read timeout only when
// no subscription is active. Subscriptions are long-lived and idle by nature, so a
// per-read deadline would tear them down after a quiet interval; while one is active
// the read blocks until ws.ctx is canceled (Close). deadlineHit reports that the bound
// fired (a one-shot timeout) rather than a real transport error or context cancel.
func (ws *WSStat) readFrame(conn *websocket.Conn) (*wsRead, bool) {
	readCtx := ws.ctx
	var cancel context.CancelFunc
	if !ws.hasActiveSubscriptions() {
		readCtx, cancel = context.WithTimeout(ws.ctx, ws.timeout)
	}
	coderType, p, err := conn.Read(readCtx)
	deadlineHit := false
	if cancel != nil {
		deadlineHit = readCtx.Err() == context.DeadlineExceeded && ws.ctx.Err() == nil
		cancel()
	}
	return &wsRead{data: p, err: err, messageType: fromCoderType(coderType)}, deadlineHit
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
// Sets times: dialStart, wsHandshakeDone
func (ws *WSStat) DialContext(
	ctx context.Context, targetURL *url.URL, customHeaders http.Header,
) error {
	// A nil ctx would panic in context.WithCancel below; the Measure* contract promises a clean
	// abort instead, and direct callers get the same guarantee.
	if ctx == nil {
		return errors.New("nil context")
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
	compression := websocket.CompressionDisabled
	if ws.compress {
		compression = websocket.CompressionContextTakeover
	}
	ws.timings.dialStart = time.Now()
	conn, resp, err := websocket.Dial(ws.ctx, targetURL.String(), &websocket.DialOptions{
		HTTPClient:      ws.httpClient,
		HTTPHeader:      headers,
		Subprotocols:    ws.subprotocols,
		CompressionMode: compression,
	})
	if err != nil {
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

// WriteMessage sends a message through the WebSocket connection.
// Sets time: MessageWrites
func (ws *WSStat) WriteMessage(messageType int, data []byte) {
	// Check if connection is closing before attempting to write
	select {
	case <-ws.ctx.Done():
		ws.log.Debug().Msg("Dropping write message, connection closing")
		return
	default:
	}

	ws.timings.mu.Lock()
	ws.timings.messageWrites = append(ws.timings.messageWrites, time.Now())
	ws.timings.mu.Unlock()

	select {
	case ws.writeChan <- &wsWrite{data: data, messageType: messageType}:
		// Message sent successfully
	case <-ws.ctx.Done():
		// Connection is closing, drop the message
		ws.log.Debug().Msg("Dropping write message, connection closing")
	}
}

// WriteMessageJSON sends a message through the WebSocket connection.
// Sets time: MessageWrites
func (ws *WSStat) WriteMessageJSON(v any) {
	// Check if connection is closing before attempting to write
	select {
	case <-ws.ctx.Done():
		ws.log.Debug().Msg("Dropping JSON write message, connection closing")
		return
	default:
	}

	b, err := json.Marshal(v)
	if err != nil {
		ws.log.Debug().Err(err).Msg("Failed to encode JSON")
		return
	}

	ws.timings.mu.Lock()
	ws.timings.messageWrites = append(ws.timings.messageWrites, time.Now())
	ws.timings.mu.Unlock()

	select {
	case ws.writeChan <- &wsWrite{data: b, messageType: TextMessage}:
		// Message sent successfully
	case <-ws.ctx.Done():
		// Connection is closing, drop the message
		ws.log.Debug().Msg("Dropping JSON write message, connection closing")
	}
}

// PingPong sends a ping through the WebSocket connection and blocks until the matching pong
// is received. coder's Ping is a synchronous round-trip, so both the write and read timings
// are recorded around the single call.
// Sets result times: MessageReads, MessageWrites
func (ws *WSStat) PingPong() error {
	if ws.closed.Load() {
		return ErrClosed
	}
	conn := ws.conn.Load()
	if conn == nil {
		return ErrConnectionNotEstablished
	}

	ws.timings.mu.Lock()
	ws.timings.messageWrites = append(ws.timings.messageWrites, time.Now())
	ws.timings.mu.Unlock()

	pingCtx, cancel := context.WithTimeout(ws.ctx, ws.timeout)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		return err
	}

	ws.timings.mu.Lock()
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

// ReadMessage reads a message from the WebSocket connection and measures the round-trip time.
// If an error occurs, it will be returned.
// Sets time: MessageReads
func (ws *WSStat) ReadMessage() (int, []byte, error) {
	if ws.closed.Load() {
		return 0, nil, ErrClosed
	}
	if ws.conn.Load() == nil {
		return 0, nil, ErrConnectionNotEstablished
	}
	select {
	case <-ws.ctx.Done():
		return 0, nil, ws.ctx.Err()
	case msg, ok := <-ws.readChan:
		if !ok {
			return 0, nil, ErrClosed
		}

		if msg.err != nil {
			return msg.messageType, nil, classifyReadErr(msg.err)
		}

		ws.timings.mu.Lock()
		ws.timings.messageReads = append(ws.timings.messageReads, time.Now())
		ws.timings.mu.Unlock()

		return msg.messageType, msg.data, nil
	}
}

// ReadMessageJSON reads a message from the WebSocket connection and measures the round-trip time.
// Sets time: MessageReads
func (ws *WSStat) ReadMessageJSON() (any, error) {
	if ws.closed.Load() {
		return nil, ErrClosed
	}
	if ws.conn.Load() == nil {
		return nil, ErrConnectionNotEstablished
	}
	select {
	case <-ws.ctx.Done():
		return nil, ws.ctx.Err()
	case msg, ok := <-ws.readChan:
		if !ok {
			return nil, ErrClosed
		}

		if msg.err != nil {
			return nil, classifyReadErr(msg.err)
		}

		ws.timings.mu.Lock()
		ws.timings.messageReads = append(ws.timings.messageReads, time.Now())
		ws.timings.mu.Unlock()

		var resp any
		err := json.Unmarshal(msg.data, &resp)
		if err != nil {
			return nil, err
		}

		return resp, nil
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
	status := websocket.StatusCode(ws.closeStatus.Load())
	reason := ""
	if p := ws.closeReason.Load(); p != nil {
		reason = *p
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		if err := conn.Close(status, reason); err != nil {
			ws.log.Debug().Err(err).Msg("close handshake")
		}
	}()

	timer := time.NewTimer(ws.closeGrace)
	select {
	case <-closed:
		timer.Stop()
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

// CloseWith closes the connection sending a chosen close code and reason in the RFC 6455
// closing handshake, instead of Close's default StatusNormalClosure (1000) with an empty
// reason. code must be a sendable close status (1000-1003, 1007-1011, or 3000-4999) and
// reason at most 123 bytes (the control-frame payload limit minus the 2-byte code); an invalid
// code or over-long reason returns an error without closing. Returns ErrClosed if the
// connection is already closing. Otherwise teardown proceeds exactly as Close, which it calls;
// like Close it is idempotent, but the code/reason only take effect when CloseWith wins the
// race to initiate the close.
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
	ws.closeStatus.Store(int64(code))
	ws.closeReason.Store(&reason)
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
// instrumentation that gorilla's Dialer hooks used to.
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

				tlsConn := tls.Client(netConn, tlsConfig)
				if err := tlsConn.Handshake(); err != nil {
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
}

// WithBufferSize sets the buffer size for read/write/pong channels.
func WithBufferSize(n int) Option { return func(o *options) { o.bufferSize = n } }

// WithLogger sets the logger for the WSStat instance.
func WithLogger(logger zerolog.Logger) Option { return func(o *options) { o.logger = logger } }

// WithTimeout sets the timeout used for dialing and read deadlines.
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

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
