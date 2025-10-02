package main

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWSURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		noTLS    bool
		expected string
		wantErr  bool
	}{
		{
			name:     "full wss URL",
			input:    "wss://example.com/path",
			expected: "wss://example.com/path",
		},
		{
			name:     "full ws URL",
			input:    "ws://example.com/path",
			expected: "ws://example.com/path",
		},
		{
			name:     "no scheme defaults to wss",
			input:    "example.com/path",
			noTLS:    false,
			expected: "wss://example.com/path",
		},
		{
			name:     "no scheme with noTLS defaults to ws",
			input:    "example.com/path",
			noTLS:    true,
			expected: "ws://example.com/path",
		},
		{
			name:     "localhost without scheme",
			input:    "localhost:8080",
			expected: "wss://localhost:8080",
		},
		{
			name:    "invalid URL",
			input:   "ht!tp://invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldNoTLS := *noTLS
			defer func() { *noTLS = oldNoTLS }()
			*noTLS = tt.noTLS

			result, err := parseWSURI(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result.String())
		})
	}
}

func TestResolveCountValue(t *testing.T) {
	t.Parallel()

	origCount := countFlag
	defer func() {
		countFlag = origCount
	}()

	tests := []struct {
		name          string
		subscribe     bool
		subscribeOnce bool
		countSet      bool
		countValue    int
		expected      int
	}{
		{
			name:       "no flags, default count",
			countValue: 1,
			expected:   1,
		},
		{
			name:       "subscribe without count set",
			subscribe:  true,
			countValue: 1,
			expected:   0,
		},
		{
			name:       "subscribe with count set",
			subscribe:  true,
			countSet:   true,
			countValue: 5,
			expected:   5,
		},
		{
			name:          "subscribe-once",
			subscribeOnce: true,
			countValue:    1,
			expected:      1,
		},
		{
			name:          "subscribe-once with count set",
			subscribeOnce: true,
			countSet:      true,
			countValue:    3,
			expected:      3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			countFlag = newTrackedIntFlag(tt.countValue)
			if tt.countSet {
				require.NoError(t, (&countFlag).Set(string(rune(tt.countValue+'0'))))
			}

			result := resolveCountValue(tt.subscribe, tt.subscribeOnce)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// revive:disable:function-length test setup requires saving/restoring many flags
func TestParseConfig(t *testing.T) {
	// These tests need to manipulate global flag state, so we can't run them in parallel
	// Save and restore original values
	origArgs := os.Args
	origNoTLS := *noTLS
	origColorArg := *colorArg
	origQuiet := *quiet
	origShowVersion := *showVersion
	origTextMessage := *textMessage
	origRPCMethod := *rpcMethod
	origFormatOption := *formatOption
	origSubscribe := *subscribe
	origSubscribeOnce := *subscribeOnce
	origBufferSize := *bufferSize
	origVerbosityLevel := verbosityLevel.count
	origCountFlag := countFlag
	origHeaderArguments := headerArguments

	defer func() {
		os.Args = origArgs
		*noTLS = origNoTLS
		*colorArg = origColorArg
		*quiet = origQuiet
		*showVersion = origShowVersion
		*textMessage = origTextMessage
		*rpcMethod = origRPCMethod
		*formatOption = origFormatOption
		*subscribe = origSubscribe
		*subscribeOnce = origSubscribeOnce
		*bufferSize = origBufferSize
		verbosityLevel.count = origVerbosityLevel
		countFlag = origCountFlag
		headerArguments = origHeaderArguments
	}()

	resetFlags := func() {
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

		// Reset all flag values
		noTLS = flag.Bool("no-tls", false, "")
		colorArg = flag.String("color", "auto", "")
		quiet = flag.Bool("q", false, "")
		showVersion = flag.Bool("version", false, "")
		textMessage = flag.String("text", "", "")
		rpcMethod = flag.String("rpc-method", "", "")
		formatOption = flag.String("format", "auto", "")
		subscribe = flag.Bool("subscribe", false, "")
		subscribeOnce = flag.Bool("subscribe-once", false, "")
		bufferSize = flag.Int("buffer", 0, "")
		summaryInterval = flag.Duration("summary-interval", 0, "")

		verbosityLevel = newVerbosityCounter()
		countFlag = newTrackedIntFlag(1)
		headerArguments = headerList{}

		// Re-register custom flags
		flag.Var(&countFlag, "count", "")
		flag.Var(&headerArguments, "H", "")
		flag.Var(&headerArguments, "header", "")
		flag.Var(verbosityLevel, "v", "")
	}

	tests := []struct {
		name      string
		args      []string
		wantErr   bool
		errIs     error
		checkFunc func(*testing.T, *Config)
	}{
		{
			name:    "version flag",
			args:    []string{"cmd", "-version"},
			wantErr: true,
			errIs:   errVersionRequested,
		},
		{
			name:    "quiet and verbose conflict",
			args:    []string{"cmd", "-q", "-v", "example.com"},
			wantErr: true,
		},
		{
			name:    "text and rpc-method conflict",
			args:    []string{"cmd", "-text", "hello", "-rpc-method", "test", "example.com"},
			wantErr: true,
		},
		{
			name:    "no arguments",
			args:    []string{"cmd"},
			wantErr: true,
		},
		{
			name:    "too many arguments",
			args:    []string{"cmd", "example.com", "extra"},
			wantErr: true,
		},
		{
			name:    "invalid color option",
			args:    []string{"cmd", "-color", "invalid", "example.com"},
			wantErr: true,
		},
		{
			name: "valid basic config",
			args: []string{"cmd", "example.com"},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "wss://example.com", cfg.TargetURL.String())
				assert.Equal(t, 1, cfg.Count)
				assert.Equal(t, "auto", cfg.Format)
				assert.Equal(t, "auto", cfg.ColorMode)
				assert.False(t, cfg.Quiet)
				assert.Equal(t, 0, cfg.Verbosity)
			},
		},
		{
			name: "with headers",
			args: []string{
				"cmd", "-H", "Auth: Bearer token",
				"-H", "Origin: https://foo.com", "example.com",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Len(t, cfg.Headers, 2)
				assert.Contains(t, cfg.Headers, "Auth: Bearer token")
				assert.Contains(t, cfg.Headers, "Origin: https://foo.com")
			},
		},
		{
			name: "subscribe mode without count",
			args: []string{"cmd", "-subscribe", "example.com"},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Subscribe)
				assert.Equal(t, 0, cfg.Count) // unlimited
			},
		},
		{
			name: "subscribe mode with count",
			args: []string{"cmd", "-subscribe", "-count", "5", "example.com"},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Subscribe)
				assert.Equal(t, 5, cfg.Count)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			os.Args = tt.args

			cfg, err := parseConfig()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errIs != nil {
					assert.ErrorIs(t, err, tt.errIs)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cfg)

			if tt.checkFunc != nil {
				tt.checkFunc(t, cfg)
			}
		})
	}
}
