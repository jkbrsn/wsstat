package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCountValue(t *testing.T) {
	t.Parallel()

	origCount := countFlag
	defer func() {
		countFlag = origCount
	}()

	countFlag = newTrackedIntFlag(1)
	assert.Equal(t, 0, resolveCountValue(true, false))
	assert.Equal(t, 1, resolveCountValue(false, false))
	assert.Equal(t, 1, resolveCountValue(true, true))

	require.NoError(t, (&countFlag).Set("3"))
	assert.Equal(t, 3, resolveCountValue(true, false))
}
