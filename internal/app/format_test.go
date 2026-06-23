package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMsPrecision pins the sub-ms timing contract: durations render as float ms rounded to
// 3 decimals, so a sub-millisecond phase is non-zero in both text and JSON instead of
// truncating to 0ms. Whole-ms values stay integer-shaped (no trailing ".0").
func TestMsPrecision(t *testing.T) {
	cases := []struct {
		d      time.Duration
		ms     float64
		str    string
		render string
	}{
		{500 * time.Microsecond, 0.5, "0.5", "0.5ms"},
		{1234567 * time.Nanosecond, 1.235, "1.235", "1.235ms"}, // rounds at µs resolution
		{50 * time.Millisecond, 50, "50", "50ms"},
		{100 * time.Nanosecond, 0, "0", "0ms"}, // positive but below µs resolution rounds to 0
	}
	// formatDuration reserves "-" for non-positive durations.
	assert.Equal(t, "-", formatDuration(0))
	for _, c := range cases {
		assert.InDelta(t, c.ms, msFloat(c.d), 1e-9, "msFloat(%s)", c.d)
		assert.Equal(t, c.str, msString(c.d), "msString(%s)", c.d)
		assert.Equal(t, c.render, formatDuration(c.d), "formatDuration(%s)", c.d)
	}

	// msPtr keeps nil-for-zero semantics but a real sub-ms phase is non-nil and non-zero.
	assert.Nil(t, msPtr(0))
	if p := msPtr(500 * time.Microsecond); assert.NotNil(t, p) {
		assert.InDelta(t, 0.5, *p, 1e-9)
	}
}

func TestParseOutput(t *testing.T) {
	cases := []struct {
		in      string
		want    Output
		wantErr bool
	}{
		{in: "", want: OutputText},
		{in: "text", want: OutputText},
		{in: "json", want: OutputJSON},
		{in: "raw", want: OutputRaw},
		{in: "  JSON  ", want: OutputJSON},
		{in: "Text", want: OutputText},
		{in: "compact", wantErr: true},
		{in: "auto", wantErr: true},
		{in: "garbage", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseOutput(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseBody(t *testing.T) {
	cases := []struct {
		in      string
		want    Body
		wantErr bool
	}{
		{in: "", want: BodyAuto},
		{in: "auto", want: BodyAuto},
		{in: "compact", want: BodyCompact},
		{in: "  Compact  ", want: BodyCompact},
		{in: "truncate", wantErr: true},
		{in: "json", wantErr: true},
		{in: "garbage", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseBody(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
