package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{in: "", want: formatAuto},
		{in: "auto", want: formatAuto},
		{in: "compact", want: formatCompact},
		{in: "json", want: formatJSON},
		{in: "raw", want: formatRaw},
		{in: "  JSON  ", want: formatJSON},
		{in: "Compact", want: formatCompact},
		{in: "garbage", wantErr: true},
		{in: "pretty", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseFormat(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
