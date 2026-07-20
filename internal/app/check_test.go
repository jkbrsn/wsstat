package app

import (
	"context"
	"crypto/sha1" //nolint:gosec // RFC 6455 mandates SHA-1 for the handshake accept key
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jkbrsn/wsstat/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wsAcceptMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsAcceptKey computes the Sec-WebSocket-Accept value for a client key (RFC 6455 §1.3).
func wsAcceptKey(key string) string {
	h := sha1.New() //nolint:gosec // RFC 6455 handshake requires SHA-1
	_, _ = io.WriteString(h, key+wsAcceptMagic)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// rawUpgrade completes a 101 handshake by hand, adding extra response headers (a bogus
// subprotocol, extension, or version acceptance), and returns the hijacked connection so a
// test handler can deliberately misbehave in ways coder's Accept would never produce.
func rawUpgrade(w http.ResponseWriter, r *http.Request, extra map[string]string) (net.Conn, bool) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, false
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return nil, false
	}
	lines := []string{
		"HTTP/1.1 101 Switching Protocols",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: " + wsAcceptKey(r.Header.Get("Sec-WebSocket-Key")),
	}
	for k, v := range extra {
		lines = append(lines, k+": "+v)
	}
	_, _ = buf.WriteString(strings.Join(lines, "\r\n") + "\r\n\r\n")
	_ = buf.Flush()
	return conn, true
}

// coderEcho is a well-behaved WebSocket server: it echoes data frames, auto-answers pings and
// the closing handshake, selects the offered "wsstat-check" subprotocol, and negotiates
// permessage-deflate. It is the baseline every check-mode test builds on.
func coderEcho(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{checkSubprotocolName},
		CompressionMode:    websocket.CompressionContextTakeover,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(-1)
	for {
		typ, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if err := conn.Write(context.Background(), typ, data); err != nil {
			return
		}
	}
}

// newCheckServer starts an httptest server with the given handler and returns its ws:// URL.
func newCheckServer(t *testing.T, h http.HandlerFunc) *url.URL {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	u, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)
	return u
}

// newCheckClient builds a check-mode client with short timeouts so the raw-server tests that
// never echo a close frame do not stall on the default close grace.
func newCheckClient() *Client {
	return &Client{
		mode:       ModeCheck,
		timeout:    2 * time.Second,
		closeGrace: 200 * time.Millisecond,
		colorMode:  "never",
	}
}

// newCheckClientFast builds a check-mode client with short per-check and close-grace budgets so
// the tests that deliberately drive timeout paths (no pong, dropped connection) finish quickly.
func newCheckClientFast() *Client {
	return &Client{
		mode:       ModeCheck,
		timeout:    400 * time.Millisecond,
		closeGrace: 200 * time.Millisecond,
		colorMode:  "never",
	}
}

// entryByID returns the report entry with the given ID, failing the test if absent.
func entryByID(t *testing.T, r *CheckReport, id string) CheckEntry {
	t.Helper()
	for _, e := range r.Entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no check entry with id %q", id)
	return CheckEntry{}
}

func TestRunCheck(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		id      string      // check to assert (empty means assert whole report)
		want    CheckStatus // expected status for id
		assert  func(*testing.T, *CheckReport)
	}{
		{
			name:    "well-behaved server passes every check",
			handler: coderEcho,
			assert: func(t *testing.T, r *CheckReport) {
				for _, e := range r.Entries {
					assert.Equalf(t, CheckPass, e.Status, "check %s: %s", e.ID, e.Detail)
				}
				assert.Equal(t, len(checkOrder), r.Passed())
			},
		},
		{
			name:    "unoffered subprotocol fails subprotocol-echo",
			handler: unofferedSubprotocolHandler,
			id:      checkSubprotoEcho,
			want:    CheckFail,
		},
		{
			name:    "malformed deflate params warn",
			handler: malformedDeflateHandler,
			id:      checkDeflate,
			want:    CheckWarn,
		},
		{
			name:    "101 to version 99 fails version-reject",
			handler: acceptBadVersionHandler,
			id:      checkVersionReject,
			want:    CheckFail,
		},
		{
			name:    "close without echo warns close-echo",
			handler: noCloseEchoHandler,
			id:      checkCloseEcho,
			want:    CheckWarn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := newCheckServer(t, tc.handler)
			report, err := newCheckClient().RunCheck(context.Background(), target)
			require.NoError(t, err)
			require.Len(t, report.Entries, len(checkOrder))
			if tc.assert != nil {
				tc.assert(t, report)
				return
			}
			assert.Equalf(t, tc.want, entryByID(t, report, tc.id).Status,
				"detail: %s", entryByID(t, report, tc.id).Detail)
		})
	}
}

// unofferedSubprotocolHandler echoes normally, but when a subprotocol is offered it returns one
// that was not offered, which coder's client rejects during the dial.
func unofferedSubprotocolHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-WebSocket-Protocol") == "" {
		coderEcho(w, r)
		return
	}
	if conn, ok := rawUpgrade(w, r, map[string]string{
		"Sec-WebSocket-Protocol": "not-offered-proto",
	}); ok {
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
}

// malformedDeflateHandler echoes normally, but when permessage-deflate is offered it responds
// with an out-of-range server_max_window_bits that coder accepts but RFC 7692 forbids.
func malformedDeflateHandler(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		coderEcho(w, r)
		return
	}
	if conn, ok := rawUpgrade(w, r, map[string]string{
		"Sec-WebSocket-Extensions": "permessage-deflate; server_max_window_bits=99",
	}); ok {
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
}

// acceptBadVersionHandler echoes normally, but answers 101 to the unsupported version probe
// instead of rejecting it.
func acceptBadVersionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-WebSocket-Version") != "99" {
		coderEcho(w, r)
		return
	}
	if conn, ok := rawUpgrade(w, r, nil); ok {
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
}

// noCloseEchoHandler completes the handshake but never answers the closing handshake: it reads
// and discards frames (never echoing a close) until the client goes away or a deadline fires.
func noCloseEchoHandler(w http.ResponseWriter, r *http.Request) {
	conn, ok := rawUpgrade(w, r, nil)
	if !ok {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
}

// TestRunCheckUnreachable asserts a transport-level dial failure (nothing listening) is a
// runtime error, not an RFC 6455 verdict: an outage or a typo must not trip the exit-3 gate.
func TestRunCheckUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(coderEcho))
	target, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)
	server.Close() // nothing listens on this port anymore

	report, err := newCheckClient().RunCheck(context.Background(), target)
	require.Error(t, err)
	assert.Nil(t, report)
	assert.Contains(t, err.Error(), "dialing")
}

// TestRunCheckUpgradeRefused asserts a server that answered but refused the upgrade is an RFC
// verdict: the handshake fails, every dependent check skips, and a full report is returned.
func TestRunCheckUpgradeRefused(t *testing.T) {
	target := newCheckServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no websockets here", http.StatusForbidden)
	})

	report, err := newCheckClient().RunCheck(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, report.Entries, len(checkOrder))

	assert.Equal(t, CheckFail, entryByID(t, report, checkUpgrade).Status)
	for _, e := range report.Entries {
		if e.ID == checkUpgrade {
			continue
		}
		assert.Equalf(t, CheckSkip, e.Status, "check %s should be skipped", e.ID)
	}
	assert.Equal(t, 1, report.Failed())
	assert.Equal(t, len(checkOrder)-1, report.Skipped())
}

// silentHandler completes the handshake by hand but never answers pings, echoes, or the closing
// handshake: it discards every client frame until a deadline fires. It drives the no-pong fail
// path (behavior.ping-pong) and, on the fragmentation connection, the dropped-after-fragments
// fail path.
func silentHandler(w http.ResponseWriter, r *http.Request) {
	conn, ok := rawUpgrade(w, r, nil)
	if !ok {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
}

// TestRunCheckNoPong drives the two MUST-level behavior fail paths that a healthy suite never
// exercises: a server that never pongs fails behavior.ping-pong, and one that swallows a
// fragmented message fails behavior.fragmentation.
func TestRunCheckNoPong(t *testing.T) {
	target := newCheckServer(t, silentHandler)
	report, err := newCheckClientFast().RunCheck(context.Background(), target)
	require.NoError(t, err)

	assert.Equal(t, CheckFail, entryByID(t, report, checkPingPong).Status)
	assert.Equal(t, CheckFail, entryByID(t, report, checkFragmentation).Status)
}

// noDeflateHandler is a well-behaved echo server with compression disabled, so the deflate
// connection sees permessage-deflate simply not negotiated (the ext=="" pass branch).
func noDeflateHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{checkSubprotocolName},
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(-1)
	for {
		typ, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if err := conn.Write(context.Background(), typ, data); err != nil {
			return
		}
	}
}

// rejectDeflateHandler rejects the handshake only when permessage-deflate is offered, driving the
// negotiation-failed warn branch; every other connection is a well-behaved echo.
func rejectDeflateHandler(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	coderEcho(w, r)
}

// TestRunCheckDeflateBranches covers the deflate outcomes the malformed-params test misses: the
// not-negotiated pass and the negotiation-failed warn.
func TestRunCheckDeflateBranches(t *testing.T) {
	t.Run("not negotiated passes", func(t *testing.T) {
		target := newCheckServer(t, noDeflateHandler)
		report, err := newCheckClient().RunCheck(context.Background(), target)
		require.NoError(t, err)
		e := entryByID(t, report, checkDeflate)
		assert.Equal(t, CheckPass, e.Status)
		assert.Contains(t, e.Detail, "not negotiated")
	})

	t.Run("rejected negotiation warns", func(t *testing.T) {
		target := newCheckServer(t, rejectDeflateHandler)
		report, err := newCheckClient().RunCheck(context.Background(), target)
		require.NoError(t, err)
		assert.Equal(t, CheckWarn, entryByID(t, report, checkDeflate).Status)
	})
}

// versionNoHeaderHandler rejects the version-99 probe with 426 but omits the advertised
// Sec-WebSocket-Version header, driving the version-reject warn branch.
func versionNoHeaderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-WebSocket-Version") == "99" {
		w.WriteHeader(http.StatusUpgradeRequired)
		return
	}
	coderEcho(w, r)
}

// versionProbeErrorHandler abruptly closes the version-99 probe's TCP connection with no
// response, driving the version-reject "probe failed" warn branch.
func versionProbeErrorHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-WebSocket-Version") == "99" {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	coderEcho(w, r)
}

// TestRunCheckVersionBranches covers the two version-reject warn branches the accept-101 fail
// test misses: a rejection missing the advertised-version header, and a probe transport error.
func TestRunCheckVersionBranches(t *testing.T) {
	t.Run("rejected without version header warns", func(t *testing.T) {
		target := newCheckServer(t, versionNoHeaderHandler)
		report, err := newCheckClient().RunCheck(context.Background(), target)
		require.NoError(t, err)
		assert.Equal(t, CheckWarn, entryByID(t, report, checkVersionReject).Status)
	})

	t.Run("probe transport error warns", func(t *testing.T) {
		target := newCheckServer(t, versionProbeErrorHandler)
		report, err := newCheckClient().RunCheck(context.Background(), target)
		require.NoError(t, err)
		e := entryByID(t, report, checkVersionReject)
		assert.Equal(t, CheckWarn, e.Status)
		assert.Contains(t, e.Detail, "probe failed")
	})
}

// readWSFrame reads and discards one WebSocket frame from a hijacked connection, returning its
// opcode. It handles the masked, small frames the check client sends.
func readWSFrame(conn net.Conn) (byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return 0, err
	}
	opcode := hdr[0] & 0x0f
	length := int(hdr[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(conn, ext); err != nil {
			return 0, err
		}
		length = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(conn, ext); err != nil {
			return 0, err
		}
		length = int(binary.BigEndian.Uint64(ext))
	default:
		// 7-bit length already read from hdr[1].
	}
	if hdr[1]&0x80 != 0 {
		var mask [4]byte
		if _, err := io.ReadFull(conn, mask[:]); err != nil {
			return 0, err
		}
	}
	if length > 0 {
		if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
			return 0, err
		}
	}
	return opcode, nil
}

// closeWrongCodeHandler completes the closing handshake but echoes status 1001 (going away)
// instead of mirroring the client's 1000, driving the "valid registered code != 1000" warn
// branch that a mirroring echo server can never reach.
func closeWrongCodeHandler(w http.ResponseWriter, r *http.Request) {
	conn, ok := rawUpgrade(w, r, nil)
	if !ok {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	for {
		op, err := readWSFrame(conn)
		if err != nil {
			return
		}
		if op == 0x8 { // client close frame
			break
		}
	}
	// Close frame (FIN|opcode 0x8), 2-byte payload carrying status 1001 (0x03E9), unmasked.
	_, _ = conn.Write([]byte{0x88, 0x02, 0x03, 0xe9})
}

// TestRunCheckCloseBranches covers the close-echo outcomes the mirroring echo server cannot
// reach: a non-1000 registered echo warns, and a fresh-connection dial failure skips.
func TestRunCheckCloseBranches(t *testing.T) {
	t.Run("non-1000 registered code warns", func(t *testing.T) {
		target := newCheckServer(t, closeWrongCodeHandler)
		report, err := newCheckClientFast().RunCheck(context.Background(), target)
		require.NoError(t, err)
		e := entryByID(t, report, checkCloseEcho)
		assert.Equal(t, CheckWarn, e.Status)
		assert.Contains(t, e.Detail, "1001")
	})

	t.Run("dial failure skips", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(coderEcho))
		u, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
		require.NoError(t, err)
		server.Close() // nothing listens now
		b := newCheckBuilder(u)
		newCheckClient().checkCloseHandshake(context.Background(), u, nil, b)
		assert.Equal(t, CheckSkip, b.entries[checkCloseEcho].Status)
	})
}

// TestSubprotocolEchoDialBranches covers the subprotocol-echo dial-failure classification: a
// transport failure on the fresh connection is a prerequisite miss (skip, as in the
// fragmentation and close-echo checks), and a server that answers but refuses the upgrade when
// a subprotocol is offered is a warn — RFC 6455 §4.2.2 only constrains the value a server may
// select when it completes the handshake.
func TestSubprotocolEchoDialBranches(t *testing.T) {
	t.Run("transport failure skips", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(coderEcho))
		u, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
		require.NoError(t, err)
		server.Close() // nothing listens now
		b := newCheckBuilder(u)
		newCheckClient().checkSubprotocolEcho(context.Background(), u, nil, b)
		assert.Equal(t, CheckSkip, b.entries[checkSubprotoEcho].Status)
	})

	t.Run("refused upgrade warns", func(t *testing.T) {
		target := newCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Sec-WebSocket-Protocol") != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			coderEcho(w, r)
		})
		b := newCheckBuilder(target)
		newCheckClient().checkSubprotocolEcho(context.Background(), target, nil, b)
		e := b.entries[checkSubprotoEcho]
		assert.Equal(t, CheckWarn, e.Status)
		assert.Contains(t, e.Detail, "handshake rejected")
	})
}

// TestFragmentationDialFailureSkips exercises checkFragmentationTolerance's own dial-failure
// branch directly: through RunCheck a failed primary handshake short-circuits before this
// check ever dials, so the branch is unreachable end-to-end.
func TestFragmentationDialFailureSkips(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(coderEcho))
	u, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)
	server.Close() // nothing listens now
	b := newCheckBuilder(u)
	newCheckClient().checkFragmentationTolerance(context.Background(), u, nil, b)
	e := b.entries[checkFragmentation]
	assert.Equal(t, CheckSkip, e.Status)
	assert.Contains(t, e.Detail, "handshake failed")
}

// TestRecordHeaderTokens exercises recordHeaderTokens directly: coder's client always yields
// well-formed handshake response headers, so both warn branches and the nil-headers guard are
// unreachable through a real dial.
func TestRecordHeaderTokens(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   CheckStatus
	}{
		{"both tokens present", http.Header{
			"Upgrade": {"websocket"}, "Connection": {"Upgrade"}}, CheckPass},
		{"upgrade missing token", http.Header{
			"Upgrade": {"h2c"}, "Connection": {"Upgrade"}}, CheckWarn},
		{"connection missing token", http.Header{
			"Upgrade": {"websocket"}, "Connection": {"keep-alive"}}, CheckWarn},
		{"nil headers", nil, CheckWarn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newCheckBuilder(nil)
			recordHeaderTokens(&wsstat.Result{ResponseHeaders: tc.header}, b)
			assert.Equal(t, tc.want, b.entries[checkHeaders].Status)
		})
	}
}

// TestRecordSubprotocolNone exercises recordSubprotocolNone directly: coder rejects an
// unoffered subprotocol during the dial, so its fail branch is unreachable end-to-end.
func TestRecordSubprotocolNone(t *testing.T) {
	t.Run("none selected passes", func(t *testing.T) {
		b := newCheckBuilder(nil)
		recordSubprotocolNone(&wsstat.Result{Subprotocol: ""}, b)
		assert.Equal(t, CheckPass, b.entries[checkSubprotoNone].Status)
	})
	t.Run("unsolicited selection fails", func(t *testing.T) {
		b := newCheckBuilder(nil)
		recordSubprotocolNone(&wsstat.Result{Subprotocol: "chat"}, b)
		assert.Equal(t, CheckFail, b.entries[checkSubprotoNone].Status)
	})
}

// TestRunCheckCanceled asserts a run whose context is already canceled fabricates no failing
// verdicts (which would trip the exit-3 CI gate) and records every check as skipped instead.
func TestRunCheckCanceled(t *testing.T) {
	target := newCheckServer(t, coderEcho)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := newCheckClient().RunCheck(ctx, target)
	require.NoError(t, err)
	require.Len(t, report.Entries, len(checkOrder))
	assert.Equal(t, 0, report.Failed(), "a canceled run must fabricate no failures")
	assert.Equal(t, len(checkOrder), report.Skipped(), "every check must be skipped")
}

// TestCheckTimeout pins the per-check timeout resolution: an unset --timeout uses the check
// default, a set one is used as-is.
func TestCheckTimeout(t *testing.T) {
	assert.Equal(t, checkDefaultTimeout, (&Client{}).checkTimeout())
	assert.Equal(t, 2*time.Second, (&Client{timeout: 2 * time.Second}).checkTimeout())
}

// TestCheckCancellationSkips asserts each post-handshake check records skip (not a fabricated
// fail/warn verdict) when the run context is canceled before it runs.
func TestCheckCancellationSkips(t *testing.T) {
	target := newCheckServer(t, coderEcho)
	header, err := parseHeaders(nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newCheckClient()
	cases := []struct {
		id  string
		run func(context.Context, *url.URL, http.Header, *checkBuilder)
	}{
		{checkSubprotoEcho, c.checkSubprotocolEcho},
		{checkDeflate, c.checkDeflateExtension},
		{checkVersionReject, c.checkVersionRejection},
		{checkFragmentation, c.checkFragmentationTolerance},
		{checkCloseEcho, c.checkCloseHandshake},
	}
	for _, tc := range cases {
		b := newCheckBuilder(target)
		tc.run(ctx, target, header, b)
		assert.Equalf(t, CheckSkip, b.entries[tc.id].Status,
			"check %s must skip on cancel, got %s", tc.id, b.entries[tc.id].Detail)
	}
}

func TestValidateDeflate(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		ok   bool
	}{
		{"plain", "permessage-deflate", true},
		{"valid params", "permessage-deflate; client_no_context_takeover", true},
		{"valid window bits", "permessage-deflate; server_max_window_bits=15", true},
		{"quoted window bits", `permessage-deflate; server_max_window_bits="10"`, true},
		{"missing window bits value", "permessage-deflate; client_max_window_bits", false},
		{"low window bits", "permessage-deflate; server_max_window_bits=7", false},
		{"high window bits", "permessage-deflate; server_max_window_bits=99", false},
		{"non-numeric window bits", "permessage-deflate; server_max_window_bits=x", false},
		{"unknown param", "permessage-deflate; frobnicate", false},
		{"duplicate param", "permessage-deflate; " +
			"server_no_context_takeover; server_no_context_takeover", false},
		{"wrong extension", "x-webkit-deflate-frame", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := validateDeflate(tc.ext)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestHasToken(t *testing.T) {
	assert.True(t, hasToken("Upgrade", "upgrade"))
	assert.True(t, hasToken("keep-alive, Upgrade", "upgrade"))
	assert.True(t, hasToken("websocket", "WebSocket"))
	assert.False(t, hasToken("keep-alive", "upgrade"))
	assert.False(t, hasToken("", "upgrade"))
}

// TestValidateCheck exercises the ModeCheck branch: it rejects every measure/stream/ping-only
// knob and raw output.
func TestValidateCheck(t *testing.T) {
	t.Run("bare check mode is valid", func(t *testing.T) {
		c := &Client{mode: ModeCheck}
		require.NoError(t, c.Validate())
	})

	rejections := []struct {
		name   string
		client Client
	}{
		{"raw output", Client{mode: ModeCheck, output: "raw"}},
		{"file sink", Client{mode: ModeCheck, responseFilePath: "cap.ndjson"}},
		{"text", Client{mode: ModeCheck, textMessages: []string{"hi"}}},
		{"rpc method", Client{mode: ModeCheck, rpcMethod: "eth_x"}},
		{"once", Client{mode: ModeCheck, once: true}},
		{"buffer", Client{mode: ModeCheck, buffer: 8}},
		{"summary interval", Client{mode: ModeCheck, summaryInterval: time.Second}},
		{"send delay", Client{mode: ModeCheck, sendDelay: time.Second}},
		{"interval", Client{mode: ModeCheck, interval: time.Second}},
		{"body compact", Client{mode: ModeCheck, body: BodyCompact}},
		{"clip", Client{mode: ModeCheck, clip: true}},
	}
	for _, tc := range rejections {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			assert.Error(t, tc.client.Validate())
		})
	}
}
