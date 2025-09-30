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
