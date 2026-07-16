package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkEchoServer starts a well-behaved WebSocket echo server that passes every Tier 1 check.
// When failVersion is true it answers the unsupported-version probe (Sec-WebSocket-Version: 99)
// with a 101, a conformance violation that fails negotiation.version-reject while every
// coder-dialed check still passes. Torn down via t.Cleanup.
//
//revive:disable-next-line:flag-parameter test-server behavior toggle
func checkEchoServer(t *testing.T, failVersion bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failVersion && r.Header.Get("Sec-WebSocket-Version") == "99" {
			hijack101(t, w)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       []string{"wsstat-check"},
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
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// hijack101 answers a request with a bare 101 by hand, bypassing coder's version validation.
func hijack101(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	require.True(t, ok, "test server must support hijacking")
	conn, buf, err := hj.Hijack()
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	_ = buf.Flush()
}

// runCheckExitCode runs runCheck with stdout suppressed and maps its error to a process exit
// code, mirroring main's dispatch (errCheckFailed -> exitCheckFailed).
func runCheckExitCode(t *testing.T, args []string) int {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	runErr := runCheck(args)
	require.NoError(t, w.Close())
	os.Stdout = old
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()

	if runErr == nil {
		return 0
	}
	if errors.Is(runErr, errCheckFailed) {
		return exitCheckFailed
	}
	var ce *cliError
	if errors.As(runErr, &ce) {
		return ce.code
	}
	return exitRuntime
}

// TestRunCheckExitCodes exercises the check-mode exit-code contract end-to-end against a live
// server: a conformant endpoint exits 0, a conformance violation exits 3.
func TestRunCheckExitCodes(t *testing.T) {
	t.Run("all pass exits 0", func(t *testing.T) {
		url := checkEchoServer(t, false)
		assert.Equal(t, 0, runCheckExitCode(t, []string{"--timeout", "2s", url}))
	})

	t.Run("a failing check exits 3", func(t *testing.T) {
		url := checkEchoServer(t, true)
		assert.Equal(t, exitCheckFailed, runCheckExitCode(t, []string{"--timeout", "2s", url}))
	})
}

// TestBuildCheckRejectsUnsupportedFlags verifies check-mode validation maps stream/measure-only
// knobs to exit 2 while a plain invocation parses.
func TestBuildCheckRejectsUnsupportedFlags(t *testing.T) {
	t.Run("valid check parses", func(t *testing.T) {
		client, target, err := buildCheck([]string{"wss://example.com"})
		require.NoError(t, err)
		require.NotNil(t, client)
		require.NotNil(t, target)
	})

	t.Run("raw output rejected", func(t *testing.T) {
		_, _, err := buildCheck([]string{"-o", "raw", "wss://example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "raw")
	})

	t.Run("text message rejected", func(t *testing.T) {
		_, _, err := buildCheck([]string{"-t", "hi", "wss://example.com"})
		require.Error(t, err)
	})
}
