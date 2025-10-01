package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientValidate exercises the validation logic for burst and mutually-exclusive flags.
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

	t.Run("subscribe unlimited allowed", func(t *testing.T) {
		c := &Client{subscribe: true}
		require.NoError(t, c.Validate())
		assert.Equal(t, 0, c.Count())
	})

	t.Run("subscribe with explicit count ok", func(t *testing.T) {
		c := &Client{count: 3, subscribe: true}
		require.NoError(t, c.Validate())
	})

	t.Run("subscribe once coerces to one", func(t *testing.T) {
		c := &Client{subscribeOnce: true}
		require.NoError(t, c.Validate())
		assert.True(t, c.subscribe)
		assert.Equal(t, 1, c.Count())
	})

	t.Run("subscribe once forbids alternative counts", func(t *testing.T) {
		c := &Client{count: 2, subscribeOnce: true}
		assert.Error(t, c.Validate())
	})

	t.Run("invalid format", func(t *testing.T) {
		c := &Client{format: "xml"}
		assert.Error(t, c.Validate())
	})

	t.Run("json format allowed", func(t *testing.T) {
		c := &Client{format: "json"}
		require.NoError(t, c.Validate())
		assert.Equal(t, formatJSON, c.Format())
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
			name:    "subscribe once with explicit count 1",
			client:  Client{subscribeOnce: true, count: 1},
			wantErr: false,
		},
		{
			name:    "subscribe once with count 2 fails",
			client:  Client{subscribeOnce: true, count: 2},
			wantErr: true,
			errMsg:  "count must equal 1",
		},
		{
			name:    "subscribe and subscribeOnce both set",
			client:  Client{subscribe: true, subscribeOnce: true},
			wantErr: false,
		},
		{
			name:    "text and rpc method both set",
			client:  Client{textMessage: "hi", rpcMethod: "test"},
			wantErr: true,
			errMsg:  "mutually exclusive",
		},
		{
			name:    "valid json format with quiet",
			client:  Client{format: "json", quiet: true},
			wantErr: false,
		},
		{
			name:    "auto format normalizes",
			client:  Client{format: "AUTO"},
			wantErr: false,
		},
		{
			name:    "raw format is valid",
			client:  Client{format: "raw"},
			wantErr: false,
		},
		{
			name:    "color always is valid",
			client:  Client{colorMode: "always"},
			wantErr: false,
		},
		{
			name:    "color never is valid",
			client:  Client{colorMode: "never"},
			wantErr: false,
		},
		{
			name:    "empty format defaults to auto",
			client:  Client{format: ""},
			wantErr: false,
		},
		{
			name:    "empty color defaults to auto",
			client:  Client{colorMode: ""},
			wantErr: false,
		},
		{
			name:    "zero buffer is valid",
			client:  Client{buffer: 0},
			wantErr: false,
		},
		{
			name:    "positive buffer is valid",
			client:  Client{buffer: 100},
			wantErr: false,
		},
		{
			name:    "subscribe with count 0 is valid",
			client:  Client{subscribe: true, count: 0},
			wantErr: false,
		},
		{
			name:    "non-subscribe with count 0 coerces to 1",
			client:  Client{count: 0},
			wantErr: false,
		},
		{
			name:    "verbosity levels are unrestricted",
			client:  Client{verbosityLevel: 10},
			wantErr: false,
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
	t.Run("count defaults to 1 in non-subscribe mode", func(t *testing.T) {
		c := &Client{count: 0}
		require.NoError(t, c.Validate())
		assert.Equal(t, 1, c.count)
	})

	t.Run("count stays 0 in subscribe mode", func(t *testing.T) {
		c := &Client{count: 0, subscribe: true}
		require.NoError(t, c.Validate())
		assert.Equal(t, 0, c.count)
	})

	t.Run("format defaults to auto", func(t *testing.T) {
		c := &Client{format: ""}
		require.NoError(t, c.Validate())
		assert.Equal(t, formatAuto, c.format)
	})

	t.Run("color mode defaults to auto", func(t *testing.T) {
		c := &Client{colorMode: ""}
		require.NoError(t, c.Validate())
		assert.Equal(t, "auto", c.colorMode)
	})

	t.Run("subscribeOnce implies subscribe", func(t *testing.T) {
		c := &Client{subscribeOnce: true}
		require.NoError(t, c.Validate())
		assert.True(t, c.subscribe)
	})

	t.Run("subscribeOnce sets count to 1 if zero", func(t *testing.T) {
		c := &Client{subscribeOnce: true, count: 0}
		require.NoError(t, c.Validate())
		assert.Equal(t, 1, c.count)
	})
}
