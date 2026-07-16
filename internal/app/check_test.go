package app

import (
	"context"
	"crypto/sha1" //nolint:gosec // RFC 6455 mandates SHA-1 for the handshake accept key
	"encoding/base64"
	"io"
	"net"
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
		id      string           // check to assert (empty means assert whole report)
		want    CheckStatus      // expected status for id
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

// TestRunCheckUnreachable asserts a dial failure fails the handshake, skips every dependent
// check, and still returns a full report.
func TestRunCheckUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(coderEcho))
	target, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))
	require.NoError(t, err)
	server.Close() // nothing listens on this port anymore

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

func TestValidateDeflate(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		ok   bool
	}{
		{"plain", "permessage-deflate", true},
		{"valid params", "permessage-deflate; client_no_context_takeover", true},
		{"valid window bits", "permessage-deflate; server_max_window_bits=15", true},
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
	}
	for _, tc := range rejections {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			assert.Error(t, tc.client.Validate())
		})
	}
}
