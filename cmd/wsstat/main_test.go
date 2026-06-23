package main

import (
	"errors"
	"flag"
	"testing"

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
		{name: "format short", args: []string{"-f", "raw", "example.com"}, wantErr: true, hint: "-o"},
		{name: "no-tls", args: []string{"-no-tls", "example.com"}, wantErr: true, hint: "ws://"},
		// A removed-flag name passed as a flag *value* must not be misread as the flag.
		{name: "removed name as text value ok", args: []string{"-t", "-s", "example.com"}, wantErr: false},
		{name: "format name as text value ok", args: []string{"--text", "-format", "example.com"}, wantErr: false},
		{name: "current flags ok", args: []string{"-o", "json", "example.com"}, wantErr: false},
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
