// Package wsstat measures the latency of WebSocket connections.
// It wraps the gorilla/websocket package and includes latency measurements in the Result struct.
package wsstat

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
)

// WSStat wraps the gorilla/websocket package with latency measuring capabilities.
type WSStat struct {
	log zerolog.Logger

	conn    atomic.Pointer[websocket.Conn]
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

		if err := conn.SetReadDeadline(time.Now().Add(ws.timeout)); err != nil {
			ws.log.Debug().Err(err).Msg("Failed to set read deadline")
		}

		messageType, p, err := conn.ReadMessage()
		if err != nil {
			ws.dispatchIncoming(&wsRead{err: err, messageType: messageType})
			select {
			case ws.readChan <- &wsRead{err: err, messageType: messageType}:
			case <-ws.ctx.Done():
				ws.log.Debug().Msg("Context done, dropping error read")
			}
			return
		}

		// Message read successfully, dispatch if not handled by subscription
		if ws.dispatchIncoming(&wsRead{data: p, messageType: messageType}) {
			continue
		}

		select {
		case ws.readChan <- &wsRead{data: p, messageType: messageType}:
		case <-ws.ctx.Done():
			ws.log.Debug().Msg("Context done, dropping read message")
			return
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

			if err := conn.SetWriteDeadline(time.Now().Add(ws.timeout)); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to set write deadline")
			}

			if err := conn.WriteMessage(write.messageType, write.data); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to write message")
				return
			}
		}
	}
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
	ws.conn.Store(conn)
	conn.SetPongHandler(func(_ string) error {
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
	case ws.writeChan <- &wsWrite{data: b, messageType: websocket.TextMessage}:
		// Message sent successfully
	case <-ws.ctx.Done():
		// Connection is closing, drop the message
		ws.log.Debug().Msg("Dropping JSON write message, connection closing")
	}
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

// ReadMessage reads a message from the WebSocket connection and measures the round-trip time.
// If an error occurs, it will be returned.
// Sets time: MessageReads
func (ws *WSStat) ReadMessage() (int, []byte, error) {
	select {
	case <-ws.ctx.Done():
		return 0, nil, ws.ctx.Err()
	case msg, ok := <-ws.readChan:
		if !ok {
			return 0, nil, ws.ctx.Err()
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

		ws.timings.mu.Lock()
		ws.timings.messageReads = append(ws.timings.messageReads, time.Now())
		ws.timings.mu.Unlock()

		return msg.messageType, msg.data, nil
	}
}

// ReadMessageJSON reads a message from the WebSocket connection and measures the round-trip time.
// Sets time: MessageReads
func (ws *WSStat) ReadMessageJSON() (any, error) {
	select {
	case <-ws.ctx.Done():
		return nil, ws.ctx.Err()
	case msg, ok := <-ws.readChan:
		if !ok {
			return nil, ws.ctx.Err()
		}

		if msg.err != nil {
			return nil, msg.err
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

// ReadPong reads a pong message from the WebSocket connection and measures the round-trip time.
// Sets time: MessageReads
func (ws *WSStat) ReadPong() error {
	select {
	case <-ws.ctx.Done():
		return ws.ctx.Err()
	case _, ok := <-ws.pongChan:
		if !ok {
			return ws.ctx.Err()
		}

		ws.timings.mu.Lock()
		ws.timings.messageReads = append(ws.timings.messageReads, time.Now())
		ws.timings.mu.Unlock()
		return nil
	}
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

// Close closes the WebSocket connection and cleans up the WSStat instance.
// Sets result times: CloseDone
func (ws *WSStat) Close() {
	ws.closeOnce.Do(func() {
		// Cancel the context
		ws.cancel()

		for _, state := range ws.activeSubscriptions() {
			ws.finalizeSubscription(state, context.Canceled)
		}

		conn := ws.conn.Load()

		// If the connection is not already closed, close it gracefully
		if conn != nil {
			// Set read deadline to stop reading messages
			if err := conn.SetReadDeadline(time.Now()); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to set read deadline")
			}

			// Send close frame
			formattedCloseMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			deadline := time.Now().Add(time.Second)
			if err := conn.WriteControl(
				websocket.CloseMessage,
				formattedCloseMessage,
				deadline,
			); err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				ws.log.Debug().Err(err).Msg("Failed to write close message")
			}

			if err := conn.Close(); err != nil {
				ws.log.Debug().Err(err).Msg("Failed to close connection")
			}
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

// resolveDialTargets resolves the target address for dialing a WebSocket connection.
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

// dialWithAddresses dials a WebSocket connection using the specified network and target address.
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

// options stores the configuration for a WSStat instance.
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
