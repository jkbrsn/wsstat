package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"time"

	"github.com/jkbrsn/wsstat/v3"
)

const (
	// defaultPingInterval is the delay between successive pings when --interval is unset.
	defaultPingInterval = time.Second
	// minPingInterval is the floor on --interval; a smaller value is rejected so a typo
	// cannot flood a production endpoint.
	minPingInterval = 10 * time.Millisecond
	// defaultReadTimeout mirrors the core's read/dial timeout default (wsstat.defaultTimeout),
	// used to render loss reasons when --timeout is unset.
	defaultReadTimeout = 5 * time.Second
	// pctScale converts a fraction to a percentage.
	pctScale = 100
)

// pingOutcome classifies a single ping's result. There are only two: a pong was received, or
// the ping was lost. A loss is always terminal here: wsstat's read pump tears down the socket
// after ws.timeout of silence (no active subscription in ping mode), so a connection cannot
// survive a missed pong, and redial-on-drop is deliberately out of scope for v1.
type pingOutcome int

const (
	// pingPong is a successful round-trip (a pong was received).
	pingPong pingOutcome = iota
	// pingLost is a missed pong or a dead connection; it ends the run.
	pingLost
)

// pingStats accumulates per-ping RTTs across a run: sent/received counts, min/max/sum, and a
// sum-of-squares for population stddev (what ping(8) labels mdev). Only received pongs feed the
// RTT aggregates; a lost ping counts toward sent only.
type pingStats struct {
	sent     int
	received int
	min      time.Duration
	max      time.Duration
	sum      time.Duration
	sumSq    float64 // sum of squared RTTs in ns^2 (float64 to avoid int64 overflow)
}

// observe records a received pong's round-trip time.
func (s *pingStats) observe(rtt time.Duration) {
	s.received++
	if s.received == 1 || rtt < s.min {
		s.min = rtt
	}
	if rtt > s.max {
		s.max = rtt
	}
	s.sum += rtt
	ns := float64(rtt)
	s.sumSq += ns * ns
}

// report snapshots the accumulator into a PingReport. RTT aggregates are left zero when no
// pong was received (the output layer omits them).
func (s *pingStats) report(target *url.URL) *PingReport {
	r := &PingReport{Target: target, Sent: s.sent, Received: s.received}
	if s.received == 0 {
		return r
	}
	n := float64(s.received)
	r.Min = s.min
	r.Max = s.max
	r.Avg = s.sum / time.Duration(s.received)
	mean := float64(s.sum) / n
	variance := s.sumSq/n - mean*mean
	if variance < 0 {
		// Floating-point cancellation can push an all-equal sample slightly negative.
		variance = 0
	}
	r.Stddev = time.Duration(math.Sqrt(variance))
	return r
}

// PingReport is the outcome of a ping run, returned so the caller can decide the exit code
// (zero pongs received == total loss). Per-ping lines are printed live inside RunPing.
type PingReport struct {
	Target                *url.URL
	Sent                  int
	Received              int
	Min, Avg, Max, Stddev time.Duration
}

// LossPct returns the packet-loss percentage over the run (0 when nothing was sent).
func (r *PingReport) LossPct() float64 {
	if r.Sent == 0 {
		return 0
	}
	return float64(r.Sent-r.Received) / float64(r.Sent) * pctScale
}

// validatePing checks ping-mode configuration and applies the interval default. Ping dials once
// and sends bare ping frames, so it rejects every measure/stream-only knob that implies a
// payload, a second message, or a summary cadence. The interval must stay below the read
// timeout: wsstat's read pump drops an idle socket after that timeout, so a longer interval
// would tear the connection down between pings.
func (c *Client) validatePing() error {
	switch {
	case c.output == OutputRaw:
		return errors.New("-o raw has no meaning in ping mode (no response payloads)")
	case c.responseFilePath != "":
		return errors.New("--file has no meaning in ping mode (no response payloads)")
	case len(c.textMessages) > 0:
		return errors.New("-t/--text is not supported in ping mode")
	case c.rpcMethod != "":
		return errors.New("--rpc-method is not supported in ping mode")
	case c.once:
		return errors.New("--once is not supported in ping mode")
	case c.buffer > 0:
		return errors.New("-b/--buffer is not supported in ping mode")
	case c.summaryInterval > 0:
		return errors.New("--summary-interval is not supported in ping mode")
	case c.sendDelay > 0:
		return errors.New("--send-delay is not supported in ping mode")
	}
	if c.interval == 0 {
		c.interval = defaultPingInterval
	}
	if c.interval < minPingInterval {
		return fmt.Errorf("interval must be at least %s", minPingInterval)
	}
	if c.interval >= c.pingTimeout() {
		return fmt.Errorf(
			"interval (%s) must be below the read timeout (%s); raise --timeout",
			c.interval, c.pingTimeout())
	}
	return nil
}

// pingTimeout returns the effective per-ping read timeout, used to bound the pong wait and to
// render loss reasons.
func (c *Client) pingTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return defaultReadTimeout
}

// pingLossReason renders a human reason for a terminal ping loss. wsstat's read pump drops the
// socket after pingTimeout of silence, so an unanswered ping surfaces as a closed socket
// (net.ErrClosed) or a bare deadline rather than a mid-flight timeout; both mean "no pong in
// time". A peer-initiated close surfaces as wsstat.ErrClosed. Only stdlib and wsstat sentinels
// are consulted (no coder/websocket import).
func (c *Client) pingLossReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, net.ErrClosed):
		return fmt.Sprintf("no response within %s", c.pingTimeout())
	case errors.Is(err, wsstat.ErrClosed):
		return "connection closed"
	default:
		return err.Error()
	}
}

// RunPing dials the target once, then sends a WebSocket ping frame every --interval on that
// connection, printing a per-ping RTT line live and a ping(8)-style summary at the end. The run
// ends when the count is reached, the context is canceled (Ctrl-C or --deadline), or a ping is
// lost (a missed pong or a closed connection); all paths print the summary.
//
// The error return is reserved for runtime failures the caller must surface as a non-zero exit
// (bad header, dial failure, output-write failure). Context cancellation and connection loss are
// swallowed: the summary is printed and a nil error is returned, so the caller derives the exit
// code from the report (zero pongs received == total loss) rather than from the error.
func (c *Client) RunPing(ctx context.Context, target *url.URL) (*PingReport, error) {
	header, err := parseHeaders(c.headers)
	if err != nil {
		return nil, err
	}

	ws := wsstat.New(c.wsstatOptions()...)
	if err := ws.DialContext(ctx, target, header); err != nil {
		ws.Close()
		return nil, handleConnectionError(err, target.String())
	}
	defer ws.Close()

	if err := c.printPingHeader(target, ws.ExtractResult()); err != nil {
		return nil, err
	}

	interval := c.interval
	if interval <= 0 {
		interval = defaultPingInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	stats := &pingStats{}
loop:
	for seq := 1; c.count == 0 || seq <= c.count; seq++ {
		if ctx.Err() != nil {
			break
		}
		alive, err := c.pingOnce(ctx, ws, stats, seq)
		if err != nil {
			return nil, err
		}
		if !alive || (c.count != 0 && seq == c.count) {
			break
		}

		select {
		case <-ctx.Done():
			break loop // labeled: a bare break would exit only the select
		case <-ticker.C:
		}
	}

	report := stats.report(target)
	if err := c.printPingSummary(report); err != nil {
		return nil, err
	}
	return report, nil
}

// pingOnce sends one ping, records it, and prints the reply line. It returns alive=false when
// the run should end (a lost ping or a context canceled mid-ping); the second return is an
// output-write error only. A ping interrupted by ctx cancellation (Ctrl-C or --deadline) still
// counts as sent but prints no reply line, since a --deadline expiry is not a real loss.
func (c *Client) pingOnce(
	ctx context.Context, ws *wsstat.WSStat, stats *pingStats, seq int,
) (bool, error) {
	start := time.Now()
	pingErr := ws.PingPong()
	rtt := time.Since(start)
	stats.sent++

	if ctx.Err() != nil {
		return false, nil
	}
	if pingErr != nil {
		return false, c.printPingReply(seq, rtt, pingLost, c.pingLossReason(pingErr))
	}
	stats.observe(rtt)
	return true, c.printPingReply(seq, rtt, pingPong, "")
}

// pingReplyJSONFor builds the NDJSON envelope for a single ping reply.
func (*Client) pingReplyJSONFor(
	seq int, rtt time.Duration, outcome pingOutcome, reason string,
) pingReplyJSON {
	rec := pingReplyJSON{Schema: JSONSchemaVersion, Type: "ping_reply", Seq: seq}
	if outcome == pingPong {
		rec.RTTMs = new(msFloat(rtt))
	} else {
		rec.Lost = true
		rec.Error = reason
	}
	return rec
}

// pingSummaryJSONFor builds the NDJSON envelope for the run summary.
func (*Client) pingSummaryJSONFor(report *PingReport) pingSummaryJSON {
	rec := pingSummaryJSON{
		Schema:   JSONSchemaVersion,
		Type:     "ping_summary",
		URL:      report.Target.String(),
		Sent:     report.Sent,
		Received: report.Received,
		LossPct:  report.LossPct(),
	}
	if report.Received > 0 {
		rec.MinMs = new(msFloat(report.Min))
		rec.AvgMs = new(msFloat(report.Avg))
		rec.MaxMs = new(msFloat(report.Max))
		rec.StddevMs = new(msFloat(report.Stddev))
	}
	return rec
}
