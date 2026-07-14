package main

import (
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jkbrsn/wsstat/v3/internal/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revive:disable:line-length-limit table-driven test rows

// buildDispatch mirrors main's args[0] dispatch so removed-flag detection is
// exercised on the same FlagSet the real run path uses.
func buildDispatch(args []string) error {
	var err error
	if len(args) > 0 && args[0] == "stream" {
		_, _, err = buildStream(args[1:])
	} else {
		_, _, err = buildMeasure(args)
	}
	return err
}

func TestRemovedFlagsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
		hint    string
	}{
		{name: "subscribe", args: []string{"-subscribe", "example.com"}, wantErr: true, hint: "stream"},
		{name: "subscribe long form", args: []string{"--subscribe", "example.com"}, wantErr: true, hint: "stream"},
		{name: "subscribe short", args: []string{"-s", "example.com"}, wantErr: true, hint: "stream"},
		{name: "subscribe-once", args: []string{"-subscribe-once", "example.com"}, wantErr: true, hint: "stream --once"},
		{name: "format", args: []string{"-format", "json", "example.com"}, wantErr: true, hint: "-o"},
		{name: "format with equals", args: []string{"--format=json", "example.com"}, wantErr: true, hint: "-o"},
		{name: "no-tls", args: []string{"-no-tls", "example.com"}, wantErr: true, hint: "ws://"},
		// A removed-flag name passed as a flag *value* must not be misread as the flag.
		{name: "removed name as text value ok", args: []string{"-t", "-s", "example.com"}, wantErr: false},
		{name: "format name as text value ok", args: []string{"--text", "-format", "example.com"}, wantErr: false},
		{name: "current flags ok", args: []string{"-o", "json", "example.com"}, wantErr: false},
		// -f was reclaimed as the short form of --file in v3.2.
		{name: "file short alias ok", args: []string{"-f", "out.ndjson", "example.com"}, wantErr: false},
		{name: "stream subcommand ok", args: []string{"stream", "--once", "example.com"}, wantErr: false},
		{name: "bare url ok", args: []string{"example.com"}, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := buildDispatch(tt.args)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "removed in v3")
			assert.Contains(t, err.Error(), tt.hint)
		})
	}
}

// TestMeasureStreamRejectPingFlags pins that ping-only flags stay unknown outside ping
// mode: a refactor that re-broadens flag registration must not let -w/--deadline parse in
// measure or stream.
func TestMeasureStreamRejectPingFlags(t *testing.T) {
	t.Parallel()

	for _, flagArg := range []string{"-w", "--deadline"} {
		_, _, err := buildMeasure([]string{flagArg, "10s", "wss://example.com"})
		assert.ErrorIs(t, err, errUsageShown, "measure must reject %s", flagArg)
		_, _, err = buildStream([]string{flagArg, "10s", "wss://example.com"})
		assert.ErrorIs(t, err, errUsageShown, "stream must reject %s", flagArg)
	}
}

// TestErrorClassification verifies the exit-code contract: flag-parse sentinels pass
// through untouched, post-parse validation maps to exit 2, and runtime failures map to
// exit 1 carrying the output contract for the JSON envelope.
func TestErrorClassification(t *testing.T) {
	t.Parallel()

	t.Run("help passes through", func(t *testing.T) {
		assert.ErrorIs(t, usageErr(flag.ErrHelp), flag.ErrHelp)
	})

	t.Run("usage-shown passes through", func(t *testing.T) {
		assert.ErrorIs(t, usageErr(errUsageShown), errUsageShown)
	})

	t.Run("version-shown passes through", func(t *testing.T) {
		assert.ErrorIs(t, usageErr(errVersionShown), errVersionShown)
	})

	t.Run("validation becomes exit 2", func(t *testing.T) {
		var ce *cliError
		require.ErrorAs(t, usageErr(errors.New("count must be greater than 0")), &ce)
		assert.Equal(t, exitUsage, ce.code)
		assert.Empty(t, string(ce.output), "usage errors do not carry an output contract")
	})

	t.Run("runtime becomes exit 1 with output", func(t *testing.T) {
		var ce *cliError
		require.ErrorAs(t, runtimeErr(app.OutputJSON, errors.New("dial refused")), &ce)
		assert.Equal(t, exitRuntime, ce.code)
		assert.Equal(t, app.OutputJSON, ce.output)
	})

	t.Run("runtime nil stays nil", func(t *testing.T) {
		assert.NoError(t, runtimeErr(app.OutputText, nil))
	})

	t.Run("validation from buildMeasure classifies as exit 2", func(t *testing.T) {
		_, _, err := buildMeasure([]string{"-c", "0", "example.com"})
		require.Error(t, err)
		var ce *cliError
		require.ErrorAs(t, usageErr(err), &ce)
		assert.Equal(t, exitUsage, ce.code)
	})
}

// TestDispatchRouting verifies the build paths reached by each dispatch branch.
// The os.Exit branches (no-args, --version, help) are exercised by the binary, not here.
func TestDispatchRouting(t *testing.T) {
	t.Parallel()

	t.Run("bare form parses as measure", func(t *testing.T) {
		client, target, err := buildMeasure([]string{"wss://example.com"})
		require.NoError(t, err)
		assert.Equal(t, "wss://example.com", target.String())
		assert.Equal(t, 1, client.Count())
	})

	t.Run("stream subcommand args parse", func(t *testing.T) {
		client, target, err := buildStream([]string{"--once", "wss://example.com"})
		require.NoError(t, err)
		assert.Equal(t, "wss://example.com", target.String())
		assert.True(t, client.Once())
	})
}

// pingTestServer starts a WebSocket server and returns its ws:// URL. When answer is true it
// reads in the background, so coder auto-answers pings; when false it never reads, so pings go
// unanswered and time out client-side. Torn down via t.Cleanup.
//
//revive:disable-next-line:flag-parameter test-server behavior toggle
func pingTestServer(t *testing.T, answer bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if answer {
			ctx := conn.CloseRead(r.Context())
			<-ctx.Done()
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// runPingExitCode runs runPing with stdout suppressed and maps its error to a process exit code.
func runPingExitCode(t *testing.T, args []string) int {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	runErr := runPing(args)
	require.NoError(t, w.Close())
	os.Stdout = old
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()

	if runErr == nil {
		return 0
	}
	if errors.Is(runErr, errPingTotalLoss) {
		return exitRuntime // main exits exitRuntime on this sentinel without extra output
	}
	var ce *cliError
	if errors.As(runErr, &ce) {
		return ce.code
	}
	return exitRuntime
}

// TestRunPingExitCodes exercises the runtime exit-code contract end-to-end against a live
// WebSocket server: a pong yields exit 0, total loss yields exit 1, and a deadline terminates
// the run on its own with exit 0.
func TestRunPingExitCodes(t *testing.T) {
	t.Run("count reached exits 0", func(t *testing.T) {
		url := pingTestServer(t, true)
		assert.Equal(t, 0, runPingExitCode(t, []string{"-c", "2", "-i", "20ms", url}))
	})

	t.Run("total loss exits 1", func(t *testing.T) {
		url := pingTestServer(t, false)
		code := runPingExitCode(t, []string{
			"-c", "2", "-i", "20ms", "--timeout", "150ms", "--close-timeout", "150ms", url,
		})
		assert.Equal(t, exitRuntime, code)
	})

	t.Run("deadline self-terminates exit 0", func(t *testing.T) {
		url := pingTestServer(t, true)
		assert.Equal(t, 0, runPingExitCode(t, []string{"-w", "300ms", "-i", "100ms", url}))
	})
}

// TestBuildPingUsageErrors verifies ping-mode usage rejections map to exit 2 and a valid
// invocation parses cleanly.
func TestBuildPingUsageErrors(t *testing.T) {
	t.Parallel()

	usageCases := []struct {
		name string
		args []string
	}{
		{"text rejected", []string{"-t", "hi", "wss://example.com"}},
		{"empty text rejected", []string{"-t", "", "wss://example.com"}},
		{"rpc-method rejected", []string{"--rpc-method", "eth_x", "wss://example.com"}},
		{"empty rpc-method rejected", []string{"--rpc-method", "", "wss://example.com"}},
		{"rpc-version rejected", []string{"--rpc-version", "1.0", "wss://example.com"}},
		{"file rejected", []string{"--file", "cap.ndjson", "wss://example.com"}},
		{"body rejected", []string{"--body", "compact", "wss://example.com"}},
		{"clip rejected", []string{"--clip", "wss://example.com"}},
		{"raw output rejected", []string{"-o", "raw", "wss://example.com"}},
		{"zero deadline rejected", []string{"-w", "0s", "wss://example.com"}},
		{"interval below floor rejected", []string{"-i", "1ms", "wss://example.com"}},
	}
	for _, tc := range usageCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPing(tc.args)
			var ce *cliError
			require.ErrorAs(t, usageErr(err), &ce)
			assert.Equal(t, exitUsage, ce.code)
		})
	}

	t.Run("valid ping parses", func(t *testing.T) {
		cfg, err := buildPing([]string{"-c", "3", "-i", "500ms", "wss://example.com"})
		require.NoError(t, err)
		assert.Equal(t, "wss://example.com", cfg.target.String())
		assert.Equal(t, 3, cfg.client.Count())
		assert.Equal(t, 500*time.Millisecond, cfg.client.Interval())
		assert.Equal(t, time.Duration(0), cfg.deadline)
	})

	t.Run("unsupported flag error names the mode", func(t *testing.T) {
		_, err := buildPing([]string{"-t", "hi", "wss://example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "-t is not supported in ping mode")
	})

	t.Run("unsupported stub consumes its value", func(t *testing.T) {
		// The value token must be swallowed by the stub, not misread as the URL:
		// the error is the targeted rejection, not a positional-argument error.
		_, err := buildPing([]string{"--file", "wss://other.example.com", "wss://example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--file is not supported in ping mode")
	})
}

// TestPingUsageOmitsUnsupportedFlags pins the registration/usage invariant: every flag
// ping rejects is both stub-registered (so buildPing errors) and absent from its help.
func TestPingUsageOmitsUnsupportedFlags(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	printPingUsage(&buf)
	help := buf.String()

	for name, u := range pingUnsupported {
		if len(name) > 1 {
			assert.NotContains(t, help, "--"+name, "ping help must not advertise --%s", name)
		}
		args := []string{"-" + name, "wss://example.com"}
		if u.valued {
			args = []string{"-" + name, "x", "wss://example.com"}
		}
		_, err := buildPing(args)
		require.Error(t, err, "-%s must be rejected in ping mode", name)
		assert.Contains(t, err.Error(), "not supported in ping mode")
	}
}
