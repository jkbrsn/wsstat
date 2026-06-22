package wsstat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureText(t *testing.T) {
	result, responses, err := MeasureText(
		context.Background(), echoServerAddrWs, []string{"hello", "world"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"hello", "world"}, responses)
	assert.Positive(t, result.TotalTime)
	assert.Equal(t, 2, result.MessageCount)
}

func TestMeasureJSON(t *testing.T) {
	payload := map[string]any{"foo": "bar"}
	result, responses, err := MeasureJSON(
		context.Background(), echoServerAddrWs, []any{payload})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, responses, 1)
	assert.Equal(t, payload, responses[0])
}

func TestMeasurePing(t *testing.T) {
	result, err := MeasurePing(context.Background(), echoServerAddrWs, 3)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Positive(t, result.TotalTime)
}

func TestMeasureTextCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := MeasureText(ctx, echoServerAddrWs, []string{"hello"})
	require.Error(t, err)
}
