package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                            "-",
		125729 * time.Microsecond:    "125.729ms",
		58709613 * time.Microsecond:  "58.71s",
		209250491 * time.Microsecond: "3m29s",
		2*time.Hour + 5*time.Minute + 7*time.Second: "2h05m07s",
	}
	for d, want := range cases {
		assert.Equal(t, want, formatElapsed(d), d.String())
	}
}
