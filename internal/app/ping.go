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
	// pingCloseGrace bounds the closing handshake so teardown cannot blow past --deadline:
	// the deferred Close runs after the deadline fires, and the default 3s grace would let a
	// non-echoing peer hold the process well beyond the advertised max run time. An echoing
	// peer completes the handshake in one RTT, far under this cap.
	pingCloseGrace = 500 * time.Millisecond
	// pctScale converts a fraction to a percentage.
	pctScale = 100
)

// pingOutcome classifies a single ping's result. The connection dials with WithUnboundedReads,
// so a missed pong no longer tears the socket down: a timeout is a survivable loss and the run
// continues, exactly like ping(8). Only a real connection close (or a context canceled mid-ping)
// ends the run.
type pingOutcome int

const (
	// pingPong is a successful round-trip (a pong was received).
	pingPong pingOutcome = iota
	// pingTimeout is a missed pong within the per-ping timeout; the connection survives, so
	// the run continues.
	pingTimeout
	// pingDead is a closed connection or other transport error; it ends the run.
	pingDead
	// pingCanceled marks a ping interrupted mid-flight by context cancellation (Ctrl-C or
	// --deadline). It counts as neither sent nor lost, prints no reply line, and ends the run.
	pingCanceled
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
// payload, a second message, or a summary cadence.
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

// classifyPing maps a non-nil PingPong error to an outcome and a human reason. With unbounded
// reads a missed pong surfaces as a clean context.DeadlineExceeded and leaves the connection
// alive, so it is a survivable timeout; anything else (a peer close, a transport error) means
// the connection is gone. Only stdlib and wsstat sentinels are consulted (no coder/websocket
// import).
func (c *Client) classifyPing(err error) (pingOutcome, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return pingTimeout, fmt.Sprintf("no response within %s", c.pingTimeout())
	}
	return pingDead, deadReason(err)
}

// deadReason renders the human reason for a terminal ping loss.
func deadReason(err error) string {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, wsstat.ErrClosed) {
		return "connection closed"
	}
	return err.Error()
}

// RunPing dials the target once (with unbounded reads so an idle connection is not torn down
// between pings), then sends a WebSocket ping frame every --interval on that connection, printing
// a per-ping RTT line live and a ping(8)-style summary at the end. A missed pong is reported and
// the run continues; the run ends only when the count is reached, the context is canceled (Ctrl-C
// or --deadline), or the connection closes. All paths print the summary.
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

	// Unbounded reads keep the connection alive while only ping/pong traffic flows: pongs are
	// control frames the read pump never sees as reads, so the default per-read timeout would
	// otherwise close the socket one --timeout after dial. Discarded reads keep the pump
	// running against a chatty peer (welcome messages, heartbeats, feed data): nothing here
	// consumes data frames, and a stalled pump would starve pong processing and misreport a
	// healthy endpoint as total loss.
	ws := wsstat.New(append(
		c.wsstatOptions(), wsstat.WithUnboundedReads(), wsstat.WithDiscardReads(),
		wsstat.WithCloseGrace(pingCloseGrace))...)
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
		outcome, err := c.pingOnce(ctx, ws, stats, seq)
		if err != nil {
			return nil, err
		}
		if outcome == pingDead || outcome == pingCanceled {
			break
		}
		if c.count != 0 && seq == c.count {
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

// pingOnce sends one ping, records it, and prints the reply line, returning the outcome. A pong
// or a survivable timeout lets the run continue; pingDead (a closed connection) and pingCanceled
// (a context canceled mid-ping) end it. A pong that arrived in the same instant the context was
// canceled is still a received pong (the run then ends via the loop's context check), so a
// --deadline that fires just after the reply cannot flip a live host to total loss. A canceled
// ping with no pong counts as neither sent nor lost: its wait was cut short of the full timeout
// window, so letting it into the stats would report phantom loss on every Ctrl-C. sent otherwise
// counts attempts, so a write that failed on an already-dead connection is included (unlike
// ping(8), which counts only transmitted probes). The error return is an output-write error.
func (c *Client) pingOnce(
	ctx context.Context, ws *wsstat.WSStat, stats *pingStats, seq int,
) (pingOutcome, error) {
	start := time.Now()
	pingErr := ws.PingPongContext(ctx)
	rtt := time.Since(start)

	if pingErr == nil {
		stats.sent++
		stats.observe(rtt)
		return pingPong, c.printPingReply(seq, rtt, pingPong, "")
	}
	if ctx.Err() != nil {
		return pingCanceled, nil
	}
	stats.sent++
	outcome, reason := c.classifyPing(pingErr)
	return outcome, c.printPingReply(seq, rtt, outcome, reason)
}

// pingReplyJSONFor builds the NDJSON envelope for a single ping reply.
func (*Client) pingReplyJSONFor(
	seq int, rtt time.Duration, outcome pingOutcome, reason string,
) pingReplyJSON {
	rec := pingReplyJSON{Schema: JSONSchemaVersion, Type: "ping_reply", Seq: seq}
	if outcome == pingPong {
		// Pointer (not omitempty float) so a sub-microsecond RTT that rounds to 0.0 still
		// serializes; a lost reply leaves it nil.
		ms := msFloat(rtt)
		rec.RTTMs = &ms
	} else {
		// pingTimeout and pingDead both record a lost ping with a reason.
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
		// Pointers (not omitempty floats) so a legitimately-zero aggregate — e.g. a
		// single-sample stddev — stays present whenever a pong was received.
		minMs, avgMs := msFloat(report.Min), msFloat(report.Avg)
		maxMs, stddevMs := msFloat(report.Max), msFloat(report.Stddev)
		rec.MinMs, rec.AvgMs, rec.MaxMs, rec.StddevMs = &minMs, &avgMs, &maxMs, &stddevMs
	}
	return rec
}
