package app

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleCheckReport builds a report exercising every status for output tests.
func sampleCheckReport(t *testing.T) *CheckReport {
	t.Helper()
	target, err := url.Parse("wss://echo.example.com/ws")
	require.NoError(t, err)
	return &CheckReport{
		Target: target,
		Entries: []CheckEntry{
			{ID: checkUpgrade, Group: "handshake", Status: CheckPass,
				Detail: "101 Switching Protocols", Took: 12 * time.Millisecond},
			{ID: checkAccept, Group: "handshake", Status: CheckPass,
				Detail: "validated during handshake"},
			{ID: checkHeaders, Group: "handshake", Status: CheckFail,
				Detail: `Connection header missing "Upgrade" token`},
			{ID: checkSubprotoNone, Group: "negotiation", Status: CheckPass,
				Detail: "none offered, none selected"},
			{ID: checkDeflate, Group: "negotiation", Status: CheckWarn,
				Detail: "server ignores client_max_window_bits"},
			{ID: checkVersionReject, Group: "negotiation", Status: CheckSkip},
			{ID: checkPingPong, Group: "behavior", Status: CheckPass,
				Detail: "ping -> pong", Took: 8 * time.Millisecond},
		},
	}
}

func TestPrintCheckResultsText(t *testing.T) {
	report := sampleCheckReport(t)

	t.Run("grouped layout with ASCII markers", func(t *testing.T) {
		client := &Client{output: OutputText, colorMode: "never"}
		out := captureStdoutFrom(t, func() error {
			return client.PrintCheckResults(report)
		})
		// Group headers.
		assert.Contains(t, out, "Handshake")
		assert.Contains(t, out, "Negotiation")
		assert.Contains(t, out, "Behavior")
		// ASCII markers for every status.
		assert.Contains(t, out, "ok")
		assert.Contains(t, out, "FAIL")
		assert.Contains(t, out, "warn")
		assert.Contains(t, out, "skip")
		// Static labels, not the dynamic detail at default verbosity.
		assert.Contains(t, out, "101 upgrade")
		assert.NotContains(t, out, "Switching Protocols")
		// Summary tally.
		assert.Contains(t, out, "4 passed, 1 warning, 1 failed, 1 skipped")
	})

	t.Run("verbose adds detail and took", func(t *testing.T) {
		client := &Client{output: OutputText, colorMode: "never", verbosityLevel: 1}
		out := captureStdoutFrom(t, func() error {
			return client.PrintCheckResults(report)
		})
		assert.Contains(t, out, "101 Switching Protocols")
		assert.Contains(t, out, "12ms")
	})

	t.Run("quiet prints only the summary", func(t *testing.T) {
		client := &Client{output: OutputText, colorMode: "never", quiet: true}
		out := captureStdoutFrom(t, func() error {
			return client.PrintCheckResults(report)
		})
		assert.NotContains(t, out, "Handshake")
		assert.NotContains(t, out, "101 upgrade")
		assert.Contains(t, out, "4 passed, 1 warning, 1 failed, 1 skipped")
	})

	t.Run("color always emits glyphs", func(t *testing.T) {
		client := &Client{output: OutputText, colorMode: "always"}
		out := captureStdoutFrom(t, func() error {
			return client.PrintCheckResults(report)
		})
		assert.Contains(t, out, "✓")     // pass glyph
		assert.Contains(t, out, "✗")     // fail glyph
		assert.Contains(t, out, "\x1b[") // ANSI color codes
	})
}

func TestCheckSummaryLinePluralization(t *testing.T) {
	target, err := url.Parse("wss://echo.example.com")
	require.NoError(t, err)
	report := &CheckReport{
		Target: target,
		Entries: []CheckEntry{
			{ID: checkUpgrade, Group: "handshake", Status: CheckPass},
			{ID: checkAccept, Group: "handshake", Status: CheckWarn},
		},
	}
	// One warning is singular; no skipped means the skipped clause is omitted.
	assert.Equal(t, "1 passed, 1 warning, 0 failed", checkSummaryLine(report))
}

func TestPrintCheckResultsJSON(t *testing.T) {
	report := sampleCheckReport(t)
	client := &Client{output: OutputJSON}
	out := captureStdoutFrom(t, func() error {
		return client.PrintCheckResults(report)
	})
	payload := decodeJSONLine(t, out)

	assert.Equal(t, "check_report", payload["type"])
	assert.Equal(t, JSONSchemaVersion, payload["schema_version"])
	assert.Equal(t, "wss://echo.example.com/ws", payload["url"])
	assert.EqualValues(t, 4, payload["passed"])
	assert.EqualValues(t, 1, payload["warned"])
	assert.EqualValues(t, 1, payload["failed"])
	assert.EqualValues(t, 1, payload["skipped"])

	checks, ok := payload["checks"].([]any)
	require.True(t, ok, "checks must be an array: %v", payload)
	require.Len(t, checks, len(report.Entries))

	first := asMap(t, checks[0])
	assert.Equal(t, checkUpgrade, first["id"])
	assert.Equal(t, "handshake", first["group"])
	assert.Equal(t, "pass", first["status"])
	assert.Equal(t, "101 Switching Protocols", first["detail"])
	assert.InDelta(t, 12.0, first["took_ms"], 0.001)

	// A skipped entry carries no detail; took_ms is always present (even at 0).
	skipped := asMap(t, checks[5])
	assert.Equal(t, "skip", skipped["status"])
	_, hasDetail := skipped["detail"]
	assert.False(t, hasDetail, "empty detail is omitted")
	assert.EqualValues(t, 0, skipped["took_ms"])
}
