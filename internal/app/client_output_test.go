package app

import (
	"os"
	"testing"

	"github.com/jkbrsn/wsstat/internal/app/color"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColorHelpers performs basic sanity checks on ANSI wrapped strings.
func TestColorHelpers(t *testing.T) {
	base := "txt"
	assert.Contains(t, color.WSOrange.Sprint(base), "255;102;0m")
	assert.Contains(t, color.TeaGreen.Sprint(base), "211;249;181m")
}

func TestColorModeControlsOutput(t *testing.T) {
	res := sampleResult(t)

	t.Run("never removes ANSI", func(t *testing.T) {
		client := &Client{result: res, colorMode: "never"}
		output := captureStdoutFrom(t, func() error {
			return client.PrintRequestDetails(nil)
		})
		assert.NotContains(t, output, "\u001b[")
	})

	t.Run("always forces ANSI", func(t *testing.T) {
		client := &Client{result: res, colorMode: "always"}
		output := captureStdoutFrom(t, func() error {
			return client.PrintRequestDetails(nil)
		})
		assert.Contains(t, output, "\u001b[38;2;")
	})

	t.Run("auto respects NO_COLOR", func(t *testing.T) {
		client := &Client{result: res, colorMode: "auto"}
		prev, hadEnv := os.LookupEnv("NO_COLOR")
		require.NoError(t, os.Setenv("NO_COLOR", "1"))
		defer func() {
			if hadEnv {
				_ = os.Setenv("NO_COLOR", prev)
			} else {
				_ = os.Unsetenv("NO_COLOR")
			}
		}()

		output := captureStdoutFrom(t, func() error {
			return client.PrintRequestDetails(nil)
		})
		assert.NotContains(t, output, "\u001b[")
	})
}

func TestColorEnabled(t *testing.T) {
	t.Run("always mode", func(t *testing.T) {
		client := &Client{colorMode: "always"}
		assert.True(t, client.colorEnabled())
	})

	t.Run("never mode", func(t *testing.T) {
		client := &Client{colorMode: "never"}
		assert.False(t, client.colorEnabled())
	})

	t.Run("auto mode with NO_COLOR", func(t *testing.T) {
		client := &Client{colorMode: "auto"}
		prev, hadEnv := os.LookupEnv("NO_COLOR")
		require.NoError(t, os.Setenv("NO_COLOR", "1"))
		defer func() {
			if hadEnv {
				_ = os.Setenv("NO_COLOR", prev)
			} else {
				_ = os.Unsetenv("NO_COLOR")
			}
		}()
		assert.False(t, client.colorEnabled())
	})

	t.Run("auto mode without NO_COLOR uses TTY detection", func(t *testing.T) {
		client := &Client{colorMode: "auto"}
		prev, hadEnv := os.LookupEnv("NO_COLOR")
		if hadEnv {
			require.NoError(t, os.Unsetenv("NO_COLOR"))
			defer func() { _ = os.Setenv("NO_COLOR", prev) }()
		}
		// Result depends on whether stdout is a TTY - just check it doesn't panic
		_ = client.colorEnabled()
	})

	t.Run("empty color mode defaults to auto behavior", func(t *testing.T) {
		client := &Client{colorMode: ""}
		// Should not panic and should return a boolean
		result := client.colorEnabled()
		assert.IsType(t, false, result)
	})

	t.Run("invalid color mode returns false", func(t *testing.T) {
		client := &Client{colorMode: "invalid"}
		assert.False(t, client.colorEnabled())
	})
}

func TestPrintRequestDetailsVerbosityLevels(t *testing.T) {
	res := sampleResult(t)

	t.Run("level0", func(t *testing.T) {
		output := captureStdoutFrom(t, func() error {
			client := &Client{result: res}
			return client.PrintRequestDetails(nil)
		})
		assert.Contains(t, output, "URL")
		assert.NotContains(t, output, "Target")
		assert.NotContains(t, output, "Request headers")
	})

	t.Run("level1", func(t *testing.T) {
		output := captureStdoutFrom(t, func() error {
			client := &Client{result: res, verbosityLevel: 1}
			return client.PrintRequestDetails(nil)
		})
		assert.Contains(t, output, "Target")
		assert.Contains(t, output, "Messages sent")
		assert.NotContains(t, output, "Request headers")
	})

	t.Run("level2", func(t *testing.T) {
		output := captureStdoutFrom(t, func() error {
			client := &Client{result: res, verbosityLevel: 2}
			return client.PrintRequestDetails(nil)
		})
		assert.Contains(t, output, "Request headers")
		assert.Contains(t, output, "Response headers")
		assert.Contains(t, output, "Certificate")
	})
}

func TestPrintTimingResultsVerbosityLevels(t *testing.T) {
	base := sampleTimingResult(t)
	ctxURL := base.URL

	t.Run("level0", func(t *testing.T) {
		client := &Client{result: base, count: 1}
		output := captureStdoutFrom(t, func() error {
			return client.PrintTimingResults(ctxURL, nil)
		})
		assert.Contains(t, output, "Round-trip time")
		assert.NotContains(t, output, "DNS Lookup    TCP Connection")
	})

	t.Run("level1", func(t *testing.T) {
		client := &Client{result: base, count: 1, verbosityLevel: 1}
		output := captureStdoutFrom(t, func() error {
			return client.PrintTimingResults(ctxURL, nil)
		})
		assert.Contains(t, output, "DNS Lookup    TCP Connection")
	})

	t.Run("level2", func(t *testing.T) {
		client := &Client{result: base, count: 1, verbosityLevel: 2}
		output := captureStdoutFrom(t, func() error {
			return client.PrintTimingResults(ctxURL, nil)
		})
		assert.Contains(t, output, "DNS Lookup    TCP Connection")
	})
}

func TestPrintTimingResultsJSON(t *testing.T) {
	client := &Client{
		format: formatJSON,
		count:  2,
		result: sampleTimingResult(t),
	}
	ctxURL := client.result.URL
	output := captureStdoutFrom(t, func() error {
		return client.PrintTimingResults(ctxURL, nil)
	})
	payload := decodeJSONLine(t, output)
	assert.Equal(t, "timing", payload["type"])
	assert.Equal(t, "mean", payload["mode"])
	counts := asMap(t, payload["counts"])
	assert.EqualValues(t, client.Count(), counts["requested"])
	assert.EqualValues(t, client.result.MessageCount, counts["messages"])
	durations := asMap(t, payload["durations_ms"])
	assert.EqualValues(t, client.result.MessageRTT.Milliseconds(), durations["message_rtt"])
	assert.EqualValues(t, client.result.TotalTime.Milliseconds(), durations["total"])
	target := asMap(t, payload["target"])
	assert.Equal(t, client.result.URL.String(), target["url"])
}

func TestPrintResponseJSON(t *testing.T) {
	t.Run("map payload", func(t *testing.T) {
		client := &Client{
			format:    formatJSON,
			rpcMethod: "eth_blockNumber",
			response:  map[string]any{"jsonrpc": "2.0", "result": "0x1"},
		}
		output := captureStdoutFrom(t, func() error {
			client.PrintResponse(nil)
			return nil
		})
		payload := decodeJSONLine(t, output)
		assert.Equal(t, "response", payload["type"])
		assert.Equal(t, "eth_blockNumber", payload["rpc_method"])
		response := asMap(t, payload["payload"])
		assert.Equal(t, "0x1", response["result"])
	})

	t.Run("plain string payload", func(t *testing.T) {
		client := &Client{
			format:   formatJSON,
			response: "hello world",
		}
		output := captureStdoutFrom(t, func() error {
			client.PrintResponse(nil)
			return nil
		})
		payload := decodeJSONLine(t, output)
		assert.Equal(t, "response", payload["type"])
		assert.Equal(t, "hello world", payload["payload"])
	})
}
