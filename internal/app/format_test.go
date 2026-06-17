package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
