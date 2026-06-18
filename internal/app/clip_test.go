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

// clipLines must clip every line of a multi-line (auto) body, not just the first.
func TestClipLinesMultiLine(t *testing.T) {
	body := "{\n  \"aaaaaaaaaaaaaaaaaaaa\": \"bbbbbbbbbbbbbbbbbbbb\"\n}"
	got := clipLines(body, 10)
	for line := range strings.SplitSeq(got, "\n") {
		assert.LessOrEqual(t, utf8.RuneCountInString(line), 10)
	}
	parts := strings.Split(got, "\n")
	// The opening/closing braces are short enough to survive unchanged.
	assert.Equal(t, "{", parts[0])
	assert.Equal(t, "}", parts[2])
	// The long middle line is clipped with the trailing marker.
	assert.True(t, strings.HasSuffix(parts[1], truncMarker), "middle line should be clipped")
}

// In tests stdout is not a terminal, so clipBody must leave output unchanged
// even when --clip is enabled (width cannot be determined).
func TestClipBodyNonTTYNoOp(t *testing.T) {
	long := `[0001 @ ts] {"a":"` + strings.Repeat("x", 500) + `"}`

	c := &Client{clip: true}
	assert.Equal(t, long, c.clipBody(long), "non-TTY clip should not clip")

	c.clip = false
	assert.Equal(t, long, c.clipBody(long), "clip disabled never clips")
}
