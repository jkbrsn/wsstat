package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pingServerMode selects how the ping test server answers (or ignores) client pings.
type pingServerMode int

const (
	// pingServerAnswer auto-answers every ping (coder responds below the app once reading).
	pingServerAnswer pingServerMode = iota
	// pingServerNoRead never reads, so pings go unanswered and time out client-side.
	pingServerNoRead
	// pingServerCloseAfter answers for closeAfter, then closes the connection normally.
	pingServerCloseAfter
	// pingServerDelayRead ignores pings for delayRead (they time out), then answers.
	pingServerDelayRead
)

type pingServerOpts struct {
	mode       pingServerMode
	closeAfter time.Duration
	delayRead  time.Duration
}

// newPingServer starts an httptest WebSocket server whose ping handling follows opts, and
// returns its ws:// URL. The server is torn down via t.Cleanup.
func newPingServer(t *testing.T, opts pingServerOpts) *url.URL {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()

		switch opts.mode {
		case pingServerNoRead:
			<-ctx.Done()
		case pingServerDelayRead:
			select {
			case <-time.After(opts.delayRead):
			case <-ctx.Done():
				return
			}
			readCtx := conn.CloseRead(ctx)
			<-readCtx.Done()
		case pingServerCloseAfter:
			readCtx := conn.CloseRead(ctx)
			select {
			case <-time.After(opts.closeAfter):
				_ = conn.Close(websocket.StatusNormalClosure, "bye")
			case <-readCtx.Done():
			}
		default: // pingServerAnswer
			readCtx := conn.CloseRead(ctx)
			<-readCtx.Done()
		}
	}))
	t.Cleanup(server.Close)

	u, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)
	return u
}

func TestPingStats(t *testing.T) {
	t.Parallel()

	t.Run("empty yields no rtt aggregates", func(t *testing.T) {
		s := &pingStats{}
		s.sent = 3
		r := s.report(nil)
		assert.Equal(t, 3, r.Sent)
		assert.Equal(t, 0, r.Received)
		assert.Zero(t, r.Min)
		assert.Zero(t, r.Avg)
		assert.Zero(t, r.Max)
		assert.Zero(t, r.Stddev)
		assert.InDelta(t, 100.0, r.LossPct(), 0.001)
	})

	t.Run("single sample has zero stddev", func(t *testing.T) {
		s := &pingStats{}
		s.sent = 1
		s.observe(10 * time.Millisecond)
		r := s.report(nil)
		assert.Equal(t, 1, r.Received)
		assert.Equal(t, 10*time.Millisecond, r.Min)
		assert.Equal(t, 10*time.Millisecond, r.Avg)
		assert.Equal(t, 10*time.Millisecond, r.Max)
		assert.Zero(t, r.Stddev)
		assert.InDelta(t, 0.0, r.LossPct(), 0.001)
	})

	t.Run("known fixture stddev", func(t *testing.T) {
		// Samples 10/20/30/40ms: mean 25ms, population variance 125ms^2, stddev sqrt(125)
		// = 11.1803ms.
		s := &pingStats{}
		s.sent = 4
		for _, ms := range []int{10, 20, 30, 40} {
			s.observe(time.Duration(ms) * time.Millisecond)
		}
		r := s.report(nil)
		assert.Equal(t, 4, r.Received)
		assert.Equal(t, 10*time.Millisecond, r.Min)
		assert.Equal(t, 40*time.Millisecond, r.Max)
		assert.Equal(t, 25*time.Millisecond, r.Avg)
		assert.InDelta(t, 11.1803, float64(r.Stddev)/float64(time.Millisecond), 0.001)
		assert.InDelta(t, 0.0, r.LossPct(), 0.001)
	})
}

func TestRunPingCountReached(t *testing.T) {
	// No t.Parallel: captureStdoutFrom swaps the global os.Stdout.
	u := newPingServer(t, pingServerOpts{mode: pingServerAnswer})
	c := &Client{count: 3, mode: ModePing, interval: 10 * time.Millisecond}
	require.NoError(t, c.Validate())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var report *PingReport
	out := captureStdoutFrom(t, func() error {
		var err error
		report, err = c.RunPing(ctx, u)
		return err
	})

	require.NotNil(t, report)
	assert.Equal(t, 3, report.Sent)
	assert.Equal(t, 3, report.Received)
	// Sequence numbers are 1-based and monotonic.
	assert.Less(t, strings.Index(out, "seq=1"), strings.Index(out, "seq=2"))
	assert.Less(t, strings.Index(out, "seq=2"), strings.Index(out, "seq=3"))
	assert.Equal(t, 3, strings.Count(out, "pong: seq="))
	assert.Contains(t, out, "3 sent, 3 received, 0.0% loss")
	assert.Contains(t, out, "STATS ")
}

func TestRunPingContextCancelPrintsSummary(t *testing.T) {
	u := newPingServer(t, pingServerOpts{mode: pingServerAnswer})
	c := &Client{count: 0, mode: ModePing, interval: 15 * time.Millisecond}
	require.NoError(t, c.Validate())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	var report *PingReport
	out := captureStdoutFrom(t, func() error {
		var err error
		report, err = c.RunPing(ctx, u)
		return err // nil: ctx cancellation is swallowed
	})

	require.NotNil(t, report)
	assert.GreaterOrEqual(t, report.Received, 1, "at least one pong before cancel")
	assert.Contains(t, out, "STATS ")
}

func TestRunPingConnectionDeath(t *testing.T) {
	u := newPingServer(t, pingServerOpts{
		mode: pingServerCloseAfter, closeAfter: 60 * time.Millisecond,
	})
	c := &Client{count: 0, mode: ModePing, interval: 15 * time.Millisecond, timeout: time.Second}
	require.NoError(t, c.Validate())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var report *PingReport
	out := captureStdoutFrom(t, func() error {
		var err error
		report, err = c.RunPing(ctx, u)
		return err // nil: connection death is reported via the summary, not the error
	})

	require.NotNil(t, report)
	assert.GreaterOrEqual(t, report.Received, 1, "some pongs before the server closed")
	assert.Equal(t, report.Received+1, report.Sent, "the failing ping counts as sent, not received")
	assert.Contains(t, out, "lost: seq=")
	assert.Contains(t, out, "STATS ")
}

func TestRunPingZeroPongs(t *testing.T) {
	// A server that never answers times out every ping. With unbounded reads a timeout is
	// survivable, so the run does not end early: all -c pings fire, each a timeout, and the
	// summary reports total loss.
	u := newPingServer(t, pingServerOpts{mode: pingServerNoRead})
	c := &Client{
		count:      3,
		mode:       ModePing,
		interval:   10 * time.Millisecond,
		timeout:    120 * time.Millisecond,
		closeGrace: 100 * time.Millisecond,
	}
	require.NoError(t, c.Validate())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var report *PingReport
	out := captureStdoutFrom(t, func() error {
		var err error
		report, err = c.RunPing(ctx, u)
		return err
	})

	require.NotNil(t, report)
	assert.Equal(t, 3, report.Sent, "all pings fire; a timeout is survivable")
	assert.Equal(t, 0, report.Received)
	assert.InDelta(t, 100.0, report.LossPct(), 0.001)
	assert.Equal(t, 3, strings.Count(out, "timeout: seq="))
	assert.Contains(t, out, "3 sent, 0 received, 100.0% loss")
	assert.NotContains(t, out, "rtt:")
}

func TestRunPingTimeoutIsSurvivable(t *testing.T) {
	// The server ignores the first ping (it times out) then starts answering. With unbounded
	// reads the connection survives the timeout, so later pings pong: a run continues through
	// a transient loss rather than ending on it. This is the core of the fix.
	u := newPingServer(t, pingServerOpts{
		mode: pingServerDelayRead, delayRead: 90 * time.Millisecond,
	})
	c := &Client{
		count:    5,
		mode:     ModePing,
		interval: 50 * time.Millisecond,
		timeout:  60 * time.Millisecond,
	}
	require.NoError(t, c.Validate())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var report *PingReport
	out := captureStdoutFrom(t, func() error {
		var err error
		report, err = c.RunPing(ctx, u)
		return err
	})

	require.NotNil(t, report)
	assert.Equal(t, 5, report.Sent, "all pings fire; the run is not cut short by the timeout")
	assert.GreaterOrEqual(t, report.Received, 1, "connection survives the timeout and later pongs")
	assert.Contains(t, out, "timeout: seq=1", "the first ping times out")
	assert.Contains(t, out, "pong: seq=", "a later ping succeeds on the same connection")
}

func TestRunPingTinyIntervalStaysSequential(t *testing.T) {
	// A small interval must not pile up pings: PingPong is synchronous, so the run fires
	// exactly count sequential pings with monotonic seq (missed ticks are dropped).
	u := newPingServer(t, pingServerOpts{mode: pingServerAnswer})
	c := &Client{count: 5, mode: ModePing, interval: minPingInterval}
	require.NoError(t, c.Validate())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var report *PingReport
	out := captureStdoutFrom(t, func() error {
		var err error
		report, err = c.RunPing(ctx, u)
		return err
	})

	require.NotNil(t, report)
	assert.Equal(t, 5, report.Sent)
	assert.Equal(t, 5, report.Received)
	assert.Equal(t, 5, strings.Count(out, "pong: seq="))
}

func TestPingHeaderOutput(t *testing.T) {
	res := sampleTimingResult(t) // wss:// with dns/tcp/tls/ws timings
	u := res.URL

	t.Run("text includes dial phases", func(t *testing.T) {
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error { return c.printPingHeader(u, res) })
		assert.Contains(t, out, "PING "+u.String())
		assert.Contains(t, out, "dns 10ms")
		assert.Contains(t, out, "tcp 20ms")
		assert.Contains(t, out, "tls 30ms")
		assert.Contains(t, out, "ws 40ms")
	})

	t.Run("ws scheme omits tls segment", func(t *testing.T) {
		wsRes := sampleTimingResult(t)
		wsURL, err := url.Parse("ws://plain.test/ws")
		require.NoError(t, err)
		wsRes.URL = wsURL
		wsRes.TLSHandshake = 0
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error { return c.printPingHeader(wsURL, wsRes) })
		assert.NotContains(t, out, "tls ")
		assert.Contains(t, out, "ws 40ms")
	})

	t.Run("quiet suppresses header", func(t *testing.T) {
		c := &Client{output: OutputText, quiet: true}
		out := captureStdoutFrom(t, func() error { return c.printPingHeader(u, res) })
		assert.Empty(t, out)
	})

	t.Run("json suppresses header", func(t *testing.T) {
		c := &Client{output: OutputJSON}
		out := captureStdoutFrom(t, func() error { return c.printPingHeader(u, res) })
		assert.Empty(t, out)
	})
}

func TestPingReplyOutput(t *testing.T) {
	t.Run("text pong", func(t *testing.T) {
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error {
			return c.printPingReply(2, 12300*time.Microsecond, pingPong, "")
		})
		assert.Equal(t, "pong: seq=2 rtt=12.3ms\n", out)
	})

	t.Run("text timeout", func(t *testing.T) {
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error {
			return c.printPingReply(3, 0, pingTimeout, "no response within 5s")
		})
		assert.Equal(t, "timeout: seq=3 (5s)\n", out)
	})

	t.Run("text connection loss", func(t *testing.T) {
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error {
			return c.printPingReply(4, 0, pingDead, "connection closed")
		})
		assert.Equal(t, "lost: seq=4 connection closed\n", out)
	})

	t.Run("quiet suppresses text reply", func(t *testing.T) {
		c := &Client{output: OutputText, quiet: true}
		out := captureStdoutFrom(t, func() error {
			return c.printPingReply(1, time.Millisecond, pingPong, "")
		})
		assert.Empty(t, out)
	})

	t.Run("json pong record", func(t *testing.T) {
		c := &Client{output: OutputJSON}
		out := captureStdoutFrom(t, func() error {
			return c.printPingReply(4, 12300*time.Microsecond, pingPong, "")
		})
		p := decodeJSONLine(t, out)
		assert.Equal(t, "ping_reply", p["type"])
		assert.EqualValues(t, 4, p["seq"])
		assert.InDelta(t, 12.3, p["rtt_ms"], 0.001)
		_, hasLost := p["lost"]
		assert.False(t, hasLost, "pong record omits lost")
	})

	t.Run("json lost record", func(t *testing.T) {
		c := &Client{output: OutputJSON}
		out := captureStdoutFrom(t, func() error {
			return c.printPingReply(5, 0, pingTimeout, "no response within 5s")
		})
		p := decodeJSONLine(t, out)
		assert.Equal(t, "ping_reply", p["type"])
		assert.EqualValues(t, 5, p["seq"])
		assert.Equal(t, true, p["lost"])
		assert.Equal(t, "no response within 5s", p["error"])
		_, hasRTT := p["rtt_ms"]
		assert.False(t, hasRTT, "lost record omits rtt_ms")
	})
}

func TestPingSummaryOutput(t *testing.T) {
	u, err := url.Parse("wss://echo.test/ws")
	require.NoError(t, err)

	full := func() *PingReport {
		return &PingReport{
			Target: u, Sent: 4, Received: 3,
			Min:    11800 * time.Microsecond,
			Avg:    12100 * time.Microsecond,
			Max:    12300 * time.Microsecond,
			Stddev: 200 * time.Microsecond,
		}
	}

	t.Run("text with rtt line", func(t *testing.T) {
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error { return c.printPingSummary(full()) })
		assert.Contains(t, out, "STATS "+u.String()+" (4 sent, 3 received, 25.0% loss)")
		assert.Contains(t, out, "rtt: min=11.8ms avg=12.1ms max=12.3ms stddev=0.2ms")
	})

	t.Run("text omits rtt when zero received", func(t *testing.T) {
		c := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error {
			return c.printPingSummary(&PingReport{Target: u, Sent: 2, Received: 0})
		})
		assert.Contains(t, out, "2 sent, 0 received, 100.0% loss")
		assert.NotContains(t, out, "rtt:")
	})

	t.Run("json record", func(t *testing.T) {
		c := &Client{output: OutputJSON}
		out := captureStdoutFrom(t, func() error { return c.printPingSummary(full()) })
		p := decodeJSONLine(t, out)
		assert.Equal(t, "ping_summary", p["type"])
		assert.Equal(t, u.String(), p["url"])
		assert.EqualValues(t, 4, p["sent"])
		assert.EqualValues(t, 3, p["received"])
		assert.InDelta(t, 25.0, p["loss_pct"], 0.001)
		assert.InDelta(t, 11.8, p["min_ms"], 0.001)
		assert.InDelta(t, 0.2, p["stddev_ms"], 0.001)
	})

	t.Run("json omits rtt when zero received", func(t *testing.T) {
		c := &Client{output: OutputJSON}
		out := captureStdoutFrom(t, func() error {
			return c.printPingSummary(&PingReport{Target: u, Sent: 3, Received: 0})
		})
		p := decodeJSONLine(t, out)
		assert.EqualValues(t, 0, p["received"])
		assert.InDelta(t, 100.0, p["loss_pct"], 0.001)
		_, hasMin := p["min_ms"]
		assert.False(t, hasMin, "zero-received summary omits min_ms")
	})

	t.Run("json single sample keeps zero stddev", func(t *testing.T) {
		// A single pong has a legitimately-zero stddev; it must stay present (not dropped by
		// omitempty) so a received>0 summary always carries all four aggregates.
		report := &PingReport{
			Target: u, Sent: 1, Received: 1,
			Min: 5 * time.Millisecond, Avg: 5 * time.Millisecond,
			Max: 5 * time.Millisecond, Stddev: 0,
		}
		c := &Client{output: OutputJSON}
		out := captureStdoutFrom(t, func() error { return c.printPingSummary(report) })
		p := decodeJSONLine(t, out)
		v, ok := p["stddev_ms"]
		require.True(t, ok, "stddev_ms present for a received>0 summary even when zero")
		assert.InDelta(t, 0.0, v, 0.0001)
		assert.InDelta(t, 5.0, p["min_ms"], 0.001)
	})
}
