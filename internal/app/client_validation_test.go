package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientValidate exercises the validation logic for counts and mutually-exclusive flags.
func TestClientValidate(t *testing.T) {
	t.Run("negative count", func(t *testing.T) {
		c := &Client{count: -1}
		assert.Error(t, c.Validate())
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		c := &Client{count: 1, textMessage: "hi", rpcMethod: "foo"}
		assert.Error(t, c.Validate())
	})

	t.Run("defaults to one when unset", func(t *testing.T) {
		c := &Client{}
		require.NoError(t, c.Validate())
		assert.Equal(t, 1, c.Count())
	})

	t.Run("valid", func(t *testing.T) {
		c := &Client{count: 2, textMessage: "hi"}
		require.NoError(t, c.Validate())
	})

	t.Run("stream unlimited allowed", func(t *testing.T) {
		c := &Client{mode: ModeStream}
		require.NoError(t, c.Validate())
		assert.Equal(t, 0, c.Count())
	})

	t.Run("stream with explicit count ok", func(t *testing.T) {
		c := &Client{count: 3, mode: ModeStream}
		require.NoError(t, c.Validate())
	})

	t.Run("stream once with count 0 ok", func(t *testing.T) {
		c := &Client{mode: ModeStream, once: true}
		require.NoError(t, c.Validate())
	})

	t.Run("stream once with count 1 ok", func(t *testing.T) {
		c := &Client{mode: ModeStream, once: true, count: 1}
		require.NoError(t, c.Validate())
	})

	t.Run("stream once forbids count > 1", func(t *testing.T) {
		c := &Client{mode: ModeStream, once: true, count: 2}
		assert.Error(t, c.Validate())
	})

	t.Run("invalid output", func(t *testing.T) {
		c := &Client{output: "xml"}
		assert.Error(t, c.Validate())
	})

	t.Run("json output allowed", func(t *testing.T) {
		c := &Client{output: "json"}
		require.NoError(t, c.Validate())
		assert.Equal(t, OutputJSON, c.Output())
	})

	t.Run("invalid body", func(t *testing.T) {
		c := &Client{body: "truncate"}
		assert.Error(t, c.Validate())
	})

	t.Run("compact body allowed", func(t *testing.T) {
		c := &Client{body: "compact"}
		require.NoError(t, c.Validate())
		assert.Equal(t, BodyCompact, c.Body())
	})

	t.Run("negative buffer", func(t *testing.T) {
		c := &Client{buffer: -1}
		assert.Error(t, c.Validate())
	})

	t.Run("negative summary interval", func(t *testing.T) {
		c := &Client{summaryInterval: -1}
		assert.Error(t, c.Validate())
	})

	t.Run("invalid color", func(t *testing.T) {
		c := &Client{colorMode: "purple"}
		assert.Error(t, c.Validate())
	})
}

//revive:disable:function-length comprehensive table-driven test
func TestValidateComplexCombinations(t *testing.T) {
	tests := []struct {
		name    string
		client  Client
		wantErr bool
		errMsg  string
	}{
		{
			name:   "stream once with count 1",
			client: Client{mode: ModeStream, once: true, count: 1},
		},
		{
			name:    "stream once with count 2 fails",
			client:  Client{mode: ModeStream, once: true, count: 2},
			wantErr: true,
			errMsg:  "--once",
		},
		{
			name:    "text and rpc method both set",
			client:  Client{textMessage: "hi", rpcMethod: "test"},
			wantErr: true,
			errMsg:  "mutually exclusive",
		},
		{
			name:   "valid json output with quiet",
			client: Client{output: "json", quiet: true},
		},
		{
			name:   "text output normalizes",
			client: Client{output: "TEXT"},
		},
		{
			name:   "raw output is valid",
			client: Client{output: "raw"},
		},
		{
			name:   "compact body is valid",
			client: Client{body: "compact"},
		},
		{
			name:   "color always is valid",
			client: Client{colorMode: "always"},
		},
		{
			name:   "color never is valid",
			client: Client{colorMode: "never"},
		},
		{
			name:   "empty output defaults to text",
			client: Client{output: ""},
		},
		{
			name:   "empty body defaults to auto",
			client: Client{body: ""},
		},
		{
			name:   "empty color defaults to auto",
			client: Client{colorMode: ""},
		},
		{
			name:   "zero buffer is valid",
			client: Client{buffer: 0},
		},
		{
			name:   "positive buffer is valid",
			client: Client{buffer: 100},
		},
		{
			name:   "stream with count 0 is valid",
			client: Client{mode: ModeStream, count: 0},
		},
		{
			name:   "measure with count 0 coerces to 1",
			client: Client{count: 0},
		},
		{
			name:   "verbosity levels are unrestricted",
			client: Client{verbosityLevel: 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.client.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

//revive:enable:function-length

func TestValidateDefaults(t *testing.T) {
	t.Run("count defaults to 1 in measure mode", func(t *testing.T) {
		c := &Client{count: 0}
		require.NoError(t, c.Validate())
		assert.Equal(t, 1, c.count)
	})

	t.Run("count stays 0 in stream mode", func(t *testing.T) {
		c := &Client{count: 0, mode: ModeStream}
		require.NoError(t, c.Validate())
		assert.Equal(t, 0, c.count)
	})

	t.Run("output defaults to text", func(t *testing.T) {
		c := &Client{output: ""}
		require.NoError(t, c.Validate())
		assert.Equal(t, OutputText, c.output)
	})

	t.Run("body defaults to auto", func(t *testing.T) {
		c := &Client{body: ""}
		require.NoError(t, c.Validate())
		assert.Equal(t, BodyAuto, c.body)
	})

	t.Run("color mode defaults to auto", func(t *testing.T) {
		c := &Client{colorMode: ""}
		require.NoError(t, c.Validate())
		assert.Equal(t, "auto", c.colorMode)
	})
}
