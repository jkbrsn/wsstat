package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeprecatedAliasIntSetsTarget(t *testing.T) {
	t.Parallel()

	aliasUsed := false
	flagValue := newTrackedIntFlag(1)
	alias := deprecatedAliasInt{target: &flagValue, used: &aliasUsed}

	require.NoError(t, alias.Set("5"))
	assert.True(t, aliasUsed)
	assert.True(t, flagValue.WasSet())
	assert.Equal(t, 5, flagValue.Value())
}

func TestResolveCountValue(t *testing.T) {
	t.Parallel()

	origCount := countFlag
	origBurstUsed := burstFlagUsed
	defer func() {
		countFlag = origCount
		burstFlagUsed = origBurstUsed
	}()

	countFlag = newTrackedIntFlag(1)
	assert.Equal(t, 0, resolveCountValue(true, false))
	assert.Equal(t, 1, resolveCountValue(false, false))
	assert.Equal(t, 1, resolveCountValue(true, true))

	require.NoError(t, (&countFlag).Set("3"))
	assert.Equal(t, 3, resolveCountValue(true, false))
}
