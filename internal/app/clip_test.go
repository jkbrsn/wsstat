package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestClipToWidth(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "fits exactly", in: "hello", width: 5, want: "hello"},
		{name: "shorter than width", in: "hi", width: 10, want: "hi"},
		{name: "clipped with marker", in: "hello world", width: 8, want: "hello..."},
		{name: "width equals marker", in: "hello", width: 3, want: "..."},
		{name: "width below marker", in: "hello", width: 2, want: ".."},
		{name: "zero width unchanged", in: "hello", width: 0, want: "hello"},
		{name: "multibyte counted by rune", in: "héllo wörld", width: 8, want: "héllo..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clipToWidth(tc.in, tc.width)
			assert.Equal(t, tc.want, got)
			if tc.width > 0 {
				assert.LessOrEqual(t, utf8.RuneCountInString(got), tc.width)
			}
		})
	}
}

// In tests stdout is not a terminal, so truncate must degrade to the same
// single-line output as compact (no clipping).
func TestClipLineNonTTYFallback(t *testing.T) {
	long := `[0001 @ ts] {"a":"` + strings.Repeat("x", 500) + `"}`
	c := &Client{format: formatTruncate}
	assert.Equal(t, long, c.clipLine(long), "non-TTY truncate should not clip")

	c.format = formatCompact
	assert.Equal(t, long, c.clipLine(long), "compact never clips")
}
