package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderList(t *testing.T) {
	t.Parallel()

	t.Run("Set valid header", func(t *testing.T) {
		var h headerList
		err := h.Set("Authorization: Bearer token")
		require.NoError(t, err)
		assert.Len(t, h, 1)
		assert.Equal(t, "Authorization: Bearer token", h[0])
	})

	t.Run("Set multiple headers", func(t *testing.T) {
		var h headerList
		require.NoError(t, h.Set("Header1: value1"))
		require.NoError(t, h.Set("Header2: value2"))
		assert.Len(t, h, 2)
		assert.Equal(t, "Header1: value1", h[0])
		assert.Equal(t, "Header2: value2", h[1])
	})

	t.Run("Set trims whitespace", func(t *testing.T) {
		var h headerList
		err := h.Set("  Authorization: Bearer token  ")
		require.NoError(t, err)
		assert.Equal(t, "Authorization: Bearer token", h[0])
	})

	t.Run("Set rejects empty string", func(t *testing.T) {
		var h headerList
		err := h.Set("")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "header must not be empty")
	})

	t.Run("Set rejects whitespace-only string", func(t *testing.T) {
		var h headerList
		err := h.Set("   ")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "header must not be empty")
	})

	t.Run("Set rejects header without colon", func(t *testing.T) {
		var h headerList
		err := h.Set("InvalidHeader")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must be in 'Key: Value' format")
	})

	t.Run("Set accepts header with colon but no space", func(t *testing.T) {
		var h headerList
		err := h.Set("Key:Value")
		require.NoError(t, err)
		assert.Equal(t, "Key:Value", h[0])
	})

	t.Run("Set accepts header with empty value", func(t *testing.T) {
		var h headerList
		err := h.Set("Key:")
		require.NoError(t, err)
		assert.Equal(t, "Key:", h[0])
	})

	t.Run("String returns comma-separated list", func(t *testing.T) {
		h := headerList{"Header1: value1", "Header2: value2"}
		assert.Equal(t, "Header1: value1, Header2: value2", h.String())
	})

	t.Run("String returns empty for nil", func(t *testing.T) {
		var h headerList
		assert.Equal(t, "", h.String())
	})

	t.Run("Values returns copy", func(t *testing.T) {
		h := headerList{"Header1: value1", "Header2: value2"}
		values := h.Values()
		assert.Equal(t, []string{"Header1: value1", "Header2: value2"}, values)

		// Verify it's a copy, not a reference
		values[0] = "Modified"
		assert.Equal(t, "Header1: value1", h[0])
	})
}

func TestTrackedIntFlag(t *testing.T) {
	t.Parallel()

	t.Run("Set valid integer", func(t *testing.T) {
		f := newTrackedIntFlag(1)
		err := f.Set("42")
		require.NoError(t, err)
		assert.Equal(t, 42, f.Value())
		assert.True(t, f.WasSet())
	})

	t.Run("Set negative integer", func(t *testing.T) {
		f := newTrackedIntFlag(1)
		err := f.Set("-5")
		require.NoError(t, err)
		assert.Equal(t, -5, f.Value())
		assert.True(t, f.WasSet())
	})

	t.Run("Set zero", func(t *testing.T) {
		f := newTrackedIntFlag(1)
		err := f.Set("0")
		require.NoError(t, err)
		assert.Equal(t, 0, f.Value())
		assert.True(t, f.WasSet())
	})

	t.Run("Set invalid integer", func(t *testing.T) {
		f := newTrackedIntFlag(1)
		err := f.Set("not-a-number")
		assert.Error(t, err)
		assert.False(t, f.WasSet())
		assert.Equal(t, 1, f.Value()) // Should retain default
	})

	t.Run("Set empty string", func(t *testing.T) {
		f := newTrackedIntFlag(1)
		err := f.Set("")
		assert.Error(t, err)
		assert.False(t, f.WasSet())
	})

	t.Run("WasSet tracks set state", func(t *testing.T) {
		f := newTrackedIntFlag(5)
		assert.False(t, f.WasSet())

		require.NoError(t, f.Set("10"))
		assert.True(t, f.WasSet())
	})

	t.Run("String returns default value", func(t *testing.T) {
		f := newTrackedIntFlag(42)
		assert.Equal(t, "42", f.String())
	})

	t.Run("String returns set value", func(t *testing.T) {
		f := newTrackedIntFlag(1)
		require.NoError(t, f.Set("99"))
		assert.Equal(t, "99", f.String())
	})

	t.Run("default value not set", func(t *testing.T) {
		f := newTrackedIntFlag(10)
		assert.Equal(t, 10, f.Value())
		assert.False(t, f.WasSet())
	})
}
