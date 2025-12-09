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
func TestResolveList(t *testing.T) {
	t.Parallel()

	t.Run("parse valid IPv4", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:192.168.1.1")
		require.NoError(t, err)
		values := r.Values()
		assert.Equal(t, "192.168.1.1", values["example.com:443"])
	})

	t.Run("parse valid IPv6", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:2001:db8::1")
		require.NoError(t, err)
		values := r.Values()
		assert.Equal(t, "2001:db8::1", values["example.com:443"])
	})

	t.Run("parse IPv6 loopback", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:::1")
		require.NoError(t, err)
		values := r.Values()
		assert.Equal(t, "::1", values["example.com:443"])
	})

	t.Run("parse IPv6 with brackets", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:[2001:db8::1]")
		require.NoError(t, err)
		values := r.Values()
		assert.Equal(t, "2001:db8::1", values["example.com:443"])
	})

	t.Run("parse IPv6 loopback with brackets", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:[::1]")
		require.NoError(t, err)
		values := r.Values()
		assert.Equal(t, "::1", values["example.com:443"])
	})

	t.Run("multiple entries", func(t *testing.T) {
		var r resolveList
		require.NoError(t, r.Set("example.com:443:192.168.1.1"))
		require.NoError(t, r.Set("api.example.com:80:192.168.1.2"))
		require.NoError(t, r.Set("example.com:8080:127.0.0.1"))

		values := r.Values()
		assert.Len(t, values, 3)
		assert.Equal(t, "192.168.1.1", values["example.com:443"])
		assert.Equal(t, "192.168.1.2", values["api.example.com:80"])
		assert.Equal(t, "127.0.0.1", values["example.com:8080"])
	})

	t.Run("case insensitive hostname", func(t *testing.T) {
		var r resolveList
		err := r.Set("Example.COM:443:192.168.1.1")
		require.NoError(t, err)
		values := r.Values()
		// Should be normalized to lowercase
		assert.Equal(t, "192.168.1.1", values["example.com:443"])
	})

	t.Run("override same host:port", func(t *testing.T) {
		var r resolveList
		require.NoError(t, r.Set("example.com:443:192.168.1.1"))
		require.NoError(t, r.Set("example.com:443:192.168.1.2"))

		values := r.Values()
		// Last one wins
		assert.Equal(t, "192.168.1.2", values["example.com:443"])
	})

	t.Run("reject missing parts", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "resolve must be in 'host:port:address' format")
	})

	t.Run("reject only two parts", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "address must not be empty")
	})

	t.Run("reject empty host", func(t *testing.T) {
		var r resolveList
		err := r.Set(":443:192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "host must not be empty")
	})

	t.Run("reject empty port", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com::192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "port must not be empty")
	})

	t.Run("reject invalid port", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:abc:192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid port")
	})

	t.Run("reject port zero", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:0:192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "port must be between 1 and")
	})

	t.Run("reject port too high", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:99999:192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "port must be between 1 and")
	})

	t.Run("accept port 1", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:1:192.168.1.1")
		require.NoError(t, err)
	})

	t.Run("accept port 65535", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:65535:192.168.1.1")
		require.NoError(t, err)
	})

	t.Run("reject invalid IP address", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:not-an-ip")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid IP address")
	})

	t.Run("reject malformed IPv4", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:256.1.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid IP address")
	})

	t.Run("reject malformed IPv6", func(t *testing.T) {
		var r resolveList
		err := r.Set("example.com:443:gggg::1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid IP address")
	})

	t.Run("String returns empty for nil", func(t *testing.T) {
		var r resolveList
		assert.Equal(t, "", r.String())
	})

	t.Run("String returns formatted entries", func(t *testing.T) {
		var r resolveList
		require.NoError(t, r.Set("example.com:443:192.168.1.1"))
		// String format may vary due to map iteration order
		str := r.String()
		assert.Contains(t, str, "example.com:443")
		assert.Contains(t, str, "192.168.1.1")
	})

	t.Run("Values returns nil for uninitialized", func(t *testing.T) {
		var r resolveList
		values := r.Values()
		assert.Nil(t, values)
	})

	t.Run("Values returns copy", func(t *testing.T) {
		var r resolveList
		require.NoError(t, r.Set("example.com:443:192.168.1.1"))

		values := r.Values()
		values["example.com:443"] = "modified"

		// Original should not be modified
		assert.Equal(t, "192.168.1.1", r.Values()["example.com:443"])
	})

	t.Run("trim whitespace in parts", func(t *testing.T) {
		var r resolveList
		err := r.Set(" example.com : 443 : 192.168.1.1 ")
		require.NoError(t, err)
		values := r.Values()
		assert.Equal(t, "192.168.1.1", values["example.com:443"])
	})
}
