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
	"github.com/jkbrsn/wsstat/v3/internal/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkEchoServer starts a well-behaved WebSocket echo server that passes every Tier 1 check.
// A non-nil version99 handler overrides the response to the unsupported-version probe
// (Sec-WebSocket-Version: 99), so a test can drive the version-reject fail or warn branches
// while every coder-dialed check still passes. Torn down via t.Cleanup.
func checkEchoServer(t *testing.T, version99 http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version99 != nil && r.Header.Get("Sec-WebSocket-Version") == "99" {
			version99(w, r)
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
	defer func() {
		os.Stdout = old // restore even if runCheck panics
		_ = w.Close()
		_ = r.Close()
	}()
	runErr := runCheck(args)
	require.NoError(t, w.Close())
	os.Stdout = old
	_, _ = io.Copy(io.Discard, r)

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
		url := checkEchoServer(t, nil)
		assert.Equal(t, 0, runCheckExitCode(t, []string{"--timeout", "2s", url}))
	})

	t.Run("a failing check exits 3", func(t *testing.T) {
		url := checkEchoServer(t, func(w http.ResponseWriter, _ *http.Request) {
			hijack101(t, w) // accepting version 99 fails negotiation.version-reject
		})
		assert.Equal(t, exitCheckFailed, runCheckExitCode(t, []string{"--timeout", "2s", url}))
	})

	t.Run("warnings alone exit 0", func(t *testing.T) {
		url := checkEchoServer(t, func(w http.ResponseWriter, _ *http.Request) {
			// Reject without advertising a supported version: warns version-reject.
			w.WriteHeader(http.StatusUpgradeRequired)
		})
		assert.Equal(t, 0, runCheckExitCode(t, []string{"--timeout", "2s", url}))
	})

	t.Run("an unreachable endpoint exits 1", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		srv.Close() // nothing listens now
		assert.Equal(t, exitRuntime, runCheckExitCode(t, []string{"--timeout", "2s", url}))
	})
}

// TestCheckRunOutcome pins the post-run exit mapping: a fail verdict wins exit 3 even on an
// interrupted run, an interrupt without a fail is a runtime error (exit 1, never a fabricated
// pass), and a clean run maps to nil.
func TestCheckRunOutcome(t *testing.T) {
	live := context.Background()
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	failed := &app.CheckReport{Entries: []app.CheckEntry{{ID: "x", Status: app.CheckFail}}}
	skipped := &app.CheckReport{Entries: []app.CheckEntry{{ID: "x", Status: app.CheckSkip}}}
	clean := &app.CheckReport{Entries: []app.CheckEntry{{ID: "x", Status: app.CheckPass}}}

	assert.NoError(t, checkRunOutcome(live, app.OutputText, clean))
	assert.ErrorIs(t, checkRunOutcome(live, app.OutputText, failed), errCheckFailed)
	assert.ErrorIs(t, checkRunOutcome(interrupted, app.OutputText, failed), errCheckFailed,
		"an observed fail verdict must win over the interrupt")

	err := checkRunOutcome(interrupted, app.OutputText, skipped)
	require.Error(t, err, "an interrupted run must not exit 0")
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, exitRuntime, ce.code)
	assert.Contains(t, err.Error(), "interrupted")
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
		assert.Contains(t, err.Error(), "not supported in check mode")
	})

	t.Run("stdin payload rejected without reading stdin", func(t *testing.T) {
		// -t @- must be rejected at the flag layer; reaching resolveCommon would block on
		// io.ReadAll(os.Stdin).
		_, _, err := buildCheck([]string{"-t", "@-", "wss://example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not supported in check mode")
	})
}
